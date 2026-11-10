package daemon

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"pekoe/internal/state"
)

// webInboxSetup: the web handler, with session "main" open and running
// (so it can ask).
func webInboxSetup(t *testing.T, devices ...string) (*Daemon, *fakeEngine, string, http.Handler) {
	t.Helper()
	d, fe, repo, h := webSetup(t, devices...)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	d.setState("main", state.Running, "")
	return d, fe, repo, h
}

func webInboxItems(t *testing.T, h http.Handler) []state.InboxItem {
	t.Helper()
	w := doWeb(t, h, webReq{path: "/api/inbox", from: ipPhone})
	if w.Code != 200 {
		t.Fatalf("inbox: %d %s", w.Code, w.Body)
	}
	var res struct{ Items []state.InboxItem }
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	return res.Items
}

func inboxAnswerReq(from string, id int64, body map[string]any) webReq {
	return webReq{method: "POST", path: "/api/inbox/" + itoa(id) + "/answer", from: from, body: body}
}

// Completion criterion: the inbox is answered from the web UI, and an
// answer to a rev that is not the item's any more is refused with 409.
func TestWebInboxAnswer(t *testing.T) {
	d, _, repo, h := webInboxSetup(t)
	first := ask(t, d, "main", map[string]any{"question": "merge?", "kind": "approve"})
	second := ask(t, d, "main", map[string]any{"question": "which?", "kind": "decision", "options": []string{"a", "b"}, "recommend": 1})
	its := webInboxItems(t, h)
	if len(its) != 2 || its[0].ID != first.ID || its[1].ID != second.ID {
		t.Fatalf("open items, oldest first: %+v", its)
	}
	if its[0].Facts == nil || its[0].Facts.Slug != "main" || its[0].Facts.Head == "" {
		t.Fatalf("facts: %+v", its[0].Facts)
	}

	// HEAD moves after the person looked: their rev is refused
	seen := its[0]
	os.WriteFile(filepath.Join(repo, "b.txt"), []byte("x"), 0o644)
	run(t, repo, "git", "add", ".")
	run(t, repo, "git", "commit", "-qm", "more")
	w := doWeb(t, h, inboxAnswerReq(ipPhone, seen.ID, map[string]any{"choice": 0, "rev": seen.Rev}))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "look again") {
		t.Fatalf("stale rev: %d %s", w.Code, w.Body)
	}
	if it := inboxGet(t, d, seen.ID); it.Status != state.InboxOpen || it.Rev == seen.Rev {
		t.Fatalf("after a refused answer: %+v", it)
	}

	for name, q := range map[string]struct {
		id   string
		body map[string]any
		code int
	}{
		"no rev":       {itoa(seen.ID), map[string]any{"choice": 0}, http.StatusBadRequest},
		"out of range": {itoa(seen.ID), map[string]any{"choice": 5, "rev": seen.Rev + 1}, http.StatusBadRequest},
		"no choice":    {itoa(seen.ID), map[string]any{"rev": seen.Rev + 1}, http.StatusBadRequest},
		"bad id":       {"x", map[string]any{"choice": 0, "rev": 1}, http.StatusBadRequest},
		"no such item": {"999", map[string]any{"choice": 0, "rev": 1}, http.StatusNotFound},
	} {
		w := doWeb(t, h, webReq{method: "POST", path: "/api/inbox/" + q.id + "/answer", from: ipPhone, body: q.body})
		if w.Code != q.code {
			t.Errorf("%s: %d %s, want %d", name, w.Code, w.Body, q.code)
		}
	}

	// look again, answer the rev now shown
	now := webInboxItems(t, h)[0]
	w = doWeb(t, h, inboxAnswerReq(ipPhone, now.ID, map[string]any{"choice": 0, "rev": now.Rev, "note": "go"}))
	if w.Code != 200 {
		t.Fatalf("answer: %d %s", w.Code, w.Body)
	}
	got := inboxGet(t, d, now.ID)
	if got.Status != state.InboxAnswered || got.Answer == nil || *got.Answer.Choice != 0 || got.Answer.By != callerHuman ||
		got.Answer.Via != "web:iphone" || got.Answer.Note != "go" {
		t.Fatalf("answered: %+v %+v", got, got.Answer)
	}
	// answered already: 409, and it leaves the inbox list
	if w := doWeb(t, h, inboxAnswerReq(ipPhone, now.ID, map[string]any{"choice": 1, "rev": got.Rev})); w.Code != http.StatusConflict {
		t.Fatalf("answer twice: %d %s", w.Code, w.Body)
	}
	if its := webInboxItems(t, h); len(its) != 1 || its[0].ID != second.ID {
		t.Fatalf("left open: %+v", its)
	}
}

// Completion criterion: a question with <script> in it is never HTML. The
// API sends it as JSON (no tag survives: encoding/json writes <), with
// nosniff and the CSP that allows no inline script; the page puts text in
// with textContent and never builds HTML (app.js has no HTML sink).
func TestWebInboxScriptIsText(t *testing.T) {
	d, _, _, h := webInboxSetup(t)
	evil := `<script>alert(1)</script>`
	it := ask(t, d, "main", map[string]any{
		"question": "ok? " + evil, "kind": "decision",
		"context": `<img src=x onerror=alert(2)>`,
		"options": []string{"<b>yes</b>", "no"},
		"links":   []string{"javascript:alert(3)", "https://github.com/x/y/pull/1"},
	})
	w := doWeb(t, h, webReq{path: "/api/inbox", from: ipPhone})
	body := w.Body.String()
	if w.Code != 200 || strings.Contains(body, "<script") || strings.Contains(body, "<img") || strings.Contains(body, "<b>") {
		t.Fatalf("raw tags in the response: %d %s", w.Code, body)
	}
	hd := w.Header()
	if !strings.HasPrefix(hd.Get("Content-Type"), "application/json") || hd.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.Contains(hd.Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatalf("headers: %v", hd)
	}
	if its := webInboxItems(t, h); len(its) != 1 || its[0].ID != it.ID || its[0].Question != "ok? "+evil {
		t.Fatalf("the words, as they were: %+v", its)
	}

	js, err := webFiles.ReadFile("webui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	sinks := regexp.MustCompile(`innerHTML|outerHTML|insertAdjacentHTML|document\.write|createContextualFragment|DOMParser|srcdoc|\beval\(|new Function|setAttribute\(\s*["']on`)
	if m := sinks.FindAllString(string(js), -1); len(m) > 0 {
		t.Fatalf("app.js builds HTML or code from strings: %v", m)
	}
	// links: only http(s) become anchors (a javascript: link stays text)
	if !strings.Contains(string(js), `/^https?:\/\//i`) {
		t.Fatalf("app.js does not check a link's scheme before making it an anchor")
	}
}

// The new endpoints are behind the same door as the rest: other users,
// tagged nodes, this machine, devices outside allow_devices and writes
// without the header are refused, and nothing is answered.
func TestWebInboxRejectsOthers(t *testing.T) {
	d, _, _, h := webInboxSetup(t, "iPhone")
	it := ask(t, d, "main", map[string]any{"question": "merge?", "kind": "approve"})
	ans := map[string]any{"choice": 0, "rev": it.Rev}
	for name, from := range map[string]string{
		"other user":               ipOther,
		"tagged node":              ipTagged,
		"unknown peer":             ipNobody,
		"this machine":             ipMac,
		"this machine (userspace)": ipMacUS,
		"loopback":                 "127.0.0.1",
		"device not allowed":       ipLaptop,
	} {
		for _, q := range []webReq{
			{path: "/api/inbox", from: from},
			{path: "/api/tapi", from: from},
			{path: "/api/overview", from: from},
			inboxAnswerReq(from, it.ID, ans),
		} {
			if w := doWeb(t, h, q); w.Code != http.StatusForbidden {
				t.Errorf("%s %s %s: %d, want 403", name, q.method, q.path, w.Code)
			}
		}
	}
	q := inboxAnswerReq(ipPhone, it.ID, ans)
	q.noWebHeader = true
	if w := doWeb(t, h, q); w.Code != http.StatusForbidden {
		t.Errorf("no %s: %d", webHeader, w.Code)
	}
	q = inboxAnswerReq(ipPhone, it.ID, ans)
	q.hdr = map[string]string{"Origin": "https://evil.example.com"}
	if w := doWeb(t, h, q); w.Code != http.StatusForbidden {
		t.Errorf("cross origin: %d", w.Code)
	}
	if got := inboxGet(t, d, it.ID); got.Status != state.InboxOpen {
		t.Fatalf("a refused request answered: %+v", got)
	}
	if w := doWeb(t, h, inboxAnswerReq(ipPhone, it.ID, ans)); w.Code != 200 {
		t.Fatalf("the allowed device: %d %s", w.Code, w.Body)
	}
}

func TestWebTapi(t *testing.T) {
	d, _, _, h := webInboxSetup(t)
	ask(t, d, "main", map[string]any{"question": "merge?", "kind": "approve"})
	d.tapiUpdate(func(ts *tapiState) {
		ts.state, ts.lastMessage, ts.sessionID = state.Waiting, "PR #42 を見てください", "claude-secret-id"
		ts.waitKind, ts.pending = "permission", &state.Tool{Name: "Bash", Input: "gh api secret"}
	})
	w := doWeb(t, h, webReq{path: "/api/tapi", from: ipPhone})
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	body := w.Body.String()
	for _, leak := range []string{"claude-secret-id", "gh api secret", `"dir"`} {
		if strings.Contains(body, leak) {
			t.Errorf("tapi shows %s: %s", leak, body)
		}
	}
	var v webTapi
	json.Unmarshal(w.Body.Bytes(), &v)
	if v.State != state.Waiting || v.LastMessage != "PR #42 を見てください" || v.InboxOpen != 1 || v.Look.Word != "wait" || v.StateSince.IsZero() {
		t.Fatalf("tapi: %+v", v)
	}
}

// The overview: the counts, and each session's flow with what merge.check
// says is missing.
func TestWebOverview(t *testing.T) {
	d, _, repo, h := webInboxSetup(t)
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/a"})
	waitCreated(t, d, "feat-a")
	d.update("feat-a", func(x *state.Session) { x.Parent, x.Request = TapiSlug, 0 })
	d.setState("feat-a", state.Running, "")
	// main: landed by pekoe land, PR #42 green and CLEAN, reviewed at an
	// older HEAD, after feat-a (still running)
	d.update("main", func(x *state.Session) {
		x.PR, x.Landing, x.After, x.Parent = 42, true, []string{"feat-a"}, TapiSlug
		x.PRStatus = &state.PRStatus{State: "OPEN", Pass: 3, MergeState: "CLEAN", HeadSHA: mergeHead}
	})
	review(t, d, "pass", mergeHead2[:10])
	ask(t, d, "main", map[string]any{"question": "merge?", "kind": "approve"})

	w := doWeb(t, h, webReq{path: "/api/overview", from: ipPhone})
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), repo) {
		t.Fatalf("leaks paths: %s", w.Body)
	}
	var res struct {
		Counts webCounts
		Groups []webFlowGroup
	}
	json.Unmarshal(w.Body.Bytes(), &res)
	c := res.Counts
	if c.Running != 2 || c.OpenPRs != 1 || c.Ready != 0 || c.InboxOpen != 1 {
		t.Fatalf("counts: %+v", c)
	}
	if len(res.Groups) != 1 || res.Groups[0].Parent != TapiSlug || len(res.Groups[0].Rows) != 2 {
		t.Fatalf("groups: %s", w.Body)
	}
	var main, feat *webFlow
	for i, f := range res.Groups[0].Rows {
		switch f.Slug {
		case "main":
			main = &res.Groups[0].Rows[i]
		case "feat-a":
			feat = &res.Groups[0].Rows[i]
		}
	}
	steps := func(f *webFlow) string {
		var out []string
		for _, s := range f.Steps {
			out = append(out, s.Name+":"+s.Status)
		}
		return strings.Join(out, " ")
	}
	if got := steps(main); got != "impl:done review:now pr:done ci:done merge:todo" {
		t.Errorf("main: %s", got)
	}
	if main.Merge == nil || strings.Join(main.Merge.Missing, ",") != "review,after" {
		t.Errorf("main's missing: %+v", main.Merge)
	}
	if got := steps(feat); got != "impl:now review:todo pr:todo ci:todo merge:todo" || feat.Merge != nil {
		t.Errorf("feat-a: %s %+v", got, feat.Merge)
	}

	// a failed review and a red CI show as fail
	review(t, d, "fail", mergeHead[:10])
	d.update("main", func(x *state.Session) { x.PRStatus.Fail, x.PRStatus.Pass = 1, 2 })
	w = doWeb(t, h, webReq{path: "/api/overview", from: ipPhone})
	json.Unmarshal(w.Body.Bytes(), &res)
	for _, f := range res.Groups[0].Rows {
		if f.Slug == "main" {
			if got := steps(&f); got != "impl:done review:fail pr:done ci:fail merge:todo" {
				t.Errorf("main after a fail: %s", got)
			}
		}
	}
}

// Sessions are grouped by who asked for them and for which request.
func TestWebOverviewGroups(t *testing.T) {
	d, _, repo, h := webInboxSetup(t)
	e := state.LedgerEntry{Text: "CSV の空行を飛ばして", Status: "open"}
	if err := d.DB.AddLedger(&e); err != nil {
		t.Fatal(err)
	}
	call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/a"})
	waitCreated(t, d, "feat-a")
	d.update("feat-a", func(x *state.Session) { x.Parent, x.Request = TapiSlug, e.ID })
	w := doWeb(t, h, webReq{path: "/api/overview", from: ipPhone})
	var res struct{ Groups []webFlowGroup }
	json.Unmarshal(w.Body.Bytes(), &res)
	if len(res.Groups) != 2 {
		t.Fatalf("groups: %s", w.Body)
	}
	g := res.Groups[0]
	if g.Parent != TapiSlug || g.Request == nil || g.Request.ID != e.ID || g.Request.Text != e.Text || g.Request.Status != "open" || g.Rows[0].Slug != "feat-a" {
		t.Fatalf("tapi's request first: %+v", g)
	}
	if g := res.Groups[1]; g.Parent != "" || g.Request != nil || g.Rows[0].Slug != "main" {
		t.Fatalf("opened directly last: %+v", g)
	}
}
