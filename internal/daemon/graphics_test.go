package daemon

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestSplitCarry(t *testing.T) {
	whole := "a\x1b_Gf=100,a=T,U=1;QUJD\x1b\\b"
	for cut := 1; cut < len(whole); cut++ {
		c1, carry := splitCarry([]byte(whole[:cut]))
		c2, rest := splitCarry(append(carry, whole[cut:]...))
		if rest != nil {
			t.Fatalf("cut %d: leftover %q", cut, rest)
		}
		if got := string(c1) + string(c2); got != whole {
			t.Fatalf("cut %d: %q", cut, got)
		}
		if n := len(graphicsCommands(append(c1, c2...))); n != 1 {
			t.Fatalf("cut %d: %d commands", cut, n)
		}
	}
}

func TestGraphicsCommandsUnwrapsTmuxPassthrough(t *testing.T) {
	inner := "\x1b_Ga=T,U=1,i=7;QUJD\x1b\\"
	wrapped := "\x1bPtmux;" + strings.ReplaceAll(inner, "\x1b", "\x1b\x1b") + "\x1b\\"
	cs := graphicsCommands([]byte("x" + wrapped + "y"))
	if len(cs) != 1 || string(cs[0].raw) != inner || cs[0].keys["i"] != "7" {
		t.Fatalf("%+v", cs)
	}
}

func TestPassGraphics(t *testing.T) {
	d, fe, _ := setup(t)
	var sent []string
	d.testHook = func(ev string, data any) {
		if ev == "term.passthrough" {
			b, _ := base64.StdEncoding.DecodeString(data.(map[string]any)["seq"].(string))
			sent = append(sent, string(b))
		}
	}
	d.passGraphics("s", "agent", []byte("\x1b_Ga=T,U=1,i=1;AA\x1b\\\x1b_Ga=T,i=2;AA\x1b\\\x1b_Ga=p,i=3\x1b\\"))
	if len(sent) != 2 || !strings.Contains(sent[0], "a=T,U=1") || !strings.Contains(sent[1], "a=t,i=2") {
		t.Fatalf("passthrough: %q", sent)
	}
	// capability query: answered only when the outer terminal supports it
	d.passGraphics("s", "agent", []byte("\x1b_Ga=q,i=31;AA\x1b\\"))
	if strings.Contains(fe.last(), "OK") {
		t.Fatal("answered without capability")
	}
	d.hTermCaps(capsP{KittyGraphics: true})
	d.passGraphics("s", "agent", []byte("\x1b_Ga=q,i=31;AA\x1b\\"))
	if len(sent) != 2 {
		t.Fatal("queries must not be forwarded")
	}
	if !strings.Contains(fe.last(), "hex:\x1b_Gi=31;OK\x1b\\") {
		t.Fatalf("query not answered: %q", fe.last())
	}
}
