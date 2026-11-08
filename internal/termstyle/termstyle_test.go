package termstyle

import (
	"reflect"
	"testing"
)

func TestLine(t *testing.T) {
	cases := []struct {
		name, in string
		want     []Run
	}{
		{"plain", "hello  ", []Run{{Text: "hello"}}},
		{"16 colors to roles", "\x1b[31mERR\x1b[0m ok \x1b[92mpass\x1b[39m.",
			[]Run{{Text: "ERR", Fg: "err"}, {Text: " ok "}, {Text: "pass", Fg: "ok"}, {Text: "."}}},
		{"256 and truecolor", "\x1b[38;5;208ma\x1b[38;2;215;119;87mb\x1b[48;5;237mc",
			[]Run{{Text: "a", Fg: "#ff8700"}, {Text: "b", Fg: "#d77757"}, {Text: "c", Fg: "#d77757", Bg: "#3a3a3a"}}},
		{"colon form", "\x1b[38:2::1:2:3mx\x1b[4:3my\x1b[4:0mz",
			[]Run{{Text: "x", Fg: "#010203"}, {Text: "y", Fg: "#010203", Underline: true}, {Text: "z", Fg: "#010203"}}},
		{"attributes", "\x1b[1;3mB\x1b[22;23;2mf\x1b[0m\x1b[9ms",
			[]Run{{Text: "B", Bold: true, Italic: true}, {Text: "f", Faint: true}, {Text: "s", Strike: true}}},
		{"reverse with defaults", "\x1b[7m cursor \x1b[27m",
			[]Run{{Text: " cursor", Fg: "bg", Bg: "text"}}},
		{"same style merges", "\x1b[32ma\x1b[32mb", []Run{{Text: "ab", Fg: "ok"}}},
		{"links, other escapes and controls dropped", "\x1b]8;;https://x\x07link\x1b]8;;\x1b\\\x1b[2K\x1b[?25l\r\x00tab\there",
			[]Run{{Text: "linktab\there"}}},
		{"open bg at the end (erase line) adds nothing", "  \x1b[1mHead\x1b[0m\x1b[48;5;237m   ",
			[]Run{{Text: "  "}, {Text: "Head", Bold: true}}},
		{"underline colour is skipped", "\x1b[58;2;0;0;35mx\x1b[58:5:9my\x1b[59mz",
			[]Run{{Text: "xyz"}}},
		{"cut escape", "abc\x1b[3", []Run{{Text: "abc"}}},
		{"bad truecolor", "\x1b[38;2;999;0;0mx", []Run{{Text: "x"}}},
		{"markup is text", "<img src=x onerror=alert(1)>&amp;", []Run{{Text: "<img src=x onerror=alert(1)>&amp;"}}},
		{"empty", "\x1b[0m   ", []Run{}},
	}
	for _, c := range cases {
		if got := Line(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
	}
}
