package sysinfo

import (
	"strconv"
	"strings"
	"time"
)

// CPUTimes are cumulative machine-wide CPU counters (any unit, as long as both
// fields use the same one).
type CPUTimes struct {
	Idle  uint64
	Total uint64
}

// CPUPercent is the busy share of CPU time between two readings.
func CPUPercent(before, after CPUTimes) (float64, bool) {
	if after.Total <= before.Total || after.Idle < before.Idle {
		return 0, false
	}
	total := after.Total - before.Total
	idle := after.Idle - before.Idle
	if idle > total {
		return 0, true
	}
	return round1(float64(total-idle) / float64(total) * 100), true
}

// ParseProcStat decodes the aggregate "cpu" line of /proc/stat. Idle includes
// iowait; guest time is already counted in user/nice, so it is not added again.
func ParseProcStat(s string) (CPUTimes, bool) {
	for _, line := range strings.Split(s, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "cpu" {
			continue
		}
		var vals []uint64
		for _, f := range fields[1:] {
			n, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				return CPUTimes{}, false
			}
			vals = append(vals, n)
		}
		// user nice system idle iowait irq softirq steal guest guest_nice
		var t CPUTimes
		for i, v := range vals {
			if i >= 8 {
				break
			}
			t.Total += v
		}
		t.Idle = vals[3]
		if len(vals) > 4 {
			t.Idle += vals[4]
		}
		return t, true
	}
	return CPUTimes{}, false
}

// CPUStat is one /proc/stat cpu line split the way htop colors it.
type CPUStat struct {
	User, Nice, System, IRQ, Steal, Idle, Total uint64
}

// CoreUsage is the busy share of one CPU (or all of them) between two readings,
// in percent. System includes irq and softirq time.
type CoreUsage struct {
	User   float64 `json:"user"`
	Nice   float64 `json:"nice"`
	System float64 `json:"system"`
	Steal  float64 `json:"steal"`
	Total  float64 `json:"total"`
}

func cpuStatFrom(v []uint64) CPUStat {
	at := func(i int) uint64 {
		if i < len(v) {
			return v[i]
		}
		return 0
	}
	// user nice system idle iowait irq softirq steal guest guest_nice; guest time is
	// already included in user and nice.
	s := CPUStat{
		User: at(0), Nice: at(1), System: at(2), Idle: at(3) + at(4),
		IRQ: at(5) + at(6), Steal: at(7),
	}
	s.Total = s.User + s.Nice + s.System + s.Idle + s.IRQ + s.Steal
	return s
}

// ParseProcStatAll decodes the aggregate "cpu" line and every "cpuN" line of /proc/stat.
func ParseProcStatAll(s string) (CPUStat, []CPUStat, bool) {
	var agg CPUStat
	var cores []CPUStat
	found := false
	for _, line := range strings.Split(s, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.HasPrefix(fields[0], "cpu") {
			continue
		}
		vals := make([]uint64, 0, len(fields)-1)
		for _, f := range fields[1:] {
			n, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				return CPUStat{}, nil, false
			}
			vals = append(vals, n)
		}
		if fields[0] == "cpu" {
			agg, found = cpuStatFrom(vals), true
		} else {
			cores = append(cores, cpuStatFrom(vals))
		}
	}
	return agg, cores, found
}

// UsageBetween is the CPU usage between two readings. A counter that went
// backwards (hypervisor steal or irq accounting glitches) yields zero usage rather
// than a bogus 100% spike.
func UsageBetween(before, after CPUStat) CoreUsage {
	b := []uint64{before.User, before.Nice, before.System, before.IRQ, before.Steal, before.Idle}
	a := []uint64{after.User, after.Nice, after.System, after.IRQ, after.Steal, after.Idle}
	var total uint64
	for i := range a {
		if a[i] < b[i] {
			return CoreUsage{}
		}
		total += a[i] - b[i]
	}
	if total == 0 {
		return CoreUsage{}
	}
	pct := func(x, y uint64) float64 { return round1(float64(x-y) / float64(total) * 100) }
	u := CoreUsage{
		User:   pct(after.User, before.User),
		Nice:   pct(after.Nice, before.Nice),
		System: round1(float64(after.System-before.System+after.IRQ-before.IRQ) / float64(total) * 100),
		Steal:  pct(after.Steal, before.Steal),
	}
	u.Total = round1(u.User + u.Nice + u.System + u.Steal)
	return u
}

// ParseTasks returns the thread count and runnable count from /proc/loadavg
// ("0.04 0.16 0.13 1/275 12345").
func ParseTasks(s string) (threads, running int, ok bool) {
	fields := strings.Fields(s)
	if len(fields) < 4 {
		return 0, 0, false
	}
	r, t, found := strings.Cut(fields[3], "/")
	if !found {
		return 0, 0, false
	}
	running, err1 := strconv.Atoi(r)
	threads, err2 := strconv.Atoi(t)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return threads, running, true
}

// sampleCPU measures busy CPU over d using ReadCPUTimes (0 when unsupported).
func sampleCPU(d time.Duration) float64 {
	a, ok := ReadCPUTimes()
	if !ok {
		return 0
	}
	time.Sleep(d)
	b, ok := ReadCPUTimes()
	if !ok {
		return 0
	}
	pct, _ := CPUPercent(a, b)
	return pct
}
