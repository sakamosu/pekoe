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
	"time"
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
	m.toastMerge(it)
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
			m.queueInboxPopup(it)
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
			im.it, im.confirm = it, false // a merge confirmed on what was there before: choose again
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

// toastMerge says how pekoed's merge on a person's "merge" goes: under
// way, merged, or not merged and why (once per state).
func (m *model) toastMerge(it state.InboxItem) {
	a := it.Answer
	if a == nil || a.Merge == nil || it.From != "pekoed" {
		return
	}
	r := a.Merge
	if m.mergeToasted == nil {
		m.mergeToasted = map[int64]string{}
	}
	if m.mergeToasted[it.ID] == r.State {
		return
	}
	m.mergeToasted[it.ID] = r.State
	switch r.State {
	case state.MergeRunning:
		m.setMsg(fmt.Sprintf("merging PR #%d (%s) · checking again first", r.PR, it.About), false)
	case state.MergeMerged:
		m.setMsg(fmt.Sprintf("✓ pekoed merged PR #%d (%s)", r.PR, it.About), false)
	case state.MergeFailed:
		m.setMsg(fmt.Sprintf("✗ PR #%d (%s) %s", r.PR, it.About, inboxText(r.Detail)), true)
	}
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
	// popup: put in front on its own (inbox_popup.go), not opened with ]a.
	// sel -1 is then "skip", keys before shownAt+popupGrace are dropped
	popup   bool
	shownAt time.Time
	selAt   time.Time // when sel last changed (a popup's Enter waits a beat after it)
	// confirm: "merge" was chosen and Enter pressed once (mergeChoice):
	// only another Enter, popupConfirm after confirmAt, answers; any other
	// key takes it back
	confirm   bool
	confirmAt time.Time
}

// mergeChoice: answering sel to it makes pekoed merge a PR (the daemon's
// mergeAsked: pekoed's own approve item whose option 0 reads "merge"). It
// cannot be taken back, so Enter asks again.
func mergeChoice(it state.InboxItem, sel int) bool {
	return it.From == "pekoed" && it.Kind == "approve" && sel == 0 && len(it.Options) > 0 && it.Options[0] == "merge"
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
	if im.confirm {
		return [][2]string{{"↵", "merge"}, {"other keys", "cancel"}}
	}
	keys := [][2]string{}
	if len(im.it.Options) > 0 {
		keys = append(keys, [2]string{fmt.Sprintf("1-%d", len(im.it.Options)), "choose"})
	}
	if im.popup { // other keys go back to skip: link, dismiss from ]a
		keys = append(keys, [][2]string{{"↵", "answer / skip"}, {"esc", "skip"}}...)
		if im.sel >= 0 {
			keys = append(keys, [2]string{"n", "note"})
		}
		return append(keys, [2]string{"]a", "link · dismiss (after skip)"})
	}
	return append(keys, [][2]string{{"↵", "answer"}, {"n", "note"}, {"o", "open link"}, {"g", "go to session"}, {"x", "dismiss"}, {"esc", "later"}}...)
}

func (im *inboxModal) view() []string {
	m, th, it := im.m, im.m.th, im.it
	width := m.modalWidth(min(m.width*60/100, 100))
	cw := width - 6
	title := th.FG(theme.Warn, theme.Prefix(m.ic.Wait)) + th.Bold(theme.Primary, fmt.Sprintf("Inbox #%d", it.ID)) +
		th.FG(theme.Dim, " · "+it.Kind+" · from "+it.From)
	if im.popup {
		title += th.FG(theme.Warn, " · new")
	}
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
	if len(it.Options) > 0 || im.popup {
		lines = append(lines, "")
		if im.popup { // first and chosen: Enter right away skips
			mark := "  "
			if im.sel < 0 {
				mark = th.Bold(theme.Primary, "› ")
			}
			lines = append(lines, mark+th.Bold(theme.Secondary, "-")+"  "+th.FG(theme.Dim, "skip · answer later from the inbox (]a)"))
		}
		for i, o := range it.Options {
			mark := "  "
			if i == im.sel {
				mark = th.Bold(theme.Primary, "› ")
			}
			row := mark + th.Bold(theme.Secondary, fmt.Sprint(i+1)) + "  " + clip(inboxText(o), cw-30)
			if it.Recommend != nil && *it.Recommend == i {
				row += "  " + th.FG(theme.Dim, "recommended (agent)")
			}
			lines = append(lines, row)
		}
	}
	lines = append(lines, "", th.FG(theme.Dim, "note ")+im.note.render(th, im.noting))
	switch {
	case im.dismiss:
		lines = append(lines, "", th.FG(theme.Warn, "dismiss without an answer? the agent is told it was closed (y / n)"))
	case im.confirm:
		pr := ""
		if f := it.Facts; f != nil && f.PR > 0 {
			pr = fmt.Sprintf(" PR #%d", f.PR)
		}
		lines = append(lines, "", th.Bold(theme.Warn, "  merge"+pr+"? pekoed merges it now: ↵ again to merge · any other key cancels"))
	case im.sending:
		lines = append(lines, "", th.FG(theme.Dim, "answering…"))
	case im.err != "":
		lines = append(lines, "", th.FG(theme.Err, im.err))
	case im.popup && im.grace(time.Now()):
		lines = append(lines, "", th.FG(theme.Dim, "a new question · keys wait a moment"))
	}
	return m.modalFrame(width, title, lines, im.hints(), false)
}

func (im *inboxModal) key(k tea.KeyMsg) tea.Cmd {
	m := im.m
	key := k.String()
	if im.sending {
		return nil
	}
	if im.popup && im.grace(time.Now()) {
		return nil // a key typed for something else must not answer it
	}
	if im.confirm {
		if key != "enter" {
			im.unconfirm()
			return nil
		}
		if time.Since(im.confirmAt) < popupConfirm {
			im.confirmAt = time.Now() // Enter mashed on: wait for a pause
			return nil
		}
		im.confirm = false
		return im.send("inbox.answer", map[string]any{"id": im.it.ID, "rev": im.it.Rev, "note": im.note.String(), "choice": im.sel})
	}
	if im.popup && !im.noting && !popupKey(key, len(im.it.Options), im.sel) {
		// typing meant for somewhere else: back to skip, wait again
		im.sel, im.shownAt = -1, time.Now()
		return nil
	}
	if im.noting {
		switch key {
		case "enter":
			im.noting = false
			im.selAt = time.Now() // a popup's Enter to answer waits a beat after it
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
		if im.popup {
			return im.skip()
		}
		return done(nil)
	case "j", "down":
		if len(im.it.Options) > 0 {
			im.sel = min(im.sel+1, len(im.it.Options)-1)
			im.selAt = time.Now()
		}
	case "k", "up":
		if im.sel > 0 || im.popup && im.sel == 0 { // up to skip
			im.sel--
			im.selAt = time.Now()
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
		if im.popup && im.sel < 0 && (len(im.it.Options) > 0 || strings.TrimSpace(im.note.String()) == "") {
			return im.skip()
		}
		if im.popup && time.Since(im.selAt) < popupConfirm {
			// "1⏎" typed in one go was meant for an agent's own prompt
			im.err = "chosen just now: ↵ again to answer"
			im.selAt = time.Time{}
			return nil
		}
		p := map[string]any{"id": im.it.ID, "rev": im.it.Rev, "note": im.note.String()}
		if len(im.it.Options) > 0 {
			if im.sel < 0 {
				im.err = fmt.Sprintf("choose first (1-%d); nothing is chosen for you", len(im.it.Options))
				return nil
			}
			p["choice"] = im.sel
			if mergeChoice(im.it, im.sel) {
				im.confirm, im.confirmAt, im.err = true, time.Now(), ""
				return nil
			}
		} else if strings.TrimSpace(im.note.String()) == "" {
			im.err = "no options: write the answer as a note (n)"
			return nil
		}
		return im.send("inbox.answer", p)
	default:
		if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
			if n := int(key[0] - '1'); n < len(im.it.Options) {
				im.sel = n
				im.selAt = time.Now()
				im.err = ""
			}
		}
	}
	return nil
}

// popupKey: a key the popup takes (choose, answer, skip, and a note once
// an option is chosen: "send back" needs one); any other is typing meant
// for somewhere else.
func popupKey(key string, options, sel int) bool {
	switch key {
	case "esc", "q", "j", "down", "k", "up", "enter":
		return true
	case "n":
		return sel >= 0
	}
	return len(key) == 1 && key[0] >= '1' && key[0] <= '9' && int(key[0]-'1') < options
}

// unconfirm takes the merge back: nothing is chosen (a popup is on skip
// and waits again), nothing is answered.
func (im *inboxModal) unconfirm() {
	im.confirm, im.sel = false, -1
	im.err = "merge cancelled · nothing was answered"
	if im.popup {
		im.shownAt = time.Now()
	}
}

// grace: the popup came up a moment ago and takes no key yet.
func (im *inboxModal) grace(now time.Time) bool {
	return now.Sub(im.shownAt) < popupGrace
}

// skip closes the popup with no answer: the item stays in the inbox for
// ]a and the bar, and does not come up again.
func (im *inboxModal) skip() tea.Cmd {
	im.m.setMsg(fmt.Sprintf("skipped inbox #%d · it stays in the inbox · ]a", im.it.ID), false)
	return done(nil)
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
			im.sel, im.confirm = -1, false // read it again before choosing
			return m.openInboxRefresh(im)
		}
		return nil
	}
	m.modal = nil
	if m.mergeToasted[msg.id] == "" {
		m.setMsg(fmt.Sprintf("answered inbox #%d", msg.id), false)
	}
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
