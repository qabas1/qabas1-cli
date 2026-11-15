package interrupt_test

import (
	"bytes"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/qabas1/qabas1-cli/internal/exitcode"
	"github.com/qabas1/qabas1-cli/internal/interrupt"
)

func TestProtectDefersSignal(t *testing.T) {
	interrupt.ResetForTest()
	t.Cleanup(interrupt.ResetForTest)

	var buf bytes.Buffer
	interrupt.SetStderrForTest(&buf)
	var exited atomic.Int32
	interrupt.SetExitForTest(func(code int) { exited.Store(int32(code)) })

	ran := false
	err := interrupt.Protect("BACKUP", func() error {
		interrupt.SimulateSignalForTest()
		ran = true
		if interrupt.DepthForTest() != 1 {
			t.Fatalf("depth %d", interrupt.DepthForTest())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("fn not run")
	}
	if exited.Load() != 0 {
		t.Fatalf("exited with %d", exited.Load())
	}
	if !interrupt.Requested() {
		t.Fatal("expected deferred interrupt")
	}
	if !bytes.Contains(buf.Bytes(), []byte("ignored until BACKUP finishes")) {
		t.Fatalf("notice: %q", buf.String())
	}
	if err := interrupt.Check(); exitcode.CodeOf(err) != exitcode.Generic {
		t.Fatalf("Check: %v", err)
	}
}

func TestNestedProtect(t *testing.T) {
	interrupt.ResetForTest()
	t.Cleanup(interrupt.ResetForTest)
	interrupt.SetExitForTest(func(int) {})
	interrupt.SetStderrForTest(&bytes.Buffer{})

	err := interrupt.Protect("OUTER", func() error {
		return interrupt.Protect("INNER", func() error {
			if interrupt.DepthForTest() != 2 {
				t.Fatalf("depth %d", interrupt.DepthForTest())
			}
			interrupt.SimulateSignalForTest()
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if interrupt.DepthForTest() != 0 {
		t.Fatalf("depth after leave %d", interrupt.DepthForTest())
	}
	if !interrupt.Requested() {
		t.Fatal("expected deferred")
	}
}

func TestSignalOutsideProtectExits(t *testing.T) {
	interrupt.ResetForTest()
	t.Cleanup(interrupt.ResetForTest)

	var exited atomic.Int32
	interrupt.SetExitForTest(func(code int) { exited.Store(int32(code)) })
	interrupt.SetStderrForTest(&bytes.Buffer{})

	interrupt.SimulateSignalForTest()
	if exited.Load() != interrupt.ExitCode {
		t.Fatalf("exit code %d", exited.Load())
	}
	if !interrupt.Requested() {
		t.Fatal("expected requested")
	}
}

func TestProtectPropagatesError(t *testing.T) {
	interrupt.ResetForTest()
	t.Cleanup(interrupt.ResetForTest)

	want := errors.New("boom")
	err := interrupt.Protect("X", func() error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("got %v", err)
	}
}
