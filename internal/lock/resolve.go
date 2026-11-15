package lock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/qabas1/qabasone-cli/internal/exitcode"
)

// CriticalPhases are never interrupted automatically: stopping them mid-way can
// leave a half-restored database or a half-switched release.
var CriticalPhases = map[string]bool{"RESTORE": true, "SWITCH": true, "COMMIT": true}

// HeartbeatStale is how old a heartbeat may get before the holder counts as frozen.
const HeartbeatStale = 2 * time.Minute

var (
	termGrace = 30 * time.Second
	killGrace = 10 * time.Second
	graceTick = 500 * time.Millisecond

	// isQabasExe guards against signaling a process that reused the holder's pid.
	isQabasExe = func(exe string) bool {
		if exe == "" {
			return false
		}
		name := strings.ToLower(filepath.Base(strings.ReplaceAll(exe, `\`, "/")))
		return strings.HasPrefix(name, "qabas")
	}
)

type procInfo struct {
	Alive bool
	State string
	// Exe is argv[0] on unix and the image path on Windows.
	Exe string
}

// Status describes who holds the lock and what auto-resolve would do about it.
type Status struct {
	Path         string        `json:"path"`
	Held         bool          `json:"held"`
	Holder       *Holder       `json:"holder,omitempty"`
	Alive        bool          `json:"alive"`
	State        string        `json:"state,omitempty"`
	Runtime      time.Duration `json:"runtime_ns,omitempty"`
	HeartbeatAge time.Duration `json:"heartbeat_age_ns,omitempty"`
	Critical     bool          `json:"critical"`
	Stuck        bool          `json:"stuck"`
	Reason       string        `json:"reason,omitempty"`
	// Signalable is false when the pid is gone or no longer a qabas process.
	Signalable bool `json:"signalable"`
}

// Summary is a one-line description of the holder.
func (s Status) Summary() string {
	if !s.Held {
		return "lock is free"
	}
	if s.Holder == nil {
		return "held by an unknown process"
	}
	h := s.Holder
	out := fmt.Sprintf("pid %d", h.PID)
	if h.Command != "" {
		out += fmt.Sprintf(" %q", h.Command)
	}
	parts := []string{"running " + shortDuration(s.Runtime)}
	if h.Phase != "" {
		parts = append(parts, "phase "+h.Phase)
	}
	if s.State != "" && s.State != "S" && s.State != "R" {
		parts = append(parts, "state "+s.State)
	}
	return out + " (" + strings.Join(parts, ", ") + ")"
}

// Inspect reports whether the lock is held and, if so, by whom.
func Inspect(path string) (Status, error) {
	held, err := probe(path)
	if err != nil {
		return Status{Path: path}, err
	}
	if !held {
		return Status{Path: path}, nil
	}
	return inspectHeld(path, time.Now()), nil
}

func probe(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()
	if err := exclusive(f); err != nil {
		return true, nil
	}
	_ = unlock(f)
	return false, nil
}

func inspectHeld(path string, now time.Time) Status {
	s := Status{Path: path, Held: true}
	h, err := ReadHolder(path)
	if err != nil {
		s.Reason = "holder record unreadable: " + err.Error()
		return s
	}
	s.Holder = &h
	p := inspectProc(h.PID)
	s.Alive = p.Alive
	s.State = p.State
	s.Signalable = p.Alive && isQabasExe(p.Exe)
	evaluate(&s, p, now)
	return s
}

func evaluate(s *Status, p procInfo, now time.Time) {
	h := s.Holder
	if !h.Started.IsZero() {
		s.Runtime = now.Sub(h.Started)
	}
	if !h.Heartbeat.IsZero() {
		s.HeartbeatAge = now.Sub(h.Heartbeat)
	}
	switch {
	case CriticalPhases[h.Phase]:
		s.Critical = true
		s.Reason = "in critical phase " + h.Phase
	case !p.Alive:
		s.Reason = fmt.Sprintf("pid %d has exited; the lock is held by another process", h.PID)
	case p.stopped():
		s.Stuck = true
		s.Reason = "process is stopped (Ctrl+Z or SIGSTOP)"
	case !h.Legacy && s.HeartbeatAge > HeartbeatStale:
		s.Stuck = true
		s.Reason = "no heartbeat for " + shortDuration(s.HeartbeatAge)
	case s.Runtime > h.Limit():
		s.Stuck = true
		s.Reason = fmt.Sprintf("running for %s, over its %s limit", shortDuration(s.Runtime), shortDuration(h.Limit()))
	default:
		s.Reason = "healthy"
	}
	if s.Stuck && !s.Signalable {
		s.Reason += fmt.Sprintf("; pid %d is not a qabas process, will not signal it", h.PID)
	}
}

// Options control AcquireOrResolve.
type Options struct {
	Info Info
	// Wait is how long to wait for a busy lock: 0 tries once, negative waits until ctx ends.
	Wait time.Duration
	// Poll is the retry interval while waiting (default 2s).
	Poll time.Duration
	// Takeover stops stuck holders outside critical phases.
	Takeover   bool
	OnWait     func(Status)
	OnTakeover func(Status)
}

// AcquireOrResolve takes the lock, stopping a stuck holder when allowed and
// otherwise waiting for the holder to finish.
func AcquireOrResolve(ctx context.Context, path string, o Options) (*Lock, error) {
	if o.Poll <= 0 {
		o.Poll = 2 * time.Second
	}
	deadline := time.Now().Add(o.Wait)
	for {
		l, err := Acquire(path, o.Info)
		if err == nil {
			return l, nil
		}
		if !errors.Is(err, ErrBusy) {
			return nil, err
		}
		st := inspectHeld(path, time.Now())
		if st.Holder != nil && st.Signalable {
			if o.Takeover && st.Stuck {
				if o.OnTakeover != nil {
					o.OnTakeover(st)
				}
				l, err := takeover(ctx, path, st, o.Info)
				if err == nil {
					return l, nil
				}
				if !errors.Is(err, ErrBusy) {
					return nil, err
				}
				st = inspectHeld(path, time.Now())
			} else if st.Critical && st.State != "" && (procInfo{State: st.State}).stopped() {
				_ = resumeProc(st.Holder.PID)
			}
		}
		if o.Wait == 0 || (o.Wait > 0 && time.Now().Add(o.Poll).After(deadline)) {
			return nil, busyErr(st)
		}
		if o.OnWait != nil {
			o.OnWait(st)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(o.Poll):
		}
	}
}

func takeover(ctx context.Context, path string, st Status, info Info) (*Lock, error) {
	pid := st.Holder.PID
	_ = resumeProc(pid)
	_ = termProc(pid)
	if l, err := waitFree(ctx, path, info, termGrace); err == nil || !errors.Is(err, ErrBusy) {
		return l, err
	}
	again := inspectHeld(path, time.Now())
	if again.Holder == nil || again.Holder.PID != pid || again.Critical || !again.Signalable {
		return nil, busyErr(again)
	}
	_ = killProc(pid)
	return waitFree(ctx, path, info, killGrace)
}

func waitFree(ctx context.Context, path string, info Info, grace time.Duration) (*Lock, error) {
	deadline := time.Now().Add(grace)
	for {
		l, err := Acquire(path, info)
		if err == nil || !errors.Is(err, ErrBusy) || time.Now().After(deadline) {
			return l, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(graceTick):
		}
	}
}

func busyErr(st Status) error {
	return exitcode.NewHint(exitcode.Generic, "see who holds it: qabas lock status",
		"%w: %s", ErrBusy, st.Summary())
}

func shortDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return d.Round(time.Second).String()
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
