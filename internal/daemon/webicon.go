package daemon

// Cache busting for the home screen icons (webui/icon.png, icon-512.png)
// and the favicons (favicon-32.png, favicon-16.png: the same picture
// cropped close to tapi, so it reads in a browser tab).
// iOS keeps the icon it saw for a URL (the "Add to Home Screen" sheet
// showed the old picture even after the PNG changed), so the pages and the
// manifest refer to /icon.png?v=<first 8 hex of its sha256>: a new picture
// is a new URL, with nothing to edit by hand. The versions are worked out
// from the embedded files and put into the pages as they are served
// (webVersionedFS); the plain /icon.png stays for clients that have the
// old pages.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"strings"
)

// webIconNames are the icons served at /<name> (web.go).
var webIconNames = []string{"icon.png", "icon-512.png", "favicon-32.png", "favicon-16.png"}

// webIconFiles are the files (paths in webui) that refer to the icons.
var webIconFiles = map[string]bool{
	"index.html":           true,
	"gui/index.html":       true,
	"manifest.webmanifest": true,
}

// webIconVersions maps an icon's name to its version: the first 8 hex
// digits of the sha256 of the embedded PNG.
var webIconVersions = func() map[string]string {
	m := map[string]string{}
	for _, name := range webIconNames {
		b, err := webFiles.ReadFile("webui/" + name)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(b)
		m[name] = hex.EncodeToString(sum[:])[:8]
	}
	return m
}()

// webIconURL is the versioned URL of an icon.
func webIconURL(name string) string {
	return "/" + name + "?v=" + webIconVersions[name]
}

// webVersionIcons rewrites the quoted icon URLs ("/icon.png") in a page or
// the manifest into their versioned form.
func webVersionIcons(b []byte) []byte {
	var pairs []string
	for _, name := range webIconNames {
		pairs = append(pairs, `"/`+name+`"`, `"`+webIconURL(name)+`"`)
	}
	return []byte(strings.NewReplacer(pairs...).Replace(string(b)))
}

// webVersionedFS is webui with the icon URLs versioned in webIconFiles.
// Every way to the pages (/, /index.html, /gui/, a surface's index) goes
// through it, so none of them hands out the bare URL.
type webVersionedFS struct{ fs.FS }

func (v webVersionedFS) Open(name string) (fs.File, error) {
	f, err := v.FS.Open(name)
	if err != nil || !webIconFiles[name] {
		return f, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	b = webVersionIcons(b)
	return &webMemFile{Reader: bytes.NewReader(b), info: webMemInfo{st, int64(len(b))}}, nil
}

// webMemFile is a rewritten file, seekable so http.ServeContent can size it.
type webMemFile struct {
	*bytes.Reader
	info webMemInfo
}

func (f *webMemFile) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f *webMemFile) Close() error               { return nil }

// webMemInfo is the original file's info with the rewritten size.
type webMemInfo struct {
	fs.FileInfo
	size int64
}

func (i webMemInfo) Size() int64 { return i.size }
