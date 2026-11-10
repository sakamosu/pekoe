package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pekoe/internal/state"
)

// webTapiSetup: the web handler with tapi chat open (idle).
func webTapiSetup(t *testing.T, devices ...string) (*Daemon, *fakeEngine, http.Handler) {
	t.Helper()
	d, fe, _, h := webSetup(t, devices...)
	call[Tapi](t, d, "tapi.open", nil)
	return d, fe, h
}

func tapiSendReq(from, text string) webReq {
	return webReq{method: "POST", path: "/api/tapi/send", from: from, body: map[string]any{"text": text}}
}

func tapiSends(t *testing.T, d *Daemon) []state.SendRecord {
	t.Helper()
	rs, err := d.DB.Sends(state.SendFilter{To: TapiSlug, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

// Without allow_devices, any device of the user could instruct tapi: the
// send is refused and nothing is typed.
func TestWebTapiSendNeedsAllowDevices(t *testing.T) {
	_, fe, h := webTapiSetup(t)
	before := sentCount(fe)
	w := doWeb(t, h, tapiSendReq(ipPhone, "feat-csv を作って"))
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "allow_devices") {
		t.Fatalf("send without allow_devices: %d %s", w.Code, w.Body)
	}
	if sentCount(fe) != before {
		t.Fatalf("typed into tapi: %s", fe.last())
	}
	var v webTapi
	json.Unmarshal(doWeb(t, h, webReq{path: "/api/tapi", from: ipPhone}).Body.Bytes(), &v)
	if v.Sendable {
		t.Fatalf("sendable without allow_devices: %+v", v)
	}
	// the screen and the keypad do not instruct: they stay
	if w := doWeb(t, h, webReq{path: "/api/tapi/screen", from: ipPhone}); w.Code != 200 {
		t.Fatalf("screen: %d %s", w.Code, w.Body)
	}
}

// With allow_devices: typed with Enter, recorded in the send log with the
// device, and the same checks as a session's send.
func TestWebTapiSend(t *testing.T) {
	d, fe, h := webTapiSetup(t, "iphone")
	var v webTapi
	json.Unmarshal(doWeb(t, h, webReq{path: "/api/tapi", from: ipPhone}).Body.Bytes(), &v)
	if !v.Sendable || !v.Started {
		t.Fatalf("tapi: %+v", v)
	}
	w := doWeb(t, h, tapiSendReq(ipPhone, "  feat-csv を作って  "))
	if w.Code != 200 {
		t.Fatalf("send: %d %s", w.Code, w.Body)
	}
	if !strings.HasSuffix(fe.last(), "text:feat-csv を作って\nkeys:Enter") {
		t.Fatalf("typed: %s", fe.last())
	}
	rs := tapiSends(t, d)
	if len(rs) != 1 {
		t.Fatalf("send log: %+v", rs)
	}
	if r := rs[0]; r.From != callerHuman || r.To != TapiSlug || r.Kind != "send" || r.Text != "feat-csv を作って" || r.Via != "web:iphone" || !r.OK {
		t.Fatalf("record: %+v", r)
	}
	// the person reads it back with send.log --to +tapi
	if got := call[[]state.SendRecord](t, d, "send.log", map[string]any{"to": TapiSlug}); len(got) != 1 || got[0].Via != "web:iphone" {
		t.Fatalf("send.log: %+v", got)
	}
	// other devices never get as far
	if w := doWeb(t, h, tapiSendReq(ipLaptop, "x")); w.Code != http.StatusForbidden {
		t.Fatalf("laptop: %d", w.Code)
	}

	// control characters are keys, not text
	before := sentCount(fe)
	for _, text := range []string{"a\x1b[Zb", "stop\x03", "x\x1b[201~y", "del\x7f", "", strings.Repeat("あ", webMaxText+1)} {
		if w := doWeb(t, h, tapiSendReq(ipPhone, text)); w.Code != http.StatusBadRequest {
			t.Errorf("send %q: %d", text, w.Code)
		}
	}
	if sentCount(fe) != before {
		t.Fatalf("typed a refused text: %s", fe.last())
	}

	// a permission prompt (hook state): 409, nothing typed, recorded as refused
	d.tapiUpdate(func(ts *tapiState) {
		ts.state, ts.waitKind, ts.pending = state.Waiting, "permission", &state.Tool{Name: "Bash", Input: "gh pr merge 42"}
	})
	if w := doWeb(t, h, tapiSendReq(ipPhone, "2つ目も")); w.Code != http.StatusConflict {
		t.Fatalf("send over a permission prompt: %d %s", w.Code, w.Body)
	}
	if sentCount(fe) != before {
		t.Fatalf("typed into the permission menu: %s", fe.last())
	}
	if rs := tapiSends(t, d); len(rs) != 2 || rs[0].OK || rs[0].Text != "2つ目も" || rs[0].Error == "" {
		t.Fatalf("refused send not recorded: %+v", rs)
	}

	// running, but a permission menu on the screen: 409 too
	d.tapiUpdate(func(ts *tapiState) { ts.state, ts.waitKind, ts.pending = state.Running, "", nil })
	fe.mu.Lock()
	fe.lines = []string{"│ Bash command", "│   gh pr merge 42", "│ Do you want to proceed?", "│ ❯ 1. Yes", "│   2. Yes, and don't ask again", "│   3. No (esc)", ""}
	fe.mu.Unlock()
	if w := doWeb(t, h, tapiSendReq(ipPhone, "続けて")); w.Code != http.StatusConflict || sentCount(fe) != before {
		t.Fatalf("send over a menu on screen: %d %s / %s", w.Code, w.Body, fe.last())
	}
	if rs := tapiSends(t, d); len(rs) != 3 || rs[0].OK {
		t.Fatalf("refused send not recorded: %+v", rs)
	}
}

func TestWebTapiNotStarted(t *testing.T) {
	_, fe, _, h := webSetup(t, "iphone")
	before := sentCount(fe)
	for _, q := range []webReq{
		tapiSendReq(ipPhone, "hi"),
		{path: "/api/tapi/screen", from: ipPhone},
		{method: "POST", path: "/api/tapi/keys", from: ipPhone, body: map[string]any{"keys": []string{"Enter"}}},
	} {
		if w := doWeb(t, h, q); w.Code != http.StatusConflict {
			t.Errorf("%s %s before tapi.open: %d %s", q.method, q.path, w.Code, w.Body)
		}
	}
	if sentCount(fe) != before {
		t.Fatalf("typed: %s", fe.last())
	}
}

// The screen is the session screen's shape: colored rows and plain lines.
func TestWebTapiScreen(t *testing.T) {
	_, fe, h := webTapiSetup(t)
	fe.mu.Lock()
	fe.lines = []string{"\x1b[31mred\x1b[0m line", "> "}
	fe.mu.Unlock()
	w := doWeb(t, h, webReq{path: "/api/tapi/screen", from: ipPhone})
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var res struct {
		Slug  string
		Rows  [][]map[string]any
		Lines []string
	}
	json.Unmarshal(w.Body.Bytes(), &res)
	if res.Slug != TapiSlug || len(res.Rows) != 2 || len(res.Lines) != 2 || res.Lines[0] != "red line" || res.Rows[0][0]["fg"] != "err" {
		t.Fatalf("screen: %s", w.Body)
	}
}

// The keypad: only while tapi waits on a question, only the session
// keypad's keys, never over a permission menu.
func TestWebTapiKeys(t *testing.T) {
	d, fe, h := webTapiSetup(t)
	keys := func(k ...string) *httptest.ResponseRecorder {
		return doWeb(t, h, webReq{method: "POST", path: "/api/tapi/keys", from: ipPhone, body: map[string]any{"keys": k}})
	}
	before := sentCount(fe)
	if w := keys("Down"); w.Code != http.StatusConflict {
		t.Fatalf("keys while idle: %d", w.Code)
	}
	d.tapiUpdate(func(ts *tapiState) { ts.state, ts.waitKind = state.Waiting, "permission" })
	if w := keys("2"); w.Code != http.StatusConflict {
		t.Fatalf("keys on a permission prompt: %d", w.Code)
	}
	d.tapiUpdate(func(ts *tapiState) { ts.waitKind = "question" })
	for _, bad := range [][]string{{"C-c"}, {"y"}, {"12"}, {}, make([]string, webMaxKeys+1)} {
		if w := keys(bad...); w.Code != http.StatusBadRequest {
			t.Errorf("keys %q: %d", bad, w.Code)
		}
	}
	fe.mu.Lock()
	fe.lines = []string{"│ Do you want to proceed?", "│ ❯ 1. Yes", "│   2. Yes, and don't ask again", ""}
	fe.mu.Unlock()
	if w := keys("2"); w.Code != http.StatusConflict {
		t.Fatalf("keys over a permission menu on screen: %d", w.Code)
	}
	if sentCount(fe) != before {
		t.Fatalf("pressed: %s", fe.last())
	}
	fe.mu.Lock()
	fe.lines = []string{"Which database?", "❯ 1. sqlite", "  2. postgres", ""}
	fe.mu.Unlock()
	if w := keys("Down", "Enter"); w.Code != 200 {
		t.Fatalf("keys on a question: %d %s", w.Code, w.Body)
	}
	if !strings.HasSuffix(fe.last(), "keys:Down,Enter") {
		t.Fatalf("pressed: %s", fe.last())
	}
}

// There is no way to answer tapi's permission prompt from the web: no
// route of its own, and the session routes do not reach +tapi.
func TestWebTapiNoAllowDeny(t *testing.T) {
	d, fe, h := webTapiSetup(t, "iphone")
	d.tapiUpdate(func(ts *tapiState) {
		ts.state, ts.waitKind, ts.pending = state.Waiting, "permission", &state.Tool{Name: "Bash", Input: "gh pr merge 42"}
	})
	before := sentCount(fe)
	for _, q := range []webReq{
		{method: "POST", path: "/api/tapi/answer", from: ipPhone, body: map[string]any{"answer": "allow"}},
		{method: "POST", path: "/api/tapi/allow", from: ipPhone, body: map[string]any{}},
		{method: "POST", path: "/api/sessions/+tapi/answer", from: ipPhone, body: map[string]any{"answer": "allow", "tool": map[string]any{"name": "Bash", "input": "gh pr merge 42"}}},
		{method: "POST", path: "/api/sessions/%2Btapi/answer", from: ipPhone, body: map[string]any{"answer": "allow"}},
		{method: "POST", path: "/api/sessions/+tapi/send", from: ipPhone, body: map[string]any{"text": "1"}},
		{method: "POST", path: "/api/sessions/+tapi/keys", from: ipPhone, body: map[string]any{"keys": []string{"Enter"}}},
		{method: "POST", path: "/api/tapi/keys", from: ipPhone, body: map[string]any{"keys": []string{"Enter"}}},
		tapiSendReq(ipPhone, "allow"),
	} {
		if w := doWeb(t, h, q); w.Code == 200 {
			t.Errorf("%s %s answered tapi: %s", q.method, q.path, w.Body)
		}
	}
	if sentCount(fe) != before {
		t.Fatalf("something reached tapi's permission prompt: %s", fe.last())
	}
	// and /api/tapi does not show what the prompt is about
	if body := doWeb(t, h, webReq{path: "/api/tapi", from: ipPhone}).Body.String(); strings.Contains(body, "gh pr merge") {
		t.Fatalf("pending tool shown: %s", body)
	}
}
