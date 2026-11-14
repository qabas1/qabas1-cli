// Package interrupt defers SIGINT/SIGTERM during critical sections so
// backup, restore, and deploy switch operations can finish cleanly.
package interrupt

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"

	"github.com/qabasone/qabasone-cli/internal/exitcode"
)

// ExitCode is the conventional shell exit status for SIGINT (128+2).
const ExitCode = 130

var (
	mu        sync.Mutex
	depth     int
	label     string
	deferred  atomic.Bool
	notified  atomic.Bool
	installed atomic.Bool
	stderr    io.Writer = os.Stderr
	exitFn              = func(code int) { os.Exit(code) }
)

// ErrAborted is returned by Check when an interrupt was deferred during a
// critical section (or requested and not yet cleared).
func ErrAborted() error {
	return exitcode.NewHint(exitcode.Generic,
		"re-run the command when ready; check: qabas status",
		"aborted: interrupt received")
}

// Install starts a background listener for os.Interrupt and SIGTERM.
// Safe to call more than once; only the first call arms the handler.
func Install() {
	if !installed.CompareAndSwap(false, true) {
		return
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, shutdownSignals...)
	go func() {
		for range ch {
			handle()
		}
	}()
}

func handle() {
	mu.Lock()
	inCritical := depth > 0
	cur := label
	mu.Unlock()

	deferred.Store(true)
	if inCritical {
		if notified.CompareAndSwap(false, true) {
			fmt.Fprintf(stderr, "\n^C ignored until %s finishes (press again will not force-kill)\n", cur)
		} else {
			fmt.Fprintf(stderr, "\n^C deferred: still finishing %s\n", cur)
		}
		return
	}
	fmt.Fprintln(stderr, "\ninterrupted")
	exitFn(ExitCode)
}

// Protect runs fn while deferring process exit on SIGINT/SIGTERM.
// After fn returns successfully, a deferred interrupt does not fail Protect;
// callers should use Check at the next safe boundary.
func Protect(phase string, fn func() error) error {
	enter(phase)
	defer leave()
	return fn()
}

func enter(phase string) {
	mu.Lock()
	defer mu.Unlock()
	depth++
	if phase != "" {
		label = phase
	}
	if depth == 1 {
		notified.Store(false)
	}
}

func leave() {
	mu.Lock()
	defer mu.Unlock()
	if depth > 0 {
		depth--
	}
	if depth == 0 {
		label = ""
	}
}

// Check returns ErrAborted when an interrupt was requested.
func Check() error {
	if deferred.Load() {
		return ErrAborted()
	}
	return nil
}

// Active reports whether a Protect critical section is running.
func Active() bool {
	mu.Lock()
	defer mu.Unlock()
	return depth > 0
}

// Label returns the phase of the running Protect critical section, or "".
func Label() string {
	mu.Lock()
	defer mu.Unlock()
	return label
}

// Requested reports whether an interrupt has been deferred or received.
func Requested() bool {
	return deferred.Load()
}

// Clear clears the deferred interrupt flag. Intended for tests.
func Clear() {
	deferred.Store(false)
	notified.Store(false)
}

// ResetForTest clears all interrupt state. Intended for tests only.
func ResetForTest() {
	mu.Lock()
	depth = 0
	label = ""
	mu.Unlock()
	deferred.Store(false)
	notified.Store(false)
	exitFn = func(code int) { os.Exit(code) }
	stderr = os.Stderr
}

// SimulateSignalForTest invokes the same path as a real signal.
// Intended for tests only.
func SimulateSignalForTest() {
	handle()
}

// SetStderrForTest redirects interrupt notices. Intended for tests only.
func SetStderrForTest(w io.Writer) {
	if w == nil {
		stderr = os.Stderr
		return
	}
	stderr = w
}

// SetExitForTest replaces os.Exit. Intended for tests only.
func SetExitForTest(fn func(int)) {
	if fn == nil {
		exitFn = func(code int) { os.Exit(code) }
		return
	}
	exitFn = fn
}

// DepthForTest returns the current Protect nesting depth.
func DepthForTest() int {
	mu.Lock()
	defer mu.Unlock()
	return depth
}
