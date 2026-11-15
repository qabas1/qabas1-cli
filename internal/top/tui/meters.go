package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/qabas1/qabasone-cli/internal/sysinfo"
)

// meterLabelW is the right-aligned label width of every meter ("   0", " Mem", "Disk").
const meterLabelW = 4

// maxCoreRowsPerColumn is how many meter rows a column of cores may use before
// several cores share a row, as htop does on big machines.
const maxCoreRowsPerColumn = 8

// segment is one colored part of a meter bar, as a fraction of the full bar.
type segment struct {
	frac  float64
	style lipgloss.Style
}

// meterDetail controls how much of the header meter block is drawn when rows are short.
type meterDetail int

const (
	meterFull      meterDetail = iota // one meter per core, Mem, Swp, Disk
	meterAggregate                    // one CPU meter instead of per-core meters
	meterMinimal                      // CPU and Mem only
)

// meter renders htop's "label[|||||      value]" in exactly width cells: the label is
// right-aligned, and the value overlays the right end of the bar inside the brackets.
func (m Model) meter(label string, segs []segment, value string, valueStyle *lipgloss.Style, width int) string {
	inner := width - meterLabelW - 2
	if inner < 1 {
		return fitWidth(pad(label, meterLabelW, true), width)
	}
	const unstyled = -1
	cells := make([]int, inner) // style index per cell, unstyled = blank
	for i := range cells {
		cells[i] = unstyled
	}
	cum, prev := 0.0, 0
	for si, s := range segs {
		cum += min(max(s.frac, 0), 1)
		end := min(int(cum*float64(inner)+0.5), inner)
		for i := prev; i < end; i++ {
			cells[i] = si
		}
		prev = max(prev, end)
	}
	text := []rune(strings.Repeat(" ", inner))
	for i, c := range cells {
		if c != unstyled {
			text[i] = '|'
		}
	}
	valueAt := inner
	if v := []rune(value); len(v) > 0 && len(v) <= inner {
		valueAt = inner - len(v)
		copy(text[valueAt:], v)
	}

	var b strings.Builder
	b.WriteString(m.style.meterLabel.Render(pad(label, meterLabelW, true)))
	b.WriteString(m.style.bold.Render("["))
	for i := 0; i < valueAt; {
		j := i
		for j < valueAt && cells[j] == cells[i] {
			j++
		}
		run := string(text[i:j])
		if cells[i] != unstyled {
			run = segs[cells[i]].style.Render(run)
		}
		b.WriteString(run)
		i = j
	}
	if valueAt < inner {
		v := string(text[valueAt:])
		if valueStyle != nil {
			v = valueStyle.Render(v)
		} else {
			v = m.style.meterValue.Render(v)
		}
		b.WriteString(v)
	}
	b.WriteString(m.style.bold.Render("]"))
	return b.String()
}

func (m Model) cpuSegments(u sysinfo.CoreUsage) []segment {
	return []segment{
		{u.Nice / 100, m.style.cpuNice},
		{u.User / 100, m.style.cpuUser},
		{u.System / 100, m.style.cpuSystem},
		{u.Steal / 100, m.style.cpuSteal},
	}
}

func (m Model) cpuMeter(label string, u sysinfo.CoreUsage, width int) string {
	lvl := m.style.level(cpuLevel(u.Total))
	return m.meter(label, m.cpuSegments(u), fmt.Sprintf("%.1f%%", u.Total), &lvl, width)
}

// aggregateCPU is the whole-machine usage, split when the breakdown is known.
func (m Model) aggregateCPU() sysinfo.CoreUsage {
	h := m.snap.Host
	if h.CPU != nil {
		return *h.CPU
	}
	pct := h.Info.CPU.UsagePercent
	return sysinfo.CoreUsage{User: pct, Total: pct}
}

func (m Model) memMeter(mem *sysinfo.Memory, width int) string {
	total := float64(mem.TotalBytes)
	used := float64(mem.HtopUsedBytes())
	segs := []segment{
		{used / total, m.style.memUsed},
		{float64(mem.BuffersBytes) / total, m.style.memBuffers},
		{float64(mem.CachedBytes) / total, m.style.memCache},
	}
	var lvl *lipgloss.Style
	if total > 0 {
		free := float64(mem.AvailableBytes) / total * 100
		switch {
		case free < sysinfo.MemCritPercent:
			lvl = &m.style.crit
		case free < sysinfo.MemWarnPercent:
			lvl = &m.style.warn
		}
	}
	return m.meter("Mem", segs, compactBytes(used)+"/"+compactBytes(total), lvl, width)
}

func (m Model) swapMeter(mem *sysinfo.Memory, width int) string {
	used := float64(mem.SwapTotalBytes - min(mem.SwapFreeBytes, mem.SwapTotalBytes))
	frac := 0.0
	if mem.SwapTotalBytes > 0 {
		frac = used / float64(mem.SwapTotalBytes)
	}
	return m.meter("Swp", []segment{{frac, m.style.swap}}, compactBytes(used)+"/"+compactBytes(float64(mem.SwapTotalBytes)), nil, width)
}

func (m Model) diskMeter(d sysinfo.Disk, width int) string {
	lvl := m.style.level(sysinfo.DiskFinding(d).Level)
	value := fmt.Sprintf("%s %.0f%% %s free", d.Label, d.UsedPercent, compactBytes(float64(d.FreeBytes)))
	if len(value) > width-meterLabelW-2-4 {
		value = fmt.Sprintf("%.0f%%", d.UsedPercent)
	}
	return m.meter("Disk", []segment{{d.UsedPercent / 100, lvl}}, value, nil, width)
}

// coreRows lays out per-core meters in a column of width w, packing several cores
// per row when the column would exceed maxCoreRowsPerColumn rows.
func (m Model) coreRows(cores []sysinfo.CoreUsage, first, w int) []string {
	if len(cores) == 0 {
		return nil
	}
	perRow := min((len(cores)+maxCoreRowsPerColumn-1)/maxCoreRowsPerColumn, 4)
	cw := (w - (perRow - 1)) / perRow
	var out []string
	for i := 0; i < len(cores); i += perRow {
		var parts []string
		for j := i; j < min(i+perRow, len(cores)); j++ {
			parts = append(parts, m.cpuMeter(fmt.Sprint(first+j), cores[j], cw))
		}
		out = append(out, fitWidth(strings.Join(parts, " "), w))
	}
	return out
}

// infoItems are htop's text meters: tasks, load average and uptime.
func (m Model) infoItems() []string {
	h, st := m.snap.Host, m.style
	val := func(s string) string { return st.meterValueBold.Render(s) }
	var out []string
	var tasks []string
	if h.Threads > 0 {
		tasks = append(tasks, val(fmt.Sprint(h.Threads))+" thr, "+val(fmt.Sprint(h.Running))+" running")
	}
	if n := countRunning(m.snap.Services.Rows); n > 0 {
		tasks = append(tasks, val(fmt.Sprint(n))+" containers")
	}
	if len(tasks) > 0 {
		out = append(out, st.meterLabel.Render("Tasks: ")+strings.Join(tasks, st.dim.Render(" · ")))
	}
	if l := h.Info.CPU.Load; len(l) == 3 {
		out = append(out, st.meterLabel.Render("Load average: ")+val(fmt.Sprintf("%.2f %.2f %.2f", l[0], l[1], l[2]))+
			st.dim.Render(fmt.Sprintf(" (%d cores)", h.Info.CPU.Cores)))
	}
	if h.Info.UptimeSec > 0 {
		out = append(out, st.meterLabel.Render("Uptime: ")+val(uptime(time.Duration(h.Info.UptimeSec)*time.Second)))
	}
	return out
}

// uptime formats like htop: "23:53:14" or "46 days, 06:12:03".
func uptime(d time.Duration) string {
	s := int64(d / time.Second)
	days, s := s/86400, s%86400
	hms := fmt.Sprintf("%02d:%02d:%02d", s/3600, s%3600/60, s%60)
	switch days {
	case 0:
		return hms
	case 1:
		return "1 day, " + hms
	}
	return fmt.Sprintf("%d days, %s", days, hms)
}

// meterLines is the htop-style meter block: two columns at standard width (left:
// first half of the cores, Mem, Swp, Disk; right: other cores, tasks, load,
// uptime), one column below that.
func (m Model) meterLines(detail meterDetail) []string {
	info := m.snap.Host.Info
	cores := m.snap.Host.Cores
	if detail != meterFull {
		cores = nil
	}
	twoCol := m.width >= standardWidth
	colW := m.width
	if twoCol {
		colW = (m.width - 2) / 2
	}

	var cpuLeft, cpuRight []string
	switch {
	case len(cores) == 0:
		cpuLeft = []string{m.cpuMeter("CPU", m.aggregateCPU(), colW)}
	case twoCol:
		half := (len(cores) + 1) / 2
		cpuLeft = m.coreRows(cores[:half], 0, colW)
		cpuRight = m.coreRows(cores[half:], half, colW)
	default:
		cpuLeft = m.coreRows(cores, 0, colW)
	}

	var memRows []string
	if mem := info.Memory; mem != nil && mem.TotalBytes > 0 {
		memRows = append(memRows, m.memMeter(mem, colW))
		if detail != meterMinimal {
			memRows = append(memRows, m.swapMeter(mem, colW))
		}
	}
	if detail != meterMinimal {
		for _, d := range info.Disks {
			memRows = append(memRows, m.diskMeter(d, colW))
		}
	}
	items := m.infoItems()

	if !twoCol {
		out := append(cpuLeft, memRows...)
		if len(items) > 0 {
			out = append(out, m.wrapJoin(items, m.style.dim.Render(" · "))...)
		}
		return out
	}
	left := append(cpuLeft, memRows...)
	right := append(cpuRight, items...)
	n := max(len(left), len(right))
	out := make([]string, n)
	for i := range out {
		var l, r string
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out[i] = fitWidth(l, colW) + "  " + r
	}
	return out
}
