package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pekoe/internal/engine"
	"pekoe/internal/errcatch"
	"pekoe/internal/paths"
	"pekoe/internal/state"
)

// dev window output → one report per distinct error (chunks, repeats), an
// error.updated event, nothing written to the agent until error.send; the
// sent file has the output and the source excerpt and the pty gets one line.
func TestDevErrorsBecomeCards(t *testing.T) {
	d, fe, repo := setup(t)
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	os.MkdirAll(filepath.Join(repo, "src"), 0o755)
	var src strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&src, "const l%d = %d\n", i, i)
	}
	os.WriteFile(filepath.Join(repo, "src/api.ts"), []byte(src.String()), 0o644)

	var mu sync.Mutex
	var events []errorEvent
	d.testHook = func(ev string, data any) {
		if e, ok := data.(errorEvent); ok && ev == "error.updated" {
			mu.Lock()
			events = append(events, e)
			mu.Unlock()
		}
	}
	out := "\x1b[31mTypeError: Cannot read properties of undefined (reading 'id')\x1b[39m\n" +
		"    at handler (" + repo + "/src/api.ts:12:7)\n    at node:internal/process/task_queues:95:5\n"
	sentBefore := len(fe.sent)
	now := time.Now()
	for rep := 0; rep < 3; rep++ { // HMR prints it three times
		for i := 0; i < len(out); i += 9 {
			d.onOutput(engine.Output{Slug: s.Slug, Window: engine.WinDev, Data: []byte(out[i:min(i+9, len(out))])})
		}
		d.flushDev(now.Add(time.Duration(rep+1) * time.Second))
	}
	list := call[[]errcatch.Report](t, d, "error.list", map[string]any{"slug": s.Slug})
	if len(list) != 1 {
		t.Fatalf("want 1 report, got %+v", list)
	}
	r := list[0]
	if r.Count != 3 || r.Source != "dev" || r.File != "src/api.ts" || r.Line != 12 || !r.InTree {
		t.Fatalf("report: %+v", r)
	}
	mu.Lock()
	if len(events) != 3 || !events[0].Fresh || events[1].Fresh {
		t.Fatalf("events: first fresh, then counted: %+v", events)
	}
	mu.Unlock()
	if len(fe.sent) != sentBefore {
		t.Fatalf("nothing goes to the agent before the human sends: %v", fe.sent[sentBefore:])
	}
	// agent windows are not scanned
	d.onOutput(engine.Output{Slug: s.Slug, Window: engine.WinAgent, Data: []byte("RangeError: x\n    at f (/a.js:1:1)\nok\n")})
	if n := len(call[[]errcatch.Report](t, d, "error.list", map[string]any{})); n != 1 {
		t.Fatalf("agent output was scanned: %d", n)
	}

	det := call[errDetail](t, d, "error.get", map[string]any{"id": r.ID})
	if !strings.Contains(det.Excerpt, "> 12 | const l12 = 12") || !strings.Contains(det.Excerpt, "   7 | const l7") || !strings.Contains(det.Excerpt, "  17 | const l17") {
		t.Fatalf("excerpt:\n%s", det.Excerpt)
	}

	call[errcatch.Report](t, d, "error.send", map[string]any{"id": r.ID})
	var text string
	for _, x := range fe.sent[sentBefore:] {
		if strings.HasPrefix(x, "text:") {
			text = strings.TrimPrefix(x, "text:")
		}
	}
	if !strings.HasPrefix(text, "@") || strings.Contains(text, "\n") || !strings.Contains(text, "/errors/"+s.Slug+"-0001.md dev server で出ているエラーです") {
		t.Fatalf("pty line: %q", text)
	}
	if !strings.HasSuffix(fe.last(), "keys:Enter") {
		t.Fatalf("the prompt is submitted: %s", fe.last())
	}
	file := strings.Fields(strings.TrimPrefix(text, "@"))[0]
	md, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"TypeError: Cannot read properties of undefined", "- seen: 3 times", "## source: src/api.ts:12", "> 12 | const l12 = 12"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("file lacks %q:\n%s", want, md)
		}
	}
	if n := len(call[[]errcatch.Report](t, d, "error.list", map[string]any{"slug": s.Slug})); n != 0 {
		t.Fatal("a sent error is no longer offered")
	}
	call[any](t, d, "worktree.rm", map[string]any{"slug": s.Slug, "force": true})
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("rm must remove the session's error files")
	}
	if n := len(call[[]errcatch.Report](t, d, "error.list", map[string]any{"all": true})); n != 0 {
		t.Fatal("rm must forget the session's errors")
	}
}

// Browser errors with a stack become cards; text-only console.error stays in
// the Browser panel. Sending the card removes it from the panel's list; the
// bulk send marks the cards of that port sent.
func TestBrowserErrorsShareTheCardFlow(t *testing.T) {
	d, fe, repo := setup(t)
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	d.token = "secret"
	post := func(body string) {
		r := httptest.NewRequest("POST", "/console", strings.NewReader(body))
		r.Header.Set("Origin", "chrome-extension://x")
		r.Header.Set("Authorization", "Bearer secret")
		d.guard(d.httpConsole)(httptest.NewRecorder(), r)
	}
	url := fmt.Sprintf("http://localhost:%d/list", s.Port)
	post(fmt.Sprintf(`{"url":%q,"errors":[
		{"message":"Uncaught TypeError: a is null","source":"http://localhost:%d/src/List.tsx","line":8,"stack":"TypeError: a is null\n    at List (http://localhost:%d/src/List.tsx?t=1:8:3)"},
		{"message":"Warning: Each child in a list should have a unique \"key\" prop."}
	]}`, url, s.Port, s.Port))
	list := call[[]errcatch.Report](t, d, "error.list", map[string]any{"slug": s.Slug})
	if len(list) != 1 || list[0].Source != "browser" || list[0].Port != s.Port || list[0].Message != "TypeError: a is null" || list[0].URL != url {
		t.Fatalf("browser reports: %+v", list)
	}
	if tabs := d.browserTabs(); len(tabs) != 0 {
		t.Fatalf("no tabs reported yet: %+v", tabs)
	}
	d.mu.Lock()
	n := len(d.errors[s.Port])
	d.mu.Unlock()
	if n != 2 {
		t.Fatalf("the Browser panel keeps every console error: %d", n)
	}
	// the stack is short but multi-line: a file, not a paste
	call[errcatch.Report](t, d, "error.send", map[string]any{"id": list[0].ID})
	if !strings.Contains(fe.sent[len(fe.sent)-2], "browser :"+fmt.Sprint(s.Port)+" で出ているエラーです") {
		t.Fatalf("sent: %v", fe.sent[len(fe.sent)-2:])
	}
	d.mu.Lock()
	left := append([]ConsoleError(nil), d.errors[s.Port]...)
	d.mu.Unlock()
	if len(left) != 1 || !strings.HasPrefix(left[0].Message, "Warning") {
		t.Fatalf("the sent error leaves the Browser panel: %+v", left)
	}

	// bulk send (E) marks the open cards of the port sent, via a file too
	post(fmt.Sprintf(`{"url":%q,"errors":[{"message":"Error: other","stack":"Error: other\n    at f (http://localhost:%d/src/f.ts:1:1)"}]}`, url, s.Port))
	if n := len(call[[]errcatch.Report](t, d, "error.list", map[string]any{"slug": s.Slug})); n != 1 {
		t.Fatalf("open: %d", n)
	}
	res := call[map[string]any](t, d, "browser.send_errors", map[string]any{"port": s.Port})
	if res["sent"].(float64) != 2 {
		t.Fatalf("bulk: %+v", res)
	}
	if !strings.Contains(fe.sent[len(fe.sent)-2], "console error が 2 件") || !strings.HasPrefix(fe.sent[len(fe.sent)-2], "text:@") {
		t.Fatalf("bulk line: %s", fe.sent[len(fe.sent)-2])
	}
	// the file fences what the page wrote, after a note
	file := strings.Fields(strings.TrimPrefix(fe.sent[len(fe.sent)-2], "text:@"))[0]
	if b, err := os.ReadFile(file); err != nil || !strings.Contains(string(b), "信頼できないデータ") || !strings.Contains(string(b), "```text\n- Warning") || !strings.Contains(string(b), "\n- Error: other\n") {
		t.Fatalf("bulk file: %v\n%s", err, b)
	}
	if n := len(call[[]errcatch.Report](t, d, "error.list", map[string]any{"slug": s.Slug})); n != 0 {
		t.Fatal("bulk send must mark the cards sent")
	}
	// ignore
	post(fmt.Sprintf(`{"url":%q,"errors":[{"message":"Error: third","stack":"Error: third\n    at g (http://localhost:%d/src/g.ts:1:1)"}]}`, url, s.Port))
	l := call[[]errcatch.Report](t, d, "error.list", map[string]any{"slug": s.Slug})
	call[errcatch.Report](t, d, "error.ignore", map[string]any{"id": l[0].ID})
	if n := len(call[[]errcatch.Report](t, d, "error.list", map[string]any{"slug": s.Slug})); n != 0 {
		t.Fatal("ignored error still offered")
	}
}

// A localhost page decides what it logs: one request keeps at most
// consoleMaxPerReq errors with each field cut, and a port gets at most
// consoleMaxPerMin errors a minute.
func TestConsoleLimits(t *testing.T) {
	d, _, _ := setup(t)
	d.token = "secret"
	post := func(port, n int, msg string) int {
		errs := make([]ConsoleError, n)
		for i := range errs {
			errs[i] = ConsoleError{Message: fmt.Sprintf("%s %d", msg, i)}
			if i == 0 {
				errs[i].Stack, errs[i].Source = strings.Repeat("s", consoleMaxStack*2), strings.Repeat("x", 5000)
			}
		}
		b, _ := json.Marshal(map[string]any{"url": fmt.Sprintf("http://localhost:%d/", port), "errors": errs})
		r := httptest.NewRequest("POST", "/console", bytes.NewReader(b))
		r.Header.Set("Origin", "chrome-extension://x")
		r.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		d.guard(d.httpConsole)(w, r)
		return w.Code
	}
	kept := func(port int) []ConsoleError {
		d.mu.Lock()
		defer d.mu.Unlock()
		return append([]ConsoleError(nil), d.errors[port]...)
	}
	if c := post(4100, 200, "boom"); c != 200 {
		t.Fatalf("post: %d", c)
	}
	got := kept(4100)
	if len(got) != consoleMaxPerReq {
		t.Fatalf("kept %d of one request", len(got))
	}
	if n := len([]rune(got[0].Stack)); n != consoleMaxStack || len([]rune(got[0].Source)) != consoleMaxField {
		t.Fatalf("fields not cut: stack %d source %d", n, len([]rune(got[0].Source)))
	}
	// the minute's budget: 120 in all, the rest dropped
	post(4101, 50, "a")
	post(4101, 50, "b")
	post(4101, 50, "c")
	post(4101, 50, "d")
	d.mu.Lock()
	taken := d.consoleRate[4101].n
	d.mu.Unlock()
	if taken != consoleMaxPerMin {
		t.Fatalf("took %d in a minute", taken)
	}
	if n := len(kept(4101)); n != 100 { // the panel keeps the last 100
		t.Fatalf("kept %d", n)
	}
	post(4101, 1, "late")
	if l := kept(4101); l[len(l)-1].Message == "late 0" {
		t.Fatal("over the minute's budget, still taken")
	}
	// another port has its own budget; a new minute refills it
	if post(4102, 1, "e"); len(kept(4102)) != 1 {
		t.Fatal("other port")
	}
	if n := d.consoleBudget(4101, 10, time.Now().Add(61*time.Second)); n != 10 {
		t.Fatalf("next minute: %d", n)
	}
	// a body over the cap is refused
	big := `{"url":"http://localhost:4103/","errors":[{"message":"` + strings.Repeat("m", consoleMaxBody) + `"}]}`
	r := httptest.NewRequest("POST", "/console", strings.NewReader(big))
	r.Header.Set("Origin", "chrome-extension://x")
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	d.guard(d.httpConsole)(w, r)
	if w.Code != http.StatusBadRequest || len(kept(4103)) != 0 {
		t.Fatalf("big body: %d", w.Code)
	}
}

// rm of fix-login leaves the attachments of fix-login-page alone.
func TestRemoveAttachmentsOnlyOwn(t *testing.T) {
	t.Setenv("PEKOE_HOME", t.TempDir())
	dir := paths.ErrorsDir("r")
	os.MkdirAll(dir, 0o755)
	for _, f := range []string{"fix-login-0001.md", "fix-login-page-0003.md", "fix-login-2-0004.md"} {
		os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644)
	}
	removeAttachments("r", "fix-login")
	if _, err := os.Stat(filepath.Join(dir, "fix-login-0001.md")); !os.IsNotExist(err) {
		t.Fatal("own attachment kept")
	}
	for _, f := range []string{"fix-login-page-0003.md", "fix-login-2-0004.md"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("%s of another session removed", f)
		}
	}
}
