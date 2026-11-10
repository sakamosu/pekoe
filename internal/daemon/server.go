// Package daemon is pekoed: the single owner of worktrees, sessions, state
// and review records. Every client (TUI, CLI, Neovim, Chrome, MCP) is a thin
// display layer over the JSON Lines API served here (see docs/api.md).
package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"pekoe/internal/config"
	"pekoe/internal/engine"
	"pekoe/internal/errcatch"
	"pekoe/internal/paths"
	"pekoe/internal/state"
)

// Version is set at build time with -ldflags "-X pekoe/internal/daemon.Version=…"
// (GoReleaser for releases, git describe in the Makefile); "dev" otherwise.
var Version = "dev"

type Request struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
	// Token is the human token (human.go), sent by the TUI, the CLI and
	// Neovim; the human-only methods are refused without it.
	Token string `json:"token,omitempty"`
}

type Response struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Result any             `json:"result"`
	Error  string          `json:"error,omitempty"`
}

type Event struct {
	Event string `json:"event"`
	Data  any    `json:"data"`
}

type conn struct {
	c      net.Conn
	mu     sync.Mutex
	subs   []string
	human  bool // a subscribe carried the human token: every event, not only agentEvents
	closed bool
}

func (c *conn) send(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.c.Write(append(b, '\n')); err != nil {
		c.closed = true
		c.c.Close()
	}
}

func (c *conn) wants(ev string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.human && !agentEvent(ev) {
		return false
	}
	for _, s := range c.subs {
		if s == "*" || strings.HasPrefix(ev, s) {
			return true
		}
	}
	return false
}

type Daemon struct {
	DB      *state.DB
	Eng     engine.Engine
	Global  *config.Global
	ln      net.Listener
	lock    *os.File // flock on pekoed.lock, held for the daemon's lifetime (lock.go)
	sockID  fileID   // the socket file this daemon created
	httpSrv *http.Server
	token   string
	web     webState // phone UI on the tailnet ([web], web.go / webts.go)
	authKey string   // TS_AUTHKEY, taken out of the environment (takeAuthKeyEnv)

	humanToken string // unlocks the human-only methods over the socket (human.go)

	mu          sync.Mutex
	runMu       sync.Mutex // run.start / run.close, one at a time (lifecycle.go)
	tapiMu      sync.Mutex // tapi.open / restart / reset, one at a time (tapi.go)
	tapi        tapiState  // tapi chat, in memory only (tapi.go; under mu)
	saveMu      sync.Mutex // orders session writes to state.db against rm (sessions.go persist)
	sessions    map[string]*state.Session
	conns       map[*conn]bool
	cancels     map[string]context.CancelFunc // creating sessions
	asks        map[string]string             // slug -> hunk id awaiting an answer
	tabs        []Tab
	errors      map[int][]ConsoleError // port -> recent errors
	consoleRate map[int]consoleWindow  // port -> errors taken this minute (http.go, under mu)
	out         map[string]*outState
	progress    map[string][]Step
	stop        chan struct{}
	stopOnce    sync.Once
	killAgents  atomic.Bool // daemon.stop {kill_agents}: also stop the tmux server
	handlers    map[string]func(json.RawMessage) (any, error)
	testHook    func(ev string, data any) // observes events in tests
	graphicsOK  bool                      // the attached TUI's terminal speaks Kitty graphics
	paneW       int                       // the TUI's agent pane size (last session.resize "*"),
	paneH       int                       // used when a client spawns without knowing it (CLI)
	preview     *previewer                // in-terminal browser preview (preview.go)
	cf          conflictScan              // conflict forecast (conflicts.go)

	prMu      sync.Mutex           // serializes PR summary updates
	prFetched map[string]time.Time // slug -> last PR fetch (under mu)
	prPolling atomic.Bool          // watchPRStatus is running

	catch      *errcatch.Store              // errors offered as cards (errcatch.go)
	devScan    map[string]*errcatch.Scanner // slug -> dev window scanner (output loop only)
	devFlushAt time.Time

	locateMiss map[string]time.Time // repo-ids knownRepos could not locate (repos.go), under mu

	issueCache map[string]issueCacheEntry // issues.list / issues.view answers (issues.go)
}

func New() (*Daemon, error) {
	if err := paths.Ensure(paths.DataDir(), paths.ConfigDir(), paths.ReposConfigDir()); err != nil {
		return nil, err
	}
	// files written by versions that used 0644 / 0755
	for _, dir := range []string{paths.DataDir(), paths.ConfigDir()} {
		if err := paths.Tighten(dir); err != nil {
			log.Printf("permissions of %s: %v", dir, err)
		}
	}
	g, err := config.LoadGlobal()
	if err != nil {
		return nil, err
	}
	// before state.db is opened and restored: a second pekoed must never
	// read or write it (issue #42)
	lock, err := acquireLock()
	if err != nil {
		return nil, err
	}
	db, err := state.Open(paths.StateDB())
	if err != nil {
		lock.Close()
		return nil, err
	}
	humanToken, err := loadOrCreateHumanToken()
	if err != nil {
		db.Close()
		lock.Close()
		return nil, fmt.Errorf("human token: %w", err)
	}
	d := &Daemon{
		DB: db, Global: g, lock: lock, authKey: takeAuthKeyEnv(), humanToken: humanToken,
		Eng:       newTmux(),
		sessions:  map[string]*state.Session{},
		conns:     map[*conn]bool{},
		cancels:   map[string]context.CancelFunc{},
		asks:      map[string]string{},
		errors:    map[int][]ConsoleError{},
		out:       map[string]*outState{},
		progress:  map[string][]Step{},
		stop:      make(chan struct{}),
		prFetched: map[string]time.Time{},
		catch:     errcatch.NewStore(),
		devScan:   map[string]*errcatch.Scanner{},
		tapi:      newTapiState(),
	}
	d.registerHandlers()
	return d, nil
}

func newTmux() *engine.Tmux {
	t := engine.NewTmux(paths.TmuxSocketName())
	t.PidFile = paths.TmuxPidFile(t.Socket)
	return t
}

// Run serves until Stop is called.
func (d *Daemon) Run() error {
	if err := d.Eng.Start(); err != nil {
		return fmt.Errorf("terminal engine: %w", err)
	}
	sock := paths.Socket()
	// The lock (New) rules out a second pekoed on this data directory; this
	// catches one on another data directory that shares $PEKOE_SOCK.
	if socketServes() {
		d.releaseLock()
		return ErrAlreadyRunning
	}
	_ = os.Remove(sock)
	_ = os.MkdirAll(filepath.Dir(sock), paths.DirPerm)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		d.releaseLock()
		return err
	}
	// Closing a UnixListener unlinks its path without checking whose file
	// is there now; shutdown removes it only while it is still ours.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	d.sockID, _ = statID(sock)
	_ = os.Chmod(sock, 0o600)
	d.ln = ln
	_ = os.WriteFile(paths.PidFile(), []byte(fmt.Sprint(os.Getpid())), paths.FilePerm)

	d.restore()
	go d.outputLoop()
	go d.tickLoop()
	if err := d.startHTTP(); err != nil {
		log.Printf("http: %v (extension disabled)", err)
	}
	d.startWeb() // no-op (tsnet untouched) unless [web] enable = true
	log.Printf("pekoed %s listening on %s", Version, sock)

	go func() {
		<-d.stop
		ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			select {
			case <-d.stop:
				d.shutdown()
				return nil
			default:
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			d.shutdown()
			return err
		}
		go d.serve(c)
	}
}

func (d *Daemon) Stop() { d.stopOnce.Do(func() { close(d.stop) }) }

func (d *Daemon) shutdown() {
	d.stopWeb()
	if d.httpSrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		d.httpSrv.Shutdown(ctx)
		cancel()
	}
	d.Eng.Close()            // agents keep running inside tmux …
	if d.killAgents.Load() { // … unless asked to end them (sandboxes)
		if err := d.Eng.KillServer(); err != nil {
			log.Printf("kill agents: %v", err)
		}
	}
	d.DB.Close()
	// Only what is still ours: a stale path must not take down the socket
	// or pid file of a daemon that started meanwhile (issue #42).
	if d.sockID != (fileID{}) {
		removeIfOwned(paths.Socket(), d.sockID)
	}
	removePidFileIfOwned()
	d.releaseLock() // last: the next pekoed may start from here on
}

func (d *Daemon) releaseLock() {
	if d.lock != nil {
		d.lock.Close()
		d.lock = nil
	}
}

func (d *Daemon) serve(nc net.Conn) {
	c := &conn{c: nc}
	d.mu.Lock()
	d.conns[c] = true
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.conns, c)
		d.mu.Unlock()
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		nc.Close()
	}()
	sc := bufio.NewScanner(nc)
	sc.Buffer(make([]byte, 64*1024), 64*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			c.send(Response{Error: "bad request: " + err.Error()})
			continue
		}
		if req.Method == "subscribe" {
			var p struct {
				Events []string `json:"events"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if len(p.Events) == 0 {
				p.Events = []string{"*"}
			}
			human := d.isHuman(req.Token)
			c.mu.Lock()
			c.subs = append(c.subs, p.Events...)
			c.human = c.human || human
			c.mu.Unlock()
			c.send(Response{ID: req.ID, Result: nil})
			continue
		}
		// Long operations must not block the connection's other requests.
		go func(req Request) {
			if err := d.authorize(req); err != nil {
				c.send(Response{ID: req.ID, Error: err.Error()})
				return
			}
			res, err := d.Call(req.Method, req.Params)
			r := Response{ID: req.ID, Result: res}
			if err != nil {
				r.Error = err.Error()
				r.Result = nil
			}
			c.send(r)
		}(req)
	}
}

// Call dispatches one API method (also used in-process by tests).
func (d *Daemon) Call(method string, params json.RawMessage) (res any, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic in %s: %v", method, r)
			err = fmt.Errorf("internal error in %s: %v", method, r)
		}
	}()
	h, ok := d.handlers[method]
	if !ok {
		return nil, fmt.Errorf("unknown method: %s", method)
	}
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	if params, err = d.normalizeSlugParam(params); err != nil {
		return nil, err
	}
	return h(params)
}

func (d *Daemon) emit(ev string, data any) {
	if d.testHook != nil {
		d.testHook(ev, data)
	}
	d.mu.Lock()
	cs := make([]*conn, 0, len(d.conns))
	for c := range d.conns {
		cs = append(cs, c)
	}
	d.mu.Unlock()
	msg := Event{Event: ev, Data: data}
	for _, c := range cs {
		if c.wants(ev) {
			go c.send(msg)
		}
	}
}

// bind decodes params into T and calls f.
func bind[T any](f func(T) (any, error)) func(json.RawMessage) (any, error) {
	return func(raw json.RawMessage) (any, error) {
		var p T
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("bad params: %w", err)
		}
		return f(p)
	}
}

type slugP struct {
	Slug string `json:"slug"`
}

func (d *Daemon) registerHandlers() {
	d.handlers = map[string]func(json.RawMessage) (any, error){
		"daemon.status": func(json.RawMessage) (any, error) {
			d.mu.Lock()
			n := len(d.sessions)
			d.mu.Unlock()
			return map[string]any{"pid": os.Getpid(), "version": Version, "sessions": n, "http_port": d.Global.HTTP.Port, "socket": paths.Socket()}, nil
		},
		"daemon.stop": func(raw json.RawMessage) (any, error) {
			var p struct {
				KillAgents bool `json:"kill_agents"`
			}
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &p)
			}
			if p.KillAgents {
				d.killAgents.Store(true)
			}
			go func() { time.Sleep(100 * time.Millisecond); d.Stop() }()
			return nil, nil
		},
		"ext.info": func(json.RawMessage) (any, error) {
			return map[string]any{"port": d.Global.HTTP.Port, "token": d.token}, nil
		},
		"web.status": func(json.RawMessage) (any, error) {
			st := d.web.get()
			st.Built = webBuilt
			if st.State == "" {
				st.State = "off"
			}
			return st, nil
		},
		"repo.list":       bind(d.hRepoList),
		"repo.resolve":    bind(d.hRepoResolve),
		"session.list":    bind(d.hList),
		"session.get":     bind(func(p slugP) (any, error) { return d.snapshot(p.Slug) }),
		"session.new":     bind(d.hNew),
		"session.cancel":  bind(d.hCancel),
		"session.open":    bind(d.hOpen),
		"session.adopt":   bind(d.hAdopt),
		"session.send":    bind(d.hSend),
		"session.keys":    bind(d.hKeys),
		"session.answer":  bind(d.hAnswer),
		"session.screen":  bind(d.hScreen),
		"session.resize":  bind(d.hResize),
		"session.seen":    bind(d.hSeen),
		"session.kill":    bind(d.hKill),
		"session.restart": bind(d.hRestart),
		"session.undo":    bind(d.hUndo),
		"session.event":   bind(d.hEvent),
		"session.progress": bind(func(p slugP) (any, error) {
			d.mu.Lock()
			defer d.mu.Unlock()
			return map[string]any{"slug": p.Slug, "steps": d.progress[p.Slug]}, nil
		}),
		"attention.next":        bind(d.hAttentionNext),
		"review.next":           bind(d.hReviewNext),
		"worktree.list":         bind(d.hWorktreeList),
		"worktree.rm":           bind(d.hRm),
		"land":                  bind(d.hLand),
		"dev.toggle":            bind(d.hDevToggle),
		"run.start":             bind(d.hRunStart),
		"run.close":             bind(d.hRunClose),
		"changes":               bind(d.hChanges),
		"conflicts.list":        bind(d.hConflicts),
		"review.start":          bind(d.hReviewStart),
		"review.end":            bind(d.hReviewEnd),
		"review.files":          bind(d.hReviewFiles),
		"review.approve":        bind(func(p hunkP) (any, error) { return d.setHunk(p, "approved") }),
		"review.unapprove":      bind(func(p hunkP) (any, error) { return d.setHunk(p, "") }),
		"review.reject":         bind(d.hReject),
		"review.comment":        bind(d.hComment),
		"review.comment.delete": bind(d.hCommentDelete),
		"review.ask":            bind(d.hAsk),
		"review.flush":          bind(d.hFlush),
		"review.risks":          bind(func(p slugP) (any, error) { return d.DB.Risks(p.Slug) }),
		"review.pr":             bind(d.hReviewPR),
		"pr.view":               bind(d.hPRView),
		"pr.seen":               bind(d.hPRSeen),
		"pr.failed_log":         bind(d.hFailedLog),
		"pick.list":             bind(d.hPickList),
		"pick.send":             bind(d.hPickSend),
		"pick.discard":          bind(d.hPickDiscard),
		"browser.tabs":          func(json.RawMessage) (any, error) { return d.browserTabs(), nil },
		"browser.send_errors":   bind(d.hSendErrors),
		"error.list":            bind(d.hErrorList),
		"error.get":             bind(d.hErrorGet),
		"error.send":            bind(d.hErrorSend),
		"error.ignore":          bind(d.hErrorIgnore),
		"editor.open":           bind(d.hEditorOpen),
		"init.detect":           bind(d.hInitDetect),
		"init.write":            bind(d.hInitWrite),
		"review.surface":        bind(d.hReviewSurface),
		"window.kill":           bind(d.hWindowKill),
		"term.caps":             bind(d.hTermCaps),
		"issues.list":           bind(d.hIssuesList),
		"issues.view":           bind(d.hIssuesView),
		"tapi.get":              func(json.RawMessage) (any, error) { return d.tapiView(), nil },
		"tapi.open":             bind(d.hTapiOpen),
		"tapi.restart":          bind(d.hTapiRestart),
		"tapi.reset":            bind(d.hTapiReset),
		"tapi.screen":           bind(d.hTapiScreen),
		"tapi.send":             bind(d.hTapiSend),
		"tapi.keys":             bind(d.hTapiKeys),
		"tapi.resize":           bind(d.hTapiResize),
	}
	for k, h := range d.previewHandlers() {
		d.handlers[k] = h
	}
}
