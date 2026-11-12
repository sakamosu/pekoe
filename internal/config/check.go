package config

// Checking config.toml (`pekoe config check`, and `pekoe config edit` after
// the editor closes): what LoadGlobal would not tell. A file that does not
// parse is an error (LoadGlobal fails: pekoed does not start, the TUI runs
// on the defaults); everything else is a warning — a key pekoe does not
// know (a typo reads as unset) or a value it does not take (it falls back
// to the default).

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Problem is one thing wrong in config.toml.
type Problem struct {
	Key   string // dotted key ("theme.mode"); "" for a syntax error
	Msg   string
	Fatal bool // the file does not parse: nothing in it is used
}

func (p Problem) String() string {
	if p.Key == "" {
		return p.Msg
	}
	return p.Key + ": " + p.Msg
}

// Check reads config.toml's contents and lists what is wrong with them,
// syntax error first; nil when nothing is.
func Check(b []byte) []Problem {
	g := &Global{}
	md, err := toml.Decode(string(b), g)
	if err != nil {
		msg := "cannot be read: " + err.Error() // e.g. a number where text goes
		var pe toml.ParseError
		if errors.As(err, &pe) {
			msg = fmt.Sprintf("syntax error: line %d: %s", pe.Position.Line, pe.Message)
		}
		return []Problem{{Msg: msg, Fatal: true}}
	}
	var out []Problem
	add := func(key, format string, a ...any) {
		out = append(out, Problem{Key: key, Msg: fmt.Sprintf(format, a...)})
	}
	for _, k := range md.Undecoded() {
		add(k.String(), "unknown key (ignored)")
	}
	oneOf := func(key, v string, fold bool, allowed ...string) {
		if v == "" {
			return
		}
		for _, a := range allowed {
			if v == a || fold && strings.EqualFold(v, a) {
				return
			}
		}
		add(key, "%q is not one of %s (the default is used)", v, strings.Join(allowed, " | "))
	}
	port := func(key string, n int) {
		if n < 0 || n > 65535 {
			add(key, "%d is not a port (1-65535)", n)
		}
	}

	port("http.port", g.HTTP.Port)
	port("ports.base", g.Ports.Base)

	oneOf("theme.mode", g.Theme.Mode, false, "auto", "truecolor", "ansi", "none")
	oneOf("theme.icons", g.Theme.Icons, false, "nerd", "unicode", "ascii")
	for _, k := range sortedKeys(g.Theme.Colors) {
		hexColor(add, "theme.colors."+k, g.Theme.Colors[k])
	}
	for _, k := range sortedKeys(g.Theme.ANSI) {
		if n := g.Theme.ANSI[k]; n < 0 || n > 255 {
			add("theme.ansi."+k, "%d is not an ANSI color (0-255)", n)
		}
	}

	oneOf("mascot.shape", g.Mascot.Shape, false, "auto", "round", "block")
	oneOf("mascot.glyphs", g.Mascot.Glyphs, false, "auto", "unicode", "narrow", "ascii")
	oneOf("mascot.palette", g.Mascot.Palette, true, "dark", "light", "D", "E")
	for _, k := range sortedKeys(g.Mascot.Colors) {
		if !slices.Contains([]string{"hi", "bd", "sh", "eye"}, k) {
			add("mascot.colors."+k, "unknown color (hi | bd | sh | eye; ignored)")
			continue
		}
		hexColor(add, "mascot.colors."+k, g.Mascot.Colors[k])
	}

	if s := g.Review.Surface; s != "" && s != "nvim" && s != "tui" {
		add("review.surface", "%q is not one of nvim | tui (the built-in tui surface is used)", s)
	}
	oneOf("orchestrator.notify", g.Orchestrator.Notify, true, "escalate", "quiet")
	if s := strings.TrimSpace(g.Cleanup.StaleAfter); s != "" {
		if d, err := time.ParseDuration(s); err != nil || d <= 0 {
			add("cleanup.stale_after", "%q is not a positive Go duration like 72h or 36h30m (%s is used)", g.Cleanup.StaleAfter, fmtDur(DefaultStaleAfter))
		}
	}

	durs := []struct {
		key, v   string
		def, min time.Duration
	}{
		{"notify.blink_after", g.Notify.BlinkAfter, DefaultBlinkAfter, MinBlinkAfter},
		{"notify.os_after", g.Notify.OSAfter, DefaultOSAfter, MinOSAfter},
		{"notify.toast_for", g.Notify.ToastFor, DefaultToastFor, MinToastFor},
		{"mascot.sleep_after", g.Mascot.SleepAfter, DefaultSleepAfter, MinSleepAfter},
		{"pr.poll_every", g.PR.PollEvery, DefaultPRPollEvery, MinPRPollEvery},
	}
	for _, c := range durs {
		s := strings.TrimSpace(c.v)
		if s == "" {
			continue
		}
		switch d, err := time.ParseDuration(s); {
		case err != nil || d <= 0:
			add(c.key, "%q is not a positive Go duration like 30s or 2m (%s is used)", c.v, fmtDur(c.def))
		case d < c.min:
			add(c.key, "%s is below the minimum %s (%s is used)", s, fmtDur(c.min), fmtDur(c.min))
		}
	}

	agents := DefaultAgents()
	for _, name := range sortedKeys(g.Agents) {
		a := g.Agents[name]
		key := "agents." + name
		oneOf(key+".prompt", a.Prompt, false, "arg", "paste")
		if _, builtin := agents[name]; !builtin && strings.TrimSpace(a.Command) == "" {
			add(key+".command", "missing: an agent of your own needs a command")
		}
		agents[name] = a
	}
	if t := g.Tapi.Agent; t != "" {
		if _, ok := agents[t]; !ok {
			add("tapi.agent", "%q is not in [agents.*] (%s)", t, strings.Join(sortedKeys(agents), ", "))
		}
	}

	if g.Web.Enable && len(g.Web.AllowUsers) == 0 {
		add("web.allow_users", "empty while enable = true: nobody could log in, so the Web UI does not start")
	}
	if sd := g.Web.StateDir; sd != "" && !filepath.IsAbs(sd) && !strings.HasPrefix(sd, "~/") {
		add("web.state_dir", "%q must be an absolute path (or start with ~/)", sd)
	}
	return out
}

var hexRe = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

func hexColor(add func(string, string, ...any), key, v string) {
	if !hexRe.MatchString(v) {
		add(key, "%q is not a color like \"#ff87da\"", v)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// fmtDur prints a duration the way one writes it in config.toml: 72h, not
// 72h0m0s.
func fmtDur(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
