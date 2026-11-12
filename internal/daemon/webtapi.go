package daemon

// The web UI's tapi chat (docs/orchestration.md 「Web UI」, docs/web.md
// 「tapi chat の扱い」): its state, its screen, sending it a line and the
// keypad for its questions. tapi reads every repository and opens sessions
// for whatever it is told, so a send to it weighs more than a send to one
// session:
//   - it is refused unless [web] allow_devices narrows the web UI to named
//     devices (a stray device of the same user cannot instruct tapi)
//   - the page shows a confirm sheet first (to: tapi, the whole text)
//   - what was sent, by whom and from which device goes to pekoed's log
//     and the send log (to: +tapi)
// There is no allow/deny for tapi's permission prompts here: those stay at
// the terminal. While one is up, send and keys are refused (409), as for a
// session.

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"pekoe/internal/engine"
	"pekoe/internal/state"
	"pekoe/internal/termstyle"
	"pekoe/internal/theme"
)

// webTapiLogText: how much of a send to tapi goes into pekoed's log (the
// whole text is in the send log).
const webTapiLogText = 200

// webTapi is what the phone sees of tapi chat: its state, how long, its
// last reply and the counts. Not the pending tool (allow/deny for tapi
// stays at the terminal), its directory or the Claude session id.
type webTapi struct {
	Started     bool        `json:"started"`
	Alive       bool        `json:"alive"`
	State       state.State `json:"state"`
	WaitKind    string      `json:"wait_kind,omitempty"`
	StateSince  time.Time   `json:"state_since"`
	LastMessage string      `json:"last_message,omitempty"`
	InboxOpen   int         `json:"inbox_open"`
	LedgerOpen  int         `json:"ledger_open"`
	Look        webLook     `json:"look"`
	// Answerable: a permission prompt is up (answer it at the terminal;
	// send and keys are refused meanwhile).
	Answerable bool `json:"answerable"`
	// Sendable: [web] allow_devices is set, so the web UI may send to tapi.
	Sendable bool `json:"sendable"`
}

func (d *Daemon) webTapiView(sendable bool) webTapi {
	t := d.tapiView()
	return webTapi{
		Started: t.Started, Alive: t.Alive, State: t.State, WaitKind: t.WaitKind,
		StateSince: t.StateSince, LastMessage: t.LastMessage,
		InboxOpen: t.InboxOpen, LedgerOpen: t.LedgerOpen,
		Look:       toWebLook(theme.Look(theme.WebIcons(), t.State, 0, false)),
		Answerable: tapiAnswerable(t.State, t.WaitKind),
		Sendable:   sendable,
	}
}

func (d *Daemon) webTapi(sendable bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, d.webTapiView(sendable))
	}
}

// tapiAnswerable: tapi waits on a permission prompt (not a question).
func tapiAnswerable(st state.State, waitKind string) bool {
	return st == state.Waiting && waitKind != "question"
}

// tapiNow is tapi's state and wait kind as pekoed has it.
func (d *Daemon) tapiNow() (state.State, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.tapi.state, d.tapi.waitKind
}

var errTapiNotStarted = errors.New("tapi chat is not started: open it in the pekoe TUI")

// webTapiReady: tapi's window is there and its agent runs (409 otherwise).
func (d *Daemon) webTapiReady(w http.ResponseWriter) bool {
	if !d.tapiStarted() {
		http.Error(w, errTapiNotStarted.Error(), http.StatusConflict)
		return false
	}
	if !d.Eng.Alive(TapiSlug, engine.WinAgent) {
		http.Error(w, "tapi chat has exited: restart it in the pekoe TUI", http.StatusConflict)
		return false
	}
	return true
}

// webTapiScreen is tapi.screen's last webTailRows lines, in the shape of a
// session's screen.
func (d *Daemon) webTapiScreen(w http.ResponseWriter, r *http.Request) {
	if !d.tapiStarted() {
		http.Error(w, errTapiNotStarted.Error(), http.StatusConflict)
		return
	}
	res, err := d.Call("tapi.screen", nil)
	if err != nil {
		webErr(w, err)
		return
	}
	sc, ok := res.(*engine.Screen)
	if !ok || sc == nil {
		webErr(w, errors.New("no screen"))
		return
	}
	rows := termstyle.Tail(sc.Lines, webTailRows)
	writeJSON(w, map[string]any{"slug": TapiSlug, "rows": rows, "lines": plainOf(rows), "dead": sc.Dead})
}

// webTapiTranscript is tapi.transcript after ?after=<cursor>: the chat
// view's new items (transcript.go). 409 when tapi is not started, 404 when
// there is no transcript to read: the page shows the screen instead.
func (d *Daemon) webTapiTranscript(w http.ResponseWriter, r *http.Request) {
	if !d.tapiStarted() {
		http.Error(w, errTapiNotStarted.Error(), http.StatusConflict)
		return
	}
	after := r.URL.Query().Get("after")
	if len(after) > 200 {
		http.Error(w, "bad cursor", http.StatusBadRequest)
		return
	}
	res, err := d.Call("tapi.transcript", mustJSON(tapiTranscriptP{After: after}))
	if errors.Is(err, errNoTranscript) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err != nil {
		webErr(w, err)
		return
	}
	writeJSON(w, res)
}

// webTapiSend is tapi.send with submit, after the same checks as a
// session's send (400 for control characters, 409 while a permission
// prompt is up), and only when allow_devices is set (403).
func (d *Daemon) webTapiSend(sendable bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := peerFrom(r)
		if !sendable {
			log.Printf("web: %s tried to send to tapi from %s: refused, [web] allow_devices is not set", p.Login, p.Device)
			http.Error(w, "sending to tapi from the web needs [web] allow_devices in config.toml (tapi can act on every repository)", http.StatusForbidden)
			return
		}
		text, ok := webReadText(w, r)
		if !ok {
			return
		}
		who := humanCaller(webVia(p))
		refuse := func(code int, err error) {
			id := d.recordSend(who, TapiSlug, "send", text, err)
			log.Printf("web: %s's send to tapi refused (send #%d, from %s): %v", p.Login, id, p.Device, err)
			http.Error(w, err.Error(), code)
		}
		if !d.webTapiReady(w) {
			return
		}
		// as for a session: a permission menu takes the first character
		// as its choice, and the web never answers tapi's prompts
		if st, wk := d.tapiNow(); tapiAnswerable(st, wk) {
			refuse(http.StatusConflict, errors.New("tapi is waiting for a permission answer: answer it at the terminal first"))
			return
		}
		// tapi.send checks for a prompt again (hook state and the screen),
		// before typing and right before Enter
		_, err := d.CallAs(who, "tapi.send", mustJSON(tapiSendP{Text: text, Submit: true}))
		id := d.recordSend(who, TapiSlug, "send", text, err)
		if err != nil {
			log.Printf("web: %s's send to tapi failed (send #%d, from %s): %v", p.Login, id, p.Device, err)
			webErr(w, err)
			return
		}
		log.Printf("web: %s sends to tapi (send #%d, %d chars, from %s): %q", p.Login, id, len([]rune(text)), p.Device, cutRunes(text, webTapiLogText))
		writeJSON(w, map[string]any{"ok": true, "send": id})
	}
}

// webTapiKeys presses keys in tapi's question menu: the session keypad's
// rules (webKeys), only while tapi waits on a question and no permission
// menu is on its screen.
func (d *Daemon) webTapiKeys(w http.ResponseWriter, r *http.Request) {
	keys, ok := webReadKeys(w, r)
	if !ok {
		return
	}
	if !d.webTapiReady(w) {
		return
	}
	st, wk := d.tapiNow()
	if tapiAnswerable(st, wk) {
		http.Error(w, "tapi is waiting for a permission answer: answer it at the terminal", http.StatusConflict)
		return
	}
	if st != state.Waiting || wk != "question" {
		http.Error(w, fmt.Sprintf("tapi is not waiting on a question (state: %s)", st), http.StatusConflict)
		return
	}
	// tapi's hooks can be faked like a session's: trust the screen
	if sc, err := d.Eng.Capture(TapiSlug, engine.WinAgent); err != nil || permissionOnScreen(sc.Lines) {
		http.Error(w, "tapi shows a permission menu: answer it in the pekoe TUI", http.StatusConflict)
		return
	}
	p := peerFrom(r)
	log.Printf("web: %s presses %s on tapi (from %s)", p.Login, strings.Join(keys, " "), p.Device)
	if _, err := d.CallAs(humanCaller(webVia(p)), "tapi.keys", mustJSON(keysP{Keys: keys})); err != nil {
		webErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
