package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pekoe/internal/daemon"
	"pekoe/internal/gitx"
	"pekoe/internal/state"
)

// A broken config.toml is reported before the review starts (it used to
// dereference a nil config after review.start, leaving the session in
// review).
func TestRunReviewBrokenConfig(t *testing.T) {
	f, cl := startFakePekoed(t)
	home := t.TempDir()
	t.Setenv("PEKOE_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config", "config.toml"), []byte("[review\nsurface = "), 0o644); err != nil {
		t.Fatal(err)
	}
	err := RunReview(cl, "api-1")
	if err == nil || !strings.Contains(err.Error(), "config.toml") {
		t.Fatalf("err %v", err)
	}
	if n := len(f.find("review.start")); n != 0 {
		t.Fatalf("review.start called %d times", n)
	}
}

// Comments sit under the line they were made on: a '+' line rather than
// the '-' line with the same number before it, and in their own hunk even
// when an earlier hunk has a '-' line with that number.
func TestReviewCommentPlacement(t *testing.T) {
	var del []gitx.Line
	for n := 40; n < 60; n++ {
		del = append(del, gitx.Line{Kind: "-", Old: n, Text: "gone"})
	}
	r := &reviewModel{rv: &daemon.ReviewView{
		Files: []daemon.FileView{{Path: "a.go", Hunks: []daemon.HunkView{
			{Hunk: gitx.Hunk{ID: "h1", Lines: []gitx.Line{{Kind: "-", Old: 10, Text: "foo"}, {Kind: "+", New: 10, Text: "bar"}}}},
			{Hunk: gitx.Hunk{ID: "h2", Lines: del}},
			{Hunk: gitx.Hunk{ID: "h3", Lines: []gitx.Line{{Kind: " ", Old: 64, New: 44, Text: "ctx"}, {Kind: "+", New: 45, Text: "added"}}}},
		}}},
		Comments: []state.Comment{
			{ID: 1, Path: "a.go", Line: 10, HunkID: "h1", Text: "on bar"},
			{ID: 2, Path: "a.go", Line: 45, HunkID: "h3", Text: "on added"},
			{ID: 3, Path: "a.go", Line: 59, Text: "no hunk id"},
		},
	}}
	r.build()
	under := map[string]string{} // comment → the line above it
	prev := ""
	for _, d := range r.rows {
		switch d.kind {
		case "line":
			prev = d.hunkID + ":" + d.line.Kind + d.line.Text
		case "comment":
			under[d.text] = prev
		}
	}
	want := map[string]string{"on bar": "h1:+bar", "on added": "h3:+added", "no hunk id": "h2:-gone"}
	for text, w := range want {
		if under[text] != w {
			t.Errorf("%q under %q, want %q", text, under[text], w)
		}
	}
	if len(under) != len(want) {
		t.Errorf("comments shown: %v", under)
	}
}
