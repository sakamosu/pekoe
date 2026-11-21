package daemon

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// A line longer than a page (a Read or a screenshot of 7 MiB in a tool
// result) is skipped, and the page back that ends right after it must still
// go on to the lines before it: it used to answer the cursor it was given,
// and Load earlier never got past the long line.
func TestReadChatBeforePastLongLine(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after int // lines after the long one
	}{
		{"lines after it", 3},
		{"the last line", 0}, // the first read is all of the long line
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cccccccc-3333.jsonl")
			var lines []string
			for i := 0; i < 5; i++ {
				lines = append(lines, fmt.Sprintf(`{"type":"user","message":{"role":"user","content":"old %d"},"uuid":"o%d","timestamp":"2026-11-21T09:00:0%dZ","origin":{"kind":"human"}}`, i, i, i))
			}
			big, _ := json.Marshal(strings.Repeat("x", chatTail+(1<<20)))
			lines = append(lines, `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":`+string(big)+`}]},"uuid":"big","timestamp":"2026-11-21T09:30:00Z"}`)
			for i := 0; i < tc.after; i++ {
				lines = append(lines, fmt.Sprintf(`{"type":"user","message":{"role":"user","content":"new %d"},"uuid":"n%d","timestamp":"2026-11-21T10:00:0%dZ","origin":{"kind":"human"}}`, i, i, i))
			}
			writeChat(t, path, lines)
			page, err := readChat(path, "")
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Items) != tc.after || !page.Earlier || page.Before == "" {
				t.Fatalf("first read: %d items, earlier %v, before %q", len(page.Items), page.Earlier, page.Before)
			}
			var seen []string
			before := page.Before
			for i := 0; before != ""; i++ {
				if i == 4 {
					t.Fatalf("still reading back after %d pages: %q", i, seen)
				}
				p, err := readChatBefore(path, before)
				if err != nil {
					t.Fatalf("page back %d: %v", i, err)
				}
				if p.Before == before {
					t.Fatalf("page back %d answered the cursor it was given (%q)", i, before)
				}
				var texts []string
				for _, it := range p.Items {
					texts = append(texts, it.Text)
				}
				seen = append(texts, seen...)
				before = p.Before
			}
			if got := strings.Join(seen, "|"); got != "old 0|old 1|old 2|old 3|old 4" {
				t.Fatalf("read back %q", got)
			}
		})
	}
}
