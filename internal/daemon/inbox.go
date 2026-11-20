package daemon

// The inbox (docs/orchestration.md "4. 判断の受信箱"): an agent asks a
// person to decide, approve or do something (`pekoe ask-human`), and the
// person's answer goes back to the agent that asked.
//
// What the person reads first is what pekoed read itself (facts: the
// session's branch, HEAD, diff size, PR, CI, review, conflict forecast),
// then the agent's words, which are an opinion: its recommendation is
// never the default choice. Every item has a rev that goes up whenever it
// changes, facts included; an answer carries the rev the person looked at
// and is refused when the item changed since.
//
// The asker is the caller pekoed identified (caller.go), never what the
// agent says it is. Agents may post, read and withdraw their own items
// without the token; only a person answers or dismisses (humanMethods).
//
// The answer reaches the asker (U8 (a)): if it is waiting (`pekoe wait
// --on inbox`, `ask-human --wait`), as the wait's result; if not, as one
// line typed into its input box once it is free (idle or done, no
// permission prompt by the hooks' state or on screen, checked when taken
// and again right before typing, as session.send without the token does;
// the line starts with "[", never a digit; nobody typed into it in the last
// 30 seconds). Until then it stays pending in state.db and is tried again.

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"pekoe/internal/engine"
	"pekoe/internal/gitx"
	"pekoe/internal/ids"
	"pekoe/internal/state"
	"pekoe/internal/untrusted"
)

const (
	inboxQuestionMax = 500 // characters
	inboxOptionsMax  = 6
	inboxOptionLen   = 200
	inboxContextMax  = 4000
	inboxLinksMax    = 10
	inboxLinkLen     = 500
	inboxNoteMax     = 4000
	inboxOpenMax     = 10 // open items per caller
	// inboxTypedQuiet: no delivery into a window a person typed into this
	// recently (they may be in the middle of a line)
	inboxTypedQuiet = 30 * time.Second
	callerPekoed    = "pekoed"
)

var inboxKinds = map[string]bool{"decision": true, "approve": true, "login": true, "fyi": true}

// cleanInbox drops ANSI sequences, control characters (line breaks and
// tabs stay when multiline) and the invisible format characters that can
// reorder or hide text (bidi overrides, zero-width), so the text shows on
// every screen as what it is.
func cleanInbox(s string, multiline bool) string {
	s = engine.CleanText(stripANSI(s))
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			if !multiline {
				return ' '
			}
			return r
		case unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

type inboxPostP struct {
	Question  string   `json:"question"`
	Kind      string   `json:"kind"`
	Options   []string `json:"options"`
	Recommend *int     `json:"recommend"`
	Context   string   `json:"context"`
	Links     []string `json:"links"`
	About     string   `json:"about"`
	Decided   *int     `json:"decided"`
}

// inboxCaller is who an inbox call is from: the caller as recorded
// (Caller.ID). An agent window is its session, token or not; the token
// from a session's run window (TUI `!`) or review window is a person's,
// so a question asked there is the person's, not the agent's.
func inboxCaller(who *Caller) string { return who.ID() }

// errReservedWindow refuses an inbox call from a window named like pekoed,
// a person or an unknown caller (a session made before the names were
// reserved): its items would read as pekoed's own or a person's, and as
// "human" it would also lift the open-item cap and withdraw anyone's
// questions.
func errReservedWindow(method, name string) error {
	return fmt.Errorf("%s: a session named %s cannot use the inbox (rename its branch)", method, name)
}

func (d *Daemon) hInboxPost(who *Caller, p inboxPostP) (any, error) {
	if name := who.reservedWindow(); name != "" {
		return nil, errReservedWindow("inbox.post", name)
	}
	from := inboxCaller(who)
	if from == callerUnknown {
		return nil, errUnknownCaller("inbox.post")
	}
	if from == callerPekoed {
		// unreachable: pekoed is reserved (above); kept as a guard
		return nil, errReservedWindow("inbox.post", callerPekoed)
	}
	it, err := cleanInboxPost(p)
	if err != nil {
		return nil, err
	}
	it.From, it.Claimed = from, who.ClaimedDiff(from)
	if p.About != "" {
		slug, err := d.resolveSessionRef(p.About)
		if err != nil {
			return nil, err
		}
		if _, err := d.snapshot(slug); err != nil {
			return nil, fmt.Errorf("inbox.post: about: %w", err)
		}
		it.About = slug
	} else if _, err := d.snapshot(from); err == nil {
		it.About = from // a session asks about its own work
	}
	now := time.Now()
	it.Rev, it.CreatedAt, it.UpdatedAt, it.Delivery = 1, now, now, state.DeliveryNone
	if it.About != "" {
		it.Facts = d.inboxFacts(it.About)
	}
	if it.Kind == "fyi" {
		// what the agent decided itself: closed from the start
		it.Status = state.InboxAnswered
		it.Answer = &state.InboxAnswer{Choice: p.Decided, By: from, At: now}
	} else {
		it.Status = state.InboxOpen
	}
	d.inboxMu.Lock()
	if it.Status == state.InboxOpen && who.ID() != callerHuman {
		n, err := d.DB.InboxOpenCount(from)
		if err != nil {
			d.inboxMu.Unlock()
			return nil, err
		}
		if n >= inboxOpenMax {
			d.inboxMu.Unlock()
			return nil, fmt.Errorf("inbox.post: %s already has %d open questions; fold what you need into one question, or withdraw one (pekoe inbox withdraw <id>)", from, n)
		}
	}
	err = d.DB.AddInbox(it)
	d.inboxMu.Unlock()
	if err != nil {
		return nil, err
	}
	d.emit("inbox.updated", it)
	return it, nil
}

// cleanInboxPost checks p's kind, fields and sizes (too long is refused,
// not cut: the agent shortens it) and drops control characters.
func cleanInboxPost(p inboxPostP) (*state.InboxItem, error) {
	if !inboxKinds[p.Kind] {
		return nil, fmt.Errorf("inbox.post: kind must be decision, approve, login or fyi (got %q)", p.Kind)
	}
	q := cleanInbox(p.Question, true)
	if q == "" {
		return nil, errors.New("inbox.post: question required")
	}
	if n := utf8.RuneCountInString(q); n > inboxQuestionMax {
		return nil, fmt.Errorf("inbox.post: question is %d characters, at most %d (put the background in context)", n, inboxQuestionMax)
	}
	ctx := cleanInbox(p.Context, true)
	if n := utf8.RuneCountInString(ctx); n > inboxContextMax {
		return nil, fmt.Errorf("inbox.post: context is %d characters, at most %d", n, inboxContextMax)
	}
	if len(p.Options) > inboxOptionsMax {
		return nil, fmt.Errorf("inbox.post: at most %d options", inboxOptionsMax)
	}
	opts := []string{}
	for _, o := range p.Options {
		o = cleanInbox(o, false)
		if o == "" {
			return nil, errors.New("inbox.post: an option is empty")
		}
		if utf8.RuneCountInString(o) > inboxOptionLen {
			return nil, fmt.Errorf("inbox.post: an option is at most %d characters", inboxOptionLen)
		}
		opts = append(opts, o)
	}
	switch p.Kind {
	case "login":
		if len(opts) > 0 || p.Recommend != nil {
			return nil, errors.New("inbox.post: login has fixed choices (done / can't): no options or recommend")
		}
		opts = state.InboxKindOptions(p.Kind)
	case "approve":
		if len(opts) == 0 {
			opts = state.InboxKindOptions(p.Kind)
		}
	}
	if len(opts) == 1 {
		return nil, errors.New("inbox.post: give two options or more (or none, for an answer in words)")
	}
	if p.Recommend != nil && (*p.Recommend < 0 || *p.Recommend >= len(opts)) {
		return nil, fmt.Errorf("inbox.post: recommend is an option's index, 0 to %d", len(opts)-1)
	}
	if p.Kind == "fyi" {
		if p.Recommend != nil {
			return nil, errors.New("inbox.post: fyi records what was decided (decided), not a recommendation")
		}
		if p.Decided != nil && (*p.Decided < 0 || *p.Decided >= len(opts)) {
			return nil, fmt.Errorf("inbox.post: decided is an option's index, 0 to %d", len(opts)-1)
		}
	} else if p.Decided != nil {
		return nil, errors.New("inbox.post: decided goes with kind fyi")
	}
	if len(p.Links) > inboxLinksMax {
		return nil, fmt.Errorf("inbox.post: at most %d links", inboxLinksMax)
	}
	links := []string{}
	for _, l := range p.Links {
		l = cleanInbox(l, false)
		if l == "" {
			continue
		}
		if utf8.RuneCountInString(l) > inboxLinkLen {
			return nil, fmt.Errorf("inbox.post: a link is at most %d characters", inboxLinkLen)
		}
		links = append(links, l)
	}
	return &state.InboxItem{Kind: p.Kind, Question: q, Options: opts, Recommend: p.Recommend, Context: ctx, Links: links}, nil
}

// inboxFacts reads what pekoed knows about slug now. HEAD comes from git
// (in its guarded form, gitx); the rest from the session as pekoed keeps it.
func (d *Daemon) inboxFacts(slug string) *state.InboxFacts {
	s, err := d.snapshot(slug)
	if err != nil {
		return &state.InboxFacts{Slug: slug, Gone: true}
	}
	f := &state.InboxFacts{Slug: slug, State: s.State, Branch: s.Branch, Base: s.Base, Diffstat: s.Diffstat, PR: s.PR,
		Test: s.Test, Overlaps: s.Overlaps, GitBlocked: s.GitBlocked}
	if r := s.Report; r != nil {
		f.Report = &state.InboxReport{ID: r.ID, Status: r.Status, At: r.At, Head: r.Head}
	}
	if f.Overlaps != nil {
		f.Overlaps = append([]state.Overlap(nil), f.Overlaps...)
	}
	if d.checkGitDir(s) {
		f.Head, _ = gitx.RevParse(s.Path, "HEAD")
	}
	if s.PR > 0 {
		if base := d.webBase(s.RepoDir); base != "" {
			f.PRURL = fmt.Sprintf("%s/pull/%d", base, s.PR)
		}
	}
	if r := d.ghRepoInfoOf(s.RepoDir); r.Fork() {
		f.PRRepo, f.PushRepo = r.Base, r.Origin
	}
	if ps := s.PRStatus; ps != nil {
		f.PRStatus = &state.InboxPR{State: ps.State, Draft: ps.Draft, Pass: ps.Pass, Fail: ps.Fail, Pending: ps.Pending,
			ReviewDecision: ps.ReviewDecision, Unresolved: ps.Unresolved, MergeState: ps.MergeState, HeadSHA: ps.HeadSHA}
	}
	if mergeable(s) {
		f.Merge = d.mergeCheck(s)
	}
	return f
}

// webBase is https://<host>/<owner>/<name> of the repository a
// repository's PRs go into (ghRepoInfo.Base: origin, or for a fork the
// base gh would pick), "" when it has none.
func (d *Daemon) webBase(repoDir string) string {
	if r := d.ghRepoInfoOf(repoDir).Base; r != "" {
		return "https://" + r
	}
	return ""
}

// webBaseOf is https://<host>/<owner>/<name> of an origin URL ("" when it
// names none).
func webBaseOf(origin string) string {
	if u := gitx.NormalizeURL(origin); strings.Count(u, "/") >= 2 && !strings.ContainsAny(u, " \"<>") {
		return "https://" + u
	}
	return ""
}

// refreshFacts reads an open item's facts again; when they changed, the
// item's rev goes up (an answer to the old rev is refused). Under inboxMu.
func (d *Daemon) refreshFactsLocked(it *state.InboxItem) bool {
	if it.Status != state.InboxOpen || it.About == "" {
		return false
	}
	f := d.inboxFacts(it.About)
	if f.Same(it.Facts) {
		return false
	}
	it.Facts, it.Rev, it.UpdatedAt = f, it.Rev+1, time.Now()
	if err := d.DB.SaveInbox(it); err != nil {
		log.Printf("inbox %s: %v", ids.Question(it.ID), err)
	}
	return true
}

type inboxListP struct {
	Status string `json:"status"` // open (default) | answered | withdrawn | dismissed | all
	Mine   bool   `json:"mine"`
	Limit  int    `json:"limit"`
}

func (d *Daemon) hInboxList(who *Caller, p inboxListP) (any, error) {
	f := state.InboxFilter{Limit: clampLimit(p.Limit, 100, 500)}
	switch p.Status {
	case "", state.InboxOpen:
		f.Status = []string{state.InboxOpen}
	case state.InboxAnswered, state.InboxWithdrawn, state.InboxDismissed:
		f.Status = []string{p.Status}
	case "all":
	default:
		return nil, fmt.Errorf("inbox.list: status must be open, answered, withdrawn, dismissed or all (got %q)", p.Status)
	}
	me := inboxCaller(who)
	if p.Mine {
		if name := who.reservedWindow(); name != "" {
			return nil, errReservedWindow("inbox.list --mine", name)
		}
		if me == callerUnknown {
			return nil, errUnknownCaller("inbox.list --mine")
		}
		f.From = me
	}
	d.inboxMu.Lock()
	its, err := d.DB.InboxList(f)
	var changed []state.InboxItem
	for i := range its {
		if d.refreshFactsLocked(&its[i]) {
			changed = append(changed, its[i])
		}
	}
	d.inboxMu.Unlock()
	if err != nil {
		return nil, err
	}
	for i := range changed {
		d.emit("inbox.updated", &changed[i])
	}
	for i := range its {
		d.inboxRead(who, &its[i])
	}
	return its, nil
}

type inboxIDP struct {
	ID   int64  `json:"id"`
	Note string `json:"note"`
}

func (d *Daemon) hInboxGet(who *Caller, p inboxIDP) (any, error) {
	d.inboxMu.Lock()
	it, err := d.DB.InboxGet(p.ID)
	changed := err == nil && d.refreshFactsLocked(it)
	d.inboxMu.Unlock()
	if err != nil {
		return nil, inboxErr(p.ID, err)
	}
	if changed {
		d.emit("inbox.updated", it)
	}
	d.inboxRead(who, it)
	return it, nil
}

// inboxRead: the asker read its own answer through the API, so it needs
// no line typed into its window. The window decides, not the token: an
// agent that reads the token (sandbox off) is still the asker.
func (d *Daemon) inboxRead(who *Caller, it *state.InboxItem) {
	if who.ID() == callerHuman || it.Delivery != state.DeliveryPending || inboxCaller(who) != inboxRecipient(it) {
		return
	}
	if ok, _ := d.DB.SetInboxDelivery(it.ID, state.DeliveryPending, state.DeliveryWaited); ok {
		it.Delivery = state.DeliveryWaited
		d.emit("inbox.updated", it)
	}
}

func inboxErr(id int64, err error) error {
	if errors.Is(err, state.ErrNoInboxItem) {
		return fmt.Errorf("%w: %s", state.ErrNoInboxItem, ids.Question(id))
	}
	return err
}

// Errors the Web UI tells apart (409): the item is not open any more, or it
// changed after the person looked at it.
var (
	errInboxClosed  = errors.New("already")
	errInboxChanged = errors.New("the item changed; look again")
)

// closeInbox changes an open item under inboxMu with f, which may refuse;
// the item's rev goes up.
func (d *Daemon) closeInbox(id int64, method string, f func(it *state.InboxItem) error) (*state.InboxItem, error) {
	d.inboxMu.Lock()
	defer d.inboxMu.Unlock()
	it, err := d.DB.InboxGet(id)
	if err != nil {
		return nil, inboxErr(id, err)
	}
	if it.Status != state.InboxOpen {
		return nil, fmt.Errorf("%s: inbox %s is %s %w", method, ids.Question(id), it.Status, errInboxClosed)
	}
	if err := f(it); err != nil {
		return nil, err
	}
	it.Rev++
	it.UpdatedAt = time.Now()
	if err := d.DB.SaveInbox(it); err != nil {
		return nil, err
	}
	return it, nil
}

func (d *Daemon) hInboxWithdraw(who *Caller, p inboxIDP) (any, error) {
	if name := who.reservedWindow(); name != "" {
		return nil, errReservedWindow("inbox.withdraw", name)
	}
	me := inboxCaller(who)
	if me == callerUnknown {
		return nil, errUnknownCaller("inbox.withdraw")
	}
	note, err := inboxNote(p.Note)
	if err != nil {
		return nil, err
	}
	it, err := d.closeInbox(p.ID, "inbox.withdraw", func(it *state.InboxItem) error {
		// the person is told by the caller's window, not by the token: an
		// agent that reads the token (sandbox off, tapi chat) is still an
		// agent and takes back only its own questions
		if me != callerHuman && it.From != me {
			return fmt.Errorf("inbox.withdraw: %s was asked by %s; a caller withdraws only its own questions", ids.Question(it.ID), it.From)
		}
		it.Status = state.InboxWithdrawn
		it.Answer = &state.InboxAnswer{Note: note, By: me, Via: who.Via, At: time.Now()}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if it.From != me {
		// a person withdrew someone else's question: the asker is told as
		// of a dismissal, so it does not wait for an answer
		d.inboxClosed(it)
		return it, nil
	}
	d.emit("inbox.updated", it)
	return it, nil
}

type inboxAnswerP struct {
	ID     int64  `json:"id"`
	Choice *int   `json:"choice"`
	Note   string `json:"note"`
	Rev    int64  `json:"rev"`
}

func inboxNote(s string) (string, error) {
	n := cleanInbox(s, true)
	if c := utf8.RuneCountInString(n); c > inboxNoteMax {
		return "", fmt.Errorf("note is %d characters, at most %d", c, inboxNoteMax)
	}
	return n, nil
}

// hInboxAnswer is a person's answer (human only): to the rev they looked
// at. The facts are read again first, so an answer given while the
// session moved on (a new HEAD, CI went red) is refused too.
func (d *Daemon) hInboxAnswer(who *Caller, p inboxAnswerP) (any, error) {
	if p.Rev <= 0 {
		return nil, errors.New("inbox.answer: rev required (the rev of the item you looked at)")
	}
	note, err := inboxNote(p.Note)
	if err != nil {
		return nil, fmt.Errorf("inbox.answer: %w", err)
	}
	changed := false
	it, err := d.closeInbox(p.ID, "inbox.answer", func(it *state.InboxItem) error {
		changed = d.refreshFactsLocked(it)
		if p.Rev != it.Rev {
			return fmt.Errorf("inbox.answer: inbox %s changed (rev %d, you answered rev %d); %w", ids.Question(it.ID), it.Rev, p.Rev, errInboxChanged)
		}
		switch {
		case len(it.Options) > 0 && p.Choice == nil:
			return fmt.Errorf("inbox.answer: choose one of the %d options", len(it.Options))
		case p.Choice != nil && (*p.Choice < 0 || *p.Choice >= len(it.Options)):
			if len(it.Options) == 0 {
				return errors.New("inbox.answer: this question has no options; answer with a note")
			}
			return fmt.Errorf("inbox.answer: choice is an option's index, 0 to %d", len(it.Options)-1)
		case len(it.Options) == 0 && note == "":
			return errors.New("inbox.answer: this question has no options; answer with a note")
		case isMergeItem(it) && *p.Choice == mergeSendBack && note == "":
			return errors.New("inbox.answer: send back needs a note (it is sent to the session)")
		}
		it.Status = state.InboxAnswered
		// by the caller's window, as withdraw: an agent that reads the token
		// (sandbox off, tapi chat) answers as itself, not as a person
		it.Answer = &state.InboxAnswer{Choice: p.Choice, Note: note, By: inboxCaller(who), Via: who.Via, At: time.Now()}
		if mergeAsked(it, p.Choice) {
			// pekoed merges it now (mergerun.go): saved with the answer, so
			// the answer and "it is being merged" are one change
			pr, head := 0, ""
			if f := it.Facts; f != nil {
				pr = f.PR
				if f.Merge != nil {
					head = f.Merge.PRHead
				}
			}
			if rec := d.mergeRecordOf(it.About); rec.ID == it.ID {
				pr, head = rec.PR, rec.Head // what pekoed asked about
			}
			it.Answer.Merge = &state.MergeRun{State: state.MergeRunning, PR: pr, Head: head, At: it.Answer.At}
		}
		return nil
	})
	if err != nil {
		if changed {
			if cur, e := d.DB.InboxGet(p.ID); e == nil {
				d.emit("inbox.updated", cur)
			}
		}
		return nil, err
	}
	d.inboxClosed(it)
	if a := it.Answer; a.Merge != nil {
		d.startMerge(it)
	}
	return it, nil
}

// hInboxDismiss closes an item without an answer (human only). The asker
// is told as it would be of an answer, so it does not wait for one.
func (d *Daemon) hInboxDismiss(who *Caller, p inboxIDP) (any, error) {
	note, err := inboxNote(p.Note)
	if err != nil {
		return nil, fmt.Errorf("inbox.dismiss: %w", err)
	}
	it, err := d.closeInbox(p.ID, "inbox.dismiss", func(it *state.InboxItem) error {
		it.Status = state.InboxDismissed
		it.Answer = &state.InboxAnswer{Note: note, By: inboxCaller(who), Via: who.Via, At: time.Now()}
		return nil
	})
	if err != nil {
		return nil, err
	}
	d.inboxClosed(it)
	return it, nil
}

// inboxClosed sends a person's answer (or dismissal, or withdrawal of
// someone else's question), saved already, on its way to the asker: a
// wait of the asker's that takes it gets it now (waited); else it waits
// for the asker's input box (pending). A person or an unknown caller is
// told nothing.
func (d *Daemon) inboxClosed(it *state.InboxItem) {
	switch it.From {
	case callerHuman, callerUnknown, "":
		d.emit("inbox.updated", it)
		return
	case callerPekoed:
		// pekoed's own item: only a merge sent back goes anywhere, as a
		// line typed into the session's window
		to := inboxRecipient(it)
		if to != "" {
			d.inboxMu.Lock()
			if ok, _ := d.DB.SetInboxDelivery(it.ID, state.DeliveryNone, state.DeliveryPending); ok {
				it.Delivery = state.DeliveryPending
			}
			d.inboxMu.Unlock()
		}
		d.emit("inbox.updated", it)
		if it.Delivery == state.DeliveryPending {
			go d.deliverInbox(to)
		}
		return
	}
	slug := it.About
	if slug == "" {
		slug = it.From
	}
	d.inboxMu.Lock()
	c := Change{Kind: kindInbox, Slug: slug, Summary: inboxSummary(it), to: it.From, inbox: it.ID}
	it.Delivery = state.DeliveryPending
	if d.changes.addSeen(c) {
		it.Delivery = state.DeliveryWaited
	}
	if _, err := d.DB.SetInboxDelivery(it.ID, state.DeliveryNone, it.Delivery); err != nil {
		log.Printf("inbox %s: %v", ids.Question(it.ID), err)
	}
	d.inboxMu.Unlock()
	d.emit("inbox.updated", it)
	if it.Delivery == state.DeliveryPending {
		go d.deliverInbox(it.From)
	}
}

// inboxSessionGone: session slug is gone (rm), and an item belongs to a
// slug, which a later session of the same branch takes again. What the
// removed session asked must not become the new one's: its open items are
// withdrawn by pekoed (no answer can reach it any more; nor do they count
// against the new session's open limit, nor can it withdraw them), and an
// answer not yet typed into its window is never typed (delivery none):
// its own, and pekoed's merge item sent back to it.
func (d *Daemon) inboxSessionGone(slug string) {
	if slug == "" || slug == TapiSlug || slug == callerHuman || slug == callerUnknown || slug == callerPekoed {
		return
	}
	open, err := d.DB.InboxList(state.InboxFilter{Status: []string{state.InboxOpen}, From: slug})
	if err != nil {
		log.Printf("inbox of removed %s: %v", slug, err)
	}
	for _, o := range open {
		it, err := d.closeInbox(o.ID, "session removed", func(it *state.InboxItem) error {
			if it.From != slug {
				return errInboxClosed
			}
			it.Status = state.InboxWithdrawn
			it.Answer = &state.InboxAnswer{Note: "session removed", By: callerPekoed, Via: callerPekoed, At: time.Now()}
			return nil
		})
		if err == nil {
			d.emit("inbox.updated", it)
		}
	}
	pending, err := d.DB.InboxList(state.InboxFilter{Delivery: state.DeliveryPending})
	if err != nil {
		log.Printf("inbox of removed %s: %v", slug, err)
	}
	for _, p := range pending {
		if inboxRecipient(&p) != slug {
			continue
		}
		d.inboxMu.Lock()
		ok, _ := d.DB.SetInboxDelivery(p.ID, state.DeliveryPending, state.DeliveryNone)
		d.inboxMu.Unlock()
		if ok {
			p.Delivery = state.DeliveryNone
			d.emit("inbox.updated", &p)
		}
	}
}

// inboxRecipient is who a closed item's answer is typed to: the asker, or
// for pekoed's merge item sent back, the session it is about ("" nobody).
func inboxRecipient(it *state.InboxItem) string {
	if it.From != callerPekoed {
		return it.From
	}
	if mergeSentBack(it) {
		return it.About
	}
	return ""
}

// inboxSummary is the wait Change's one line.
func inboxSummary(it *state.InboxItem) string {
	s := fmt.Sprintf("inbox %s %s", ids.Question(it.ID), it.Status)
	if a := it.Answer; a != nil && a.Choice != nil && *a.Choice < len(it.Options) {
		s += fmt.Sprintf(": %d %s", *a.Choice+1, lineCut(it.Options[*a.Choice], 60))
	}
	return s
}

// inboxLine is the line typed into the asker's input box. It starts with
// "[", never a digit (in a permission menu a digit picks a choice).
func inboxLine(it *state.InboxItem) string {
	a := it.Answer
	var b strings.Builder
	fmt.Fprintf(&b, "[pekoe inbox %s] ", ids.Question(it.ID))
	switch {
	case mergeSentBack(it):
		pr := 0
		if it.Facts != nil {
			pr = it.Facts.PR
		}
		fmt.Fprintf(&b, "人が PR #%d のマージを差し戻しました", pr)
	case it.Status == state.InboxDismissed:
		b.WriteString("答えずに閉じられました")
	case it.Status == state.InboxWithdrawn:
		b.WriteString("人が取り下げました")
	case a != nil && a.Choice != nil && *a.Choice < len(it.Options):
		fmt.Fprintf(&b, "答え: %d「%s」", *a.Choice+1, lineCut(it.Options[*a.Choice], 200))
	default:
		b.WriteString("答え:")
	}
	if a != nil && a.Note != "" {
		fmt.Fprintf(&b, "(メモ: %s)", lineCut(a.Note, 1000))
	}
	fmt.Fprintf(&b, " 詳しくは pekoe --json inbox %s", ids.Question(it.ID))
	return untrusted.Line(b.String())
}

// noteTyped records that a person typed into slug's agent window (the
// TUI forwards keys and text there): no delivery for a while.
func (d *Daemon) noteTyped(slug string) {
	d.mu.Lock()
	d.typedAt[slug] = time.Now()
	d.mu.Unlock()
}

func (d *Daemon) typedRecently(slug string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return time.Since(d.typedAt[slug]) < inboxTypedQuiet
}

// inboxReady: to's agent can take a line now: its input box is free (idle
// or done), no permission prompt is up (hooks or screen), nobody typed
// into it lately. The second prompt check is send's, right before typing.
func (d *Daemon) inboxReady(to string) (bool, error) {
	if d.typedRecently(to) {
		return false, nil
	}
	if to == TapiSlug {
		if !d.tapiStarted() || !d.Eng.Alive(TapiSlug, engine.WinAgent) {
			// its agent exited and tmux kept the window (remain-on-exit):
			// a line typed into the dead pane reaches nobody
			return false, nil
		}
		d.mu.Lock()
		st, wk := d.tapi.state, d.tapi.waitKind
		d.mu.Unlock()
		return st == state.Idle && !d.promptUp(TapiSlug, st, wk), nil
	}
	s, err := d.snapshot(to)
	if err != nil {
		return false, err
	}
	if s.State != state.Idle && s.State != state.Done {
		return false, nil
	}
	if !d.Eng.Alive(to, engine.WinAgent) {
		return false, nil
	}
	return !d.promptUp(to, s.State, s.WaitKind), nil
}

// deliverInbox types the oldest pending answer for to into its window when
// it is free. One at a time: the agent takes it up, and the next goes when
// it is free again.
func (d *Daemon) deliverInbox(to string) {
	if to == "" {
		return
	}
	mu, _ := d.inboxDeliver.LoadOrStore(to, &sync.Mutex{})
	if !mu.(*sync.Mutex).TryLock() {
		return // a delivery to it is under way
	}
	defer mu.(*sync.Mutex).Unlock()
	if to == TapiSlug && d.deliverOn() {
		// [orchestrator] deliver: tapi's answers go in the delivery's
		// line, with its sessions' changes (deliver.go)
		d.deliverTapiLocked()
		return
	}
	all, err := d.DB.InboxList(state.InboxFilter{Delivery: state.DeliveryPending})
	if err != nil {
		return
	}
	var its []state.InboxItem
	for _, it := range all {
		if inboxRecipient(&it) == to {
			its = append(its, it)
		}
	}
	if len(its) == 0 {
		return
	}
	it := &its[len(its)-1] // oldest
	ready, err := d.inboxReady(to)
	if err != nil {
		// the asker is gone: nobody to tell
		for i := range its {
			if ok, _ := d.DB.SetInboxDelivery(its[i].ID, state.DeliveryPending, state.DeliveryNone); ok {
				its[i].Delivery = state.DeliveryNone
				d.emit("inbox.updated", &its[i])
			}
		}
		return
	}
	if !ready {
		return
	}
	if cur, err := d.DB.InboxGet(it.ID); err != nil || cur.Delivery != state.DeliveryPending {
		// the asker read it meanwhile (inbox.get, a wait that took it
		// after the asker looked and before its wait took hold)
		return
	}
	line := inboxLine(it)
	if to == TapiSlug {
		_, err = d.hTapiSend(tapiSendP{Text: line, Submit: true})
	} else {
		_, err = d.send(sendP{Slug: to, Text: line, Submit: true}, true)
	}
	if errors.Is(err, errPromptUp) || errors.Is(err, errPromptUpTUI) {
		return // a prompt came up before the text went: next time
	}
	// errPromptUpTyped: the line is in the input box, not submitted; it is
	// not typed again (it would be there twice), the log says what happened
	r := &state.SendRecord{At: time.Now(), From: callerPekoed, To: to, Kind: "inbox", Text: line, Via: callerPekoed, OK: err == nil}
	if err != nil {
		r.Error = err.Error()
	}
	if e := d.DB.AddSend(r); e != nil {
		log.Printf("send log: %v", e)
	}
	if err != nil && !errors.Is(err, errPromptUpTyped) {
		log.Printf("inbox %s to %s: %v", ids.Question(it.ID), to, err)
		return
	}
	if ok, _ := d.DB.SetInboxDelivery(it.ID, state.DeliveryPending, state.DeliveryDelivered); ok {
		it.Delivery = state.DeliveryDelivered
		d.emit("inbox.updated", it)
	}
}

// deliverAllInbox tries every asker with a pending answer (the tick: a
// prompt went away without a state change, pekoed restarted).
func (d *Daemon) deliverAllInbox() {
	if !d.inboxTick.CompareAndSwap(false, true) {
		return
	}
	defer d.inboxTick.Store(false)
	its, err := d.DB.InboxList(state.InboxFilter{Delivery: state.DeliveryPending})
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, it := range its {
		if to := inboxRecipient(&it); to != "" && !seen[to] {
			seen[to] = true
			d.deliverInbox(to)
		}
	}
}

// inboxWaited marks the inbox answers a wait returned as received (under
// inboxMu: after inboxClosed has set them pending).
func (d *Daemon) inboxWaited(cs []Change) {
	d.inboxMu.Lock()
	defer d.inboxMu.Unlock()
	for _, c := range cs {
		if c.inbox == 0 {
			continue
		}
		if ok, _ := d.DB.SetInboxDelivery(c.inbox, state.DeliveryPending, state.DeliveryWaited); ok {
			if it, err := d.DB.InboxGet(c.inbox); err == nil {
				d.emit("inbox.updated", it)
			}
		}
	}
}
