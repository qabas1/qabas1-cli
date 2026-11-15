package backup

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qabas1/qabas1-cli/internal/nodeexec"
)

func fakeBackupDir(t *testing.T) (string, Meta) {
	t.Helper()
	m := Meta{ID: "qb-20260930-013000", Name: ScheduledName, CreatedAt: time.Date(2026, 9, 30, 1, 30, 0, 0, time.UTC)}
	dir := filepath.Join(t.TempDir(), m.ID)
	if err := os.MkdirAll(filepath.Join(dir, "postgres"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "s3"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"meta.yaml":               "id: qb-20260930-013000\nname: scheduled\n",
		"postgres/cluster.tar.gz": "pgdata",
		"s3/data.tar":             "objects",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir, m
}

func TestWriteTarManifestMatchesContent(t *testing.T) {
	dir, _ := fakeBackupDir(t)
	pr, pw := io.Pipe()
	done := make(chan string, 1)
	go func() {
		sums, err := writeTar(pw, dir)
		_ = pw.CloseWithError(err)
		done <- sums
	}()
	tr := tar.NewReader(pr)
	files := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if hdr.Mode != 0o600 || hdr.Uid != 0 {
				t.Fatalf("%s mode %o uid %d", hdr.Name, hdr.Mode, hdr.Uid)
			}
			b, _ := io.ReadAll(tr)
			files[hdr.Name] = string(b)
		}
	}
	if files["s3/data.tar"] != "objects" || files["postgres/cluster.tar.gz"] != "pgdata" || files["meta.yaml"] == "" {
		t.Fatalf("tar content %v", files)
	}
	_, _ = io.Copy(io.Discard, pr)
	sums := <-done
	want := fmt.Sprintf("%x  ./s3/data.tar\n", sha256.Sum256([]byte("objects")))
	if !strings.Contains(sums, want) {
		t.Fatalf("manifest missing %q:\n%s", want, sums)
	}
}

func TestOffsitePushPartialVerifyRename(t *testing.T) {
	dir, m := fakeBackupDir(t)
	x := nodeexec.NewFake("dr1")
	o := Offsite{X: x, Source: "hq"}
	if err := o.Push(context.Background(), dir, m); err != nil {
		t.Fatal(err)
	}
	partial := "'" + OffsiteRoot + "/hq/qb-20260930-013000.partial'"
	if !x.Ran("tar -xf - -C " + partial) {
		t.Fatal("not extracted into the partial dir")
	}
	if !x.Ran("chmod 0700 '"+OffsiteRoot+"' '"+OffsiteRoot+"/hq'") || !x.Ran("umask 077") {
		t.Fatal("off-site dirs not private")
	}
	if !x.Ran("sha256sum -c --quiet --strict -") || !x.Ran("./s3/data.tar") {
		t.Fatal("copy not verified against the manifest")
	}
	if !x.Ran("mv " + partial + " '" + OffsiteRoot + "/hq/qb-20260930-013000'") {
		t.Fatal("partial not renamed into place")
	}
}

func TestOffsitePushVerifyFailureRemovesPartial(t *testing.T) {
	dir, m := fakeBackupDir(t)
	x := nodeexec.NewFake("dr1").On("sha256sum -c", nodeexec.Result{ExitCode: 1, Stderr: "s3/data.tar: FAILED"})
	err := Offsite{X: x, Source: "hq"}.Push(context.Background(), dir, m)
	if err == nil || !strings.Contains(err.Error(), "verify") {
		t.Fatalf("err = %v", err)
	}
	if !x.Ran("rm -rf '" + OffsiteRoot + "/hq/qb-20260930-013000.partial'") {
		t.Fatal("failed partial not removed")
	}
	if x.Ran("mv '") {
		t.Fatal("unverified copy was moved into place")
	}
	if _, err := os.Stat(filepath.Join(dir, "s3", "data.tar")); err != nil {
		t.Fatal("local backup touched")
	}
}

func TestOffsitePushRejectsOddID(t *testing.T) {
	dir, _ := fakeBackupDir(t)
	if err := (Offsite{X: nodeexec.NewFake("dr1"), Source: "hq"}).Push(context.Background(), dir, Meta{ID: "../etc"}); err == nil {
		t.Fatal("accepted path-like id")
	}
}

func TestOffsitePruneScheduledOnly(t *testing.T) {
	listing := "---\nid: qb-20260930-013000\nname: scheduled\ncreatedAt: 2026-09-30T01:30:00Z\n" +
		"---\nid: qb-20260930-003000\nname: scheduled\ncreatedAt: 2026-09-30T00:30:00Z\n" +
		"---\nid: qb-20260929-120000\nname: pre-deploy\ncreatedAt: 2026-09-29T12:00:00Z\n"
	x := nodeexec.NewFake("dr1").OnOut("/qb-*/meta.yaml", listing)
	removed, err := Offsite{X: x, Source: "hq"}.Prune(context.Background(), 7, 4, 6, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != "qb-20260930-003000" {
		t.Fatalf("removed %v, want the older same-day scheduled copy", removed)
	}
	if x.Ran("rm -rf '" + OffsiteRoot + "/hq/qb-20260929-120000'") {
		t.Fatal("non-scheduled copy pruned")
	}
}
