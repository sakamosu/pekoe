package daemon

// Images sent from the web UI (docs/web.md 「画像の扱い」): a photo or a
// screenshot attached to a send, to a session or to tapi chat. pekoed
// checks that each one is an image by its bytes (PNG, JPEG or GIF: what
// Claude Code reads; the page turns HEIC and the rest into JPEG or PNG
// before sending), names it itself, writes it under the data directory
// (never into a worktree, where it could ride along in a commit) and adds
// one "[image] <path>" line per image to the text, so the agent opens it
// with its Read tool. A Claude Code agent may read its own directory
// without asking (claudeSandboxSettings); the tokens and the rest that
// agents must not read stay where they were.
//
// The images go with the session (rm) and after webImageKeep.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // DecodeConfig: the formats accepted
	_ "image/jpeg"
	_ "image/png"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"pekoe/internal/engine"
	"pekoe/internal/paths"
)

const (
	webMaxImages     = 4       // images in one send
	webMaxImageBytes = 5 << 20 // one image (what Claude takes in one image)
	webMaxImageSide  = 8000    // pixels, width or height (same)
	// webMaxSendBody: a send's body may carry the images (base64 in JSON)
	webMaxSendBody = webMaxImages*(webMaxImageBytes/3*4+8) + webMaxBody
)

// webImageKeep: how long a sent image stays (a removed session's go at once).
var webImageKeep = 7 * 24 * time.Hour

// webImage is one checked image, not written yet.
type webImage struct {
	ext  string // png | jpg | gif
	data []byte
	w, h int
}

// webSendIn is a send's body: the text, and the images (base64 in JSON).
type webSendIn struct {
	Text   string `json:"text"`
	Images []struct {
		Data []byte `json:"data"`
	} `json:"images"`
}

// webSendBody: a send's body is larger than the other writes (the images).
func webSendBody(r *http.Request) bool {
	p := r.URL.Path
	return r.Method == http.MethodPost && (p == "/api/tapi/send" ||
		strings.HasPrefix(p, "/api/sessions/") && strings.HasSuffix(p, "/send"))
}

// webReadSend reads a send's {text, images}: the text trimmed, at most
// webMaxText characters, no control characters; empty only with an image.
// The images checked (webCheckImage). On false it has written the 400.
func webReadSend(w http.ResponseWriter, r *http.Request) (string, []webImage, bool) {
	var in webSendIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		var mb *http.MaxBytesError
		if errors.As(err, &mb) {
			http.Error(w, fmt.Sprintf("too large: at most %d images of %d MB each", webMaxImages, webMaxImageBytes>>20), http.StatusRequestEntityTooLarge)
			return "", nil, false
		}
		http.Error(w, "bad json", http.StatusBadRequest)
		return "", nil, false
	}
	if len(in.Images) > webMaxImages {
		http.Error(w, fmt.Sprintf("at most %d images in one send", webMaxImages), http.StatusBadRequest)
		return "", nil, false
	}
	text := strings.TrimSpace(in.Text)
	if text == "" && len(in.Images) == 0 {
		http.Error(w, "empty text", http.StatusBadRequest)
		return "", nil, false
	}
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) > webMaxText {
		http.Error(w, fmt.Sprintf("text must be at most %d characters", webMaxText), http.StatusBadRequest)
		return "", nil, false
	}
	// text, not keys: an ESC or ^C in it would press keys the keypad never
	// offers (Shift-Tab, interrupt). SendText drops them too; say so here.
	if engine.HasControl(text) {
		http.Error(w, "text must not contain control characters", http.StatusBadRequest)
		return "", nil, false
	}
	imgs := make([]webImage, 0, len(in.Images))
	for i, im := range in.Images {
		img, err := webCheckImage(im.Data)
		if err != nil {
			code := http.StatusBadRequest
			if errors.Is(err, errNotImage) {
				code = http.StatusUnsupportedMediaType
			}
			http.Error(w, fmt.Sprintf("image %d: %v", i+1, err), code)
			return "", nil, false
		}
		imgs = append(imgs, img)
	}
	return text, imgs, true
}

var errNotImage = errors.New("not a PNG, JPEG or GIF image")

// webCheckImage: b is a PNG, JPEG or GIF by its first bytes, its header
// reads as one, and it is within the size limits. What it says it is
// (a name, a type) is not asked: there is nothing else.
func webCheckImage(b []byte) (webImage, error) {
	if len(b) == 0 {
		return webImage{}, errors.New("empty")
	}
	if len(b) > webMaxImageBytes {
		return webImage{}, fmt.Errorf("%d bytes, more than %d MB", len(b), webMaxImageBytes>>20)
	}
	var ext, format string
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		ext, format = "png", "png"
	case bytes.HasPrefix(b, []byte{0xff, 0xd8, 0xff}):
		ext, format = "jpg", "jpeg"
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		ext, format = "gif", "gif"
	case len(b) >= 12 && string(b[4:8]) == "ftyp" && strings.HasPrefix(string(b[8:12]), "he"):
		return webImage{}, fmt.Errorf("%w (HEIC: the page sends it as JPEG; update the page)", errNotImage)
	default:
		return webImage{}, errNotImage
	}
	cfg, got, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || got != format {
		return webImage{}, fmt.Errorf("%w (its header does not read: %v)", errNotImage, err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > webMaxImageSide || cfg.Height > webMaxImageSide {
		return webImage{}, fmt.Errorf("%d×%d pixels: at most %d on a side", cfg.Width, cfg.Height, webMaxImageSide)
	}
	return webImage{ext: ext, data: b, w: cfg.Width, h: cfg.Height}, nil
}

// webImageDir is where the images sent to slug go: the data directory's
// images/<slug> (tapi chat's: images/+tapi; no session slug has a "+").
func webImageDir(slug string) string {
	return filepath.Join(paths.WebImagesDir(), slug)
}

// webImageDirOK: slug names one directory right below images/.
func webImageDirOK(slug string) bool {
	return slug != "" && slug != "." && slug != ".." && !strings.ContainsAny(slug, `/\`)
}

// saveWebImages writes imgs for slug, named by pekoed (time, a random
// part, the number), mode 0600, and returns their paths.
func saveWebImages(slug string, imgs []webImage) ([]string, error) {
	if len(imgs) == 0 {
		return nil, nil
	}
	if !webImageDirOK(slug) {
		return nil, fmt.Errorf("no image directory for %q", slug)
	}
	dir := webImageDir(slug)
	if err := paths.Ensure(dir); err != nil {
		return nil, err
	}
	var rnd [4]byte
	_, _ = rand.Read(rnd[:])
	stamp := time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(rnd[:])
	var out []string
	for i, im := range imgs {
		p := filepath.Join(dir, fmt.Sprintf("%s-%d.%s", stamp, i+1, im.ext))
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, paths.FilePerm)
		if err == nil {
			_, err = f.Write(im.data)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
		if err != nil {
			os.Remove(p)
			removeFiles(out)
			return nil, fmt.Errorf("saving image %d: %w", i+1, err)
		}
		out = append(out, p)
	}
	return out, nil
}

func removeFiles(ps []string) {
	for _, p := range ps {
		os.Remove(p)
	}
}

// withImageLines is what is typed: the text, then one "[image] <path>" line
// per image, so the agent opens each with its Read tool.
func withImageLines(text string, files []string) string {
	if len(files) == 0 {
		return text
	}
	var b strings.Builder
	b.WriteString(text)
	if text != "" {
		b.WriteString("\n\n")
	}
	for i, f := range files {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("[image] " + f)
	}
	return b.String()
}

// webStoreImages saves imgs for slug and returns what to type with the
// paths in it. On an error it has written the 500.
func webStoreImages(w http.ResponseWriter, slug, text string, imgs []webImage) (string, []string, bool) {
	files, err := saveWebImages(slug, imgs)
	if err != nil {
		log.Printf("web: images for %s: %v", slug, err)
		http.Error(w, "could not save the images: "+err.Error(), http.StatusInternalServerError)
		return "", nil, false
	}
	return withImageLines(text, files), files, true
}

// keepImagesAfter: a failed send leaves its images only when the text was
// typed (Enter held back): the paths are in the agent's input box.
func keepImagesAfter(err error, files []string) {
	if err != nil && !errors.Is(err, errPromptUpTyped) {
		removeFiles(files)
	}
}

// imagesNote is the part of a log line about the images.
func imagesNote(files []string) string {
	if len(files) == 0 {
		return ""
	}
	return fmt.Sprintf(" with %d image(s) %s", len(files), strings.Join(files, " "))
}

// removeWebImages deletes the images sent to a removed session.
func removeWebImages(slug string) {
	if !webImageDirOK(slug) {
		return
	}
	if err := os.RemoveAll(webImageDir(slug)); err != nil {
		log.Printf("web: removing the images of %s: %v", slug, err)
	}
}

// sweepWebImages deletes the images older than webImageKeep, and the
// directories left empty.
func sweepWebImages(now time.Time) {
	root := paths.WebImagesDir()
	ents, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range ents {
		if !e.IsDir() || e.Type()&fs.ModeSymlink != 0 {
			continue
		}
		dir := filepath.Join(root, e.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		left := 0
		for _, f := range files {
			info, err := f.Info()
			if err != nil || !f.Type().IsRegular() {
				left++
				continue
			}
			if now.Sub(info.ModTime()) > webImageKeep {
				if os.Remove(filepath.Join(dir, f.Name())) == nil {
					continue
				}
			}
			left++
		}
		if left == 0 {
			os.Remove(dir) // only if empty
		}
	}
}

// withImageCount is a refused send's record: the images were not saved,
// so the record says how many there were instead of their paths.
func withImageCount(text string, n int) string {
	if n == 0 {
		return text
	}
	line := fmt.Sprintf("[image] ×%d (not saved: the send was refused)", n)
	if text == "" {
		return line
	}
	return text + "\n\n" + line
}
