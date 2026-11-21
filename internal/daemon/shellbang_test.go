package daemon

import (
	"net"
	"os"
	"strings"
	"testing"

	"pekoe/internal/state"
)

// Without the human token, text that starts with '!' (past leading white
// space) is refused: in Claude Code's empty input box it switches to the
// shell mode and runs the rest as a command. A person's send keeps it.
func TestAgentSendRefusesShellBang(t *testing.T) {
	d, fe, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	fe.panes["+tapi~agent"] = os.Getpid()
	pid := childOfTest(t)
	d.peerPID = func(net.Conn) int { return pid }
	c := dialPipe(t, d)
	before := sentCount(fe)
	refused := []string{"!rm -rf ~", "  !ls", "\n!ls", "\t !ls", "　!ls", "\x1b!ls", "!"}
	for _, text := range refused {
		for _, submit := range []bool{false, true} {
			if e := c.call("session.send", map[string]any{"slug": "main", "text": text, "submit": submit}, ""); !strings.Contains(e, "may not start with '!'") {
				t.Errorf("send %q (submit %v): error = %q", text, submit, e)
			}
		}
	}
	if got := sentSince(fe, before); len(got) != 0 {
		t.Fatalf("a send starting with '!' reached the agent: %v", got)
	}
	// logged as not sent, from the caller
	rs := call[[]state.SendRecord](t, d, "send.log", map[string]any{"limit": 1})
	if len(rs) != 1 || rs[0].From != TapiSlug || rs[0].To != "main" || rs[0].OK || rs[0].Text != "!" || !strings.Contains(rs[0].Error, "'!'") {
		t.Fatalf("refused send: %+v", rs)
	}

	// '!' later in the text is fine
	for _, text := range []string{"run !ls later", "a\n!ls", "！ls"} {
		if e := c.call("session.send", map[string]any{"slug": "main", "text": text, "submit": true}, ""); e != "" {
			t.Errorf("send %q: %s", text, e)
		}
	}
	// a person over the socket (TUI, CLI, Neovim) keeps the shell mode
	n := sentCount(fe)
	if e := c.call("session.send", map[string]any{"slug": "main", "text": "!git status", "submit": true}, d.humanToken); e != "" {
		t.Fatalf("send with the token: %s", e)
	}
	if got := sentSince(fe, n); len(got) == 0 || got[0] != "text:!git status" {
		t.Fatalf("typed with the token: %v", got)
	}
}

// The Web UI and the local GUI are a person's: '!' goes through.
func TestWebAndGUISendKeepShellBang(t *testing.T) {
	t.Run("web", func(t *testing.T) {
		d, fe, repo, h := webSetup(t)
		call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
		w := doWeb(t, h, webReq{method: "POST", path: "/api/sessions/main/send", from: ipPhone, body: map[string]any{"text": "!git status"}})
		if w.Code != 200 {
			t.Fatalf("send: %d %s", w.Code, w.Body)
		}
		if !strings.HasSuffix(fe.last(), "text:!git status\nkeys:Enter") {
			t.Fatalf("typed: %s", fe.last())
		}
	})
	t.Run("gui", func(t *testing.T) {
		d, fe, repo, h, _ := guiSetup(t)
		call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
		s := guiSignIn(t, d, h)
		w := doGUI(t, h, guiReq{method: "POST", path: "/api/sessions/main/send", bearer: s, body: map[string]any{"text": "!git status"}})
		if w.Code != 200 {
			t.Fatalf("send: %d %s", w.Code, w.Body)
		}
		if !strings.HasSuffix(fe.last(), "text:!git status\nkeys:Enter") {
			t.Fatalf("typed: %s", fe.last())
		}
	})
}

// session.new without the token: a prompt starting with '!' is refused
// (an agent that takes its prompt by paste would get it in its input box),
// logged, and nothing is made. A person may.
func TestAgentNewRefusesShellBangPrompt(t *testing.T) {
	d, _, repo := setup(t)
	c := dialPipe(t, d)
	if e := c.call("session.new", map[string]any{"repo": repo, "branch": "feat/a"}, d.humanToken); e != "" {
		t.Fatalf("first session: %s", e)
	}
	for _, prompt := range []string{"!touch /tmp/x", "  !ls"} {
		if e := c.call("session.new", map[string]any{"repo": repo, "branch": "feat/b", "prompt": prompt}, ""); !strings.Contains(e, "may not start with '!'") {
			t.Fatalf("prompt %q: error = %q", prompt, e)
		}
	}
	if _, err := d.get("feat-b"); err == nil {
		t.Fatal("session made from a refused prompt")
	}
	rs := call[[]state.SendRecord](t, d, "send.log", map[string]any{"limit": 1})
	if len(rs) != 1 || rs[0].Kind != "new" || rs[0].OK {
		t.Fatalf("refused new: %+v", rs)
	}
	if e := c.call("session.new", map[string]any{"repo": repo, "branch": "feat/c", "prompt": "fix it, then !ls"}, ""); e != "" {
		t.Fatalf("'!' later in the prompt: %s", e)
	}
	if e := c.call("session.new", map[string]any{"repo": repo, "branch": "feat/d", "prompt": "!ls"}, d.humanToken); e != "" {
		t.Fatalf("with the token: %s", e)
	}
}
