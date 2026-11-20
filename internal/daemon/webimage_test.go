package daemon

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"pekoe/internal/paths"
	"pekoe/internal/state"
)

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func testJPEG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewGray(image.Rect(0, 0, 4, 3)), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// imgs is the body's images ([]byte goes as base64, as the page sends it).
func imgs(bs ...[]byte) []map[string]any {
	out := []map[string]any{}
	for _, b := range bs {
		out = append(out, map[string]any{"data": b})
	}
	return out
}

func sendImagesReq(path, text string, bs ...[]byte) webReq {
	return webReq{method: "POST", path: path, from: ipPhone, body: map[string]any{"text": text, "images": imgs(bs...)}}
}

// imageFiles lists what is saved for slug.
func imageFiles(t *testing.T, slug string) []string {
	t.Helper()
	fs, _ := filepath.Glob(filepath.Join(paths.WebImagesDir(), slug, "*"))
	return fs
}

// typedLines: the "[image] <path>" lines of what was typed.
func typedImages(typed string) []string {
	var out []string
	for _, l := range strings.Split(typed, "\n") {
		if p, ok := strings.CutPrefix(l, "[image] "); ok {
			out = append(out, p)
		}
	}
	return out
}

// lastText: the text the fake engine was given last.
func lastText(fe *fakeEngine) string {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	for i := len(fe.sent) - 1; i >= 0; i-- {
		if t, ok := strings.CutPrefix(fe.sent[i], "text:"); ok {
			return t
		}
	}
	return ""
}

// A send with images: pekoed saves them under the data directory (not in
// the worktree), named by itself, mode 0600, and types the text with one
// "[image] <path>" line each, which the agent opens with Read. The send
// log has the same text.
func TestWebSendImages(t *testing.T) {
	d, fe, repo, h := webSetup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	p1, j1 := testPNG(t, 3, 2), testJPEG(t)
	w := doWeb(t, h, sendImagesReq("/api/sessions/main/send", "  この画面のずれを直して  ", p1, j1))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"images":2`) {
		t.Fatalf("send: %d %s", w.Code, w.Body)
	}
	typed := lastText(fe)
	files := typedImages(typed)
	if len(files) != 2 || !strings.HasPrefix(typed, "この画面のずれを直して\n\n[image] ") || !strings.HasSuffix(fe.last(), "keys:Enter") {
		t.Fatalf("typed: %q / %s", typed, fe.last())
	}
	dir := filepath.Join(paths.WebImagesDir(), "main")
	for i, want := range [][]byte{p1, j1} {
		f := files[i]
		if filepath.Dir(f) != dir || !strings.HasSuffix(f, []string{".png", ".jpg"}[i]) {
			t.Errorf("image %d at %s", i, f)
		}
		if strings.HasPrefix(paths.Real(f), paths.Real(repo)) {
			t.Errorf("image %d in the worktree: %s", i, f)
		}
		got, err := os.ReadFile(f)
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("image %d: %v", i, err)
		}
		if st, err := os.Stat(f); err != nil || st.Mode().Perm() != 0o600 {
			t.Errorf("image %d mode: %v %v", i, st.Mode(), err)
		}
	}
	rs, _ := d.DB.Sends(state.SendFilter{To: "main", Limit: 10})
	if len(rs) != 1 || rs[0].Text != typed || rs[0].Via != "web:iphone" || !rs[0].OK {
		t.Fatalf("send log: %+v", rs)
	}

	// images alone (no words) go too
	if w := doWeb(t, h, sendImagesReq("/api/sessions/main/send", "", p1)); w.Code != 200 {
		t.Fatalf("image only: %d %s", w.Code, w.Body)
	}
	if typed := lastText(fe); !strings.HasPrefix(typed, "[image] "+dir+"/") || len(typedImages(typed)) != 1 {
		t.Fatalf("image only typed: %q", typed)
	}
	if n := len(imageFiles(t, "main")); n != 3 {
		t.Fatalf("saved %d files, want 3", n)
	}
}

// What is not an image, or too much of one, is refused before anything is
// saved or typed.
func TestWebSendImagesRefused(t *testing.T) {
	d, fe, repo, h := webSetup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	before := sentCount(fe)
	big := append(testPNG(t, 2, 2), make([]byte, webMaxImageBytes)...) // a PNG header, 5 MB after it
	heic := append([]byte("\x00\x00\x00\x18ftypheic"), make([]byte, 64)...)
	for name, c := range map[string]struct {
		body webReq
		code int
	}{
		"text file":     {sendImagesReq("/api/sessions/main/send", "見て", []byte("#!/bin/sh\necho hi\n")), http.StatusUnsupportedMediaType},
		"html":          {sendImagesReq("/api/sessions/main/send", "見て", []byte("<html><script>alert(1)</script>")), http.StatusUnsupportedMediaType},
		"heic":          {sendImagesReq("/api/sessions/main/send", "見て", heic), http.StatusUnsupportedMediaType},
		"png magic":     {sendImagesReq("/api/sessions/main/send", "見て", []byte("\x89PNG\r\n\x1a\nnot really")), http.StatusUnsupportedMediaType},
		"empty":         {sendImagesReq("/api/sessions/main/send", "見て", []byte{}), http.StatusBadRequest},
		"too large":     {sendImagesReq("/api/sessions/main/send", "見て", big), http.StatusBadRequest},
		"too wide":      {sendImagesReq("/api/sessions/main/send", "見て", testPNG(t, webMaxImageSide+1, 1)), http.StatusBadRequest},
		"too many":      {sendImagesReq("/api/sessions/main/send", "見て", testJPEG(t), testJPEG(t), testJPEG(t), testJPEG(t), testJPEG(t)), http.StatusBadRequest},
		"control":       {sendImagesReq("/api/sessions/main/send", "a\x1b[Zb", testJPEG(t)), http.StatusBadRequest},
		"nothing":       {sendImagesReq("/api/sessions/main/send", "   "), http.StatusBadRequest},
		"no session":    {sendImagesReq("/api/sessions/nope/send", "見て", testJPEG(t)), http.StatusNotFound},
		"one bad of 2":  {sendImagesReq("/api/sessions/main/send", "見て", testJPEG(t), []byte("%PDF-1.7 broken")), http.StatusUnsupportedMediaType},
		"over the body": {webReq{method: "POST", path: "/api/sessions/main/send", from: ipPhone, body: map[string]any{"text": strings.Repeat("x", webMaxSendBody)}}, http.StatusRequestEntityTooLarge},
	} {
		if w := doWeb(t, h, c.body); w.Code != c.code {
			t.Errorf("%s: %d %s (want %d)", name, w.Code, w.Body, c.code)
		}
	}
	if sentCount(fe) != before {
		t.Fatalf("typed a refused send: %s", fe.last())
	}
	if fs := imageFiles(t, "main"); len(fs) != 0 {
		t.Fatalf("saved for a refused send: %v", fs)
	}

	// the most a send may carry gets through: 4 images just under 5 MB
	near := append(testPNG(t, 2, 2), make([]byte, webMaxImageBytes-200)...)
	if w := doWeb(t, h, sendImagesReq("/api/sessions/main/send", "", near, near, near, near)); w.Code != 200 {
		t.Fatalf("4 × 5 MB: %d %s", w.Code, w.Body)
	}
	// the other writes keep their small limit
	if w := doWeb(t, h, webReq{method: "POST", path: "/api/sessions/main/keys", from: ipPhone, body: map[string]any{"keys": []string{strings.Repeat("x", webMaxBody)}}}); w.Code != http.StatusBadRequest {
		t.Fatalf("keys with a large body: %d", w.Code)
	}
}

// While a permission prompt waits, a send with images is refused and
// nothing is saved; if the prompt comes up between typing and Enter, the
// images stay (their paths are in the input box), otherwise they go.
func TestWebSendImagesWhileWaiting(t *testing.T) {
	d, fe, repo, h := webSetup(t)
	waitingSession(t, d, repo)
	before := sentCount(fe)
	if w := doWeb(t, h, sendImagesReq("/api/sessions/main/send", "見て", testJPEG(t))); w.Code != http.StatusConflict {
		t.Fatalf("send while waiting: %d %s", w.Code, w.Body)
	}
	if sentCount(fe) != before || len(imageFiles(t, "main")) != 0 {
		t.Fatalf("typed or saved: %s / %v", fe.last(), imageFiles(t, "main"))
	}
	// a menu on the screen (no hook yet): refused before typing, nothing kept
	event(t, d, "PostToolUse", "main", map[string]any{"tool_name": "Bash"})
	fe.mu.Lock()
	fe.lines = []string{"│ Bash command", "│   rm -rf x", "│ Do you want to proceed?", "│ ❯ 1. Yes", "│   2. No", ""}
	fe.mu.Unlock()
	if w := doWeb(t, h, sendImagesReq("/api/sessions/main/send", "見て", testJPEG(t))); w.Code != http.StatusConflict {
		t.Fatalf("send over a menu on screen: %d %s", w.Code, w.Body)
	}
	if fs := imageFiles(t, "main"); len(fs) != 0 {
		t.Fatalf("kept the images of a send that typed nothing: %v", fs)
	}
}

// tapi: the same, under its own rules: allow_devices first (nothing is
// saved without it), images in images/+tapi, recorded in the send log.
func TestWebTapiSendImages(t *testing.T) {
	_, fe, h := webTapiSetup(t)
	before := sentCount(fe)
	if w := doWeb(t, h, sendImagesReq("/api/tapi/send", "これ", testJPEG(t))); w.Code != http.StatusForbidden {
		t.Fatalf("without allow_devices: %d %s", w.Code, w.Body)
	}
	if sentCount(fe) != before || len(imageFiles(t, TapiSlug)) != 0 {
		t.Fatal("typed or saved without allow_devices")
	}

	d, fe, h := webTapiSetup(t, "iphone")
	w := doWeb(t, h, sendImagesReq("/api/tapi/send", "この不具合を誰かに", testPNG(t, 5, 5)))
	if w.Code != 200 {
		t.Fatalf("send: %d %s", w.Code, w.Body)
	}
	typed := lastText(fe)
	files := typedImages(typed)
	if len(files) != 1 || filepath.Dir(files[0]) != filepath.Join(paths.WebImagesDir(), TapiSlug) || !strings.HasPrefix(typed, "この不具合を誰かに\n\n") {
		t.Fatalf("typed: %q", typed)
	}
	if _, err := os.Stat(files[0]); err != nil {
		t.Fatal(err)
	}
	if rs := tapiSends(t, d); len(rs) != 1 || rs[0].Text != typed || !rs[0].OK {
		t.Fatalf("send log: %+v", rs)
	}
	// not an image: refused before anything
	if w := doWeb(t, h, sendImagesReq("/api/tapi/send", "これ", []byte("%PDF-1.7"))); w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("pdf: %d", w.Code)
	}
	// a permission prompt: 409, nothing saved, the record says how many
	d.tapiUpdate(func(ts *tapiState) {
		ts.state, ts.waitKind, ts.pending = state.Waiting, "permission", &state.Tool{Name: "Bash", Input: "gh pr merge 42"}
	})
	if w := doWeb(t, h, sendImagesReq("/api/tapi/send", "2つ目も", testJPEG(t), testJPEG(t))); w.Code != http.StatusConflict {
		t.Fatalf("over a permission prompt: %d %s", w.Code, w.Body)
	}
	if fs := imageFiles(t, TapiSlug); len(fs) != 1 {
		t.Fatalf("saved for a refused send: %v", fs)
	}
	if rs := tapiSends(t, d); len(rs) != 2 || rs[0].OK || rs[0].Text != "2つ目も\n\n[image] ×2 (not saved: the send was refused)" {
		t.Fatalf("refused record: %+v", rs[0])
	}
}

// rm takes the session's images along; the rest go after webImageKeep.
func TestWebImagesCleanup(t *testing.T) {
	d, _, repo, h := webSetup(t)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	if w := doWeb(t, h, sendImagesReq("/api/sessions/main/send", "", testJPEG(t))); w.Code != 200 {
		t.Fatalf("send: %d %s", w.Code, w.Body)
	}
	// another session's (here: tapi's) are not touched by main's rm
	other := filepath.Join(paths.WebImagesDir(), TapiSlug)
	paths.Ensure(other)
	keep := filepath.Join(other, "new.png")
	old := filepath.Join(other, "old.png")
	os.WriteFile(keep, []byte("x"), 0o600)
	os.WriteFile(old, []byte("x"), 0o600)
	call[any](t, d, "worktree.rm", map[string]any{"slug": "main", "force": true})
	if _, err := os.Stat(filepath.Join(paths.WebImagesDir(), "main")); !os.IsNotExist(err) {
		t.Fatalf("main's images left after rm: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("rm took another session's images")
	}

	ago := time.Now().Add(-webImageKeep - time.Hour)
	os.Chtimes(old, ago, ago)
	sweepWebImages(time.Now())
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("an image past webImageKeep is still there")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Error("a recent image was swept")
	}
	os.Chtimes(keep, ago, ago)
	sweepWebImages(time.Now())
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Error("an emptied directory is left")
	}
	for _, s := range []string{"", ".", "..", "a/b", `a\b`} {
		if webImageDirOK(s) {
			t.Errorf("%q names an image directory", s)
		}
	}
}

// The agent may open its own images with Read (no prompt), Bash may read
// them (not under denyRead), and the secrets stay unreadable.
func TestClaudeSandboxImages(t *testing.T) {
	d, _, repo := setup(t)
	for _, slug := range []string{"feat-x", TapiSlug} {
		c := decodeSettings(t, d.claudeSandboxSettings(slug, []string{repo}, nil))
		dir := webImageDir(slug)
		if !slices.Contains(c.Permissions.Allow, "Read(/"+dir+"/**)") || !slices.Contains(c.Permissions.Allow, "Read(/"+realpath(dir)+"/**)") {
			t.Errorf("%s: permissions.allow: %v", slug, c.Permissions.Allow)
		}
		for _, a := range c.Permissions.Allow {
			if !strings.HasPrefix(a, "Read(/") || strings.Contains(a, "Edit") {
				t.Errorf("%s: allow rule %s", slug, a)
			}
		}
		for _, p := range c.Sandbox.Filesystem.DenyRead {
			for _, im := range []string{dir, realpath(dir)} {
				if im == p || strings.HasPrefix(im, p+"/") {
					t.Errorf("%s: its images are under denyRead %s", slug, p)
				}
			}
		}
		for _, secret := range []string{paths.HumanTokenFile(), paths.TokenFile(), paths.StateDB()} {
			if !slices.Contains(c.Sandbox.Filesystem.DenyRead, secret) || !slices.Contains(c.Permissions.Deny, "Read(/"+secret+")") {
				t.Errorf("%s: %s readable", slug, secret)
			}
			if strings.HasPrefix(secret, paths.WebImagesDir()+"/") {
				t.Errorf("a secret under the images: %s", secret)
			}
		}
	}
	// not another session's
	c := decodeSettings(t, d.claudeSandboxSettings("feat-x", []string{repo}, nil))
	for _, a := range c.Permissions.Allow {
		if strings.Contains(a, "/images/feat-y") || strings.HasSuffix(a, "/images/**)") {
			t.Errorf("allow rule reaches other sessions' images: %s", a)
		}
	}
}
