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
	cl := connect()
	defer cl.Close()
	// --wait <id>: wait for an earlier question's answer
	if f["wait"] != "" && len(pos) == 1 && f["kind"] == "" && len(multiVal(f, "option")) == 0 {
		if id, err := strconv.ParseInt(strings.TrimPrefix(pos[0], "#"), 10, 64); err == nil {
			return askWait(id, timeout)
		}
	}
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
	rec, err := optionIndex("recommend", f["recommend"], opts)
	if err != nil {
		return &usageError{use: use, msg: err.Error()}
	}
	if rec >= 0 {
		p["recommend"] = rec
	}
	dec, err := optionIndex("decided", f["decided"], opts)
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
	fmt.Printf("inbox #%d (%s) asked; wait for the answer with: pekoe --json ask-human --wait %d\n", it.ID, it.Kind, it.ID)
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
	for {
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
					fmt.Printf("timed out: no answer yet; wait again with: pekoe --json ask-human --wait %d\n", id)
				}
			}
			return nil
		}
		// at most a minute per wait: an answer given between the get above
		// and the wait taking hold is seen on the next round
		step := min(left, time.Minute)
		p := map[string]any{"on": []string{"inbox"}, "since": cursor, "timeout_s": int(step / time.Second)}
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
	fmt.Printf("inbox #%d %s\n", it.ID, it.Status)
	return nil
}

func itemID(s string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimPrefix(s, "#"), 10, 64)
	if err != nil || id < 1 {
		return 0, fmt.Errorf("bad item id %q", s)
	}
	return id, nil
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
		parts = append(parts, fmt.Sprintf("report #%d %s", f.Report.ID, f.Report.Status))
	}
	if f.GitBlocked != "" {
		parts = append(parts, ".git changed: git stopped")
	}
	return strings.Join(parts, " · ")
}

// printInboxItem prints pekoed's part first (who asked, facts, links,
// the answer), then the agent's words fenced. full: with the context.
func printInboxItem(it state.InboxItem, full bool) {
	head := fmt.Sprintf("#%d %s %s · from %s", it.ID, it.Kind, it.Status, it.From)
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
	for _, l := range it.Links {
		fmt.Println("  link: " + untrusted.Line(l))
	}
	if a := it.Answer; a != nil {
		s := "  " + it.Status + " by " + a.By
		if a.Choice != nil && *a.Choice < len(it.Options) {
			s += fmt.Sprintf(": %d %s", *a.Choice+1, untrusted.Line(it.Options[*a.Choice]))
		}
		if a.Note != "" {
			s += " · note: " + untrusted.Line(a.Note)
		}
		if it.Delivery != state.DeliveryNone {
			s += " · delivery " + it.Delivery
		}
		fmt.Println(s)
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
