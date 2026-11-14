// Package tui renders `qabas top` as an interactive, read-only Bubble Tea dashboard.
package tui

import (
	"context"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/qabasone/qabasone-cli/internal/top"
)

const (
	MinInterval = time.Second
	MaxInterval = time.Minute
)

// Pane identifies a focusable section.
type Pane int

const (
	PaneServices Pane = iota
	PanePostgres
	PaneReplicas
	PaneFindings
	paneCount
)

func (p Pane) String() string {
	return [...]string{"Services", "Postgres", "Replicas", "Findings"}[p]
}

type keyMap struct {
	Focus    key.Binding
	Jump     key.Binding
	Sort     key.Binding
	Reverse  key.Binding
	Filter   key.Binding
	Up       key.Binding
	Down     key.Binding
	Faster   key.Binding
	Slower   key.Binding
	Pause    key.Binding
	Refresh  key.Binding
	Help     key.Binding
	Quit     key.Binding
	Escape   key.Binding
	Confirm  key.Binding
	Backward key.Binding
}

func defaultKeys() keyMap {
	return keyMap{
		Focus:    key.NewBinding(key.WithKeys("tab", "shift+tab"), key.WithHelp("tab", "next pane")),
		Jump:     key.NewBinding(key.WithKeys("1", "2", "3", "4"), key.WithHelp("1-4", "jump to pane")),
		Sort:     key.NewBinding(key.WithKeys("s", "f6"), key.WithHelp("s/F6", "sort")),
		Reverse:  key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reverse")),
		Filter:   key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "scroll up")),
		Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "scroll down")),
		Faster:   key.NewBinding(key.WithKeys("+", "="), key.WithHelp("+", "faster")),
		Slower:   key.NewBinding(key.WithKeys("-", "_"), key.WithHelp("-", "slower")),
		Pause:    key.NewBinding(key.WithKeys("p", " "), key.WithHelp("p", "pause")),
		Refresh:  key.NewBinding(key.WithKeys("ctrl+r", "f5"), key.WithHelp("F5", "refresh now")),
		Help:     key.NewBinding(key.WithKeys("?", "f1"), key.WithHelp("?", "help")),
		Quit:     key.NewBinding(key.WithKeys("q", "ctrl+c", "f10"), key.WithHelp("q", "quit")),
		Escape:   key.NewBinding(key.WithKeys("esc")),
		Confirm:  key.NewBinding(key.WithKeys("enter")),
		Backward: key.NewBinding(key.WithKeys("backspace")),
	}
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Focus, k.Sort, k.Filter, k.Pause, k.Help, k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Focus, k.Jump, k.Up, k.Down},
		{k.Sort, k.Reverse, k.Filter},
		{k.Faster, k.Slower, k.Pause, k.Refresh},
		{k.Help, k.Quit},
	}
}

// Options configure a dashboard.
type Options struct {
	Collect  func(ctx context.Context) top.Snapshot
	Interval time.Duration
	Sort     SortKey
	// Renderer controls color output (nil = stdout with NO_COLOR honored by the caller).
	Renderer *lipgloss.Renderer
	// Width and Height are the terminal size at startup (0 = 100x30 until the
	// first resize message).
	Width, Height int
	// SizeFunc reports the visible terminal size. When set it is authoritative:
	// resize messages are re-checked against it (Windows reports the console
	// buffer, which can include scrollback).
	SizeFunc func() (w, h int, ok bool)
	// PollSize re-checks SizeFunc at this interval (0 = off). Needed where zoom
	// or font changes do not raise a resize event (Windows consoles).
	PollSize time.Duration
}

// Model is the Bubble Tea model for the dashboard.
type Model struct {
	collect  func(ctx context.Context) top.Snapshot
	interval time.Duration

	snap       top.Snapshot
	have       bool
	collecting bool
	gen        int

	width, height int
	focus         Pane
	scroll        [paneCount]int
	sortKey       SortKey
	reverse       bool
	filter        string
	filtering     bool
	paused        bool
	showHelp      bool

	sizeFunc func() (int, int, bool)
	pollSize time.Duration

	keys  keyMap
	help  help.Model
	style styles
}

func New(o Options) Model {
	iv := clampInterval(o.Interval)
	r := o.Renderer
	if r == nil {
		r = lipgloss.DefaultRenderer()
	}
	h := help.New()
	h.Styles.ShortKey = r.NewStyle().Bold(true)
	h.Styles.FullKey = r.NewStyle().Bold(true)
	h.Styles.ShortDesc = r.NewStyle().Faint(true)
	h.Styles.FullDesc = r.NewStyle().Faint(true)
	h.Styles.ShortSeparator = r.NewStyle().Faint(true)
	h.Styles.FullSeparator = r.NewStyle().Faint(true)
	m := Model{
		collect:    o.Collect,
		interval:   iv,
		collecting: true,
		sortKey:    o.Sort,
		width:      100,
		height:     30,
		sizeFunc:   o.SizeFunc,
		pollSize:   o.PollSize,
		keys:       defaultKeys(),
		help:       h,
		style:      newStyles(r),
	}
	if o.Width > 0 && o.Height > 0 {
		m.width, m.height = o.Width, o.Height
	}
	m.help.Width = m.width
	return m
}

type sizePollMsg struct{}

func (m Model) pollCmd() tea.Cmd {
	if m.pollSize <= 0 || m.sizeFunc == nil {
		return nil
	}
	return tea.Tick(m.pollSize, func(time.Time) tea.Msg { return sizePollMsg{} })
}

// resize applies a new terminal size; the frame is re-laid out on the next View.
func (m *Model) resize(w, h int) {
	if w <= 0 || h <= 0 {
		return
	}
	m.width, m.height = w, h
	m.help.Width = w
	m.clampScroll()
}

type snapMsg struct {
	snap top.Snapshot
	gen  int
}

type tickMsg struct{ gen int }

func (m Model) Init() tea.Cmd { return tea.Batch(m.collectCmd(), m.pollCmd()) }

func (m Model) collectCmd() tea.Cmd {
	collect, gen := m.collect, m.gen
	return func() tea.Msg {
		return snapMsg{snap: collect(context.Background()), gen: gen}
	}
}

func (m Model) tickCmd() tea.Cmd {
	gen := m.gen
	return tea.Tick(m.interval, func(time.Time) tea.Msg { return tickMsg{gen: gen} })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w, h := msg.Width, msg.Height
		if m.sizeFunc != nil {
			if sw, sh, ok := m.sizeFunc(); ok {
				w, h = sw, sh
			}
		}
		m.resize(w, h)
		return m, nil
	case sizePollMsg:
		if w, h, ok := m.sizeFunc(); ok && (w != m.width || h != m.height) {
			m.resize(w, h)
		}
		return m, m.pollCmd()
	case snapMsg:
		m.snap, m.have, m.collecting = msg.snap, true, false
		m.clampScroll()
		if m.paused {
			return m, nil
		}
		if msg.gen != m.gen {
			// A refresh was requested while this collection was in flight.
			m.collecting = true
			return m, m.collectCmd()
		}
		return m, m.tickCmd()
	case tickMsg:
		if msg.gen != m.gen || m.paused || m.collecting {
			return m, nil
		}
		m.collecting = true
		return m, m.collectCmd()
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.filtering {
		switch {
		case key.Matches(msg, m.keys.Escape):
			m.filtering, m.filter = false, ""
		case key.Matches(msg, m.keys.Confirm):
			m.filtering = false
		case key.Matches(msg, m.keys.Backward):
			if r := []rune(m.filter); len(r) > 0 {
				m.filter = string(r[:len(r)-1])
			}
		case msg.String() == "ctrl+c":
			return m, tea.Quit
		case msg.Type == tea.KeySpace:
			m.filter += " "
		case msg.Type == tea.KeyRunes:
			m.filter += string(msg.Runes)
		}
		m.scroll[PaneServices] = 0
		return m, nil
	}
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, m.keys.Escape):
		m.showHelp = false
		m.filter = ""
	case key.Matches(msg, m.keys.Help):
		m.showHelp = !m.showHelp
	case key.Matches(msg, m.keys.Focus):
		if msg.String() == "shift+tab" {
			m.focus = (m.focus + paneCount - 1) % paneCount
		} else {
			m.focus = (m.focus + 1) % paneCount
		}
	case key.Matches(msg, m.keys.Jump):
		m.focus = Pane(msg.Runes[0] - '1')
	case key.Matches(msg, m.keys.Sort):
		m.sortKey = m.sortKey.Next()
	case key.Matches(msg, m.keys.Reverse):
		m.reverse = !m.reverse
	case key.Matches(msg, m.keys.Filter):
		m.filtering, m.focus = true, PaneServices
	case key.Matches(msg, m.keys.Up):
		if m.scroll[m.focus] > 0 {
			m.scroll[m.focus]--
		}
	case key.Matches(msg, m.keys.Down):
		m.scroll[m.focus]++
		m.clampScroll()
	case key.Matches(msg, m.keys.Faster):
		return m.setInterval(m.interval / 2)
	case key.Matches(msg, m.keys.Slower):
		return m.setInterval(m.interval * 2)
	case key.Matches(msg, m.keys.Pause):
		m.paused = !m.paused
		if !m.paused {
			return m.restartLoop()
		}
	case key.Matches(msg, m.keys.Refresh):
		return m.restartLoop()
	}
	return m, nil
}

func (m Model) setInterval(d time.Duration) (tea.Model, tea.Cmd) {
	m.interval = clampInterval(d)
	if m.paused {
		return m, nil
	}
	return m.restartLoop()
}

// restartLoop invalidates pending ticks and refreshes now.
func (m Model) restartLoop() (tea.Model, tea.Cmd) {
	m.gen++
	if m.collecting {
		return m, nil
	}
	m.collecting = true
	return m, m.collectCmd()
}

func (m *Model) clampScroll() {
	for p := Pane(0); p < paneCount; p++ {
		if n := m.paneLen(p); m.scroll[p] >= n {
			m.scroll[p] = max(n-1, 0)
		}
	}
}

func (m Model) paneLen(p Pane) int {
	switch p {
	case PaneServices:
		return len(m.visibleServices())
	case PanePostgres:
		if m.snap.Postgres != nil {
			return len(m.snap.Postgres.Queries)
		}
	case PaneReplicas:
		if m.snap.Replicas != nil {
			return len(m.snap.Replicas.Rows)
		}
	case PaneFindings:
		return len(m.snap.Findings)
	}
	return 0
}

func (m Model) visibleServices() []top.Service {
	return SortServices(FilterServices(m.snap.Services.Rows, m.filter), m.sortKey, m.reverse)
}

func clampInterval(d time.Duration) time.Duration {
	switch {
	case d <= 0:
		return 2 * time.Second
	case d < MinInterval:
		return MinInterval
	case d > MaxInterval:
		return MaxInterval
	}
	return d
}

// Run starts the dashboard in the alternate screen and blocks until quit.
func Run(o Options) error {
	_, err := tea.NewProgram(New(o), tea.WithAltScreen()).Run()
	return err
}
