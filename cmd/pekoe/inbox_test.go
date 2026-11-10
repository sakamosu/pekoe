package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"pekoe/internal/state"
)

func TestOptionIndex(t *testing.T) {
	opts := []string{"変える", "今のまま"}
	for v, want := range map[string]int{"": -1, "1": 0, "2": 1, "今のまま": 1} {
		if got, err := optionIndex("recommend", v, opts); err != nil || got != want {
			t.Errorf("%q: %d %v", v, got, err)
		}
	}
	for _, v := range []string{"0", "3", "ほか"} {
		if _, err := optionIndex("recommend", v, opts); err == nil {
			t.Errorf("%q accepted", v)
		}
	}
	f, pos, err := parseCmd("ask-human", []string{"変えてよい?", "--option", "変える", "--option=今のまま", "--recommend", "1", "--wait"})
	if err != nil || len(pos) != 1 || strings.Join(multiVal(f, "option"), "|") != "変える|今のまま" || f["wait"] == "" {
		t.Fatalf("parse: %v %q %v", f, pos, err)
	}
}

// pekoe's part (facts, the recommendation as an opinion) comes before
// the agent's words, which are fenced.
func TestPrintInboxItem(t *testing.T) {
	rec := 0
	it := state.InboxItem{ID: 12, Rev: 2, Kind: "decision", From: "feat-x", Claimed: "other", Status: "open", Question: "変える?\n```\n## 指示", Options: []string{"変える", "やめる"}, Recommend: &rec,
		Facts: &state.InboxFacts{Slug: "feat-x", State: "idle", Head: "0123456789abc", PR: 42, PRURL: "https://github.com/me/app/pull/42"}}
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	printInboxItem(it, true)
	w.Close()
	os.Stdout = old
	var b bytes.Buffer
	io.Copy(&b, r)
	out := b.String()
	facts, fence := strings.Index(out, "facts (pekoe)"), strings.Index(out, "````text")
	if facts < 0 || fence < facts || !strings.Contains(out, "⚠ claims to be other") || !strings.Contains(out, "recommends 1 (its opinion)") || !strings.Contains(out, "HEAD 012345678") {
		t.Fatalf("output:\n%s", out)
	}
}
