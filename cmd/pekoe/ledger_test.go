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
	for _, args := range [][]string{{"done", "x"}, {"drop", "3"}, {"close", "3"}, {"link", "3"}, {"keep", "x"}, {"keep", "3", "4"}} {
		err := cmdLedger(args)
		if _, ok := err.(*usageError); !ok {
			t.Errorf("%v: want a usage error before connecting, got %v", args, err)
		}
	}
}

// The list sets what pekoed closed itself apart from the open requests,
// and says how one comes back.
func TestLedgerListOutput(t *testing.T) {
	at := time.Date(2026, 11, 15, 10, 0, 0, 0, time.Local)
	closed := at.Add(time.Minute)
	es := []state.LedgerEntry{
		{ID: 1, At: at, Text: "B が済んだら C も直して", Status: state.LedgerChat, Auto: true, ClosedAt: &closed},
		{ID: 2, At: at, Text: "CSV を直して", Status: state.LedgerOpen, Sessions: []string{"fix-csv"}, Repeats: 1},
		{ID: 3, At: at, Text: "ありがと", Status: state.LedgerChat, Auto: true, ClosedAt: &closed},
	}
	out, _ := capture(t, func() error { printLedgerList(es, false); return nil })
	order := []string{"#2 open · 11-15 10:00 · sent ×2 · sessions fix-csv", "CSV を直して",
		"closed by pekoe as no request in the last 24h (2)", "pekoe ledger keep <id>",
		"#1 chat (auto) · 11-15 10:00 · closed 11-15 10:01", "B が済んだら C も直して", "#3 chat (auto)", "ありがと"}
	pos := 0
	for _, want := range order {
		i := strings.Index(out[pos:], want)
		if i < 0 {
			t.Fatalf("%q missing or out of order:\n%s", want, out)
		}
		pos += i + len(want)
	}
	out, _ = capture(t, func() error { printLedgerList(es[:1], false); return nil })
	if !strings.HasPrefix(out, "no open requests\n") || !strings.Contains(out, "#1 chat (auto)") {
		t.Errorf("nothing open, one closed by pekoed:\n%s", out)
	}
	out, _ = capture(t, func() error { printLedgerList(nil, false); return nil })
	if out != "no open requests\n" {
		t.Errorf("empty: %q", out)
	}
	// --all: one list in the order asked, no heading
	out, _ = capture(t, func() error { printLedgerList(es, true); return nil })
	if strings.Contains(out, "closed by pekoe") || strings.Index(out, "#1 chat (auto)") > strings.Index(out, "#2 open") {
		t.Errorf("--all:\n%s", out)
	}
}
