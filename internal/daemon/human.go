package daemon

// Human-only methods. An agent runs as the same user as pekoed, so the
// socket's 0600 does not tell a person from an agent: an agent allowed to
// run `pekoe` could answer its own (or another session's) permission
// prompt. The methods below are what a person decides or runs on the spot;
// over the unix socket they need the human token, a random string pekoed
// keeps in $PEKOE_HOME/share/human-token (0600). The TUI, the CLI and the
// Neovim plugin read the file and send it with each request; agents get
// neither the token nor its path in their environment, and the docs show
// how to keep the agent's sandbox from reading the file.
//
// The other human entrances do not go through serve: the web UI (webGuard)
// and the Chrome extension (http-token) call the handlers in process.

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"pekoe/internal/engine"
	"pekoe/internal/paths"
)

// humanOnly: methods refused over the socket without the human token.
var humanOnly = map[string]bool{
	"session.answer": true, // allow / deny / always on a permission prompt
	"session.keys":   true, // raw keys: can pick a menu entry or switch modes
	"tapi.send":      true, // tapi chat's input box (the TUI's chat)
	"tapi.keys":      true,
	"init.write":     true, // repo config: setup / dev commands run outside the agent
	"run.start":      true, // runs a command in the worktree
	"dev.toggle":     true, // runs the repo's dev command
	"daemon.stop":    true, // stops pekoed (and, with kill_agents, every agent)
}

var errHumanOnly = errors.New("only a person can do this: use the pekoe TUI, or run pekoe from your own terminal (agents cannot answer prompts, press keys or run commands through pekoe)")

// loadOrCreateHumanToken keeps one token per data directory, so clients
// that read it once stay valid across daemon restarts.
func loadOrCreateHumanToken() (string, error) {
	f := paths.HumanTokenFile()
	if b, err := os.ReadFile(f); err == nil {
		if t := strings.TrimSpace(string(b)); len(t) >= 32 {
			_ = os.Chmod(f, 0o600)
			return t, nil
		}
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	t := hex.EncodeToString(buf)
	_ = os.Remove(f) // a fresh file, so it is created 0600 (not an old mode, not a link)
	w, err := os.OpenFile(f, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := w.WriteString(t + "\n"); err != nil {
		w.Close()
		return "", err
	}
	return t, w.Close()
}

func (d *Daemon) isHuman(token string) bool {
	return d.humanToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(d.humanToken)) == 1
}

// authorize checks a request that came in over the unix socket.
func (d *Daemon) authorize(req Request) error {
	if d.isHuman(req.Token) {
		return nil
	}
	if humanOnly[req.Method] {
		return fmt.Errorf("%s: %w", req.Method, errHumanOnly)
	}
	if req.Method == "session.send" {
		return d.authorizeSend(req.Params)
	}
	return nil
}

// authorizeSend: sending text stays open to agents (a hub agent hands work
// to its sessions), but only into an agent's input box, and not while it
// shows a permission menu, which would take the text's first character as
// its choice. Other windows (dev, run) are shells.
func (d *Daemon) authorizeSend(raw json.RawMessage) error {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	raw, err := d.normalizeSlugParam(raw)
	if err != nil {
		return err
	}
	var p sendP
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("bad params: %w", err)
	}
	if win(p.Window) != engine.WinAgent {
		return fmt.Errorf("session.send to the %s window: %w", p.Window, errHumanOnly)
	}
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return err
	}
	if webAnswerable(s) {
		return fmt.Errorf("%s is waiting for a permission answer; answer it in the pekoe TUI first, then send", p.Slug)
	}
	return nil
}
