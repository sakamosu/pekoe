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

// claude --continue (tapi.restart, a session that could not be resumed)
// starts a new transcript with the conversation so far copied into it, and
// writes a "continued-in" line naming it at the end of the old one. That is
// the same conversation going on: the page starts over from the new
// transcript, with no "new conversation" line and the words drawn once.
func TestReadChatContinuedIn(t *testing.T) {
	dir := t.TempDir()
	conv := []string{
		`{"parentUuid":null,"type":"user","message":{"role":"user","content":"first ask"},"uuid":"u1","timestamp":"2026-11-21T10:00:00Z","origin":{"kind":"human"}}`,
		`{"parentUuid":"u1","type":"assistant","message":{"model":"claude-opus-5-5","content":[{"type":"text","text":"done"}]},"uuid":"a1","timestamp":"2026-11-21T10:00:05Z"}`,
	}
	oldID, newID := "aaaaaaaa-1111", "bbbbbbbb-2222"
	p1 := filepath.Join(dir, oldID+".jsonl")
	p2 := filepath.Join(dir, newID+".jsonl")
	writeChat(t, p1, conv)
	first, err := readChat(p1, "")
	if err != nil || len(first.Items) != 2 {
		t.Fatalf("first read: %+v %v", first, err)
	}
	// Claude Code starts again with --continue: the line in the old
	// transcript comes at once, the new transcript only with the first
	// prompt (and the hooks name it before it is there)
	appendChat(t, p1, `{"type":"continued-in","timestamp":"2026-11-21T11:00:00Z","sessionId":"`+oldID+`","continuedInSessionId":"`+newID+`"}`+"\n"+
		`{"type":"last-prompt","sessionId":"`+oldID+`"}`+"\n")
	wait, err := readChat(p2, first.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if wait.Reset || wait.NewConversation || len(wait.Items) != 0 || wait.Cursor != first.Cursor {
		t.Fatalf("before the copy is written: %+v (cursor was %q)", wait, first.Cursor)
	}
	// a page back in the old one meanwhile reads the old one
	back := chatCursor(oldID, int64(len(conv[0])+1))
	if p, err := readChatBefore(p2, back); err != nil || len(p.Items) != 1 || p.Items[0].Text != "first ask" || p.Before != "" {
		t.Fatalf("page back before the copy: %+v %v", p, err)
	}
	writeChat(t, p2, append(append([]string{}, conv...),
		`{"parentUuid":"a1","type":"user","message":{"role":"user","content":"after the restart"},"uuid":"u2","timestamp":"2026-11-21T11:00:10Z","origin":{"kind":"human"}}`))
	next, err := readChat(p2, wait.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if !next.Reset || next.NewConversation || next.Conversation != newID {
		t.Fatalf("reset %v new_conversation %v conversation %q", next.Reset, next.NewConversation, next.Conversation)
	}
	var texts []string
	for _, it := range next.Items {
		texts = append(texts, it.Text)
	}
	if got := strings.Join(texts, "|"); got != "first ask|done|after the restart" {
		t.Fatalf("items %q", got)
	}
	if _, err := readChatBefore(p2, back); err != errChatMoved {
		t.Fatalf("page back in the old one after the copy: %v", err)
	}

	// /clear (no continued-in for it) is still another conversation
	p3 := filepath.Join(dir, "cccccccc-3333.jsonl")
	writeChat(t, p3, []string{`{"type":"user","message":{"role":"user","content":"fresh"},"uuid":"c1","timestamp":"2026-11-21T12:00:00Z","origin":{"kind":"human"}}`})
	cleared, err := readChat(p3, next.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if !cleared.Reset || !cleared.NewConversation || len(cleared.Items) != 1 {
		t.Fatalf("/clear: %+v", cleared)
	}
	// a cleared conversation not written yet is a new one at once, as before
	gone, err := readChat(filepath.Join(dir, "dddddddd-4444.jsonl"), cleared.Cursor)
	if err != nil || !gone.Reset || !gone.NewConversation {
		t.Fatalf("an empty new conversation: %+v %v", gone, err)
	}
}
