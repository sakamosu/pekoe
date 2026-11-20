package daemon

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"pekoe/internal/engine"
	"pekoe/internal/state"
)

func execCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

const (
	bucketDur = 30 * time.Second
	nBuckets  = 10
)

type outState struct {
	unread    int64
	buckets   [nBuckets]int
	bucketAt  time.Time
	lastOut   time.Time
	lastEmit  time.Time
	pending   bool
	osc       map[string][]byte // window -> partial escape sequence carried between its chunks
	checkedAt time.Time
	// recheck: restore found the agent window alive and the session
	// running or waiting, but its hooks may have been lost while pekoed was
	// away (a Stop then leaves it running for good). fallbackDetect reads
	// the screen until it shows the turn is over, or any hook clears it.
	recheck bool
	// turn counts the hooks that show the agent at work after a Stop
	// could have ended its turn (UserPromptSubmit, PreToolUse): a Stop that
	// settles late (onStop waits up to 2 s for the transcript) changes the
	// state only if none came since it (tapiState.gen is the same for tapi)
	turn uint64
}

func (o *outState) roll(now time.Time) {
	if o.bucketAt.IsZero() {
		o.bucketAt = now.Truncate(bucketDur)
		return
	}
	for now.Sub(o.bucketAt) >= bucketDur {
		copy(o.buckets[:], o.buckets[1:])
		o.buckets[nBuckets-1] = 0
		o.bucketAt = o.bucketAt.Add(bucketDur)
		if now.Sub(o.bucketAt) > bucketDur*nBuckets {
			o.buckets = [nBuckets]int{}
			o.bucketAt = now.Truncate(bucketDur)
		}
	}
}

// activityLocked scales the byte buckets to 0..7 for a sparkline.
func (d *Daemon) activityLocked(slug string) []int {
	o := d.out[slug]
	if o == nil {
		return nil
	}
	o.roll(time.Now())
	maxv := 0
	for _, b := range o.buckets {
		if b > maxv {
			maxv = b
		}
	}
	res := make([]int, nBuckets)
	if maxv == 0 {
		return res
	}
	for i, b := range o.buckets {
		res[i] = (b*7 + maxv - 1) / maxv
	}
	return res
}

var (
	oscRe   = regexp.MustCompile(`\x1b\]([0-9]+);([^\x07\x1b]*)(?:\x07|\x1b\\)`)
	trustRe = regexp.MustCompile(`(?i)trust this folder|do you trust the files`)
	// busyRe: Claude Code's screen while a turn is going on (its spinner
	// line) or a menu waits for an answer (AskUserQuestion: menuRe)
	busyRe  = regexp.MustCompile(`(?i)esc to (interrupt|cancel)|enter to select`)
	menuRe  = regexp.MustCompile(`(?i)enter to select`)
	yesNoRe = regexp.MustCompile(`(?i)(\[y/n\]|\(y/n\)|\[Y/n\]|\[y/N\]|do you want to (proceed|allow|run)|allow this)`)
)

func (d *Daemon) outputLoop() {
	ch := d.Eng.Output()
	flush := time.NewTicker(30 * time.Millisecond)
	defer flush.Stop()
	for {
		select {
		case <-d.stop:
			return
		case o := <-ch:
			d.onOutput(o)
		case <-flush.C:
			d.flushOutputEvents()
			d.flushDev(time.Now())
		}
	}
}

func (d *Daemon) onOutput(o engine.Output) {
	if o.Slug == TapiSlug {
		d.tapiOutput(o)
		return
	}
	now := time.Now()
	d.mu.Lock()
	st := d.out[o.Slug]
	s := d.sessions[o.Slug]
	if st == nil || s == nil {
		d.mu.Unlock()
		return
	}
	if o.Window == engine.WinAgent {
		st.roll(now)
		st.buckets[nBuckets-1] += len(o.Data)
		st.unread += int64(len(o.Data))
		st.lastOut = now
	}
	st.pending = true
	// per window: another window's output in between must not be glued
	// to the first half of a sequence
	data, carry := splitCarry(append(st.osc[o.Window], o.Data...))
	if len(carry) > 0 {
		if st.osc == nil {
			st.osc = map[string][]byte{}
		}
		st.osc[o.Window] = carry
	} else {
		delete(st.osc, o.Window)
	}
	agent := d.cfg().Agents[s.Agent]
	hooks := agent.Hooks
	d.mu.Unlock()

	if o.Window == engine.WinDev {
		d.feedDev(o.Slug, o.Data, now)
		d.writeDevLog(o.Slug, o.Data)
	}
	if bytes.Contains(data, []byte("\x1b_G")) || bytes.Contains(data, []byte("\x1bPtmux;")) {
		d.passGraphics(o.Slug, o.Window, data)
	}
	if o.Window != engine.WinAgent {
		return
	}
	// Pass through sequences the outer terminal (Ghostty) must see.
	for _, m := range oscRe.FindAllSubmatch(data, -1) {
		code, _ := strconv.Atoi(string(m[1]))
		switch code {
		case 52: // clipboard
			d.emit("term.passthrough", map[string]any{"slug": o.Slug, "seq": base64.StdEncoding.EncodeToString(m[0])})
		case 9, 99, 777: // desktop notifications: also a state signal for agents without hooks
			d.emit("term.passthrough", map[string]any{"slug": o.Slug, "seq": base64.StdEncoding.EncodeToString(m[0])})
			if !hooks {
				d.setState(o.Slug, state.Waiting, "question")
			}
		}
	}
	if !hooks {
		if bytes.IndexByte(data, 0x07) >= 0 && !oscRe.Match(data) {
			d.setState(o.Slug, state.Waiting, "question")
		} else if s.State != state.Running && s.State != state.Reviewing && s.State != state.Waiting {
			d.setState(o.Slug, state.Running, "")
		}
	}
}

func (d *Daemon) flushOutputEvents() {
	now := time.Now()
	d.mu.Lock()
	var slugs []string
	for slug, st := range d.out {
		if st.pending && now.Sub(st.lastEmit) >= 30*time.Millisecond {
			st.pending = false
			st.lastEmit = now
			slugs = append(slugs, slug)
		}
	}
	d.mu.Unlock()
	for _, s := range slugs {
		d.emit("session.output", map[string]any{"slug": s})
	}
	d.flushTapiOutput(now)
}

// tickLoop: activity decay, fallback state detection for agents without
// hooks (final fallback: [y/n] on the virtual screen), PR and merge watching,
// conflict forecast.
func (d *Daemon) tickLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	// a PR merged while pekoed was down (or restarting) lands now, not 2
	// minutes into this run
	go d.watchMerges()
	go d.failStaleMerges() // a merge on a person's answer cut off by a stop (mergerun.go)
	n := 0
	for {
		select {
		case <-d.stop:
			return
		case <-t.C:
		}
		n++
		d.reloadConfig() // config.toml changed: take it up (config.go)
		if n%5 == 1 {
			d.kickQueue() // max_running changed, or a kick was missed (queue.go)
		}
		d.fallbackDetect()
		if n%30 == 0 {
			d.mu.Lock()
			var all []*state.Session
			for _, s := range d.sessions {
				c := *s
				c.Activity = d.activityLocked(s.Slug)
				all = append(all, &c)
			}
			d.mu.Unlock()
			for _, s := range all {
				d.emit("session.updated", s)
			}
		}
		if n%120 == 0 {
			go d.watchMerges()
		}
		if n%60 == 5 {
			go d.watchPRs()
		}
		if n%15 == 10 {
			go d.watchPRStatus() // each open PR at most once a minute
		}
		if n%10 == 3 {
			d.kickConflicts() // conflict forecast: git every ~10 s
		}
		if n%5 == 4 {
			go d.deliverAllInbox() // answers held back by a prompt or a person typing
			go d.deliverTapi()     // [orchestrator] deliver (deliver.go)
		}
		if n%10 == 7 {
			go d.checkMerges() // merge evidence in the inbox
		}
		if n%2 == 0 {
			go d.checkDevExits() // a dev hook that ended by itself (devlog.go)
		}
		if n%10 == 9 {
			go d.checkDevWatches() // .git/hooks and config under running dev servers (gitwatch.go)
		}
	}
}

func (d *Daemon) fallbackDetect() {
	now := time.Now()
	d.mu.Lock()
	var check []string
	// trust: Claude Code's folder trust dialog comes before any hook, so
	// its words are read only while there is none yet (no Claude session
	// id). After that they are only words on a screen: a grep result, a
	// quoted README, the agent's reply, that restore's recheck would take
	// for a dialog that never goes away
	trust := map[string]bool{}
	for slug, s := range d.sessions {
		st := d.out[slug]
		if st == nil {
			continue
		}
		// agents with hooks are read from the screen only before their first
		// hook arrives (Claude Code's folder trust dialog precedes all hooks)
		if d.cfg().Agents[s.Agent].Hooks && s.ClaudeSessionID != "" && !st.recheck {
			continue
		}
		quiet := now.Sub(st.lastOut)
		active := s.State == state.Running || (s.State == state.Idle && s.ClaudeSessionID == "") ||
			(st.recheck && s.State == state.Waiting)
		if active && quiet > 1500*time.Millisecond && st.lastOut.After(st.checkedAt) {
			st.checkedAt = now
			check = append(check, slug)
			trust[slug] = s.ClaudeSessionID == ""
		}
	}
	d.mu.Unlock()
	for _, slug := range check {
		sc, err := d.Eng.Capture(slug, engine.WinAgent)
		if err != nil {
			continue
		}
		tailLines := sc.Lines
		if len(tailLines) > 8 {
			tailLines = tailLines[len(tailLines)-8:]
		}
		text := stripANSI(strings.Join(tailLines, "\n"))
		full := stripANSI(strings.Join(sc.Lines, "\n"))
		if trust[slug] && trustRe.MatchString(full) {
			d.update(slug, func(x *state.Session) {
				x.State, x.WaitKind = state.Waiting, "permission"
				x.PendingTool = &state.Tool{Name: "Trust", Input: "trust this folder: " + x.Path}
			})
			continue
		}
		if d.cfg().Agents[d.sessionAgent(slug)].Hooks {
			d.recheckAfterRestart(slug, text, sc.Lines)
			continue
		}
		if yesNoRe.MatchString(text) {
			d.setState(slug, state.Waiting, "permission")
		} else {
			d.setState(slug, state.Idle, "")
		}
	}
}

// recheckAfterRestart settles a session restore marked (outState.recheck)
// from its screen, as the hook lost while pekoed was away would have:
//   - a permission prompt makes a running session waiting for permission
//     (the lost Notification), and a menu (AskUserQuestion's "enter to
//     select") waiting for an answer, so that the permission modal, wait
//     --on waiting and the OS notification still come. The mark stays: the
//     answer's hook (PostToolUse) clears it
//   - still in its turn (the spinner line) keeps it as it is and looks
//     again after the next quiet spell
//   - anything else means the turn ended while pekoed was away: a running
//     session is done (the lost Stop, so wait --on done returns, with no
//     report if there was none), a waiting one idle (its prompt went away
//     with no Stop: answered no, or esc, in the pane)
//
// A hook that arrived meanwhile cleared the mark and wins. text is the
// tail of the screen, lines all of it.
//
// A permission prompt is known by its shape, not by its words: Claude
// Code's dialog ("Do you want to …" over a "❯ 1. Yes" choice) or a [y/n]
// on the last line (permissionOnScreen). yesNoRe is not used here: it also
// matches the agent's own reply ("Plan ready. Do you want to proceed with
// …", "--force [y/N]"), which stays on a screen that will not change once
// the turn is over, so the session would wait for an answer for good.
func (d *Daemon) recheckAfterRestart(slug, text string, lines []string) {
	kind := ""
	switch {
	case permissionOnScreen(lines):
		kind = "permission"
	case menuRe.MatchString(text):
		kind = "question"
	case busyRe.MatchString(text):
		return
	}
	d.update(slug, func(x *state.Session) {
		st := d.out[slug] // update holds d.mu
		if st == nil || !st.recheck {
			return
		}
		switch {
		case kind != "":
			if x.State == state.Running {
				x.State, x.WaitKind = state.Waiting, kind
			}
			return
		case x.State == state.Running:
			x.State, x.WaitKind, x.PendingTool = state.Done, "", nil
		case x.State == state.Waiting:
			x.State, x.WaitKind, x.PendingTool = state.Idle, "", nil
		}
		st.recheck = false
	})
}

func (d *Daemon) sessionAgent(slug string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if s := d.sessions[slug]; s != nil {
		return s.Agent
	}
	return ""
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("json: %v", err)
	}
	return b
}
