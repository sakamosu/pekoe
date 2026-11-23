package config

// What each setting of config.toml is, in one line, for the local GUI's
// Settings view (docs/gui.md): the README's Configuration comments, kept
// next to the struct they describe. TestDocsCoverKeys checks both ways that
// every key Global declares has a line here and that no line is left over
// for a key that went.

import (
	"reflect"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// SectionDocs: one line per section ([web], [gui] …; "agents.*" stands for
// every [agents.<name>]; animate is a bare key and its own section).
var SectionDocs = map[string]string{
	"http":         "pekoed's HTTP listener for the Chrome extension (127.0.0.1)",
	"web":          "the Web UI on the tailnet (make install WEB=1 builds only, docs/web.md)",
	"gui":          "the local GUI on 127.0.0.1 (pekoe gui, docs/gui.md)",
	"keys":         "the TUI's keys",
	"theme":        "the TUI's colors and icons (tokens in docs/ui-guidelines.md)",
	"mascot":       "the mascot tapi (docs/mascot.md)",
	"agents.*":     "an agent pekoe can start; the name is what agent = picks",
	"review":       "where a review opens",
	"ports":        "the port ledger: each session gets its own port",
	"tapi":         "tapi chat (t in the TUI)",
	"tips":         "tips in the TUI's empty view and tapi chat's greeting",
	"sandbox":      "the sandbox of commands from worktrees ([hooks], gatekeeper) and of pekoed's git",
	"animate":      "the TUI's animation",
	"picks":        "picks from the Chrome extension",
	"orchestrator": "tapi as the orchestrator (docs/orchestration.md)",
	"notify":       "when the TUI tells you something waits",
	"cleanup":      "pekoe cleanup / :cleanup",
	"pr":           "pekoed's polling of the sessions' PRs",
	"voice":        "tapi says state changes aloud (macOS; docs/voice.md)",
}

// KeyDocs: one line per key, by its dotted name ("agents.*.model" for every
// agent's).
var KeyDocs = map[string]string{
	"http.port": "port of the listener for the Chrome extension",

	"web.enable":            "serve the Web UI on the tailnet",
	"web.hostname":          "machine name on the tailnet",
	"web.allow_users":       "Tailscale login names let in; empty lets nobody in",
	"web.allow_devices":     "only these devices of allow_users; empty: any of theirs",
	"web.state_dir":         "where the tsnet node keeps its keys",
	"web.auth_key":          "Tailscale auth key for the first join (TS_AUTHKEY works too)",
	"web.confirm_tapi_send": "a confirmation sheet before a send to tapi; false sends at once (no restart needed)",

	"gui.enable":      "false refuses pekoe gui; nothing listens before the first pekoe gui either way",
	"gui.port":        "0: a free port each time it starts listening; read when it starts",
	"gui.session_ttl": "a browser's sign-in lapses after this long with no action (the screen refreshing doesn't count)",
	"gui.browser":     "macOS app to open it with (open -a); empty: the default browser",

	"keys.prefix": "key to leave input: ctrl+ a letter (not i / m), ctrl+@ \\ ] ^ _, F1-F20 or alt+ a key",
	"keys.map":    "not read by pekoe yet",

	"theme.mode":      "truecolor | ansi | none | auto",
	"theme.icons":     "nerd | unicode | ascii",
	"theme.ambiguous": "how the terminal counts ambiguous-width glyphs (● · …): auto (measured when the TUI opens) | narrow (one cell) | wide (two cells: pekoe draws without them; read when the TUI opens)",
	"theme.colors":    "colors by role, overriding the theme's (#rrggbb)",
	"theme.ansi":      "ANSI color numbers by role, for mode = ansi (0-255)",

	"mascot.shape":       "auto | round (sextant blocks) | block (quadrant blocks)",
	"mascot.glyphs":      "auto (ascii when [theme] ambiguous is wide) | unicode | ascii",
	"mascot.palette":     "dark (milky pearl, for dark backgrounds) | light (caramel pearl)",
	"mascot.colors":      "override the pearl's hi / bd / sh / eye (#rrggbb)",
	"mascot.splash":      "splash screen at startup",
	"mascot.sleep_after": "tapi falls asleep when every session is idle and no key was pressed for this long (at least 10s)",

	"agents.*.command": "the command that starts it",
	"agents.*.resume":  "the command that resumes a conversation ({{session_id}})",
	"agents.*.prompt":  "how the first prompt goes in: arg (on the command line after --) | paste",
	"agents.*.allow":   "keys that allow a permission prompt once",
	"agents.*.always":  "keys that always allow",
	"agents.*.deny":    "keys that deny",
	"agents.*.hooks":   "the agent reports its state through pekoe event",
	"agents.*.sandbox": "pass pekoe's sandbox settings (--settings) to Claude Code; other agents get none",
	"agents.*.model":   "passed as --model on every start (Claude Code only); unset follows ~/.claude/settings.json",
	"agents.*.effort":  "passed as --effort: low | medium | high | xhigh | max (Claude Code only)",

	"review.surface":  "nvim | tui",
	"ports.base":      "first port of the port ledger",
	"tapi.agent":      "a name from [agents.*]; agents other than claude get no --add-dir",
	"tips.enable":     "false hides the tips",
	"sandbox.enable":  "false runs those commands without a sandbox (where none works they are not run; git runs anyway)",
	"animate":         "animate the state icons",
	"picks.auto_send": "send picks with an instruction right away to the session that owns the page's port",

	"orchestrator.notify":      "escalate: tapi's wait and the inbox's questions escalate like a session's | quiet",
	"orchestrator.inbox_popup": "a new question an agent waits on comes up in front as the answer dialog",
	"orchestrator.deliver":     "idle: pekoed types one line of news into tapi's input box when it is free | off",
	"orchestrator.max_running": "at most this many sessions at work at once; more are queued. 0: no cap",

	"notify.toast_for":   "how long a toast shows (at least 1s)",
	"notify.blink_after": "the bottom line's speech blinks after a wait this long (at least 1s)",
	"notify.os_after":    "a desktop notification after a wait this long, while the terminal is not focused (at least 10s)",

	"cleanup.stale_after": "list a stopped session as stale after this long without a change",
	"pr.poll_every":       "ask GitHub about each open PR at most this often (at least 30s)",

	"voice.enable":          "false (the default): nothing is said, no backend runs, nothing plays",
	"voice.lang":            "auto (LC_ALL, LC_MESSAGES, LANG) | ja | en",
	"voice.backend":         "\"say\" (bundled) or a name in [voice.backends]; PATH is never searched",
	"voice.fallback":        "used when backend cannot be: \"say\" or a name in [voice.backends]; \"\" for none",
	"voice.focus_mode":      "the session shown in the TUI: read | narrate | mute (read speaks as narrate until it is built)",
	"voice.background_mode": "the other sessions: read | narrate | mute",
	"voice.read":            "max_sentences (1-50) of a message read aloud, and skip (code | table | url | path)",
	"voice.phrases":         "override what tapi says (wait, done, error, ask, idle_all and their _many); {name} / {names}",
	"voice.backends":        "external backends: name = absolute (or ~/) path; only these are run",
	"voice.say":             "the bundled say backend's voice: ja / en (\"\": Kyoko or the first ja_JP voice / the system's voice)",
}

// ShownKey is one setting as the Settings view shows it.
type ShownKey struct {
	Key   string `json:"key"`   // the name in its section ("port", "colors")
	Value any    `json:"value"` // string, int, bool, []string, map[string]string|int
	Doc   string `json:"doc"`
	// Restart: a change to it waits for pekoe daemon restart (NeedsRestart)
	Restart bool `json:"restart,omitempty"`
	// Set: config.toml has the key (MarkSet); otherwise it is the default
	Set bool `json:"set,omitempty"`
}

// ShownSection is one section of config.toml ([web], [agents.claude] …).
type ShownSection struct {
	Name string     `json:"name"` // "web", "agents.claude", "animate"
	Doc  string     `json:"doc"`
	Keys []ShownKey `json:"keys"`
}

// Shown is Effective(g) section by section, in the order Global declares
// them ([agents.<name>] by name): what the Settings view lists. Secrets are
// hidden as Effective hides them.
func Shown(g *Global) []ShownSection {
	e := reflect.ValueOf(*Effective(g))
	t := e.Type()
	var out []ShownSection
	for i := range t.NumField() {
		name := tomlName(t.Field(i))
		if name == "" {
			continue
		}
		v := e.Field(i)
		switch {
		case v.Kind() == reflect.Struct:
			out = append(out, ShownSection{Name: name, Doc: SectionDocs[name], Keys: shownKeys(name, name, v)})
		case v.Kind() == reflect.Map && v.Type().Elem().Kind() == reflect.Struct: // [agents.<name>]
			keys := make([]string, 0, v.Len())
			for _, k := range v.MapKeys() {
				keys = append(keys, k.String())
			}
			sort.Strings(keys)
			for _, k := range keys {
				out = append(out, ShownSection{Name: name + "." + k, Doc: SectionDocs[name+".*"], Keys: shownKeys(name+".*", name+"."+k, v.MapIndex(reflect.ValueOf(k)))})
			}
		default: // a bare key (animate)
			out = append(out, ShownSection{Name: name, Doc: SectionDocs[name], Keys: []ShownKey{{Key: name, Value: plain(v), Doc: KeyDocs[name], Restart: NeedsRestart(name)}}})
		}
	}
	return out
}

func shownKeys(docPrefix, prefix string, v reflect.Value) []ShownKey {
	t := v.Type()
	keys := []ShownKey{}
	for i := range t.NumField() {
		name := tomlName(t.Field(i))
		if name == "" {
			continue
		}
		keys = append(keys, ShownKey{Key: name, Value: plain(v.Field(i)), Doc: KeyDocs[docPrefix+"."+name], Restart: NeedsRestart(prefix + "." + name)})
	}
	return keys
}

// plain is a setting's value as JSON carries it: a pointer as what it
// points at (Effective leaves none nil), nil slices and maps empty.
func plain(v reflect.Value) any {
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Slice:
		if v.IsNil() {
			return []string{}
		}
	case reflect.Map:
		if v.IsNil() {
			return map[string]string{}
		}
	}
	return v.Interface()
}

func tomlName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
	if name == "-" {
		return ""
	}
	return name
}

// NeedsRestart: a change to the setting (dotted, "web.hostname") waits for
// pekoed to restart, as RestartNeeded tells: [http] port, [web] but
// confirm_tapi_send, and [sandbox]. TestNeedsRestartMatchesRestartNeeded
// changes each key and checks the two agree.
func NeedsRestart(key string) bool {
	switch {
	case key == "http.port", key == "sandbox.enable":
		return true
	case key == "web.confirm_tapi_send":
		return false
	}
	return strings.HasPrefix(key, "web.")
}

// MarkSet sets Set on the keys raw (config.toml's contents) has: the rest
// are at their default. raw that does not parse marks nothing.
func MarkSet(ss []ShownSection, raw []byte) {
	var g Global
	md, err := toml.Decode(string(raw), &g)
	if err != nil {
		return
	}
	for i := range ss {
		s := &ss[i]
		for j := range s.Keys {
			k := &s.Keys[j]
			path := []string{s.Name}
			if name, ok := strings.CutPrefix(s.Name, "agents."); ok {
				path = []string{"agents", name} // a name may hold a dot
			}
			if s.Name != k.Key {
				path = append(path, k.Key)
			}
			k.Set = md.IsDefined(path...)
		}
	}
}
