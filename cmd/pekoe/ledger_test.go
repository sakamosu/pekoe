package main

import (
	"strings"
	"testing"
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
	for _, args := range [][]string{{"done", "x"}, {"drop", "3"}, {"close", "3"}, {"link", "3"}} {
		err := cmdLedger(args)
		if _, ok := err.(*usageError); !ok {
			t.Errorf("%v: want a usage error before connecting, got %v", args, err)
		}
	}
}
