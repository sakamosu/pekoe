package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

// transcript_path comes from a hook payload, which an agent can send
// without the token. A device that never ends or a FIFO nobody writes to
// must not hang pekoed or eat its memory; a real transcript is still read,
// up to the limit.
func TestTailFileOnlyRegularFiles(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{fifo, dir, "/dev/zero"} {
		done := make(chan []byte, 1)
		go func() { done <- tailFile(p, 1024) }()
		select {
		case b := <-done:
			if b != nil {
				t.Errorf("%s: read %d bytes", p, len(b))
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s: tailFile did not return", p)
		}
	}
	tr := filepath.Join(dir, "t.jsonl")
	os.WriteFile(tr, []byte(strings.Repeat("x", 100)+"\n"+`{"type":"assistant","message":{"role":"assistant","content":"done"}}`+"\n"), 0o600)
	if got := lastAssistantText(tr); got != "done" {
		t.Fatalf("transcript: %q", got)
	}
	if b := tailFile(tr, 10); len(b) > 10 {
		t.Fatalf("read past the limit: %d bytes", len(b))
	}
	// a symlink to a regular file (CLAUDE_CONFIG_DIR on another disk) is fine
	ln := filepath.Join(dir, "ln.jsonl")
	os.Symlink(tr, ln)
	if got := lastAssistantText(ln); got != "done" {
		t.Fatalf("through a symlink: %q", got)
	}
}

// chatFixture is a transcript as Claude Code writes it: the person, the
// agent's thinking / words / tool calls, the tool results (as "user"
// lines), system injections, a subagent's line and pekoed's own typing.
// Every *_SECRET must stay out of what readChat returns.
var chatFixture = []string{
	`{"type":"permission-mode","permissionMode":"default"}`,
	`{"type":"user","timestamp":"2026-11-12T11:58:00Z","origin":{"kind":"human"},"message":{"role":"user","content":"今日中にやること整理して"}}`,
	`{"type":"assistant","timestamp":"2026-11-12T11:58:03Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"THINK_SECRET"}]}}`,
	`{"type":"assistant","timestamp":"2026-11-12T11:58:04Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"gh release list ` + strings.Repeat("x", 300) + `","description":"DESC_NOT_MAIN"}}]}}`,
	`{"type":"user","timestamp":"2026-11-12T11:58:05Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"TOOL_RESULT_SECRET"}]},"toolUseResult":{"stdout":"TOOL_RESULT_SECRET"}}`,
	`{"type":"user","timestamp":"2026-11-12T11:58:05Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":[{"type":"text","text":"TOOL_RESULT_TEXT_SECRET"}]},{"type":"text","text":"ALONGSIDE_RESULT_SECRET"}]}}`,
	`{"type":"user","timestamp":"2026-11-12T11:58:06Z","isMeta":true,"message":{"role":"user","content":"META_SECRET"}}`,
	`{"type":"user","timestamp":"2026-11-12T11:58:06Z","message":{"role":"user","content":[{"type":"text","text":"<system-reminder>REMINDER_SECRET</system-reminder>"}]}}`,
	`{"type":"assistant","timestamp":"2026-11-12T11:58:07Z","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"SIDECHAIN_SECRET"}]}}`,
	`{"type":"assistant","timestamp":"2026-11-12T11:58:20Z","message":{"role":"assistant","content":[{"type":"text","text":"## 1. 了解\n- [ ] ` + "`pekoe`" + ` を出す"}]}}`,
	`{"type":"system","subtype":"stop_hook_summary","timestamp":"2026-11-12T11:58:20Z","hookInfos":[{"command":"HOOK_SECRET"}]}`,
	`{"type":"system","subtype":"turn_duration","timestamp":"2026-11-12T11:58:21Z","durationMs":17000,"isMeta":false}`,
	`{"type":"user","timestamp":"2026-11-12T12:00:00Z","isCompactSummary":true,"isVisibleInTranscriptOnly":true,"message":{"role":"user","content":"COMPACT_SECRET"}}`,
	`{"type":"user","timestamp":"2026-11-12T12:00:01Z","message":{"role":"user","content":"<command-name>/clear</command-name>\n<command-message>COMMAND_SECRET</command-message>"}}`,
	`{"type":"user","timestamp":"2026-11-12T12:00:01Z","message":{"role":"user","content":"<local-command-stdout>LOCAL_SECRET</local-command-stdout>"}}`,
	`{"type":"user","timestamp":"2026-11-12T12:00:02Z","origin":{"kind":"task-notification"},"message":{"role":"user","content":"<task-notification>TASK_SECRET</task-notification>"}}`,
	`{"type":"user","timestamp":"2026-11-12T12:00:03Z","origin":{"kind":"human"},"message":{"role":"user","content":"[pekoe inbox #3] 答え: yes\nsecond line"}}`,
	`{"type":"assistant","timestamp":"2026-11-12T12:00:04Z","message":{"role":"assistant","model":"<synthetic>","content":[{"type":"text","text":"SYNTHETIC_SECRET"}]}}`,
	`{"type":"assistant","timestamp":"2026-11-12T12:00:05Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"t3","name":"AskUserQuestion","input":{"questions":[{"question":"どっちから？","header":"order","options":[{"label":"証明書","description":"DESC_SECRET"},{"label":"パスワード"}]}]}}]}}`,
	`not json at all`,
}

func writeChat(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendChat(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(s)
	f.Close()
}

func TestReadChat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "11111111-aaaa.jsonl")
	writeChat(t, path, chatFixture)
	page, err := readChat(path, "")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(page)
	for _, secret := range []string{"THINK_SECRET", "TOOL_RESULT", "ALONGSIDE_RESULT", "META_SECRET", "REMINDER_SECRET",
		"SIDECHAIN_SECRET", "HOOK_SECRET", "COMPACT_SECRET", "COMMAND_SECRET", "LOCAL_SECRET", "TASK_SECRET",
		"SYNTHETIC_SECRET", "DESC_NOT_MAIN", "DESC_SECRET", "thinking"} {
		if strings.Contains(string(b), secret) {
			t.Errorf("%s in the page: %s", secret, b)
		}
	}
	var kinds []string
	for _, it := range page.Items {
		kinds = append(kinds, it.Kind)
	}
	if got := strings.Join(kinds, " "); got != "human tool agent turn pekoe tool" {
		t.Fatalf("kinds: %s\n%s", got, b)
	}
	it := page.Items
	if it[0].Text != "今日中にやること整理して" || it[0].At.IsZero() {
		t.Errorf("human: %+v", it[0])
	}
	// the tool's main argument, cut at 200 bytes
	if it[1].Name != "Bash" || !strings.HasPrefix(it[1].Input, "gh release list xxx") || len(it[1].Input) > 200+len("…") || !strings.HasSuffix(it[1].Input, "…") {
		t.Errorf("tool: %+v (%d bytes)", it[1], len(it[1].Input))
	}
	if !strings.HasPrefix(it[2].Text, "## 1. 了解") {
		t.Errorf("agent: %+v", it[2])
	}
	if it[3].MS != 17000 {
		t.Errorf("turn: %+v", it[3])
	}
	if it[4].Text != "[pekoe inbox #3] 答え: yes second line" {
		t.Errorf("pekoe: %q", it[4].Text)
	}
	if it[5].Name != "AskUserQuestion" || it[5].Question != "どっちから？\n1. 証明書\n2. パスワード" || it[5].Input != "" {
		t.Errorf("question: %+v", it[5])
	}
	if !page.Reset || page.NewConversation || page.Earlier || page.Conversation != "11111111-aaaa" {
		t.Errorf("page: reset %v new %v earlier %v conv %q", page.Reset, page.NewConversation, page.Earlier, page.Conversation)
	}
	fi, _ := os.Stat(path)
	if page.Cursor != chatCursor("11111111-aaaa", fi.Size()) {
		t.Errorf("cursor %q, size %d", page.Cursor, fi.Size())
	}
}

// After a cursor only what is new comes back; a line still being written
// waits for its newline; a cursor of another conversation starts over and
// says so; a transcript that got shorter starts over.
func TestReadChatCursor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conv-a.jsonl")
	writeChat(t, path, chatFixture[:3])
	p1, _ := readChat(path, "")
	if len(p1.Items) != 1 {
		t.Fatalf("first: %+v", p1.Items)
	}
	p2, _ := readChat(path, p1.Cursor)
	if len(p2.Items) != 0 || p2.Reset || p2.Cursor != p1.Cursor {
		t.Fatalf("nothing new: %+v", p2)
	}
	half := `{"type":"user","timestamp":"2026-11-12T13:00:00Z","message":{"role":"user","content":"つぎ"}}`
	appendChat(t, path, chatFixture[9]+"\n"+half[:30])
	p3, _ := readChat(path, p2.Cursor)
	if len(p3.Items) != 1 || p3.Items[0].Kind != "agent" || p3.Reset {
		t.Fatalf("new line: %+v", p3)
	}
	appendChat(t, path, half[30:]+"\n")
	p4, _ := readChat(path, p3.Cursor)
	if len(p4.Items) != 1 || p4.Items[0].Text != "つぎ" {
		t.Fatalf("completed line: %+v", p4)
	}
	// /clear: tapi's hooks name another file
	other := filepath.Join(dir, "conv-b.jsonl")
	writeChat(t, other, chatFixture[1:2])
	p5, _ := readChat(other, p4.Cursor)
	if !p5.Reset || !p5.NewConversation || len(p5.Items) != 1 || p5.Conversation != "conv-b" {
		t.Fatalf("another conversation: %+v", p5)
	}
	// not written yet (cleared, nothing said): empty, a new conversation
	p6, err := readChat(filepath.Join(dir, "conv-c.jsonl"), p5.Cursor)
	if err != nil || !p6.NewConversation || len(p6.Items) != 0 || p6.Cursor != "conv-c:0" {
		t.Fatalf("not written yet: %+v %v", p6, err)
	}
	// shorter than the cursor: read again from the start
	writeChat(t, path, chatFixture[1:2])
	p7, _ := readChat(path, p4.Cursor)
	if !p7.Reset || p7.NewConversation || len(p7.Items) != 1 {
		t.Fatalf("shrunk: %+v", p7)
	}
	// a cursor that does not parse is a first read
	for _, bad := range []string{"x", "conv-a:-1", "../x:3", ":5"} {
		if p, err := readChat(path, bad); err != nil || !p.Reset {
			t.Fatalf("cursor %q: %+v %v", bad, p, err)
		}
	}
	if _, err := readChat(filepath.Join(dir, "not a transcript.txt"), ""); err == nil {
		t.Fatal("read a file that is no transcript")
	}
}

// A long transcript is read from its end on the first read; long words are
// cut, a line too long to be a message is skipped.
func TestReadChatLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conv.jsonl")
	long := strings.Repeat("あ", chatMaxText)
	lines := []string{
		`{"type":"user","timestamp":"2026-11-12T13:00:00Z","message":{"role":"user","content":"` + long + `"}}`,
		`{"type":"api-request-blob","x":"` + strings.Repeat("y", chatMaxLine+10) + `"}`,
	}
	for i := 0; i < chatMaxItems+20; i++ {
		lines = append(lines, `{"type":"assistant","timestamp":"2026-11-12T13:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"n`+strconv.Itoa(i)+`"}]}}`)
	}
	writeChat(t, path, lines)
	p, err := readChat(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != chatMaxItems || !p.Earlier || p.Items[len(p.Items)-1].Text != "n"+strconv.Itoa(chatMaxItems+19) {
		t.Fatalf("items %d earlier %v last %+v", len(p.Items), p.Earlier, p.Items[len(p.Items)-1])
	}
	c := chatItems([]byte(lines[0]))
	if len(c) != 1 || len(c[0].Text) > chatMaxText+len("…") || !utf8.ValidString(c[0].Text) || !strings.HasSuffix(c[0].Text, "…") {
		t.Fatalf("long text: %d bytes", len(c[0].Text))
	}
}
