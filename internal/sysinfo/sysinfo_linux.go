//go:build linux

package sysinfo

import (
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func platformInfo() Platform {
	var p Platform
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		p.OSName = ParseOSRelease(string(b))
	}
	var u unix.Utsname
	if unix.Uname(&u) == nil {
		p.Kernel = unix.ByteSliceToString(u.Release[:])
	}
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		p.CPUModel = ParseCPUModel(string(b))
	}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		p.Load = ParseLoadavg(string(b))
	}
	var si unix.Sysinfo_t
	if unix.Sysinfo(&si) == nil {
		p.Uptime = time.Duration(si.Uptime) * time.Second
	}
	p.UsagePercent = sampleCPU(250 * time.Millisecond)
	return p
}

// ReadCPUTimes reports cumulative idle and total CPU ticks from /proc/stat.
func ReadCPUTimes() (CPUTimes, bool) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return CPUTimes{}, false
	}
	return ParseProcStat(string(b))
}

// ReadCPUStat reports the aggregate and per-core CPU counters from /proc/stat.
func ReadCPUStat() (CPUStat, []CPUStat, bool) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return CPUStat{}, nil, false
	}
	return ParseProcStatAll(string(b))
}

// ReadTasks reports the thread count and runnable count from /proc/loadavg.
func ReadTasks() (threads, running int, ok bool) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, false
	}
	return ParseTasks(string(b))
}

func memoryInfo() (Memory, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return Memory{}, err
	}
	return ParseMeminfo(string(b)), nil
}

// DiskUsage reports the volume that holds path (or its nearest existing parent).
func DiskUsage(path string) (Disk, error) {
	dir := existingAncestor(path)
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return Disk{}, err
	}
	bs := uint64(st.Bsize)
	d := Disk{
		Path:        strings.TrimSpace(path),
		TotalBytes:  st.Blocks * bs,
		FreeBytes:   st.Bavail * bs,
		InodesTotal: st.Files,
		InodesFree:  st.Ffree,
	}
	d.UsedPercent = usedPercent(d.TotalBytes, d.FreeBytes)
	return d, nil
}
