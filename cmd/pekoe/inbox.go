package main

// ask-human / inbox (docs/orchestration.md "4. 判断の受信箱"): an agent asks
// a person, the person answers. Indexes are 1-based here (the API's are
// 0-based). What the agent wrote is fenced (internal/untrusted) and comes
// after the facts pekoed read itself.

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"pekoe/internal/ids"
	"pekoe/internal/state"
	"pekoe/internal/untrusted"
)

// optionIndex reads --recommend / --decided: an option's number (from 1)
// or its text. -1 when not given.
func optionIndex(flag, v string, opts []string) (int, error) {
	if v == "" {
		return -1, nil
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n < 1 || n > len(opts) {
			return 0, fmt.Errorf("--%s %d: there are %d options", flag, n, len(opts))
		}
		return n - 1, nil
	}
	for i, o := range opts {
		if o == v {
			return i, nil
		}
	}
	return 0, fmt.Errorf("--%s %q is not one of the --option values", flag, v)
}

// askChoices is what an item of kind ends up with for options: opts, or
// with none, the kind's fixed ones (state.InboxKindOptions, as pekoed).
func askChoices(kind string, opts []string) []string {
	if len(opts) == 0 {
		return state.InboxKindOptions(kind)
	}
	return opts
}

func cmdAskHuman(args []string) error {
	f, pos, err := parseCmd("ask-human", args)
	if err != nil {
		return err
	}
	use := specs["ask-human"].use
	timeout, err := waitTimeout(f["timeout"])
	if err != nil {
		return &usageError{use: use, msg: err.Error()}
	}
	// --wait <id>: wait for an earlier question's answer (r12 or p12 is
	// another kind's number, not a question to ask)
	if f["wait"] != "" && len(pos) == 1 && f["kind"] == "" && len(multiVal(f, "option")) == 0 && ids.Numbered(pos[0]) {
		id, err := ids.ParseQuestion(pos[0])
		if err != nil {
			return &usageError{use: use, msg: err.Error()}
		}
		return askWait(id, timeout)
	}
	cl := connect()
	defer cl.Close()
	if len(pos) == 0 {
		return &usageError{use: use, msg: "question required"}
	}
	q := strings.Join(pos, " ")
	opts := multiVal(f, "option")
	kind := f["kind"]
	if kind == "" {
		kind = "decision"
	}
	p := map[string]any{"question": q, "kind": kind, "options": opts, "links": multiVal(f, "link")}
	// --recommend / --decided name one of the options pekoed will keep:
	// the kind's own when none are given (approve: approve / decline)
	choices := askChoices(kind, opts)
	rec, err := optionIndex("recommend", f["recommend"], choices)
	if err != nil {
		return &usageError{use: use, msg: err.Error()}
	}
	if rec >= 0 {
		p["recommend"] = rec
	}
	dec, err := optionIndex("decided", f["decided"], choices)
	if err != nil {
		return &usageError{use: use, msg: err.Error()}
	}
	if dec >= 0 {
		p["decided"] = dec
	}
	if f["about"] != "" {
		p["about"] = f["about"]
	}
	if c := f["context"]; c == "-" {
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		if err != nil {
			return err
		}
		p["context"] = string(b)
	} else if c != "" {
		p["context"] = c
	}
	var it state.InboxItem
	if err := cl.Call("inbox.post", p, &it); err != nil {
		return err
	}
	if f["wait"] != "" && it.Status == state.InboxOpen {
		return askWait(it.ID, timeout)
	}
	if jsonOut {
		printJSON(it)
		return nil
	}
	fmt.Printf("inbox %s (%s) asked; wait for the answer with: pekoe --json ask-human --wait %s\n", ids.Question(it.ID), it.Kind, ids.Question(it.ID))
	return nil
}

// askWait waits until item id is answered, dismissed or withdrawn (or the
// timeout), and prints it. Reading it with inbox.get tells pekoed the
// asker has the answer, so nothing is typed into its window.
func askWait(id int64, timeout time.Duration) error {
	cl := connect()
	defer cl.Close()
	end := time.Now().Add(timeout)
	cursor := ""
	for again := false; ; again = true {
		var it state.InboxItem
		if err := cl.Call("inbox.get", map[string]any{"id": id}, &it); err != nil {
			return err
		}
		left := time.Until(end)
		if it.Status != state.InboxOpen || left < time.Second {
			if jsonOut {
				printJSON(it)
			} else {
				printInboxItem(it, true)
				if it.Status == state.InboxOpen {
					fmt.Printf("timed out: no answer yet; wait again with: pekoe --json ask-human --wait %s\n", ids.Question(id))
				}
			}
			return nil
		}
		// only this item's change: the answers to the caller's other
		// questions stay theirs (pending, typed or taken by their own
		// wait). pekoed returns it even when it came between the get above
		// and the wait taking hold. At most a minute per wait all the same
		step := min(left, time.Minute)
		p := map[string]any{"on": []string{"inbox"}, "inbox": id, "since": cursor, "timeout_s": int(step / time.Second)}
		if again {
			// a call again: pekoed does not count it as tapi starting a
			// wait in its turn (it may run in the background)
			p["rewait"] = true
		}
		var w waitOut
		if err := cl.CallTimeout("wait", p, &w, step+time.Minute); err != nil {
			return err
		}
		cursor = w.Cursor
		if w.Reset {
			cursor = ""
		}
	}
}

func cmdInbox(args []string) error {
	name := "inbox"
	if len(args) > 0 {
		switch args[0] {
		case "answer", "withdraw", "dismiss":
			name, args = "inbox "+args[0], args[1:]
		}
	}
	f, pos, err := parseCmd(name, args)
	if err != nil {
		return err
	}
	use := specs[name].use
	cl := connect()
	defer cl.Close()
	if name == "inbox" {
		if len(pos) > 1 {
			return &usageError{use: use, msg: "unexpected argument " + pos[1]}
		}
		if len(pos) == 1 {
			id, err := itemID(pos[0])
			if err != nil {
				return &usageError{use: use, msg: err.Error()}
			}
			var it state.InboxItem
			if err := cl.Call("inbox.get", map[string]any{"id": id}, &it); err != nil {
				return err
			}
			if jsonOut {
				printJSON(it)
			} else {
				printInboxItem(it, true)
			}
			return nil
		}
		st := "open"
		if f["all"] != "" {
			st = "all"
		}
		var its []state.InboxItem
		if err := cl.Call("inbox.list", map[string]any{"status": st, "mine": f["mine"] != ""}, &its); err != nil {
			return err
		}
		if jsonOut {
			printJSON(its)
			return nil
		}
		if len(its) == 0 {
			fmt.Println("the inbox is empty")
			return nil
		}
		for i, it := range its {
			if i > 0 {
				fmt.Println()
			}
			printInboxItem(it, false)
		}
		return nil
	}
	if len(pos) == 0 {
		return &usageError{use: use, msg: "item id required"}
	}
	id, err := itemID(pos[0])
	if err != nil {
		return &usageError{use: use, msg: err.Error()}
	}
	p := map[string]any{"id": id}
	if f["note"] != "" {
		p["note"] = f["note"]
	}
	method := "inbox." + strings.TrimPrefix(name, "inbox ")
	switch name {
	case "inbox answer":
		if len(pos) > 2 {
			return &usageError{use: use, msg: "unexpected argument " + pos[2]}
		}
		if len(pos) == 2 {
			n, err := strconv.Atoi(pos[1])
			if err != nil || n < 1 {
				return &usageError{use: use, msg: "choice is an option's number, from 1"}
			}
			p["choice"] = n - 1
		}
		rev := int64(0)
		if f["rev"] != "" {
			if rev, err = strconv.ParseInt(f["rev"], 10, 64); err != nil {
				return &usageError{use: use, msg: "--rev must be a number"}
			}
		} else {
			// no --rev: the item as it is now (pekoe inbox shows the rev)
			var it state.InboxItem
			if err := cl.Call("inbox.get", map[string]any{"id": id}, &it); err != nil {
				return err
			}
			rev = it.Rev
		}
		p["rev"] = rev
	default:
		if len(pos) > 1 {
			return &usageError{use: use, msg: "unexpected argument " + pos[1]}
		}
	}
	var it state.InboxItem
	if err := cl.Call(method, p, &it); err != nil {
		return err
	}
	if jsonOut {
		printJSON(it)
		return nil
	}
	fmt.Printf("inbox %s %s\n", ids.Question(it.ID), it.Status)
	if a := it.Answer; a != nil && a.Merge != nil {
		fmt.Printf("%s; see how it goes with: pekoe inbox %s\n", mergeRunLine(a.Merge), ids.Question(it.ID))
	}
	return nil
}

func itemID(s string) (int64, error) {
	return ids.ParseQuestion(s)
}

// factsLine is what pekoed read about the session, on one line.
func factsLine(f *state.InboxFacts) string {
	if f == nil {
		return ""
	}
	if f.Gone {
		return f.Slug + " (session gone)"
	}
	parts := []string{f.Slug + " " + string(f.State)}
	if f.Branch != "" {
		parts = append(parts, f.Branch)
	}
	if f.Head != "" {
		parts = append(parts, "HEAD "+short(f.Head))
	}
	parts = append(parts, fmt.Sprintf("+%d -%d (%d files)", f.Diffstat.Added, f.Diffstat.Deleted, f.Diffstat.Files))
	if f.PR > 0 {
		pr := fmt.Sprintf("PR #%d", f.PR)
		if ps := f.PRStatus; ps != nil {
			pr += fmt.Sprintf(" %s CI %d pass %d fail %d pending", ps.State, ps.Pass, ps.Fail, ps.Pending)
			if ps.ReviewDecision != "" {
				pr += " " + ps.ReviewDecision
			}
			if ps.MergeState != "" {
				pr += " " + ps.MergeState
			}
		}
		parts = append(parts, pr)
	}
	if f.Test.Status != "" {
		parts = append(parts, "test "+f.Test.Status)
	}
	for _, o := range f.Overlaps {
		if o.Conflict {
			parts = append(parts, "conflicts with "+o.Slug)
		} else {
			parts = append(parts, "overlaps "+o.Slug)
		}
	}
	if f.Report != nil {
		parts = append(parts, fmt.Sprintf("report %s %s", ids.Report(f.Report.ID), f.Report.Status))
	}
	if f.GitBlocked != "" {
		parts = append(parts, ".git changed: git stopped")
	}
	return strings.Join(parts, " · ")
}

// printInboxItem prints pekoed's part first (who asked, facts, links,
// the answer), then the agent's words fenced. full: with the context.
func printInboxItem(it state.InboxItem, full bool) {
	head := fmt.Sprintf("%s %s %s · from %s", ids.Question(it.ID), it.Kind, it.Status, it.From)
	if it.Claimed != "" {
		head += fmt.Sprintf(" ⚠ claims to be %s", untrusted.Line(it.Claimed))
	}
	head += fmt.Sprintf(" · rev %d · %s", it.Rev, it.CreatedAt.Local().Format("01-02 15:04"))
	fmt.Println(head)
	if l := factsLine(it.Facts); l != "" {
		fmt.Println("  facts (pekoe): " + l)
	}
	if it.Facts != nil && it.Facts.PRURL != "" {
		fmt.Println("  pr: " + it.Facts.PRURL)
	}
	if it.Facts != nil && it.Facts.PRRepo != "" {
		fmt.Printf("  fork: PR in %s, branch pushed to %s (origin)\n", it.Facts.PRRepo, it.Facts.PushRepo)
	}
	for _, l := range it.Links {
		fmt.Println("  link: " + untrusted.Line(l))
	}
	if a := it.Answer; a != nil {
		s := "  " + it.Status + " by " + a.By
		if a.Choice != nil && *a.Choice < len(it.Options) {
			s += fmt.Sprintf(": %d %s", *a.Choice+1, untrusted.Line(it.Options[*a.Choice]))
		} else if a.Choice == nil && it.Status == state.InboxAnswered && len(it.Options) > 0 {
			// a note alone answered it (--json: answer.choice is absent)
			s += ": none of the options"
		}
		if a.Note != "" {
			s += " · note: " + untrusted.Line(a.Note)
		}
		if it.Delivery != state.DeliveryNone {
			s += " · delivery " + it.Delivery
		}
		fmt.Println(s)
		if r := a.Merge; r != nil {
			fmt.Println("  " + mergeRunLine(r))
		}
	}
	if it.Recommend != nil {
		fmt.Printf("  the agent recommends %d (its opinion)\n", *it.Recommend+1)
	}
	// the agent's words: fenced, so they cannot pose as pekoe's
	text := it.Question
	if len(it.Options) > 0 {
		text += "\n"
		for i, o := range it.Options {
			text += fmt.Sprintf("\n%d. %s", i+1, o)
		}
	}
	if full && it.Context != "" {
		text += "\n\n" + it.Context
	}
	fmt.Println(untrusted.Fence("text", text))
}

// mergeRunLine is pekoed's merge on a person's "merge" (pekoed's words).
func mergeRunLine(r *state.MergeRun) string {
	s := fmt.Sprintf("merge by pekoed: %s · PR #%d · HEAD %s", r.State, r.PR, short(r.Head))
	if r.Method != "" {
		s += " · " + r.Method
	}
	if r.Detail != "" {
		s += " · " + r.Detail
	}
	return s
}
