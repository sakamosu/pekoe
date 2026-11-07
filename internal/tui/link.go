package tui

import (
	"os/exec"
	"regexp"
	"runtime"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Links in the agent pane. boba holds the mouse, so Ghostty never sees a
// click on an OSC 8 link (or a bare URL) the agent printed; boba resolves the
// cell under the click itself and hands the URL to the OS, as Ghostty would.

// bareURL matches URLs printed without OSC 8; trailing punctuation that
// usually closes a sentence is trimmed by trimURL.
var bareURL = regexp.MustCompile(`(?:https?|file)://[^\s<>"'` + "`" + `]+`)

// linkAt returns the URL under cell x of an ANSI line: the OSC 8 target when
// the cell sits inside one, otherwise a bare URL covering it.
func linkAt(line string, x int) string {
	if x < 0 {
		return ""
	}
	var (
		p     = ansi.NewParser()
		state byte
		col   int
		link  string
		plain strings.Builder
		cols  []int // start column of each byte of plain
	)
	for s := line; len(s) > 0; {
		seq, width, n, ns := ansi.DecodeSequence(s, state, p)
		state = ns
		s = s[n:]
		if width == 0 {
			if strings.HasPrefix(seq, "\x1b]8;") {
				link = osc8Target(seq)
			}
			continue
		}
		if x >= col && x < col+width {
			if link != "" {
				return link
			}
		}
		for i := 0; i < len(seq); i++ {
			cols = append(cols, col)
		}
		plain.WriteString(seq)
		col += width
	}
	text := plain.String()
	for _, loc := range bareURL.FindAllStringIndex(text, -1) {
		u := trimURL(text[loc[0]:loc[1]])
		end := loc[0] + len(u)
		endCol := col
		if end < len(cols) {
			endCol = cols[end]
		}
		if x >= cols[loc[0]] && x < endCol {
			return u
		}
	}
	return ""
}

// osc8Target extracts the URI of "ESC ] 8 ; params ; URI ST" (empty closes).
func osc8Target(seq string) string {
	body := strings.TrimPrefix(seq, "\x1b]8;")
	body = strings.TrimSuffix(strings.TrimSuffix(body, "\x07"), "\x1b\\")
	_, uri, ok := strings.Cut(body, ";")
	if !ok {
		return ""
	}
	return uri
}

func trimURL(u string) string {
	for len(u) > 0 {
		c := u[len(u)-1]
		switch {
		case strings.IndexByte(".,;:!?", c) >= 0:
		case c == ')' && strings.Count(u, "(") < strings.Count(u, ")"):
		case c == ']' && strings.Count(u, "[") < strings.Count(u, "]"):
		default:
			return u
		}
		u = u[:len(u)-1]
	}
	return u
}

// openURL hands a URL to the OS default handler (open / xdg-open).
func openURL(u string) error {
	if u == "" || strings.HasPrefix(u, "-") {
		return nil
	}
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	return exec.Command(name, u).Run()
}
