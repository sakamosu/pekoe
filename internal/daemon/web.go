package daemon

// The phone web UI ([web], docs/web.md). pekoed serves it on the tailnet
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
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"pekoe/internal/config"
	"pekoe/internal/engine"
	"pekoe/internal/mascot"
	"pekoe/internal/state"
	"pekoe/internal/termstyle"
	"pekoe/internal/theme"
)

var webHexRe = regexp.MustCompile(`^#[0-9a-f]{6}$`)

//go:embed webui
var webFiles embed.FS

// webPeer is the other end of a web UI connection, as the tailnet sees it.
type webPeer struct {
	Login  string `json:"login"`  // UserProfile.LoginName, e.g. you@github
	Device string `json:"device"` // machine name, e.g. iphone-15
	Tagged bool   `json:"-"`      // a tagged node (owned by tags, not a person)
	Self   bool   `json:"-"`      // the node runs on this machine (its hostname)
	// Via is how the caller is recorded (send log, inbox answers) when it
	// is not "web:<device>": "gui" for the local GUI (gui.go).
	Via string `json:"-"`
}

// whoIsFunc resolves a connection's remote address ("100.x.y.z:port").
type whoIsFunc func(ctx context.Context, remoteAddr string) (*webPeer, error)

type webOpts struct {
	cfg   config.Web
	who   whoIsFunc
	hosts func() []string // the names this node answers to (tsnet CertDomains)
	// local reports whether ip belongs to this machine (default: an
	// address of one of its network interfaces).
	local func(ip netip.Addr) bool
}

const (
	webHeader   = "X-Pekoe-Web" // every write carries it: forces a CORS preflight cross-origin
	webMaxBody  = 64 << 10
	webMaxText  = 4000 // runes in one send
	webTailRows = 80   // screen lines returned
	webMaxKeys  = 8    // keys in one /keys request
)

// webKeys: what the keypad may press in a question's menu (tmux key
// names). Moving, toggling, picking a numbered choice, submitting,
// backing out: no modifiers (C-c, M-…) and no free text, which is send's.
var webKeys = map[string]bool{
	"Up": true, "Down": true, "Left": true, "Right": true,
	"Tab": true, "BTab": true, "Space": true, "Enter": true, "Escape": true,
	"0": true, "1": true, "2": true, "3": true, "4": true,
	"5": true, "6": true, "7": true, "8": true, "9": true,
}

// webHandler builds the whole web UI: static files and /api/.
func (d *Daemon) webHandler(o webOpts) http.Handler {
	if o.local == nil {
		o.local = isLocalAddr
	}
	// tapi chat: send only with allow_devices, and the confirm sheet as
	// [web] confirm_tapi_send says (read per request)
	sendable := len(lowerSet(o.cfg.AllowDevices)) > 0
	return webGuard(o, d.webMux(webSurface{
		tapiSend:    sendable,
		confirmTapi: func() bool { return d.cfg().Web.ConfirmTapiSendOn() },
	}))
}

// webSurface is what differs between the two front doors of the same
// handlers: the phone on the tailnet (webGuard) and the local GUI on
// 127.0.0.1 (guiGuard, gui.go).
type webSurface struct {
	tapiSend    bool        // POST /api/tapi/send is allowed
	confirmTapi func() bool // the page puts a send to tapi through the confirm sheet
	// always: a permission prompt may be answered "always" (the agent's
	// "don't ask again"). Only the local GUI, as the TUI's A; never the
	// phone (docs/gui.md 「決まったこと」5).
	always bool
	// index is the page served at / (a path in webui): the phone's
	// index.html, or the local GUI's desktop page.
	index string
}

// webMux registers the static files and the API both front doors serve.
func (d *Daemon) webMux(sf webSurface) *http.ServeMux {
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFiles, "webui")
	files := http.FileServerFS(static)
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	}))
	if sf.index != "" {
		mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFileFS(w, r, static, sf.index)
		})
	}
	mux.HandleFunc("GET /icon.png", serveIcon(180))
	mux.HandleFunc("GET /icon-512.png", serveIcon(512))
	mux.HandleFunc("GET /manifest.webmanifest", func(w http.ResponseWriter, r *http.Request) {
		b, _ := webFiles.ReadFile("webui/manifest.webmanifest")
		w.Header().Set("Content-Type", "application/manifest+json")
		w.Write(b)
	})
	mux.HandleFunc("GET /theme.css", d.webThemeCSS)
	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, peerFrom(r))
	})
	mux.HandleFunc("GET /api/sessions", d.webSessions)
	mux.HandleFunc("GET /api/sessions/{slug}/screen", d.webScreen)
	mux.HandleFunc("POST /api/sessions/{slug}/answer", d.webAnswer(sf.always))
	mux.HandleFunc("POST /api/sessions/{slug}/send", d.webSend)
	mux.HandleFunc("POST /api/sessions/{slug}/keys", d.webKeys)
	mux.HandleFunc("GET /api/inbox", d.webInbox)
	mux.HandleFunc("POST /api/inbox/{id}/answer", d.webInboxAnswer)
	// tapi chat (webtapi.go): its screen, send (webSurface.tapiSend) and
	// the question keypad. No allow/deny: tapi's permission prompts are
	// answered at the terminal.
	mux.HandleFunc("GET /api/tapi", d.webTapi(sf))
	mux.HandleFunc("GET /api/tapi/screen", d.webTapiScreen)
	mux.HandleFunc("GET /api/tapi/transcript", d.webTapiTranscript)
	mux.HandleFunc("POST /api/tapi/send", d.webTapiSend(sf.tapiSend))
	mux.HandleFunc("POST /api/tapi/keys", d.webTapiKeys)
	mux.HandleFunc("GET /api/overview", d.webOverview)
	return mux
}

// webCSP is the pages' Content-Security-Policy, on both front doors.
const webCSP = "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

// webSecurityHeaders: what every response of both front doors carries.
func webSecurityHeaders(h http.Header) {
	h.Set("Content-Security-Policy", webCSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
}

// webLimitBody caps the request body (a send carries its images).
func webLimitBody(w http.ResponseWriter, r *http.Request) {
	limit := int64(webMaxBody)
	if webSendBody(r) {
		limit = webMaxSendBody // the images ride in it (webimage.go)
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
}

type peerKey struct{}

func peerFrom(r *http.Request) *webPeer {
	p, _ := r.Context().Value(peerKey{}).(*webPeer)
	return p
}

func webGuard(o webOpts, next http.Handler) http.Handler {
	users := lowerSet(o.cfg.AllowUsers)
	devices := lowerSet(o.cfg.AllowDevices)
	ownHost := func(host string) bool {
		for _, h := range o.hosts() {
			if strings.EqualFold(strings.TrimSuffix(h, "."), host) {
				return true
			}
		}
		return false
	}
	deny := func(w http.ResponseWriter, r *http.Request, code int, why string) {
		log.Printf("web: %d %s %s from %s: %s", code, r.Method, r.URL.Path, r.RemoteAddr, why)
		http.Error(w, http.StatusText(code), code)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		webSecurityHeaders(h)
		host := strings.ToLower(r.Host)
		if hh, _, err := net.SplitHostPort(host); err == nil {
			host = hh
		}
		if host == "" || !ownHost(host) {
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
		case p.Self:
			deny(w, r, http.StatusForbidden, "node "+p.Device+" runs on this machine")
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
		webLimitBody(w, r)
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

// webSession is what the phone sees of a session. Not the worktree and
// repository paths, env or the Claude session id; paths inside a pending
// tool's input, an error or the last message stay (they are what the user
// judges by).
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
	Test        string      `json:"test,omitempty"`        // pass | fail | running
	GitBlocked  bool        `json:"git_blocked,omitempty"` // its .git was changed: pekoe runs no git there
	Queue       string      `json:"queue,omitempty"`       // queued: why it has not started (queue.go)
	// Look and PRLook are the TUI's Sessions-panel symbol, color role and
	// word (theme.Look / theme.PRLook, unicode icons): one table for both.
	Look   webLook  `json:"look"`
	PRLook *webLook `json:"pr_look,omitempty"`
}

type webLook struct {
	Icon string `json:"icon"`
	Role string `json:"role"`
	Word string `json:"word"`
}

func toWebLook(l theme.StateLook) webLook {
	return webLook{Icon: l.Icon, Role: string(l.Role), Word: l.Word}
}

// webAnswerable: a permission prompt the agent's allow/deny keys answer.
// A question (AskUserQuestion, elicitation) wants words, not "1".
func webAnswerable(s *state.Session) bool {
	return s.State == state.Waiting && s.WaitKind != "question" && !s.Stopped
}

func toWebSession(s *state.Session) webSession {
	ws := webSession{
		Slug: s.Slug, Repo: s.Repo, Branch: s.Branch, Agent: s.Agent,
		State: s.State, WaitKind: s.WaitKind, StateSince: s.StateSince,
		Answerable: webAnswerable(s), PendingTool: s.PendingTool,
		LastTool: s.LastTool, LastMessage: s.LastMessage, Diffstat: s.Diffstat,
		PR: s.PR, Stopped: s.Stopped, Error: s.Error, GitBlocked: s.GitBlocked != "", Queue: s.Queue,
		Test: s.Test.Status, Look: toWebLook(theme.Look(theme.WebIcons(), s.State, 0, false)),
	}
	if l, ok := theme.PRLook(theme.WebIcons(), s.PRStatus); ok {
		wl := toWebLook(l)
		ws.PRLook = &wl
	}
	return ws
}

// webThemeCSS is the TUI's palette as CSS variables, so the phone shows
// the same colors (and the user's [theme.colors]) as the terminal. The
// local GUI's page (<html data-gui>) also gets the light palette
// (docs/gui-design.md 決まったこと 11): by the OS's appearance, or by the
// page's own choice (data-theme). The phone's page has no data-gui and stays
// dark, as the TUI.
func (d *Daemon) webThemeCSS(w http.ResponseWriter, r *http.Request) {
	var cfg *config.Theme
	if g := d.cfg(); g != nil {
		cfg = &g.Theme
	}
	p := theme.WebPalette(cfg)
	var b strings.Builder
	b.WriteString(":root {\n")
	end := p[theme.PrimaryEndKey]
	if end == "" {
		end = p[string(theme.Primary)] // solid focus border, as in the TUI
	}
	writeWebPalette(&b, p, end)
	// tapi's face in the chat title, painted as the TUI paints pearl1
	var mc config.Mascot
	if d.Global != nil {
		mc = d.Global.Mascot
	}
	pal := mascot.PaletteFor(mc.LightPalette(), mc.Colors)
	def := mascot.PaletteFor(mc.LightPalette(), nil)
	for _, c := range []struct{ name, v, def string }{
		{"bd", pal.Bd, def.Bd}, {"sh", pal.Sh, def.Sh}, {"eye", pal.Eye, def.Eye},
	} {
		v := strings.ToLower(c.v)
		if !webHexRe.MatchString(v) {
			v = c.def // only #rrggbb goes into CSS
		}
		fmt.Fprintf(&b, "  --tapi-%s: %s;\n", c.name, v)
	}
	b.WriteString("}\n")
	light := theme.WebLightPalette()
	var lb strings.Builder
	lb.WriteString("  color-scheme: light;\n")
	writeWebPalette(&lb, light, light[theme.PrimaryEndKey])
	fmt.Fprintf(&b, "@media (prefers-color-scheme: light) {\n:root[data-gui]:not([data-theme=\"dark\"]) {\n%s}\n}\n", lb.String())
	fmt.Fprintf(&b, ":root[data-gui][data-theme=\"light\"] {\n%s}\n", lb.String())
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write([]byte(b.String()))
}

// writeWebPalette writes a palette's roles as CSS variables (end: the
// focus ramp's end, primary when the border is solid).
func writeWebPalette(b *strings.Builder, p map[string]string, end string) {
	for _, role := range theme.WebRoles {
		fmt.Fprintf(b, "  --%s: %s;\n", role, p[string(role)])
	}
	fmt.Fprintf(b, "  --chrome: %s;\n", p[theme.ChromeKey])
	fmt.Fprintf(b, "  --raised: %s;\n", p[theme.RaisedKey])
	fmt.Fprintf(b, "  --primary-end: %s;\n", end)
	fmt.Fprintf(b, "  --bubble-me: %s;\n", p[theme.BubbleMeKey])
}

// webRank orders the list: waiting first (oldest wait on top), then
// running, then everything else by most recent change.
func webRank(s *state.Session) int {
	switch s.State {
	case state.Waiting:
		return 0
	case state.Running, state.Creating, state.Queued:
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
	// rows: the lines with their colors (termstyle); lines: the same
	// lines as plain text (the confirm sheet, older pages)
	rows := termstyle.Tail(sc.Lines, webTailRows)
	writeJSON(w, map[string]any{"slug": slug, "rows": rows, "lines": plainOf(rows), "dead": sc.Dead})
}

// plainTail: the last n lines without colours, trailing blank lines dropped.
func plainTail(lines []string, n int) []string { return plainOf(termstyle.Tail(lines, n)) }

func plainOf(rows [][]termstyle.Run) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = termstyle.Text(r)
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

// webAnswer answers a permission prompt: allow or deny, and "always" only
// where the surface allows it (the local GUI). Only these words: the key
// aliases session.answer also takes (y, n, a, A) never come over HTTP.
func (d *Daemon) webAnswer(always bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { d.webAnswerAs(w, r, always) }
}

func (d *Daemon) webAnswerAs(w http.ResponseWriter, r *http.Request, always bool) {
	slug := r.PathValue("slug")
	var in webAnswerIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	// "always" never comes from the phone: a standing permission deserves
	// the full terminal (or the Mac's own screen, the local GUI).
	switch {
	case in.Answer == "allow", in.Answer == "deny":
	case in.Answer == "always" && always:
	case always:
		http.Error(w, "answer must be allow, deny or always", http.StatusBadRequest)
		return
	default:
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
	p := peerFrom(r)
	log.Printf("web: %s answers %s on %s (%s from %s)", p.Login, in.Answer, slug, toolLabel(s.PendingTool), p.Device)
	// the prompt check is inside session.answer (Expect), right before the
	// keys go out, so a prompt that changes meanwhile is not answered
	res, err := d.Call("session.answer", mustJSON(answerP{Slug: slug, Answer: in.Answer, Expect: &answerExpect{Tool: in.Tool}}))
	if errors.Is(err, errPromptChanged) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
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
	text, imgs, ok := webReadSend(w, r)
	if !ok {
		return
	}
	s, err := d.snapshot(slug)
	if err != nil {
		webErr(w, err)
		return
	}
	// A permission menu takes the first character as its choice: "2…"
	// would be "Yes, and don't ask again", the always the web never gives.
	if webAnswerable(s) {
		http.Error(w, slug+" is waiting for allow/deny: answer it first", http.StatusConflict)
		return
	}
	// the images go in only now: a refused send above leaves nothing behind
	typed, files, err := webStoreImages(w, s.Slug, text, imgs)
	if err != nil {
		// as a send refused before typing: in the send log, the images counted
		d.recordSend(humanCaller(webVia(peerFrom(r))), s.Slug, "send", withImageCount(text, len(imgs)), err)
		return
	}
	p := peerFrom(r)
	log.Printf("web: %s sends %d chars to %s (from %s)%s", p.Login, len(text), slug, p.Device, imagesNote(files))
	// session.send records what is typed (the image lines too) in the send log
	_, err = d.CallAs(humanCaller(webVia(p)), "session.send", mustJSON(sendP{Slug: slug, Text: typed, Submit: true}))
	keepImagesAfter(err, files)
	if err != nil {
		webErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "images": len(files)})
}

// webQuestion: a question (AskUserQuestion, elicitation) is up, the menu
// the keypad drives.
func webQuestion(s *state.Session) bool {
	return s.State == state.Waiting && s.WaitKind == "question" && !s.Stopped
}

// webKeys presses a few keys in a question's menu (arrows, Tab, Space,
// digits, Enter, Escape). Only while a question waits and no permission
// menu is on the screen: in a permission menu "2" or Down+Enter would pick
// "don't ask again", the always the web never gives, so that menu stays
// allow/deny only.
func (d *Daemon) webKeys(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	keys, ok := webReadKeys(w, r)
	if !ok {
		return
	}
	s, err := d.snapshot(slug)
	if err != nil {
		webErr(w, err)
		return
	}
	if webAnswerable(s) {
		http.Error(w, slug+" is waiting for allow/deny: answer it with allow or deny", http.StatusConflict)
		return
	}
	if !webQuestion(s) {
		http.Error(w, fmt.Sprintf("%s is not waiting on a question (state: %s)", slug, s.State), http.StatusConflict)
		return
	}
	// The state comes from hooks, and an agent can send a fake
	// AskUserQuestion PreToolUse (session.event needs no token) to turn its
	// real permission prompt into a "question". The screen it cannot fake:
	// with a permission menu on it, no keys.
	if sc, err := d.Eng.Capture(slug, engine.WinAgent); err != nil || permissionOnScreen(sc.Lines) {
		http.Error(w, slug+" shows a permission menu: answer it in the pekoe TUI", http.StatusConflict)
		return
	}
	p := peerFrom(r)
	log.Printf("web: %s presses %s on %s (from %s)", p.Login, strings.Join(keys, " "), slug, p.Device)
	if _, err := d.Call("session.keys", mustJSON(keysP{Slug: slug, Keys: keys})); err != nil {
		webErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// webReadKeys reads a keypad request's {keys}: 1 to webMaxKeys keys, each
// in webKeys. On false it has written the 400.
func webReadKeys(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	var in struct {
		Keys []string `json:"keys"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return nil, false
	}
	if len(in.Keys) == 0 || len(in.Keys) > webMaxKeys {
		http.Error(w, fmt.Sprintf("keys must be 1 to %d keys", webMaxKeys), http.StatusBadRequest)
		return nil, false
	}
	for _, k := range in.Keys {
		if !webKeys[k] {
			http.Error(w, fmt.Sprintf("key %q is not allowed", k), http.StatusBadRequest)
			return nil, false
		}
	}
	return in.Keys, true
}

func webErr(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	switch {
	case strings.Contains(err.Error(), "no such session"):
		code = http.StatusNotFound
	case errors.Is(err, errPromptUp), errors.Is(err, errPromptUpTyped), errors.Is(err, errPromptUpTUI):
		code = http.StatusConflict
	}
	http.Error(w, err.Error(), code)
}

// takeAuthKeyEnv removes the tailnet auth key from pekoed's environment and
// returns it. pekoed's environment reaches tmux and so every agent pane: an
// agent holding the key could join the tailnet as the user and call the
// web UI from a node that is not "this machine". Called before the
// terminal engine starts.
func takeAuthKeyEnv() string {
	key := ""
	for _, k := range []string{"TS_AUTHKEY", "TS_AUTH_KEY"} {
		if v := os.Getenv(k); v != "" && key == "" {
			key = v
		}
		os.Unsetenv(k)
	}
	return key
}

// ForgetWindowEnv removes from pekoed's environment what says which
// pekoe window it was started from ($TMUX / $TMUX_PANE of a pane,
// $PEKOE_SESSION): an agent that runs pekoe while pekoed is down starts
// it with the agent's environment. Everything pekoed starts inherits its
// environment, and what of that outlives pekoed's tree (a git fsmonitor,
// a database a setup hook started) would then pass for a process born in
// that session's window, stopped when the session goes
// (engine.KillLeftovers). `pekoe daemon run` calls it first.
func ForgetWindowEnv() {
	for _, k := range []string{"TMUX", "TMUX_PANE", "PEKOE_SESSION"} {
		os.Unsetenv(k)
	}
}

// ---- status (pekoe web) ----

// webStatus is what `pekoe web` shows: whether the node is up, and the
// login URL while it waits for the first join.
type webStatus struct {
	Built   bool     `json:"built"` // this binary has the web UI (-tags web)
	Enabled bool     `json:"enabled"`
	State   string   `json:"state"` // off | unavailable (not built) | starting | needs-login | running | error
	AuthURL string   `json:"auth_url,omitempty"`
	URLs    []string `json:"urls,omitempty"`
	Error   string   `json:"error,omitempty"`
	// the tsnet backend as Tailscale names it (NoState, Starting, Running…)
	// and the last error it reported (e.g. a netmap it could not read);
	// they say why a node stays "starting"
	Backend      string `json:"backend,omitempty"`
	BackendError string `json:"backend_error,omitempty"`
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
