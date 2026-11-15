package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/qabas1/qabasone-cli/internal/sysinfo"
	"github.com/qabas1/qabasone-cli/internal/top"
)

type styles struct {
	title, paneTitle, paneFocus, header, dim, ok, warn, crit, bold lipgloss.Style

	// htop meter colors.
	meterLabel, meterValue, meterValueBold lipgloss.Style
	cpuUser, cpuNice, cpuSystem, cpuSteal  lipgloss.Style
	memUsed, memBuffers, memCache, swap    lipgloss.Style
}

func newStyles(r *lipgloss.Renderer) styles {
	return styles{
		meterLabel:     r.NewStyle().Foreground(lipgloss.Color("6")),
		meterValue:     r.NewStyle().Foreground(lipgloss.Color("8")),
		meterValueBold: r.NewStyle().Bold(true).Foreground(lipgloss.Color("6")),
		cpuUser:        r.NewStyle().Foreground(lipgloss.Color("2")),
		cpuNice:        r.NewStyle().Foreground(lipgloss.Color("4")),
		cpuSystem:      r.NewStyle().Foreground(lipgloss.Color("1")),
		cpuSteal:       r.NewStyle().Foreground(lipgloss.Color("6")),
		memUsed:        r.NewStyle().Foreground(lipgloss.Color("2")),
		memBuffers:     r.NewStyle().Foreground(lipgloss.Color("4")),
		memCache:       r.NewStyle().Foreground(lipgloss.Color("3")),
		swap:           r.NewStyle().Foreground(lipgloss.Color("1")),

		title:     r.NewStyle().Bold(true).Foreground(lipgloss.Color("14")),
		paneTitle: r.NewStyle().Bold(true),
		paneFocus: r.NewStyle().Bold(true).Reverse(true),
		header:    r.NewStyle().Bold(true).Underline(true),
		dim:       r.NewStyle().Faint(true),
		ok:        r.NewStyle().Foreground(lipgloss.Color("10")),
		warn:      r.NewStyle().Foreground(lipgloss.Color("11")),
		crit:      r.NewStyle().Foreground(lipgloss.Color("9")).Bold(true),
		bold:      r.NewStyle().Bold(true),
	}
}

func (s styles) level(l string) lipgloss.Style {
	switch l {
	case sysinfo.LevelCrit:
		return s.crit
	case sysinfo.LevelWarn:
		return s.warn
	}
	return s.ok
}

// fillOrder is which panes get spare rows first (after the focused pane).
var fillOrder = []Pane{PaneServices, PaneFindings, PanePostgres, PaneReplicas}

// View renders exactly width x height cells for the current terminal size.
func (m Model) View() string {
	if !m.have {
		return m.frame([]string{m.style.title.Render("qabas top") + "  collecting QabasOne stats…"})
	}
	mode := sizeModeFor(m.width, m.height)
	if mode == modeTooSmall {
		return m.tooSmall()
	}
	foot := m.footerLines(mode)
	var head []string
	for _, d := range []meterDetail{meterFull, meterAggregate, meterMinimal} {
		head = m.headerLines(d)
		if m.height-len(head)-len(foot) >= minBodyRows {
			break
		}
	}
	for len(head) > 1 && m.height-len(head)-len(foot) < minBodyRows {
		head = head[:len(head)-1]
	}
	bodyH := max(m.height-len(head)-len(foot), 0)
	lines := append(append(head, m.stackBody(bodyH)...), foot...)
	return m.frame(lines)
}

// minBodyRows is the fewest pane rows the header may leave; past that the meters
// get less detailed and then header lines are dropped.
const minBodyRows = 6

// frame pads or cuts to exactly height lines of exactly width cells.
func (m Model) frame(lines []string) string {
	lines = fitLines(lines, m.height)
	for i, l := range lines {
		lines[i] = fitWidth(l, m.width)
	}
	return strings.Join(lines, "\n")
}

func (m Model) tooSmall() string {
	msg := []string{
		m.style.warn.Render(fmt.Sprintf("terminal too small (%dx%d)", m.width, m.height)),
		fmt.Sprintf("enlarge or zoom out (need %dx%d)", minWidth, minHeight),
		m.style.dim.Render("q quit"),
	}
	lines := make([]string, max((m.height-len(msg))/2, 0), m.height)
	for _, l := range msg {
		pad := max((m.width-lipgloss.Width(l))/2, 0)
		lines = append(lines, strings.Repeat(" ", pad)+l)
	}
	return m.frame(lines)
}

func (m Model) focusFirst(order []Pane, panes []Pane) []int {
	pos := map[Pane]int{}
	for i, p := range panes {
		pos[p] = i
	}
	var out []int
	if i, ok := pos[m.focus]; ok {
		out = append(out, i)
	}
	for _, p := range order {
		if i, ok := pos[p]; ok && p != m.focus {
			out = append(out, i)
		}
	}
	return out
}

// renderPanes lays out panes (top to bottom) in exactly rows lines of width w.
func (m Model) renderPanes(ids []Pane, w, rows int) []string {
	panes := make([]pane, len(ids))
	needs := make([]paneNeed, len(ids))
	for i, id := range ids {
		panes[i] = m.buildPane(id, w)
		panes[i].gap = true
		needs[i] = panes[i].need()
	}
	alloc := allocRows(rows, needs, m.focusFirst(fillOrder, ids))
	var out []string
	for i, p := range panes {
		out = append(out, p.render(alloc[i], m.scroll[ids[i]])...)
	}
	return fitLines(out, rows)
}

// stackBody draws the panes one under another at full width.
func (m Model) stackBody(rows int) []string {
	return m.renderPanes([]Pane{PaneServices, PanePostgres, PaneReplicas, PaneFindings}, m.width, rows)
}

func (m Model) buildPane(p Pane, w int) pane {
	switch p {
	case PaneServices:
		return m.servicesPane(w)
	case PanePostgres:
		return m.postgresPane(w)
	case PaneReplicas:
		return m.replicasPane(w)
	}
	return m.findingsPane(w)
}

func (m Model) headerLines(detail meterDetail) []string {
	s, st := m.snap, m.style
	info := s.Host.Info
	sep := st.dim.Render(" · ")
	parts := []string{st.title.Render("qabas top")}
	if info.Hostname != "" {
		parts = append(parts, st.bold.Render(info.Hostname))
	}
	switch {
	case s.State.Role != "":
		parts = append(parts, "QabasOne "+s.State.Role)
	case s.State.NotInstalled():
		parts = append(parts, st.warn.Render("QabasOne not installed"))
	default:
		rel := "QabasOne " + dash(s.State.Release)
		if s.State.Release != "" && s.State.Channel != "" {
			rel += " (" + s.State.Channel + ")"
		}
		parts = append(parts, rel)
	}
	parts = append(parts, s.CollectedAt.Format("2006-01-02 15:04:05"), "every "+m.interval.String())
	if m.paused {
		parts = append(parts, st.warn.Render("[PAUSED]"))
	}
	if m.collecting {
		parts = append(parts, st.dim.Render("refreshing…"))
	}
	out := m.wrapJoin(parts, sep)
	out = append(out, m.meterLines(detail)...)
	if s.Host.Err != "" {
		out = append(out, st.warn.Render("host: "+s.Host.Err))
	}
	return append(out, m.stateLines()...)
}

// installHint is shown wherever QabasOne-specific data is missing because it is not installed.
const installHint = "QabasOne is not installed on this machine · install: sudo qabas install"

func (m Model) stateLines() []string {
	s, st := m.snap.State, m.style
	if s.NotInstalled() {
		return m.wrapJoin([]string{st.warn.Render(installHint)}, "")
	}
	now := m.snap.CollectedAt
	maint := st.ok.Render("off")
	if s.Maintenance {
		maint = st.warn.Render("ON")
	}
	lock := st.ok.Render("free")
	if s.Lock != "" {
		lock = st.warn.Render(s.Lock)
		if s.LockStuck {
			lock = st.crit.Render(s.Lock + " (stuck)")
		}
	}
	parts := []string{"Maintenance " + maint, "Lock " + lock}
	if !s.LastBackup.IsZero() {
		parts = append(parts, "Last backup "+ago(now, s.LastBackup))
	}
	if !s.NextBackup.IsZero() {
		parts = append(parts, "Next backup "+until(now, s.NextBackup))
	}
	if s.AutopilotStatus != "" {
		ap := s.AutopilotStatus
		if !s.AutopilotLast.IsZero() {
			ap += ", last " + ago(now, s.AutopilotLast)
		}
		parts = append(parts, "Autopilot "+ap)
	}
	if s.Err != "" {
		parts = append(parts, st.warn.Render("state: "+s.Err))
	}
	return m.wrapJoin(parts, st.dim.Render(" · "))
}

// wrapJoin joins parts with sep, starting a new line instead of overflowing the width.
func (m Model) wrapJoin(parts []string, sep string) []string {
	var lines []string
	cur := ""
	for _, p := range parts {
		switch {
		case cur == "":
			cur = p
		case m.width > 0 && lipgloss.Width(cur+sep+p) > m.width:
			lines = append(lines, cur)
			cur = "  " + p
		default:
			cur += sep + p
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

func (m Model) paneTitle(p Pane, extra string) string {
	title := fmt.Sprintf(" %d %s ", int(p)+1, p)
	style := m.style.paneTitle
	if m.focus == p {
		style = m.style.paneFocus
	}
	if extra != "" {
		return style.Render(title) + " " + m.style.dim.Render(extra)
	}
	return style.Render(title)
}

func (m Model) moreLine(hint string) func(int) string {
	return func(n int) string {
		return m.style.dim.Render(fmt.Sprintf("… %d more%s", n, hint))
	}
}

func (m Model) row(cols []column, cells []string, styleFor func(title string) *lipgloss.Style) string {
	var b strings.Builder
	for i, c := range cols {
		if i > 0 {
			b.WriteString(" ")
		}
		cell := pad(cells[i], c.width, c.right)
		if styleFor != nil {
			if s := styleFor(c.title); s != nil {
				cell = s.Render(cell)
			}
		}
		b.WriteString(cell)
	}
	return b.String()
}

func (m Model) headerRow(cols []column) string {
	var b strings.Builder
	for i, c := range cols {
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString(pad(c.title, c.width, c.right))
	}
	return m.style.header.Render(b.String())
}

// cellsFor picks the cells of the columns that survived layout, by title.
func cellsFor(cols []column, all map[string]string) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = all[c.title]
	}
	return out
}

func (m Model) servicesPane(w int) pane {
	rows := m.visibleServices()
	extra := fmt.Sprintf("%d running · sort: %s", countRunning(m.snap.Services.Rows), m.sortKey)
	if m.reverse {
		extra += " (reversed)"
	}
	if m.filter != "" || m.filtering {
		extra += fmt.Sprintf(" · filter: %q (%d shown)", m.filter, len(rows))
	}
	if m.snap.Services.Source == top.SourceCLI {
		extra += " · rounded rates"
	}
	p := pane{title: m.paneTitle(PaneServices, extra), more: m.moreLine(" (↓ to scroll)")}
	if m.snap.State.NotInstalled() {
		p.title = m.paneTitle(PaneServices, "not installed")
		if err := m.snap.Services.Err; strings.Contains(strings.ToLower(err), "docker") {
			p.fixed = append(p.fixed, m.style.warn.Render(err))
		}
		p.body = []string{m.style.dim.Render(installHint)}
		return p
	}
	if m.snap.Services.Err != "" {
		p.fixed = append(p.fixed, m.style.warn.Render(m.snap.Services.Err))
	}
	if len(m.snap.Services.Rows) == 0 {
		if m.snap.Services.Err == "" {
			p.body = []string{m.style.dim.Render("no QabasOne containers running (start: sudo qabas up)")}
		}
		return p
	}
	longest := 7
	for _, r := range m.snap.Services.Rows {
		longest = max(longest, lipgloss.Width(r.Service))
	}
	cols := flexColumns([]flexCol{
		{title: "SERVICE", min: 10, pref: min(longest, 28), weight: 2, prio: requiredPrio},
		{title: "STATE", min: 7, pref: 10, prio: 90},
		{title: "HEALTH", min: 7, pref: 9, prio: 60},
		{title: "CPU%", min: 5, pref: 6, prio: 95, right: true},
		{title: "MEM / LIMIT", min: 11, pref: 13, weight: 1, prio: 85, right: true},
		{title: "MEM%", min: 5, pref: 6, prio: 80, right: true},
		{title: "NET RX/TX /s", min: 9, pref: 13, prio: 30, right: true},
		{title: "I/O R/W /s", min: 9, pref: 13, prio: 20, right: true},
		{title: "RST", min: 3, pref: 3, prio: 50, right: true},
		{title: "UPTIME", min: 6, pref: 7, prio: 10, right: true},
	}, w)
	p.header = m.headerRow(cols)
	for _, r := range rows {
		mem := "-"
		if r.MemBytes > 0 {
			mem = compactBytes(float64(r.MemBytes))
			if r.MemLimitBytes > 0 {
				mem += " / " + compactBytes(float64(r.MemLimitBytes))
			}
		}
		up := "-"
		if !r.StartedAt.IsZero() {
			up = top.ShortDuration(m.snap.CollectedAt.Sub(r.StartedAt))
		}
		cells := cellsFor(cols, map[string]string{
			"SERVICE": r.Service, "STATE": dash(r.State), "HEALTH": dash(r.Health),
			"CPU%": fmt.Sprintf("%.1f", r.CPUPercent), "MEM / LIMIT": mem, "MEM%": fmt.Sprintf("%.1f", r.MemPercent),
			"NET RX/TX /s": compactBytes(r.NetRxPerSec) + "/" + compactBytes(r.NetTxPerSec),
			"I/O R/W /s":   compactBytes(r.BlockRPerSec) + "/" + compactBytes(r.BlockWPerSec),
			"RST":          fmt.Sprintf("%d", r.Restarts), "UPTIME": up,
		})
		p.body = append(p.body, m.row(cols, cells, func(title string) *lipgloss.Style { return m.serviceCellStyle(title, r) }))
	}
	return p
}

func (m Model) serviceCellStyle(col string, r top.Service) *lipgloss.Style {
	var s lipgloss.Style
	switch col {
	case "STATE":
		s = m.style.ok
		if !strings.EqualFold(r.State, "running") {
			s = m.style.crit
		}
	case "HEALTH":
		switch strings.ToLower(r.Health) {
		case "healthy":
			s = m.style.ok
		case "unhealthy":
			s = m.style.crit
		case "starting":
			s = m.style.warn
		default:
			return nil
		}
	case "CPU%":
		if r.CPUPercent < top.ServiceCPUWarnPercent/2 {
			return nil
		}
		s = m.style.warn
	case "MEM%":
		if r.MemPercent < top.ServiceMemWarnPercent {
			return nil
		}
		s = m.style.warn
	case "RST":
		if r.Restarts < top.RestartWarnCount {
			return nil
		}
		s = m.style.warn
	default:
		return nil
	}
	return &s
}

// missingReason explains an empty database pane: the user's flag, or nothing installed.
func (m Model) missingReason(byFlag bool, flag string) string {
	switch {
	case byFlag:
		return "disabled (" + flag + ")"
	case m.snap.State.NotInstalled():
		return "not installed"
	}
	return "unavailable"
}

func (m Model) postgresPane(w int) pane {
	pg := m.snap.Postgres
	p := pane{title: m.paneTitle(PanePostgres, ""), more: m.moreLine("")}
	if pg == nil {
		p.title = m.paneTitle(PanePostgres, m.missingReason(m.snap.PostgresDisabled, "--no-postgres"))
		return p
	}
	if pg.Err != "" {
		p.fixed = []string{m.style.warn.Render("unavailable: " + pg.Err)}
		return p
	}
	role := "primary"
	if pg.InRecovery {
		role = "standby"
	}
	connPct := 0.0
	if pg.MaxConnections > 0 {
		connPct = float64(pg.Connections) / float64(pg.MaxConnections) * 100
	}
	connLvl := sysinfo.LevelOK
	switch {
	case connPct >= top.ConnCritPercent:
		connLvl = sysinfo.LevelCrit
	case connPct >= top.ConnWarnPercent:
		connLvl = sysinfo.LevelWarn
	}
	locks := fmt.Sprintf("lock waits %d", pg.LockWaits)
	if pg.LockWaits > 0 {
		locks = m.style.warn.Render(locks)
	}
	sum := []string{
		role,
		fmt.Sprintf("conns %s %d/%d", m.bar(connPct, connLvl, max(barWidth(w)/2, 6)), pg.Connections, pg.MaxConnections),
		fmt.Sprintf("active %d", pg.Active),
		fmt.Sprintf("TPS %.1f", pg.TPS),
		fmt.Sprintf("cache hit %.1f%%", pg.CacheHitPercent),
		locks,
		fmt.Sprintf("deadlocks %d", pg.Deadlocks),
		m.archiveText(pg),
	}
	p.fixed = wrapParts(sum, m.style.dim.Render(" · "), w)
	if len(pg.Queries) == 0 {
		p.body = []string{m.style.dim.Render("no running queries")}
		return p
	}
	cols := flexColumns([]flexCol{
		{title: "PID", min: 5, pref: 7, prio: 90, right: true},
		{title: "USER", min: 4, pref: 12, prio: 40},
		{title: "STATE", min: 6, pref: 10, prio: 70},
		{title: "WAIT", min: 6, pref: 16, prio: 30},
		{title: "TIME", min: 5, pref: 8, prio: 95, right: true},
		{title: "QUERY", min: 10, pref: 10, weight: 1, prio: requiredPrio},
	}, w)
	p.header = m.headerRow(cols)
	for _, q := range pg.Queries {
		d := time.Duration(q.DurationSec * float64(time.Second))
		cells := cellsFor(cols, map[string]string{
			"PID": fmt.Sprintf("%d", q.PID), "USER": dash(q.User), "STATE": dash(q.State),
			"WAIT": dash(q.Wait), "TIME": top.ShortDuration(d), "QUERY": q.Text,
		})
		p.body = append(p.body, m.row(cols, cells, func(title string) *lipgloss.Style {
			if title == "TIME" && q.State == "active" && d >= top.LongQueryWarn {
				return &m.style.warn
			}
			return nil
		}))
	}
	return p
}

// archiveText prefers the PITR archive stream (pg_receivewal); pg_stat_archiver only
// matters when archive_mode is on.
func (m Model) archiveText(pg *top.PostgresSection) string {
	if rs := m.snap.Replicas; rs != nil && rs.Archive != nil {
		a := rs.Archive
		switch {
		case a.Slot != "" && !a.SlotActive:
			return m.style.crit.Render("WAL archive slot inactive")
		case !a.Connected && !a.SlotActive:
			return m.style.warn.Render("WAL archive not connected")
		}
		s := "WAL archive streaming (pg_receivewal), " + sysinfo.HumanBytes(uint64(a.LagBytes)) + " behind"
		if time.Duration(a.LagSeconds*float64(time.Second)) >= top.ArchiveLagWarn || a.LagBytes >= top.ArchiveLagWarnBytes {
			return m.style.warn.Render(s)
		}
		return s
	}
	switch {
	case strings.EqualFold(pg.ArchiveMode, "off"):
		return "WAL archive off"
	case !pg.LastFailedAt.IsZero() && pg.LastFailedAt.After(pg.LastArchivedAt):
		return m.style.crit.Render("WAL archive failing (" + ago(m.snap.CollectedAt, pg.LastFailedAt) + ")")
	case pg.ArchivedCount == 0:
		return "WAL archive idle"
	}
	return "WAL archive ok, last " + ago(m.snap.CollectedAt, pg.LastArchivedAt)
}

func (m Model) replicasPane(w int) pane {
	rs := m.snap.Replicas
	p := pane{title: m.paneTitle(PaneReplicas, ""), more: m.moreLine("")}
	if rs == nil {
		p.title = m.paneTitle(PaneReplicas, m.missingReason(m.snap.ReplicasDisabled, "--no-replicas"))
		return p
	}
	if rs.Err != "" {
		p.fixed = []string{m.style.warn.Render("unavailable: " + rs.Err)}
		return p
	}
	if rs.Standby {
		lag := top.ShortDuration(time.Duration(rs.LagSeconds * float64(time.Second)))
		if rs.LagSeconds < 1 {
			lag = "in sync"
		}
		p.title = m.paneTitle(PaneReplicas, "this node is a standby")
		p.fixed = []string{fmt.Sprintf("replay lag %s · WAL receiver %s", lag, dash(rs.Upstream))}
		return p
	}
	p.title = m.paneTitle(PaneReplicas, fmt.Sprintf("%d standby(s)", len(rs.Rows)))
	if len(rs.Rows) == 0 {
		p.body = []string{m.style.dim.Render("no standbys attached (add one: qabas guide replica)")}
		return p
	}
	cols := flexColumns([]flexCol{
		{title: "NAME", min: 6, pref: 14, weight: 1, prio: requiredPrio},
		{title: "ADDRESS", min: 7, pref: 16, prio: 30},
		{title: "STATE", min: 9, pref: 12, prio: 95},
		{title: "SYNC", min: 4, pref: 6, prio: 20},
		{title: "LAG", min: 4, pref: 8, prio: 90, right: true},
		{title: "BEHIND", min: 7, pref: 10, prio: 60, right: true},
		{title: "SLOT WAL", min: 7, pref: 10, prio: 50, right: true},
		{title: "WAL STATUS", min: 6, pref: 10, prio: 40},
	}, w)
	p.header = m.headerRow(cols)
	for _, r := range rs.Rows {
		lag := top.ShortDuration(time.Duration(r.LagSeconds * float64(time.Second)))
		if r.LagSeconds < 1 {
			lag = "0s"
		}
		cells := cellsFor(cols, map[string]string{
			"NAME": r.Name, "ADDRESS": dash(r.ClientAddr), "STATE": r.State, "SYNC": dash(r.SyncState), "LAG": lag,
			"BEHIND":     sysinfo.HumanBytes(uint64(max(r.LagBytes, 0))),
			"SLOT WAL":   sysinfo.HumanBytes(uint64(max(r.RetainedBytes, 0))),
			"WAL STATUS": dash(r.WALStatus),
		})
		p.body = append(p.body, m.row(cols, cells, func(title string) *lipgloss.Style {
			switch title {
			case "STATE":
				if r.State == "streaming" {
					return &m.style.ok
				}
				return &m.style.crit
			case "WAL STATUS":
				if r.WALStatus == "lost" {
					return &m.style.crit
				}
			}
			return nil
		}))
	}
	return p
}

func (m Model) findingsPane(w int) pane {
	fs := m.snap.Findings
	crit, warn := 0, 0
	longest := 5
	for _, f := range fs {
		switch f.Level {
		case sysinfo.LevelCrit:
			crit++
		case sysinfo.LevelWarn:
			warn++
		}
		longest = max(longest, lipgloss.Width(f.Check))
	}
	p := pane{title: m.paneTitle(PaneFindings, fmt.Sprintf("%d critical · %d warning", crit, warn)), more: m.moreLine(" (press 4, then ↓)")}
	if len(fs) == 0 {
		p.body = []string{m.style.ok.Render("all checks ok")}
		return p
	}
	cols := flexColumns([]flexCol{
		{title: "LEVEL", min: 4, pref: 4, prio: requiredPrio},
		{title: "CHECK", min: 8, pref: min(longest, 28), prio: 90},
		{title: "DETAIL", min: 10, pref: 10, weight: 1, prio: requiredPrio},
	}, w)
	for _, f := range fs {
		cells := cellsFor(cols, map[string]string{"LEVEL": strings.ToUpper(f.Level), "CHECK": f.Check, "DETAIL": f.Detail})
		p.body = append(p.body, m.row(cols, cells, func(title string) *lipgloss.Style {
			switch title {
			case "LEVEL":
				s := m.style.level(f.Level)
				return &s
			case "CHECK":
				return &m.style.bold
			}
			return nil
		}))
	}
	return p
}

func (m Model) footerLines(mode sizeMode) []string {
	if m.filtering {
		return []string{m.style.bold.Render("Filter: ") + m.filter + "█" + m.style.dim.Render("  enter apply · esc clear")}
	}
	if m.showHelp {
		return strings.Split(m.help.FullHelpView(m.keys.FullHelp()), "\n")
	}
	if mode == modeCompact {
		return []string{m.help.ShortHelpView([]key.Binding{m.keys.Focus, m.keys.Help, m.keys.Quit})}
	}
	return []string{m.help.ShortHelpView(m.keys.ShortHelp())}
}

func (m Model) bar(pct float64, level string, width int) string {
	pct = min(max(pct, 0), 100)
	fill := int(pct/100*float64(width) + 0.5)
	return "[" + m.style.level(level).Render(strings.Repeat("|", fill)) + strings.Repeat(" ", width-fill) + "]"
}

// wrapParts joins parts with sep, wrapping onto new lines within width w.
func wrapParts(parts []string, sep string, w int) []string {
	var lines []string
	cur := ""
	for _, p := range parts {
		switch {
		case cur == "":
			cur = p
		case lipgloss.Width(cur+sep+p) > w:
			lines = append(lines, cur)
			cur = "  " + p
		default:
			cur += sep + p
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

func cpuLevel(pct float64) string {
	switch {
	case pct >= 95:
		return sysinfo.LevelCrit
	case pct >= 80:
		return sysinfo.LevelWarn
	}
	return sysinfo.LevelOK
}

func countRunning(rows []top.Service) int {
	n := 0
	for _, r := range rows {
		if strings.EqualFold(r.State, "running") {
			n++
		}
	}
	return n
}

// pad fits s into exactly w cells, truncating with an ellipsis when too long.
func pad(s string, w int, right bool) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	gap := strings.Repeat(" ", max(w-lipgloss.Width(s), 0))
	if right {
		return gap + s
	}
	return s + gap
}

// compactBytes renders sizes in at most ~6 cells ("812K", "1.2G").
func compactBytes(b float64) string {
	if b < 1 {
		return "0"
	}
	units := []string{"B", "K", "M", "G", "T"}
	i := 0
	for b >= 1024 && i < len(units)-1 {
		b /= 1024
		i++
	}
	if b >= 100 || i == 0 {
		return fmt.Sprintf("%.0f%s", b, units[i])
	}
	return fmt.Sprintf("%.1f%s", b, units[i])
}

func ago(now, t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	if d < time.Second {
		return "just now"
	}
	return top.ShortDuration(d) + " ago"
}

func until(now, t time.Time) string {
	d := t.Sub(now)
	if d <= 0 {
		return "due"
	}
	return "in " + top.ShortDuration(d)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
