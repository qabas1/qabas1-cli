package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/qabas1/qabas1-cli/internal/nodeexec"
	"gopkg.in/yaml.v3"
)

// OffsiteRoot holds copies pushed to a replica, one directory per source writer.
const OffsiteRoot = "/var/lib/qabasone/backups-offsite"

var backupIDRe = regexp.MustCompile(`^qb-[0-9]{8}-[0-9]{6}$`)

// Offsite pushes managed backups to another node over an existing executor (pinned SSH).
type Offsite struct {
	X      nodeexec.Executor
	Source string // writer node name: copies land in OffsiteRoot/<Source>/<id>
}

func (o Offsite) dir() string { return OffsiteRoot + "/" + o.Source }

// Push streams the backup directory as a tar into <id>.partial on the node, checks
// every file against a SHA-256 manifest built while streaming, then renames it into place.
func (o Offsite) Push(ctx context.Context, localDir string, m Meta) error {
	if !backupIDRe.MatchString(m.ID) {
		return fmt.Errorf("refusing to push backup with unexpected id %q", m.ID)
	}
	base := nodeexec.Quote(o.dir())
	partial := nodeexec.Quote(o.dir() + "/" + m.ID + ".partial")
	final := nodeexec.Quote(o.dir() + "/" + m.ID)

	prep := "set -eu\numask 077\nmkdir -p " + base + "\nchmod 0700 " + nodeexec.Quote(OffsiteRoot) + " " + base + "\n" +
		"find " + base + " -maxdepth 1 -name '*.partial' -mmin +1440 -exec rm -rf {} + 2>/dev/null || true\n" +
		"rm -rf " + partial + "\nmkdir " + partial + "\n" +
		"tar -xf - -C " + partial + " --no-same-owner\n"

	pr, pw := io.Pipe()
	type tarResult struct {
		sums string
		err  error
	}
	manifest := make(chan tarResult, 1)
	go func() {
		sums, err := writeTar(pw, localDir)
		manifest <- tarResult{sums, err}
		_ = pw.CloseWithError(err)
	}()
	if err := runScript(ctx, o.X, prep, pr); err != nil {
		_ = pr.CloseWithError(err)
		<-manifest
		return fmt.Errorf("copy %s to %s: %w", m.ID, o.X.Node(), err)
	}
	tr := <-manifest
	if tr.err != nil {
		_, _ = o.X.Run(context.Background(), "rm -rf "+partial, nil)
		return fmt.Errorf("read %s: %w", m.ID, tr.err)
	}
	sums := tr.sums

	check := "set -eu\ncd " + partial + "\nLC_ALL=C sha256sum -c --quiet --strict -\n"
	if err := runScript(ctx, o.X, check, strings.NewReader(sums)); err != nil {
		_, _ = o.X.Run(context.Background(), "rm -rf "+partial, nil)
		return fmt.Errorf("verify %s on %s: %w", m.ID, o.X.Node(), err)
	}
	return runScript(ctx, o.X, "set -eu\nrm -rf "+final+"\nmv "+partial+" "+final+"\n", nil)
}

// List reads the meta.yaml of every complete copy on the node.
func (o Offsite) List(ctx context.Context) ([]Meta, error) {
	script := "for f in " + nodeexec.Quote(o.dir()) + "/qb-*/meta.yaml; do [ -f \"$f\" ] || continue; echo '---'; cat \"$f\"; done\n"
	res, err := o.X.Run(ctx, script, nil)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, &nodeexec.ScriptError{Node: o.X.Node(), ExitCode: res.ExitCode, Stderr: res.Stderr}
	}
	var out []Meta
	dec := yaml.NewDecoder(strings.NewReader(res.Stdout))
	for {
		var m Meta
		err := dec.Decode(&m)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse off-site meta on %s: %w", o.X.Node(), err)
		}
		if backupIDRe.MatchString(m.ID) {
			out = append(out, m)
		}
	}
	return out, nil
}

// Prune applies GFS retention to the scheduled copies on the node and returns removed ids.
func (o Offsite) Prune(ctx context.Context, daily, weekly, monthly int, loc *time.Location) ([]string, error) {
	metas, err := o.List(ctx)
	if err != nil {
		return nil, err
	}
	_, drop := SelectGFS(OnlyScheduled(metas), daily, weekly, monthly, loc)
	var removed []string
	for _, m := range drop {
		if err := runScript(ctx, o.X, "rm -rf "+nodeexec.Quote(o.dir()+"/"+m.ID), nil); err != nil {
			return removed, err
		}
		removed = append(removed, m.ID)
	}
	return removed, nil
}

// writeTar archives dir (relative paths) into w and returns a sha256sum manifest.
func writeTar(w io.Writer, dir string) (string, error) {
	tw := tar.NewWriter(w)
	var sums bytes.Buffer
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !info.IsDir() && !info.Mode().IsRegular() {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = rel
		hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""
		if info.IsDir() {
			hdr.Name += "/"
			hdr.Mode = 0o700
			return tw.WriteHeader(hdr)
		}
		hdr.Mode = 0o600
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(tw, io.TeeReader(f, h)); err != nil {
			return err
		}
		fmt.Fprintf(&sums, "%x  ./%s\n", h.Sum(nil), path.Clean(rel))
		return nil
	})
	if err != nil {
		return "", err
	}
	return sums.String(), tw.Close()
}

func runScript(ctx context.Context, x nodeexec.Executor, script string, stdin io.Reader) error {
	res, err := x.Run(ctx, script, stdin)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return &nodeexec.ScriptError{Node: x.Node(), ExitCode: res.ExitCode, Stderr: res.Stderr}
	}
	return nil
}
