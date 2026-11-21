package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"pekoe/internal/paths"
	"pekoe/internal/state"
	"pekoe/internal/testenv"
)

// fakeListener stands in for 127.0.0.1:<port>: the tests call the handler
// directly (an agent's sandbox refuses binding a TCP port).
type fakeListener struct {
	port int
	once sync.Once
	done chan struct{}
}

func (l *fakeListener) Accept() (net.Conn, error) {
	<-l.done
	return nil, net.ErrClosed
}
func (l *fakeListener) Close() error { l.once.Do(func() { close(l.done) }); return nil }
func (l *fakeListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: l.port}
}

const guiTestPort = 47999

// guiSetup: a daemon whose GUI "listens" on a fake listener, a clock the
// test moves, and the GUI handler.
func guiSetup(t *testing.T) (*Daemon, *fakeEngine, string, http.Handler, *time.Time) {
	t.Helper()
	d, fe, repo := setup(t)
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	d.gui.now = func() time.Time { return now }
	d.gui.listen = func(network, addr string) (net.Listener, error) {
		return &fakeListener{port: guiTestPort, done: make(chan struct{})}, nil
	}
	t.Cleanup(func() { d.stopGUI() })
	return d, fe, repo, d.guiHandler(guiTestPort), &now
}

var guiPageTokenRe = regexp.MustCompile(`#t=([0-9a-f]{64})"`)

// guiOpenToken calls gui.open and reads the token back from the page, as
// the browser would.
func guiOpenToken(t *testing.T, d *Daemon) (guiOpenResult, string) {
	t.Helper()
	res := call[guiOpenResult](t, d, "gui.open", nil)
	b, err := os.ReadFile(res.File)
	if err != nil {
		t.Fatal(err)
	}
	m := guiPageTokenRe.FindSubmatch(b)
	if m == nil {
		t.Fatalf("no token in the page:\n%s", b)
	}
	return res, string(m[1])
}

type guiReq struct {
	method, path, host, from, bearer string
	body                             any
	hdr                              map[string]string
	noJSON                           bool
}

func doGUI(t *testing.T, h http.Handler, q guiReq) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if q.body != nil {
		b, _ := json.Marshal(q.body)
		body = strings.NewReader(string(b))
	}
	if q.method == "" {
		q.method = http.MethodGet
	}
	host := q.host
	if host == "" {
		host = fmt.Sprintf("127.0.0.1:%d", guiTestPort)
	}
	r := httptest.NewRequest(q.method, "http://"+host+q.path, body)
	r.Host = host
	r.RemoteAddr = "127.0.0.1:51234"
	if q.from != "" {
		r.RemoteAddr = q.from
	}
	if q.method == http.MethodPost && !q.noJSON {
		r.Header.Set("Content-Type", "application/json")
	}
	if q.bearer != "" {
		r.Header.Set("Authorization", "Bearer "+q.bearer)
	}
	for k, v := range q.hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// guiSignIn trades a fresh one-time token for a session token.
func guiSignIn(t *testing.T, d *Daemon, h http.Handler) string {
	t.Helper()
	_, tok := guiOpenToken(t, d)
	w := doGUI(t, h, guiReq{method: "POST", path: "/api/gui/session", body: map[string]any{"token": tok}})
	if w.Code != 200 {
		t.Fatalf("exchange: %d %s", w.Code, w.Body)
	}
	var out struct {
		Token string `json:"token"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	if len(out.Token) != 64 || out.Token == tok {
		t.Fatalf("session token: %q", out.Token)
	}
	return out.Token
}

// gui.open writes the redirect page: 0600 in a 0700 directory, the token
// only inside it (not in the answer the CLI prints from), the port from
// the listener.
func TestGUIOpenWritesPage(t *testing.T) {
	d, _, _, _, _ := guiSetup(t)
	res, tok := guiOpenToken(t, d)
	if res.Port != guiTestPort {
		t.Fatalf("port %d", res.Port)
	}
	if filepath.Dir(res.File) != paths.GUIDir() || !strings.HasPrefix(filepath.Base(res.File), "open-") || !strings.HasSuffix(res.File, ".html") {
		t.Fatalf("file %s", res.File)
	}
	fi, err := os.Stat(res.File)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("page mode: %v %v", fi.Mode(), err)
	}
	if di, err := os.Stat(paths.GUIDir()); err != nil || di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode: %v %v", di.Mode(), err)
	}
	b, _ := os.ReadFile(res.File)
	if !strings.Contains(string(b), fmt.Sprintf("http://127.0.0.1:%d/#t=%s", guiTestPort, tok)) {
		t.Fatalf("page:\n%s", b)
	}
	raw, _ := json.Marshal(res)
	if strings.Contains(string(raw), tok) {
		t.Fatalf("gui.open's answer carries the token: %s", raw)
	}
	if st := call[guiStatus](t, d, "gui.status", nil); !st.Listening || st.Port != guiTestPort || st.Sessions != 0 {
		t.Fatalf("status %+v", st)
	}
}

// The token trades once for a session token; the page goes; the API then
// needs the bearer.
func TestGUISignIn(t *testing.T) {
	d, _, _, h, _ := guiSetup(t)
	res, tok := guiOpenToken(t, d)
	// without a bearer the API is closed; the static files are not
	if w := doGUI(t, h, guiReq{path: "/api/sessions"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("no bearer: %d", w.Code)
	}
	if w := doGUI(t, h, guiReq{path: "/api/sessions", bearer: tok}); w.Code != http.StatusUnauthorized {
		t.Fatalf("the one-time token is no session token: %d", w.Code)
	}
	for _, p := range []string{"/", "/app.js", "/app.css", "/theme.css"} {
		if w := doGUI(t, h, guiReq{path: p}); w.Code != 200 {
			t.Fatalf("%s: %d", p, w.Code)
		}
	}
	w := doGUI(t, h, guiReq{method: "POST", path: "/api/gui/session", body: map[string]any{"token": tok}})
	if w.Code != 200 {
		t.Fatalf("exchange: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(res.File); !os.IsNotExist(err) {
		t.Fatalf("the page is still there: %v", err)
	}
	var out struct {
		Token string `json:"token"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	if w := doGUI(t, h, guiReq{path: "/api/sessions", bearer: out.Token}); w.Code != 200 {
		t.Fatalf("with the session token: %d %s", w.Code, w.Body)
	}
	if w := doGUI(t, h, guiReq{path: "/api/sessions", bearer: strings.Repeat("0", 64)}); w.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong token: %d", w.Code)
	}
	if w := doGUI(t, h, guiReq{path: "/api/sessions", hdr: map[string]string{"Authorization": out.Token}}); w.Code != http.StatusUnauthorized {
		t.Fatalf("not a Bearer: %d", w.Code)
	}
	var me webPeer
	json.Unmarshal(doGUI(t, h, guiReq{path: "/api/me", bearer: out.Token}).Body.Bytes(), &me)
	if me.Device != "gui" {
		t.Fatalf("me: %+v", me)
	}
	if st := call[guiStatus](t, d, "gui.status", nil); st.Sessions != 1 || st.LastUsed == nil {
		t.Fatalf("status %+v", st)
	}
}

// A one-time token used a second time: someone traded it before the
// person's browser. Every session token goes (the first one too).
func TestGUITokenUsedTwiceRevokesAll(t *testing.T) {
	d, _, _, h, _ := guiSetup(t)
	other := guiSignIn(t, d, h)
	_, tok := guiOpenToken(t, d)
	first := doGUI(t, h, guiReq{method: "POST", path: "/api/gui/session", body: map[string]any{"token": tok}})
	if first.Code != 200 {
		t.Fatalf("first: %d", first.Code)
	}
	var got struct {
		Token string `json:"token"`
	}
	json.Unmarshal(first.Body.Bytes(), &got)
	second := doGUI(t, h, guiReq{method: "POST", path: "/api/gui/session", body: map[string]any{"token": tok}})
	if second.Code != http.StatusUnauthorized || !strings.Contains(second.Body.String(), "someone else") {
		t.Fatalf("second: %d %s", second.Code, second.Body)
	}
	for _, s := range []string{got.Token, other} {
		if w := doGUI(t, h, guiReq{path: "/api/sessions", bearer: s}); w.Code != http.StatusUnauthorized {
			t.Fatalf("session token still works after the double use: %d", w.Code)
		}
	}
}

// 60 s: a one-time token from the history is of no use.
func TestGUITokenExpires(t *testing.T) {
	d, _, _, h, now := guiSetup(t)
	_, tok := guiOpenToken(t, d)
	*now = now.Add(guiOpenTTL + time.Second)
	w := doGUI(t, h, guiReq{method: "POST", path: "/api/gui/session", body: map[string]any{"token": tok}})
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "pekoe gui") {
		t.Fatalf("expired token: %d %s", w.Code, w.Body)
	}
	if w := doGUI(t, h, guiReq{method: "POST", path: "/api/gui/session", body: map[string]any{"token": strings.Repeat("a", 64)}}); w.Code != http.StatusUnauthorized {
		t.Fatalf("unknown token: %d", w.Code)
	}
}

// session_ttl after the last use; using it keeps it alive.
func TestGUISessionTTL(t *testing.T) {
	d, _, _, h, now := guiSetup(t)
	d.Global.GUI.SessionTTL = "1h"
	s := guiSignIn(t, d, h)
	*now = now.Add(50 * time.Minute)
	if w := doGUI(t, h, guiReq{path: "/api/sessions", bearer: s}); w.Code != 200 {
		t.Fatalf("within the ttl: %d", w.Code)
	}
	*now = now.Add(50 * time.Minute) // 50 min after the last use
	if w := doGUI(t, h, guiReq{path: "/api/sessions", bearer: s}); w.Code != 200 {
		t.Fatalf("used 50 min ago: %d", w.Code)
	}
	*now = now.Add(61 * time.Minute)
	if w := doGUI(t, h, guiReq{path: "/api/sessions", bearer: s}); w.Code != http.StatusUnauthorized {
		t.Fatalf("after the ttl: %d", w.Code)
	}
}

// --revoke signs every browser out; --stop also stops listening.
func TestGUIRevokeAndStop(t *testing.T) {
	d, _, _, h, _ := guiSetup(t)
	s := guiSignIn(t, d, h)
	res, _ := guiOpenToken(t, d)
	call[map[string]any](t, d, "gui.revoke", nil)
	if w := doGUI(t, h, guiReq{path: "/api/sessions", bearer: s}); w.Code != http.StatusUnauthorized {
		t.Fatalf("after revoke: %d", w.Code)
	}
	if _, err := os.Stat(res.File); !os.IsNotExist(err) {
		t.Fatalf("an unused page survived revoke: %v", err)
	}
	s = guiSignIn(t, d, h)
	if r := call[map[string]any](t, d, "gui.stop", nil); r["stopped"] != true {
		t.Fatalf("stop: %v", r)
	}
	if st := call[guiStatus](t, d, "gui.status", nil); st.Listening || st.Sessions != 0 {
		t.Fatalf("status after stop: %+v", st)
	}
	if w := doGUI(t, h, guiReq{path: "/api/sessions", bearer: s}); w.Code != http.StatusUnauthorized {
		t.Fatalf("after stop: %d", w.Code)
	}
}

// guiGuard: loopback, the exact Host, same origin, no CORS, JSON writes,
// the web UI's headers.
func TestGUIGuard(t *testing.T) {
	d, _, _, h, _ := guiSetup(t)
	s := guiSignIn(t, d, h)
	self := fmt.Sprintf("http://127.0.0.1:%d", guiTestPort)
	for _, c := range []struct {
		name string
		q    guiReq
		code int
	}{
		{"ok", guiReq{path: "/api/sessions", bearer: s}, 200},
		{"same origin", guiReq{path: "/api/sessions", bearer: s, hdr: map[string]string{"Origin": self, "Sec-Fetch-Site": "same-origin"}}, 200},
		{"not loopback", guiReq{path: "/api/sessions", bearer: s, from: "192.168.1.5:5000"}, 403},
		{"not loopback, static", guiReq{path: "/", from: "192.168.1.5:5000"}, 403},
		{"localhost", guiReq{path: "/api/sessions", bearer: s, host: fmt.Sprintf("localhost:%d", guiTestPort)}, 421},
		{"localhost, static", guiReq{path: "/", host: fmt.Sprintf("localhost:%d", guiTestPort)}, 421},
		{"another port", guiReq{path: "/api/sessions", bearer: s, host: "127.0.0.1:3005"}, 421},
		{"no port", guiReq{path: "/", host: "127.0.0.1"}, 421},
		{"rebinding", guiReq{path: "/api/sessions", bearer: s, host: fmt.Sprintf("evil.example:%d", guiTestPort)}, 421},
		{"dev server origin", guiReq{path: "/api/sessions", bearer: s, hdr: map[string]string{"Origin": "http://127.0.0.1:3005"}}, 403},
		{"localhost origin", guiReq{path: "/api/sessions", bearer: s, hdr: map[string]string{"Origin": fmt.Sprintf("http://localhost:%d", guiTestPort)}}, 403},
		{"null origin", guiReq{path: "/api/sessions", bearer: s, hdr: map[string]string{"Origin": "null"}}, 403},
		{"cross-site fetch", guiReq{path: "/api/sessions", bearer: s, hdr: map[string]string{"Sec-Fetch-Site": "cross-site"}}, 403},
		{"same-site fetch", guiReq{path: "/api/sessions", bearer: s, hdr: map[string]string{"Sec-Fetch-Site": "same-site"}}, 403},
		{"preflight", guiReq{method: "OPTIONS", path: "/api/sessions", hdr: map[string]string{"Origin": "http://127.0.0.1:3005", "Access-Control-Request-Headers": "authorization"}}, 403},
		{"PUT", guiReq{method: "PUT", path: "/api/sessions", bearer: s}, 403},
		{"form post", guiReq{method: "POST", path: "/api/sessions/main/send", bearer: s, noJSON: true, hdr: map[string]string{"Content-Type": "text/plain"}}, 415},
		{"exchange as a form", guiReq{method: "POST", path: "/api/gui/session", noJSON: true, hdr: map[string]string{"Content-Type": "application/x-www-form-urlencoded"}}, 415},
		{"cross-site exchange", guiReq{method: "POST", path: "/api/gui/session", body: map[string]any{"token": "x"}, hdr: map[string]string{"Sec-Fetch-Site": "cross-site"}}, 403},
		{"post outside the api", guiReq{method: "POST", path: "/", body: map[string]any{}}, 403},
	} {
		w := doGUI(t, h, c.q)
		if w.Code != c.code {
			t.Errorf("%s: %d, want %d (%s)", c.name, w.Code, c.code, strings.TrimSpace(w.Body.String()))
		}
		hd := w.Header()
		if hd.Get("Content-Security-Policy") != webCSP || hd.Get("X-Frame-Options") != "DENY" || hd.Get("X-Content-Type-Options") != "nosniff" || hd.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s: headers %v", c.name, hd)
		}
		for k := range hd {
			if strings.HasPrefix(k, "Access-Control-") {
				t.Errorf("%s: CORS header %s", c.name, k)
			}
		}
		if strings.HasPrefix(c.q.path, "/api/") && hd.Get("Cache-Control") != "no-store" && w.Code != 421 && w.Code != 403 {
			t.Errorf("%s: api without no-store", c.name)
		}
	}
}

// When the port cannot be bound, gui.open fails and writes no page: the
// CLI opens no browser at whatever holds the port.
func TestGUIBindFailureOpensNothing(t *testing.T) {
	d, _, _ := setup(t)
	d.Global.GUI.Port = 47778
	d.gui.listen = func(network, addr string) (net.Listener, error) {
		return nil, errors.New("bind: address already in use")
	}
	_, err := d.Call("gui.open", nil)
	if err == nil || !strings.Contains(err.Error(), "lsof -nP -iTCP:47778") || !strings.Contains(err.Error(), "not opened") {
		t.Fatalf("err = %v", err)
	}
	if files, _ := os.ReadDir(paths.GUIDir()); len(files) != 0 {
		t.Fatalf("a page was written: %v", files)
	}
	if st := call[guiStatus](t, d, "gui.status", nil); st.Listening {
		t.Fatalf("listening: %+v", st)
	}
}

// The same with a real port someone else holds.
func TestGUIBindFailureRealPort(t *testing.T) {
	testenv.NeedTCP(t)
	d, _, _ := setup(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	d.Global.GUI.Port = ln.Addr().(*net.TCPAddr).Port
	if _, err := d.Call("gui.open", nil); err == nil || !strings.Contains(err.Error(), "cannot listen") {
		t.Fatalf("err = %v", err)
	}
	if files, _ := os.ReadDir(paths.GUIDir()); len(files) != 0 {
		t.Fatalf("a page was written: %v", files)
	}
}

// A real listener on port 0 and a real request through it.
func TestGUIRealListener(t *testing.T) {
	testenv.NeedTCP(t)
	d, _, _ := setup(t)
	t.Cleanup(func() { d.stopGUI() })
	res := call[guiOpenResult](t, d, "gui.open", nil)
	if res.Port == 0 {
		t.Fatal("no port")
	}
	r, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/sessions", res.Port))
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no bearer: %d", r.StatusCode)
	}
	again := call[guiOpenResult](t, d, "gui.open", nil)
	if again.Port != res.Port {
		t.Fatalf("a second gui.open moved the port: %d → %d", res.Port, again.Port)
	}
}

// [gui] enable = false: gui.open refuses and nothing listens.
func TestGUIOff(t *testing.T) {
	d, _, _ := setup(t)
	off := false
	d.Global.GUI.Enable = &off
	listened := false
	d.gui.listen = func(network, addr string) (net.Listener, error) {
		listened = true
		return nil, errors.New("no")
	}
	if _, err := d.Call("gui.open", nil); err == nil || !strings.Contains(err.Error(), "enable = false") {
		t.Fatalf("err = %v", err)
	}
	if listened {
		t.Fatal("tried to listen")
	}
}

// gui.* are a person's: an agent (no human token) is refused.
func TestGUIMethodsNeedHumanToken(t *testing.T) {
	for _, m := range []string{"gui.open", "gui.status", "gui.revoke", "gui.stop"} {
		if _, ok := humanMethods[m]; !ok {
			t.Errorf("%s is not human-only", m)
		}
		if _, ok := agentMethods[m]; ok {
			t.Errorf("%s is open to agents", m)
		}
	}
}

// On the GUI, tapi takes a send without allow_devices and without the
// confirm sheet (as in the TUI), recorded as via gui; the phone's rules
// stay as they were.
func TestGUITapiSend(t *testing.T) {
	d, fe, _, h, _ := guiSetup(t)
	call[Tapi](t, d, "tapi.open", nil)
	s := guiSignIn(t, d, h)
	var v webTapi
	json.Unmarshal(doGUI(t, h, guiReq{path: "/api/tapi", bearer: s}).Body.Bytes(), &v)
	if !v.Sendable || v.ConfirmSend {
		t.Fatalf("tapi on the GUI: %+v", v)
	}
	w := doGUI(t, h, guiReq{method: "POST", path: "/api/tapi/send", bearer: s, body: map[string]any{"text": "feat-csv を作って"}})
	if w.Code != 200 {
		t.Fatalf("send: %d %s", w.Code, w.Body)
	}
	if !strings.HasSuffix(fe.last(), "text:feat-csv を作って\nkeys:Enter") {
		t.Fatalf("typed: %s", fe.last())
	}
	rs := tapiSends(t, d)
	if len(rs) != 1 || rs[0].Via != viaGUI || rs[0].From != callerHuman {
		t.Fatalf("send log: %+v", rs)
	}
	// the phone, without allow_devices, is still refused
	wh := testWebHandler(d)
	if w := doWeb(t, wh, tapiSendReq(ipPhone, "x")); w.Code != http.StatusForbidden {
		t.Fatalf("phone: %d", w.Code)
	}
	var pv webTapi
	json.Unmarshal(doWeb(t, wh, webReq{path: "/api/tapi", from: ipPhone}).Body.Bytes(), &pv)
	if pv.Sendable || !pv.ConfirmSend {
		t.Fatalf("tapi on the phone: %+v", pv)
	}
}

// allow / deny through the GUI, and always (docs/gui.md 「決まったこと」5:
// the TUI's A). Only the word "always": the key aliases session.answer
// also takes (a, A, y, n…) never come over HTTP. The phone's front door
// still refuses always (TestWebNoAlways, TestGUIAlwaysOnlyOnGUI).
func TestGUIAnswer(t *testing.T) {
	d, fe, repo, h, _ := guiSetup(t)
	tool := waitingSession(t, d, repo)
	s := guiSignIn(t, d, h)
	before := sentCount(fe)
	for _, a := range []string{"a", "A", "y", "yes", "n", "", "2", "Always", "always "} {
		if w := doGUI(t, h, guiReq{method: "POST", path: "/api/sessions/main/answer", bearer: s, body: map[string]any{"answer": a, "tool": tool}}); w.Code != http.StatusBadRequest {
			t.Errorf("answer %q: %d, want 400", a, w.Code)
		}
	}
	if n := sentCount(fe); n != before {
		t.Fatalf("typed into the agent: %s", fe.last())
	}
	if w := doGUI(t, h, guiReq{method: "POST", path: "/api/sessions/main/answer", body: map[string]any{"answer": "always", "tool": tool}}); w.Code != http.StatusUnauthorized {
		t.Fatalf("without a bearer: %d", w.Code)
	}
	if got := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"}); got.State != state.Waiting {
		t.Fatalf("answered without a bearer: %+v", got.State)
	}
	// always on another prompt than the one shown: refused (409), nothing typed
	other := state.Tool{Name: "Bash", Input: "rm -rf /"}
	if w := doGUI(t, h, guiReq{method: "POST", path: "/api/sessions/main/answer", bearer: s, body: map[string]any{"answer": "always", "tool": other}}); w.Code != http.StatusConflict {
		t.Fatalf("always on a prompt not shown: %d %s", w.Code, w.Body)
	}
	if n := sentCount(fe); n != before {
		t.Fatalf("typed into the agent: %s", fe.last())
	}
	if w := doGUI(t, h, guiReq{method: "POST", path: "/api/sessions/main/answer", bearer: s, body: map[string]any{"answer": "always", "tool": tool}}); w.Code != 200 {
		t.Fatalf("always: %d %s", w.Code, w.Body)
	}
	// [agents.claude] always = "2" ("Yes, and don't ask again")
	if !strings.HasSuffix(fe.last(), "lit:2") {
		t.Fatalf("typed: %s", fe.last())
	}
	if got := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"}); got.State != state.Running {
		t.Fatalf("after always: %s", got.State)
	}

	event(t, d, "PreToolUse", "main", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "pnpm build"}})
	event(t, d, "Notification", "main", map[string]any{"notification_type": "permission_prompt", "message": "needs permission"})
	tool = *call[state.Session](t, d, "session.get", map[string]any{"slug": "main"}).PendingTool
	if w := doGUI(t, h, guiReq{method: "POST", path: "/api/sessions/main/answer", bearer: s, body: map[string]any{"answer": "allow", "tool": tool}}); w.Code != 200 {
		t.Fatalf("allow: %d %s", w.Code, w.Body)
	}
	if !strings.HasSuffix(fe.last(), "lit:1") {
		t.Fatalf("typed: %s", fe.last())
	}
}

// The same handlers on the two front doors: always goes through the local
// GUI's and is a 400 on the phone's, for the same waiting session.
func TestGUIAlwaysOnlyOnGUI(t *testing.T) {
	d, fe, repo, gh, _ := guiSetup(t)
	tool := waitingSession(t, d, repo)
	wh := testWebHandler(d)
	before := sentCount(fe)
	if w := doWeb(t, wh, answerReq(ipPhone, "always", &tool)); w.Code != http.StatusBadRequest {
		t.Fatalf("always on the phone: %d", w.Code)
	}
	if n := sentCount(fe); n != before {
		t.Fatalf("the phone's always typed: %s", fe.last())
	}
	s := guiSignIn(t, d, gh)
	if w := doGUI(t, gh, guiReq{method: "POST", path: "/api/sessions/main/answer", bearer: s, body: map[string]any{"answer": "always", "tool": tool}}); w.Code != 200 {
		t.Fatalf("always on the GUI: %d %s", w.Code, w.Body)
	}
	if !strings.HasSuffix(fe.last(), "lit:2") {
		t.Fatalf("typed: %s", fe.last())
	}
}

// / is the desktop page on the local GUI and the phone's page on the
// tailnet; the modules both load are served to both (as JavaScript, for
// <script type="module">).
func TestGUIDesktopPage(t *testing.T) {
	d, _, _, h, _ := guiSetup(t)
	w := doGUI(t, h, guiReq{path: "/"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `src="/gui/gui.js"`) || !strings.Contains(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("GUI /: %d %s", w.Code, w.Body)
	}
	if csp := w.Header().Get("Content-Security-Policy"); csp != webCSP {
		t.Fatalf("csp %q", csp)
	}
	for _, p := range []string{"/gui/gui.js", "/gui/gui.css", "/lib/dom.js", "/lib/api.js", "/lib/sheets.js", "/app.css"} {
		w := doGUI(t, h, guiReq{path: p})
		want := "javascript"
		if strings.HasSuffix(p, ".css") {
			want = "text/css"
		}
		if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), want) {
			t.Errorf("%s: %d %s", p, w.Code, w.Header().Get("Content-Type"))
		}
	}
	ph := testWebHandler(d)
	w = doWeb(t, ph, webReq{path: "/", from: ipPhone})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `src="/app.js"`) || strings.Contains(w.Body.String(), "gui.js") {
		t.Fatalf("phone /: %d %s", w.Code, w.Body)
	}
	for _, p := range []string{"/lib/dom.js", "/lib/sheets.js"} {
		if w := doWeb(t, ph, webReq{path: p, from: ipPhone}); w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "javascript") {
			t.Errorf("phone %s: %d %s", p, w.Code, w.Header().Get("Content-Type"))
		}
	}
}

// A page a previous pekoed left is gone when pekoed starts.
func TestGUIPagesClearedAtStart(t *testing.T) {
	d, _, _ := setup(t)
	old := filepath.Join(paths.GUIDir(), "open-left-over.html")
	if err := os.WriteFile(old, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.DB.Close()
	d.releaseLock()
	d2, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { d2.DB.Close(); d2.releaseLock() }()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("left-over page: %v", err)
	}
}

// The desktop page's look is provisional (docs/gui.md 段階 1): every style
// is in gui/gui.css, so the final design can replace it and keep the DOM.
// No style attribute in the HTML, no style set from gui.js, no <style>.
func TestGUIStylesOnlyInCSS(t *testing.T) {
	html, _ := webFiles.ReadFile("webui/gui/index.html")
	if m := regexp.MustCompile(`(?i)\sstyle\s*=|<style`).FindAllString(string(html), -1); len(m) > 0 {
		t.Errorf("gui/index.html: %v", m)
	}
	js, _ := webFiles.ReadFile("webui/gui/gui.js")
	if m := regexp.MustCompile(`\.style\b|setAttribute\(\s*["']style|cssText`).FindAllString(string(js), -1); len(m) > 0 {
		t.Errorf("gui/gui.js sets styles: %v", m)
	}
}
