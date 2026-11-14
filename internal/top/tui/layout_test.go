package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestSizeModeFor(t *testing.T) {
	cases := []struct {
		w, h int
		want sizeMode
	}{
		{49, 40, modeTooSmall},
		{120, 11, modeTooSmall},
		{50, 12, modeCompact},
		{99, 60, modeCompact},
		{100, 20, modeStandard},
		{179, 50, modeStandard},
		{300, 90, modeStandard},
	}
	for _, c := range cases {
		if got := sizeModeFor(c.w, c.h); got != c.want {
			t.Errorf("%dx%d: want %s, got %s", c.w, c.h, c.want, got)
		}
	}
}

func serviceCols() []flexCol {
	return []flexCol{
		{title: "SERVICE", min: 10, pref: 20, weight: 2, prio: requiredPrio},
		{title: "STATE", min: 7, pref: 10, prio: 90},
		{title: "HEALTH", min: 7, pref: 9, prio: 60},
		{title: "CPU%", min: 5, pref: 6, prio: 95},
		{title: "MEM / LIMIT", min: 11, pref: 13, weight: 1, prio: 85},
		{title: "MEM%", min: 5, pref: 6, prio: 80},
		{title: "NET", min: 9, pref: 13, prio: 30},
		{title: "I/O", min: 9, pref: 13, prio: 20},
		{title: "RST", min: 3, pref: 3, prio: 50},
		{title: "UPTIME", min: 6, pref: 7, prio: 10},
	}
}

func titles(cols []column) string {
	var out []string
	for _, c := range cols {
		out = append(out, c.title)
	}
	return strings.Join(out, ",")
}

func TestFlexColumnsFillsWidthExactly(t *testing.T) {
	for w := 50; w <= 260; w++ {
		cols := flexColumns(serviceCols(), w)
		total := len(cols) - 1
		for _, c := range cols {
			total += c.width
		}
		if total != w {
			t.Fatalf("width %d: columns use %d (%s)", w, total, titles(cols))
		}
	}
}

func TestFlexColumnsDropOrder(t *testing.T) {
	all := serviceCols()
	minAll := len(all) - 1
	for _, c := range all {
		minAll += c.min
	}
	want := []string{
		"SERVICE,STATE,HEALTH,CPU%,MEM / LIMIT,MEM%,NET,I/O,RST,UPTIME",
		"SERVICE,STATE,HEALTH,CPU%,MEM / LIMIT,MEM%,NET,I/O,RST",
		"SERVICE,STATE,HEALTH,CPU%,MEM / LIMIT,MEM%,NET,RST",
		"SERVICE,STATE,HEALTH,CPU%,MEM / LIMIT,MEM%,RST",
		"SERVICE,STATE,HEALTH,CPU%,MEM / LIMIT,MEM%",
		"SERVICE,STATE,CPU%,MEM / LIMIT,MEM%",
	}
	// Each step removes the lowest priority column: UPTIME, I/O, NET, RST, HEALTH.
	w := minAll
	for i, dropped := range []int{0, 7, 10, 10, 4, 8} {
		w -= dropped
		if got := titles(flexColumns(all, w)); got != want[i] {
			t.Errorf("width %d: want %s, got %s", w, want[i], got)
		}
	}
	if got := titles(flexColumns(all, 5)); got != "SERVICE" {
		t.Errorf("required columns must survive: %s", got)
	}
}

func TestFlexColumnsWideGivesLeftoverByWeight(t *testing.T) {
	cols := flexColumns(serviceCols(), 200)
	if cols[0].width <= 20 || cols[4].width <= 13 {
		t.Fatalf("weighted columns did not grow: %+v", cols)
	}
	if cols[1].width != 10 || cols[9].width != 7 {
		t.Fatalf("unweighted columns must stop at preferred width: %+v", cols)
	}
}

func TestAllocRows(t *testing.T) {
	panes := []paneNeed{{min: 3, need: 10}, {min: 2, need: 6}, {min: 2, need: 3}, {min: 2, need: 8}}
	sum := func(xs []int) (n int) {
		for _, x := range xs {
			n += x
		}
		return
	}
	for total := 0; total <= 40; total++ {
		got := allocRows(total, panes, []int{0, 3, 1, 2})
		if sum(got) != total {
			t.Fatalf("total %d: rows %v", total, got)
		}
	}
	got := allocRows(9, panes, []int{0, 3, 1, 2})
	if want := []int{3, 2, 2, 2}; !equal(got, want) {
		t.Fatalf("minimums first: want %v, got %v", want, got)
	}
	got = allocRows(20, panes, []int{0, 3, 1, 2})
	if want := []int{10, 2, 2, 6}; !equal(got, want) {
		t.Fatalf("fill order: want %v, got %v", want, got)
	}
	got = allocRows(20, panes, []int{3, 0, 1, 2})
	if want := []int{8, 2, 2, 8}; !equal(got, want) {
		t.Fatalf("focused pane first: want %v, got %v", want, got)
	}
	got = allocRows(6, panes, []int{0, 3, 1, 2})
	if want := []int{4, 0, 0, 2}; !equal(got, want) {
		t.Fatalf("panes that cannot get their minimum are hidden: want %v, got %v", want, got)
	}
	got = allocRows(6, panes, []int{1, 0, 3, 2})
	if want := []int{3, 3, 0, 0}; !equal(got, want) {
		t.Fatalf("the focused pane is never starved: want %v, got %v", want, got)
	}
	got = allocRows(50, panes, []int{0, 1, 2, 3})
	if want := []int{10, 6, 3, 31}; !equal(got, want) {
		t.Fatalf("leftover to the last pane: want %v, got %v", want, got)
	}
}

func equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestFitWidth(t *testing.T) {
	if got := fitWidth("abcdef", 4); got != "abcd" {
		t.Fatalf("clip: %q", got)
	}
	if got := fitWidth("ab", 4); got != "ab  " || lipgloss.Width(got) != 4 {
		t.Fatalf("pad: %q", got)
	}
	if barWidth(40) != 10 || barWidth(200) != 30 || barWidth(400) != 40 {
		t.Fatal("barWidth")
	}
}
