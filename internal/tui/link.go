package tui

import (
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// Links in the agent pane. pekoe holds the mouse, so Ghostty never sees a
// click on an OSC 8 link (or a bare URL) the agent printed; pekoe resolves the
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

// linkClick opens a link with a left click on it: the press remembers the
// URL under the pointer (u), the release opens it when it is still on it.
func (m *model) linkClick(ev tea.MouseMsg, u string) tea.Cmd {
	switch ev.Action {
	case tea.MouseActionPress:
		m.linkDown = u
	case tea.MouseActionRelease:
		down := m.linkDown
		m.linkDown = ""
		if down != "" && down == u {
			return func() tea.Msg {
				if err := openURL(u); err != nil {
					return errMsg{err}
				}
				return infoMsg("opened " + u)
			}
		}
	}
	return nil
}

// openURL hands a URL to the OS default handler (open / xdg-open), when it
// is an http or https link (checkWebURL). Agents print the links pekoe opens,
// and an OSC 8 link shows any text over its URI: a file:// URI, or a scheme
// some app registered, would have open start a program outside the agent's
// sandbox (a script the agent wrote in its worktree, say).
func openURL(u string) error {
	if u == "" {
		return nil
	}
	if err := checkWebURL(u); err != nil {
		return err
	}
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	return exec.Command(name, u).Run()
}

// checkWebURL refuses what openURL does not open: anything but an http or https
// URL with a host. The error shows the URL as it is (quoted: it may hold
// characters a terminal would act on) and why it stays closed.
func checkWebURL(u string) error {
	p, err := url.Parse(u)
	if err == nil && (p.Scheme == "http" || p.Scheme == "https") && p.Host != "" && !strings.HasPrefix(u, "-") {
		return nil
	}
	shown := u
	if len(shown) > 200 {
		shown = shown[:200] + "…"
	}
	why := "not a web link"
	if err == nil && p.Scheme != "" && p.Scheme != "http" && p.Scheme != "https" {
		why = p.Scheme + ": links could start a program"
	}
	return fmt.Errorf("not opened: pekoe opens only http and https links (%s): %q", why, shown)
}
