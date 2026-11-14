// Package sysinfo collects machine resources (CPU, memory, disk, uptime,
// Docker usage) with native OS probes and grades them against thresholds
// used by status, doctor, and autopilot.
package sysinfo

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/qabasone/qabasone-cli/internal/paths"
)

type Info struct {
	CollectedAt time.Time `json:"collectedAt"`
	Hostname    string    `json:"hostname,omitempty"`
	OS          string    `json:"os"`
	OSName      string    `json:"osName,omitempty"`
	Kernel      string    `json:"kernel,omitempty"`
	Arch        string    `json:"arch"`
	Timezone    string    `json:"timezone,omitempty"`
	UptimeSec   int64     `json:"uptimeSec,omitempty"`
	BootTime    time.Time `json:"bootTime,omitempty"`
	CPU         CPU       `json:"cpu"`
	Memory      *Memory   `json:"memory,omitempty"`
	Disks       []Disk    `json:"disks,omitempty"`
	Docker      *Docker   `json:"docker,omitempty"`
	Addresses   []string  `json:"addresses,omitempty"`
	Findings    []Finding `json:"findings,omitempty"`
}

type CPU struct {
	Model        string    `json:"model,omitempty"`
	Cores        int       `json:"cores"`
	Load         []float64 `json:"load,omitempty"`
	UsagePercent float64   `json:"usagePercent,omitempty"`
}

type Memory struct {
	TotalBytes     uint64  `json:"totalBytes"`
	AvailableBytes uint64  `json:"availableBytes"`
	UsedPercent    float64 `json:"usedPercent"`
	SwapTotalBytes uint64  `json:"swapTotalBytes,omitempty"`
	SwapFreeBytes  uint64  `json:"swapFreeBytes,omitempty"`
	// FreeBytes, BuffersBytes and CachedBytes are only known on Linux; they split
	// memory the way htop draws it (used = total - free - buffers - cached).
	FreeBytes    uint64 `json:"freeBytes,omitempty"`
	BuffersBytes uint64 `json:"buffersBytes,omitempty"`
	CachedBytes  uint64 `json:"cachedBytes,omitempty"`
}

// HtopUsedBytes is memory used by programs, excluding buffers and page cache
// (htop's green segment). Without the Linux breakdown it is total - available.
func (m Memory) HtopUsedBytes() uint64 {
	if m.FreeBytes == 0 {
		if m.AvailableBytes > m.TotalBytes {
			return 0
		}
		return m.TotalBytes - m.AvailableBytes
	}
	other := m.FreeBytes + m.BuffersBytes + m.CachedBytes
	if other > m.TotalBytes {
		return 0
	}
	return m.TotalBytes - other
}

type Disk struct {
	Label       string  `json:"label"`
	Path        string  `json:"path"`
	TotalBytes  uint64  `json:"totalBytes"`
	FreeBytes   uint64  `json:"freeBytes"`
	UsedPercent float64 `json:"usedPercent"`
	InodesTotal uint64  `json:"inodesTotal,omitempty"`
	InodesFree  uint64  `json:"inodesFree,omitempty"`
}

// Platform holds OS-specific facts that do not need a path argument.
type Platform struct {
	OSName       string
	Kernel       string
	CPUModel     string
	Uptime       time.Duration
	Load         []float64
	UsagePercent float64
}

// Probes are the lookups Collect relies on. Injectable for tests.
type Probes struct {
	GOOS     string
	GOARCH   string
	NumCPU   int
	Now      func() time.Time
	Hostname func() (string, error)
	Platform func() Platform
	Memory   func() (Memory, error)
	Disk     func(path string) (Disk, error)
	Docker   func() *Docker
	Addrs    func() []string
}

func DefaultProbes() Probes {
	return Probes{
		GOOS:     runtime.GOOS,
		GOARCH:   runtime.GOARCH,
		NumCPU:   runtime.NumCPU(),
		Now:      time.Now,
		Hostname: os.Hostname,
		Platform: platformInfo,
		Memory:   memoryInfo,
		Disk:     DiskUsage,
		Docker:   dockerInfo,
		Addrs:    addresses,
	}
}

// Target is a labelled path whose volume should be reported.
type Target struct {
	Label string
	Path  string
}

// Targets lists the volumes QabasOne depends on.
func Targets(p paths.Paths) []Target {
	sys := "/"
	if runtime.GOOS == "windows" {
		sys = os.Getenv("SystemDrive") + `\`
	}
	return []Target{
		{Label: "system", Path: sys},
		{Label: "state", Path: p.StateDir},
		{Label: "backups", Path: p.Backups()},
		{Label: "logs", Path: p.LogDir},
	}
}

// Collect gathers machine info for the given disk targets using real probes.
func Collect(targets ...Target) Info { return CollectWith(DefaultProbes(), targets...) }

func CollectWith(pr Probes, targets ...Target) Info {
	now := pr.Now()
	info := Info{
		CollectedAt: now,
		OS:          pr.GOOS,
		Arch:        pr.GOARCH,
		CPU:         CPU{Cores: pr.NumCPU},
	}
	if name, _ := now.Zone(); name != "" {
		info.Timezone = now.Location().String()
		if info.Timezone == "Local" {
			info.Timezone = name
		}
	}
	if pr.Hostname != nil {
		info.Hostname, _ = pr.Hostname()
	}
	if pr.Platform != nil {
		pl := pr.Platform()
		info.OSName = pl.OSName
		info.Kernel = pl.Kernel
		info.CPU.Model = pl.CPUModel
		info.CPU.Load = pl.Load
		info.CPU.UsagePercent = pl.UsagePercent
		if pl.Uptime > 0 {
			info.UptimeSec = int64(pl.Uptime.Seconds())
			info.BootTime = now.Add(-pl.Uptime).Truncate(time.Second)
		}
	}
	if pr.Memory != nil {
		if m, err := pr.Memory(); err == nil && m.TotalBytes > 0 {
			info.Memory = &m
		}
	}
	if pr.Docker != nil {
		info.Docker = pr.Docker()
		if info.Docker != nil && pr.GOOS == "linux" && info.Docker.RootDir != "" {
			targets = append(targets, Target{Label: "docker", Path: info.Docker.RootDir})
		}
	}
	if pr.Disk != nil {
		info.Disks = collectDisks(pr.Disk, targets)
	}
	if pr.Addrs != nil {
		info.Addresses = pr.Addrs()
	}
	info.Findings = Evaluate(info)
	return info
}

// collectDisks reports each target once per underlying volume.
func collectDisks(probe func(string) (Disk, error), targets []Target) []Disk {
	var disks []Disk
	seen := map[[2]uint64]int{}
	for _, t := range targets {
		if t.Path == "" {
			continue
		}
		d, err := probe(t.Path)
		if err != nil || d.TotalBytes == 0 {
			continue
		}
		d.Label = t.Label
		key := [2]uint64{d.TotalBytes, d.FreeBytes}
		if i, ok := seen[key]; ok {
			disks[i].Label += "," + t.Label
			continue
		}
		seen[key] = len(disks)
		disks = append(disks, d)
	}
	return disks
}

// existingAncestor returns the nearest existing directory for path so volumes
// can be measured before the directory is created.
func existingAncestor(path string) string {
	for d := filepath.Clean(path); ; d = filepath.Dir(d) {
		if _, err := os.Stat(d); err == nil {
			return d
		}
		if d == filepath.Dir(d) {
			return d
		}
	}
}

func usedPercent(total, free uint64) float64 {
	if total == 0 {
		return 0
	}
	return round1(float64(total-free) / float64(total) * 100)
}

func round1(f float64) float64 { return float64(int64(f*10+0.5)) / 10 }

func addresses() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.IsLinkLocalUnicast() || ipn.IP.IsLoopback() {
				continue
			}
			out = append(out, ipn.IP.String())
		}
	}
	return out
}
