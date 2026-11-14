package sysinfo

import "testing"

func TestParseProcStat(t *testing.T) {
	in := "cpu  100 5 50 800 40 3 2 0 7 0\ncpu0 50 2 25 400 20 1 1 0 0 0\nintr 1 2 3\n"
	got, ok := ParseProcStat(in)
	if !ok {
		t.Fatal("aggregate cpu line not found")
	}
	if got.Total != 1000 || got.Idle != 840 {
		t.Fatalf("got %+v", got)
	}
	if _, ok := ParseProcStat("cpu0 1 2 3 4\n"); ok {
		t.Fatal("per-core lines must not count as the aggregate")
	}
	if _, ok := ParseProcStat("cpu 1 x 3 4\n"); ok {
		t.Fatal("garbage must be rejected")
	}
}

const procStatAll = `cpu  400 20 200 3000 100 10 20 50 0 0
cpu0 100 0 50 800 20 5 5 20 0 0
cpu1 300 20 150 2200 80 5 15 30 0 0
intr 1 2 3
ctxt 99
`

func TestParseProcStatAll(t *testing.T) {
	agg, cores, ok := ParseProcStatAll(procStatAll)
	if !ok || len(cores) != 2 {
		t.Fatalf("ok %v cores %d", ok, len(cores))
	}
	want := CPUStat{User: 400, Nice: 20, System: 200, IRQ: 30, Steal: 50, Idle: 3100, Total: 3800}
	if agg != want {
		t.Fatalf("agg: want %+v, got %+v", want, agg)
	}
	if cores[1].User != 300 || cores[1].Idle != 2280 || cores[1].IRQ != 20 {
		t.Fatalf("core 1: %+v", cores[1])
	}
}

func TestUsageBetween(t *testing.T) {
	before := CPUStat{User: 100, Nice: 0, System: 50, IRQ: 10, Steal: 0, Idle: 840}
	after := CPUStat{User: 150, Nice: 10, System: 70, IRQ: 20, Steal: 10, Idle: 1740}
	u := UsageBetween(before, after)
	// deltas: user 50, nice 10, system 20+irq 10, steal 10, idle 900 => total 1000
	if u.User != 5 || u.Nice != 1 || u.System != 3 || u.Steal != 1 || u.Total != 10 {
		t.Fatalf("got %+v", u)
	}
	glitch := after
	glitch.Steal = 5
	if got := UsageBetween(after, glitch); got != (CoreUsage{}) {
		t.Fatalf("a counter going backwards must read as idle, not 100%%: %+v", got)
	}
	if got := UsageBetween(after, after); got != (CoreUsage{}) {
		t.Fatalf("no elapsed time: %+v", got)
	}
}

func TestParseTasks(t *testing.T) {
	th, run, ok := ParseTasks("0.04 0.16 0.13 1/275 12345\n")
	if !ok || th != 275 || run != 1 {
		t.Fatalf("got %d %d %v", th, run, ok)
	}
	if _, _, ok := ParseTasks("0.04 0.16 0.13"); ok {
		t.Fatal("short line must fail")
	}
}

func TestMeminfoHtopBreakdown(t *testing.T) {
	m := ParseMeminfo("MemTotal: 1000 kB\nMemFree: 200 kB\nMemAvailable: 600 kB\nBuffers: 50 kB\nCached: 300 kB\nSReclaimable: 40 kB\nShmem: 20 kB\n")
	if m.FreeBytes != 200*1024 || m.BuffersBytes != 50*1024 || m.CachedBytes != 320*1024 {
		t.Fatalf("got %+v", m)
	}
	if got := m.HtopUsedBytes(); got != 430*1024 {
		t.Fatalf("htop used: %d", got)
	}
	win := Memory{TotalBytes: 1000, AvailableBytes: 600}
	if win.HtopUsedBytes() != 400 {
		t.Fatal("without the Linux breakdown used is total - available")
	}
}

func TestCPUPercent(t *testing.T) {
	cases := []struct {
		name          string
		before, after CPUTimes
		want          float64
		ok            bool
	}{
		{"busy quarter", CPUTimes{Idle: 800, Total: 1000}, CPUTimes{Idle: 1100, Total: 1400}, 25, true},
		{"idle", CPUTimes{Idle: 0, Total: 0}, CPUTimes{Idle: 100, Total: 100}, 0, true},
		{"no elapsed time", CPUTimes{Idle: 5, Total: 10}, CPUTimes{Idle: 5, Total: 10}, 0, false},
		{"counter reset", CPUTimes{Idle: 500, Total: 1000}, CPUTimes{Idle: 10, Total: 2000}, 0, false},
	}
	for _, c := range cases {
		got, ok := CPUPercent(c.before, c.after)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: want %v/%v, got %v/%v", c.name, c.want, c.ok, got, ok)
		}
	}
}
