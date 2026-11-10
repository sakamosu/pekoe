package daemon

// The send log (docs/orchestration.md "5. 送信の記録"): the text pekoed
// passes from a caller to a session — session.new's prompt and
// session.send's instructions — with who sent it, so the hub agent can
// read back what it asked for and see what a person told a session
// directly. Keys (session.keys) and the TUI forwarding what the user types
// (session.send without submit) are not recorded: too fine, and they may
// be a password.

import (
	"encoding/json"
	"errors"
	"log"
	"time"
	"unicode/utf8"

	"pekoe/internal/engine"
	"pekoe/internal/paths"
	"pekoe/internal/state"
)

// sendLogMax cuts a recorded text (bytes).
const sendLogMax = 64 << 10

// hNewAs is session.new: the session's parent is its caller, and the
// prompt goes into the send log.
func (d *Daemon) hNewAs(who *Caller, p newP) (any, error) {
	p.parent = who.ID()
	var res any
	err := d.checkRequest(who, p.Request)
	switch {
	case err != nil:
	case who.Human():
		res, err = d.hNew(p)
	default:
		res, err = d.hAgentNew(p)
	}
	if s, ok := res.(*state.Session); ok && s != nil && err == nil && p.Request != 0 {
		// the entry was open a moment ago; if tapi closed it since, the
		// session keeps its request and the entry its status
		_, _ = d.linkLedger("session.new --request", p.Request, s.Slug)
	}
	to := paths.Slug(p.Branch)
	if s, ok := res.(*state.Session); ok && s != nil {
		to = s.Slug
	}
	text := p.Prompt
	if p.From != "" {
		text = "--from " + p.From + "\n" + text
	}
	d.recordSend(who, to, "new", text, err)
	return res, err
}

// hSendAs is session.send, recorded when it is an instruction to an agent:
// submitted, or sent without the human token.
func (d *Daemon) hSendAs(who *Caller, p sendP) (any, error) {
	if who.Human() && win(p.Window) == engine.WinAgent {
		d.noteTyped(p.Slug) // a person's text: no inbox delivery in the middle of it
	}
	res, err := d.send(p, !who.Human())
	if sendLogged(who, p) {
		d.recordSend(who, p.Slug, "send", p.Text, err)
	}
	return res, err
}

func sendLogged(who *Caller, p sendP) bool {
	return win(p.Window) == engine.WinAgent && (p.Submit || !who.Human())
}

// recordRefusedSend logs a session.send that authorize turned down (a
// permission prompt was up, the text started with a digit).
func (d *Daemon) recordRefusedSend(who *Caller, raw json.RawMessage, why error) {
	var p sendP
	if len(raw) == 0 || json.Unmarshal(raw, &p) != nil || !sendLogged(who, p) {
		return
	}
	if r, err := d.normalizeSlugParam(raw); err == nil {
		_ = json.Unmarshal(r, &p)
	}
	d.recordSend(who, p.Slug, "send", p.Text, why)
}

func (d *Daemon) recordSend(who *Caller, to, kind, text string, err error) {
	if len(text) > sendLogMax {
		text = text[:sendLogMax]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	via := who.Via
	if via == "" {
		via = viaSocket
	}
	r := &state.SendRecord{At: time.Now(), From: who.ID(), To: to, Kind: kind, Text: text, Via: via, OK: err == nil}
	if err != nil {
		r.Error = err.Error()
	}
	if e := d.DB.AddSend(r); e != nil {
		log.Printf("send log: %v", e)
	}
}

type sendLogP struct {
	To    string `json:"to"`
	From  string `json:"from"`
	Since int64  `json:"since"`
	Limit int    `json:"limit"`
}

// hSendLog lists the send log, newest first. Without the human token only
// the records the caller sent or received: the others are other sessions'
// instructions.
func (d *Daemon) hSendLog(who *Caller, p sendLogP) (any, error) {
	f := state.SendFilter{From: p.From, To: p.To, Since: p.Since, Limit: clampLimit(p.Limit, 100, 1000)}
	if !who.Human() {
		id := who.ID()
		if id == callerUnknown {
			return nil, errUnknownCaller("send.log")
		}
		f.Party = id
	}
	return d.DB.Sends(f)
}

// clampLimit is n within 1..max, def when 0 or less.
func clampLimit(n, def, max int) int {
	if n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// errUnknownCaller refuses a method that is only for the caller's own
// session to a process pekoed cannot place in a pekoe window.
func errUnknownCaller(method string) error {
	return errors.New(method + ": pekoe cannot tell which session is calling (run it from the session's own agent, inside its pekoe window)")
}
