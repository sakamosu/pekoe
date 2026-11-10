package tui

// :cleanup — the cleanup candidates (daemon/cleanup.go): landed, merged on
// GitHub, stopped for long, with what removing them would lose. space marks,
// D removes the marked ones (or the selected one) after a confirm that
// repeats the warnings; nothing is removed without it.

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"pekoe/internal/daemon"
	"pekoe/internal/theme"
)

// cleanupMsg carries a fresh list; info is what the last removal did.
type cleanupMsg struct {
	list daemon.CleanupList
	info string
	err  error
}

func (m *model) loadCleanup(info string) tea.Cmd {
	return func() tea.Msg {
		var l daemon.CleanupList
		err := m.cl.Call("cleanup.list", map[string]any{}, &l)
		return cleanupMsg{list: l, info: info, err: err}
	}
}

// onCleanup opens the list, or refreshes it when it is open (after D).
func (m *model) onCleanup(msg cleanupMsg) tea.Cmd {
	if msg.err != nil {
		m.setMsg(msg.err.Error(), true)
		return nil
	}
	if msg.info != "" {
		m.setMsg(msg.info, strings.Contains(msg.info, "not removed"))
	}
	if c, ok := m.modal.(*cleanupModal); ok {
		c.set(msg.list)
		return nil
	}
	if m.modal != nil {
		return nil // something else came up meanwhile
	}
	if len(msg.list.Items) == 0 {
		if msg.info == "" {
			m.setMsg("nothing to clean up (stale: stopped for "+shortDur(msg.list.StaleAfter)+")", false)
		}
		return nil
	}
	c := &cleanupModal{m: m}
	c.set(msg.list)
	m.modal = c
	return nil
}

type cleanupModal struct {
	m      *model
	list   daemon.CleanupList
	sel    int
	marked map[string]bool
}

func (c *cleanupModal) set(l daemon.CleanupList) {
	c.list = l
	keep := map[string]bool{}
	for _, it := range l.Items {
		if c.marked[it.Slug] {
			keep[it.Slug] = true
		}
	}
	c.marked = keep
	c.sel = clampi(c.sel, 0, max(len(l.Items)-1, 0))
}

func (c *cleanupModal) mode() (string, string) { return "CLEANUP", c.m.ic.Warning }

func (c *cleanupModal) hints() [][2]string {
	return [][2]string{{"j/k", "move"}, {"space", "mark"}, {"a", "mark landed"}, {"D", "remove"}, {"esc", "close"}}
}

func (c *cleanupModal) view() []string {
	m := c.m
	th := m.th
	width := m.modalWidth(96)
	cw := width - 6
	lines := []string{"", th.FG(theme.Dim, fmt.Sprintf("landed · merged on GitHub · stopped for %s+  —  nothing is removed until you confirm", shortDur(c.list.StaleAfter))), ""}
	if len(c.list.Items) == 0 {
		lines = append(lines, th.FG(theme.Dim, "nothing to clean up"))
	}
	now := time.Now()
	for i, it := range c.list.Items {
		mark := "  "
		if c.marked[it.Slug] {
			mark = th.FG(theme.Secondary, m.ic.Check) + " "
		}
		role := theme.Dim
		switch it.Kind {
		case daemon.CleanupMerged:
			role = theme.Ok
		case daemon.CleanupStale:
			role = theme.Warn
		}
		l := mark + th.FG(role, fit(it.Kind, 6)) + " " + th.FG(theme.Text, fit(it.Branch, 28)) + " " +
			th.FG(theme.Dim, fit(it.State, 8)+" "+fit(sinceAgo(it.Since, now), 4))
		if it.PR > 0 {
			l += " " + th.FG(theme.Dim, fmt.Sprintf("#%d", it.PR))
		}
		if len(it.Warnings) > 0 {
			l += " " + th.FG(theme.Warn, m.ic.Warning+" "+strings.Join(it.Warnings, " · "))
		}
		l = fit(l, cw)
		if i == c.sel {
			l = th.SelectStyle().Render(fit(l, cw))
		}
		lines = append(lines, l)
	}
	return m.modalFrame(width, th.Bold(theme.Primary, "Cleanup"), lines, c.hints(), false)
}

// picked: the marked candidates, or the selected one when none is marked.
func (c *cleanupModal) picked() []daemon.CleanupItem {
	var out []daemon.CleanupItem
	for _, it := range c.list.Items {
		if c.marked[it.Slug] {
			out = append(out, it)
		}
	}
	if len(out) == 0 && c.sel < len(c.list.Items) {
		out = append(out, c.list.Items[c.sel])
	}
	return out
}

func (c *cleanupModal) key(k tea.KeyMsg) tea.Cmd {
	m := c.m
	n := len(c.list.Items)
	switch k.String() {
	case "j", "down":
		c.sel = clampi(c.sel+1, 0, max(n-1, 0))
	case "k", "up":
		c.sel = clampi(c.sel-1, 0, max(n-1, 0))
	case " ", "space":
		if c.sel < n {
			s := c.list.Items[c.sel].Slug
			c.marked[s] = !c.marked[s]
			if !c.marked[s] {
				delete(c.marked, s)
			}
			c.sel = clampi(c.sel+1, 0, max(n-1, 0))
		}
	case "a":
		for _, it := range c.list.Items {
			if it.Kind == daemon.CleanupLanded {
				c.marked[it.Slug] = true
			}
		}
	case "D", "d":
		its := c.picked()
		if len(its) == 0 {
			return nil
		}
		m.modal = c.confirm(its)
	case "esc", "q":
		return done(nil)
	}
	return nil
}

// confirm asks before removing its, with each one's warnings above the
// question; y removes them one by one and reopens the list.
func (c *cleanupModal) confirm(its []daemon.CleanupItem) *confirmModal {
	m := c.m
	deletes := 0
	var notes []confirmNote
	for _, it := range its {
		if it.DeletesWorktree {
			deletes++
		}
		detail := it.Kind
		if it.DeletesWorktree {
			detail += " · deletes " + homeTilde(it.Path)
		}
		role, icon := theme.Dim, m.ic.Check
		if len(it.Warnings) > 0 {
			role, icon = theme.Warn, m.ic.Warning
			detail += " · " + strings.Join(it.Warnings, " · ")
		}
		notes = append(notes, confirmNote{role, icon, it.Slug, detail})
	}
	const most = 6
	if len(notes) > most {
		notes = append(notes[:most-1], confirmNote{theme.Dim, "", "", fmt.Sprintf("+%d more", len(notes)-(most-1))})
	}
	what := "only pekoe's record (no worktree left)"
	if deletes > 0 {
		what = fmt.Sprintf("teardown, then delete %d worktree%s with any uncommitted changes", deletes, plural(deletes))
	}
	subject := fmt.Sprintf("%d sessions", len(its))
	if len(its) == 1 {
		subject = its[0].Slug
	}
	slugs := make([]string, len(its))
	for i, it := range its {
		slugs[i] = it.Slug
	}
	// y closes the confirm; the list opens again with what is left
	return newConfirmModal(m, "Remove", what, "", true, func() tea.Cmd {
		return func() tea.Msg {
			var failed []string
			for _, s := range slugs {
				if err := m.cl.Call("worktree.rm", map[string]any{"slug": s, "force": true}, nil); err != nil {
					failed = append(failed, s+": "+err.Error())
				}
			}
			info := fmt.Sprintf("removed %d", len(slugs)-len(failed))
			if len(failed) > 0 {
				info += " · not removed: " + strings.Join(failed, "; ")
			}
			var l daemon.CleanupList
			err := m.cl.Call("cleanup.list", map[string]any{}, &l)
			return cleanupMsg{list: l, info: info, err: err}
		}
	}).about(subject).note(notes)
}

// shortDur prints "72h0m0s" as "72h".
func shortDur(s string) string {
	d, err := time.ParseDuration(s)
	switch {
	case err != nil:
		return s
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return d.String()
}

// sinceAgo is how long ago t was ("-" when unknown).
func sinceAgo(t, now time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return theme.Ago(now.Sub(t))
}
