package tui

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pekoe/internal/paths"
)

// Layout is how the wide (>= 100 columns) screen splits between the left
// column (1-3) and the main panel (4), adjusted with <prefix> z < > = and
// kept in paths.LayoutFile() so a restarted TUI and agents spawned from the
// CLI (agentPaneSize) get the same pane width.
type Layout struct {
	// Left is the left column's share of the terminal width; 0 is the
	// default (leftDefault). A share, not columns, so it follows the
	// terminal when that is resized.
	Left float64 `json:"left,omitempty"`
	// Zoom hides the left column: the main panel takes the full width.
	Zoom bool `json:"zoom,omitempty"`
}

const (
	leftMin      = 34 // the narrowest left column that still reads
	agentMin     = 20 // the narrowest main panel content (its pty width)
	agentChrome  = 4  // the main panel's border and padding around the pty
	leftStep     = 4  // columns per <prefix> < / >
	layoutSettle = 200 * time.Millisecond
)

// leftDefault is the left column when nothing is saved: 40% of the
// terminal, between 34 and 56 columns.
func leftDefault(total int) int { return clampi(total*40/100, leftMin, 56) }

// leftMax is the widest left column that leaves the main panel agentMin.
func leftMax(total int) int { return max(total-1-agentChrome-agentMin, leftMin) }

// leftWidth is the left column's width for a terminal total columns wide.
func (l Layout) leftWidth(total int) int {
	if l.Left <= 0 {
		return leftDefault(total)
	}
	return clampi(int(math.Round(float64(total)*l.Left)), leftMin, leftMax(total))
}

// resized moves the left column's edge by delta columns.
func (l Layout) resized(total, delta int) Layout {
	cols := clampi(l.leftWidth(total)+delta, leftMin, leftMax(total))
	l.Left = float64(cols) / float64(total)
	l.Zoom = false
	return l
}

// agentWidth is the main panel's pty width.
func (l Layout) agentWidth(total int) int {
	if narrow(total) || l.Zoom {
		return max(total-agentChrome, agentMin)
	}
	return max(total-l.leftWidth(total)-1-agentChrome, agentMin)
}

// LoadLayout reads the saved layout; a missing or broken file is the default.
func LoadLayout() Layout {
	var l Layout
	b, err := os.ReadFile(paths.LayoutFile())
	if err != nil || json.Unmarshal(b, &l) != nil || l.Left < 0 || l.Left >= 1 {
		return Layout{}
	}
	return l
}

// saveLayout writes l to path atomically (a CLI may read it at any time).
func saveLayout(path string, l Layout) error {
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// prefixLabel is the prefix key as the bar and the help show it: ^q.
func (m *model) prefixLabel() string {
	p := m.g.Keys.Prefix
	if p == "" {
		p = "ctrl+q"
	}
	return strings.ReplaceAll(p, "ctrl+", "^")
}

func (m *model) leftWidth() int { return m.layout.leftWidth(m.width) }

// zoomed: the main panel takes the full width (never below 100 columns,
// where the panels stack anyway).
func (m *model) zoomed() bool { return m.layout.Zoom && !narrow(m.width) }

// layoutSettledMsg: no layout key for layoutSettle; seq names the last one.
type layoutSettledMsg struct{ seq int }

// layoutKey handles <prefix> z < > =; ok is false for any other key.
// The view follows at once; the ptys and the saved file only once the keys
// settle (layoutSettled), so holding < down resizes tmux once, not per repeat.
func (m *model) layoutKey(key string) (tea.Cmd, bool) {
	if key != "z" && key != "<" && key != ">" && key != "=" {
		return nil, false
	}
	if narrow(m.width) { // stacked panels: nothing to resize; the saved layout stays
		return func() tea.Msg { return infoMsg("too narrow to resize the panels") }, true
	}
	l := m.layout
	switch key {
	case "z":
		l.Zoom = !l.Zoom
	case "<":
		l = l.resized(m.width, -leftStep)
	case ">":
		l = l.resized(m.width, leftStep)
	case "=":
		l = Layout{}
	}
	m.layout = l
	if l.Zoom {
		m.focus = pAgent // the left column is hidden
	}
	m.layoutSeq++
	seq := m.layoutSeq
	return tea.Tick(layoutSettle, func(time.Time) tea.Msg { return layoutSettledMsg{seq} }), true
}

// layoutPending: a layout change waits for layoutSettled; until then the
// ptys keep their old size on purpose (fitScreen must not chase each step).
func (m *model) layoutPending() bool { return m.layoutSeq != m.layoutDone }

func (m *model) layoutSettled(seq int) tea.Cmd {
	if seq != m.layoutSeq {
		return nil // a newer key restarted the wait
	}
	m.layoutDone = seq
	l, path := m.layout, m.layoutPath
	var save tea.Cmd
	if path != "" {
		save = func() tea.Msg {
			if err := saveLayout(path, l); err != nil {
				return errMsg{err}
			}
			return nil
		}
	}
	if m.cl == nil {
		return save
	}
	return tea.Batch(save, m.resizeAll(), m.fetchScreen(), m.fetchChat())
}

// unzoomIfLeft: focusing a left panel (1-3, tab, <prefix> h, a finished
// session) brings the hidden left column back.
func (m *model) unzoomIfLeft() tea.Cmd {
	if !m.layout.Zoom || m.focus == pAgent || narrow(m.width) {
		return nil
	}
	m.layout.Zoom = false
	m.layoutSeq++
	seq := m.layoutSeq
	return tea.Tick(layoutSettle, func(time.Time) tea.Msg { return layoutSettledMsg{seq} })
}
