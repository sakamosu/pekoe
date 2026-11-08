package daemon

// Error catcher: errors printed in a session's dev window, and browser
// console errors the extension forwards, become reports (internal/errcatch)
// that the TUI offers as a card: "send this error?". Nothing is sent to the
// agent until the human says so.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"pekoe/internal/errcatch"
	"pekoe/internal/paths"
)

// feedDev scans dev window output (called from the output loop only).
func (d *Daemon) feedDev(slug string, data []byte, now time.Time) {
	sc := d.devScan[slug]
	if sc == nil {
		sc = errcatch.NewScanner()
		d.devScan[slug] = sc
	}
	for _, b := range sc.Feed(data, now) {
		d.catchBlock(slug, errcatch.Origin{Source: errcatch.SourceDev}, b, now)
	}
}

// flushDev closes blocks whose output went quiet (output loop only).
func (d *Daemon) flushDev(now time.Time) {
	if now.Sub(d.devFlushAt) < 100*time.Millisecond {
		return
	}
	d.devFlushAt = now
	for slug, sc := range d.devScan {
		if _, err := d.get(slug); err != nil {
			delete(d.devScan, slug)
			continue
		}
		if !sc.Pending() {
			continue
		}
		for _, b := range sc.Flush(now) {
			d.catchBlock(slug, errcatch.Origin{Source: errcatch.SourceDev}, b, now)
		}
	}
}

// catchBlock records one detected error and tells the clients.
func (d *Daemon) catchBlock(slug string, o errcatch.Origin, b errcatch.Block, at time.Time) {
	s, err := d.snapshot(slug)
	if err != nil {
		return
	}
	r, ch := d.catch.Add(slug, o, s.Path, b, at)
	if ch == errcatch.Unchanged {
		return
	}
	d.emit("error.updated", errorEvent{Report: r, Fresh: ch == errcatch.Fresh})
}

type errorEvent struct {
	Report errcatch.Report `json:"report"`
	Fresh  bool            `json:"fresh"` // new, or recurred after being sent / ignored
}

// consoleBlock is a browser console error as a detector block, or false when
// it is only a console.error text (no stack, no script location): those stay
// in the Browser panel's count but are not offered as cards (React warnings
// and logging would bury the real errors).
func consoleBlock(e ConsoleError) (errcatch.Block, bool) {
	if strings.TrimSpace(e.Stack) == "" && e.Source == "" {
		return errcatch.Block{}, false
	}
	return errcatch.FromConsole(e.Message, e.Stack, e.Source, e.Line), true
}

// catchConsole offers browser errors of a port owned by a session.
func (d *Daemon) catchConsole(port int, url string, errs []ConsoleError) {
	_, slug, ok := d.DB.PortOwner(port)
	if !ok {
		return
	}
	now := time.Now()
	for _, e := range errs {
		if b, ok := consoleBlock(e); ok {
			d.catchBlock(slug, errcatch.Origin{Source: errcatch.SourceBrowser, Port: port, URL: url}, b, now)
		}
	}
}

// ---- API ----

type errListP struct {
	Slug string `json:"slug"`
	All  bool   `json:"all"`
}

func (d *Daemon) hErrorList(p errListP) (any, error) {
	return d.catch.List(p.Slug, p.All), nil
}

type errDetail struct {
	errcatch.Report
	Excerpt string `json:"excerpt,omitempty"`
}

func (d *Daemon) hErrorGet(p idP) (any, error) {
	r, ok := d.catch.Get(p.ID)
	if !ok {
		return nil, fmt.Errorf("no such error: #%d", p.ID)
	}
	return errDetail{Report: r, Excerpt: d.excerpt(r)}, nil
}

// excerpt: ±5 lines of the top frame when it is the project's own file.
func (d *Daemon) excerpt(r errcatch.Report) string {
	if !r.InTree {
		return ""
	}
	s, err := d.snapshot(r.Slug)
	if err != nil {
		return ""
	}
	return errcatch.Excerpt(s.Path, r.File, r.Line, errcatch.ExcerptLines)
}

type errSendP struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug"` // default: the session the error came from
	Text string `json:"text"` // default: errcatch.Instruction
}

// hErrorSend hands one error to the agent: short ones as one line, others
// as a markdown file (output + source excerpt) referenced with @.
func (d *Daemon) hErrorSend(p errSendP) (any, error) {
	r, ok := d.catch.Get(p.ID)
	if !ok {
		return nil, fmt.Errorf("no such error: #%d", p.ID)
	}
	slug := p.Slug
	if slug == "" {
		slug = r.Slug
	}
	s, err := d.snapshot(slug)
	if err != nil {
		return nil, err
	}
	excerpt := d.excerpt(r)
	instr := strings.TrimSpace(p.Text)
	if instr == "" {
		instr = errcatch.Instruction(r)
	}
	if errcatch.Short(r, excerpt) && p.Text == "" {
		_, err = d.hSend(sendP{Slug: slug, Text: errcatch.InlinePrompt(r), Submit: true})
	} else {
		_, err = d.sendAttachment(slug, s.RepoID, func(n int) string { return errcatch.Markdown(int64(n), r, excerpt) }, instr)
	}
	if err != nil {
		return nil, err
	}
	r, _ = d.catch.Mark(r.ID, errcatch.StatusSent, time.Now())
	if r.Source == errcatch.SourceBrowser {
		d.dropConsole(r)
	}
	d.emit("error.updated", errorEvent{Report: r})
	return r, nil
}

func (d *Daemon) hErrorIgnore(p idP) (any, error) {
	r, err := d.catch.Mark(p.ID, errcatch.StatusIgnored, time.Now())
	if err != nil {
		return nil, err
	}
	d.emit("error.updated", errorEvent{Report: r})
	return r, nil
}

// dropConsole removes the browser panel's copies of a sent report so the
// bulk send (E) does not send it again.
func (d *Daemon) dropConsole(r errcatch.Report) {
	root := ""
	if s, err := d.snapshot(r.Slug); err == nil {
		root = s.Path
	}
	d.mu.Lock()
	kept := d.errors[r.Port][:0]
	for _, e := range d.errors[r.Port] {
		if b, ok := consoleBlock(e); ok && errcatch.FingerprintIn(root, b) == r.Fingerprint {
			continue
		}
		kept = append(kept, e)
	}
	if len(kept) == 0 {
		delete(d.errors, r.Port)
	} else {
		d.errors[r.Port] = kept
	}
	d.mu.Unlock()
	d.emit("browser.updated", d.browserTabs())
}

// ---- attachments: long text goes to a file, the pty gets one @ line ----

var attachNumRe = regexp.MustCompile(`-(\d+)\.md$`)

// sendAttachment writes body(n) to ~/.local/share/pekoe/<repo-id>/errors/
// <slug>-<n>.md and pastes "@<file> <instruction>" into the agent.
func (d *Daemon) sendAttachment(slug, repoID string, body func(n int) string, instruction string) (string, error) {
	if repoID == "" {
		repoID = "_unresolved"
	}
	dir := paths.ErrorsDir(repoID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	n := 1
	if ents, err := os.ReadDir(dir); err == nil {
		for _, e := range ents {
			if m := attachNumRe.FindStringSubmatch(e.Name()); m != nil {
				if v, _ := strconv.Atoi(m[1]); v >= n {
					n = v + 1
				}
			}
		}
	}
	file := filepath.Join(dir, fmt.Sprintf("%s-%04d.md", slug, n))
	if err := os.WriteFile(file, []byte(body(n)), 0o644); err != nil {
		return "", err
	}
	line := "@" + file
	if strings.TrimSpace(instruction) != "" {
		line += " " + strings.TrimSpace(instruction)
	}
	if _, err := d.hSend(sendP{Slug: slug, Text: line, Submit: true}); err != nil {
		return "", err
	}
	return file, nil
}

// removeAttachments deletes a removed session's error files.
func removeAttachments(repoID, slug string) {
	files, _ := filepath.Glob(filepath.Join(paths.ErrorsDir(repoID), slug+"-*.md"))
	for _, f := range files {
		if attachNumRe.MatchString(strings.TrimPrefix(filepath.Base(f), slug)) {
			os.Remove(f)
		}
	}
}

var errNoConsole = errors.New("no console errors")
