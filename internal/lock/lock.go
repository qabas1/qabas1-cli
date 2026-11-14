package lock

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/qabasone/qabasone-cli/internal/exitcode"
	"github.com/qabasone/qabasone-cli/internal/interrupt"
)

// EnvStuckAfter overrides DefaultLimit (a Go duration such as "8h").
const EnvStuckAfter = "QABAS_LOCK_STUCK_AFTER"

// ErrBusy means another live process holds the lock.
var ErrBusy = errors.New("another qabas operation is running")

var (
	heartbeatEvery = 10 * time.Second
	phaseFn        = interrupt.Label
)

// Info describes the operation taking the lock.
type Info struct {
	// Command defaults to os.Args.
	Command string
	// Limit is the runtime after which the holder counts as stuck; 0 means DefaultLimit.
	Limit time.Duration
}

// Holder is the record the lock holder keeps up to date while it runs.
type Holder struct {
	PID          int       `json:"pid"`
	Command      string    `json:"command,omitempty"`
	Started      time.Time `json:"started"`
	Heartbeat    time.Time `json:"heartbeat,omitempty"`
	Phase        string    `json:"phase,omitempty"`
	LimitSeconds int64     `json:"limit_seconds,omitempty"`
	// Legacy marks a "pid unix-time" stamp written by an older qabas.
	Legacy bool `json:"legacy,omitempty"`
}

// Limit returns the holder's runtime limit.
func (h Holder) Limit() time.Duration {
	if h.LimitSeconds > 0 {
		return time.Duration(h.LimitSeconds) * time.Second
	}
	return DefaultLimit()
}

// DefaultLimit is 6h unless QABAS_LOCK_STUCK_AFTER says otherwise.
func DefaultLimit() time.Duration {
	if v := os.Getenv(EnvStuckAfter); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 6 * time.Hour
}

type Lock struct {
	file   *os.File
	path   string
	mu     sync.Mutex
	holder Holder
	stop   chan struct{}
	done   chan struct{}
}

// Acquire takes the lock without waiting. A busy lock returns an error wrapping ErrBusy.
func Acquire(path string, info Info) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, exitcode.Wrap(exitcode.Generic, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		if os.IsPermission(err) {
			return nil, exitcode.New(exitcode.Generic, "open lock %s: %w (%s)", path, err, staleLockHint(path))
		}
		return nil, exitcode.New(exitcode.Generic, "open lock %s: %w", path, err)
	}
	if err := exclusive(f); err != nil {
		_ = f.Close()
		return nil, exitcode.NewHint(exitcode.Generic, "see who holds it: qabas lock status", "%w (%s)", ErrBusy, path)
	}
	if info.Command == "" {
		info.Command = strings.Join(os.Args, " ")
	}
	if info.Limit <= 0 {
		info.Limit = DefaultLimit()
	}
	now := time.Now().UTC()
	l := &Lock{
		file: f,
		path: path,
		holder: Holder{
			PID:          os.Getpid(),
			Command:      info.Command,
			Started:      now,
			Heartbeat:    now,
			Phase:        phaseFn(),
			LimitSeconds: int64(info.Limit / time.Second),
		},
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	l.write()
	go l.beat()
	return l, nil
}

func (l *Lock) beat() {
	defer close(l.done)
	t := time.NewTicker(heartbeatEvery)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-t.C:
			l.mu.Lock()
			if l.file != nil {
				l.holder.Heartbeat = time.Now().UTC()
				l.holder.Phase = phaseFn()
				l.writeLocked()
			}
			l.mu.Unlock()
		}
	}
}

func (l *Lock) write() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.writeLocked()
}

func (l *Lock) writeLocked() {
	b, err := json.Marshal(l.holder)
	if err != nil {
		return
	}
	_ = writeRecord(l.file, l.path, append(b, '\n'))
}

// ReadHolder returns the record of the current or last holder.
func ReadHolder(path string) (Holder, error) {
	var h Holder
	var err error
	for i := 0; i < 2; i++ {
		var b []byte
		b, err = readRecord(path)
		if err != nil {
			return Holder{}, err
		}
		h, err = parseHolder(b)
		if err == nil {
			return h, nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return Holder{}, err
}

func parseHolder(b []byte) (Holder, error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return Holder{}, errors.New("empty lock record")
	}
	if b[0] == '{' {
		var h Holder
		// Decode only the first value: a rewrite may briefly leave a longer tail behind it.
		if err := json.NewDecoder(bytes.NewReader(b)).Decode(&h); err != nil {
			return Holder{}, fmt.Errorf("parse lock record: %w", err)
		}
		if h.PID <= 0 {
			return Holder{}, errors.New("lock record has no pid")
		}
		return h, nil
	}
	fields := strings.Fields(string(b))
	if len(fields) < 2 {
		return Holder{}, fmt.Errorf("unrecognized lock record %q", string(b))
	}
	pid, err1 := strconv.Atoi(fields[0])
	ts, err2 := strconv.ParseInt(fields[1], 10, 64)
	if err1 != nil || err2 != nil || pid <= 0 {
		return Holder{}, fmt.Errorf("unrecognized lock record %q", string(b))
	}
	return Holder{PID: pid, Started: time.Unix(ts, 0).UTC(), Legacy: true}, nil
}

func staleLockHint(path string) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("run from an elevated PowerShell; remove stale lock: Remove-Item '%s'", path)
	}
	return fmt.Sprintf("mixed sudo/non-sudo runs? remove stale lock: sudo rm %s", path)
}

func (l *Lock) Release() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	if l.file == nil {
		l.mu.Unlock()
		return nil
	}
	close(l.stop)
	f := l.file
	l.file = nil
	l.mu.Unlock()
	<-l.done
	clearRecord(l.path)
	_ = unlock(f)
	return f.Close()
}
