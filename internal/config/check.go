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

	"pekoe/internal/voice"
	"pekoe/internal/voice/backend"
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
	port("gui.port", g.GUI.Port)

	if p := g.Keys.Prefix; strings.TrimSpace(p) != "" {
		if _, ok := PrefixKey(p); !ok {
			add("keys.prefix", "%q is not a key the TUI can tell apart (ctrl+ a letter other than i or m, F1-F20, or alt+ a key; ctrl+i / ctrl+m / ctrl+[ arrive as tab / enter / esc): %s is used", p, DefaultPrefix)
		}
	}

	oneOf("theme.mode", g.Theme.Mode, false, "auto", "truecolor", "ansi", "none")
	oneOf("theme.icons", g.Theme.Icons, false, "nerd", "unicode", "ascii")
	oneOf("theme.ambiguous", g.Theme.Ambiguous, false, "auto", "narrow", "wide")
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
	if g.Mascot.Glyphs == "narrow" {
		// a former setting (● as 🞄): the default glyphs fit every font now
		add("mascot.glyphs", `"narrow" is no longer needed: the default glyphs fit fonts that draw ambiguous-width glyphs two cells wide; remove it (it now does nothing: the same as auto). If the terminal itself counts them as two cells, set [theme] ambiguous = "wide"`)
	}
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
	oneOf("orchestrator.deliver", strings.TrimSpace(g.Orchestrator.Deliver), true, "off", "idle")
	if n := g.Orchestrator.MaxRunning; n < 0 {
		add("orchestrator.max_running", "%d is below 0 (no cap is used; 0 means no cap)", n)
	}
	if s := strings.TrimSpace(g.Cleanup.StaleAfter); s != "" {
		if d, err := time.ParseDuration(s); err != nil || d <= 0 {
			add("cleanup.stale_after", "%q is not a positive Go duration like 72h or 36h30m (%s is used)", g.Cleanup.StaleAfter, fmtDur(DefaultStaleAfter))
		}
	}

	if s := strings.TrimSpace(g.GUI.SessionTTL); s != "" {
		if d, err := time.ParseDuration(s); err != nil || d <= 0 {
			add("gui.session_ttl", "%q is not a positive Go duration like 12h or 30m (%s is used)", g.GUI.SessionTTL, fmtDur(DefaultGUISessionTTL))
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
		cmd := a.Command
		if cmd == "" {
			cmd = agents[name].Command
		}
		oneOf(key+".effort", a.Effort, false, Efforts...)
		if a.Model != "" && !ValidModel(a.Model) {
			add(key+".model", "%q is not a model name (letters, digits and . _ - : / [ ]); not passed, so Claude Code's default is used", a.Model)
		}
		if (a.Model != "" || a.Effort != "") && !IsClaude(cmd) {
			add(key, "model and effort are only passed to Claude Code (command \"claude\"); ignored for %q", cmd)
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
	checkVoice(add, g.Voice, md.IsDefined("voice", "read", "max_sentences"), md.IsDefined("voice", "read", "max_turn_sentences"))
	return out
}

// checkVoice: [voice] (docs/voice.md 11). [voice.backends] is checked by
// path only: no backend is run.
func checkVoice(add func(string, string, ...any), v Voice, maxSet, turnSet bool) {
	oneOf := func(key, val string, allowed ...string) {
		if val != "" && !slices.Contains(allowed, val) {
			add(key, "%q is not one of %s (the default is used)", val, strings.Join(allowed, " | "))
		}
	}
	oneOf("voice.lang", v.Lang, "auto", "ja", "en")
	oneOf("voice.focus_mode", v.FocusMode, VoiceModes...)
	oneOf("voice.background_mode", v.BackgroundMode, VoiceModes...)
	if n := v.Read.MaxSentences; maxSet && (n < 1 || n > MaxVoiceSentences) {
		add("voice.read.max_sentences", "%d is not between 1 and %d (%d is used)", n, MaxVoiceSentences, DefaultVoiceMaxSentences)
	}
	if n := v.Read.MaxTurnSentences; turnSet && (n < 1 || n > MaxVoiceTurnSentences) {
		add("voice.read.max_turn_sentences", "%d is not between 1 and %d (%d is used)", n, MaxVoiceTurnSentences, DefaultVoiceTurnSentences)
	}
	for _, k := range v.Read.Skip {
		if !slices.Contains(VoiceSkips, k) {
			add("voice.read.skip", "%q is not one of %s (ignored)", k, strings.Join(VoiceSkips, " | "))
		}
	}
	for _, msg := range voice.CheckPhrases(v.Phrases) {
		// "[voice.phrases] wait: …" → key voice.phrases.wait
		rest := strings.TrimPrefix(msg, "[voice.phrases] ")
		k, why, _ := strings.Cut(rest, ": ")
		add("voice.phrases."+k, "%s", why)
	}
	for _, name := range sortedKeys(v.Backends) {
		key := "voice.backends." + name
		p := v.Backends[name]
		switch {
		case name == backend.Builtin:
			add(key, "%q is the bundled backend's name; this entry is never used (backend = \"say\" always runs the bundled one)", name)
			continue
		case !backend.ValidName(name):
			add(key, "%q is not a backend name (lower-case letters, digits and -); not used", name)
			continue
		}
		abs, err := backend.ExpandPath(p)
		if err != nil {
			add(key, "%v; not used", err)
			continue
		}
		if err := (&backend.Backend{Name: name, Path: abs}).Check(); err != nil {
			add(key, "%s; not used", strings.TrimPrefix(err.Error(), "voice backend "+name+" path: "))
			continue
		}
		if w := backend.HomeWarning(abs); w != "" {
			add(key, "note: %s", w)
		}
	}
	named := func(key, name string) {
		if name == "" || name == backend.Builtin {
			return
		}
		if _, ok := v.Backends[name]; !ok {
			add(key, "%q is neither \"say\" nor in [voice.backends] (PATH is not searched)", name)
		}
	}
	named("voice.backend", strings.TrimSpace(v.Backend))
	if v.Fallback != nil {
		named("voice.fallback", strings.TrimSpace(*v.Fallback))
	}
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

// CheckRepo reads a repository's config (repos/<repo-id>.toml) and lists
// what is wrong with it, as Check does for config.toml: a syntax error is
// fatal (pekoe reads nothing from the file), the rest are warnings.
func CheckRepo(b []byte) []Problem {
	r := &Repo{}
	md, err := toml.Decode(string(b), r)
	if err != nil {
		msg := "cannot be read: " + err.Error()
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
	if _, err := r.Merge.MergeMethod(); err != nil {
		add("merge.method", "%q is not one of %s (merge is used)", r.Merge.Method, strings.Join(MergeMethods, " | "))
	}
	checkWatch(add, r.Watch, md)
	return out
}

// checkWatch: [watch] of a repository (issue watch mode).
func checkWatch(add func(string, string, ...any), w Watch, md toml.MetaData) {
	seen := map[string]bool{}
	for _, l := range w.Labels {
		t := strings.TrimSpace(l)
		switch {
		case t == "":
			add("watch.labels", "an empty label (ignored)")
		case seen[t]:
			add("watch.labels", "%q is listed twice", t)
		case strings.Contains(t, ","):
			// gh issue list --label reads a comma as two labels
			add("watch.labels", "%q has a comma: gh would read it as two labels", t)
		}
		seen[t] = true
	}
	if md.IsDefined("watch", "labels") && len(w.Labels) > 0 && len(seen) == 1 && seen[""] {
		add("watch.labels", "no label left: %s is watched", strings.Join(DefaultWatchLabels, ", "))
	}
	if s := strings.TrimSpace(w.Every); s != "" {
		switch d, err := time.ParseDuration(s); {
		case err != nil || d <= 0:
			add("watch.every", "%q is not a positive Go duration like 5m or 10m (%s is used)", w.Every, fmtDur(DefaultWatchEvery))
		case d < MinWatchEvery:
			add("watch.every", "%s is below the minimum %s (%s is used): each round asks GitHub once per label", s, fmtDur(MinWatchEvery), fmtDur(MinWatchEvery))
		}
	}
	if md.IsDefined("watch", "max_per_poll") && w.MaxPerPoll < 1 {
		add("watch.max_per_poll", "%d is below 1 (%d is used)", w.MaxPerPoll, DefaultWatchPerPoll)
	}
	if md.IsDefined("watch", "max_running") && w.MaxRunning < 1 {
		add("watch.max_running", "%d is below 1 (%d is used)", w.MaxRunning, DefaultWatchRunning)
	}
}
