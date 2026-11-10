package daemon

// Human-only methods. An agent runs as the same user as pekoed, so the
// socket's 0600 does not tell a person from an agent: an agent allowed to
// run `pekoe` could answer its own (or another session's) permission
// prompt, or have pekoed (which runs outside the agent's sandbox) run
// commands or read files for it. Over the unix socket every method needs
// the human token, a random string pekoed keeps in
// $PEKOE_HOME/share/human-token (0600), except the few in agentMethods
// that agents are meant to call. The TUI, the CLI and the Neovim plugin
// read the file and send it with each request; agents get neither the
// token nor its path in their environment, and the docs show how to keep
// the agent's sandbox from reading the file.
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

// agentMethods: what works over the socket without the human token. This
// is an allow list: a method that is not here needs the token, so a new
// method is human-only until someone decides an agent needs it (and checks
// it neither runs anything nor reads anything outside the agent's reach).
// TestEveryMethodClassified keeps this and humanMethods in step with the
// registered handlers.
var agentMethods = map[string]string{
	"session.event": "the agents' hooks (`pekoe event`)",
	"session.list":  "`pekoe ls`, the MCP server's session_list",
	"session.get":   "one entry of session.list",
	"session.new":   "the hub agent hands work to a new session (`pekoe new`); hAgentNew limits it to known repositories",
	"session.send":  "the hub agent's follow-up instructions (`pekoe send`); authorizeSend limits it",
	"repo.resolve":  "`pekoe ls` / `pekoe new --repo` find the repository",
	"editor.open":   "the MCP server's open_in_editor (only an event for nvim)",
}

// humanMethods: every other registered method, with why an agent may not
// call it. Only the classification test reads it; authorize refuses
// whatever is not in agentMethods.
var humanMethods = map[string]string{
	// a person's decision or keystrokes
	"session.answer":        "allow / deny / always on a permission prompt",
	"session.keys":          "raw keys: can pick a menu entry or switch modes",
	"tapi.send":             "tapi chat's input box",
	"tapi.keys":             "raw keys into tapi chat",
	"review.approve":        "a person's review decision",
	"review.unapprove":      "a person's review decision",
	"review.reject":         "a person's review decision, sent to the agent",
	"review.comment":        "a person's review comment, sent to the agent",
	"review.comment.delete": "a person's review comment",
	"review.ask":            "a person's question, sent to the agent",
	"review.flush":          "sends the person's review to the agent",
	"pick.send":             "sends a Chrome pick to an agent",
	"pick.discard":          "a person's choice",
	"error.send":            "sends an error (with a source excerpt) to an agent",
	"error.ignore":          "a person's choice",
	"browser.send_errors":   "sends browser errors to an agent",
	// runs something outside the agent's sandbox
	"init.write":      "repo config: setup / dev commands run outside the agent",
	"run.start":       "runs a command in the worktree",
	"dev.toggle":      "runs the repo's dev command",
	"review.surface":  "starts nvim in the session's review window",
	"review.pr":       "gh: reads and writes the PR with the person's credentials",
	"land":            "push and PR creation",
	"session.open":    "starts an agent in any directory",
	"session.adopt":   "takes over an existing worktree",
	"session.restart": "restarts an agent",
	"session.undo":    "rewrites the worktree",
	"session.kill":    "stops an agent",
	"session.cancel":  "removes a session being created",
	"worktree.rm":     "removes a worktree",
	"window.kill":     "stops a window",
	"run.close":       "stops the run window",
	"preview.capture": "drives Chrome on a URL",
	"preview.watch":   "makes the preview retake screenshots",
	"issues.list":     "gh: reads issues with the person's credentials",
	"issues.view":     "gh: reads an issue with the person's credentials",
	"pr.view":         "gh: reads the PR with the person's credentials",
	"pr.failed_log":   "gh: reads CI logs with the person's credentials",
	"tapi.open":       "starts tapi chat",
	"tapi.restart":    "restarts tapi chat",
	"tapi.reset":      "clears tapi chat",
	"daemon.stop":     "stops pekoed (and, with kill_agents, every agent)",
	// secrets, or what other sessions show or read
	"ext.info":       "returns the HTTP token of the Chrome extension",
	"web.status":     "returns the Web UI's login URL",
	"session.screen": "another session's screen",
	"tapi.screen":    "tapi chat's screen",
	"tapi.get":       "tapi chat's state and screen",
	"error.get":      "a source excerpt of the worktree",
	"error.list":     "errors of every session (page text, file paths)",
	"pick.list":      "what the person picked in Chrome",
	"browser.tabs":   "the person's open Chrome tabs",
	"preview.get":    "screenshots of the dev server",
	"review.files":   "the diff under review, with the person's comments",
	"review.risks":   "review notes",
	"changes":        "the diff of a session",
	"conflicts.list": "changes across sessions",
	"init.detect":    "reads any repository's files",
	"repo.list":      "every repository pekoe knows",
	"worktree.list":  "every worktree",
	// the TUI's bookkeeping: no use to an agent
	"daemon.status":    "pid, port and socket (the TUI and `pekoe daemon`)",
	"session.resize":   "the TUI's pane size",
	"session.seen":     "the TUI's unread marks",
	"session.progress": "setup progress of the TUI's new-session view",
	"attention.next":   "the TUI's jump to the next waiting session",
	"review.next":      "the TUI's jump to the next review",
	"review.start":     "the TUI enters review",
	"review.end":       "the TUI leaves review",
	"pr.seen":          "the TUI's unread marks",
	"term.caps":        "the TUI's terminal capabilities",
	"tapi.resize":      "the TUI's pane size",
}

// agentEvents: the events a connection without the human token receives
// (`pekoe new` follows session.progress and session.updated). Others carry
// other sessions' clipboard (term.passthrough), picks, errors or review
// text.
var agentEvents = []string{
	"session.created", "session.updated", "session.removed", "session.progress",
	"session.waiting", "session.done", "session.output",
}

func agentEvent(ev string) bool {
	for _, e := range agentEvents {
		if ev == e {
			return true
		}
	}
	return false
}

// ErrHumanOnly refuses a human-only operation to an agent (also used by
// `pekoe update`, which refuses before it reaches pekoed).
var ErrHumanOnly = errors.New("only a person can do this: use the pekoe TUI, or run pekoe from your own terminal (agents cannot answer prompts, press keys or run commands through pekoe)")

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
	if _, ok := agentMethods[req.Method]; !ok {
		if _, known := d.handlers[req.Method]; !known {
			return nil // Call answers "unknown method"
		}
		return fmt.Errorf("%s: %w", req.Method, ErrHumanOnly)
	}
	if req.Method == "session.send" {
		return d.authorizeSend(req.Params)
	}
	return nil
}

// hAgentNew is session.new without the human token: only in a repository
// pekoe already knows (repo.list: one with a session, a remembered one, or
// one with a repos/<id>.toml), at the directory pekoe knows it by.
// Registering a new repository is a person's decision: pekoed runs git
// there (worktree add checks files out), and a repository an agent made
// itself could name filter or merge drivers in its own config, which run
// outside any sandbox where none is available. A repository's id comes from
// its origin URL, which an agent's own clone can copy, so the directory has
// to match too; the session is then made from the known directory, not
// from the path the agent passed (which could be a link it retargets).
func (d *Daemon) hAgentNew(p newP) (any, error) {
	if p.Repo == "" {
		return d.hNew(p) // "repo path required"
	}
	repo, err := resolveRepo(p.Repo)
	if err != nil {
		return nil, err
	}
	for _, k := range d.knownRepos() {
		if k.ID == repo.ID && k.Dir != "" && realpath(k.Dir) == realpath(repo.Dir) {
			p.Repo = k.Dir
			return d.hNew(p)
		}
	}
	return nil, fmt.Errorf("session.new in %s, a repository pekoe does not know yet (start its first session yourself, from the TUI or your own terminal): %w", repo.Dir, ErrHumanOnly)
}

// authorizeSend: sending text stays open to agents (a hub agent hands work
// to its sessions), but only into an agent's input box, and not while it
// shows a permission menu, which would take the text's first character as
// its choice: with or without submit, by the hooks' state or the screen
// (promptUp, the test hSend applies to every submit). hAgentSend checks
// the prompt again right before typing; a dialog can still come up in the
// few milliseconds between that check and the keys, so text that starts
// with a digit, which would pick a menu entry right away, is refused too.
// Other windows (dev, run) are shells.
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
		return fmt.Errorf("session.send to the %s window: %w", p.Window, ErrHumanOnly)
	}
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return err
	}
	if d.promptUp(p.Slug, s.State, s.WaitKind) {
		return promptUpErr(p.Slug, errPromptUpTUI)
	}
	if t := strings.TrimLeft(engine.CleanText(p.Text), " \t\n"); t != "" && t[0] >= '0' && t[0] <= '9' {
		return errDigitSend
	}
	return nil
}

// errDigitSend: see authorizeSend.
var errDigitSend = errors.New("session.send without the human token: text may not start with a digit (in a permission menu it would pick a choice); start it with a word")
