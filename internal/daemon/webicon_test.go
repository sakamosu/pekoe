package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The pages and the manifest refer to the icons by a URL carrying the
// first 8 hex of the PNG's sha256, so a new picture is a new URL (iOS
// keeps the icon it saw for a URL); that URL is cached for good and serves
// the same bytes as the bare one, which stays for old pages.
func TestWebIconVersioned(t *testing.T) {
	_, _, _, h := webSetup(t)
	for _, name := range webIconNames {
		png, _ := webFiles.ReadFile("webui/" + name)
		sum := sha256.Sum256(png)
		want := "/" + name + "?v=" + hex.EncodeToString(sum[:])[:8]
		if got := webIconURL(name); got != want {
			t.Fatalf("%s: url %q, want %q", name, got, want)
		}
		w := doWeb(t, h, webReq{path: want, from: ipPhone})
		if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || !bytes.Equal(w.Body.Bytes(), png) {
			t.Fatalf("%s: %d %q, %d bytes", want, w.Code, w.Header().Get("Content-Type"), w.Body.Len())
		}
		if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") || !strings.Contains(cc, "max-age=31536000") {
			t.Errorf("%s: Cache-Control %q", want, cc)
		}
		// a stale version is not promised forever
		w = doWeb(t, h, webReq{path: "/" + name + "?v=00000000", from: ipPhone})
		if w.Code != 200 || w.Header().Get("Cache-Control") != "max-age=86400" {
			t.Errorf("%s stale: %d %q", name, w.Code, w.Header().Get("Cache-Control"))
		}
	}

	bare := regexp.MustCompile(`"/(icon(-512)?|favicon-(16|32))\.png"`)
	check := func(what, body string, urls ...string) {
		t.Helper()
		if bare.MatchString(body) {
			t.Errorf("%s: an unversioned icon URL: %s", what, bare.FindString(body))
		}
		for _, u := range urls {
			if !strings.Contains(body, `"`+u+`"`) {
				t.Errorf("%s: no %s", what, u)
			}
		}
	}
	icon, icon512 := webIconURL("icon.png"), webIconURL("icon-512.png")
	// the tab's icon is the favicons (with their sizes), not the home
	// screen icon; the home screen icon stays apple-touch-icon's
	favicons := func(what, body string) {
		t.Helper()
		for _, s := range []string{"32", "16"} {
			link := `<link rel="icon" type="image/png" sizes="` + s + `x` + s + `" href="` + webIconURL("favicon-"+s+".png") + `">`
			if !strings.Contains(body, link) {
				t.Errorf("%s: no %s", what, link)
			}
		}
		if strings.Contains(body, `rel="icon" href="`+icon+`"`) {
			t.Errorf("%s: the tab's icon is still the home screen icon", what)
		}
	}
	for _, path := range []string{"/", "/index.html"} {
		w := doWeb(t, h, webReq{path: path, from: ipPhone})
		if path == "/index.html" && w.Code == 301 {
			continue // FileServer sends /index.html to /
		}
		// Content-Length is the rewritten page's, not the embedded one's
		if w.Code != 200 || w.Header().Get("Content-Length") != strconv.Itoa(w.Body.Len()) {
			t.Fatalf("%s: %d len %q body %d", path, w.Code, w.Header().Get("Content-Length"), w.Body.Len())
		}
		check(path, w.Body.String(), icon)
		favicons(path, w.Body.String())
		if !strings.Contains(w.Body.String(), `rel="apple-touch-icon" href="`+icon+`"`) {
			t.Errorf("%s: apple-touch-icon not versioned", path)
		}
	}
	w := doWeb(t, h, webReq{path: "/manifest.webmanifest", from: ipPhone})
	check("manifest", w.Body.String(), icon, icon512)

	// the local GUI's page, at / and at /gui/
	gui := d0(t).webMux(webSurface{index: "gui/index.html", gui: true})
	for _, path := range []string{"/", "/gui/"} {
		rec := httptest.NewRecorder()
		gui.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "data-gui>") {
			t.Fatalf("gui %s: %d", path, rec.Code)
		}
		check("gui "+path, rec.Body.String())
		favicons("gui "+path, rec.Body.String())
	}
}

func d0(t *testing.T) *Daemon {
	d, _, _ := setup(t)
	return d
}
