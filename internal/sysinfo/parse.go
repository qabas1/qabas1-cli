package sysinfo

import (
	"bufio"
	"strconv"
	"strings"
)

// ParseMeminfo decodes /proc/meminfo (values in kB).
func ParseMeminfo(s string) Memory {
	kv := map[string]uint64{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		key, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		n, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		kv[strings.TrimSpace(key)] = n * 1024
	}
	m := Memory{
		TotalBytes:     kv["MemTotal"],
		SwapTotalBytes: kv["SwapTotal"],
		SwapFreeBytes:  kv["SwapFree"],
		FreeBytes:      kv["MemFree"],
		BuffersBytes:   kv["Buffers"],
	}
	// htop: cached = Cached + SReclaimable - Shmem.
	if c := kv["Cached"] + kv["SReclaimable"]; c > kv["Shmem"] {
		m.CachedBytes = c - kv["Shmem"]
	}
	if v, ok := kv["MemAvailable"]; ok {
		m.AvailableBytes = v
	} else {
		m.AvailableBytes = kv["MemFree"] + kv["Buffers"] + kv["Cached"]
	}
	if m.AvailableBytes > m.TotalBytes {
		m.AvailableBytes = m.TotalBytes
	}
	m.UsedPercent = usedPercent(m.TotalBytes, m.AvailableBytes)
	return m
}

// ParseOSRelease returns PRETTY_NAME (or NAME VERSION) from /etc/os-release.
func ParseOSRelease(s string) string {
	kv := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok {
			continue
		}
		kv[k] = strings.Trim(v, `"'`)
	}
	if v := kv["PRETTY_NAME"]; v != "" {
		return v
	}
	return strings.TrimSpace(kv["NAME"] + " " + kv["VERSION"])
}

// ParseCPUModel returns the first CPU model from /proc/cpuinfo.
func ParseCPUModel(s string) string {
	sc := bufio.NewScanner(strings.NewReader(s))
	var fallback string
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "model name":
			return v
		case "Hardware", "Model", "cpu model":
			if fallback == "" {
				fallback = v
			}
		}
	}
	return fallback
}

// ParseLoadavg returns the 1/5/15-minute load averages from /proc/loadavg.
func ParseLoadavg(s string) []float64 {
	fields := strings.Fields(s)
	if len(fields) < 3 {
		return nil
	}
	out := make([]float64, 0, 3)
	for _, f := range fields[:3] {
		n, err := strconv.ParseFloat(f, 64)
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	return out
}
