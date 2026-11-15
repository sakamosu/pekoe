package main

import (
	"strings"
	"testing"
	"time"

	"pekoe/internal/state"
)

func TestLedgerFlags(t *testing.T) {
	f, pos, err := parseCmd("ledger done", []string{"3", "--evidence", "https://x/pull/1", "--evidence=/tmp/a.png", "--note", "PR 出た"})
	if err != nil || len(pos) != 1 || pos[0] != "3" || strings.Join(multiVal(f, "evidence"), "|") != "https://x/pull/1|/tmp/a.png" || f["note"] != "PR 出た" {
		t.Fatalf("done: %v %q %v", f, pos, err)
	}
	if f, _, err := parseCmd("ledger close", []string{"3", "--as", "chat"}); err != nil || f["as"] != "chat" {
		t.Fatalf("close: %v %v", f, err)
	}
	if f, _, err := parseCmd("new", []string{"feat/x", "--request", "4"}); err != nil || f["request"] != "4" {
		t.Fatalf("new --request: %v %v", f, err)
	}
	for _, args := range [][]string{{"done", "x"}, {"drop", "3"}, {"close", "3"}, {"link", "3"}, {"keep", "x"}, {"keep", "3", "4"}, {"x"}, {"3", "4"}, {"3", "--all"}} {
		err := cmdLedger(args)
		if _, ok := err.(*usageError); !ok {
			t.Errorf("%v: want a usage error before connecting, got %v", args, err)
		}
	}
}

// The list sets what pekoed closed itself apart from the open requests,
// says how one comes back, how many more there are, and where a cut text
// is whole.
func TestLedgerListOutput(t *testing.T) {
	at := time.Date(2026, 11, 15, 10, 0, 0, 0, time.Local)
	closed := at.Add(time.Minute)
	v := &state.LedgerView{
		Open: []state.LedgerEntry{{ID: 2, At: at, Text: "CSV を直して", Status: state.LedgerOpen, Sessions: []string{"fix-csv"}, Repeats: 1}},
		AutoClosed: []state.LedgerEntry{
			{ID: 1, At: at, Text: "B が済んだら C も直して", Status: state.LedgerChat, Auto: true, ClosedAt: &closed, Cut: true},
			{ID: 3, At: at, Text: "ありがと", Status: state.LedgerChat, Auto: true, ClosedAt: &closed},
		},
		AutoClosedMore: 4,
	}
	out, _ := capture(t, func() error { printLedgerList(v, false); return nil })
	order := []string{"#2 open · 11-15 10:00 · sent ×2 · sessions fix-csv", "CSV を直して",
		"closed by pekoe as no request in the last 24h (6)", "pekoe ledger keep <id>",
		"#1 chat (auto) · 11-15 10:00 · closed 11-15 10:01 · cut (pekoe ledger 1)", "B が済んだら C も直して", "#3 chat (auto)", "ありがと",
		"+4 older (pekoe ledger --all)"}
	pos := 0
	for _, want := range order {
		i := strings.Index(out[pos:], want)
		if i < 0 {
			t.Fatalf("%q missing or out of order:\n%s", want, out)
		}
		pos += i + len(want)
	}
	out, _ = capture(t, func() error {
		printLedgerList(&state.LedgerView{AutoClosed: v.AutoClosed[1:]}, false)
		return nil
	})
	if !strings.HasPrefix(out, "no open requests\n") || !strings.Contains(out, "(1)") || strings.Contains(out, "older") {
		t.Errorf("nothing open, one closed by pekoed:\n%s", out)
	}
	out, _ = capture(t, func() error { printLedgerList(&state.LedgerView{}, false); return nil })
	if out != "no open requests\n" {
		t.Errorf("empty: %q", out)
	}
	out, _ = capture(t, func() error { printLedgerList(&state.LedgerView{Open: v.AutoClosed}, true); return nil })
	if strings.Contains(out, "closed by pekoe") || !strings.Contains(out, "#1 chat (auto)") {
		t.Errorf("--all:\n%s", out)
	}
}

// ledger.list with auto answers a view; an older pekoed, a list of the
// open ones.
func TestParseLedgerView(t *testing.T) {
	v, err := parseLedgerView([]byte(`{"open":[{"id":2,"status":"open"}],"auto_closed":[{"id":1,"status":"chat","auto":true,"cut":true}],"auto_closed_more":3}`))
	if err != nil || len(v.Open) != 1 || len(v.AutoClosed) != 1 || !v.AutoClosed[0].Cut || v.AutoClosedMore != 3 {
		t.Fatalf("view: %+v %v", v, err)
	}
	v, err = parseLedgerView([]byte(`[{"id":2,"status":"open"}]`))
	if err != nil || len(v.Open) != 1 || v.Open[0].ID != 2 || v.AutoClosed == nil || v.AutoClosedMore != 0 {
		t.Fatalf("an older pekoed: %+v %v", v, err)
	}
	if v, err = parseLedgerView([]byte(`null`)); err != nil || v.Open == nil {
		t.Fatalf("null: %+v %v", v, err)
	}
}
