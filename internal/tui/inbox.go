package tui

// The inbox (docs/orchestration.md "4. 判断の受信箱"), the stage-1 minimum:
// a toast when an agent asks something, ]a visits the open questions, and
// a modal to answer one. The modal puts what pekoed read itself (facts,
// links) first and the agent's words after, as text only (the daemon
// dropped control characters already; they are dropped here again, and an
// OSC 8 link is made only of an http / https link). The agent's
// recommendation is labelled as its opinion and is never preselected:
// Enter does nothing until a choice is picked.

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"pekoe/internal/mascot"
	"pekoe/internal/state"
	"pekoe/internal/theme"
)

type inboxMsg []state.InboxItem

func (m *model) loadInbox() tea.Cmd {
	return func() tea.Msg {
		if m.cl == nil {
			return nil
		}
		var its []state.InboxItem
		if err := m.cl.Call("inbox.list", map[string]any{"status": "open"}, &its); err != nil {
			return nil // an older pekoed has no inbox
		}
		return inboxMsg(its)
	}
}

// onInboxList takes the open items; the ones there at start are not
// toasted one by one, only counted.
func (m *model) onInboxList(its []state.InboxItem) {
	first := m.inboxToasted == nil
	if first {
		m.inboxToasted = map[int64]bool{}
	}
	m.inbox = nil
	for i := len(its) - 1; i >= 0; i-- { // oldest first
		it := its[i]
		if it.Status != state.InboxOpen {
			continue
		}
		m.inbox = append(m.inbox, it)
		if first {
			m.inboxToasted[it.ID] = true
		}
	}
	if first && len(m.inbox) > 0 {
		m.setMsg(fmt.Sprintf("? %d open in the inbox · ]a to answer", len(m.inbox)), false)
	}
}

// onInboxEvent keeps m.inbox to the open items and toasts a new one once.
func (m *model) onInboxEvent(data json.RawMessage) tea.Cmd {
	var it state.InboxItem
	if json.Unmarshal(data, &it) != nil || it.ID == 0 {
		return nil
	}
	if m.inboxToasted == nil {
		m.inboxToasted = map[int64]bool{}
	}
	i := m.inboxIndex(it.ID)
	if it.Status == state.InboxOpen {
		if i >= 0 {
			if it.Rev >= m.inbox[i].Rev {
				m.inbox[i] = it
			}
		} else {
			m.inbox = append(m.inbox, it)
		}
		if !m.inboxToasted[it.ID] {
			m.inboxToasted[it.ID] = true
			text := "? " + it.From + " asks: " + clip(inboxText(firstLine(it.Question)), 40) + " · ]a"
			if inboxHoldsUp(it) && !m.quietNotify() {
				m.waitToast("", mascot.Wait, text) // an agent waits on it (notify.go)
			} else {
				m.setMsg(text, false)
			}
		}
	} else if i >= 0 {
		m.inbox = append(m.inbox[:i], m.inbox[i+1:]...)
	}
	if im, ok := m.modal.(*inboxModal); ok && im.it.ID == it.ID {
		switch {
		case it.Status != state.InboxOpen && !im.sending:
			m.modal = nil
			m.setMsg(fmt.Sprintf("inbox #%d was %s elsewhere", it.ID, it.Status), false)
		case it.Status == state.InboxOpen && it.Rev > im.it.Rev:
			im.it = it
			if im.opened {
				im.opened = false // the read on opening: what the person sees first
			} else {
				im.changed, im.sel = true, -1 // choose again, on what is there now
			}
		case it.Status == state.InboxOpen:
			im.opened = false
		}
	}
	return nil
}

func (m *model) inboxIndex(id int64) int {
	for i := range m.inbox {
		if m.inbox[i].ID == id {
			return i
		}
	}
	return -1
}

// nextInbox is the oldest open question not visited yet this round (fyi
// never waits for anyone).
func (m *model) nextInbox() *state.InboxItem {
	for i := range m.inbox {
		if it := &m.inbox[i]; it.Kind != "fyi" && !m.inboxVisited[it.ID] {
			return it
		}
	}
	return nil
}

// openInbox shows the answer modal for it.
func (m *model) openInbox(it state.InboxItem) tea.Cmd {
	if m.inboxVisited == nil {
		m.inboxVisited = map[int64]bool{}
	}
	m.inboxVisited[it.ID] = true
	m.typing = false
	im := &inboxModal{m: m, it: it, sel: -1, note: &input{}, opened: true}
	m.modal = im
	return m.openInboxRefresh(im) // the facts as of now (pekoed reads them again)
}

// inboxRoundReset starts the ]a round over: every open question is due
// again.
func (m *model) inboxRoundReset() {
	m.inboxVisited = map[int64]bool{}
	m.attnRound = map[string]bool{}
}

// inboxText is agent text made safe to draw: no escape or control
// characters, no invisible format characters (bidi overrides).
func inboxText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, cleanText(s))
}

// webLink is u as an OSC 8 link when it is an http / https URL with
// nothing odd in it; anything else is plain text.
func webLink(u string) string {
	u = inboxText(u)
	if !(strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")) || strings.ContainsAny(u, " \n\"<>") {
		return u
	}
	return "\x1b]8;;" + u + "\x1b\\" + u + "\x1b]8;;\x1b\\"
}

// ---- the modal ----

type inboxModal struct {
	m       *model
	it      state.InboxItem
	sel     int // chosen option; -1 none (the recommendation is not a default)
	note    *input
	noting  bool // typing the note
	dismiss bool // x pressed: y to dismiss
	changed bool // the item changed while open (facts moved)
	opened  bool // the read on opening has not come back yet
	sending bool
	err     string
}

type inboxAnswerMsg struct {
	id  int64
	err error
}

func (im *inboxModal) mode() (string, string) { return "INBOX", im.m.ic.Wait }

func (im *inboxModal) hints() [][2]string {
	if im.noting {
		return [][2]string{{"↵", "done"}, {"esc", "cancel note"}}
	}
	if im.dismiss {
		return [][2]string{{"y", "dismiss"}, {"n", "keep"}}
	}
	keys := [][2]string{}
	if len(im.it.Options) > 0 {
		keys = append(keys, [2]string{fmt.Sprintf("1-%d", len(im.it.Options)), "choose"})
	}
	return append(keys, [][2]string{{"↵", "answer"}, {"n", "note"}, {"o", "open link"}, {"g", "go to session"}, {"x", "dismiss"}, {"esc", "later"}}...)
}

func (im *inboxModal) view() []string {
	m, th, it := im.m, im.m.th, im.it
	width := m.modalWidth(min(m.width*60/100, 100))
	cw := width - 6
	title := th.FG(theme.Warn, theme.Prefix(m.ic.Wait)) + th.Bold(theme.Primary, fmt.Sprintf("Inbox #%d", it.ID)) +
		th.FG(theme.Dim, " · "+it.Kind+" · from "+it.From)
	if it.Claimed != "" {
		title += " " + th.FG(theme.Warn, theme.Prefix(m.ic.Warning)+"claims to be "+clip(inboxText(it.Claimed), 20))
	}
	label := func(k string) string { return th.FG(theme.Dim, fmt.Sprintf("  %-9s", k)) }
	lines := []string{"", th.Bold(theme.Text, "pekoe read")}
	if f := it.Facts; f != nil {
		if f.Gone {
			lines = append(lines, label("session")+th.FG(theme.Warn, f.Slug+" is gone"))
		} else {
			lines = append(lines, label("session")+f.Slug+th.FG(theme.Dim, " · "+string(f.State)+" · "+f.Branch))
			head := "-"
			if f.Head != "" {
				head = f.Head[:min(9, len(f.Head))]
			}
			lines = append(lines, label("HEAD")+head+th.FG(theme.Dim, fmt.Sprintf(" · +%d −%d · %d files", f.Diffstat.Added, f.Diffstat.Deleted, f.Diffstat.Files)))
			if f.PR > 0 {
				pr := fmt.Sprintf("#%d", f.PR)
				if ps := f.PRStatus; ps != nil {
					pr += th.FG(theme.Dim, fmt.Sprintf(" · %s · CI %d pass %d fail %d pending", ps.State, ps.Pass, ps.Fail, ps.Pending))
					if ps.ReviewDecision != "" {
						pr += th.FG(theme.Dim, " · "+ps.ReviewDecision)
					}
					if ps.MergeState != "" {
						pr += th.FG(theme.Dim, " · "+ps.MergeState)
					}
				}
				lines = append(lines, label("PR")+pr)
				if f.PRURL != "" {
					lines = append(lines, label("")+webLink(f.PRURL))
				}
			}
			if f.Test.Status != "" {
				lines = append(lines, label("test")+f.Test.Status)
			}
			for _, o := range f.Overlaps {
				what := "overlaps"
				if o.Conflict {
					what = th.FG(theme.Err, "conflicts")
				}
				lines = append(lines, label("forecast")+what+" with "+o.Slug)
			}
			if f.Report != nil {
				lines = append(lines, label("report")+fmt.Sprintf("#%d %s", f.Report.ID, f.Report.Status))
			}
			if f.GitBlocked != "" {
				lines = append(lines, label("git")+th.FG(theme.Warn, ".git changed: git stopped"))
			}
		}
	} else {
		lines = append(lines, th.FG(theme.Dim, "  (not about a session)"))
	}
	for _, l := range it.Links {
		lines = append(lines, label("link")+webLink(l))
	}
	if im.changed {
		lines = append(lines, "", th.FG(theme.Warn, "  changed while you looked: check the facts again"))
	}

	lines = append(lines, "\x00rule", th.FG(theme.Dim, "the agent says (its words, not pekoe's)"))
	for _, l := range wrapText(inboxText(it.Question), cw) {
		lines = append(lines, th.FG(theme.Text, l))
	}
	if it.Context != "" {
		ctx := wrapText(inboxText(it.Context), cw-2)
		room := max(m.height-len(lines)-len(it.Options)-14, 2)
		if len(ctx) > room {
			ctx = append(ctx[:room], "…  (pekoe inbox "+fmt.Sprint(it.ID)+" shows all)")
		}
		lines = append(lines, "")
		for _, l := range ctx {
			lines = append(lines, th.FG(theme.Dim, "  "+l))
		}
	}
	if len(it.Options) > 0 {
		lines = append(lines, "")
		for i, o := range it.Options {
			mark := "  "
			if i == im.sel {
				mark = th.Bold(theme.Primary, "› ")
			}
			row := mark + th.Bold(theme.Secondary, fmt.Sprint(i+1)) + "  " + clip(inboxText(o), cw-30)
			if it.Recommend != nil && *it.Recommend == i {
				row += "  " + th.FG(theme.Dim, "推奨(エージェント)")
			}
			lines = append(lines, row)
		}
	}
	lines = append(lines, "", th.FG(theme.Dim, "note ")+im.note.render(th, im.noting))
	switch {
	case im.dismiss:
		lines = append(lines, "", th.FG(theme.Warn, "dismiss without an answer? the agent is told it was closed (y / n)"))
	case im.sending:
		lines = append(lines, "", th.FG(theme.Dim, "answering…"))
	case im.err != "":
		lines = append(lines, "", th.FG(theme.Err, im.err))
	}
	return m.modalFrame(width, title, lines, im.hints(), false)
}

func (im *inboxModal) key(k tea.KeyMsg) tea.Cmd {
	m := im.m
	key := k.String()
	if im.sending {
		return nil
	}
	if im.noting {
		switch key {
		case "enter":
			im.noting = false
		case "esc":
			im.noting = false
			im.note = &input{}
		default:
			im.note.key(k)
		}
		return nil
	}
	if im.dismiss {
		switch key {
		case "y":
			im.dismiss = false
			return im.send("inbox.dismiss", map[string]any{"id": im.it.ID, "note": im.note.String()})
		default:
			im.dismiss = false
		}
		return nil
	}
	switch key {
	case "esc", "q":
		return done(nil)
	case "j", "down":
		if len(im.it.Options) > 0 {
			im.sel = min(im.sel+1, len(im.it.Options)-1)
		}
	case "k", "up":
		if im.sel > 0 {
			im.sel--
		}
	case "n":
		im.noting = true
	case "x":
		im.dismiss = true
	case "o":
		u := ""
		if f := im.it.Facts; f != nil && f.PRURL != "" {
			u = f.PRURL
		}
		for _, l := range im.it.Links {
			if u == "" && (strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "http://")) {
				u = l
			}
		}
		if u == "" {
			im.err = "no http(s) link to open"
			return nil
		}
		return func() tea.Msg {
			if err := openURL(u); err != nil {
				return errMsg{err}
			}
			return infoMsg("opened " + u)
		}
	case "g":
		slug := im.it.About
		if slug == "" {
			slug = im.it.From
		}
		if _, s := m.find(slug); s != nil {
			return done(m.selectSlug(slug))
		}
		im.err = slug + " is not a session in the list"
	case "enter":
		p := map[string]any{"id": im.it.ID, "rev": im.it.Rev, "note": im.note.String()}
		if len(im.it.Options) > 0 {
			if im.sel < 0 {
				im.err = fmt.Sprintf("choose first (1-%d); nothing is chosen for you", len(im.it.Options))
				return nil
			}
			p["choice"] = im.sel
		} else if strings.TrimSpace(im.note.String()) == "" {
			im.err = "no options: write the answer as a note (n)"
			return nil
		}
		return im.send("inbox.answer", p)
	default:
		if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
			if n := int(key[0] - '1'); n < len(im.it.Options) {
				im.sel = n
				im.err = ""
			}
		}
	}
	return nil
}

func (im *inboxModal) send(method string, p map[string]any) tea.Cmd {
	m, id := im.m, im.it.ID
	im.sending, im.err = true, ""
	return func() tea.Msg {
		if m.cl == nil {
			return inboxAnswerMsg{id: id}
		}
		return inboxAnswerMsg{id: id, err: m.cl.Call(method, p, nil)}
	}
}

// onInboxAnswer: answered, or refused (the item changed: look again).
func (m *model) onInboxAnswer(msg inboxAnswerMsg) tea.Cmd {
	im, ok := m.modal.(*inboxModal)
	if !ok || im.it.ID != msg.id {
		if msg.err != nil {
			m.setMsg(msg.err.Error(), true)
		}
		return nil
	}
	im.sending = false
	if msg.err != nil {
		im.err = msg.err.Error()
		if strings.Contains(im.err, "look again") {
			im.sel = -1 // read it again before choosing
			return m.openInboxRefresh(im)
		}
		return nil
	}
	m.modal = nil
	m.setMsg(fmt.Sprintf("answered inbox #%d", msg.id), false)
	return nil
}

// openInboxRefresh reads im's item again; it comes back as an
// inbox.updated (onInboxEvent).
func (m *model) openInboxRefresh(im *inboxModal) tea.Cmd {
	id := im.it.ID
	return func() tea.Msg {
		if m.cl == nil {
			return nil
		}
		var cur state.InboxItem
		if err := m.cl.Call("inbox.get", map[string]any{"id": id}, &cur); err != nil {
			return errMsg{err}
		}
		b, _ := json.Marshal(cur)
		return evMsg{Event: "inbox.updated", Data: b}
	}
}
