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

	"boba/internal/paths"
	"boba/internal/state"
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
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", d.Global.HTTP.Port))
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
	if err := os.MkdirAll(dir, 0o755); err != nil {
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
				if os.WriteFile(base+".png", b, 0o644) == nil {
					pk.Image = base + ".png"
					pk.ImageSize = pngSize(b)
				}
			}
		}
	}
	if err := os.WriteFile(pk.File, []byte(pickMarkdown(n, pk, comp, srcLoc)), 0o644); err != nil {
		return nil, err
	}
	if err := d.DB.AddPick(pk); err != nil {
		return nil, err
	}
	d.emit("pick.received", pk)
	if slug != "" && d.Global.AutoSendOn() && strings.TrimSpace(in.Instruction) != "" {
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

func pickMarkdown(n int, p *state.Pick, comp component, src string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Pick #%04d\n\n", n)
	fmt.Fprintf(&b, "- page: %s\n", p.URL)
	if p.Title != "" {
		fmt.Fprintf(&b, "- title: %s\n", p.Title)
	}
	if p.Selector != "" {
		fmt.Fprintf(&b, "- selector: `%s`\n", p.Selector)
	}
	if comp.Name != "" {
		fmt.Fprintf(&b, "- component: `<%s>` (%s", comp.Name, comp.Framework)
		if comp.Via != "" {
			fmt.Fprintf(&b, ", via %s", comp.Via)
		}
		b.WriteString(")\n")
	}
	if src != "" {
		fmt.Fprintf(&b, "- source: %s\n", src)
	}
	if p.Image != "" {
		fmt.Fprintf(&b, "- screenshot: %s (%s)\n", p.Image, p.ImageSize)
	}
	if len(p.Styles) > 0 && string(p.Styles) != "null" {
		var st map[string]string
		if json.Unmarshal(p.Styles, &st) == nil && len(st) > 0 {
			keys := make([]string, 0, len(st))
			for k := range st {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			b.WriteString("\n## computed styles\n\n")
			for _, k := range keys {
				fmt.Fprintf(&b, "- %s: %s\n", k, st[k])
			}
		}
	}
	if p.OuterHTML != "" {
		fmt.Fprintf(&b, "\n## outerHTML\n\n```html\n%s\n```\n", p.OuterHTML)
	}
	if p.Instruction != "" {
		fmt.Fprintf(&b, "\n## instruction\n\n%s\n", p.Instruction)
	}
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

func (d *Daemon) httpConsole(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL    string         `json:"url"`
		Errors []ConsoleError `json:"errors"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	port, _ := urlPort(in.URL)
	if port > 0 {
		d.mu.Lock()
		for _, e := range in.Errors {
			e.URL = in.URL
			d.errors[port] = append(d.errors[port], e)
		}
		if n := len(d.errors[port]); n > 100 {
			d.errors[port] = d.errors[port][n-100:]
		}
		d.mu.Unlock()
		d.emit("browser.updated", d.browserTabs())
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

func (d *Daemon) hSendErrors(p portP) (any, error) {
	d.mu.Lock()
	errs := append([]ConsoleError(nil), d.errors[p.Port]...)
	d.mu.Unlock()
	if len(errs) == 0 {
		return nil, errors.New("no console errors")
	}
	slug := p.Slug
	if slug == "" {
		_, s, ok := d.DB.PortOwner(p.Port)
		if !ok {
			return nil, fmt.Errorf("no session owns :%d", p.Port)
		}
		slug = s
	}
	var b strings.Builder
	fmt.Fprintf(&b, "ブラウザ (:%d) で console error が %d 件出ています。原因を調べて直してください。\n", p.Port, len(errs))
	for _, e := range errs {
		loc := e.Source
		if e.Line > 0 {
			loc = fmt.Sprintf("%s:%d", e.Source, e.Line)
		}
		fmt.Fprintf(&b, "\n- %s %s", e.Message, loc)
		if e.Stack != "" {
			fmt.Fprintf(&b, "\n  %s", strings.ReplaceAll(truncate(e.Stack, 600), "\n", "\n  "))
		}
	}
	if _, err := d.hSend(sendP{Slug: slug, Text: b.String(), Submit: true}); err != nil {
		return nil, err
	}
	d.mu.Lock()
	delete(d.errors, p.Port)
	d.mu.Unlock()
	d.emit("browser.updated", d.browserTabs())
	return map[string]any{"sent": len(errs), "slug": slug}, nil
}
