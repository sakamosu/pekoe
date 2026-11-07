package daemon

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"boba/internal/paths"
	"boba/internal/state"
)

// fakeCapturer writes a PNG of the viewport size and records the requests.
type fakeCapturer struct {
	mu   sync.Mutex
	reqs []CaptureReq
	err  error
}

func (f *fakeCapturer) Capture(_ context.Context, r CaptureReq) error {
	f.mu.Lock()
	f.reqs = append(f.reqs, r)
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return err
	}
	file, err := os.Create(r.Out)
	if err != nil {
		return err
	}
	defer file.Close()
	return png.Encode(file, image.NewRGBA(image.Rect(0, 0, r.Width, r.Height)))
}

func (f *fakeCapturer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func (f *fakeCapturer) last() CaptureReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[len(f.reqs)-1]
}

func previewSetup(t *testing.T, listening bool) (*Daemon, *fakeCapturer, state.Session) {
	t.Helper()
	d, _, repo := setup(t)
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	fc := &fakeCapturer{}
	pv := d.pv()
	pv.cap = fc
	pv.probe = func(int) bool { return listening }
	return d, fc, s
}

// waitPreview polls until cond holds for the session's preview.
func waitPreview(t *testing.T, d *Daemon, slug string, cond func(p Preview) bool) Preview {
	t.Helper()
	var p Preview
	for i := 0; i < 200; i++ {
		p = call[Preview](t, d, "preview.get", map[string]any{"slug": slug})
		if cond(p) {
			return p
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("preview never settled: %+v", p)
	return p
}

func TestPreviewAutoRetakeOnStop(t *testing.T) {
	d, fc, s := previewSetup(t, true)
	slug := s.Slug

	// not shown anywhere: stopping does not capture
	d.setState(slug, state.Running, "")
	d.setState(slug, state.Done, "")
	time.Sleep(50 * time.Millisecond)
	if fc.count() != 0 {
		t.Fatalf("captured without a watcher: %d", fc.count())
	}

	call[Preview](t, d, "preview.watch", map[string]any{"slug": slug, "on": true})
	d.setState(slug, state.Running, "")
	d.setState(slug, state.Done, "")
	p := waitPreview(t, d, slug, func(p Preview) bool { return p.N == 1 && !p.Busy })
	if p.After == nil || p.Before != nil || p.After.Width != previewDesktopW || p.After.Height != previewDesktopH {
		t.Fatalf("first shot: %+v", p)
	}
	if r := fc.last(); r.URL != "http://localhost:"+strconv.Itoa(s.Port)+"/" || r.Mobile {
		t.Fatalf("request: %+v", r)
	}
	if !strings.HasPrefix(p.After.File, paths.PreviewDir(s.RepoID, slug)) {
		t.Fatalf("shot outside the data dir: %s", p.After.File)
	}
	first := p.After.File

	// running → waiting also retakes; the previous shot is kept as before
	d.setState(slug, state.Running, "")
	d.setState(slug, state.Waiting, "permission")
	p = waitPreview(t, d, slug, func(p Preview) bool { return p.N == 2 && !p.Busy })
	if p.Before == nil || p.Before.File != first || p.After.File == first {
		t.Fatalf("before/after: %+v", p)
	}
	second := p.After.File

	// a third shot drops the oldest file
	d.setState(slug, state.Running, "")
	d.setState(slug, state.Idle, "")
	p = waitPreview(t, d, slug, func(p Preview) bool { return p.N == 3 && !p.Busy })
	if p.Before.File != second || fileExists(first) {
		t.Fatalf("oldest shot not dropped: %+v (first exists: %v)", p, fileExists(first))
	}

	// waiting → done is not "the agent stopped working": no capture
	d.setState(slug, state.Done, "")
	time.Sleep(50 * time.Millisecond)
	if fc.count() != 3 {
		t.Fatalf("captured on a non-running transition: %d", fc.count())
	}

	// unwatched again: nothing more
	call[Preview](t, d, "preview.watch", map[string]any{"slug": slug, "on": false})
	d.setState(slug, state.Running, "")
	d.setState(slug, state.Done, "")
	time.Sleep(50 * time.Millisecond)
	if fc.count() != 3 {
		t.Fatalf("captured after unwatch: %d", fc.count())
	}
}

func TestPreviewDevServerDown(t *testing.T) {
	d, fc, s := previewSetup(t, false)
	call[Preview](t, d, "preview.watch", map[string]any{"slug": s.Slug, "on": true})
	d.setState(s.Slug, state.Running, "")
	d.setState(s.Slug, state.Done, "")
	p := waitPreview(t, d, s.Slug, func(p Preview) bool { return p.Error != "" })
	if p.Error != "dev server is not running · d to start" || fc.count() != 0 || p.After != nil {
		t.Fatalf("dev down: %+v (captures %d)", p, fc.count())
	}
	// dev server started but not listening yet
	d.update(s.Slug, func(x *state.Session) { x.Dev = true })
	p = call[Preview](t, d, "preview.capture", map[string]any{"slug": s.Slug})
	if !strings.Contains(p.Error, "not responding on :") {
		t.Fatalf("dev up but closed: %+v", p)
	}
}

func TestPreviewRemembersPathAndMobile(t *testing.T) {
	d, fc, s := previewSetup(t, true)
	yes := true
	p := call[Preview](t, d, "preview.capture", map[string]any{"slug": s.Slug, "path": "settings", "mobile": yes})
	if p.Path != "/settings" || !p.Mobile || p.After == nil || p.After.Width != previewMobileW || !p.After.Mobile {
		t.Fatalf("capture: %+v", p)
	}
	if r := fc.last(); !strings.HasSuffix(r.URL, "/settings") || !r.Mobile || r.Width != previewMobileW || r.Height != previewMobileH {
		t.Fatalf("request: %+v", r)
	}
	// the next capture (auto or R) reuses path and viewport
	call[Preview](t, d, "preview.capture", map[string]any{"slug": s.Slug})
	if r := fc.last(); !strings.HasSuffix(r.URL, "/settings") || !r.Mobile {
		t.Fatalf("not remembered: %+v", r)
	}
	// and survives a daemon restart (preview.json next to the shots)
	d.pv().slots = map[string]*previewSlot{}
	p = call[Preview](t, d, "preview.get", map[string]any{"slug": s.Slug})
	if p.Path != "/settings" || !p.Mobile || p.After == nil || p.Before == nil || p.N != 2 {
		t.Fatalf("after reload: %+v", p)
	}
	// a pasted URL keeps only its path
	call[Preview](t, d, "preview.capture", map[string]any{"slug": s.Slug, "path": "http://localhost:3000/a/b?x=1", "mobile": false})
	if r := fc.last(); !strings.HasSuffix(r.URL, "/a/b?x=1") || r.Mobile || r.Width != previewDesktopW {
		t.Fatalf("url path: %+v", r)
	}
}

func TestPreviewCaptureError(t *testing.T) {
	d, fc, s := previewSetup(t, true)
	fc.err = os.ErrPermission
	p := call[Preview](t, d, "preview.capture", map[string]any{"slug": s.Slug})
	if p.Error == "" || p.Busy || p.After != nil {
		t.Fatalf("error: %+v", p)
	}
}

func TestChromeMissing(t *testing.T) {
	t.Setenv("BOBA_CHROME", filepath.Join(t.TempDir(), "nope"))
	err := chromeCapturer{}.Capture(context.Background(), CaptureReq{URL: "http://localhost:1/", Out: filepath.Join(t.TempDir(), "x.png"), Width: 10, Height: 10})
	if err == nil || !strings.Contains(err.Error(), "Google Chrome not found") || !strings.Contains(err.Error(), "BOBA_CHROME") {
		t.Fatalf("err: %v", err)
	}
}

func TestDeviceMetrics(t *testing.T) {
	m := deviceMetrics(CaptureReq{Width: 390, Height: 844, Mobile: true})
	if m["width"] != 390 || m["height"] != 844 || m["deviceScaleFactor"] != 2 || m["mobile"] != true {
		t.Fatalf("mobile: %v", m)
	}
	m = deviceMetrics(CaptureReq{Width: 1280, Height: 800})
	if m["deviceScaleFactor"] != 1 || m["mobile"] != false {
		t.Fatalf("desktop: %v", m)
	}
	a := strings.Join(chromeArgs("/p"), " ")
	for _, want := range []string{"--headless=new", "--remote-debugging-pipe", "--user-data-dir=/p"} {
		if !strings.Contains(a, want) {
			t.Errorf("missing %q in %s", want, a)
		}
	}
}

// TestChromeCaptureReal drives the real Chrome when it is installed: the
// mobile viewport must really be 390 px wide (media queries match).
func TestChromeCaptureReal(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	if _, err := chromeBinary(); err != nil {
		t.Skip(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><head><meta name=viewport content="width=device-width"><style>body{margin:0;background:#00f}@media (max-width:500px){body{background:#f00}}</style></head><body></body></html>`)
	}))
	defer srv.Close()
	for _, mobile := range []bool{false, true} {
		w, h := previewDesktopW, previewDesktopH
		if mobile {
			w, h = previewMobileW, previewMobileH
		}
		out := filepath.Join(t.TempDir(), "s.png")
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		err := chromeCapturer{}.Capture(ctx, CaptureReq{URL: srv.URL + "/", Out: out, Width: w, Height: h, Mobile: mobile})
		cancel()
		if err != nil {
			t.Fatalf("mobile=%v: %v", mobile, err)
		}
		f, _ := os.Open(out)
		img, err := png.Decode(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		scale := 1
		if mobile {
			scale = 2
		}
		if b := img.Bounds(); b.Dx() != w*scale || b.Dy() != h*scale {
			t.Fatalf("mobile=%v: size %v", mobile, b)
		}
		r, _, bl, _ := img.At(10, 10).RGBA()
		if mobile != (r > bl) {
			t.Fatalf("mobile=%v: media query mismatch (r=%d b=%d)", mobile, r, bl)
		}
	}
	// nothing listening: a clear error, not a blank image
	err := chromeCapturer{}.Capture(context.Background(), CaptureReq{URL: "http://localhost:1/", Out: filepath.Join(t.TempDir(), "x.png"), Width: 100, Height: 100})
	if err == nil || !strings.Contains(err.Error(), "could not load") {
		t.Fatalf("closed port: %v", err)
	}
}
