package daemon

import (
	"encoding/json"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// GET /api/worktrees: every repository pekoe knows (with a directory) and
// its worktrees as worktree.list has them: the main checkout first, a
// session's worktree with its slug and port, a worktree git knows but no
// session runs in without one, and one outside ~/worktrees not managed.
// The GUI's mux only, with a session token; the phone has no such route.
// It reads: a GET removes nothing and makes nothing.
func TestGUIWorktrees(t *testing.T) {
	d, _, repo, h, _ := guiSetup(t)
	call[[]KnownRepo](t, d, "repo.list", map[string]any{"cwd": repo})
	s := guiSignIn(t, d, h)
	if w := doGUI(t, h, guiReq{path: "/api/worktrees"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("no bearer: %d", w.Code)
	}
	if w := doWeb(t, testWebHandler(d), webReq{path: "/api/worktrees", from: ipPhone}); w.Code == 200 {
		t.Fatalf("the phone lists worktrees: %d %s", w.Code, w.Body)
	}
	rs := call[[]KnownRepo](t, d, "repo.list", nil)
	if len(rs) != 1 {
		t.Fatalf("repos: %+v", rs)
	}
	if w := doGUI(t, h, guiReq{method: "POST", path: "/api/sessions", bearer: s, body: map[string]any{"repo_id": rs[0].ID, "branch": "feat/wt"}}); w.Code != 200 {
		t.Fatalf("new: %d %s", w.Code, w.Body)
	}
	// a worktree a person made by hand, outside ~/worktrees
	bare := filepath.Join(t.TempDir(), "by-hand")
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-q", "-b", "by-hand", bare).CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v %s", err, out)
	}
	w := doGUI(t, h, guiReq{path: "/api/worktrees", bearer: s})
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("worktrees: %d %v %s", w.Code, w.Header(), w.Body)
	}
	var got struct {
		Repos []guiWorktreeRepo `json:"repos"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Repos) != 1 || got.Repos[0].ID != rs[0].ID || got.Repos[0].Dir == "" || got.Repos[0].Name == "" {
		t.Fatalf("repos: %s", w.Body)
	}
	ws := got.Repos[0].Worktrees
	if len(ws) != 3 || !ws[0].Main || ws[0].Slug != "" {
		t.Fatalf("worktrees: %+v", ws)
	}
	var sess, hand *WorktreeView
	for i := range ws {
		switch ws[i].Branch {
		case "feat/wt":
			sess = &ws[i]
		case "by-hand":
			hand = &ws[i]
		}
	}
	if sess == nil || sess.Slug != "feat-wt" || sess.Port == 0 || !sess.Managed || sess.State == "" {
		t.Fatalf("the session's worktree: %+v", sess)
	}
	if hand == nil || hand.Slug != "" || hand.Managed || hand.Main {
		t.Fatalf("the worktree by hand: %+v", hand)
	}
	// a read only: the same sessions, the worktrees still there
	if n := len(call[[]map[string]any](t, d, "session.list", nil)); n != 1 {
		t.Fatalf("sessions after the read: %d", n)
	}
	if out, err := exec.Command("git", "-C", repo, "worktree", "list").CombinedOutput(); err != nil || strings.Count(string(out), "\n") != 3 {
		t.Fatalf("worktrees after the read: %v %s", err, out)
	}
}

// The Worktrees view (gui/views/worktrees.js): a group per repository
// (its name and count), the main checkout first and tagged, a worktree
// outside ~/worktrees tagged, the port; a row with a session is a button
// with the session's state (as the Sessions view draws it) that opens it,
// the open one raised; one without a session is not a button and says so.
// The box filters by branch or repository. It reads GET /api/worktrees on
// its first update after mount and again on a refresh only when the sessions changed (or a while
// passed), says when it could not read, drops an answer that comes after
// it was hidden, and draws what the wire says as text only. Run in node
// on the small fake DOM (skipped without node).
func TestGUIWorktreesView(t *testing.T) {
	out := runWebModule(t, fakeDOM+`
N.prototype.removeAttribute = function (k) { delete this.attrs[k]; };
import view from "gui/views/worktrees.js";
const evil = "<img src=x onerror=alert(1)>";
let answer = null, reads = 0, pending = [];
globalThis.fetch = (path) => { reads++; return new Promise((res) => pending.push(() => res(answer(path)))); };
const ok = (j) => ({ ok: true, status: 200, json: async () => j, text: async () => "" });
const flush = async () => { while (pending.length) pending.shift()(); for (let i = 0; i < 20; i++) await null; };
const repos = { repos: [
  { repo_id: "p", name: "pekoe", dir: "/src/pekoe", worktrees: [
    { path: "/src/pekoe", branch: "main", main: true, managed: false },
    { path: "/wt/pekoe/feat-a", branch: "feat/a", slug: "feat-a", port: 3001, managed: true, main: false, state: "running" },
    { path: "/wt/pekoe/feat-b", branch: "feat/b" + evil, slug: "feat-b", port: 3002, managed: true, main: false, state: "waiting" },
    { path: "/tmp/by-hand", branch: "", managed: false, main: false },
  ] },
  { repo_id: "o", name: "other", dir: "/src/other", worktrees: [{ path: "/src/other", branch: "trunk", main: true, managed: false }] },
] };
const S = (slug, state, extra) => ({ slug, branch: slug, repo: "pekoe", state, state_since: new Date().toISOString(), look: { icon: "◉", role: "ok", word: state }, ...extra });
const data = { sessions: [S("feat-a", "running", { diffstat: { added: 5, deleted: 2 } }), S("feat-b", "waiting", { wait_kind: "question" })], tapi: null, overview: null, current: "feat-a" };
const went = [];
const r = {};
answer = () => ok(repos);
let box = el0("div");
view.mount(box, { go: (s) => went.push(s) });
r.loading = box.textContent;
r.readsMount = reads; // nothing read before the first update
view.update(data);
r.readsMount += 10 * reads;
await flush();
r.text = box.textContent;
const groups = find(box, (n) => n.className === "sgroup");
r.groups = groups.map((g) => find(g, (n) => /\bsgroup-head\b/.test(n.className))[0].textContent);
const rows = find(box, (n) => /\bview-worktrees-row\b/.test(n.className));
r.rows = rows.map((b) => [b.tag, b.className, b.attrs["aria-current"] || "", b.title, b.textContent]);
r.imgs = find(box, (n) => n.tag === "img").length;
rows[2].click();
rows[1].click();
rows[0].click();
rows[3].click();
r.went = went.slice();
view.query("trunk");
r.trunk = find(box, (n) => /\bview-worktrees-row\b/.test(n.className)).map((b) => b.textContent);
view.query("PEKOE");
r.byRepo = find(box, (n) => /\bview-worktrees-row\b/.test(n.className)).length;
view.query("zzz");
r.none = box.textContent;
view.query("");
// a refresh soon after, the same sessions: no read; a new session: a read
view.update(data);
r.readsSame = reads;
await flush();
view.update({ ...data, sessions: [...data.sessions, S("feat-c", "idle")] });
r.readsNew = reads;
await flush();
// a failed read keeps the rows and says why
answer = () => ({ ok: false, status: 500, statusText: "x", text: async () => "git failed" });
view.update({ ...data, sessions: data.sessions.slice(0, 1) });
await flush();
r.failed = box.textContent;
// hidden while reading: the late answer is dropped
answer = () => ok({ repos: [] });
view.update(data);
const before = box.textContent;
view.unmount();
await flush();
r.afterUnmount = box.textContent === before;
// shown again: the last rows at once, then a fresh read
box = el0("div");
reads = 0;
answer = () => ok({ repos: [] });
view.mount(box, { go: () => {} });
r.remount = [reads, box.textContent.includes("feat/a")];
view.update(data);
r.remount[0] = reads;
await flush();
r.empty = box.textContent;
console.log(JSON.stringify(r));`)
	var got struct {
		Loading, Text, None, Failed, Empty string
		ReadsMount, ReadsSame, ReadsNew     int
		Groups, Trunk                       []string
		Rows                                [][]string
		Imgs, ByRepo                        int
		Went                                []string
		AfterUnmount                        bool
		Remount                             []any
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("node: %v %s", err, out)
	}
	if !strings.Contains(got.Loading, "WORKTREES") || !strings.Contains(got.Loading, "Reading worktrees") {
		t.Errorf("before the answer: %q", got.Loading)
	}
	if got.ReadsMount != 10 {
		t.Errorf("reads: %d at mount (want 0), %d after the first update (want 1)", got.ReadsMount%10, got.ReadsMount/10)
	}
	if !strings.HasPrefix(got.Text, "WORKTREES5") || !strings.Contains(got.Text, "2 with a session · 3 without") {
		t.Errorf("view: %q", got.Text)
	}
	if len(got.Groups) != 2 || got.Groups[0] != "pekoe4" || got.Groups[1] != "other1" {
		t.Errorf("groups: %v", got.Groups)
	}
	if len(got.Rows) != 5 {
		t.Fatalf("rows: %v", got.Rows)
	}
	want := []struct {
		tag, cls, cur, title string
		has                  []string
	}{
		{"div", "view-worktrees-bare", "", "/src/pekoe", []string{"main", "main", "No session"}},
		{"button", "sel", "true", "/wt/pekoe/feat-a", []string{"feat/a", ":3001", "Running", "+5", "−2"}},
		{"button", "", "", "/wt/pekoe/feat-b", []string{"feat/b<img src=x onerror=alert(1)>", ":3002", "Waiting · question"}},
		{"div", "view-worktrees-bare", "", "/tmp/by-hand", []string{"by-hand", "detached", "outside ~/worktrees", "No session"}},
		{"div", "view-worktrees-bare", "", "/src/other", []string{"trunk", "main"}},
	}
	for i, w := range want {
		g := got.Rows[i]
		if g[0] != w.tag || !strings.Contains(g[1], w.cls) || g[2] != w.cur || g[3] != w.title {
			t.Errorf("row %d: %v", i, g)
		}
		for _, s := range w.has {
			if !strings.Contains(g[4], s) {
				t.Errorf("row %d lacks %q: %q", i, s, g[4])
			}
		}
	}
	if strings.Contains(got.Rows[1][1], "bare") || strings.Contains(got.Rows[2][1], "sel") {
		t.Errorf("rows' classes: %v", got.Rows)
	}
	if got.Imgs != 0 {
		t.Error("a branch's markup became an element")
	}
	if strings.Join(got.Went, ",") != "feat-b,feat-a" {
		t.Errorf("opened: %v (a row without a session opens nothing)", got.Went)
	}
	if len(got.Trunk) != 1 || !strings.Contains(got.Trunk[0], "trunk") || got.ByRepo != 4 || !strings.Contains(got.None, "Nothing matches") {
		t.Errorf("filter: %v %d %q", got.Trunk, got.ByRepo, got.None)
	}
	if got.ReadsSame != 2-1 || got.ReadsNew != 2 {
		t.Errorf("reads: same sessions %d (want 1), a new one %d (want 2)", got.ReadsSame, got.ReadsNew)
	}
	if !strings.Contains(got.Failed, "Could not read the worktrees · git failed") || !strings.Contains(got.Failed, "feat/a") {
		t.Errorf("failed: %q", got.Failed)
	}
	if !got.AfterUnmount {
		t.Error("an answer after unmount was drawn")
	}
	if len(got.Remount) != 2 || got.Remount[0] != float64(1) || got.Remount[1] != true {
		t.Errorf("remount: %v", got.Remount)
	}
	if !strings.Contains(got.Empty, "No repositories") {
		t.Errorf("no repositories: %q", got.Empty)
	}
}
