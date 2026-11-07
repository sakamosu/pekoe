package daemon

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"boba/internal/engine"
)

// Kitty graphics passthrough (design: 入れ子の税).
//
// The right pane is redrawn from tmux's screen, so images placed at the
// cursor cannot follow. Inside tmux, applications use Unicode placeholders
// (U=1): the image data travels as an APC command and U+10EEEE cells mark
// where it goes. The cells come through capture-pane as text; the APC
// commands are taken from the raw pane output (tmux %output) here and sent to
// the outer terminal unchanged, so Ghostty draws the image in the right pane.

var (
	apcGRe    = regexp.MustCompile(`\x1b_G([^\x1b]*)\x1b\\`)
	dcsTmuxRe = regexp.MustCompile(`\x1bPtmux;((?:\x1b\x1b|[^\x1b])*)\x1b\\`)
)

// splitCarry returns the part of data that holds only complete escape
// sequences, and the earliest incomplete OSC / APC / DCS to carry over to
// the next output chunk.
func splitCarry(data []byte) (complete, carry []byte) {
	cut := -1
	consider := func(i int, terminated bool, limit int) {
		if i >= 0 && !terminated && len(data)-i < limit && (cut < 0 || i < cut) {
			cut = i
		}
	}
	if i := bytes.LastIndex(data, []byte("\x1bP")); i >= 0 {
		var term bool
		if bytes.HasPrefix(data[i:], []byte("\x1bPtmux;")) {
			term = dcsTmuxRe.Match(data[i:]) // inner ESCs are doubled
		} else {
			term = bytes.Contains(data[i+2:], []byte("\x1b\\"))
		}
		consider(i, term, 16<<20)
	}
	if i := bytes.LastIndex(data, []byte("\x1b_")); i >= 0 {
		consider(i, bytes.Contains(data[i+2:], []byte("\x1b\\")), 16<<20)
	}
	if i := bytes.LastIndex(data, []byte("\x1b]")); i >= 0 {
		consider(i, bytes.Contains(data[i+2:], []byte("\x1b\\")) || bytes.IndexByte(data[i+2:], 0x07) >= 0, 4096)
	}
	if cut < 0 {
		return data, nil
	}
	return data[:cut], append([]byte(nil), data[cut:]...)
}

type gfxCmd struct {
	raw  []byte            // ESC _ G … ESC \
	keys map[string]string // control data
}

func graphicsCommands(data []byte) []gfxCmd {
	var out []gfxCmd
	add := func(b []byte) {
		for _, m := range apcGRe.FindAllSubmatch(b, -1) {
			ctrl := string(m[1])
			if i := strings.IndexByte(ctrl, ';'); i >= 0 {
				ctrl = ctrl[:i]
			}
			keys := map[string]string{}
			for _, kv := range strings.Split(ctrl, ",") {
				if k, v, ok := strings.Cut(kv, "="); ok {
					keys[k] = v
				}
			}
			out = append(out, gfxCmd{raw: append([]byte(nil), m[0]...), keys: keys})
		}
	}
	for _, m := range dcsTmuxRe.FindAllSubmatch(data, -1) {
		add(bytes.ReplaceAll(m[1], []byte("\x1b\x1b"), []byte("\x1b")))
	}
	add(dcsTmuxRe.ReplaceAll(data, nil))
	return out
}

// passGraphics forwards image data to the outer terminal and answers
// capability queries on its behalf.
func (d *Daemon) passGraphics(slug, window string, data []byte) {
	for _, c := range graphicsCommands(data) {
		switch c.keys["a"] {
		case "q":
			d.mu.Lock()
			ok := d.graphicsOK
			d.mu.Unlock()
			if ok && c.keys["i"] != "" {
				reply := fmt.Sprintf("\x1b_Gi=%s;OK\x1b\\", c.keys["i"])
				_ = d.Eng.SendHex(slug, window, hexBytes([]byte(reply)))
			}
			continue
		case "T", "p":
			if c.keys["U"] != "1" {
				// a cursor placement would land at the wrong spot: keep the data only
				if c.keys["a"] == "p" {
					continue
				}
				c.raw = bytes.Replace(c.raw, []byte("a=T"), []byte("a=t"), 1)
			}
		}
		d.emit("term.passthrough", map[string]any{"slug": slug, "seq": base64.StdEncoding.EncodeToString(c.raw)})
	}
}

func hexBytes(b []byte) []string {
	out := make([]string, len(b))
	for i := range b {
		out[i] = hex.EncodeToString(b[i : i+1])
	}
	return out
}

type capsP struct {
	KittyGraphics bool `json:"kitty_graphics"`
}

// hTermCaps: the TUI reports what the outer terminal can do.
func (d *Daemon) hTermCaps(p capsP) (any, error) {
	d.mu.Lock()
	d.graphicsOK = p.KittyGraphics
	d.mu.Unlock()
	return nil, nil
}

var _ = engine.WinAgent
