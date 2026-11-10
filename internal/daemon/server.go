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
	"pekoe/internal/gitx"
	"pekoe/internal/paths"
	"pekoe/internal/sandbox"
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
	// Session is the client's $PEKOE_SESSION: what it says it is. Kept as
	// Caller.Claimed only; who called is told from the process (caller.go).
	Session string `json:"session,omitempty"`
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

// connQueue is how many messages may wait for a connection's writer.
const connQueue = 4096

// conn is one client connection. Everything written to it goes through one
// queue and one writer goroutine, so messages arrive in the order they were
// queued (a goroutine per message let two session.updated swap, leaving the
// client on the older state).
type conn struct {
	c      net.Conn
	mu     sync.Mutex // subs, human, closed
	subs   []string
	human  bool // a subscribe carried the human token: every event, not only agentEvents
	closed bool
	out    chan []byte
	done   chan struct{} // closed with the connection
}

func newConn(nc net.Conn) *conn {
	c := &conn{c: nc, out: make(chan []byte, connQueue), done: make(chan struct{})}
	go c.writer()
	return c
}

func (c *conn) writer() {
	for {
		select {
		case <-c.done:
			return
		case b := <-c.out:
			c.c.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := c.c.Write(b); err != nil {
				c.shut()
				return
			}
		}
	}
}

// shut closes the connection (once).
func (c *conn) shut() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		close(c.done)
		c.c.Close()
	}
}

// send queues a response; it waits for room (a client that reads nothing
// for 5 seconds is dropped, as a blocked write was before).
func (c *conn) send(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	b = append(b, '\n')
	select {
	case c.out <- b:
	case <-c.done:
	case <-time.After(5 * time.Second):
		c.shut()
	}
}

// event queues an event line; when the queue is full the event is dropped
// (the client drops events it cannot keep up with too, and the periodic
// session.updated catches up).
func (c *conn) event(b []byte) {
	select {
	case c.out <- b:
	case <-c.done:
	default:
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
	DB     *state.DB
	Eng    engine.Engine
	Global *config.Global
	// sbx wraps the commands that come from a worktree (internal/sandbox)
	sbx     *sandbox.Sandbox
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
	evMu        sync.Mutex // held from a change under mu to its emit, so events go out in change order
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
	// callerHandlers are the methods that need to know who called
	// (caller.go); each is in handlers too, called as a person there.
	callerHandlers map[string]func(*Caller, json.RawMessage) (any, error)
	// peerPID tells a connection's process (caller.go); tests swap it.
	peerPID  func(net.Conn) int
	changes  *changeLog                // what wait waits for (wait.go)
	testHook func(ev string, data any) // observes events in tests
	// portBusy says whether a session port is taken (AllocPort); nil is
	// portBusy, which binds the port. Tests swap in one that does not bind
	// (an agent's sandbox refuses every bind).
	portBusy   func(int) bool
	graphicsOK bool         // the attached TUI's terminal speaks Kitty graphics
	paneW      int          // the TUI's agent pane size (last session.resize "*"),
	paneH      int          // used when a client spawns without knowing it (CLI)
	preview    *previewer   // in-terminal browser preview (preview.go)
	cf         conflictScan // conflict forecast (conflicts.go)

	prMu      sync.Mutex           // serializes PR summary updates
	prFetched map[string]time.Time // slug -> last PR fetch (under mu)
	prPolling atomic.Bool          // watchPRStatus is running
	finishing sync.Map             // slug -> finishLanding is running for it
	sendLocks sync.Map             // slug -> *sync.Mutex: one session.send at a time (sessions.go)
	tests     testRuns             // [hooks] test runs (hooks.go)

	catch      *errcatch.Store              // errors offered as cards (errcatch.go)
	devScan    map[string]*errcatch.Scanner // slug -> dev window scanner (output loop only)
	devFlushAt time.Time

	locateMiss map[string]time.Time // repo-ids knownRepos could not locate (repos.go), under mu

	issueCache map[string]issueCacheEntry // issues.list / issues.view answers (issues.go)

	// the inbox (inbox.go)
	inboxMu      sync.Mutex           // reads and writes of an item, one at a time
	ledgerMu     sync.Mutex           // changes of a ledger entry, one at a time (ledger.go)
	inboxDeliver sync.Map             // asker -> *sync.Mutex: one delivery at a time
	inboxTick    atomic.Bool          // deliverAllInbox is running
	typedAt      map[string]time.Time // slug -> a person last typed into its agent window (under mu)
	webBases     sync.Map             // repo dir -> https://host/owner/name ("" none)
	mergeTick    atomic.Bool          // checkMerges is running (merge.go)
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
		sbx:       sandbox.New(sandbox.DefaultPolicy(g.Web.StateDir), g.Sandbox.On()),
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
		peerPID:   peerPID,
		changes:   newChangeLog(),
		typedAt:   map[string]time.Time{},
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
	// every git pekoed runs, too: the repository's config and attributes
	// (and a repository an agent made and passed to pekoe new --repo) can
	// make git run commands (gitx/guard.go)
	gitx.SetSandbox(d.sbx.Wrap)
	defer gitx.SetSandbox(nil)
	if !d.sbx.Enabled {
		log.Printf("sandbox: off ([sandbox] enable = false): hooks, the gatekeeper and git run the worktree's code without one")
	} else if err := d.sbx.Check(); err != nil {
		log.Printf("sandbox: %v", err)
	} else {
		log.Printf("sandbox: %s", d.sbx.Mechanism())
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
	c := newConn(nc)
	pr := &peer{pid: d.peerPID(nc), find: d.paneOf}
	d.mu.Lock()
	d.conns[c] = true
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.conns, c)
		d.mu.Unlock()
		c.shut()
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
		who := &Caller{human: d.isHuman(req.Token), Claimed: req.Session, Via: viaSocket, peer: pr, done: c.done}
		go func(req Request) {
			if err := d.authorize(req, who); err != nil {
				c.send(Response{ID: req.ID, Error: err.Error()})
				return
			}
			res, err := d.call(req.Method, req.Params, who)
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
func (d *Daemon) Call(method string, params json.RawMessage) (any, error) {
	return d.call(method, params, humanCaller(""))
}

// CallAs is Call on behalf of who (the Web UI says it is the web).
func (d *Daemon) CallAs(who *Caller, method string, params json.RawMessage) (any, error) {
	return d.call(method, params, who)
}

// call dispatches a request from who; a caller without the human token
// got here through authorize.
func (d *Daemon) call(method string, params json.RawMessage, who *Caller) (res any, err error) {
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
	agent := !who.Human()
	switch {
	case agent && method == "land":
		h = bind(d.hAgentLand)
	case agent && method == "worktree.rm":
		h = bind(d.hAgentRm)
	case agent && method == "conflicts.list":
		h = bind(d.hAgentConflicts)
	case agent && method == "dev.toggle":
		h = bind(func(p slugP) (any, error) { return d.hAgentDevToggle(who, p) })
	}
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	if params, err = d.normalizeSlugParam(params); err != nil {
		return nil, err
	}
	if ch, ok := d.callerHandlers[method]; ok {
		return ch(who, params)
	}
	return h(params)
}

func (d *Daemon) emit(ev string, data any) {
	if d.testHook != nil {
		d.testHook(ev, data)
	}
	if ev == "inbox.updated" {
		// Tapi.inbox_open may have changed
		defer d.emit("tapi.updated", d.tapiView())
	}
	if s, ok := data.(*state.Session); ok && ev == "session.removed" && s != nil {
		d.changes.forget(s.Slug)
		d.changes.add(Change{Kind: kindRemoved, Slug: s.Slug, Summary: s.Slug + ": removed", parent: s.Parent})
	}
	d.mu.Lock()
	cs := make([]*conn, 0, len(d.conns))
	for c := range d.conns {
		cs = append(cs, c)
	}
	d.mu.Unlock()
	var line []byte
	for _, c := range cs {
		if !c.wants(ev) {
			continue
		}
		if line == nil {
			b, err := json.Marshal(Event{Event: ev, Data: data})
			if err != nil {
				return
			}
			line = append(b, '\n')
		}
		c.event(line)
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
		"repo.list":      bind(d.hRepoList),
		"repo.resolve":   bind(d.hRepoResolve),
		"session.list":   bind(d.hList),
		"session.get":    bind(func(p slugP) (any, error) { return d.snapshot(p.Slug) }),
		"session.cancel": bind(d.hCancel),
		"session.open":   bind(d.hOpen),
		"session.adopt":  bind(d.hAdopt),
		"session.keys": bind(func(p keysP) (any, error) {
			if win(p.Window) == engine.WinAgent {
				d.noteTyped(p.Slug)
			}
			return d.hKeys(p)
		}),
		"session.answer":  bind(d.hAnswer),
		"session.screen":  bind(d.hScreen),
		"session.resize":  bind(d.hResize),
		"session.seen":    bind(d.hSeen),
		"session.kill":    bind(d.hKill),
		"session.restart": bind(d.hRestart),
		"session.undo":    bind(d.hUndo),
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
		"merge.check":           bind(d.hMergeCheck),
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
		"tapi.send":             bind(func(p tapiSendP) (any, error) { d.noteTyped(TapiSlug); return d.hTapiSend(p) }),
		"tapi.keys":             bind(func(p keysP) (any, error) { d.noteTyped(TapiSlug); return d.hTapiKeys(p) }),
		"tapi.resize":           bind(d.hTapiResize),
		"ledger.reopen":         bind(d.hLedgerReopen),
		"ledger.delete":         bind(d.hLedgerDelete),
	}
	for k, h := range d.previewHandlers() {
		d.handlers[k] = h
	}
	d.callerHandlers = map[string]func(*Caller, json.RawMessage) (any, error){
		"session.new":    bindAs(d.hNewAs),
		"session.send":   bindAs(d.hSendAs),
		"session.event":  bindAs(d.hEventAs),
		"report.post":    bindAs(d.hReportPost),
		"report.list":    bindAs(d.hReportList),
		"wait":           bindAs(d.hWait),
		"send.log":       bindAs(d.hSendLog),
		"inbox.post":     bindAs(d.hInboxPost),
		"inbox.list":     bindAs(d.hInboxList),
		"inbox.get":      bindAs(d.hInboxGet),
		"inbox.withdraw": bindAs(d.hInboxWithdraw),
		"inbox.answer":   bindAs(d.hInboxAnswer),
		"inbox.dismiss":  bindAs(d.hInboxDismiss),
		"ledger.list":    bindAs(d.hLedgerList),
		"ledger.close":   bindAs(d.hLedgerClose),
		"ledger.link":    bindAs(d.hLedgerLink),
	}
	for k, h := range d.callerHandlers {
		h := h
		d.handlers[k] = func(raw json.RawMessage) (any, error) { return h(humanCaller(""), raw) }
	}
}

// bindAs is bind for a handler that needs its caller.
func bindAs[T any](f func(*Caller, T) (any, error)) func(*Caller, json.RawMessage) (any, error) {
	return func(who *Caller, raw json.RawMessage) (any, error) {
		var p T
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("bad params: %w", err)
		}
		return f(who, p)
	}
}
