package daemon

// The local GUI (docs/gui.md, stages 0 and 1): a desktop page and the web UI's /api/
// served to this Mac's own browser on 127.0.0.1. The phone's front door
// (webGuard) refuses this machine on purpose: an agent here must not
// answer its own permission prompt. This one is for this machine, so who
// is a person is decided by a secret only a person gets:
//
//  1. `pekoe gui` (human token) calls gui.open. pekoed starts listening if
//     it is not yet, makes a one-time token (32 random bytes, 60 s, one
//     use) and writes <data>/gui/open-<random>.html (0600, dir 0700) that
//     only moves the browser to http://127.0.0.1:<port>/#t=<token>. The CLI
//     opens that file: the token is never on a command line (ps shows
//     those to every process of the user). The agents' and the hooks'
//     sandboxes deny reading <data>/gui, as they deny the human token.
//  2. The page reads the fragment (never sent to a server), drops it, and
//     trades the token for a session token: POST /api/gui/session.
//  3. Every other /api/ request carries Authorization: Bearer <session
//     token>. No cookie: a cookie for 127.0.0.1 would ride along to an
//     agent's dev server on another port of the same host. The page keeps
//     the token in sessionStorage, which is per origin (port included).
//
// A one-time token used twice means someone traded it before the person's
// browser did: every session token is revoked, and the log says so.
//
// guiGuard checks every request: loopback only, Host exactly
// 127.0.0.1:<port> (421: DNS rebinding), Origin and Sec-Fetch-Site same
// origin, no CORS (OPTIONS is 403), the bearer compared in constant time,
// JSON for writes, and the web UI's CSP and headers.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"pekoe/internal/paths"
)

const (
	guiOpenTTL     = 60 * time.Second // a one-time token's life
	guiUsedKeep    = 10 * time.Minute // a used one-time token is remembered this long (to see a second use)
	guiMaxSessions = 32               // session tokens kept at once; the oldest made goes
	guiMaxOpens    = 16               // one-time tokens not yet used
	guiTokenBytes  = 32
	viaGUI         = "gui"
)

// guiPeer is who the handlers see behind the local GUI: the person at this
// Mac, recorded as "gui".
var guiPeer = &webPeer{Login: "local", Device: "gui", Via: viaGUI}

type guiOpen struct {
	token   string
	file    string
	expires time.Time
	used    bool
	timer   *time.Timer
}

type guiSession struct {
	token    string
	created  time.Time
	lastUsed time.Time
}

// guiState is the local GUI's listener and tokens, in memory only: a
// restart of pekoed revokes every token.
type guiState struct {
	mu       sync.Mutex
	srv      *http.Server
	port     int
	opens    []*guiOpen
	sessions []*guiSession
	// listen opens the listener (net.Listen; tests put a fake here)
	listen func(network, addr string) (net.Listener, error)
	now    func() time.Time
}

func (g *guiState) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

func guiToken() (string, error) {
	b := make([]byte, guiTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// guiOpenResult is gui.open's answer: where the redirect page is (the CLI
// opens it), the port, and [gui] browser.
type guiOpenResult struct {
	Port    int       `json:"port"`
	File    string    `json:"file"`
	Expires time.Time `json:"expires"`
	Browser string    `json:"browser,omitempty"`
}

// guiStatus is gui.status's answer (`pekoe gui --status`).
type guiStatus struct {
	Enabled   bool       `json:"enabled"`
	Listening bool       `json:"listening"`
	Port      int        `json:"port,omitempty"`
	Sessions  int        `json:"sessions"`
	LastUsed  *time.Time `json:"last_used,omitempty"`
}

// errGUIOff: [gui] enable = false.
var errGUIOff = errors.New("the local GUI is off ([gui] enable = false in config.toml)")

// hGUIOpen is gui.open: listen if not yet, and write a redirect page with
// a fresh one-time token. When the port cannot be bound nothing is
// written, so `pekoe gui` opens no browser: whoever holds the port could
// be a fake GUI waiting for the token.
func (d *Daemon) hGUIOpen() (any, error) {
	cfg := d.cfg().GUI
	if !cfg.On() {
		return nil, errGUIOff
	}
	g := &d.gui
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.srv == nil {
		if err := d.guiListenLocked(cfg.Port); err != nil {
			return nil, err
		}
	}
	g.pruneLocked()
	if n := g.pendingLocked(); n >= guiMaxOpens {
		return nil, fmt.Errorf("%d GUI links are waiting to be opened; wait a minute or run `pekoe gui --revoke`", n)
	}
	tok, err := guiToken()
	if err != nil {
		return nil, err
	}
	name, err := guiToken()
	if err != nil {
		return nil, err
	}
	dir := paths.GUIDir()
	if err := os.MkdirAll(dir, paths.DirPerm); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, paths.DirPerm); err != nil {
		return nil, err
	}
	file := filepath.Join(dir, "open-"+name[:32]+".html")
	if err := writeGUIRedirect(file, g.port, tok); err != nil {
		return nil, err
	}
	o := &guiOpen{token: tok, file: file, expires: g.clock().Add(guiOpenTTL)}
	// the page goes when its token lapses (or right after it is used)
	o.timer = time.AfterFunc(guiOpenTTL, func() { os.Remove(file) })
	g.opens = append(g.opens, o)
	log.Printf("gui: one-time link written for 127.0.0.1:%d (valid %s)", g.port, guiOpenTTL)
	return guiOpenResult{Port: g.port, File: file, Expires: o.expires, Browser: strings.TrimSpace(cfg.Browser)}, nil
}

// guiListenLocked binds 127.0.0.1:port (0: any free port) and serves the
// GUI there. g.mu is held.
func (d *Daemon) guiListenLocked(port int) error {
	g := &d.gui
	listen := g.listen
	if listen == nil {
		listen = net.Listen
	}
	ln, err := listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		hint := ""
		if port != 0 {
			hint = fmt.Sprintf(" (another process holds it: lsof -nP -iTCP:%d -sTCP:LISTEN). The browser was not opened: a program on that port could pose as the GUI. Free the port or set another [gui] port (0 picks a free one)", port)
		}
		return fmt.Errorf("gui: cannot listen on 127.0.0.1:%d: %v%s", port, err, hint)
	}
	g.port = ln.Addr().(*net.TCPAddr).Port
	g.srv = &http.Server{Handler: d.guiHandler(g.port), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	srv := g.srv
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("gui: %v", err)
		}
	}()
	log.Printf("gui: listening on 127.0.0.1:%d", g.port)
	return nil
}

// writeGUIRedirect writes the page that moves the browser to the GUI with
// the token in the fragment. 0600, created new (never through a file or
// link someone put there).
func writeGUIRedirect(file string, port int, token string) error {
	u := fmt.Sprintf("http://127.0.0.1:%d/#t=%s", port, token)
	page := `<!doctype html>
<html><head><meta charset="utf-8"><meta name="referrer" content="no-referrer">
<title>pekoe</title>
<meta http-equiv="refresh" content="0;url=` + u + `">
<script>location.replace("` + u + `")</script>
</head><body>Opening pekoe…</body></html>
`
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, paths.FilePerm)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(page); err != nil {
		f.Close()
		os.Remove(file)
		return err
	}
	return f.Close()
}

// pruneLocked drops lapsed one-time tokens (and their pages) and session
// tokens unused for longer than [gui] session_ttl.
func (g *guiState) pruneLocked() {
	now := g.clock()
	keep := g.opens[:0]
	for _, o := range g.opens {
		switch {
		case !o.used && now.After(o.expires):
			o.drop()
		case o.used && now.After(o.expires.Add(guiUsedKeep)):
		default:
			keep = append(keep, o)
		}
	}
	clear(g.opens[len(keep):])
	g.opens = keep
}

func (g *guiState) pendingLocked() int {
	n := 0
	for _, o := range g.opens {
		if !o.used {
			n++
		}
	}
	return n
}

// drop removes the redirect page now.
func (o *guiOpen) drop() {
	if o.timer != nil {
		o.timer.Stop()
	}
	os.Remove(o.file)
}

// revokeLocked ends every session token and every one-time token not yet
// used (their pages go too).
func (g *guiState) revokeLocked() int {
	n := len(g.sessions)
	g.sessions = nil
	for _, o := range g.opens {
		if !o.used {
			o.drop()
		}
	}
	g.opens = nil
	return n
}

// guiExchange trades a one-time token for a session token. A token used
// before revokes every session: someone else got there first.
func (d *Daemon) guiExchange(token string) (string, int, error) {
	g := &d.gui
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.clock()
	var hit *guiOpen
	for _, o := range g.opens {
		if subtle.ConstantTimeCompare([]byte(o.token), []byte(token)) == 1 {
			hit = o
		}
	}
	switch {
	case hit == nil:
		g.pruneLocked()
		return "", http.StatusUnauthorized, errors.New("this GUI link is not valid (expired, or pekoed restarted): run `pekoe gui` again")
	case hit.used:
		n := g.revokeLocked()
		log.Printf("gui: a one-time link was used twice: someone else traded it first; revoked %d session token(s)", n)
		return "", http.StatusUnauthorized, errors.New("this GUI link was already used by someone else. Every GUI session was revoked; run `pekoe gui` again")
	case now.After(hit.expires):
		hit.drop()
		g.pruneLocked()
		return "", http.StatusUnauthorized, errors.New("this GUI link expired (60 s): run `pekoe gui` again")
	}
	hit.used = true
	hit.drop()
	tok, err := guiToken()
	if err != nil {
		return "", http.StatusInternalServerError, err
	}
	g.pruneSessionsLocked(now, d.cfg().GUI.TTL())
	if len(g.sessions) >= guiMaxSessions {
		g.sessions = g.sessions[1:] // the least recently made
	}
	g.sessions = append(g.sessions, &guiSession{token: tok, created: now, lastUsed: now})
	log.Printf("gui: a browser signed in (%d session token(s))", len(g.sessions))
	return tok, http.StatusOK, nil
}

func (g *guiState) pruneSessionsLocked(now time.Time, ttl time.Duration) {
	keep := g.sessions[:0]
	for _, s := range g.sessions {
		if now.Sub(s.lastUsed) <= ttl {
			keep = append(keep, s)
		}
	}
	clear(g.sessions[len(keep):])
	g.sessions = keep
}

// guiCheckBearer reports whether token is a live session token (and marks
// it used). Every token is compared, in constant time.
func (d *Daemon) guiCheckBearer(token string) bool {
	if token == "" {
		return false
	}
	g := &d.gui
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.clock()
	g.pruneSessionsLocked(now, d.cfg().GUI.TTL())
	var hit *guiSession
	for _, s := range g.sessions {
		if subtle.ConstantTimeCompare([]byte(s.token), []byte(token)) == 1 {
			hit = s
		}
	}
	if hit == nil {
		return false
	}
	hit.lastUsed = now
	return true
}

func (d *Daemon) hGUIStatus() (any, error) {
	g := &d.gui
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pruneSessionsLocked(g.clock(), d.cfg().GUI.TTL())
	st := guiStatus{Enabled: d.cfg().GUI.On(), Listening: g.srv != nil, Sessions: len(g.sessions)}
	if g.srv != nil {
		st.Port = g.port
	}
	for _, s := range g.sessions {
		if st.LastUsed == nil || s.lastUsed.After(*st.LastUsed) {
			t := s.lastUsed
			st.LastUsed = &t
		}
	}
	return st, nil
}

func (d *Daemon) hGUIRevoke() (any, error) {
	g := &d.gui
	g.mu.Lock()
	n := g.revokeLocked()
	g.mu.Unlock()
	log.Printf("gui: revoked %d session token(s) (pekoe gui --revoke)", n)
	return map[string]any{"revoked": n}, nil
}

func (d *Daemon) hGUIStop() (any, error) {
	n, was := d.stopGUI()
	if was {
		log.Printf("gui: stopped listening; revoked %d session token(s)", n)
	}
	return map[string]any{"stopped": was, "revoked": n}, nil
}

// stopGUI closes the listener and revokes every token.
func (d *Daemon) stopGUI() (int, bool) {
	g := &d.gui
	g.mu.Lock()
	srv := g.srv
	g.srv, g.port = nil, 0
	n := g.revokeLocked()
	g.mu.Unlock()
	if srv != nil {
		srv.Close()
	}
	return n, srv != nil
}

// clearGUIPages removes redirect pages a previous pekoed left: their tokens
// died with it.
func clearGUIPages() {
	files, _ := filepath.Glob(filepath.Join(paths.GUIDir(), "open-*.html"))
	for _, f := range files {
		os.Remove(f)
	}
}

// ---- the front door ----

// guiHandler is the local GUI on 127.0.0.1:port: the web UI's handlers
// behind guiGuard, as the TUI has them (tapi send without allow_devices and
// without the confirm sheet, "always" on a permission prompt), the desktop
// page at / (webui/gui/), and the token exchange.
func (d *Daemon) guiHandler(port int) http.Handler {
	mux := d.webMux(webSurface{tapiSend: true, confirmTapi: func() bool { return false }, always: true, index: "gui/index.html", gui: true})
	mux.HandleFunc("POST /api/gui/session", d.guiSessionHandler)
	return d.guiGuard(port, mux)
}

func (d *Daemon) guiSessionHandler(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Token == "" {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	tok, code, err := d.guiExchange(in.Token)
	if err != nil {
		log.Printf("gui: %d POST /api/gui/session: %v", code, err)
		http.Error(w, err.Error(), code)
		return
	}
	writeJSON(w, map[string]any{"token": tok})
}

func (d *Daemon) guiGuard(port int, next http.Handler) http.Handler {
	origin := fmt.Sprintf("127.0.0.1:%d", port)
	deny := func(w http.ResponseWriter, r *http.Request, code int, why string) {
		log.Printf("gui: %d %s %s: %s", code, r.Method, r.URL.Path, why)
		http.Error(w, http.StatusText(code), code)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		webSecurityHeaders(h)
		ap, err := netip.ParseAddrPort(r.RemoteAddr)
		if err != nil || !ap.Addr().Unmap().IsLoopback() {
			deny(w, r, http.StatusForbidden, "connection not from loopback: "+r.RemoteAddr)
			return
		}
		if r.Host != origin {
			deny(w, r, http.StatusMisdirectedRequest, "host "+r.Host)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != "http://"+origin {
			deny(w, r, http.StatusForbidden, "origin "+o)
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodPost:
		default:
			// OPTIONS included: no CORS, so a cross-origin page's
			// preflight for an Authorization header fails
			deny(w, r, http.StatusForbidden, "method "+r.Method)
			return
		}
		api := strings.HasPrefix(r.URL.Path, "/api/")
		if api {
			h.Set("Cache-Control", "no-store")
			if s := r.Header.Get("Sec-Fetch-Site"); s != "" && s != "same-origin" {
				deny(w, r, http.StatusForbidden, "sec-fetch-site "+s)
				return
			}
		} else if r.Method == http.MethodPost {
			deny(w, r, http.StatusForbidden, "write outside /api/")
			return
		}
		if r.Method == http.MethodPost {
			if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(ct) != "application/json" {
				deny(w, r, http.StatusUnsupportedMediaType, "content-type "+ct)
				return
			}
		}
		// the exchange carries the one-time token in its body; every other
		// API request a session token
		if api && !(r.Method == http.MethodPost && r.URL.Path == "/api/gui/session") {
			auth, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || !d.guiCheckBearer(auth) {
				deny(w, r, http.StatusUnauthorized, "no valid session token")
				return
			}
		}
		webLimitBody(w, r)
		next.ServeHTTP(w, r.WithContext(withPeer(r, guiPeer)))
	})
}

func withPeer(r *http.Request, p *webPeer) context.Context {
	return context.WithValue(r.Context(), peerKey{}, p)
}
