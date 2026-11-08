package engine

import (
	"strings"
	"testing"
)

func TestSignalName(t *testing.T) {
	for in, want := range map[string]string{"": "", "0": "", "int": "SIGINT", "TERM": "SIGTERM", "2": "SIGINT", "9": "SIGKILL", "31": "signal 31"} {
		if got := SignalName(in); got != want {
			t.Errorf("SignalName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain テキスト", "plain テキスト"},
		{"a\nb\tc", "a\nb\tc"},
		{"a\r\nb\rc", "a\nb\nc"},
		{"AAA\x1b[201~\x1b[ZBBB\x03CCC", "AAA[201~[ZBBBCCC"},
		{"x\x00y\x7fz\u009b1~", "xyz1~"},
		{"\x1b\x03", ""},
	}
	for _, c := range cases {
		if got := CleanText(c.in); got != c.want {
			t.Errorf("CleanText(%q) = %q, want %q", c.in, got, c.want)
		}
		if HasControl(c.in) != (CleanText(c.in) != strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(c.in)) {
			t.Errorf("HasControl(%q) disagrees with CleanText", c.in)
		}
	}
}
