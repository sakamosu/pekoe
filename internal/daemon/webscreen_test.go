package daemon

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"boba/internal/termstyle"
)

// What the agent prints reaches the page only as run text. Colors are a
// known role or #rrggbb, whatever bytes the agent sends, so nothing from
// the screen can turn into markup or a style of its own.
func TestWebScreenColors(t *testing.T) {
	d, fe, repo, h := webSetup(t)
	waitingSession(t, d, repo)
	fe.mu.Lock()
	fe.lines = []string{
		"\x1b[31m<script>alert(1)</script>\x1b[0m",
		"\x1b[38;2;1;2;3m\"></pre><img src=x onerror=alert(2)>",
		"\x1b[38;5;300mx\x1b[38;2;1;2mx\x1b[38;9;1mx\x1b[48;2;-1;0;0mx",
		"\x1b]8;;javascript:alert(3)\x07click\x1b]8;;\x07",
		"",
	}
	fe.mu.Unlock()
	w := doWeb(t, h, webReq{path: "/api/sessions/main/screen", from: ipPhone})
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var res struct {
		Rows  [][]map[string]any
		Lines []string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.Body.String(), "<script") || strings.Contains(w.Body.String(), "javascript:") {
		t.Fatalf("raw markup or a link target in the JSON: %s", w.Body)
	}
	if len(res.Rows) != 4 || len(res.Lines) != 4 {
		t.Fatalf("rows %d lines %d: %s", len(res.Rows), len(res.Lines), w.Body)
	}
	if res.Lines[0] != "<script>alert(1)</script>" || res.Lines[3] != "click" {
		t.Fatalf("lines: %q", res.Lines)
	}
	hex := regexp.MustCompile(`^#[0-9a-f]{6}$`)
	allowed := map[string]bool{"t": true, "fg": true, "bg": true, "b": true, "d": true, "i": true, "u": true, "s": true}
	for _, row := range res.Rows {
		for _, run := range row {
			for k, v := range run {
				if !allowed[k] {
					t.Errorf("unexpected key %q in %v", k, run)
				}
				if k == "fg" || k == "bg" {
					c, _ := v.(string)
					if !termstyle.ColorRoles[c] && !hex.MatchString(c) {
						t.Errorf("color %q is neither a role nor #rrggbb", c)
					}
				}
			}
		}
	}
	if fg := res.Rows[0][0]["fg"]; fg != "err" {
		t.Fatalf("red is the err role: %v", res.Rows[0])
	}
}
