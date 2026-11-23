package voice

import (
	"fmt"
	"sort"
	"strings"
)

// TapiSlug is tapi chat's slug (daemon.TapiSlug; voice does not import the
// daemon).
const TapiSlug = "+tapi"

// kindPrefixes are the branch-type prefixes a call name drops (5.2).
var kindPrefixes = []string{
	"feat-", "fix-", "docs-", "chore-", "refactor-", "test-", "perf-", "ci-",
	"build-", "style-", "review-",
}

// CallName is what tapi calls the session slug (5.2): the type prefix
// dropped (unless nothing is left), - _ / . as spaces, spaces squeezed.
// tapi chat is "tapi", in kana for ja so that a synthesizer does not spell
// it out.
func CallName(slug, lang string) string {
	if slug == TapiSlug {
		if lang == LangJa {
			return "たぴ"
		}
		return "tapi"
	}
	s := slug
	for _, p := range kindPrefixes {
		if strings.HasPrefix(s, p) && len(s) > len(p) {
			s = s[len(p):]
			break
		}
	}
	s = strings.Map(func(r rune) rune {
		switch r {
		case '-', '_', '/', '.':
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// Named is one session an utterance names: its slug and the display name
// of its repository (to tell two sessions with the same call name apart).
type Named struct {
	Slug string
	Repo string
}

// CallNames are the call names of the sessions one utterance names, in
// order, without repeating a slug. Names that would come out the same get
// their repository's display name in front ("web csv export"), the others
// stay as they are (5.2).
func CallNames(ns []Named, lang string) []string {
	type one struct{ name, repo string }
	var list []one
	seen := map[Named]bool{}
	count := map[string]int{}
	for _, n := range ns {
		if seen[n] {
			continue
		}
		seen[n] = true
		c := CallName(n.Slug, lang)
		list = append(list, one{c, n.Repo})
		count[c]++
	}
	out := make([]string, 0, len(list))
	for _, o := range list {
		if count[o.name] > 1 && o.repo != "" {
			out = append(out, o.repo+" "+o.name)
			continue
		}
		out = append(out, o.name)
	}
	return out
}

// JoinNames joins call names for {names} (5.3): ja AとB / A、BとC /
// A、Bほか2件; en A and B / A, B and C / A, B and 2 more.
func JoinNames(names []string, lang string) string {
	n := len(names)
	ja := lang == LangJa
	switch {
	case n == 0:
		return ""
	case n == 1:
		return names[0]
	case n == 2 && ja:
		return names[0] + "と" + names[1]
	case n == 2:
		return names[0] + " and " + names[1]
	case n == 3 && ja:
		return names[0] + "、" + names[1] + "と" + names[2]
	case n == 3:
		return names[0] + ", " + names[1] + " and " + names[2]
	case ja:
		return fmt.Sprintf("%s、%sほか%d件", names[0], names[1], n-2)
	}
	return fmt.Sprintf("%s, %s and %d more", names[0], names[1], n-2)
}

// defaultPhrases are the built-in phrases by language (5.3).
var defaultPhrases = map[string]map[string]string{
	LangJa: {
		"wait":       "{name}、確認待ちだよ",
		"wait_many":  "{names}が確認待ちだよ",
		"done":       "{name}、終わったよ",
		"done_many":  "{names}が終わったよ",
		"error":      "{name}でエラーが出たみたい",
		"error_many": "{names}でエラーが出たみたい",
		"ask":        "{name}から質問が来てるよ",
		"ask_many":   "{names}から質問が来てるよ",
		"idle_all":   "みんな手が空いたよ",
	},
	LangEn: {
		"wait":       "{name} needs you",
		"wait_many":  "{names} need you",
		"done":       "{name} is done",
		"done_many":  "{names} are done",
		"error":      "{name} hit an error",
		"error_many": "{names} hit errors",
		"ask":        "{name} has a question",
		"ask_many":   "{names} have questions",
		"idle_all":   "everyone's idle",
	},
}

// PhraseKeys are the keys [voice.phrases] takes, sorted.
func PhraseKeys() []string {
	keys := make([]string, 0, len(defaultPhrases[LangEn]))
	for k := range defaultPhrases[LangEn] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// phraseProblem is why an override of key cannot be used, "" if it can:
// an unknown key, or a phrase without the placeholder its key needs
// ({name}, {names} for _many; idle_all needs none).
func phraseProblem(key, text string) string {
	if _, ok := defaultPhrases[LangEn][key]; !ok {
		return fmt.Sprintf("[voice.phrases] %s: unknown key (known: %s)", key, strings.Join(PhraseKeys(), ", "))
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Sprintf("[voice.phrases] %s: empty; using the default", key)
	}
	ph := "{name}"
	switch {
	case key == string(KindIdleAll):
		return ""
	case strings.HasSuffix(key, "_many"):
		ph = "{names}"
	}
	if !strings.Contains(text, ph) {
		return fmt.Sprintf("[voice.phrases] %s: has no %s; using the default", key, ph)
	}
	return ""
}

// CheckPhrases are the warnings pekoe config check gives for the
// [voice.phrases] overrides, sorted by key.
func CheckPhrases(overrides map[string]string) []string {
	keys := make([]string, 0, len(overrides))
	for k := range overrides {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		if p := phraseProblem(k, overrides[k]); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Phrases are the phrases in use: the defaults for one language with the
// usable overrides on top.
type Phrases struct {
	lang string
	m    map[string]string
}

// NewPhrases are the phrases for lang (ja or en) with overrides; an
// override CheckPhrases warns about is left out (the default stays).
func NewPhrases(lang string, overrides map[string]string) Phrases {
	if lang != LangJa {
		lang = LangEn
	}
	m := map[string]string{}
	for k, v := range defaultPhrases[lang] {
		m[k] = v
	}
	for k, v := range overrides {
		if phraseProblem(k, v) == "" {
			m[k] = v
		}
	}
	return Phrases{lang: lang, m: m}
}

// Lang is the language of the phrases.
func (p Phrases) Lang() string { return p.lang }

// Render is the sentence for a narrate kind naming the sessions ns: the
// one-name phrase for one, the _many phrase for more, idle_all as is.
func (p Phrases) Render(k Kind, ns []Named) string {
	if p.m == nil {
		p = NewPhrases(p.lang, nil)
	}
	if k == KindIdleAll {
		return p.m[string(KindIdleAll)]
	}
	names := CallNames(ns, p.lang)
	if len(names) <= 1 {
		name := ""
		if len(names) == 1 {
			name = names[0]
		}
		return strings.ReplaceAll(p.m[string(k)], "{name}", name)
	}
	return strings.ReplaceAll(p.m[string(k)+"_many"], "{names}", JoinNames(names, p.lang))
}
