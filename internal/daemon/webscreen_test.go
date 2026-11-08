package daemon

import (
	"encoding/json"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestStyledLine(t *testing.T) {
	cases := []struct {
		name, in string
		want     []webRun
	}{
		{"plain", "hello  ", []webRun{{Text: "hello"}}},
		{"16 colors to roles", "\x1b[31mERR\x1b[0m ok \x1b[92mpass\x1b[39m.",
			[]webRun{{Text: "ERR", Fg: "err"}, {Text: " ok "}, {Text: "pass", Fg: "ok"}, {Text: "."}}},
		{"256 and truecolor", "\x1b[38;5;208ma\x1b[38;2;215;119;87mb\x1b[48;5;237mc",
			[]webRun{{Text: "a", Fg: "#ff8700"}, {Text: "b", Fg: "#d77757"}, {Text: "c", Fg: "#d77757", Bg: "#3a3a3a"}}},
		{"colon form", "\x1b[38:2::1:2:3mx\x1b[4:3my\x1b[4:0mz",
			[]webRun{{Text: "x", Fg: "#010203"}, {Text: "y", Fg: "#010203", Underline: true}, {Text: "z", Fg: "#010203"}}},
		{"attributes", "\x1b[1;3mB\x1b[22;23;2mf\x1b[0m\x1b[9ms",
			[]webRun{{Text: "B", Bold: true, Italic: true}, {Text: "f", Faint: true}, {Text: "s", Strike: true}}},
		{"reverse with defaults", "\x1b[7m cursor \x1b[27m",
			[]webRun{{Text: " cursor", Fg: "bg", Bg: "text"}}},
		{"same style merges", "\x1b[32ma\x1b[32mb", []webRun{{Text: "ab", Fg: "ok"}}},
		{"links, other escapes and controls dropped", "\x1b]8;;https://x\x07link\x1b]8;;\x1b\\\x1b[2K\x1b[?25l\r\x00tab\there",
			[]webRun{{Text: "linktab\there"}}},
		{"open bg at the end (erase line) adds nothing", "  \x1b[1mHead\x1b[0m\x1b[48;5;237m   ",
			[]webRun{{Text: "  "}, {Text: "Head", Bold: true}}},
		{"cut escape", "abc\x1b[3", []webRun{{Text: "abc"}}},
		{"bad truecolor", "\x1b[38;2;999;0;0mx", []webRun{{Text: "x"}}},
		{"markup is text", "<img src=x onerror=alert(1)>&amp;", []webRun{{Text: "<img src=x onerror=alert(1)>&amp;"}}},
		{"empty", "\x1b[0m   ", []webRun{}},
	}
	for _, c := range cases {
		if got := styledLine(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
	}
}

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
					if !webColorRoles[c] && !hex.MatchString(c) {
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
