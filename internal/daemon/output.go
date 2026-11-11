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
	agent := d.Global.Agents[s.Agent]
	hooks := agent.Hooks
	d.mu.Unlock()

	if o.Window == engine.WinDev {
		d.feedDev(o.Slug, o.Data, now)
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
	n := 0
	for {
		select {
		case <-d.stop:
			return
		case <-t.C:
		}
		n++
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
		}
		if n%10 == 7 {
			go d.checkMerges() // merge evidence in the inbox
		}
	}
}

func (d *Daemon) fallbackDetect() {
	now := time.Now()
	d.mu.Lock()
	var check []string
	for slug, s := range d.sessions {
		st := d.out[slug]
		if st == nil {
			continue
		}
		// agents with hooks are read from the screen only before their first
		// hook arrives (Claude Code's folder trust dialog precedes all hooks)
		if d.Global.Agents[s.Agent].Hooks && s.ClaudeSessionID != "" {
			continue
		}
		quiet := now.Sub(st.lastOut)
		active := s.State == state.Running || (s.State == state.Idle && s.ClaudeSessionID == "")
		if active && quiet > 1500*time.Millisecond && st.lastOut.After(st.checkedAt) {
			st.checkedAt = now
			check = append(check, slug)
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
		if trustRe.MatchString(full) {
			d.update(slug, func(x *state.Session) {
				x.State, x.WaitKind = state.Waiting, "permission"
				x.PendingTool = &state.Tool{Name: "Trust", Input: "trust this folder: " + x.Path}
			})
			continue
		}
		if d.Global.Agents[d.sessionAgent(slug)].Hooks {
			continue
		}
		if yesNoRe.MatchString(text) {
			d.setState(slug, state.Waiting, "permission")
		} else {
			d.setState(slug, state.Idle, "")
		}
	}
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
