package daemon

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"pekoe/internal/state"
)

// changesSetup: a session on a repository whose a.txt was edited, with a
// new file, a deleted one, a binary and a name git would quote.
func changesSetup(t *testing.T) (*Daemon, string) {
	t.Helper()
	d, _, repo := setup(t)
	os.WriteFile(filepath.Join(repo, "gone.txt"), []byte("bye\n"), 0o644)
	run(t, repo, "git", "add", ".")
	run(t, repo, "git", "commit", "-qm", "gone")
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("1\ntwo\n3\n4\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "新しい.md"), []byte("x\ny\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "img.bin"), []byte{0, 1, 2, 0, 3}, 0o644)
	os.Remove(filepath.Join(repo, "gone.txt"))
	return d, repo
}

// changes: each changed path once, its status and lines added and
// deleted (binary marked), names as they are (not git's quoting).
func TestChangesCounts(t *testing.T) {
	d, _ := changesSetup(t)
	list := call[[]changeEntry](t, d, "changes", map[string]any{"slug": "main"})
	got := map[string]changeEntry{}
	for _, c := range list {
		got[c.Path] = c
	}
	want := map[string]changeEntry{
		"a.txt":    {Status: "M", Path: "a.txt", Added: 2, Deleted: 1},
		"新しい.md":   {Status: "A", Path: "新しい.md", Added: 2},
		"img.bin":  {Status: "A", Path: "img.bin", Binary: true},
		"gone.txt": {Status: "D", Path: "gone.txt", Deleted: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("changes: %+v", list)
	}
	for p, w := range want {
		if got[p] != w {
			t.Errorf("%s: %+v, want %+v", p, got[p], w)
		}
	}
}

// changes.diff: one changed file's hunks; a binary has none; a path the
// list does not have (unchanged, outside, a pathspec) is refused without
// reading anything; long diffs and lines are cut and say so.
func TestChangesDiff(t *testing.T) {
	d, repo := changesSetup(t)
	diff := func(p string) (FileDiffView, error) {
		t.Helper()
		res, err := d.Call("changes.diff", mustJSON(map[string]any{"slug": "main", "path": p}))
		if err != nil {
			return FileDiffView{}, err
		}
		return res.(FileDiffView), nil
	}
	a, err := diff("a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != "M" || a.Added != 2 || a.Deleted != 1 || len(a.Hunks) != 1 || a.Truncated || a.Lines != a.Total {
		t.Fatalf("a.txt: %+v", a)
	}
	var kinds, texts []string
	for _, l := range a.Hunks[0].Lines {
		kinds, texts = append(kinds, l.Kind), append(texts, l.Text)
	}
	if strings.Join(kinds, "") != " -++ " && strings.Join(kinds, "") != " -+ +" {
		t.Errorf("a.txt kinds %q texts %q", kinds, texts)
	}
	if !strings.HasPrefix(a.Hunks[0].Header, "@@ ") {
		t.Errorf("header %q", a.Hunks[0].Header)
	}
	if j, err := diff("新しい.md"); err != nil || j.Status != "A" || len(j.Hunks) != 1 || len(j.Hunks[0].Lines) != 2 {
		t.Errorf("新しい.md: %+v %v", j, err)
	}
	if b, err := diff("img.bin"); err != nil || !b.Binary || len(b.Hunks) != 0 {
		t.Errorf("img.bin: %+v %v", b, err)
	}
	if g, err := diff("gone.txt"); err != nil || g.Status != "D" || g.Hunks[0].Lines[0].Kind != "-" {
		t.Errorf("gone.txt: %+v %v", g, err)
	}
	for _, p := range []string{"", "nope.txt", "*", ":(glob)*.txt", "../app/a.txt", "/etc/hosts", strings.Repeat("a", diffPathMax+1)} {
		if _, err := diff(p); !errors.Is(err, errNotChanged) {
			t.Errorf("%q: %v, want not changed", p, err)
		}
	}
	// a long file and a long line
	var big strings.Builder
	big.WriteString(strings.Repeat("é", diffLineMax) + "\n")
	for i := 0; i < diffMaxLines+50; i++ {
		big.WriteString("line\n")
	}
	os.WriteFile(filepath.Join(repo, "big.txt"), []byte(big.String()), 0o644)
	bg, err := diff("big.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !bg.Truncated || bg.Lines != diffMaxLines || bg.Total != diffMaxLines+51 {
		t.Errorf("big.txt: lines %d of %d, truncated %v", bg.Lines, bg.Total, bg.Truncated)
	}
	first := bg.Hunks[0].Lines[0]
	if !first.Cut || len(first.Text) > diffLineMax || !strings.HasPrefix(first.Text, "é") || strings.ToValidUTF8(first.Text, "?") != first.Text {
		t.Errorf("the long line: cut %v, %d bytes", first.Cut, len(first.Text))
	}
	// one line longer than the whole budget (a minified file): it is cut
	// to diffLineMax and shown, and the budget counts what is sent
	os.WriteFile(filepath.Join(repo, "min.js"), []byte(strings.Repeat("x", diffMaxBytes+10)+"\nnext\n"), 0o644)
	mn, err := diff("min.js")
	if err != nil {
		t.Fatal(err)
	}
	if mn.Truncated || mn.Lines != 2 || len(mn.Hunks) != 1 || !mn.Hunks[0].Lines[0].Cut || len(mn.Hunks[0].Lines[0].Text) != diffLineMax {
		t.Errorf("min.js: %d of %d lines, truncated %v", mn.Lines, mn.Total, mn.Truncated)
	}
	// a session whose .git was changed: pekoed runs no git there
	d.update("main", func(s *state.Session) { s.GitBlocked = "changed" })
	if _, err := diff("a.txt"); err == nil {
		t.Error("read a blocked worktree")
	}
}

// changes.diff is a person's: refused over the socket without the token.
func TestChangesDiffNeedsToken(t *testing.T) {
	d, _ := changesSetup(t)
	c := dialPipe(t, d)
	params := map[string]any{"slug": "main", "path": "a.txt"}
	for _, m := range []string{"changes", "changes.diff"} {
		for _, tok := range []string{"", "wrong-" + d.humanToken} {
			if e := c.call(m, params, tok); !strings.Contains(e, "only a person can do this") {
				t.Errorf("%s with token %q: %q, want the human-only refusal", m, tok, e)
			}
		}
		if e := c.call(m, params, d.humanToken); e != "" {
			t.Errorf("%s with the token: %s", m, e)
		}
	}
}

// On the GUI: the two routes with a session token (401 without); the
// phone's Web UI has neither. The list carries the forecast's peers.
func TestGUIChanges(t *testing.T) {
	d, _, repo, h, _ := guiSetup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("1\ntwo\n3\n"), 0o644)
	d.update("main", func(s *state.Session) {
		s.Overlaps = []state.Overlap{{Slug: "feat-x", Files: []string{"a.txt"}, Conflict: true, ConflictFiles: []string{"a.txt"}}, {Slug: "feat-y", Files: []string{"a.txt"}}}
	})
	ph := testWebHandler(d)
	paths := []string{"/api/sessions/main/changes", "/api/sessions/main/diff?path=a.txt"}
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
	var cv guiChangesView
	json.Unmarshal(get(paths[0], 200), &cv)
	if len(cv.Files) != 1 || cv.Files[0].Path != "a.txt" || cv.Files[0].Added != 1 || cv.Files[0].Deleted != 1 || len(cv.Overlaps) != 2 {
		t.Fatalf("changes: %+v", cv)
	}
	if p := cv.Files[0].Peers; len(p) != 2 || p[0] != (changePeer{Slug: "feat-x", Conflict: true}) || p[1] != (changePeer{Slug: "feat-y"}) {
		t.Errorf("peers: %+v", p)
	}
	var fd FileDiffView
	json.Unmarshal(get(paths[1], 200), &fd)
	if fd.Path != "a.txt" || len(fd.Hunks) != 1 {
		t.Fatalf("diff: %+v", fd)
	}
	get("/api/sessions/main/diff?path="+url.QueryEscape("b.txt"), http.StatusNotFound)
	get("/api/sessions/main/diff", http.StatusBadRequest)
	get("/api/sessions/main/diff?path="+strings.Repeat("a", diffPathMax+1), http.StatusBadRequest)
	get("/api/sessions/nope/changes", http.StatusNotFound)
	get("/api/sessions/nope/diff?path=a.txt", http.StatusNotFound)
}

// The rail's Changes (gui/views/changes.js), run in node on the fake DOM
// with a fake fetch: it follows the session open in the main column,
// lists its files (status, +/−, binary, ⚠ peers, the forecast at the
// foot), filters them by path, opens one file's diff in its place and
// goes back; markup in a path or a line stays text. With no session open
// it lists the sessions that changed something. It reads again only when
// the session's diffstat moves, and nothing after unmount.
func TestGUIChangesView(t *testing.T) {
	out := runWebModule(t, fakeDOM+`
const calls = [];
let list = { slug: "feat-a", files: [
  { status: "M", path: "src/<b>x.go", added: 3, deleted: 1, peers: [{ slug: "feat-b", conflict: true }] },
  { status: "A", path: "README.md", added: 2, deleted: 0 },
  { status: "A", path: "img.png", added: 0, deleted: 0, binary: true },
], overlaps: [{ slug: "feat-b", files: ["src/<b>x.go"], conflict: true }] };
const diff = { slug: "feat-a", path: "src/<b>x.go", status: "M", added: 3, deleted: 1, lines: 3, total: 9, truncated: true,
  hunks: [{ header: "@@ -1,2 +1,3 @@ func", lines: [{ kind: " ", text: "a" }, { kind: "-", text: "<script>x</script>" }, { kind: "+", text: "b", cut: true }] }] };
globalThis.fetch = async (path) => {
  calls.push(path);
  const body = path.includes("/diff?") ? diff : list;
  return { ok: true, status: 200, json: async () => body, text: async () => "" };
};
import v from "gui/views/changes.js";
const tick = () => new Promise((r) => setTimeout(r, 0));
const went = [];
const box = el0("div");
const S = (slug, files, extra) => ({ slug, branch: slug, repo: "pekoe", state: "running", diffstat: { files, added: files * 2, deleted: files }, ...extra });
const sessions = [S("feat-a", 3), S("feat-b", 1), S("feat-c", 0), S("old", 2, { state: "landed" })];
const r = {};
v.mount(box, { go: (s) => went.push(s) });
v.update({ sessions, tapi: null, overview: null, current: "feat-a" });
await tick(); await tick();
r.list = box.textContent;
r.rows = find(box, (n) => /view-changes-row\b/.test(n.className)).length;
r.marks = find(box, (n) => /view-changes-st/.test(n.className)).map((n) => n.className + "=" + n.attrs["aria-label"]);
v.query("read");
r.filtered = find(box, (n) => /view-changes-row\b/.test(n.className)).map((n) => n.title);
v.query("zzz");
r.none = box.textContent.includes("Nothing matches");
v.query("");
v.update({ sessions, tapi: null, overview: null, current: "feat-a" });
await tick();
r.callsSame = calls.length;
find(box, (n) => /view-changes-row\b/.test(n.className))[0].click();
await tick(); await tick();
const file = find(box, (n) => /view-changes-file/.test(n.className))[0];
const lst = find(box, (n) => /view-changes-list/.test(n.className))[0];
r.diffShown = !file.hidden && lst.hidden;
r.diff = file.textContent;
r.lineMarks = find(file, (n) => /view-changes-mark/.test(n.className)).map((n) => n.className + "=" + n.textContent);
r.texts = find(file, (n) => /view-changes-text/.test(n.className)).map((n) => n.children.length + ":" + n.children[0].tag);
r.diffCall = calls[calls.length - 1];
// the diffstat moves: the list and the open diff are read again
sessions[0] = S("feat-a", 4);
v.update({ sessions, tapi: null, overview: null, current: "feat-a" });
await tick(); await tick(); await tick();
r.reread = calls.slice(r.callsSame + 1);
find(file, (n) => n.tag === "button" && n.textContent === "← Files")[0].click();
r.back = !lst.hidden && file.hidden;
// no session open: the sessions with changes, to open one
v.update({ sessions, tapi: null, overview: null, current: "+tapi" });
r.noneOpen = box.textContent;
find(box, (n) => /view-changes-row\b/.test(n.className))[0].click();
r.went = went;
const before = calls.length;
v.unmount();
v.update({ sessions, tapi: null, overview: null, current: "feat-b" });
await tick();
r.after = calls.length - before;
console.log(JSON.stringify(r));`)
	var got struct {
		List, Diff, NoneOpen, DiffCall string
		Rows, CallsSame, After         int
		Marks, Filtered, LineMarks     []string
		Texts, Reread, Went            []string
		None, DiffShown, Back          bool
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("node: %v %s", err, out)
	}
	for _, want := range []string{"CHANGES3", "feat-a", "<b>x.go", "src/", "+3 −1", "⚠ feat-b", "README.md", "+2", "binary", "feat-b conflict"} {
		if !strings.Contains(got.List, want) {
			t.Errorf("the list lacks %q: %s", want, got.List)
		}
	}
	if got.Rows != 3 || !slices.Equal(got.Marks, []string{"view-changes-st r-warn=modified", "view-changes-st r-ok=added", "view-changes-st r-ok=added"}) {
		t.Errorf("rows %d marks %v", got.Rows, got.Marks)
	}
	if !slices.Equal(got.Filtered, []string{"README.md"}) || !got.None {
		t.Errorf("filter: %v none %v", got.Filtered, got.None)
	}
	if got.CallsSame != 1 {
		t.Errorf("read again with nothing moved: %d calls", got.CallsSame)
	}
	if !got.DiffShown || got.DiffCall != "/api/sessions/feat-a/diff?path="+url.QueryEscape("src/<b>x.go") {
		t.Errorf("diff: shown %v, call %q", got.DiffShown, got.DiffCall)
	}
	for _, want := range []string{"← Files", "read only", "@@ -1,2 +1,3 @@ func", "<script>x</script>", "3 of 9 lines shown"} {
		if !strings.Contains(got.Diff, want) {
			t.Errorf("the diff lacks %q: %s", want, got.Diff)
		}
	}
	if !slices.Equal(got.LineMarks, []string{"view-changes-mark= ", "view-changes-mark r-err=−", "view-changes-mark r-ok=+"}) {
		t.Errorf("marks: %v", got.LineMarks)
	}
	for _, x := range got.Texts {
		if x != "1:#text" {
			t.Errorf("a line is not one text node: %v", got.Texts)
		}
	}
	if len(got.Reread) != 2 || !strings.HasSuffix(got.Reread[0], "/changes") || !strings.Contains(got.Reread[1], "/diff?") {
		t.Errorf("read again: %v", got.Reread)
	}
	if !got.Back {
		t.Error("← Files did not go back to the list")
	}
	if !strings.Contains(got.NoneOpen, "No session open") || !strings.Contains(got.NoneOpen, "SESSIONS WITH CHANGES") ||
		strings.Contains(got.NoneOpen, "feat-c") || strings.Contains(got.NoneOpen, "old") || !slices.Equal(got.Went, []string{"feat-a"}) {
		t.Errorf("none open: %q went %v", got.NoneOpen, got.Went)
	}
	if got.After != 0 {
		t.Errorf("read %d times after unmount", got.After)
	}
}
