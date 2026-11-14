package tui

import (
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Size breakpoints. Zooming or changing the font size changes the terminal's
// cell grid, so every frame is laid out from scratch for the current size.
// Panes are always stacked vertically; width only changes columns and meters.
const (
	minWidth      = 50
	minHeight     = 12
	standardWidth = 100
)

type sizeMode int

const (
	modeTooSmall sizeMode = iota
	modeCompact
	modeStandard
)

func (m sizeMode) String() string {
	return [...]string{"too-small", "compact", "standard"}[m]
}

func sizeModeFor(w, h int) sizeMode {
	switch {
	case w < minWidth || h < minHeight:
		return modeTooSmall
	case w >= standardWidth:
		return modeStandard
	}
	return modeCompact
}

// flexCol describes a table column that can shrink, grow, or be dropped.
type flexCol struct {
	title  string
	min    int
	pref   int
	weight int  // share of leftover width once every column has its preferred width
	prio   int  // lower is dropped first when minimums do not fit; requiredPrio is never dropped
	right  bool // right-align
}

const requiredPrio = 100

// column is a laid-out table column.
type column struct {
	title string
	width int
	right bool
}

// flexColumns fits cols into exactly width cells (one-space separators): low
// priority columns are dropped until the minimums fit, columns grow to their
// preferred width in priority order, and the rest is shared by weight.
func flexColumns(cols []flexCol, width int) []column {
	keep := make([]bool, len(cols))
	for i := range keep {
		keep[i] = true
	}
	need := func() int {
		n, w := 0, 0
		for i, c := range cols {
			if keep[i] {
				n++
				w += c.min
			}
		}
		return w + max(n-1, 0)
	}
	for need() > width {
		drop := -1
		for i, c := range cols {
			if keep[i] && c.prio < requiredPrio && (drop < 0 || c.prio < cols[drop].prio) {
				drop = i
			}
		}
		if drop < 0 {
			break
		}
		keep[drop] = false
	}

	var idx []int
	for i := range cols {
		if keep[i] {
			idx = append(idx, i)
		}
	}
	widths := map[int]int{}
	used := max(len(idx)-1, 0)
	for _, i := range idx {
		widths[i] = cols[i].min
		used += cols[i].min
	}
	rem := width - used

	byPrio := append([]int(nil), idx...)
	sort.SliceStable(byPrio, func(a, b int) bool { return cols[byPrio[a]].prio > cols[byPrio[b]].prio })
	for _, i := range byPrio {
		if rem <= 0 {
			break
		}
		add := min(max(cols[i].pref-widths[i], 0), rem)
		widths[i] += add
		rem -= add
	}
	if rem > 0 {
		total, last := 0, -1
		for _, i := range idx {
			if cols[i].weight > 0 {
				total += cols[i].weight
				last = i
			}
		}
		if last < 0 && len(idx) > 0 {
			last = idx[len(idx)-1]
			total = 0
		}
		given := 0
		if total > 0 {
			for _, i := range idx {
				if cols[i].weight > 0 && i != last {
					share := rem * cols[i].weight / total
					widths[i] += share
					given += share
				}
			}
		}
		if last >= 0 {
			widths[last] += rem - given
		}
	}

	out := make([]column, 0, len(idx))
	for _, i := range idx {
		out = append(out, column{title: cols[i].title, width: widths[i], right: cols[i].right})
	}
	return out
}

// paneNeed is how many rows a pane wants (everything visible) and the fewest it
// can be drawn in (title plus one row of content).
type paneNeed struct {
	min, need int
}

// allocRows splits total rows between panes (in display order), returning counts
// that sum to total. Walking the fill order (pane indexes, focused pane first),
// each pane gets its minimum if it still fits, otherwise 0 rows (hidden); then
// needs are filled in the same order and leftover rows go to the last shown pane
// so the frame always fills the terminal.
func allocRows(total int, panes []paneNeed, order []int) []int {
	out := make([]int, len(panes))
	if len(panes) == 0 {
		return out
	}
	var valid []int
	for _, i := range order {
		if i >= 0 && i < len(panes) {
			valid = append(valid, i)
		}
	}
	left := max(total, 0)
	shown := make([]bool, len(panes))
	for _, i := range valid {
		if panes[i].min <= left {
			out[i] = panes[i].min
			left -= out[i]
			shown[i] = true
		}
	}
	for _, i := range valid {
		if left == 0 {
			break
		}
		if shown[i] {
			g := min(max(panes[i].need-out[i], 0), left)
			out[i] += g
			left -= g
		}
	}
	last := -1
	for i := range panes {
		if shown[i] {
			last = i
		}
	}
	if last < 0 {
		last = len(out) - 1
		if len(valid) > 0 {
			last = valid[0]
		}
	}
	out[last] += left
	return out
}

// pane is one section's content, laid out for a given width.
type pane struct {
	title  string
	fixed  []string // summary lines always shown under the title
	header string   // table header ("" for none)
	body   []string // scrollable rows
	more   func(hidden int) string
	gap    bool // blank line above the title
}

func (p pane) chrome() int {
	n := 1 + len(p.fixed)
	if p.header != "" {
		n++
	}
	if p.gap {
		n++
	}
	return n
}

func (p pane) need() paneNeed {
	c := p.chrome()
	return paneNeed{min: c + min(len(p.body), 1), need: c + len(p.body)}
}

// render draws the pane in exactly rows lines, scrolled to offset.
func (p pane) render(rows, offset int) []string {
	var lines []string
	if p.gap {
		lines = append(lines, "")
	}
	lines = append(lines, p.title)
	lines = append(lines, p.fixed...)
	if p.header != "" {
		lines = append(lines, p.header)
	}
	if room := rows - len(lines); room > 0 && len(p.body) > 0 {
		offset = min(max(offset, 0), max(len(p.body)-1, 0))
		show := min(room, len(p.body)-offset)
		hidden := len(p.body) - offset - show
		if hidden > 0 && show >= 2 && p.more != nil {
			show--
			hidden++
		}
		lines = append(lines, p.body[offset:offset+show]...)
		if hidden > 0 && p.more != nil && len(lines) < rows {
			lines = append(lines, p.more(hidden))
		}
	}
	return fitLines(lines, rows)
}

// fitLines pads with blank lines or cuts to exactly n lines.
func fitLines(lines []string, n int) []string {
	if n <= 0 {
		return nil
	}
	if len(lines) > n {
		return lines[:n]
	}
	for len(lines) < n {
		lines = append(lines, "")
	}
	return lines
}

// fitWidth clips s to w cells and pads it with spaces to exactly w, so a frame
// fully overwrites whatever a previous, larger frame drew.
func fitWidth(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) > w {
		s = ansi.Truncate(s, w, "")
	}
	if gap := w - lipgloss.Width(s); gap > 0 {
		s += strings.Repeat(" ", gap)
	}
	return s
}

// barWidth scales meter bars with the terminal width.
func barWidth(w int) int { return min(max(w*15/100, 10), 40) }
