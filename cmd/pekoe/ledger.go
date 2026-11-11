package main

// ledger (docs/orchestration.md "6. 依頼(ledger)"): what a person asked tapi
// chat for. pekoed records every prompt typed into tapi; tapi reads the
// open ones back (after a reset too), ties sessions to them and closes
// them. Reopening and deleting are a person's.

import (
	"fmt"
	"strconv"
	"strings"

	"pekoe/internal/state"
	"pekoe/internal/untrusted"
)

var ledgerSubs = map[string]bool{"done": true, "drop": true, "close": true, "link": true, "reopen": true, "rm": true}

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
		if len(pos) > 0 {
			return &usageError{use: use, msg: "unexpected argument " + pos[0]}
		}
		st := "open"
		if f["all"] != "" {
			st = "all"
		}
		cl := connect()
		defer cl.Close()
		var es []state.LedgerEntry
		if err := cl.Call("ledger.list", map[string]any{"status": st}, &es); err != nil {
			return err
		}
		if jsonOut {
			printJSON(es)
			return nil
		}
		if len(es) == 0 {
			fmt.Println("no open requests")
			return nil
		}
		for i, e := range es {
			if i > 0 {
				fmt.Println()
			}
			printLedger(e)
		}
		return nil
	}
	sub := strings.TrimPrefix(name, "ledger ")
	want := 1
	if sub == "link" {
		want = 2
	}
	if len(pos) < want {
		return &usageError{use: use, msg: "missing arguments"}
	}
	if len(pos) > want {
		return &usageError{use: use, msg: "unexpected argument " + pos[want]}
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(pos[0], "#"), 10, 64)
	if err != nil || id < 1 {
		return &usageError{use: use, msg: fmt.Sprintf("bad request id %q", pos[0])}
	}
	method, p := "", map[string]any{"id": id}
	switch sub {
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

// printLedger prints pekoed's part first, then the words: the request is
// what the person typed (it may hold pasted text), the note tapi's.
func printLedger(e state.LedgerEntry) {
	head := fmt.Sprintf("#%d %s · %s", e.ID, e.Status, e.At.Local().Format("01-02 15:04"))
	if len(e.Sessions) > 0 {
		head += " · sessions " + strings.Join(e.Sessions, ", ")
	}
	if e.ClosedAt != nil {
		head += " · closed " + e.ClosedAt.Local().Format("01-02 15:04")
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
