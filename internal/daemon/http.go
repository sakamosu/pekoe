package daemon

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"pekoe/internal/errcatch"
	"pekoe/internal/paths"
	"pekoe/internal/state"
	"pekoe/internal/untrusted"
)

type Tab struct {
	URL    string `json:"url"`
	Title  string `json:"title"`
	Port   int    `json:"port"`
	Path   string `json:"path"`
	Slug   string `json:"slug"`
	Errors int    `json:"errors"`
}

type ConsoleError struct {
	Message string `json:"message"`
	Source  string `json:"source,omitempty"`
	Line    int    `json:"line,omitempty"`
	Stack   string `json:"stack,omitempty"`
	At      string `json:"at,omitempty"`
	URL     string `json:"url,omitempty"`
}

func loadOrCreateToken() (string, error) {
	if b, err := os.ReadFile(paths.TokenFile()); err == nil && len(strings.TrimSpace(string(b))) >= 32 {
		return strings.TrimSpace(string(b)), nil
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	t := hex.EncodeToString(buf)
	if err := os.WriteFile(paths.TokenFile(), []byte(t+"\n"), 0o600); err != nil {
		return "", err
	}
	return t, nil
}

func (d *Daemon) startHTTP() error {
	tok, err := loadOrCreateToken()
	if err != nil {
		return err
	}
	d.token = tok
	mux := http.NewServeMux()
	mux.HandleFunc("/health", d.guard(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "version": Version})
	}))
	mux.HandleFunc("/pick", d.guard(d.httpPick))
	mux.HandleFunc("/console", d.guard(d.httpConsole))
	mux.HandleFunc("/tabs", d.guard(d.httpTabs))
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", d.cfg().HTTP.Port))
	if err != nil {
		return err
	}
	d.httpSrv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := d.httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("http: %v", err)
		}
	}()
	return nil
}

// guard: any web page can POST to localhost, so require the install-time
// token and refuse browser origins other than chrome-extension://.
func (d *Daemon) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && !strings.HasPrefix(origin, "chrome-extension://") {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(auth), []byte(d.token)) != 1 {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
		h(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func urlPort(raw string) (int, string) {
	u, err := url.Parse(raw)
	if err != nil {
		return 0, ""
	}
	host := u.Hostname()
	if host != "localhost" && host != "127.0.0.1" && host != "::1" && !strings.HasSuffix(host, ".localhost") {
		return 0, u.Path
	}
	p, _ := strconv.Atoi(u.Port())
	return p, u.Path
}

type pickIn struct {
	URL         string          `json:"url"`
	Title       string          `json:"title"`
	Instruction string          `json:"instruction"`
	OuterHTML   string          `json:"outerHTML"`
	Selector    string          `json:"selector"`
	Styles      json.RawMessage `json:"styles"`
	Component   json.RawMessage `json:"component"`
	Rect        json.RawMessage `json:"rect"`
	Screenshot  string          `json:"screenshot"`
}

type component struct {
	Name      string `json:"name"`
	Framework string `json:"framework"`
	Via       string `json:"via"`
	Source    *struct {
		File string `json:"file"`
		Line int    `json:"line"`
	} `json:"source"`
}

func (d *Daemon) httpPick(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var in pickIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p, err := d.receivePick(in)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "id": p.ID, "slug": p.Slug, "sent": p.Sent})
}

func (d *Daemon) receivePick(in pickIn) (*state.Pick, error) {
	port, _ := urlPort(in.URL)
	repoID, slug := "", ""
	if port > 0 {
		if rid, sl, ok := d.DB.PortOwner(port); ok {
			repoID, slug = rid, sl
		}
	}
	if _, err := d.get(slug); err != nil {
		slug = ""
	}
	if repoID == "" {
		repoID = "_unresolved"
	}
	var comp component
	_ = json.Unmarshal(in.Component, &comp)
	srcLoc := ""
	if comp.Source != nil && comp.Source.File != "" {
		srcLoc = fmt.Sprintf("%s:%d", comp.Source.File, comp.Source.Line)
	} else if comp.Name != "" && slug != "" {
		// React 19 dropped _debugSource: find the component by name (repo untouched)
		if s, err := d.snapshot(slug); err == nil {
			srcLoc = findComponent(s.Path, comp.Name)
		}
	}
	n := d.DB.NextPickNumber()
	dir := paths.PicksDir(repoID)
	if err := os.MkdirAll(dir, paths.DirPerm); err != nil {
		return nil, err
	}
	base := filepath.Join(dir, fmt.Sprintf("%04d", n))
	pk := &state.Pick{
		RepoID: repoID, Slug: slug, URL: in.URL, Title: in.Title, Instruction: in.Instruction,
		Selector: in.Selector, Component: in.Component, Styles: in.Styles, OuterHTML: in.OuterHTML,
		Rect: in.Rect, File: base + ".md",
	}
	if in.Screenshot != "" {
		if _, data, ok := strings.Cut(in.Screenshot, ","); ok {
			if b, err := base64.StdEncoding.DecodeString(data); err == nil {
				if os.WriteFile(base+".png", b, paths.FilePerm) == nil {
					pk.Image = base + ".png"
					pk.ImageSize = pngSize(b)
				}
			}
		}
	}
	if err := os.WriteFile(pk.File, []byte(pickMarkdown(n, pk, comp, srcLoc)), paths.FilePerm); err != nil {
		return nil, err
	}
	if err := d.DB.AddPick(pk); err != nil {
		return nil, err
	}
	d.emit("pick.received", pk)
	if slug != "" && d.cfg().AutoSendOn() && strings.TrimSpace(in.Instruction) != "" {
		if _, err := d.hPickSend(pickSendP{ID: pk.ID}); err == nil {
			pk, _ = d.DB.PickByID(pk.ID)
		}
	}
	return pk, nil
}

func pngSize(b []byte) string {
	if len(b) < 24 || string(b[1:4]) != "PNG" {
		return ""
	}
	w := int(b[16])<<24 | int(b[17])<<16 | int(b[18])<<8 | int(b[19])
	h := int(b[20])<<24 | int(b[21])<<16 | int(b[22])<<8 | int(b[23])
	return fmt.Sprintf("%d×%d", w, h)
}

func findComponent(dir, name string) string {
	if !regexp.MustCompile(`^[A-Za-z_$][\w$]*$`).MatchString(name) {
		return ""
	}
	pat := fmt.Sprintf(`(function|const|let|class|export default function)\s+%s\b`, name)
	var out []byte
	var err error
	if _, lerr := exec.LookPath("rg"); lerr == nil {
		out, err = exec.Command("rg", "-n", "--no-heading", "-m", "1", "-g", "!node_modules", "-g", "*.{tsx,jsx,ts,js,vue,svelte}", "-e", pat, dir).Output()
	} else {
		out, err = exec.Command("grep", "-rnE", "-m", "1", "--include=*.tsx", "--include=*.jsx", "--include=*.ts", "--include=*.js", "--include=*.vue", "--include=*.svelte", "--exclude-dir=node_modules", pat, dir).Output()
	}
	if err != nil {
		return ""
	}
	first := strings.SplitN(string(out), "\n", 2)[0]
	parts := strings.SplitN(first, ":", 3)
	if len(parts) < 2 {
		return ""
	}
	rel, err := filepath.Rel(dir, parts[0])
	if err != nil {
		rel = parts[0]
	}
	return rel + ":" + parts[1]
}

// pickMarkdown is the file the agent reads through "@…/picks/NNNN.md".
// Everything but the instruction (typed by the user in the extension's
// own frame) and the screenshot path came from the page, which can put
// anything in its DOM: that part goes in one fence it cannot close, each
// field flattened to one line, after a note that it is data.
func pickMarkdown(n int, p *state.Pick, comp component, src string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Pick #%04d\n", n)
	if p.Instruction != "" {
		fmt.Fprintf(&b, "\n## instruction\n\n%s\n", p.Instruction)
	}
	if p.Image != "" {
		fmt.Fprintf(&b, "\n- screenshot: %s (%s)\n", p.Image, p.ImageSize)
	}
	var e strings.Builder
	field := func(k, v string) {
		if v = untrusted.Line(v); v != "" {
			fmt.Fprintf(&e, "%s: %s\n", k, v)
		}
	}
	field("page", p.URL)
	field("title", p.Title)
	field("selector", p.Selector)
	if comp.Name != "" {
		c := "<" + comp.Name + "> (" + comp.Framework
		if comp.Via != "" {
			c += ", via " + comp.Via
		}
		field("component", c+")")
	}
	field("source", src)
	if len(p.Styles) > 0 && string(p.Styles) != "null" {
		var st map[string]string
		if json.Unmarshal(p.Styles, &st) == nil && len(st) > 0 {
			keys := make([]string, 0, len(st))
			for k := range st {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			e.WriteString("\ncomputed styles:\n")
			for _, k := range keys {
				fmt.Fprintf(&e, "  %s: %s\n", untrusted.Line(k), untrusted.Line(st[k]))
			}
		}
	}
	if p.OuterHTML != "" {
		fmt.Fprintf(&e, "\nouterHTML:\n%s\n", p.OuterHTML)
	}
	b.WriteString("\n## element\n\n")
	b.WriteString(untrusted.Block("ブラウザのページから取った要素の情報(URL・タイトル・セレクタ・コンポーネント・スタイル・outerHTML)", "text", e.String()))
	b.WriteString("\n")
	return b.String()
}

type pickListP struct {
	Slug string `json:"slug"`
}

func (d *Daemon) hPickList(p pickListP) (any, error) {
	all, err := d.DB.Picks(200)
	if err != nil {
		return nil, err
	}
	out := []*state.Pick{}
	for _, pk := range all {
		if p.Slug == "" || pk.Slug == p.Slug || (pk.Slug == "" && !pk.Sent) {
			out = append(out, pk)
		}
	}
	return out, nil
}

type pickSendP struct {
	ID        int64  `json:"id"`
	Slug      string `json:"slug"`
	Text      string `json:"text"`
	NewBranch string `json:"new_branch"`
	Repo      string `json:"repo"`
}

// hPickSend pastes one short line into the pty: "@/…/picks/0012.md <text>".
// Long HTML never goes through the agent's input box.
func (d *Daemon) hPickSend(p pickSendP) (any, error) {
	pk, err := d.DB.PickByID(p.ID)
	if err != nil {
		return nil, err
	}
	text := pk.Instruction
	if p.Text != "" {
		text = p.Text
	}
	line := "@" + pk.File
	if pk.Image != "" {
		line += " @" + pk.Image
	}
	if strings.TrimSpace(text) != "" {
		line += " " + strings.TrimSpace(text)
	}
	slug := p.Slug
	if slug == "" {
		slug = pk.Slug
	}
	if p.NewBranch != "" {
		repo := p.Repo
		if repo == "" && slug != "" {
			if s, err := d.snapshot(slug); err == nil {
				repo = s.RepoDir
			}
		}
		res, err := d.hNew(newP{Repo: repo, Branch: p.NewBranch, Prompt: line})
		if err != nil {
			return nil, err
		}
		s := res.(*state.Session)
		pk.Slug, pk.Sent, pk.SentTo = s.Slug, true, s.Slug
		_ = d.DB.SavePick(pk)
		d.emit("pick.received", pk)
		return pk, nil
	}
	if slug == "" {
		return nil, errors.New("which session? (no session owns this page's port)")
	}
	if _, err := d.hSend(sendP{Slug: slug, Text: line, Submit: strings.TrimSpace(text) != ""}); err != nil {
		return nil, err
	}
	pk.Slug, pk.Sent, pk.SentTo = slug, true, slug
	if s, err := d.snapshot(slug); err == nil {
		pk.RepoID = s.RepoID
	}
	pk.Instruction = text
	_ = d.DB.SavePick(pk)
	d.emit("pick.received", pk)
	return pk, nil
}

type idP struct {
	ID int64 `json:"id"`
}

func (d *Daemon) hPickDiscard(p idP) (any, error) {
	pk, err := d.DB.PickByID(p.ID)
	if err != nil {
		return nil, err
	}
	os.Remove(pk.File)
	if pk.Image != "" {
		os.Remove(pk.Image)
	}
	_ = d.DB.DeletePick(p.ID)
	d.emit("pick.received", map[string]any{"id": p.ID, "discarded": true})
	return nil, nil
}

// Any page on localhost can report console errors (page.js forwards what
// the page logs), so what one page can push is bounded: the body, the
// errors in one request, each field, and the errors a port gets a minute.
const (
	consoleMaxBody    = 1 << 20
	consoleMaxPerReq  = 50
	consoleMaxPerMin  = 120
	consoleMaxMessage = 2000 // runes
	consoleMaxStack   = 8000
	consoleMaxField   = 500 // source, at, url
)

// limitConsole trims one request's errors to the caps above.
func limitConsole(errs []ConsoleError) []ConsoleError {
	if len(errs) > consoleMaxPerReq {
		errs = errs[:consoleMaxPerReq]
	}
	for i := range errs {
		e := &errs[i]
		e.Message = truncate(e.Message, consoleMaxMessage)
		e.Stack = truncate(e.Stack, consoleMaxStack)
		e.Source = truncate(e.Source, consoleMaxField)
		e.At = truncate(e.At, consoleMaxField)
	}
	return errs
}

// consoleBudget takes up to n errors from port's budget for this minute and
// says how many it may keep. Called under d.mu.
func (d *Daemon) consoleBudget(port, n int, now time.Time) int {
	if d.consoleRate == nil {
		d.consoleRate = map[int]consoleWindow{}
	}
	cw := d.consoleRate[port]
	if now.Sub(cw.start) >= time.Minute {
		cw = consoleWindow{start: now}
	}
	n = min(n, consoleMaxPerMin-cw.n)
	if n < 0 {
		n = 0
	}
	cw.n += n
	d.consoleRate[port] = cw
	return n
}

type consoleWindow struct {
	start time.Time
	n     int
}

func (d *Daemon) httpConsole(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL    string         `json:"url"`
		Errors []ConsoleError `json:"errors"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, consoleMaxBody)
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	in.URL = truncate(in.URL, consoleMaxField)
	in.Errors = limitConsole(in.Errors)
	port, _ := urlPort(in.URL)
	if port > 0 {
		d.mu.Lock()
		in.Errors = in.Errors[:d.consoleBudget(port, len(in.Errors), time.Now())]
		if len(in.Errors) == 0 {
			d.mu.Unlock()
			writeJSON(w, map[string]any{"ok": true, "dropped": true})
			return
		}
		for _, e := range in.Errors {
			e.URL = in.URL
			d.errors[port] = append(d.errors[port], e)
		}
		if n := len(d.errors[port]); n > 100 {
			d.errors[port] = d.errors[port][n-100:]
		}
		d.mu.Unlock()
		d.emit("browser.updated", d.browserTabs())
		d.catchConsole(port, in.URL, in.Errors)
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (d *Daemon) httpTabs(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Tabs []Tab `json:"tabs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	d.mu.Lock()
	d.tabs = in.Tabs
	d.mu.Unlock()
	d.emit("browser.updated", d.browserTabs())
	writeJSON(w, map[string]any{"ok": true})
}

func (d *Daemon) browserTabs() []Tab {
	d.mu.Lock()
	tabs := append([]Tab(nil), d.tabs...)
	errs := map[int]int{}
	for p, e := range d.errors {
		errs[p] = len(e)
	}
	d.mu.Unlock()
	out := []Tab{}
	for _, t := range tabs {
		t.Port, t.Path = urlPort(t.URL)
		if t.Port > 0 {
			if _, slug, ok := d.DB.PortOwner(t.Port); ok {
				t.Slug = slug
			}
		}
		t.Errors = errs[t.Port]
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

type portP struct {
	Port int    `json:"port"`
	Slug string `json:"slug"`
}

// hSendErrors sends every console error of a port at once (Browser panel E).
// Like a single error card, the list goes to a file and the agent gets one
// @ line; the cards of those errors are marked sent.
func (d *Daemon) hSendErrors(p portP) (any, error) {
	started := time.Now()
	d.mu.Lock()
	errs := append([]ConsoleError(nil), d.errors[p.Port]...)
	d.mu.Unlock()
	if len(errs) == 0 {
		return nil, errNoConsole
	}
	slug := p.Slug
	if slug == "" {
		_, s, ok := d.DB.PortOwner(p.Port)
		if !ok {
			return nil, fmt.Errorf("no session owns :%d", p.Port)
		}
		slug = s
	}
	s, err := d.snapshot(slug)
	if err != nil {
		return nil, err
	}
	instr := fmt.Sprintf("ブラウザ (:%d) で console error が %d 件出ています。原因を調べて直してください。", p.Port, len(errs))
	// the page wrote the messages and stacks: one fence it cannot close
	body := func(n int) string {
		var e strings.Builder
		for i, x := range errs {
			loc := x.Source
			if x.Line > 0 {
				loc = fmt.Sprintf("%s:%d", x.Source, x.Line)
			}
			if i > 0 {
				e.WriteString("\n")
			}
			fmt.Fprintf(&e, "- %s\n", strings.TrimSpace(untrusted.Line(x.Message)+" "+untrusted.Line(loc)))
			if x.URL != "" {
				fmt.Fprintf(&e, "  page: %s\n", untrusted.Line(x.URL))
			}
			if x.Stack != "" {
				fmt.Fprintf(&e, "  %s\n", strings.ReplaceAll(strings.TrimRight(truncate(x.Stack, 1200), "\n"), "\n", "\n  "))
			}
		}
		return fmt.Sprintf("# Console errors #%04d (browser :%d)\n\n%d errors\n\n", n, p.Port, len(errs)) +
			untrusted.Block("ブラウザのコンソールに出たエラー(メッセージ・場所・スタック)", "text", e.String()) + "\n"
	}
	if _, err := d.sendAttachment(slug, s.RepoID, body, instr); err != nil {
		return nil, err
	}
	// Only what went into the file is sent: errors the page logged while
	// the attachment was being pasted stay (and so do their cards).
	d.mu.Lock()
	sent := map[ConsoleError]int{}
	for _, e := range errs {
		sent[e]++
	}
	var kept []ConsoleError
	for _, e := range d.errors[p.Port] {
		if sent[e] > 0 {
			sent[e]--
			continue
		}
		kept = append(kept, e)
	}
	if len(kept) == 0 {
		delete(d.errors, p.Port)
	} else {
		d.errors[p.Port] = kept
	}
	d.mu.Unlock()
	d.emit("browser.updated", d.browserTabs())
	for _, r := range d.catch.MarkWhere(func(r *errcatch.Report) bool {
		return r.Slug == slug && r.Source == errcatch.SourceBrowser && r.Port == p.Port && !r.Last.After(started)
	}, errcatch.StatusSent, time.Now()) {
		d.emit("error.updated", errorEvent{Report: r})
	}
	return map[string]any{"sent": len(errs), "slug": slug}, nil
}
