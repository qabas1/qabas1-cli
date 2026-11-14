package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/qabasone/qabasone-cli/internal/state"
	"github.com/qabasone/qabasone-cli/internal/sysinfo"
	"github.com/spf13/cobra"
)

type autoscaleState struct {
	Enabled   bool      `json:"enabled"`
	HighSince time.Time `json:"highSince,omitempty"`
	LowSince  time.Time `json:"lowSince,omitempty"`
	ChangedAt time.Time `json:"changedAt,omitempty"`
	LastError string    `json:"lastError,omitempty"`
}

func autoscaleFile(e *env) string { return filepath.Join(e.Paths.StateDir, "autoscale-api.json") }

func readAutoscale(e *env) autoscaleState {
	var s autoscaleState
	b, _ := os.ReadFile(autoscaleFile(e))
	_ = json.Unmarshal(b, &s)
	return s
}

func writeAutoscale(e *env, s autoscaleState) error {
	if err := os.MkdirAll(e.Paths.StateDir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(autoscaleFile(e), append(b, '\n'), 0o600)
}

func apiCPU(e *env, version string) (float64, int, error) {
	ids, err := newRuntime().APIContainerIDs(e.Paths, scaleEnvironment(e, version, scaleCount(e)))
	if err != nil {
		return 0, 0, err
	}
	if len(ids) == 0 {
		return 0, 0, fmt.Errorf("no API containers running")
	}
	for _, id := range ids {
		out, err := exec.Command("docker", "inspect", "--format", "{{.State.Health.Status}}", id).CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != "healthy" {
			return 0, 0, fmt.Errorf("API container %s is not healthy: %s", id, strings.TrimSpace(string(out)))
		}
	}
	args := append([]string{"stats", "--no-stream", "--format", "{{.CPUPerc}}"}, ids...)
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		return 0, 0, fmt.Errorf("docker stats: %w: %s", err, strings.TrimSpace(string(out)))
	}
	var sum float64
	metrics := strings.Fields(string(out))
	if len(metrics) != len(ids) {
		return 0, 0, fmt.Errorf("received %d CPU samples for %d API containers", len(metrics), len(ids))
	}
	for _, line := range metrics {
		cpu, err := strconv.ParseFloat(strings.TrimSuffix(line, "%"), 64)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid CPU metric %q", line)
		}
		sum += cpu
	}
	return sum / float64(len(ids)), len(ids), nil
}

func autoscalePass(e *env, now time.Time) error {
	s := readAutoscale(e)
	if !s.Enabled {
		return nil
	}
	st, err := state.Load(e.Paths)
	if err != nil {
		return err
	}
	if !st.Installed() {
		return fmt.Errorf("QabasOne is not installed")
	}
	if st.Maintenance {
		return nil
	}
	_, writer, err := e.scheduleCluster()
	if err != nil {
		return err
	}
	if !writer {
		return fmt.Errorf("autoscaling requires the writer node")
	}
	cpu, running, err := apiCPU(e, st.CurrentRelease)
	if err != nil {
		s.LastError = err.Error()
		_ = writeAutoscale(e, s)
		return err
	}
	count := scaleCount(e)
	if running != count {
		s.LastError = fmt.Sprintf("expected %d API containers, found %d", count, running)
		_ = writeAutoscale(e, s)
		return fmt.Errorf("%s", s.LastError)
	}
	if cpu > 70 {
		if s.HighSince.IsZero() {
			s.HighSince = now
		}
		s.LowSince = time.Time{}
	} else if cpu < 30 {
		if s.LowSince.IsZero() {
			s.LowSince = now
		}
		s.HighSince = time.Time{}
	} else {
		s.HighSince, s.LowSince = time.Time{}, time.Time{}
	}
	next := count
	if now.Sub(s.ChangedAt) >= 5*time.Minute {
		if count < scaleMax && !s.HighSince.IsZero() && now.Sub(s.HighSince) >= 3*time.Minute {
			next++
		}
		if count > scaleMin && !s.LowSince.IsZero() && now.Sub(s.LowSince) >= 10*time.Minute {
			next--
		}
	}
	if next != count {
		if next > count {
			info := sysinfo.Collect(sysinfo.Targets(e.Paths)...)
			if info.Memory == nil || info.Memory.AvailableBytes < 2*1024*1024*1024 {
				s.LastError = "insufficient available host memory for another API replica (need 2 GiB)"
				_ = writeAutoscale(e, s)
				return fmt.Errorf("%s", s.LastError)
			}
		}
		if err := setAPIScale(e, next); err != nil {
			s.LastError = err.Error()
			_ = writeAutoscale(e, s)
			return err
		}
		s.ChangedAt = now
		s.HighSince, s.LowSince = time.Time{}, time.Time{}
	}
	s.LastError = ""
	return writeAutoscale(e, s)
}

func autoscaleUnits() (string, string) {
	service := "[Unit]\nDescription=QabasOne API autoscaler\nAfter=docker.service\n\n[Service]\nType=oneshot\nExecStart=" + qabasCmd + " autoscale run --quiet --non-interactive\n"
	timer := "[Unit]\nDescription=QabasOne API autoscaler timer\n\n[Timer]\nOnBootSec=2min\nOnUnitActiveSec=30s\nAccuracySec=5s\n\n[Install]\nWantedBy=timers.target\n"
	return service, timer
}

func newAutoscaleCmd(f *flags) *cobra.Command {
	cmd := &cobra.Command{Use: "autoscale", Short: "Automatically keep 2 to 4 API replicas", RunE: parentHelp()}
	cmd.AddCommand(&cobra.Command{Use: "enable", RunE: func(_ *cobra.Command, _ []string) error {
		e := load(f)
		return e.withLock(func() error {
			if runtime.GOOS != "linux" {
				return fmt.Errorf("autoscale timer requires Linux systemd")
			}
			if err := setAPIScale(e, scaleMin); err != nil {
				return err
			}
			if f.DryRun {
				return nil
			}
			svc, timer := autoscaleUnits()
			if err := os.WriteFile("/etc/systemd/system/qabas-autoscale.service", []byte(svc), 0o644); err != nil {
				return err
			}
			if err := os.WriteFile("/etc/systemd/system/qabas-autoscale.timer", []byte(timer), 0o644); err != nil {
				return err
			}
			out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput()
			if err != nil {
				return fmt.Errorf("systemctl daemon-reload: %w: %s", err, out)
			}
			out, err = exec.Command("systemctl", "enable", "--now", "qabas-autoscale.timer").CombinedOutput()
			if err != nil {
				return fmt.Errorf("enable autoscale timer: %w: %s", err, out)
			}
			s := readAutoscale(e)
			s.Enabled = true
			s.ChangedAt = time.Now()
			s.HighSince, s.LowSince = time.Time{}, time.Time{}
			return writeAutoscale(e, s)
		})
	}})
	cmd.AddCommand(&cobra.Command{Use: "disable", RunE: func(_ *cobra.Command, _ []string) error {
		e := load(f)
		return e.withLock(func() error {
			if f.DryRun {
				e.Out.Println("would disable API autoscaling")
				return nil
			}
			s := readAutoscale(e)
			s.Enabled = false
			if err := writeAutoscale(e, s); err != nil {
				return err
			}
			if runtime.GOOS == "linux" {
				_, _ = exec.Command("systemctl", "disable", "--now", "qabas-autoscale.timer").CombinedOutput()
			}
			return nil
		})
	}})
	cmd.AddCommand(&cobra.Command{Use: "status", RunE: func(_ *cobra.Command, _ []string) error {
		e := load(f)
		s := readAutoscale(e)
		if f.JSON {
			return e.Out.Encode(map[string]any{"enabled": s.Enabled, "replicas": scaleCount(e), "lastError": s.LastError})
		}
		e.Out.Printf("enabled=%t replicas=%d lastError=%s\n", s.Enabled, scaleCount(e), s.LastError)
		return nil
	}})
	cmd.AddCommand(&cobra.Command{Use: "run", Hidden: true, RunE: func(_ *cobra.Command, _ []string) error {
		e := load(f)
		return e.withLock(func() error { return autoscalePass(e, time.Now()) })
	}})
	return cmd
}
