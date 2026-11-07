package tui

// Conflict forecast in the TUI. bobad computes it (daemon/conflicts.go) and
// ships it on each Session as Overlaps / PendingTool.AlsoIn; this file only
// draws it: ⚠N in the session list, the peer per file in Changes, notes
// above the land question.

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"boba/internal/state"
	"boba/internal/theme"
)

func overlapRole(o state.Overlap) theme.Role {
	if o.Conflict {
		return theme.Err
	}
	return theme.Warn
}

// conflictTag is the "⚠2" next to a branch in the session list: how many
// open sessions touch the same files. err when any of them would conflict.
func (m *model) conflictTag(s *state.Session) string {
	if len(s.Overlaps) == 0 {
		return ""
	}
	role := theme.Warn
	for _, o := range s.Overlaps {
		if o.Conflict {
			role = theme.Err
		}
	}
	return m.th.FG(role, fmt.Sprintf("%s%d", m.ic.Warning, len(s.Overlaps)))
}

// filePeers maps each path of the session's forecast to the sessions that
// also touch it; a file conflicting with a peer is listed under err.
func filePeers(s *state.Session) map[string][]state.Overlap {
	if s == nil || len(s.Overlaps) == 0 {
		return nil
	}
	out := map[string][]state.Overlap{}
	for _, o := range s.Overlaps {
		conflicting := map[string]bool{}
		for _, f := range o.ConflictFiles {
			conflicting[f] = true
		}
		for _, f := range o.Files {
			out[f] = append(out[f], state.Overlap{Slug: o.Slug, Conflict: conflicting[f]})
		}
	}
	return out
}

// changeLine is one row of Changes: status, path and, when other sessions
// touch the file too, ⚠ and their slugs. The path gives way first.
func (m *model) changeLine(c change, peers []state.Overlap, cw int) string {
	th := m.th
	role := theme.Warn
	switch c.Status {
	case "A":
		role = theme.Ok
	case "D":
		role = theme.Err
	}
	tag := ""
	if len(peers) > 0 {
		pr := theme.Warn
		var slugs []string
		for _, p := range peers {
			if p.Conflict {
				pr = theme.Err
			}
			slugs = append(slugs, p.Slug)
		}
		tag = " " + th.FG(pr, m.ic.Warning) + " " + th.FG(theme.Dim, strings.Join(slugs, ","))
	}
	pathW := cw - 2
	if tw := w(tag); tw < pathW-6 {
		pathW -= tw
	} else {
		tag = ""
	}
	path := c.Path
	if w(path) > pathW {
		path = fit(path, pathW)
	}
	return th.FG(role, c.Status) + " " + path + tag
}

// changesFooter sums up the forecast under Changes: one ⚠ per peer, with the
// state word (conflict / overlap) so it reads without color too.
func (m *model) changesFooter(s *state.Session) string {
	if s == nil || len(s.Overlaps) == 0 {
		return ""
	}
	var parts []string
	for _, o := range s.Overlaps {
		word := "overlap"
		if o.Conflict {
			word = "conflict"
		}
		parts = append(parts, m.th.FG(overlapRole(o), m.ic.Warning)+m.th.FG(theme.Dim, " "+o.Slug+" "+word))
	}
	return strings.Join(parts, "   ")
}

// confirmNote is one line above a confirm question (land's forecast).
type confirmNote struct {
	role   theme.Role
	icon   string
	text   string // plain: the subject (a slug)
	detail string // dim
}

// landNotes forecasts what landing s means for the other open sessions.
func (m *model) landNotes(s *state.Session) []confirmNote {
	var notes []confirmNote
	for _, o := range s.Overlaps {
		if o.Conflict {
			files := o.ConflictFiles
			if len(files) == 0 {
				files = o.Files
			}
			notes = append(notes, confirmNote{theme.Err, m.ic.Warning, o.Slug, "conflict · " + strings.Join(files, ", ")})
		} else {
			notes = append(notes, confirmNote{theme.Warn, m.ic.Warning, o.Slug, "overlap, auto-merges · " + strings.Join(o.Files, ", ")})
		}
	}
	const most = 3
	if len(notes) > most {
		rest := len(notes) - (most - 1)
		notes = append(notes[:most-1], confirmNote{theme.Dim, "", "", fmt.Sprintf("+%d more sessions · boba conflicts", rest)})
	}
	if len(notes) == 0 {
		open := 0
		for _, x := range m.sessions {
			if x.Slug != s.Slug && x.RepoID == s.RepoID && x.State != state.Landed && !x.Stopped {
				open++
			}
		}
		if open > 0 {
			notes = append(notes, confirmNote{theme.Ok, m.ic.Check, "", fmt.Sprintf("no overlap with %d open session%s", open, plural(open))})
		}
	}
	return notes
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// noteLines renders a confirm's notes to sit right above boba's ask line,
// on the same select band so they read as one block.
func (m *model) noteLines(c *confirmModal, width int) []string {
	th := m.th
	var out []string
	for _, n := range c.notes {
		line := "  "
		if n.icon != "" {
			line += th.FG(n.role, n.icon) + " "
		}
		if n.text != "" {
			line += th.FG(theme.Text, n.text) + " "
		}
		line += th.FG(theme.Dim, n.detail)
		line = fit(line, width)
		if th.Mode == theme.Truecolor {
			line = lipgloss.NewStyle().Background(th.Color(theme.Select)).Render(strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+bgSeq(th, theme.Select)))
		}
		out = append(out, line)
	}
	return out
}
