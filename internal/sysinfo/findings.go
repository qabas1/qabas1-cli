package sysinfo

import (
	"fmt"
	"time"
)

const (
	LevelOK   = "ok"
	LevelWarn = "warn"
	LevelCrit = "crit"
)

// Thresholds that grade machine resources.
const (
	MemWarnPercent         = 10.0
	MemCritPercent         = 5.0
	DiskWarnPercent        = 15.0
	DiskCritPercent        = 5.0
	DiskMinFreeBytes       = 10 << 30
	DockerReclaimWarnBytes = 5 << 30
	DockerMinMemBytes      = 2 << 30
)

type Finding struct {
	Check  string `json:"check"`
	Level  string `json:"level"`
	Detail string `json:"detail"`
}

// Evaluate grades memory, disk, and Docker usage.
func Evaluate(info Info) []Finding {
	var out []Finding
	if m := info.Memory; m != nil && m.TotalBytes > 0 {
		availPct := float64(m.AvailableBytes) / float64(m.TotalBytes) * 100
		lvl := LevelOK
		switch {
		case availPct < MemCritPercent:
			lvl = LevelCrit
		case availPct < MemWarnPercent:
			lvl = LevelWarn
		}
		out = append(out, Finding{Check: "memory", Level: lvl, Detail: fmt.Sprintf("%s available of %s (%.0f%% free)",
			HumanBytes(m.AvailableBytes), HumanBytes(m.TotalBytes), availPct)})
	}
	for _, d := range info.Disks {
		out = append(out, DiskFinding(d))
	}
	if dk := info.Docker; dk != nil {
		lvl := LevelOK
		if dk.ReclaimableBytes > DockerReclaimWarnBytes {
			lvl = LevelWarn
		}
		detail := HumanBytes(dk.ReclaimableBytes) + " reclaimable"
		if info.OS != "linux" && dk.MemTotalBytes > 0 && dk.MemTotalBytes < DockerMinMemBytes {
			lvl = LevelWarn
			detail += fmt.Sprintf("; Docker VM has only %s RAM (raise it in Docker Desktop / .wslconfig)", HumanBytes(dk.MemTotalBytes))
		}
		out = append(out, Finding{Check: "docker-disk", Level: lvl, Detail: detail})
	}
	return out
}

// DiskFinding grades free space on one volume.
func DiskFinding(d Disk) Finding {
	freePct := 100 - d.UsedPercent
	lvl := LevelOK
	switch {
	case freePct < DiskCritPercent:
		lvl = LevelCrit
	case freePct < DiskWarnPercent || d.FreeBytes < DiskMinFreeBytes:
		lvl = LevelWarn
	}
	if d.InodesTotal > 0 && float64(d.InodesFree)/float64(d.InodesTotal)*100 < DiskCritPercent {
		lvl = LevelCrit
	}
	return Finding{
		Check:  "disk:" + d.Label,
		Level:  lvl,
		Detail: fmt.Sprintf("%s free of %s (%.0f%% free) at %s", HumanBytes(d.FreeBytes), HumanBytes(d.TotalBytes), freePct, d.Path),
	}
}

// Worst returns the most severe level among findings.
func Worst(fs []Finding) string {
	w := LevelOK
	for _, f := range fs {
		if f.Level == LevelCrit {
			return LevelCrit
		}
		if f.Level == LevelWarn {
			w = LevelWarn
		}
	}
	return w
}

// HumanBytes renders bytes in binary units (KiB, MiB, GiB, TiB).
func HumanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTP"[exp])
}

// HumanDuration renders an uptime such as "3d 4h 12m".
func HumanDuration(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}
