package daemon

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"pekoe/internal/state"
)

// chatLinesN: n agent lines "n0".."n<n-1>", one item each.
func chatLinesN(n int) []string {
	var lines []string
	for i := 0; i < n; i++ {
		lines = append(lines, `{"type":"assistant","timestamp":"2026-11-12T13:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"n`+strconv.Itoa(i)+`"}]}}`)
	}
	return lines
}

// A long transcript reads back page by page: the first read has the
// newest chatMaxItems and a before cursor; each page back ends where the
// one after it starts, and the last reaches the start. Together they are
// the whole conversation once, in order.
func TestReadChatBefore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conv-a.jsonl")
	total := chatMaxItems*2 + 37
	writeChat(t, path, chatLinesN(total))
	p, err := readChat(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != chatMaxItems || !p.Earlier || p.Before == "" {
		t.Fatalf("first: %d items, earlier %v, before %q", len(p.Items), p.Earlier, p.Before)
	}
	got := p.Items
	before := p.Before
	for pages := 0; before != ""; pages++ {
		if pages > 5 {
			t.Fatal("never reached the start")
		}
		b, err := readChatBefore(path, before)
		if err != nil {
			t.Fatal(err)
		}
		if b.Cursor != "" || len(b.Items) == 0 || len(b.Items) > chatMaxItems {
			t.Fatalf("page back: cursor %q, %d items", b.Cursor, len(b.Items))
		}
		got = append(append([]ChatItem{}, b.Items...), got...)
		before = b.Before
		if (before != "") != b.Earlier {
			t.Fatalf("earlier %v with before %q", b.Earlier, before)
		}
	}
	if len(got) != total {
		t.Fatalf("%d items in all, want %d", len(got), total)
	}
	for i, it := range got {
		if it.Text != "n"+strconv.Itoa(i) {
			t.Fatalf("item %d: %q", i, it.Text)
		}
	}
	// the cursor going on is the first read's: reading back moves nothing
	if q, _ := readChat(path, p.Cursor); len(q.Items) != 0 || q.Reset {
		t.Fatalf("after a page back: %+v", q)
	}
	// a short one has nothing before
	short := filepath.Join(t.TempDir(), "conv-s.jsonl")
	writeChat(t, short, chatLinesN(3))
	if s, _ := readChat(short, ""); s.Earlier || s.Before != "" {
		t.Fatalf("short: %+v", s)
	}
	// another conversation, a shorter file, a cursor that does not parse
	if _, err := readChatBefore(path, "conv-b:10"); !errors.Is(err, errChatMoved) {
		t.Fatalf("another conversation: %v", err)
	}
	if _, err := readChatBefore(path, "conv-a:999999999"); !errors.Is(err, errChatMoved) {
		t.Fatalf("past the end: %v", err)
	}
	for _, bad := range []string{"x", "conv-a:-1", "../x:3"} {
		if _, err := readChatBefore(path, bad); !errors.Is(err, errBadChatCursor) {
			t.Fatalf("cursor %q: %v", bad, err)
		}
	}
}

// A transcript that is a symlink, or in a directory that is one, is not
// read (it could point anywhere).
func TestReadChatNoSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "elsewhere.jsonl")
	writeChat(t, real, chatFixture)
	link := filepath.Join(dir, "conv-l.jsonl")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readChat(link, ""); !errors.Is(err, errNoTranscript) {
		t.Fatalf("read through a symlink: %v", err)
	}
	sub := filepath.Join(dir, "sub")
	os.MkdirAll(sub, 0o700)
	writeChat(t, filepath.Join(sub, "conv-d.jsonl"), chatFixture)
	ldir := filepath.Join(dir, "ldir")
	os.Symlink(sub, ldir)
	if _, err := readChat(filepath.Join(ldir, "conv-d.jsonl"), ""); !errors.Is(err, errNoTranscript) {
		t.Fatalf("read through a symlinked directory: %v", err)
	}
}

// chatSetup: the session main (Claude Code, in repo) and its project
// directory.
func chatSetup(t *testing.T) (*Daemon, string, string) {
	t.Helper()
	d, _, repo := setup(t)
	ss := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	return d, repo, claudeProjectDir(ss.Path)
}

// A session's chat reads only its own worktree's project directory: the
// conversation the hooks reported when it is there, else the newest
// there. A hook naming another session's conversation, or a path
// elsewhere, reads nothing outside it. What tapi's chat never returns
// (tool results, thinking, system injections, …) a session's never does.
func TestSessionTranscriptOnlyItsOwn(t *testing.T) {
	d, repo, own := chatSetup(t)
	// nothing written yet: no transcript
	if _, err := d.Call("session.transcript", mustJSON(map[string]any{"slug": "main"})); !errors.Is(err, errNoTranscript) {
		t.Fatalf("before any conversation: %v", err)
	}
	writeChat(t, filepath.Join(own, "conv-own.jsonl"), chatFixture)
	// another worktree's conversation, and a file outside every project
	other := claudeProjectDir(filepath.Join(filepath.Dir(repo), "other"))
	writeChat(t, filepath.Join(other, "conv-other.jsonl"), []string{`{"type":"user","timestamp":"2026-11-12T13:00:00Z","message":{"role":"user","content":"OTHER_SECRET"}}`})
	outside := filepath.Join(t.TempDir(), "conv-out.jsonl")
	writeChat(t, outside, []string{`{"type":"user","timestamp":"2026-11-12T13:00:00Z","message":{"role":"user","content":"OUTSIDE_SECRET"}}`})
	read := func() ChatPage {
		t.Helper()
		return call[ChatPage](t, d, "session.transcript", map[string]any{"slug": "main"})
	}
	if p := read(); p.Conversation != "conv-own" {
		t.Fatalf("newest of its own: %+v", p)
	}
	// hooks that name the other conversation's id and paths elsewhere (a
	// hook naming the other worktree's file is not main's at all:
	// claimOutside; one naming only its id is taken as main's)
	event(t, d, "UserPromptSubmit", "main", map[string]any{"prompt": "x", "session_id": "conv-other", "transcript_path": filepath.Join(other, "conv-other.jsonl")})
	event(t, d, "UserPromptSubmit", "main", map[string]any{"prompt": "x", "session_id": "conv-other"})
	if s := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"}); s.ClaudeSessionID != "conv-other" {
		t.Fatalf("the hook's id: %q", s.ClaudeSessionID)
	}
	p := read()
	b, _ := json.Marshal(p)
	if strings.Contains(string(b), "OTHER_SECRET") || p.Conversation != "conv-own" {
		t.Fatalf("read another worktree's conversation: %s", b)
	}
	event(t, d, "UserPromptSubmit", "main", map[string]any{"prompt": "x", "session_id": "conv-out", "transcript_path": outside})
	if s := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"}); s.ClaudeSessionID != "conv-out" {
		t.Fatalf("the hook's id: %q", s.ClaudeSessionID)
	}
	b, _ = json.Marshal(read())
	if strings.Contains(string(b), "OUTSIDE_SECRET") {
		t.Fatalf("read a path a hook named: %s", b)
	}
	for _, secret := range []string{"THINK_SECRET", "TOOL_RESULT", "ALONGSIDE_RESULT", "META_SECRET", "REMINDER_SECRET",
		"SIDECHAIN_SECRET", "HOOK_SECRET", "COMPACT_SECRET", "COMMAND_SECRET", "LOCAL_SECRET", "TASK_SECRET",
		"SYNTHETIC_SECRET", "DESC_NOT_MAIN", "DESC_SECRET"} {
		if strings.Contains(string(b), secret) {
			t.Errorf("%s in a session's chat", secret)
		}
	}
	// the reported conversation, once it is in the worktree's directory
	writeChat(t, filepath.Join(own, "conv-two.jsonl"), []string{`{"type":"user","timestamp":"2026-11-12T13:00:00Z","message":{"role":"user","content":"二つ目"}}`})
	os.Chtimes(filepath.Join(own, "conv-own.jsonl"), time.Now().Add(time.Hour), time.Now().Add(time.Hour)) // the newest is not it
	event(t, d, "UserPromptSubmit", "main", map[string]any{"prompt": "y", "session_id": "conv-two"})
	if p := read(); p.Conversation != "conv-two" || len(p.Items) != 1 || p.Items[0].Text != "二つ目" {
		t.Fatalf("the reported one: %+v", p)
	}
	// a symlink in its place is not read
	os.Remove(filepath.Join(own, "conv-two.jsonl"))
	os.Symlink(outside, filepath.Join(own, "conv-two.jsonl"))
	if res, err := d.Call("session.transcript", mustJSON(map[string]any{"slug": "main"})); err == nil {
		b, _ := json.Marshal(res)
		if strings.Contains(string(b), "OUTSIDE_SECRET") {
			t.Fatalf("read through a symlink: %s", b)
		}
	}
	// tapi is no session; an unknown slug
	for _, slug := range []string{TapiSlug, "nope"} {
		if _, err := d.Call("session.transcript", mustJSON(map[string]any{"slug": slug})); err == nil || !strings.Contains(err.Error(), "no such session") {
			t.Fatalf("%s: %v", slug, err)
		}
	}
}

// An agent that is not Claude Code has no chat and no command list.
func TestSessionChatNotClaude(t *testing.T) {
	d, _, repo := setup(t)
	ss := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new", "agent": "codex"})
	writeChat(t, filepath.Join(claudeProjectDir(ss.Path), "conv-x.jsonl"), chatFixture)
	if _, err := d.Call("session.transcript", mustJSON(map[string]any{"slug": "main"})); !errors.Is(err, errNoTranscript) {
		t.Fatalf("codex's chat: %v", err)
	}
	c := call[CommandList](t, d, "session.commands", map[string]any{"slug": "main"})
	if c.Supported || len(c.Commands) != 0 || c.Agent != "codex" {
		t.Fatalf("codex's commands: %+v", c)
	}
	// files are the worktree's, whatever the agent
	if f := call[FileList](t, d, "session.files", map[string]any{"slug": "main"}); !slices.Contains(f.Files, "a.txt") {
		t.Fatalf("codex's files: %+v", f)
	}
}

// The command list: Claude Code's own, the worktree's commands (one level
// of subdirectories) and skills, the user's; frontmatter descriptions,
// else the first line; a symlink in the worktree's .claude is not
// followed; names Claude Code would not take are left out.
func TestSessionCommands(t *testing.T) {
	d, repo, _ := chatSetup(t)
	write := func(p, s string) {
		t.Helper()
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cl := filepath.Join(repo, ".claude")
	write(filepath.Join(cl, "commands", "ship.md"), "---\ndescription: \"Ship it\x07 now\"\nallowed-tools: Bash\n---\nbody\n")
	write(filepath.Join(cl, "commands", "front", "comp.md"), "# Make a component\n\nmore\n")
	write(filepath.Join(cl, "commands", "bad name.md"), "x")
	write(filepath.Join(cl, "commands", "long.md"), "---\ndescription: "+strings.Repeat("あ", 300)+"\n---\n")
	secret := filepath.Join(t.TempDir(), "secret.md")
	write(secret, "---\ndescription: LINKED_SECRET\n---\n")
	os.Symlink(secret, filepath.Join(cl, "commands", "linked.md"))
	write(filepath.Join(cl, "skills", "deploy", "SKILL.md"), "---\nname: deploy\ndescription: Deploy the app\n---\n")
	user := claudeConfigDir()
	write(filepath.Join(user, "commands", "mine.md"), "My own command\n")
	write(filepath.Join(user, "skills", "notes", "SKILL.md"), "---\ndescription: Take notes\n---\n")
	c := call[CommandList](t, d, "session.commands", map[string]any{"slug": "main"})
	if !c.Supported || c.Agent != "claude" {
		t.Fatalf("supported: %+v", c)
	}
	by := map[string]SlashCommand{}
	for _, x := range c.Commands {
		by[x.Name] = x
	}
	want := map[string]SlashCommand{
		"clear":  {Name: "clear", Source: "builtin"},
		"ship":   {Name: "ship", Source: "project", Description: "Ship it  now"},
		"comp":   {Name: "comp", Source: "project:front", Description: "Make a component"},
		"deploy": {Name: "deploy", Source: "project skill", Description: "Deploy the app"},
		"mine":   {Name: "mine", Source: "user", Description: "My own command"},
		"notes":  {Name: "notes", Source: "user skill", Description: "Take notes"},
	}
	for name, w := range want {
		g, ok := by[name]
		if !ok || g.Source != w.Source || (w.Description != "" && g.Description != w.Description) {
			t.Errorf("%s: %+v, want %+v", name, g, w)
		}
	}
	for _, name := range []string{"bad name", "linked"} {
		if _, ok := by[name]; ok {
			t.Errorf("%s listed", name)
		}
	}
	b, _ := json.Marshal(c)
	if strings.Contains(string(b), "LINKED_SECRET") {
		t.Error("read through a symlink in the worktree")
	}
	if l := by["long"].Description; len(l) > cmdMaxDesc+len("…") {
		t.Errorf("long description: %d bytes", len(l))
	}
	// the user's own command may be a symlink (dotfiles): its description is read
	mineReal := filepath.Join(t.TempDir(), "linked-mine.md")
	write(mineReal, "Linked from dotfiles\n")
	os.Symlink(mineReal, filepath.Join(user, "commands", "dot.md"))
	// the worktree's .claude itself a symlink: nothing of it is read
	os.Rename(cl, filepath.Join(repo, "claude-real"))
	outside := filepath.Join(t.TempDir(), "out")
	write(filepath.Join(outside, "commands", "evil.md"), "---\ndescription: OUTSIDE_SECRET\n---\n")
	os.Symlink(outside, cl)
	c = call[CommandList](t, d, "session.commands", map[string]any{"slug": "main"})
	b, _ = json.Marshal(c)
	if strings.Contains(string(b), "OUTSIDE_SECRET") || strings.Contains(string(b), `"evil"`) {
		t.Errorf("read through a symlinked .claude: %s", b)
	}
	got := ""
	for _, x := range c.Commands {
		if x.Name == "dot" {
			got = x.Description
		}
	}
	if got != "Linked from dotfiles" {
		t.Errorf("the user's symlinked command: %q", got)
	}
}

// The file list: tracked and untracked files of the worktree, .gitignore
// kept; a symlink to a directory outside is one entry, never walked;
// names with control characters are left out (counted); a query ranks
// the file names that start with it first; at most filesMaxShown.
func TestSessionFiles(t *testing.T) {
	d, repo, _ := chatSetup(t)
	write := func(p string) {
		t.Helper()
		os.MkdirAll(filepath.Dir(filepath.Join(repo, p)), 0o755)
		if err := os.WriteFile(filepath.Join(repo, p), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore")
	os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("node_modules/\n*.log\n"), 0o644)
	write("src/gui.js")
	write("docs/gui.md")
	write("node_modules/lib/x.js")
	write("debug.log")
	write("bad\x1bname.txt")
	for i := 0; i < filesMaxShown+10; i++ {
		write("many/f" + strconv.Itoa(i) + ".txt")
	}
	out := t.TempDir()
	os.WriteFile(filepath.Join(out, "OUTSIDE.txt"), []byte("x"), 0o644)
	os.Symlink(out, filepath.Join(repo, "linkout"))
	list := func(q string) FileList {
		t.Helper()
		d.fileLists.Delete("main")
		return call[FileList](t, d, "session.files", map[string]any{"slug": "main", "q": q})
	}
	all := list("")
	if len(all.Files) != filesMaxShown || all.Matches != all.Total || all.Skipped != 1 {
		t.Fatalf("all: %d shown, %d matches, %d total, %d skipped", len(all.Files), all.Matches, all.Total, all.Skipped)
	}
	g := list("gui")
	if len(g.Files) != 2 || g.Files[0] != "docs/gui.md" && g.Files[0] != "src/gui.js" {
		t.Fatalf("gui: %+v", g)
	}
	for _, q := range []string{"node_modules", "debug.log", "OUTSIDE", "bad"} {
		if f := list(q); len(f.Files) != 0 {
			t.Errorf("%s: %v", q, f.Files)
		}
	}
	if f := list("linkout"); len(f.Files) != 1 || f.Files[0] != "linkout" {
		t.Errorf("the symlink: %v", f.Files)
	}
	if f := list("a.txt"); len(f.Files) == 0 || f.Files[0] != "a.txt" {
		t.Errorf("a.txt first: %v", f.Files)
	}
	// a session whose .git was changed: pekoed runs no git there
	d.update("main", func(s *state.Session) { s.GitBlocked = "changed" })
	d.fileLists.Delete("main")
	if _, err := d.Call("session.files", mustJSON(map[string]any{"slug": "main"})); err == nil {
		t.Fatal("listed a blocked worktree")
	}
}

// More untracked (not ignored) files than pekoed keeps do not push the
// tracked ones out: the tracked are read first, the untracked fill what
// is left, and the list says it was cut.
func TestSessionFilesTrackedFirst(t *testing.T) {
	d, repo, _ := chatSetup(t)
	defer func(n int) { filesMaxRead = n }(filesMaxRead)
	filesMaxRead = 20
	dir := filepath.Join(repo, "build")
	os.MkdirAll(dir, 0o755)
	for i := 0; i <= filesMaxRead; i++ {
		if err := os.WriteFile(filepath.Join(dir, "o"+strconv.Itoa(i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f := call[FileList](t, d, "session.files", map[string]any{"slug": "main", "q": "a.txt"})
	if !slices.Contains(f.Files, "a.txt") || !f.Truncated || f.Total != filesMaxRead {
		t.Errorf("the tracked a.txt among %d untracked: %+v", filesMaxRead+1, f)
	}
	d.fileLists.Delete("main")
	if f := call[FileList](t, d, "session.files", map[string]any{"slug": "main", "q": "build/o"}); f.Matches != filesMaxRead-1 {
		t.Errorf("the untracked fill the rest: %+v", f)
	}
}

// A path git prints from the index with "..", "." or an empty part (an
// agent can write its worktree's index) is never offered: it could point
// outside the worktree. A nested repository's "sub/" stays.
func TestSessionFilesDotDotInIndex(t *testing.T) {
	d, repo, _ := chatSetup(t)
	names := []string{"a/../../outside.txt", "a/./dot.txt", "a//twice.txt", "/etc/passwd", "ok.txt"}
	sort.Strings(names)
	blob, _ := hex.DecodeString("e69de29bb2d1d6434b8b29ae775ad8c2e48c5391")
	var b bytes.Buffer
	b.WriteString("DIRC")
	binary.Write(&b, binary.BigEndian, []uint32{2, uint32(len(names))})
	for _, n := range names {
		var e bytes.Buffer
		binary.Write(&e, binary.BigEndian, []uint32{0, 0, 0, 0, 0, 0, 0o100644, 0, 0, 0})
		e.Write(blob)
		binary.Write(&e, binary.BigEndian, uint16(len(n)))
		e.WriteString(n)
		e.Write(make([]byte, 8-e.Len()%8))
		b.Write(e.Bytes())
	}
	sum := sha1.Sum(b.Bytes())
	b.Write(sum[:])
	if err := os.WriteFile(filepath.Join(repo, ".git", "index"), b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "sub")
	os.MkdirAll(sub, 0o755)
	os.WriteFile(filepath.Join(sub, "x"), nil, 0o644)
	run(t, sub, "git", "init", "-q")
	run(t, sub, "git", "add", "x")
	run(t, sub, "git", "commit", "-qm", "x")
	f := call[FileList](t, d, "session.files", map[string]any{"slug": "main"})
	for _, n := range f.Files {
		if n != "ok.txt" && n != "a.txt" && n != "sub/" {
			t.Errorf("offered %q: %v", n, f.Files)
		}
	}
	if !slices.Contains(f.Files, "ok.txt") || !slices.Contains(f.Files, "sub/") || f.Skipped != 4 {
		t.Errorf("files %v, skipped %d", f.Files, f.Skipped)
	}
}

// Removing a session drops its last file list (up to filesMaxRead paths).
func TestSessionFilesForgotten(t *testing.T) {
	d, _, _ := chatSetup(t)
	call[FileList](t, d, "session.files", map[string]any{"slug": "main"})
	if _, ok := d.fileLists.Load("main"); !ok {
		t.Fatal("no list kept")
	}
	d.forget("main")
	if _, ok := d.fileLists.Load("main"); ok {
		t.Error("the list of a forgotten session is kept")
	}
}

// The three are a person's: without the human token (or with a wrong one)
// they are refused over the socket.
func TestSessionChatNeedsToken(t *testing.T) {
	d, _, own := chatSetup(t)
	writeChat(t, filepath.Join(own, "conv-own.jsonl"), chatFixture)
	c := dialPipe(t, d)
	for _, tc := range []struct {
		method string
		params map[string]any
	}{
		{"session.transcript", map[string]any{"slug": "main"}},
		{"session.commands", map[string]any{"slug": "main"}},
		{"session.files", map[string]any{"slug": "main"}},
	} {
		for _, tok := range []string{"", "wrong-" + d.humanToken} {
			if e := c.call(tc.method, tc.params, tok); !strings.Contains(e, "only a person can do this") {
				t.Errorf("%s with token %q: %q, want the human-only refusal", tc.method, tok, e)
			}
		}
		if e := c.call(tc.method, tc.params, d.humanToken); e != "" {
			t.Errorf("%s with the token: %s", tc.method, e)
		}
	}
}

// On the GUI: the three routes with a session token (401 without); the
// phone's Web UI has none of them. tapi's chat reads a page back too.
func TestGUISessionChat(t *testing.T) {
	d, _, repo, h, _ := guiSetup(t)
	ss := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	writeChat(t, filepath.Join(claudeProjectDir(ss.Path), "conv-own.jsonl"), chatLinesN(chatMaxItems+5))
	ph := testWebHandler(d)
	paths := []string{"/api/sessions/main/transcript", "/api/sessions/main/commands", "/api/sessions/main/files?q=a"}
	for _, p := range paths {
		if w := doGUI(t, h, guiReq{path: p}); w.Code != http.StatusUnauthorized {
			t.Errorf("%s without a bearer: %d", p, w.Code)
		}
		if w := doWeb(t, ph, webReq{path: p, from: ipPhone}); w.Code == 200 && strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			t.Errorf("the phone has %s: %s", p, w.Body)
		}
	}
	s := guiSignIn(t, d, h)
	get := func(p string, code int) []byte {
		t.Helper()
		w := doGUI(t, h, guiReq{path: p, bearer: s})
		if w.Code != code {
			t.Fatalf("%s: %d %s", p, w.Code, w.Body)
		}
		return w.Body.Bytes()
	}
	var page ChatPage
	json.Unmarshal(get(paths[0], 200), &page)
	if len(page.Items) != chatMaxItems || page.Before == "" {
		t.Fatalf("first read: %d items, before %q", len(page.Items), page.Before)
	}
	var back ChatPage
	json.Unmarshal(get(paths[0]+"?before="+url.QueryEscape(page.Before), 200), &back)
	if len(back.Items) != 5 || back.Before != "" || back.Items[0].Text != "n0" {
		t.Fatalf("page back: %+v", back)
	}
	get(paths[0]+"?before=other:5", http.StatusConflict)
	get(paths[0]+"?before=x", http.StatusBadRequest)
	get(paths[0]+"?after="+strings.Repeat("a", 201), http.StatusBadRequest)
	get("/api/sessions/nope/transcript", http.StatusNotFound)
	var cl CommandList
	json.Unmarshal(get(paths[1], 200), &cl)
	if !cl.Supported || len(cl.Commands) == 0 {
		t.Fatalf("commands: %+v", cl)
	}
	var fl FileList
	json.Unmarshal(get(paths[2], 200), &fl)
	if !slices.Contains(fl.Files, "a.txt") {
		t.Fatalf("files: %+v", fl)
	}
	get("/api/sessions/main/files?q="+strings.Repeat("a", 257), http.StatusBadRequest)
}

// The send box's pick list (gui/picker.js) with a fake DOM and fetch:
// "@ Files" before a word puts a blank after the "@" and a pick keeps the
// word; while the list of a new word is read (a key more), ↵ and Tab put
// none of the older word's list in, and take the new one's once it came.
func TestGUIPickerFiles(t *testing.T) {
	out := runWebModule(t, `import { picker } from "gui/picker.js";
class N {
  constructor(tag) { this.tag = tag; this.children = []; this.attrs = {}; this.hidden = false; this.ls = {}; this.textContent = ""; }
  append(...c) { this.children.push(...c); }
  replaceChildren(...c) { this.children = c; }
  setAttribute(k, v) { this.attrs[k] = v; }
  removeAttribute(k) { delete this.attrs[k]; }
  addEventListener(t, f) { (this.ls[t] ||= []).push(f); }
  dispatchEvent(e) { for (const f of this.ls[e.type] || []) f(e); }
  querySelector() { return null; }
  focus() { globalThis.document.activeElement = this; }
  setSelectionRange(a) { this.selectionStart = a; }
}
globalThis.document = { createElement: (t) => new N(t), getElementById: () => new N("x"), activeElement: null };
const ALL = ["src/gui.js", "src/guide.md", "docs/gui-design.md", "src/a.js"];
globalThis.fetch = async (url) => {
  const q = decodeURIComponent(url.split("q=")[1] || "");
  await new Promise((r) => setTimeout(r, 50));
  const files = ALL.filter((f) => f.includes(q));
  return { ok: true, status: 200, json: async () => ({ files, matches: files.length, total: ALL.length }) };
};
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const ta = new N("textarea"), box = new N("div");
box.id = "pk";
box.hidden = true;
const pk = picker({ ta, box, slug: () => "s1" });
const type = (v) => { ta.value = v; ta.selectionStart = v.length; ta.dispatchEvent({ type: "input" }); };
const tab = () => pk.key({ key: "Tab", shiftKey: false, preventDefault() {}, stopPropagation() {} });
const r = {};
type("see @gui");
await sleep(300);
type("see @guide");
r.staleTook = tab();
r.staleText = ta.value;
r.loading = box.children.some((c) => c.textContent === "Loading…");
await sleep(300);
r.freshTook = tab();
r.freshText = ta.value;
pk.close();
ta.value = "look at this bug";
ta.selectionStart = 8;
pk.start("file");
r.button = ta.value;
await sleep(300);
const v = ta.value, c = ta.selectionStart;
ta.value = v.slice(0, c) + "a.js" + v.slice(c);
ta.selectionStart = c + 4;
ta.dispatchEvent({ type: "input" });
await sleep(300);
tab();
r.buttonPick = ta.value;
console.log(JSON.stringify(r));`)
	var r map[string]any
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatalf("node: %v %s", err, out)
	}
	want := map[string]any{
		"staleTook":  true,
		"staleText":  "see @guide",
		"loading":    true,
		"freshTook":  true,
		"freshText":  "see @src/guide.md ",
		"button":     "look at @ this bug",
		"buttonPick": "look at @src/a.js this bug",
	}
	for k, w := range want {
		if r[k] != w {
			t.Errorf("%s: %v, want %v", k, r[k], w)
		}
	}
}

// The send box's / and @ (gui/compose.js): what is typed at the caret,
// what a pick writes, and what never goes into the box (control
// characters). Long names stay whole in the text (the list cuts them on
// screen only).
func TestGUIComposePick(t *testing.T) {
	out := runWebModule(t, `import { tokenAt, insertPick, filterCommands, pickFiles, mention, commandText, insertable, prependPage, PICK_MAX } from "gui/compose.js";
const r = {};
r.cmd = tokenAt("/com", 4);
r.cmdLead = tokenAt("  /re", 5);
r.cmdMid = tokenAt("fix /re", 7);
r.file = tokenAt("look at @src/gu", 15);
r.fileStart = tokenAt("@do", 3);
r.fileMidWord = tokenAt("mail@x", 6);
r.fileCaretIn = tokenAt("see @src/gui.js now", 8);
r.fileBeforeWord = tokenAt("compare @this with that", 9);
r.none = tokenAt("plain words", 5);
r.insCmd = insertPick("/com", r.cmd, commandText("compact"));
r.insCmdRest = insertPick("/ hello", tokenAt("/ hello", 1), commandText("clear"));
r.insFile = insertPick("look at @src/gu", r.file, mention("src/gui.js"));
r.insFileIn = insertPick("see @src/gui.js now", r.fileCaretIn, mention("src/x.js"));
r.insBeforeWord = insertPick("compare @this with that", r.fileBeforeWord, mention("src/a.js")).text;
r.insSpace = mention("docs/a b.md");
r.insCtl = insertPick("@x", tokenAt("@x", 2), mention("bad\u001bname"));
r.insLS = insertable("a b");
const long = "d/" + "x".repeat(900) + ".md";
r.insLong = insertPick("@x", tokenAt("@x", 2), mention(long)).text.length;
const cmds = [{name: "compact", description: "Summarize"}, {name: "clear"}, {name: "deploy", description: "ship it"}, {name: "recompile"}, {name: "bad\u0007"}, {name: ""}];
r.fCo = filterCommands(cmds, "co").map((c) => c.name);
r.fShip = filterCommands(cmds, "ship").map((c) => c.name);
r.fAll = filterCommands(cmds, "").length;
r.files = pickFiles(["a.txt", "b\u0000c", "c\u0085d", long]).length;
r.cap = pickFiles(Array.from({length: 80}, (_, i) => "f" + i)).length === PICK_MAX;
const pg = prependPage([{kind: "agent"}], [{kind: "human"}, {kind: "agent"}], new Set(["m0", "t3", "x"]));
r.page = [pg.items.length, [...pg.open].sort()];
console.log(JSON.stringify(r));`)
	var r map[string]any
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatalf("node: %v %s", err, out)
	}
	j := func(k string) string { b, _ := json.Marshal(r[k]); return string(b) }
	want := map[string]string{
		"cmd":            `{"end":4,"kind":"cmd","q":"com","start":0}`,
		"cmdLead":        `{"end":5,"kind":"cmd","q":"re","start":2}`,
		"cmdMid":         `null`,
		"file":           `{"end":15,"kind":"file","q":"src/gu","start":8}`,
		"fileStart":      `{"end":3,"kind":"file","q":"do","start":0}`,
		"fileMidWord":    `null`,
		"fileCaretIn":    `{"end":15,"kind":"file","q":"src","start":4}`,
		"fileBeforeWord": `{"end":9,"kind":"file","q":"","start":8}`,
		"none":           `null`,
		"insCmd":         `{"caret":9,"text":"/compact "}`,
		"insCmdRest":     `{"caret":6,"text":"/clear hello"}`,
		"insFile":        `{"caret":20,"text":"look at @src/gui.js "}`,
		"insFileIn":      `{"caret":13,"text":"see @src/x.js now"}`,
		"insBeforeWord":  `"compare @src/a.js this with that"`,
		"insSpace":       `"@\"docs/a b.md\""`,
		"insCtl":         `null`,
		"insLS":          `false`,
		"insLong":        `907`,
		"fCo":            `["compact","recompile"]`,
		"fShip":          `["deploy"]`,
		"fAll":           `4`,
		"files":          `2`,
		"cap":            `true`,
		"page":           `[3,["m2","t5"]]`,
	}
	for k, w := range want {
		if g := j(k); g != w {
			t.Errorf("%s: %s, want %s", k, g, w)
		}
	}
}
