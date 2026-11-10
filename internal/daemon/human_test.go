package daemon

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"

	"pekoe/internal/paths"
)

// sockClient talks to d.serve over an in-memory pipe, like a client of the
// unix socket.
type sockClient struct {
	t  *testing.T
	nc net.Conn
	sc *bufio.Scanner
	id int
}

func dialPipe(t *testing.T, d *Daemon) *sockClient {
	t.Helper()
	a, b := net.Pipe()
	go d.serve(b)
	t.Cleanup(func() { a.Close() })
	return &sockClient{t: t, nc: a, sc: bufio.NewScanner(a)}
}

// call sends one request (token may be "") and returns its error string.
func (c *sockClient) call(method string, params any, token string) string {
	c.t.Helper()
	c.id++
	req := map[string]any{"id": c.id, "method": method, "params": params}
	if token != "" {
		req["token"] = token
	}
	b, _ := json.Marshal(req)
	if _, err := c.nc.Write(append(b, '\n')); err != nil {
		c.t.Fatal(err)
	}
	for c.sc.Scan() {
		var r struct {
			ID    int    `json:"id"`
			Error string `json:"error"`
		}
		if json.Unmarshal(c.sc.Bytes(), &r) == nil && r.ID == c.id {
			return r.Error
		}
	}
	c.t.Fatalf("%s: connection closed", method)
	return ""
}

// sentSince returns what reached the engine after the first n records.
func sentSince(fe *fakeEngine, n int) []string {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	return append([]string{}, fe.sent[n:]...)
}

func TestHumanTokenFile(t *testing.T) {
	d, _, _ := setup(t)
	fi, err := os.Stat(paths.HumanTokenFile())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("human-token mode = %v, want 0600", fi.Mode().Perm())
	}
	if len(d.humanToken) < 32 {
		t.Fatalf("token too short: %q", d.humanToken)
	}
	// kept across restarts: a running TUI stays valid
	tok, err := loadOrCreateHumanToken()
	if err != nil || tok != d.humanToken {
		t.Fatalf("reload = %q, %v; want %q", tok, err, d.humanToken)
	}
}

// The human-only methods are refused over the socket without the token,
// before they reach the engine, and work with it.
func TestHumanOnlyMethodsNeedToken(t *testing.T) {
	d, fe, repo := setup(t)
	waitingSession(t, d, repo)
	c := dialPipe(t, d)
	before := sentCount(fe)
	for _, tc := range []struct {
		method string
		params map[string]any
	}{
		{"session.answer", map[string]any{"slug": "main", "answer": "always"}},
		{"session.keys", map[string]any{"slug": "main", "keys": []string{"2"}}},
		{"tapi.send", map[string]any{"text": "hi", "submit": true}},
		{"tapi.keys", map[string]any{"keys": []string{"Enter"}}},
		{"init.write", map[string]any{"path": repo, "toml": "[hooks]\nsetup = \"true\"\n"}},
		{"run.start", map[string]any{"slug": "main", "command": "true"}},
		{"dev.toggle", map[string]any{"slug": "main"}},
		{"daemon.stop", map[string]any{"kill_agents": true}},
		// added after the deny list and missed by it: run nvim, read
		// excerpts, return the login URL or the extension's token
		{"review.surface", map[string]any{"slug": "main"}},
		{"error.get", map[string]any{"id": 1}},
		{"error.send", map[string]any{"id": 1}},
		{"web.status", nil},
		{"ext.info", nil},
		{"session.screen", map[string]any{"slug": "main"}},
		{"session.open", map[string]any{"path": repo, "resume": "new"}},
		{"daemon.status", nil},
	} {
		for _, tok := range []string{"", "wrong-" + d.humanToken} {
			if e := c.call(tc.method, tc.params, tok); !strings.Contains(e, "only a person can do this") {
				t.Errorf("%s with token %q: error = %q, want the human-only refusal", tc.method, tok, e)
			}
		}
	}
	if got := sentSince(fe, before); len(got) != 0 {
		t.Fatalf("refused calls reached the engine: %v", got)
	}
	id := call[map[string]any](t, d, "init.detect", map[string]any{"path": repo})["repo_id"].(string)
	if _, err := os.Stat(paths.RepoConfig(id)); err == nil {
		t.Fatal("init.write wrote the repo config without the token")
	}
	if d.killAgents.Load() {
		t.Fatal("daemon.stop ran without the token")
	}

	if e := c.call("session.answer", map[string]any{"slug": "main", "answer": "allow"}, d.humanToken); e != "" {
		t.Fatalf("session.answer with the token: %s", e)
	}
	if !strings.Contains(fe.last(), "lit:1") {
		t.Fatalf("allow not typed: %s", fe.last())
	}
}

// What a hub agent uses keeps working without the token; session.send is
// limited to the agent's input box and refused on a permission prompt.
func TestAgentMethodsWithoutToken(t *testing.T) {
	d, fe, repo := setup(t)
	waitingSession(t, d, repo)
	c := dialPipe(t, d)
	for _, m := range []string{"session.list", "repo.resolve"} {
		params := map[string]any{}
		if m == "repo.resolve" {
			params = map[string]any{"repo": repo, "cwd": repo}
		}
		if e := c.call(m, params, ""); e != "" {
			t.Errorf("%s: %s", m, e)
		}
	}
	if e := c.call("session.get", map[string]any{"slug": "main"}, ""); e != "" {
		t.Errorf("session.get: %s", e)
	}
	if e := c.call("no.such", nil, ""); !strings.Contains(e, "unknown method") {
		t.Errorf("unknown method: error = %q", e)
	}
	before := sentCount(fe)
	if e := c.call("session.send", map[string]any{"slug": "main", "text": "2", "submit": true}, ""); !strings.Contains(e, "waiting for a permission answer") {
		t.Fatalf("send on a permission prompt: error = %q", e)
	}
	if e := c.call("session.send", map[string]any{"slug": "app/main", "text": "2"}, ""); !strings.Contains(e, "waiting for a permission answer") {
		t.Fatalf("send by repo/slug on a permission prompt: error = %q", e)
	}
	if got := sentSince(fe, before); len(got) != 0 {
		t.Fatalf("refused send reached the engine: %v", got)
	}
	// a person may type there (the TUI's own input goes through keys, but
	// send is theirs too)
	if e := c.call("session.send", map[string]any{"slug": "main", "text": "2"}, d.humanToken); e != "" {
		t.Fatalf("send with the token: %s", e)
	}

	// once the prompt is answered, the hub can hand over more work
	call[any](t, d, "session.answer", map[string]any{"slug": "main", "answer": "deny"})
	if e := c.call("session.send", map[string]any{"slug": "main", "text": "next, add tests", "submit": true}, ""); e != "" {
		t.Fatalf("send to an idle agent: %s", e)
	}
	if !strings.Contains(strings.Join(sentSince(fe, before), "\n"), "next, add tests") {
		t.Fatalf("text not sent: %v", sentSince(fe, before))
	}
	// other windows are shells
	for _, w := range []string{"dev", "run"} {
		if e := c.call("session.send", map[string]any{"slug": "main", "window": w, "text": "echo hi"}, ""); !strings.Contains(e, "only a person can do this") {
			t.Errorf("send to the %s window: error = %q", w, e)
		}
	}
	if e := c.call("session.send", map[string]any{"slug": "nope", "text": "x"}, ""); !strings.Contains(e, "nope") {
		t.Errorf("send to an unknown slug: error = %q", e)
	}

	// a dialog on the screen that no hook has reported yet holds the send
	// too, with or without submit; with the token only a submit is held
	fe.mu.Lock()
	fe.lines = []string{"Do you want to proceed?", "❯ 1. Yes", "  2. No"}
	fe.mu.Unlock()
	before = sentCount(fe)
	for _, submit := range []bool{true, false} {
		e := c.call("session.send", map[string]any{"slug": "main", "text": "2", "submit": submit}, "")
		if !strings.Contains(e, "main is waiting for a permission answer: answer it in the pekoe TUI first") {
			t.Fatalf("send (submit %v) over an on-screen prompt: error = %q", submit, e)
		}
	}
	if e := c.call("session.send", map[string]any{"slug": "main", "text": "go", "submit": true}, d.humanToken); !strings.Contains(e, "main is waiting for a permission answer: answer it first") {
		t.Fatalf("submit with the token over an on-screen prompt: error = %q", e)
	}
	if got := sentSince(fe, before); len(got) != 0 {
		t.Fatalf("held sends reached the engine: %v", got)
	}
}

// Every registered method is either open to agents or human-only, on
// purpose: a new method fails here until someone puts it in one of the two
// lists (and authorize refuses it to agents meanwhile).
func TestEveryMethodClassified(t *testing.T) {
	d, _, _ := setup(t)
	for m := range d.handlers {
		_, a := agentMethods[m]
		_, h := humanMethods[m]
		switch {
		case a && h:
			t.Errorf("%s is in both agentMethods and humanMethods", m)
		case !a && !h:
			t.Errorf("%s is in neither agentMethods nor humanMethods (human.go): decide whether an agent may call it without the human token", m)
		}
	}
	for _, list := range []map[string]string{agentMethods, humanMethods} {
		for m := range list {
			if _, ok := d.handlers[m]; !ok {
				t.Errorf("%s is classified but not registered", m)
			}
		}
	}
}

// A subscription without the token gets only the session events: not
// other sessions' clipboard (term.passthrough), picks, errors or review
// text.
func TestSubscribeWithoutTokenGetsSessionEvents(t *testing.T) {
	d, _, _ := setup(t)
	conns := func() map[*conn]bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		out := map[*conn]bool{}
		for c := range d.conns {
			out[c] = true
		}
		return out
	}
	newConn := func(tok string) *conn {
		before := conns()
		c := dialPipe(t, d)
		if e := c.call("subscribe", map[string]any{"events": []string{"*"}}, tok); e != "" {
			t.Fatal(e)
		}
		for k := range conns() {
			if !before[k] {
				return k
			}
		}
		t.Fatal("no connection")
		return nil
	}
	agent := newConn("")
	for _, ev := range []string{"term.passthrough", "pick.received", "error.updated", "review.answer", "editor.open", "tapi.output"} {
		if agent.wants(ev) {
			t.Errorf("agent subscription receives %s", ev)
		}
	}
	for _, ev := range []string{"session.updated", "session.progress"} {
		if !agent.wants(ev) {
			t.Errorf("agent subscription misses %s", ev)
		}
	}
	human := newConn(d.humanToken)
	if !human.wants("term.passthrough") || !human.wants("editor.open") {
		t.Error("human subscription misses events")
	}
}
