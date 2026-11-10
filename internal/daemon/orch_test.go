package daemon

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pekoe/internal/state"
	"pekoe/internal/testenv"
)

// childOfTest starts a process whose parent is the test process, standing
// in for an agent's command inside a pekoe window whose root is the test.
func childOfTest(t *testing.T) int {
	t.Helper()
	c := exec.Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Process.Kill(); c.Wait() })
	return c.Process.Pid
}

// agentAs is a caller without the human token in slug's window.
func agentAs(slug string) *Caller { return &Caller{Via: viaSocket, peer: fixedPeer(slug)} }

func callAs[T any](t *testing.T, d *Daemon, who *Caller, method string, params any) T {
	t.Helper()
	b, _ := json.Marshal(params)
	res, err := d.CallAs(who, method, b)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	var out T
	rb, _ := json.Marshal(res)
	json.Unmarshal(rb, &out)
	return out
}

func callAsErr(d *Daemon, who *Caller, method string, params any) error {
	b, _ := json.Marshal(params)
	_, err := d.CallAs(who, method, b)
	return err
}

// rawCall sends one request as given (extra fields such as session) over
// the pipe and returns its result and error.
func (c *sockClient) rawCall(req map[string]any) (json.RawMessage, string) {
	c.t.Helper()
	c.id++
	req["id"] = c.id
	b, _ := json.Marshal(req)
	if _, err := c.nc.Write(append(b, '\n')); err != nil {
		c.t.Fatal(err)
	}
	for c.sc.Scan() {
		var r struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  string          `json:"error"`
		}
		if json.Unmarshal(c.sc.Bytes(), &r) == nil && r.ID == c.id {
			return r.Result, r.Error
		}
	}
	c.t.Fatal("connection closed")
	return nil, ""
}

// The window is found from the process tree: a child of a window's root
// is that window's session; a process outside every window is no one.
func TestPaneOfWalksParents(t *testing.T) {
	d, fe, _ := setup(t)
	fe.panes["feat-x~agent"] = os.Getpid()
	fe.panes["+tapi~agent"] = 999999
	if got := d.paneOf(childOfTest(t)); got != "feat-x" {
		t.Fatalf("child of the window: %q", got)
	}
	if got := d.paneOf(os.Getpid()); got != "feat-x" {
		t.Fatalf("the window's root: %q", got)
	}
	if got := d.paneOf(os.Getppid()); got != "" {
		t.Fatalf("outside every window: %q", got)
	}
	fe.panes = map[string]int{}
	if got := d.paneOf(os.Getpid()); got != "" {
		t.Fatalf("no panes: %q", got)
	}
}

// Completion criterion of stage 1: a report.post from a process in a
// session's window goes to that session, whatever slug it claims, and one
// from outside every window is refused.
func TestReportGoesToCallersWindow(t *testing.T) {
	d, fe, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	call[state.Session](t, d, "session.open", map[string]any{"path": gitRepo(t, "x", "other"), "resume": "new"})
	fe.panes["main~agent"] = os.Getpid()
	pid := childOfTest(t)
	d.peerPID = func(net.Conn) int { return pid }

	c := dialPipe(t, d)
	res, e := c.rawCall(map[string]any{"method": "report.post", "session": "other", "params": map[string]any{"summary": "直した\n詳細", "status": "done", "evidence": []string{"https://example.com/pr/1", "/tmp/shot.png"}}})
	if e != "" {
		t.Fatal(e)
	}
	var r state.Report
	json.Unmarshal(res, &r)
	if r.Slug != "main" || r.By != "main" || r.Claimed != "other" || r.ID == 0 || len(r.Evidence) != 2 {
		t.Fatalf("report: %+v", r)
	}
	if r.Head == "" || r.Dirty {
		t.Fatalf("head / dirty read by pekoed: %+v", r)
	}
	s := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"})
	if s.Report == nil || s.Report.ID != r.ID || s.Report.Summary != "直した" || s.Report.Status != "done" {
		t.Fatalf("Session.report: %+v", s.Report)
	}
	// naming another session in params does not move it there
	if _, e := c.rawCall(map[string]any{"method": "report.post", "params": map[string]any{"slug": "other", "summary": "x", "status": "done"}}); !strings.Contains(e, "only on itself") {
		t.Fatalf("slug param: %q", e)
	}
	// a process in no pekoe window
	d.peerPID = func(net.Conn) int { return os.Getppid() }
	c2 := dialPipe(t, d)
	if _, e := c2.rawCall(map[string]any{"method": "report.post", "session": "main", "params": map[string]any{"summary": "x", "status": "done"}}); !strings.Contains(e, "cannot tell which session") {
		t.Fatalf("unknown caller: %q", e)
	}
	if rs := call[[]state.Report](t, d, "report.list", map[string]any{"slug": "main"}); len(rs) != 1 {
		t.Fatalf("report.list: %+v", rs)
	}
}

func TestReportLimits(t *testing.T) {
	d, _, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	who := agentAs("main")
	for _, bad := range []map[string]any{
		{"summary": "x", "status": "finished"},
		{"summary": "  ", "status": "done"},
		{"summary": strings.Repeat("あ", 4001), "status": "done"},
		{"summary": "x", "status": "done", "evidence": make([]string, 21)},
		{"summary": "x", "status": "done", "evidence": []string{strings.Repeat("a", 501)}},
		{"summary": "x", "status": "review"},
		{"summary": "x", "status": "done", "verdict": "pass"},
		{"summary": "x", "status": "review", "verdict": "pass", "reviewed_sha": "not-a-sha"},
	} {
		if err := callAsErr(d, who, "report.post", bad); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	r := callAs[state.Report](t, d, who, "report.post", map[string]any{"summary": "ok\x1b[31m\x07 red\r\nnext", "status": "review", "verdict": "pass", "reviewed_sha": "ABCDEF1", "evidence": []string{"a\nb", ""}})
	if r.Summary != "ok[31m red\nnext" || r.ReviewedSHA != "abcdef1" || len(r.Evidence) != 1 || r.Evidence[0] != "ab" {
		t.Fatalf("cleaned: %+v", r)
	}
	if err := callAsErr(d, agentAs(TapiSlug), "report.post", map[string]any{"summary": "x", "status": "done"}); err == nil {
		t.Fatal("tapi is not a session")
	}
	// rm takes the reports with it
	call[any](t, d, "worktree.rm", map[string]any{"slug": "main", "force": true})
	if rs := call[[]state.Report](t, d, "report.list", map[string]any{"slug": "main"}); len(rs) != 0 {
		t.Fatalf("left after rm: %+v", rs)
	}
}

type waitRes struct {
	Cursor   string   `json:"cursor"`
	Epoch    string   `json:"epoch"`
	Changes  []Change `json:"changes"`
	TimedOut bool     `json:"timed_out"`
	Reset    bool     `json:"reset"`
}

func (w waitRes) kinds() string {
	var ks []string
	for _, c := range w.Changes {
		ks = append(ks, c.Slug+":"+c.Kind)
	}
	return strings.Join(ks, " ")
}

// Completion criterion: wait --since misses nothing (what happened after a
// wait returned comes with the next one), and after a restart says reset.
func TestWaitSinceMissesNothing(t *testing.T) {
	d, _, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	d.update("main", func(s *state.Session) { s.Parent = TapiSlug })
	tapi := agentAs(TapiSlug)

	// blocks until something happens
	got := make(chan waitRes, 1)
	go func() {
		got <- callAs[waitRes](t, d, tapi, "wait", map[string]any{"mine": true, "timeout_s": 10})
	}()
	time.Sleep(100 * time.Millisecond)
	select {
	case w := <-got:
		t.Fatalf("returned before any change: %+v", w)
	default:
	}
	event(t, d, "UserPromptSubmit", "main", map[string]any{"prompt": "go"})
	stop(t, d, "main", map[string]any{})
	var w1 waitRes
	select {
	case w1 = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not return")
	}
	if len(w1.Changes) == 0 || w1.Changes[len(w1.Changes)-1].Kind != kindDone || !strings.Contains(w1.Changes[len(w1.Changes)-1].Summary, "no report") {
		t.Fatalf("done without a report: %+v", w1)
	}

	// changes between two waits come with the next one, right away
	event(t, d, "UserPromptSubmit", "main", map[string]any{"prompt": "again"})
	callAs[state.Report](t, d, agentAs("main"), "report.post", map[string]any{"summary": "done it", "status": "done"})
	stop(t, d, "main", map[string]any{})
	w2 := callAs[waitRes](t, d, tapi, "wait", map[string]any{"mine": true, "since": w1.Cursor, "on": []string{"report", "done"}, "timeout_s": 1})
	if w2.TimedOut || w2.kinds() != "main:report main:done" {
		t.Fatalf("after the cursor: %+v", w2)
	}
	if last := w2.Changes[1]; strings.Contains(last.Summary, "no report") {
		t.Fatalf("done after a report: %q", last.Summary)
	}
	// nothing new: times out with the same place to go on from
	w3 := callAs[waitRes](t, d, tapi, "wait", map[string]any{"mine": true, "since": w2.Cursor, "timeout_s": 1})
	if !w3.TimedOut || len(w3.Changes) != 0 || w3.Cursor != w2.Cursor {
		t.Fatalf("timeout: %+v", w3)
	}
	// someone else's session is not mine
	call[state.Session](t, d, "session.open", map[string]any{"path": gitRepo(t, "x", "other"), "resume": "new"})
	other := ""
	for _, s := range call[[]state.Session](t, d, "session.list", nil) {
		if s.Slug != "main" {
			other = s.Slug
		}
	}
	d.setState(other, state.Done, "")
	if w := callAs[waitRes](t, d, tapi, "wait", map[string]any{"mine": true, "since": w3.Cursor, "timeout_s": 1}); !w.TimedOut {
		t.Fatalf("not mine: %+v", w)
	}
	if w := callAs[waitRes](t, d, tapi, "wait", map[string]any{"slugs": []string{other}, "since": w3.Cursor, "timeout_s": 1}); w.kinds() != other+":done" {
		t.Fatalf("by slug: %+v", w)
	}

	// pekoed restarted: another epoch
	d.changes = newChangeLog()
	if w := callAs[waitRes](t, d, tapi, "wait", map[string]any{"since": w3.Cursor, "timeout_s": 1}); !w.Reset || w.Epoch == w1.Epoch {
		t.Fatalf("after a restart: %+v", w)
	}
	if err := callAsErr(d, tapi, "wait", map[string]any{"since": "1532"}); err == nil {
		t.Fatal("a bare number is no cursor")
	}
	if err := callAsErr(d, tapi, "wait", map[string]any{"on": []string{"nope"}}); err == nil {
		t.Fatal("unknown kind")
	}
	if err := callAsErr(d, tapi, "wait", map[string]any{"timeout_s": 3601}); err == nil {
		t.Fatal("over an hour")
	}
	if err := callAsErr(d, &Caller{peer: fixedPeer("")}, "wait", map[string]any{"mine": true}); err == nil {
		t.Fatal("mine for an unknown caller")
	}
}

// Changes that fell out of the ring cannot be given: reset.
func TestWaitRingOverflowResets(t *testing.T) {
	d, _, _ := setup(t)
	d.changes.add(Change{Kind: kindDone, Slug: "a"})
	cur := d.changes.cursorLocked()
	for i := 0; i < changeRing+5; i++ {
		d.changes.add(Change{Kind: kindDone, Slug: "b"})
	}
	if w := call[waitRes](t, d, "wait", map[string]any{"since": cur, "timeout_s": 1}); !w.Reset {
		t.Fatalf("overflow: %+v", len(w.Changes))
	}
}

func TestWaitsPerCaller(t *testing.T) {
	d, _, _ := setup(t)
	gone := make(chan struct{})
	who := &Caller{peer: fixedPeer(TapiSlug), done: gone}
	errs := make(chan error, waitsPerCaller)
	for i := 0; i < waitsPerCaller; i++ {
		go func() { errs <- callAsErr(d, who, "wait", map[string]any{"timeout_s": 60}) }()
	}
	for i := 0; ; i++ {
		d.changes.mu.Lock()
		n := d.changes.waiting[TapiSlug]
		d.changes.mu.Unlock()
		if n == waitsPerCaller {
			break
		}
		if i > 200 {
			t.Fatalf("waits open: %d", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := callAsErr(d, agentAs(TapiSlug), "wait", map[string]any{"timeout_s": 1}); err == nil || !strings.Contains(err.Error(), "already has") {
		t.Fatalf("ninth wait: %v", err)
	}
	// another caller is not limited by tapi's
	if err := callAsErr(d, agentAs("x"), "wait", map[string]any{"timeout_s": 1}); err != nil {
		t.Fatal(err)
	}
	close(gone) // the connection went: the waits end
	for i := 0; i < waitsPerCaller; i++ {
		select {
		case err := <-errs:
			if err == nil {
				t.Fatal("a wait of a closed connection returned a result")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("waits did not end with the connection")
		}
	}
}

// session.new records the caller as the parent and the prompt in the send
// log; session.send instructions are logged with their sender, the TUI's
// typing is not; without the token, send.log shows only the caller's own.
func TestParentAndSendLog(t *testing.T) {
	d, _, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	s := callAs[state.Session](t, d, agentAs(TapiSlug), "session.new", map[string]any{"repo": repo, "branch": "feat/csv", "prompt": "CSV の空行を飛ばす", "from": "gh#12"})
	if s.Parent != TapiSlug {
		t.Fatalf("parent: %+v", s)
	}
	for i := 0; i < 200; i++ {
		if g := call[state.Session](t, d, "session.get", map[string]any{"slug": s.Slug}); g.State != state.Creating {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if h := call[state.Session](t, d, "session.new", map[string]any{"repo": repo, "branch": "feat/h"}); h.Parent != callerHuman {
		t.Fatalf("a person's session: %+v", h)
	}
	call[any](t, d, "session.send", map[string]any{"slug": "main", "text": "human typing"})               // no submit: not logged
	call[any](t, d, "session.send", map[string]any{"slug": "main", "text": "please fix", "submit": true}) // logged
	callAs[any](t, d, agentAs(TapiSlug), "session.send", map[string]any{"slug": "main", "text": "from tapi", "submit": true})
	if err := callAsErr(d, agentAs("main"), "session.send", map[string]any{"slug": "nope", "text": "x", "submit": true}); err == nil {
		t.Fatal("send to no session")
	}

	all := call[[]state.SendRecord](t, d, "send.log", nil)
	var got []string
	for _, r := range all {
		got = append(got, r.From+">"+r.To+":"+r.Kind)
	}
	want := "main>nope:send +tapi>main:send human>main:send human>feat-h:new +tapi>feat-csv:new"
	if strings.Join(got, " ") != want {
		t.Fatalf("send log:\n%s\nwant\n%s", strings.Join(got, " "), want)
	}
	if all[0].OK || all[0].Error == "" || !all[1].OK {
		t.Fatalf("ok / error: %+v", all[:2])
	}
	if last := all[len(all)-1]; last.Text != "--from gh#12\nCSV の空行を飛ばす" || last.Via != viaSocket {
		t.Fatalf("new's text: %+v", last)
	}
	mine := callAs[[]state.SendRecord](t, d, agentAs(TapiSlug), "send.log", map[string]any{})
	if len(mine) != 2 {
		t.Fatalf("tapi's own: %+v", mine)
	}
	for _, r := range mine {
		if r.From != TapiSlug && r.To != TapiSlug {
			t.Fatalf("someone else's record: %+v", r)
		}
	}
	if err := callAsErr(d, &Caller{peer: fixedPeer("")}, "send.log", nil); err == nil {
		t.Fatal("unknown caller")
	}
}

// A session.send that authorize refuses (it starts with a digit) is logged
// as not sent.
func TestRefusedSendIsLogged(t *testing.T) {
	d, fe, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	fe.panes["+tapi~agent"] = os.Getpid()
	pid := childOfTest(t)
	d.peerPID = func(net.Conn) int { return pid }
	c := dialPipe(t, d)
	if e := c.call("session.send", map[string]any{"slug": "main", "text": "2 then", "submit": true}, ""); e == "" {
		t.Fatal("digit accepted")
	}
	rs := call[[]state.SendRecord](t, d, "send.log", nil)
	if len(rs) != 1 || rs[0].From != TapiSlug || rs[0].OK || rs[0].Text != "2 then" {
		t.Fatalf("refused send: %+v", rs)
	}
}

// A hook from a session's window is that session, whatever
// $PEKOE_SESSION says.
func TestHookFromWindow(t *testing.T) {
	d, _, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	callAs[any](t, d, &Caller{human: true, peer: fixedPeer("main")}, "session.event", map[string]any{"hook": "UserPromptSubmit", "session": "bogus", "payload": map[string]any{"prompt": "x", "cwd": "/"}})
	if s := call[state.Session](t, d, "session.get", map[string]any{"slug": "main"}); s.State != state.Running {
		t.Fatalf("hook from main's window: %+v", s.State)
	}
}

func TestReportPostedReachesAgents(t *testing.T) {
	if !agentEvent("report.posted") {
		t.Fatal("report.posted is not sent to subscribers without the token")
	}
}

// Over a real unix socket, the peer's pid comes from the kernel
// (LOCAL_PEERPID / SO_PEERCRED): this process connects, its window is the
// session's.
func TestPeerPIDOverUnixSocket(t *testing.T) {
	testenv.NeedUnixSocket(t)
	d, fe, repo := setup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	fe.panes["main~agent"] = os.Getpid()
	sock := filepath.Join(testenv.ShortDir(t, "pkpeer"), "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go d.serve(nc)
		}
	}()
	nc, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	if pid, err := sockPeerPID(nc); err != nil || pid != os.Getpid() {
		t.Fatalf("peer of our own end: %d %v", pid, err)
	}
	c := &sockClient{t: t, nc: nc, sc: bufio.NewScanner(nc)}
	res, e := c.rawCall(map[string]any{"method": "report.post", "session": "other", "params": map[string]any{"summary": "x", "status": "progress"}})
	if e != "" {
		t.Fatal(e)
	}
	var r state.Report
	json.Unmarshal(res, &r)
	if r.Slug != "main" || r.Claimed != "other" {
		t.Fatalf("report over the socket: %+v", r)
	}
}
