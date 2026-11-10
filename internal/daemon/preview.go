package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"pekoe/internal/paths"
	"pekoe/internal/state"
)

// In-terminal browser preview: the counterpart of the Chrome pick. A pick is
// "ask for a change"; the preview is "check the result" without leaving the
// terminal. pekoed takes a screenshot of http://localhost:<port><path> with
// headless Chrome and the TUI draws it with Kitty graphics.
//
// Screenshots live in ~/.local/share/pekoe/<repo-id>/preview/<slug>/ (never in
// the repository). The last two are kept: after (the newest) and before.

// Viewport sizes (CSS pixels).
const (
	previewDesktopW = 1280
	previewDesktopH = 800
	previewMobileW  = 390
	previewMobileH  = 844
)

// Shot is one screenshot on disk.
type Shot struct {
	File   string    `json:"file"`   // absolute path of the PNG
	Width  int       `json:"width"`  // image pixels
	Height int       `json:"height"` // image pixels
	Path   string    `json:"path"`   // URL path it shows
	Mobile bool      `json:"mobile"` // taken at the mobile viewport
	At     time.Time `json:"at"`
}

// Preview is the per-session preview state (preview.* results and the
// preview.updated event).
type Preview struct {
	Slug   string `json:"slug"`
	Port   int    `json:"port"`
	Path   string `json:"path"`   // last path asked for ("/" by default)
	Mobile bool   `json:"mobile"` // capture at the mobile viewport
	Width  int    `json:"width"`  // viewport (CSS px) of the next capture
	Height int    `json:"height"`
	After  *Shot  `json:"after,omitempty"`
	Before *Shot  `json:"before,omitempty"`
	N      int    `json:"n"`               // captures taken so far
	Busy   bool   `json:"busy"`            // a capture is running
	Error  string `json:"error,omitempty"` // why the last capture did not happen
	Watch  bool   `json:"watch"`           // some client shows this preview (auto-retake on stop)
	Rev    int64  `json:"rev"`             // bumped on every change: events may arrive out of order
}

// URL is what gets captured.
func (p *Preview) URL() string { return fmt.Sprintf("http://localhost:%d%s", p.Port, p.Path) }

// CaptureReq asks a Capturer for one screenshot.
type CaptureReq struct {
	URL    string
	Out    string // PNG to write
	Width  int    // viewport, CSS px
	Height int
	Mobile bool
}

// Capturer takes screenshots (swapped for a fake in tests).
type Capturer interface {
	Capture(ctx context.Context, r CaptureReq) error
}

// ---- state ----

type previewSlot struct {
	mu    sync.Mutex // serializes captures of one session
	p     *Preview
	again bool   // a capture was asked for while one was running
	repo  string // repo-id of the session it was made for
}

type previewer struct {
	mu    sync.Mutex
	slots map[string]*previewSlot
	cap   Capturer
	probe func(port int) bool // is anything listening on the port?
}

func (d *Daemon) pv() *previewer {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.preview == nil {
		d.preview = &previewer{slots: map[string]*previewSlot{}, cap: chromeCapturer{}, probe: portOpen}
	}
	return d.preview
}

func portOpen(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 500*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func previewDir(s *state.Session) string { return paths.PreviewDir(s.RepoID, s.Slug) }

// slot returns the session's preview, loading what was remembered on disk.
func (d *Daemon) slot(s *state.Session) *previewSlot {
	pv := d.pv()
	pv.mu.Lock()
	defer pv.mu.Unlock()
	sl := pv.slots[s.Slug]
	if sl != nil && sl.repo != s.RepoID {
		sl = nil // the slug now names a session of another repository
	}
	if sl == nil {
		p := &Preview{Path: "/"}
		if b, err := os.ReadFile(filepath.Join(previewDir(s), "preview.json")); err == nil {
			_ = json.Unmarshal(b, p)
		}
		p.Busy, p.Error, p.Watch = false, "", false
		if p.After != nil && !fileExists(p.After.File) {
			p.After = nil
		}
		if p.Before != nil && !fileExists(p.Before.File) {
			p.Before = nil
		}
		sl = &previewSlot{p: p, repo: s.RepoID}
		pv.slots[s.Slug] = sl
	}
	sl.p.Slug, sl.p.Port = s.Slug, s.Port
	sl.p.Width, sl.p.Height = previewDesktopW, previewDesktopH
	if sl.p.Mobile {
		sl.p.Width, sl.p.Height = previewMobileW, previewMobileH
	}
	return sl
}

// dropPreview forgets a removed session's preview: the slot in memory and
// its screenshots on disk (a new session of the same slug starts empty).
func (d *Daemon) dropPreview(s *state.Session) {
	pv := d.pv()
	pv.mu.Lock()
	delete(pv.slots, s.Slug)
	pv.mu.Unlock()
	if err := os.RemoveAll(previewDir(s)); err != nil {
		log.Printf("preview %s: %v", s.Slug, err)
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// copyPreview deep-copies a preview (the caller holds pv.mu).
func copyPreview(p *Preview) *Preview {
	c := *p
	if p.After != nil {
		a := *p.After
		c.After = &a
	}
	if p.Before != nil {
		b := *p.Before
		c.Before = &b
	}
	return &c
}

func (d *Daemon) previewSnapshot(sl *previewSlot) *Preview {
	pv := d.pv()
	pv.mu.Lock()
	defer pv.mu.Unlock()
	return copyPreview(sl.p)
}

// edit mutates the preview under the lock and emits preview.updated.
func (d *Daemon) previewEdit(sl *previewSlot, f func(p *Preview)) *Preview {
	pv := d.pv()
	pv.mu.Lock()
	f(sl.p)
	sl.p.Rev++
	c := copyPreview(sl.p)
	pv.mu.Unlock()
	d.emit("preview.updated", c)
	return c
}

func (d *Daemon) savePreview(s *state.Session, p *Preview) {
	keep := struct {
		Path   string `json:"path"`
		Mobile bool   `json:"mobile"`
		N      int    `json:"n"`
		After  *Shot  `json:"after,omitempty"`
		Before *Shot  `json:"before,omitempty"`
	}{p.Path, p.Mobile, p.N, p.After, p.Before}
	b, _ := json.MarshalIndent(keep, "", "  ")
	if err := os.WriteFile(filepath.Join(previewDir(s), "preview.json"), b, paths.FilePerm); err != nil {
		log.Printf("preview %s: %v", s.Slug, err)
	}
}

// ---- API ----

type previewCaptureP struct {
	Slug   string `json:"slug"`
	Path   string `json:"path"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Mobile *bool  `json:"mobile"`
}

type previewWatchP struct {
	Slug string `json:"slug"`
	On   bool   `json:"on"`
}

func (d *Daemon) previewHandlers() map[string]func(json.RawMessage) (any, error) {
	return map[string]func(json.RawMessage) (any, error){
		"preview.capture": bind(d.hPreviewCapture),
		"preview.get": bind(func(p slugP) (any, error) {
			s, err := d.snapshot(p.Slug)
			if err != nil {
				return nil, err
			}
			return d.previewSnapshot(d.slot(s)), nil
		}),
		"preview.watch": bind(d.hPreviewWatch),
	}
}

// hPreviewWatch marks the preview as shown (auto-retake when the agent stops).
func (d *Daemon) hPreviewWatch(p previewWatchP) (any, error) {
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	return d.previewEdit(d.slot(s), func(x *Preview) { x.Watch = p.On }), nil
}

// hPreviewCapture takes a screenshot now and returns when it is done. The
// path and the mobile switch are remembered for the next capture.
func (d *Daemon) hPreviewCapture(p previewCaptureP) (any, error) {
	return d.capturePreview(p)
}

func normPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/"
	}
	if u := strings.TrimPrefix(strings.TrimPrefix(p, "http://"), "https://"); u != p {
		// a full URL was pasted: keep only the path part
		if i := strings.IndexByte(u, '/'); i >= 0 {
			return u[i:]
		}
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

// devDownMsg is shown instead of a capture when nothing listens on the port.
func devDownMsg(s *state.Session) string {
	if s.Dev {
		return fmt.Sprintf("dev server is not responding on :%d", s.Port)
	}
	return "dev server is not running · d to start"
}

func (d *Daemon) capturePreview(p previewCaptureP) (*Preview, error) {
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	if s.Port == 0 {
		return nil, errors.New("this session has no port")
	}
	sl := d.slot(s)
	pv := d.pv()
	// remember what was asked for, even if a capture is already running
	d.previewEdit(sl, func(x *Preview) {
		if p.Path != "" {
			x.Path = normPath(p.Path)
		}
		if p.Mobile != nil {
			x.Mobile = *p.Mobile
		}
		x.Width, x.Height = previewDesktopW, previewDesktopH
		if x.Mobile {
			x.Width, x.Height = previewMobileW, previewMobileH
		}
		if p.Width > 0 && p.Height > 0 {
			x.Width, x.Height = p.Width, p.Height
		}
	})
	if !sl.mu.TryLock() {
		pv.mu.Lock()
		sl.again = true
		pv.mu.Unlock()
		return d.previewSnapshot(sl), nil
	}
	defer sl.mu.Unlock()
	for {
		out := d.captureOnce(s, sl, pv)
		pv.mu.Lock()
		again := sl.again
		sl.again = false
		pv.mu.Unlock()
		if !again {
			return out, nil
		}
	}
}

func (d *Daemon) captureOnce(s *state.Session, sl *previewSlot, pv *previewer) *Preview {
	if fresh, err := d.snapshot(s.Slug); err == nil {
		s = fresh // the dev server may have been started meanwhile
	}
	if !pv.probe(s.Port) {
		return d.previewEdit(sl, func(x *Preview) { x.Busy, x.Error = false, devDownMsg(s) })
	}
	cur := d.previewEdit(sl, func(x *Preview) { x.Busy, x.Error = true, "" })
	dir := previewDir(s)
	if err := paths.Ensure(dir); err != nil {
		return d.previewEdit(sl, func(x *Preview) { x.Busy, x.Error = false, err.Error() })
	}
	n := cur.N + 1
	out := filepath.Join(dir, fmt.Sprintf("shot-%d.png", n))
	_ = os.Remove(out)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	err := pv.cap.Capture(ctx, CaptureReq{URL: cur.URL(), Out: out, Width: cur.Width, Height: cur.Height, Mobile: cur.Mobile})
	cancel()
	var shot *Shot
	if err == nil {
		shot, err = readShot(out)
	}
	if err != nil {
		_ = os.Remove(out)
		return d.previewEdit(sl, func(x *Preview) { x.Busy, x.Error = false, err.Error() })
	}
	shot.Path, shot.Mobile, shot.At = cur.Path, cur.Mobile, time.Now()
	var drop string
	res := d.previewEdit(sl, func(x *Preview) {
		if x.Before != nil {
			drop = x.Before.File
		}
		x.Before, x.After = x.After, shot
		x.N = n
		x.Busy, x.Error = false, ""
	})
	if drop != "" && drop != shot.File {
		_ = os.Remove(drop)
	}
	d.savePreview(s, res)
	return res
}

func readShot(file string) (*Shot, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cfg, err := png.DecodeConfig(f)
	if err != nil {
		return nil, fmt.Errorf("screenshot is not a PNG: %v", err)
	}
	return &Shot{File: file, Width: cfg.Width, Height: cfg.Height}, nil
}

// previewOnState retakes a watched preview when its agent stops working
// (running → waiting / idle / done).
func (d *Daemon) previewOnState(s *state.Session, prev state.State) {
	if prev != state.Running {
		return
	}
	switch s.State {
	case state.Waiting, state.Idle, state.Done:
	default:
		return
	}
	pv := d.pv()
	pv.mu.Lock()
	sl := pv.slots[s.Slug]
	watched := sl != nil && sl.p.Watch
	pv.mu.Unlock()
	if !watched {
		return
	}
	go func() {
		if _, err := d.capturePreview(previewCaptureP{Slug: s.Slug}); err != nil {
			log.Printf("preview %s: %v", s.Slug, err)
		}
	}()
}
