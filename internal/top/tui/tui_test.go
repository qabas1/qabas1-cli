package tui

import (
	"context"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/qabas1/qabas1-cli/internal/sysinfo"
	"github.com/qabas1/qabas1-cli/internal/top"
)

var update = flag.Bool("update", false, "rewrite golden files")

func rows() []top.Service {
	return []top.Service{
		{Service: "api", CPUPercent: 40, MemBytes: 300, NetRxPerSec: 10, Restarts: 0},
		{Service: "worker", CPUPercent: 80, MemBytes: 100, BlockWPerSec: 50, Restarts: 4, State: "running"},
		{Service: "redis", CPUPercent: 40, MemBytes: 900, NetTxPerSec: 99, Health: "healthy"},
	}
}

func names(rs []top.Service) string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Service)
	}
	return strings.Join(out, ",")
}

func TestSortServices(t *testing.T) {
	cases := []struct {
		key     SortKey
		reverse bool
		want    string
	}{
		{SortCPU, false, "worker,api,redis"},
		{SortCPU, true, "redis,api,worker"},
		{SortMem, false, "redis,api,worker"},
		{SortNet, false, "redis,api,worker"},
		{SortIO, false, "worker,api,redis"},
		{SortRestarts, false, "worker,api,redis"},
		{SortName, false, "api,redis,worker"},
		{SortName, true, "worker,redis,api"},
	}
	in := rows()
	for _, c := range cases {
		if got := names(SortServices(in, c.key, c.reverse)); got != c.want {
			t.Errorf("%s reverse=%v: want %s, got %s", c.key, c.reverse, c.want, got)
		}
	}
	if names(in) != "api,worker,redis" {
		t.Fatal("SortServices must not modify its input")
	}
}

func TestFilterServices(t *testing.T) {
	if got := names(FilterServices(rows(), "HEALTHY")); got != "redis" {
		t.Fatalf("got %s", got)
	}
	if got := names(FilterServices(rows(), " ")); got != "api,worker,redis" {
		t.Fatalf("blank filter: %s", got)
	}
}

func TestSortKeyCycleAndParse(t *testing.T) {
	k := SortCPU
	for i := 0; i < int(sortKeyCount); i++ {
		k = k.Next()
	}
	if k != SortCPU {
		t.Fatalf("cycle ended at %s", k)
	}
	if ParseSortKey("MEM") != SortMem || ParseSortKey("bogus") != SortCPU {
		t.Fatal("ParseSortKey")
	}
}

func asciiRenderer() *lipgloss.Renderer {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.Ascii)
	return r
}

func fixture() top.Snapshot {
	now := time.Date(2026, 10, 1, 20, 51, 2, 0, time.UTC)
	s := top.Snapshot{
		CollectedAt: now,
		Host: top.HostSection{Info: sysinfo.Info{
			Hostname: "hq-1", UptimeSec: 3*86400 + 4*3600,
			CPU: sysinfo.CPU{Cores: 8, UsagePercent: 34.2, Load: []float64{0.52, 0.41, 0.3}},
			Memory: &sysinfo.Memory{TotalBytes: 8 << 30, AvailableBytes: 3 << 30, UsedPercent: 62.5,
				FreeBytes: 1 << 30, BuffersBytes: 512 << 20, CachedBytes: 1536 << 20, SwapTotalBytes: 2 << 30, SwapFreeBytes: 1536 << 20},
			Disks: []sysinfo.Disk{{Label: "system,state", Path: "/", TotalBytes: 200 << 30, FreeBytes: 120 << 30, UsedPercent: 40}},
		},
			CPU: &sysinfo.CoreUsage{User: 20, Nice: 2, System: 10, Steal: 2.2, Total: 34.2},
			Cores: []sysinfo.CoreUsage{
				{User: 10, System: 2.3, Total: 12.3}, {User: 1, System: 1, Total: 2},
				{User: 3, System: 1.4, Total: 4.4}, {User: 2, System: 1.1, Total: 3.1},
				{User: 0.5, System: 0.2, Total: 0.7}, {User: 60, Nice: 10, System: 20, Steal: 7, Total: 97},
			},
			Threads: 275, Running: 1,
		},
		Services: top.ServicesSection{Rows: []top.Service{
			{Service: "api", Name: "qabas1-api", State: "running", Health: "healthy", CPUPercent: 12.5,
				MemBytes: 256 << 20, MemLimitBytes: 1 << 30, MemPercent: 25, NetRxPerSec: 2048, NetTxPerSec: 4096,
				StartedAt: now.Add(-26 * time.Hour)},
			{Service: "worker", Name: "qabas1-worker", State: "running", Health: "unhealthy", CPUPercent: 96,
				MemBytes: 900 << 20, MemLimitBytes: 1 << 30, MemPercent: 87.9, BlockWPerSec: 1 << 20, Restarts: 4,
				StartedAt: now.Add(-5 * time.Minute)},
			{Service: "postgres", Name: "qabas1-postgres", State: "running", Health: "healthy", CPUPercent: 3.1,
				MemBytes: 1 << 30, MemPercent: 12.5, StartedAt: now.Add(-72 * time.Hour)},
			{Service: "notifications-dispatcher", Name: "qabas1-notifications-dispatcher", State: "running", CPUPercent: 0.4,
				MemBytes: 48 << 20, MemPercent: 0.6, StartedAt: now.Add(-72 * time.Hour)},
		}, Source: top.SourceEngine},
		Postgres: &top.PostgresSection{
			ArchiveMode: "on",
			Connections: 23, MaxConnections: 100, Active: 3, TPS: 152.3, CacheHitPercent: 99.2,
			ArchivedCount: 10, LastArchivedAt: now.Add(-2 * time.Minute),
			Queries: []top.Query{
				{PID: 4242, User: "qabas", State: "active", Wait: "Lock:relation", DurationSec: 420, Text: "UPDATE invoices SET note = '?' WHERE id = 7"},
				{PID: 4243, User: "qabas", State: "idle in transaction", DurationSec: 12, Text: "BEGIN"},
			},
		},
		Replicas: &top.ReplicasSection{Rows: []top.Replica{
			{Name: "dr1", ClientAddr: "10.8.0.2", State: "streaming", SyncState: "async", LagSeconds: 0.4, LagBytes: 4096,
				Slot: "qabas_replica_dr1", SlotActive: true, RetainedBytes: 64 << 20, WALStatus: "reserved"},
		}, Archive: &top.WALArchive{Connected: true, ClientAddr: "172.21.0.11", Slot: "qabas_wal_archive", SlotActive: true, WALStatus: "reserved"}},
		State: top.StateSection{Probed: true, Installed: true, Release: "1.4.2", Channel: "stable", LastBackup: now.Add(-6 * time.Hour),
			NextBackup: now.Add(3 * time.Hour), AutopilotStatus: "ok", AutopilotLast: now.Add(-12 * time.Minute)},
	}
	s.Findings = top.Evaluate(s)
	return s
}

func loaded(t *testing.T, w, h int) Model {
	t.Helper()
	m := New(Options{Collect: func(context.Context) top.Snapshot { return fixture() }, Interval: 2 * time.Second, Renderer: asciiRenderer()})
	next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	next, cmd := next.Update(snapMsg{snap: fixture(), gen: 0})
	if cmd == nil {
		t.Fatal("a fresh snapshot must schedule the next tick")
	}
	return next.(Model)
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run: go test ./internal/top/tui -update)", err)
	}
	if strings.ReplaceAll(string(want), "\r\n", "\n") != got {
		t.Fatalf("%s mismatch (run: go test ./internal/top/tui -update)\n--- got ---\n%s", name, got)
	}
}

// checkFrame asserts the view is exactly w x h cells, so a resize never leaves
// stale characters behind or wraps lines.
func checkFrame(t *testing.T, v string, w, h int) {
	t.Helper()
	lines := strings.Split(v, "\n")
	if len(lines) != h {
		t.Fatalf("%dx%d: want %d lines, got %d", w, h, h, len(lines))
	}
	for i, l := range lines {
		if lw := lipgloss.Width(l); lw != w {
			t.Fatalf("%dx%d: line %d is %d cells: %q", w, h, i, lw, l)
		}
	}
}

func TestViewGoldens(t *testing.T) {
	cases := []struct {
		name string
		w, h int
	}{
		{"compact", 60, 18},
		{"standard", 120, 40},
		{"standard-200", 200, 50},
		{"too-small", 45, 10},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := loaded(t, c.w, c.h).View()
			checkFrame(t, v, c.w, c.h)
			checkGolden(t, c.name, v)
		})
	}
}

func notInstalledSnap() top.Snapshot {
	s := fixture()
	s.Services = top.ServicesSection{Err: "QabasOne is not installed on this machine"}
	s.Postgres, s.Replicas = nil, nil
	s.State = top.StateSection{Probed: true, Channel: "stable"}
	s.Findings = top.Evaluate(s)
	return s
}

func TestViewNotInstalled(t *testing.T) {
	m := loaded(t, 120, 40)
	next, _ := m.Update(snapMsg{snap: notInstalledSnap(), gen: m.gen})
	v := next.(Model).View()
	checkFrame(t, v, 120, 40)
	checkGolden(t, "not-installed", v)
	for _, want := range []string{"QabasOne not installed", "2 Postgres  not installed", "3 Replicas  not installed",
		"QabasOne is not installed on this machine (sudo qabas install)"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q:\n%s", want, v)
		}
	}
	for _, bad := range []string{"(stable)", "all checks ok", "is QabasOne installed", "disabled", "Maintenance"} {
		if strings.Contains(v, bad) {
			t.Errorf("must not show %q:\n%s", bad, v)
		}
	}
	if n := strings.Count(v, "QabasOne is not installed on this machine"); n != 3 {
		t.Errorf("install hint should appear in the state line, Services and Findings only, got %d", n)
	}
}

func TestViewReplicaRoleIsNotShownAsUninstalled(t *testing.T) {
	s := fixture()
	s.State.Release = ""
	s.State.Role = "replica-ro"
	m := loaded(t, 120, 40)
	next, _ := m.Update(snapMsg{snap: s, gen: m.gen})
	v := next.(Model).View()
	if !strings.Contains(v, "QabasOne replica-ro") {
		t.Fatalf("replica role missing from header:\n%s", v)
	}
	if strings.Contains(v, "QabasOne not installed") || strings.Contains(v, "sudo qabas install") {
		t.Fatalf("replica was displayed as uninstalled:\n%s", v)
	}
}

func TestViewDisabledByFlag(t *testing.T) {
	m := loaded(t, 120, 40)
	s := fixture()
	s.Postgres, s.PostgresDisabled = nil, true
	next, _ := m.Update(snapMsg{snap: s, gen: m.gen})
	if v := next.(Model).View(); !strings.Contains(v, "disabled (--no-postgres)") {
		t.Fatalf("flag reason missing:\n%s", v)
	}
}

func TestViewModes(t *testing.T) {
	if v := loaded(t, 45, 10).View(); !strings.Contains(v, "too small") {
		t.Fatalf("too-small notice missing:\n%s", v)
	}
	v := loaded(t, 200, 50).View()
	if strings.Contains(v, "│") {
		t.Fatalf("panes must stack vertically at every width:\n%s", v)
	}
	lines := strings.Split(v, "\n")
	idx := func(s string) int {
		for i, l := range lines {
			if strings.Contains(l, s) {
				return i
			}
		}
		return -1
	}
	if a, b, c, d := idx(" 1 Services"), idx(" 2 Postgres"), idx(" 3 Replicas"), idx(" 4 Findings"); !(a < b && b < c && c < d && a >= 0) {
		t.Fatalf("pane order %d %d %d %d:\n%s", a, b, c, d, v)
	}
	if v := loaded(t, 120, 40).View(); !strings.Contains(v, "notifications-dispatcher") {
		t.Fatalf("long service name must fit at standard width:\n%s", v)
	}
}

func TestMeter(t *testing.T) {
	m := loaded(t, 120, 40)
	got := m.meter("0", []segment{{0.25, m.style.cpuUser}, {0.25, m.style.cpuSystem}}, "12.3%", nil, 30)
	if want := "   0[||||||||||||       12.3%]"; got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
	full := m.meter("Mem", []segment{{1, m.style.memUsed}}, "8.0G/8.0G", nil, 24)
	if want := " Mem[|||||||||8.0G/8.0G]"; full != want {
		t.Fatalf("value must overlay the bar end: %q", full)
	}
	if w := lipgloss.Width(m.meter("Disk", nil, strings.Repeat("x", 80), nil, 20)); w != 20 {
		t.Fatalf("a value that does not fit is dropped, width %d", w)
	}
}

func TestMeterGrid(t *testing.T) {
	m := loaded(t, 120, 40)
	lines := m.meterLines(meterFull)
	// 6 cores in two columns of 3, then Mem/Swp/Disk on the left and tasks/load/uptime on the right.
	if len(lines) != 6 || !strings.Contains(lines[0], "   0[") || !strings.Contains(lines[0], "   3[") ||
		!strings.Contains(lines[3], " Mem[") || !strings.Contains(lines[3], "Tasks: 275 thr, 1 running") ||
		!strings.Contains(lines[5], "Disk[") || !strings.Contains(lines[5], "Uptime: 3 days, 04:00:00") {
		t.Fatalf("two-column grid:\n%s", strings.Join(lines, "\n"))
	}

	m = loaded(t, 80, 40)
	lines = m.meterLines(meterFull)
	if strings.Contains(lines[0], "   3[") || !strings.Contains(lines[3], "   3[") {
		t.Fatalf("one column below 100 columns:\n%s", strings.Join(lines, "\n"))
	}

	m = loaded(t, 120, 40)
	m.snap.Host.Cores = make([]sysinfo.CoreUsage, 32)
	lines = m.meterLines(meterFull)
	if !strings.Contains(lines[0], "   0[") || !strings.Contains(lines[0], "   1[") || !strings.Contains(lines[0], "  16[") {
		t.Fatalf("32 cores pack two per row in each column:\n%s", strings.Join(lines, "\n"))
	}
	if n := strings.Count(strings.Join(lines[:8], "\n"), "["); n != 32 {
		t.Fatalf("32 core meters in 8 rows, got %d", n)
	}

	agg := loaded(t, 120, 40).meterLines(meterAggregate)
	if !strings.Contains(agg[0], " CPU[") || strings.Contains(strings.Join(agg, "\n"), "   0[") {
		t.Fatalf("aggregate meter:\n%s", strings.Join(agg, "\n"))
	}
}

func TestShortTerminalCollapsesMeters(t *testing.T) {
	// 14 rows: full meters (8 header lines + footer) would leave fewer than minBodyRows.
	v := loaded(t, 120, 14).View()
	if strings.Contains(v, "   0[") || !strings.Contains(v, " CPU[") {
		t.Fatalf("short terminals use one CPU meter:\n%s", v)
	}
}

func TestUptime(t *testing.T) {
	cases := map[time.Duration]string{
		23*time.Hour + 53*time.Minute + 14*time.Second: "23:53:14",
		25 * time.Hour:                "1 day, 01:00:00",
		46*24*time.Hour + 6*time.Hour: "46 days, 06:00:00",
	}
	for d, want := range cases {
		if got := uptime(d); got != want {
			t.Errorf("%v: want %s, got %s", d, want, got)
		}
	}
}

func TestArchiveText(t *testing.T) {
	m := loaded(t, 120, 40)
	cases := []struct {
		pg   top.PostgresSection
		want string
	}{
		{top.PostgresSection{ArchiveMode: "off"}, "WAL archive off"},
		{top.PostgresSection{ArchiveMode: "on"}, "WAL archive idle"},
		{top.PostgresSection{ArchiveMode: "on", ArchivedCount: 3, LastArchivedAt: m.snap.CollectedAt.Add(-time.Minute)}, "WAL archive ok"},
		{top.PostgresSection{ArchiveMode: "on", LastFailedAt: m.snap.CollectedAt.Add(-time.Minute)}, "WAL archive failing"},
	}
	stream := []struct {
		a    top.WALArchive
		want string
	}{
		{top.WALArchive{Connected: true, Slot: "qabas_wal_archive", SlotActive: true}, "WAL archive streaming (pg_receivewal), 0 B behind"},
		{top.WALArchive{Slot: "qabas_wal_archive"}, "WAL archive slot inactive"},
		{top.WALArchive{}, "WAL archive not connected"},
	}
	off := top.PostgresSection{ArchiveMode: "off"}
	for _, c := range stream {
		m.snap.Replicas.Archive = &c.a
		if got := m.archiveText(&off); got != c.want {
			t.Errorf("%+v: want %q, got %q", c.a, c.want, got)
		}
	}
	m.snap.Replicas.Archive = nil
	for _, c := range cases {
		if got := m.archiveText(&c.pg); !strings.HasPrefix(got, c.want) {
			t.Errorf("%+v: want %q, got %q", c.pg, c.want, got)
		}
	}
}

func TestViewEverySize(t *testing.T) {
	for w := 40; w <= 250; w += 3 {
		for h := 10; h <= 70; h += 4 {
			m := loaded(t, w, h)
			checkFrame(t, m.View(), w, h)
			for _, p := range []Pane{PaneServices, PanePostgres, PaneReplicas, PaneFindings} {
				m.focus = p
				checkFrame(t, m.View(), w, h)
			}
		}
	}
}

func TestResizeSequence(t *testing.T) {
	m := loaded(t, 220, 60)
	m.scroll[PaneServices] = 10
	for _, sz := range [][2]int{{220, 60}, {48, 11}, {60, 14}, {130, 35}, {250, 70}, {80, 24}} {
		next, _ := m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		m = next.(Model)
		checkFrame(t, m.View(), sz[0], sz[1])
	}
	if m.scroll[PaneServices] >= m.paneLen(PaneServices) {
		t.Fatalf("scroll %d not clamped", m.scroll[PaneServices])
	}
}

func TestSizeFuncWinsAndPollResizes(t *testing.T) {
	w, h := 150, 45
	m := New(Options{
		Collect:  func(context.Context) top.Snapshot { return fixture() },
		Renderer: asciiRenderer(),
		Width:    90, Height: 30,
		SizeFunc: func() (int, int, bool) { return w, h, true },
		PollSize: 500 * time.Millisecond,
	})
	if m.width != 90 || m.height != 30 {
		t.Fatalf("initial size %dx%d", m.width, m.height)
	}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 400, Height: 300})
	m = next.(Model)
	if m.width != 150 || m.height != 45 {
		t.Fatalf("buffer size event must defer to the visible window: %dx%d", m.width, m.height)
	}
	w, h = 70, 20
	next, cmd := m.Update(sizePollMsg{})
	m = next.(Model)
	if m.width != 70 || m.height != 20 || cmd == nil {
		t.Fatalf("poll: %dx%d cmd %v", m.width, m.height, cmd)
	}
	if New(Options{Renderer: asciiRenderer()}).pollCmd() != nil {
		t.Fatal("no polling without a size func")
	}
}

func TestViewBeforeFirstSnapshot(t *testing.T) {
	m := New(Options{Renderer: asciiRenderer()})
	if !strings.Contains(m.View(), "collecting") {
		t.Fatal(m.View())
	}
}

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func press(m Model, keys ...string) (Model, tea.Cmd) {
	var cmd tea.Cmd
	var next tea.Model = m
	for _, k := range keys {
		next, cmd = next.Update(keyMsg(k))
	}
	return next.(Model), cmd
}

func TestKeysFocusSortFilter(t *testing.T) {
	m := loaded(t, 140, 40)
	m, _ = press(m, "tab", "tab")
	if m.focus != PaneReplicas {
		t.Fatalf("focus %s", m.focus)
	}
	m, _ = press(m, "1")
	if m.focus != PaneServices {
		t.Fatalf("focus %s", m.focus)
	}
	m, _ = press(m, "s", "r")
	if m.sortKey != SortMem || !m.reverse {
		t.Fatalf("sort %s reverse %v", m.sortKey, m.reverse)
	}
	m, _ = press(m, "/", "w", "o", "x", "backspace", "enter")
	if m.filtering || m.filter != "wo" {
		t.Fatalf("filter %q filtering %v", m.filter, m.filtering)
	}
	if got := names(m.visibleServices()); got != "worker" {
		t.Fatalf("visible %s", got)
	}
	m, _ = press(m, "esc")
	if m.filter != "" {
		t.Fatal("esc must clear the filter")
	}
}

func TestKeysIntervalPauseQuit(t *testing.T) {
	m := loaded(t, 140, 40)
	m, _ = press(m, "+", "+", "+")
	if m.interval != MinInterval {
		t.Fatalf("interval %s", m.interval)
	}
	m.collecting = false
	m, _ = press(m, "p")
	if !m.paused {
		t.Fatal("p must pause")
	}
	if _, cmd := m.Update(tickMsg{gen: m.gen}); cmd != nil {
		t.Fatal("paused dashboard must not collect")
	}
	m, cmd := press(m, "p")
	if m.paused || cmd == nil || !m.collecting {
		t.Fatal("unpause must refresh immediately")
	}
	if _, cmd := press(m, "q"); cmd == nil || cmd() != tea.Quit() {
		t.Fatal("q must quit")
	}
}

func TestStaleTickIgnored(t *testing.T) {
	m := loaded(t, 140, 40)
	m.gen = 3
	if _, cmd := m.Update(tickMsg{gen: 2}); cmd != nil {
		t.Fatal("stale tick must be ignored")
	}
}

func TestClampInterval(t *testing.T) {
	if clampInterval(0) != 2*time.Second || clampInterval(time.Millisecond) != MinInterval || clampInterval(time.Hour) != MaxInterval {
		t.Fatal("clampInterval")
	}
}

func TestCompactBytes(t *testing.T) {
	cases := map[float64]string{0: "0", 512: "512B", 2048: "2.0K", 1 << 20: "1.0M", 300 << 20: "300M"}
	for in, want := range cases {
		if got := compactBytes(in); got != want {
			t.Errorf("%v: want %s, got %s", in, want, got)
		}
	}
}
