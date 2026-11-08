package daemon

// The phone web UI ([web], docs/web.md). bobad serves it on the tailnet
// only, through an embedded tsnet node (webts.go); this file is the HTTP
// layer, which knows nothing about tsnet: who is calling comes from a
// whoIsFunc, so tests can put a fake in its place.
//
// Every request is checked before it reaches a handler (webGuard):
//   - Host is the node's own name (no DNS rebinding)
//   - the tailnet says the caller is an allowed user (and device), not a
//     tagged node, and not this machine (an agent here must not answer its
//     own permission prompt through the Mac's own Tailscale)
//   - writes carry a custom header and JSON, so a page on another origin
//     in the phone's browser cannot forge them (CSRF)
// The handlers are thin: they call the same API methods as the CLI.

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"boba/internal/config"
	"boba/internal/engine"
	"boba/internal/state"
)

//go:embed webui
var webFiles embed.FS

// webPeer is the other end of a web UI connection, as the tailnet sees it.
type webPeer struct {
	Login  string `json:"login"`  // UserProfile.LoginName, e.g. you@github
	Device string `json:"device"` // machine name, e.g. iphone-15
	Tagged bool   `json:"-"`      // a tagged node (owned by tags, not a person)
}

// whoIsFunc resolves a connection's remote address ("100.x.y.z:port").
type whoIsFunc func(ctx context.Context, remoteAddr string) (*webPeer, error)

type webOpts struct {
	cfg   config.Web
	who   whoIsFunc
	hosts []string // the names this node answers to (tsnet CertDomains)
	// local reports whether ip belongs to this machine (default: an
	// address of one of its network interfaces).
	local func(ip netip.Addr) bool
}

const (
	webHeader   = "X-Boba-Web" // every write carries it: forces a CORS preflight cross-origin
	webMaxBody  = 64 << 10
	webMaxText  = 4000 // runes in one send
	webTailRows = 80   // screen lines returned
)

// webHandler builds the whole web UI: static files and /api/.
func (d *Daemon) webHandler(o webOpts) http.Handler {
	if o.local == nil {
		o.local = isLocalAddr
	}
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFiles, "webui")
	files := http.FileServerFS(static)
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	}))
	mux.HandleFunc("GET /icon.png", serveIcon(180))
	mux.HandleFunc("GET /icon-512.png", serveIcon(512))
	mux.HandleFunc("GET /manifest.webmanifest", func(w http.ResponseWriter, r *http.Request) {
		b, _ := webFiles.ReadFile("webui/manifest.webmanifest")
		w.Header().Set("Content-Type", "application/manifest+json")
		w.Write(b)
	})
	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, peerFrom(r))
	})
	mux.HandleFunc("GET /api/sessions", d.webSessions)
	mux.HandleFunc("GET /api/sessions/{slug}/screen", d.webScreen)
	mux.HandleFunc("POST /api/sessions/{slug}/answer", d.webAnswer)
	mux.HandleFunc("POST /api/sessions/{slug}/send", d.webSend)
	return webGuard(o, mux)
}

type peerKey struct{}

func peerFrom(r *http.Request) *webPeer {
	p, _ := r.Context().Value(peerKey{}).(*webPeer)
	return p
}

func webGuard(o webOpts, next http.Handler) http.Handler {
	users := lowerSet(o.cfg.AllowUsers)
	devices := lowerSet(o.cfg.AllowDevices)
	hosts := map[string]bool{}
	for _, h := range o.hosts {
		hosts[strings.ToLower(strings.TrimSuffix(h, "."))] = true
	}
	deny := func(w http.ResponseWriter, r *http.Request, code int, why string) {
		log.Printf("web: %d %s %s from %s: %s", code, r.Method, r.URL.Path, r.RemoteAddr, why)
		http.Error(w, http.StatusText(code), code)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		host := strings.ToLower(r.Host)
		if hh, _, err := net.SplitHostPort(host); err == nil {
			host = hh
		}
		if !hosts[host] {
			deny(w, r, http.StatusMisdirectedRequest, "host "+r.Host)
			return
		}
		ap, err := netip.ParseAddrPort(r.RemoteAddr)
		if err != nil {
			deny(w, r, http.StatusForbidden, "bad remote address")
			return
		}
		if o.local(ap.Addr().Unmap()) {
			deny(w, r, http.StatusForbidden, "connection from this machine")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		p, err := o.who(ctx, r.RemoteAddr)
		cancel()
		switch {
		case err != nil || p == nil:
			deny(w, r, http.StatusForbidden, fmt.Sprintf("whois: %v", err))
			return
		case p.Tagged:
			deny(w, r, http.StatusForbidden, "tagged node "+p.Device)
			return
		case !users[strings.ToLower(p.Login)]:
			deny(w, r, http.StatusForbidden, "user "+p.Login+" not in allow_users")
			return
		case len(devices) > 0 && !devices[strings.ToLower(p.Device)]:
			deny(w, r, http.StatusForbidden, "device "+p.Device+" not in allow_devices")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if r.Header.Get(webHeader) != "1" {
				deny(w, r, http.StatusForbidden, "write without "+webHeader)
				return
			}
			if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(ct) != "application/json" {
				deny(w, r, http.StatusUnsupportedMediaType, "content-type "+ct)
				return
			}
			if s := r.Header.Get("Sec-Fetch-Site"); s != "" && s != "same-origin" {
				deny(w, r, http.StatusForbidden, "sec-fetch-site "+s)
				return
			}
			if org := r.Header.Get("Origin"); org != "" && org != "https://"+host {
				deny(w, r, http.StatusForbidden, "origin "+org)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		r.Body = http.MaxBytesReader(w, r.Body, webMaxBody)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), peerKey{}, p)))
	})
}

func lowerSet(xs []string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		if x = strings.ToLower(strings.TrimSpace(x)); x != "" {
			m[x] = true
		}
	}
	return m
}

// isLocalAddr: ip is assigned to one of this machine's interfaces (e.g. the
// Mac's own Tailscale address on utun) or is a loopback address.
func isLocalAddr(ip netip.Addr) bool {
	if ip.IsLoopback() {
		return true
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return true // cannot tell: refuse
	}
	for _, a := range addrs {
		if pfx, err := netip.ParsePrefix(a.String()); err == nil && pfx.Addr().Unmap() == ip {
			return true
		}
	}
	return false
}

// ---- API ----

// webSession is what the phone sees of a session: no paths, env or tokens.
type webSession struct {
	Slug        string      `json:"slug"`
	Repo        string      `json:"repo"`
	Branch      string      `json:"branch"`
	Agent       string      `json:"agent"`
	State       state.State `json:"state"`
	WaitKind    string      `json:"wait_kind,omitempty"`
	StateSince  time.Time   `json:"state_since"`
	Answerable  bool        `json:"answerable"` // allow/deny makes sense now
	PendingTool *state.Tool `json:"pending_tool,omitempty"`
	LastTool    string      `json:"last_tool,omitempty"`
	LastMessage string      `json:"last_message,omitempty"`
	Diffstat    state.Stat  `json:"diffstat"`
	PR          int         `json:"pr,omitempty"`
	Stopped     bool        `json:"stopped,omitempty"`
	Error       string      `json:"error,omitempty"`
}

// webAnswerable: a permission prompt the agent's allow/deny keys answer.
// A question (AskUserQuestion, elicitation) wants words, not "1".
func webAnswerable(s *state.Session) bool {
	return s.State == state.Waiting && s.WaitKind != "question" && !s.Stopped
}

func toWebSession(s *state.Session) webSession {
	return webSession{
		Slug: s.Slug, Repo: s.Repo, Branch: s.Branch, Agent: s.Agent,
		State: s.State, WaitKind: s.WaitKind, StateSince: s.StateSince,
		Answerable: webAnswerable(s), PendingTool: s.PendingTool,
		LastTool: s.LastTool, LastMessage: s.LastMessage, Diffstat: s.Diffstat,
		PR: s.PR, Stopped: s.Stopped, Error: s.Error,
	}
}

// webRank orders the list: waiting first (oldest wait on top), then
// running, then everything else by most recent change.
func webRank(s *state.Session) int {
	switch s.State {
	case state.Waiting:
		return 0
	case state.Running, state.Creating:
		return 1
	case state.Done, state.Failed:
		return 2
	case state.Landed:
		return 4
	}
	return 3
}

func (d *Daemon) webSessions(w http.ResponseWriter, r *http.Request) {
	res, err := d.Call("session.list", nil)
	if err != nil {
		webErr(w, err)
		return
	}
	list := res.([]*state.Session)
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if ra, rb := webRank(a), webRank(b); ra != rb {
			return ra < rb
		}
		if a.State == state.Waiting {
			return a.StateSince.Before(b.StateSince)
		}
		return a.StateSince.After(b.StateSince)
	})
	out := make([]webSession, 0, len(list))
	for _, s := range list {
		out = append(out, toWebSession(s))
	}
	writeJSON(w, map[string]any{"sessions": out, "at": time.Now()})
}

func (d *Daemon) webScreen(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	res, err := d.Call("session.screen", mustJSON(map[string]any{"slug": slug}))
	if err != nil {
		webErr(w, err)
		return
	}
	sc, ok := res.(*engine.Screen)
	if !ok || sc == nil {
		webErr(w, errors.New("no screen"))
		return
	}
	writeJSON(w, map[string]any{"slug": slug, "lines": plainTail(sc.Lines, webTailRows), "dead": sc.Dead})
}

// plainTail: the last n lines without colours, trailing blank lines dropped.
func plainTail(lines []string, n int) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, strings.TrimRight(stripANSI(l), " "))
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

type webAnswerIn struct {
	Answer string `json:"answer"`
	// Tool is the pending tool the user saw when pressing the button. If
	// the agent has moved on to another prompt meanwhile, the answer is
	// refused (409) instead of landing on a prompt nobody read.
	Tool *state.Tool `json:"tool"`
}

func (d *Daemon) webAnswer(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	var in webAnswerIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	// "always" (and its key aliases) never comes from the phone: a
	// standing permission deserves the full terminal.
	if in.Answer != "allow" && in.Answer != "deny" {
		http.Error(w, "answer must be allow or deny", http.StatusBadRequest)
		return
	}
	s, err := d.snapshot(slug)
	if err != nil {
		webErr(w, err)
		return
	}
	if !webAnswerable(s) {
		http.Error(w, fmt.Sprintf("%s is not waiting for allow/deny (state: %s)", slug, s.State), http.StatusConflict)
		return
	}
	if !sameTool(s.PendingTool, in.Tool) {
		http.Error(w, "the prompt changed; look again", http.StatusConflict)
		return
	}
	p := peerFrom(r)
	log.Printf("web: %s answers %s on %s (%s from %s)", p.Login, in.Answer, slug, toolLabel(s.PendingTool), p.Device)
	res, err := d.Call("session.answer", mustJSON(answerP{Slug: slug, Answer: in.Answer}))
	if err != nil {
		webErr(w, err)
		return
	}
	if ss, ok := res.(*state.Session); ok {
		writeJSON(w, toWebSession(ss))
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func sameTool(a, b *state.Tool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Name == b.Name && a.Input == b.Input
}

func toolLabel(t *state.Tool) string {
	if t == nil {
		return "no tool"
	}
	return strings.TrimSpace(t.Name + " " + truncate(t.Input, 80))
}

func (d *Daemon) webSend(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	var in struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		http.Error(w, "empty text", http.StatusBadRequest)
		return
	}
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) > webMaxText {
		http.Error(w, fmt.Sprintf("text must be at most %d characters", webMaxText), http.StatusBadRequest)
		return
	}
	p := peerFrom(r)
	log.Printf("web: %s sends %d chars to %s (from %s)", p.Login, len(text), slug, p.Device)
	if _, err := d.Call("session.send", mustJSON(sendP{Slug: slug, Text: text, Submit: true})); err != nil {
		webErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func webErr(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	if strings.Contains(err.Error(), "no such session") {
		code = http.StatusNotFound
	}
	http.Error(w, err.Error(), code)
}

// ---- status (boba web) ----

// webStatus is what `boba web` shows: whether the node is up, and the
// login URL while it waits for the first join.
type webStatus struct {
	Built   bool     `json:"built"` // this binary has the web UI (-tags web)
	Enabled bool     `json:"enabled"`
	State   string   `json:"state"` // off | unavailable (not built) | starting | needs-login | running | error
	AuthURL string   `json:"auth_url,omitempty"`
	URLs    []string `json:"urls,omitempty"`
	Error   string   `json:"error,omitempty"`
}

type webState struct {
	mu   sync.Mutex
	st   webStatus
	stop func() // closes the HTTP server and the tsnet node
}

func (w *webState) set(f func(*webStatus)) {
	w.mu.Lock()
	f(&w.st)
	w.mu.Unlock()
}

func (w *webState) get() webStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.st
}
