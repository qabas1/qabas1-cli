package lock_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/qabas1/qabasone-cli/internal/exitcode"
	"github.com/qabas1/qabasone-cli/internal/interrupt"
	"github.com/qabas1/qabasone-cli/internal/lock"
)

const (
	envHold      = "QABAS_LOCK_TEST_HOLD"
	envHoldLimit = "QABAS_LOCK_TEST_LIMIT"
)

// TestMain doubles as a lock-holding helper process for the takeover tests.
func TestMain(m *testing.M) {
	if path := os.Getenv(envHold); path != "" {
		limit, _ := time.ParseDuration(os.Getenv(envHoldLimit))
		l, err := lock.Acquire(path, lock.Info{Command: "qabas test-holder", Limit: limit})
		if err != nil {
			fmt.Println("error:", err)
			os.Exit(1)
		}
		fmt.Println("ready")
		time.Sleep(time.Minute)
		_ = l.Release()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func startHolder(t *testing.T, path string, limit time.Duration) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), envHold+"="+path, envHoldLimit+"="+limit.String())
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	line, _ := bufio.NewReader(out).ReadString('\n')
	if strings.TrimSpace(line) != "ready" {
		t.Fatalf("holder did not start: %q", line)
	}
	return cmd
}

func TestExclusiveLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qabas.lock")
	a, err := lock.Acquire(path, lock.Info{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = lock.Acquire(path, lock.Info{})
	if !errors.Is(err, lock.ErrBusy) {
		t.Fatalf("second acquire: %v, want ErrBusy", err)
	}
	if exitcode.CodeOf(err) != exitcode.Generic {
		t.Fatalf("code %d", exitcode.CodeOf(err))
	}
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	b, err := lock.Acquire(path, lock.Info{})
	if err != nil {
		t.Fatal(err)
	}
	_ = b.Release()
}

func TestHolderRecordAndHeartbeat(t *testing.T) {
	defer lock.SetForTest(20*time.Millisecond, time.Second, time.Second, lock.IsQabasExe)()
	path := filepath.Join(t.TempDir(), "qabas.lock")
	l, err := lock.Acquire(path, lock.Info{Command: "qabas backup", Limit: 90 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	h, err := lock.ReadHolder(path)
	if err != nil {
		t.Fatal(err)
	}
	if h.PID != os.Getpid() || h.Command != "qabas backup" || h.Limit() != 90*time.Minute || h.Legacy {
		t.Fatalf("holder = %+v", h)
	}
	first := h.Heartbeat
	_ = interrupt.Protect("RESTORE", func() error {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if h, err = lock.ReadHolder(path); err == nil && h.Phase == "RESTORE" && h.Heartbeat.After(first) {
				return nil
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("heartbeat never recorded phase: %+v (%v)", h, err)
		return nil
	})
}

func TestParseHolderLegacy(t *testing.T) {
	h, err := lock.ParseHolder([]byte("1234 1700000000\n"))
	if err != nil {
		t.Fatal(err)
	}
	if h.PID != 1234 || !h.Legacy || h.Started.Unix() != 1700000000 {
		t.Fatalf("legacy = %+v", h)
	}
	if _, err := lock.ParseHolder([]byte(`{"pid":7,"started":"2026-10-01T00:00:00Z"}` + "\n" + `trailing"}`)); err != nil {
		t.Fatalf("record with stale tail: %v", err)
	}
	for _, bad := range []string{"", "garbage", `{"command":"x"}`} {
		if _, err := lock.ParseHolder([]byte(bad)); err == nil {
			t.Errorf("ParseHolder(%q) succeeded", bad)
		}
	}
}

func TestEvaluate(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	alive := lock.ProcInfo{Alive: true, State: "S"}
	fresh := lock.Holder{PID: 1, Started: now.Add(-time.Hour), Heartbeat: now.Add(-5 * time.Second), LimitSeconds: 6 * 3600}
	cases := []struct {
		name     string
		h        lock.Holder
		p        lock.ProcInfo
		stuck    bool
		critical bool
	}{
		{"healthy", fresh, alive, false, false},
		{"stale heartbeat", withHeartbeat(fresh, now.Add(-3*time.Minute)), alive, true, false},
		{"over limit", withStarted(fresh, now.Add(-7*time.Hour)), alive, true, false},
		{"critical never stuck", withPhase(withStarted(fresh, now.Add(-7*time.Hour)), "RESTORE"), alive, false, true},
		{"non-critical phase", withPhase(withHeartbeat(fresh, now.Add(-3*time.Minute)), "BACKUP"), alive, true, false},
		{"legacy under limit", lock.Holder{PID: 1, Started: now.Add(-time.Hour), Legacy: true}, alive, false, false},
		{"legacy over limit", lock.Holder{PID: 1, Started: now.Add(-7 * time.Hour), Legacy: true}, alive, true, false},
		{"exited", fresh, lock.ProcInfo{}, false, false},
	}
	if runtime.GOOS != "windows" {
		cases = append(cases, struct {
			name     string
			h        lock.Holder
			p        lock.ProcInfo
			stuck    bool
			critical bool
		}{"stopped", fresh, lock.ProcInfo{Alive: true, State: "T"}, true, false})
	}
	for _, c := range cases {
		s := lock.Evaluate(c.h, c.p, now)
		if s.Stuck != c.stuck || s.Critical != c.critical {
			t.Errorf("%s: stuck=%v critical=%v (%s), want stuck=%v critical=%v", c.name, s.Stuck, s.Critical, s.Reason, c.stuck, c.critical)
		}
	}
}

func withHeartbeat(h lock.Holder, t time.Time) lock.Holder { h.Heartbeat = t; return h }
func withStarted(h lock.Holder, t time.Time) lock.Holder   { h.Started = t; return h }
func withPhase(h lock.Holder, p string) lock.Holder        { h.Phase = p; return h }

func TestIsQabasExe(t *testing.T) {
	for exe, want := range map[string]bool{
		"/usr/local/bin/qabas":                true,
		"qabas":                               true,
		`C:\Program Files\QabasOne\qabas.exe`: true,
		"/usr/bin/sleep":                      false,
		"":                                    false,
	} {
		if got := lock.IsQabasExe(exe); got != want {
			t.Errorf("IsQabasExe(%q) = %v", exe, got)
		}
	}
}

func TestInspect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qabas.lock")
	if st, err := lock.Inspect(path); err != nil || st.Held {
		t.Fatalf("missing file: %+v %v", st, err)
	}
	l, err := lock.Acquire(path, lock.Info{Command: "qabas deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		// flock is per open file description, so the probe from this process sees the lock.
		st, err := lock.Inspect(path)
		if err != nil || !st.Held || st.Holder == nil || st.Holder.PID != os.Getpid() || st.Stuck {
			t.Fatalf("held: %+v %v", st, err)
		}
	}
	_ = l.Release()
	if st, err := lock.Inspect(path); err != nil || st.Held {
		t.Fatalf("released: %+v %v", st, err)
	}
}

func TestAcquireOrResolveWaitsForHealthyHolder(t *testing.T) {
	defer lock.SetForTest(time.Second, time.Second, time.Second, func(string) bool { return true })()
	path := filepath.Join(t.TempDir(), "qabas.lock")
	startHolder(t, path, time.Hour)
	waited, took := 0, false
	_, err := lock.AcquireOrResolve(context.Background(), path, lock.Options{
		Wait: 300 * time.Millisecond, Poll: 50 * time.Millisecond, Takeover: true,
		OnWait:     func(lock.Status) { waited++ },
		OnTakeover: func(lock.Status) { took = true },
	})
	if !errors.Is(err, lock.ErrBusy) || !strings.Contains(err.Error(), "qabas test-holder") {
		t.Fatalf("err = %v, want ErrBusy naming the holder", err)
	}
	if waited == 0 || took {
		t.Fatalf("waited=%d took=%v", waited, took)
	}
}

func TestAcquireOrResolveTakesOverStuckHolder(t *testing.T) {
	defer lock.SetForTest(time.Second, 2*time.Second, 5*time.Second, func(string) bool { return true })()
	path := filepath.Join(t.TempDir(), "qabas.lock")
	holder := startHolder(t, path, time.Second)
	time.Sleep(1500 * time.Millisecond)
	var took lock.Status
	l, err := lock.AcquireOrResolve(context.Background(), path, lock.Options{
		Takeover:   true,
		OnTakeover: func(s lock.Status) { took = s },
	})
	if err != nil {
		t.Fatalf("takeover failed: %v", err)
	}
	defer l.Release()
	if !took.Stuck || took.Holder == nil || took.Holder.PID != holder.Process.Pid {
		t.Fatalf("OnTakeover status = %+v", took)
	}
	h, err := lock.ReadHolder(path)
	if err != nil || h.PID != os.Getpid() {
		t.Fatalf("new holder = %+v %v", h, err)
	}
}

func TestAcquireOrResolveNeverSignalsForeignPID(t *testing.T) {
	defer lock.SetForTest(time.Second, time.Second, time.Second, func(string) bool { return false })()
	path := filepath.Join(t.TempDir(), "qabas.lock")
	holder := startHolder(t, path, time.Second)
	time.Sleep(1500 * time.Millisecond)
	_, err := lock.AcquireOrResolve(context.Background(), path, lock.Options{
		Takeover:   true,
		OnTakeover: func(lock.Status) { t.Error("takeover attempted on a non-qabas pid") },
	})
	if !errors.Is(err, lock.ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	if st, _ := lock.Inspect(path); runtime.GOOS != "windows" && !st.Held {
		t.Fatal("holder lost the lock")
	}
	if holder.ProcessState != nil {
		t.Fatal("holder exited")
	}
}
