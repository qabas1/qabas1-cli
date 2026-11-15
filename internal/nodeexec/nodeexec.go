// Package nodeexec runs shell scripts on the local node or on a registered remote
// node (over pinned SSH, with sudo -n). All output is redacted before it can reach
// an error message or log.
package nodeexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/qabas1/qabasone-cli/internal/cluster"
	"github.com/qabas1/qabasone-cli/internal/execx"
	"github.com/qabas1/qabasone-cli/internal/redact"
	"github.com/qabas1/qabasone-cli/internal/sshx"
)

type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Executor runs scripts on one node. Scripts run as root.
type Executor interface {
	Node() string
	Run(ctx context.Context, script string, stdin io.Reader) (Result, error)
	Close() error
}

// ScriptError is a non-zero exit with redacted output.
type ScriptError struct {
	Node     string
	ExitCode int
	Stderr   string
}

func (e *ScriptError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if len(msg) > 600 {
		msg = msg[len(msg)-600:]
	}
	return fmt.Sprintf("%s: exit %d: %s", e.Node, e.ExitCode, redact.String(msg))
}

// Output runs script and returns trimmed stdout, failing on non-zero exit.
func Output(ctx context.Context, x Executor, script string) (string, error) {
	res, err := x.Run(ctx, script, nil)
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", &ScriptError{Node: x.Node(), ExitCode: res.ExitCode, Stderr: res.Stderr}
	}
	return strings.TrimSpace(res.Stdout), nil
}

// WriteFile atomically writes data to p with mode on the node.
func WriteFile(ctx context.Context, x Executor, p string, data []byte, mode os.FileMode) error {
	script := fmt.Sprintf("set -eu\numask 077\nmkdir -p %s\ncat > %s\nchmod %o %s\nmv -f %s %s\n",
		Quote(path.Dir(p)), Quote(p+".qabas-tmp"), mode.Perm(), Quote(p+".qabas-tmp"), Quote(p+".qabas-tmp"), Quote(p))
	res, err := x.Run(ctx, script, bytes.NewReader(data))
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return &ScriptError{Node: x.Node(), ExitCode: res.ExitCode, Stderr: res.Stderr}
	}
	return nil
}

// Quote single-quotes s for POSIX sh.
func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'" }

// Local runs on this machine (the control node).
type Local struct{ Name string }

func (l *Local) Node() string { return l.Name }
func (l *Local) Close() error { return nil }

func (l *Local) Run(ctx context.Context, script string, stdin io.Reader) (Result, error) {
	cmd := execx.Command("sh", "-c", script)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr, cmd.Stdin = &out, &errb, stdin
	err := runCtx(ctx, cmd.Start, cmd.Wait, func() { killLocalProcessTree(cmd.Process) })
	res := Result{Stdout: out.String(), Stderr: errb.String()}
	if err != nil {
		var ee interface{ ExitCode() int }
		if errors.As(err, &ee) {
			res.ExitCode = ee.ExitCode()
			return res, nil
		}
		return res, err
	}
	return res, nil
}

func runCtx(ctx context.Context, start func() error, wait func() error, kill func()) error {
	if err := start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		kill()
		<-done
		return ctx.Err()
	}
}

// Remote runs on a registered node over pinned SSH.
type Remote struct {
	name   string
	client *ssh.Client
	sudo   bool
}

// DialNode connects to a registered node using its pinned host key. extraKeys (the
// cluster key) are offered before identity and the defaults.
func DialNode(ctx context.Context, n cluster.Node, identity string, extraKeys ...string) (*Remote, error) {
	auth, cleanup, err := sshx.AuthMethods(identity, extraKeys...)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return DialNodeAuth(ctx, n, auth)
}

// DialNodeAuth connects to a registered node with explicit auth methods.
func DialNodeAuth(ctx context.Context, n cluster.Node, auth []ssh.AuthMethod) (*Remote, error) {
	user := n.SSHUser
	if user == "" {
		user = "root"
	}
	host, port := n.SSHEndpoint()
	if host == "" {
		return nil, fmt.Errorf("node %s has no SSH address yet", n.Name)
	}
	c, err := sshx.Dial(ctx, sshx.Addr(host, port), user, n.SSHHostKey, auth)
	if err != nil {
		return nil, fmt.Errorf("ssh %s@%s: %w", user, sshx.Addr(host, port), err)
	}
	return &Remote{name: n.Name, client: c, sudo: user != "root"}, nil
}

func (r *Remote) Node() string { return r.name }
func (r *Remote) Close() error { return r.client.Close() }

func (r *Remote) Run(ctx context.Context, script string, stdin io.Reader) (Result, error) {
	sess, err := r.client.NewSession()
	if err != nil {
		return Result{}, err
	}
	defer sess.Close()
	var out, errb bytes.Buffer
	sess.Stdout, sess.Stderr, sess.Stdin = &out, &errb, stdin
	cmd := "sh -c " + Quote(script)
	if r.sudo {
		cmd = "sudo -n " + cmd
	}
	err = runCtx(ctx, func() error { return sess.Start(cmd) }, sess.Wait, func() { _ = sess.Signal(ssh.SIGKILL) })
	res := Result{Stdout: out.String(), Stderr: errb.String()}
	if err != nil {
		var ee *ssh.ExitError
		if errors.As(err, &ee) {
			res.ExitCode = ee.ExitStatus()
			return res, nil
		}
		return res, err
	}
	return res, nil
}
