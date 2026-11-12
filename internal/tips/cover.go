package tips

// Coverage: every key in the TUI's help, every pekoe subcommand and every
// section of config.toml either appears in some tip's keys or is listed in
// NoTip with a reason, and every key a tip names exists. The tests that
// check it live next to each list (internal/tui, cmd/pekoe, internal/config)
// and compare names normalized here.

import (
	"regexp"
	"slices"
	"strings"
)

// Kinds of a name in a tip's keys.
const (
	KindKey      = "key"      // a TUI key: t, ^q z, ]a, ↵ …
	KindCommand  = "command"  // a pekoe subcommand: "pekoe send"
	KindFlag     = "flag"     // a command-line flag: --no-enter
	KindConfig   = "config"   // a config.toml section (or top-level key): "[web]"
	KindExternal = "external" // Neovim / Chrome keys, checked against the docs
)

// Ref is one normalized name a tip points at.
type Ref struct {
	Kind, Name string
}

// NoTip lists what deliberately has no tip, with the reason. Names are
// normalized (Norm): TUI keys as the help shows them, "pekoe <sub>" for
// subcommands, "[section]" for config.toml.
var NoTip = map[string]string{
	// TUI keys
	"j":   "基本の移動。どのパネルのヒントにも出ている",
	"k":   "基本の移動。どのパネルのヒントにも出ている",
	"h":   "基本の移動(左に戻る)。ヒントに出ている",
	"esc": "基本の操作(戻る・閉じる)",
	"q":   "終了。説明するまでもない",
	"o":   "Issues・PR・preview の「ブラウザで開く」。各ビューのヒントに出ている補助操作",
	".":   "TIPS 自身の送りキー。tip の見出しに常に出ている",

	// subcommands
	"pekoe ls":     "基本の一覧。README の最初に出てくる",
	"pekoe rm":     "後片付け。TUI の D と同じで、tip にするほどの発見がない",
	"pekoe attach": "デバッグ用(tmux の窓に直接 attach)",
	"pekoe hooks":  "インストール時に一度だけ使う",
	"pekoe event":  "hooks から呼ばれる内部用",
	"pekoe daemon": "デーモンの操作。ふだんは TUI が起動する",
	"pekoe tip":    "TIPS 自身。tapi chat が使う",

	// config.toml
	"[http]":  "Chrome 拡張の port。pekoe ext の tip で足りる",
	"[theme]": "見た目の好み。README の設定の節で足りる",
	"[ports]": "ポート台帳の開始番号。困ったときに設定の節を見れば足りる",
	"[tips]":  "TIPS 自身。止め方は README の TIPS の節に書いてある",
}

var (
	configRe = regexp.MustCompile(`^\[[a-z_]+\]$`)
	rangeRe  = regexp.MustCompile(`^(\d)[-〜](\d)$`)
)

// Norm spells one key the same way everywhere: ↵ for Enter, lower-case
// tab / esc / space, ^q for the prefix (ctrl+q).
func Norm(k string) string {
	switch strings.ToLower(k) {
	case "enter", "return", "↵", "\r":
		return "↵"
	case "tab":
		return "tab"
	case "shift+tab":
		return "shift+tab"
	case "esc", "escape":
		return "esc"
	case "space", " ":
		return "space"
	}
	if rest, ok := strings.CutPrefix(k, "ctrl+"); ok {
		return "^" + rest
	}
	return k
}

// Expand splits a help label into single keys: "j/k" → j k, "1-4" → 1 2 3
// 4, "]a [a" → ]a [a, "^q < >" → ^q < and ^q > (a leading prefix applies
// to every key after it), "[ ]" → [ ].
func Expand(label string) []string {
	f := strings.Fields(label)
	if len(f) == 0 {
		return nil
	}
	prefix := ""
	if len(f) > 1 && strings.HasPrefix(f[0], "^") {
		prefix, f = Norm(f[0])+" ", f[1:]
	}
	var out []string
	for _, tok := range f {
		var keys []string
		switch m := rangeRe.FindStringSubmatch(tok); {
		case m != nil:
			for c := m[1][0]; c <= m[2][0]; c++ {
				keys = append(keys, string(c))
			}
		case len(tok) > 1 && strings.Contains(tok, "/") && !strings.HasPrefix(tok, "/") && !strings.HasSuffix(tok, "/"):
			keys = strings.Split(tok, "/")
		default:
			keys = []string{tok}
		}
		for _, k := range keys {
			out = append(out, prefix+Norm(k))
		}
	}
	return out
}

// Classify sorts one entry of a tip's keys into Refs.
func Classify(k string) []Ref {
	switch {
	case strings.HasPrefix(k, "pekoe "):
		return []Ref{{KindCommand, "pekoe " + strings.Fields(k)[1]}}
	case strings.HasPrefix(k, "--"):
		return []Ref{{KindFlag, k}}
	case configRe.MatchString(k):
		return []Ref{{KindConfig, k}}
	case strings.HasPrefix(k, "<leader>") || strings.HasPrefix(k, ":Pekoe") || strings.ContainsRune(k, '⌘'):
		return []Ref{{KindExternal, k}}
	}
	var refs []Ref
	for _, key := range Expand(k) {
		refs = append(refs, Ref{KindKey, key})
	}
	return refs
}

// Refs is every name the tips point at, of one kind, with the ids of the
// tips that name it.
func Refs(kind string) map[string][]string {
	out := map[string][]string{}
	for _, t := range all {
		for _, k := range t.Keys {
			for _, r := range Classify(k) {
				if r.Kind == kind {
					out[r.Name] = append(out[r.Name], t.ID)
				}
			}
		}
	}
	return out
}

// Fix is the hint a coverage test prints for a name without a tip.
func Fix(name, what string) string {
	return "add one to internal/tips/tips.toml (name " + quote(name) + " in its keys) or list it in NoTip (internal/tips/cover.go) with a reason"
}

// Coverage compares one kind of names that exist (name → what it is, e.g.
// the help's action) with the tips and NoTip, and returns what is wrong:
// a name with neither a tip nor a NoTip reason, a tip naming something that
// does not exist, and NoTip entries that are stale, doubled or unexplained.
func Coverage(kind string, have map[string]string) []string {
	var errs []string
	refs := Refs(kind)
	for _, name := range sortedKeys(have) {
		_, skip := NoTip[name]
		ids := refs[name]
		switch {
		case len(ids) == 0 && !skip:
			errs = append(errs, kind+" "+quote(name)+" ("+have[name]+") has no tip: "+Fix(name, "it"))
		case len(ids) > 0 && skip:
			errs = append(errs, kind+" "+quote(name)+" has a tip ("+strings.Join(ids, ", ")+") and is in NoTip: remove it from NoTip")
		}
	}
	for _, name := range sortedKeys(refs) {
		if _, ok := have[name]; !ok {
			errs = append(errs, "tip "+strings.Join(refs[name], ", ")+" names "+kind+" "+quote(name)+", which does not exist: fix its keys in internal/tips/tips.toml")
		}
	}
	for _, name := range sortedKeys(NoTip) {
		if c := Classify(name); len(c) != 1 || c[0].Kind != kind {
			continue
		}
		if _, ok := have[name]; !ok {
			errs = append(errs, "NoTip lists "+kind+" "+quote(name)+", which does not exist: remove it from NoTip")
		}
		if strings.TrimSpace(NoTip[name]) == "" {
			errs = append(errs, "NoTip lists "+quote(name)+" without a reason")
		}
	}
	return errs
}

func quote(s string) string { return `"` + s + `"` }

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
