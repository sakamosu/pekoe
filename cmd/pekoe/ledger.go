package main

// ledger (docs/orchestration.md "6. 依頼(ledger)"): what a person asked tapi
// chat for. pekoed records every prompt typed into tapi; tapi reads the
// open ones back (after a reset too), ties sessions to them and closes
// them. pekoed closes the input of a turn in which tapi asked nobody for
// anything as no request; `ledger keep` keeps it, or opens such an entry
// again. Reopening what tapi or a person closed and deleting are a
// person's.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"pekoe/internal/state"
	"pekoe/internal/untrusted"
)

var ledgerSubs = map[string]bool{"done": true, "drop": true, "close": true, "link": true, "keep": true, "reopen": true, "rm": true}

func cmdLedger(args []string) error {
	name := "ledger"
	if len(args) > 0 && ledgerSubs[args[0]] {
		name, args = "ledger "+args[0], args[1:]
	}
	f, pos, err := parseCmd(name, args)
	if err != nil {
		return err
	}
	use := specs[name].use
	if name == "ledger" {
		if len(pos) > 1 {
			return &usageError{use: use, msg: "unexpected argument " + pos[1]}
		}
		if len(pos) == 1 {
			return ledgerShow(use, pos[0], f["all"] != "")
		}
		cl := connect()
		defer cl.Close()
		if f["all"] != "" {
			var es []state.LedgerEntry
			if err := cl.Call("ledger.list", map[string]any{"status": "all"}, &es); err != nil {
				return err
			}
			if jsonOut {
				printJSON(es)
				return nil
			}
			printLedgerList(&state.LedgerView{Open: es}, true)
			return nil
		}
		// auto: with the open ones, the newest of what pekoed closed itself
		// in the last 24 hours, short (a request among them is opened again
		// with ledger keep)
		var raw json.RawMessage
		if err := cl.Call("ledger.list", map[string]any{"status": "open", "auto": true}, &raw); err != nil {
			return err
		}
		v, err := parseLedgerView(raw)
		if err != nil {
			return err
		}
		if jsonOut {
			printJSON(v)
			return nil
		}
		printLedgerList(v, false)
		return nil
	}
	sub := strings.TrimPrefix(name, "ledger ")
	want := 1
	if sub == "link" {
		want = 2
	}
	if len(pos) < want && !(sub == "keep" && len(pos) == 0) {
		return &usageError{use: use, msg: "missing arguments"}
	}
	if len(pos) > want {
		return &usageError{use: use, msg: "unexpected argument " + pos[want]}
	}
	var id int64 // stays 0 for `ledger keep` alone: the input of tapi's turn
	if len(pos) > 0 {
		id, err = strconv.ParseInt(strings.TrimPrefix(pos[0], "#"), 10, 64)
		if err != nil || id < 1 {
			return &usageError{use: use, msg: fmt.Sprintf("bad request id %q", pos[0])}
		}
	}
	method, p := "", map[string]any{"id": id}
	switch sub {
	case "keep":
		method = "ledger.keep"
	case "done", "drop", "close":
		as := map[string]string{"done": state.LedgerDone, "drop": state.LedgerDropped}[sub]
		if sub == "close" {
			if as = f["as"]; as == "" {
				return &usageError{use: use, msg: "--as done|dropped|chat required"}
			}
		}
		if sub == "drop" && f["note"] == "" {
			return &usageError{use: use, msg: "--note required: say why"}
		}
		method = "ledger.close"
		p["as"], p["evidence"], p["note"] = as, multiVal(f, "evidence"), f["note"]
	case "link":
		method, p["slug"] = "ledger.link", pos[1]
	case "reopen":
		method = "ledger.reopen"
	case "rm":
		if f["yes"] == "" {
			if !stdinIsTerminal() {
				return fmt.Errorf("ledger rm deletes request #%d for good; pass --yes to confirm", id)
			}
			if !confirm(fmt.Sprintf("delete request #%d from the ledger?", id)) {
				return nil
			}
		}
		method = "ledger.delete"
	}
	cl := connect()
	defer cl.Close()
	var e state.LedgerEntry
	if err := cl.Call(method, p, &e); err != nil {
		return err
	}
	if sub == "rm" {
		if jsonOut {
			printJSON(map[string]any{"id": id, "deleted": true})
		} else {
			fmt.Printf("request #%d deleted\n", id)
		}
		return nil
	}
	if jsonOut {
		printJSON(e)
		return nil
	}
	fmt.Printf("request #%d %s", e.ID, e.Status)
	if len(e.Sessions) > 0 {
		fmt.Printf(" · sessions %s", strings.Join(e.Sessions, ", "))
	}
	fmt.Println()
	return nil
}

// parseLedgerView reads ledger.list with auto: a LedgerView, or the plain
// list of open entries an older pekoed answers (it knows no auto).
func parseLedgerView(raw json.RawMessage) (*state.LedgerView, error) {
	v := &state.LedgerView{}
	if t := strings.TrimSpace(string(raw)); strings.HasPrefix(t, "[") || t == "null" {
		if err := json.Unmarshal(raw, &v.Open); err != nil {
			return nil, err
		}
	} else if err := json.Unmarshal(raw, v); err != nil {
		return nil, err
	}
	if v.Open == nil {
		v.Open = []state.LedgerEntry{}
	}
	if v.AutoClosed == nil {
		v.AutoClosed = []state.LedgerEntry{}
	}
	return v, nil
}

// ledgerShow prints one entry whole (the list cuts what pekoed closed
// itself short).
func ledgerShow(use, arg string, all bool) error {
	if all {
		return &usageError{use: use, msg: "--all lists every request; give an id or --all, not both"}
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(arg, "#"), 10, 64)
	if err != nil || id < 1 {
		return &usageError{use: use, msg: fmt.Sprintf("bad request id %q", arg)}
	}
	cl := connect()
	defer cl.Close()
	var es []state.LedgerEntry
	if err := cl.Call("ledger.list", map[string]any{"status": "all", "since": id - 1}, &es); err != nil {
		return err
	}
	if len(es) == 0 || es[0].ID != id {
		return fmt.Errorf("no such ledger entry: #%d", id)
	}
	if jsonOut {
		printJSON(es[0])
		return nil
	}
	printLedger(es[0])
	return nil
}

// printLedgerList prints the open entries (with all: every entry, in the
// order asked) and then, apart, the newest of what pekoed closed itself
// lately, short.
func printLedgerList(v *state.LedgerView, all bool) {
	for i, e := range v.Open {
		if i > 0 {
			fmt.Println()
		}
		printLedger(e)
	}
	if len(v.Open) == 0 {
		if all {
			fmt.Println("no requests")
		} else {
			fmt.Println("no open requests")
		}
	}
	n := len(v.AutoClosed) + v.AutoClosedMore
	if n == 0 {
		return
	}
	fmt.Printf("\nclosed by pekoe as no request in the last 24h (%d) · a request among them: pekoe ledger keep <id>\n", n)
	for _, e := range v.AutoClosed {
		fmt.Println()
		printLedger(e)
	}
	if v.AutoClosedMore > 0 {
		fmt.Printf("\n+%d older (pekoe ledger --all)\n", v.AutoClosedMore)
	}
}

// printLedger prints pekoed's part first, then the words: the request is
// what the person typed (it may hold pasted text), the note tapi's.
func printLedger(e state.LedgerEntry) {
	st := e.Status
	if e.Auto {
		st += " (auto)" // pekoed closed it when tapi's turn ended
	}
	head := fmt.Sprintf("#%d %s · %s", e.ID, st, e.At.Local().Format("01-02 15:04"))
	if e.Repeats > 0 {
		head += fmt.Sprintf(" · sent ×%d", e.Repeats+1)
	}
	if len(e.Sessions) > 0 {
		head += " · sessions " + strings.Join(e.Sessions, ", ")
	}
	if e.ClosedAt != nil {
		head += " · closed " + e.ClosedAt.Local().Format("01-02 15:04")
	}
	if e.Cut {
		head += fmt.Sprintf(" · cut (pekoe ledger %d)", e.ID)
	}
	fmt.Println(head)
	for _, ev := range e.Evidence {
		fmt.Println("  evidence: " + untrusted.Line(ev))
	}
	if e.Note != "" {
		fmt.Println("  note: " + untrusted.Line(e.Note))
	}
	fmt.Println(untrusted.Fence("request", e.Text))
}
