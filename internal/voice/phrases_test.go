package voice

import (
	"reflect"
	"strings"
	"testing"
)

// Rule 5.2: the call name is the slug without its type prefix.
func TestRule5_2CallName(t *testing.T) {
	cases := []struct{ slug, lang, want string }{
		{"feat-csv-export", LangEn, "csv export"},
		{"fix-ci", LangEn, "ci"},
		{"docs-voice-spec", LangJa, "voice spec"},
		{"review-pr-12", LangEn, "pr 12"},
		{"feat-", LangEn, "feat"},         // nothing left: keep the prefix
		{"fix", LangEn, "fix"},            // no dash: not a prefix
		{"featx-y", LangEn, "featx y"},    // not a listed prefix
		{"a__b..c//d", LangEn, "a b c d"}, // separators squeezed
		{"fix-feat-x", LangEn, "feat x"},  // only the first prefix goes
		{"テスト-です", LangJa, "テスト です"},
		{TapiSlug, LangEn, "tapi"},
		{TapiSlug, LangJa, "たぴ"},
	}
	for _, c := range cases {
		if got := CallName(c.slug, c.lang); got != c.want {
			t.Errorf("CallName(%q, %s) = %q, want %q", c.slug, c.lang, got, c.want)
		}
	}
}

// Rule 5.2: same call names in one utterance get the repository's name.
func TestRule5_2CallNamesRepo(t *testing.T) {
	got := CallNames([]Named{
		{"feat-csv-export", "web"},
		{"feat-csv-export", "api"},
		{"fix-ci", "web"},
		{"feat-csv-export", "web"}, // repeated: once
	}, LangEn)
	want := []string{"web csv export", "api csv export", "ci"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CallNames = %q, want %q", got, want)
	}
}

// Rule 5.3: how {names} joins.
func TestRule5_3JoinNames(t *testing.T) {
	cases := []struct {
		names []string
		lang  string
		want  string
	}{
		{[]string{"a"}, LangJa, "a"},
		{[]string{"a", "b"}, LangJa, "aとb"},
		{[]string{"a", "b", "c"}, LangJa, "a、bとc"},
		{[]string{"a", "b", "c", "d"}, LangJa, "a、bほか2件"},
		{[]string{"a", "b"}, LangEn, "a and b"},
		{[]string{"a", "b", "c"}, LangEn, "a, b and c"},
		{[]string{"a", "b", "c", "d", "e"}, LangEn, "a, b and 3 more"},
		{nil, LangEn, ""},
	}
	for _, c := range cases {
		if got := JoinNames(c.names, c.lang); got != c.want {
			t.Errorf("JoinNames(%q, %s) = %q, want %q", c.names, c.lang, got, c.want)
		}
	}
}

// Rule 5.3: the default phrases, one name and many.
func TestRule5_3RenderDefaults(t *testing.T) {
	one := []Named{{Slug: "feat-csv-export"}}
	two := []Named{{Slug: "feat-csv-export"}, {Slug: "fix-ci"}}
	cases := []struct {
		lang string
		k    Kind
		ns   []Named
		want string
	}{
		{LangJa, KindWait, one, "csv export、確認待ちだよ"},
		{LangJa, KindWait, two, "csv exportとciが確認待ちだよ"},
		{LangJa, KindDone, one, "csv export、終わったよ"},
		{LangJa, KindDone, two, "csv exportとciが終わったよ"},
		{LangJa, KindError, one, "csv exportでエラーが出たみたい"},
		{LangJa, KindError, two, "csv exportとciでエラーが出たみたい"},
		{LangJa, KindAsk, one, "csv exportから質問が来てるよ"},
		{LangJa, KindAsk, two, "csv exportとciから質問が来てるよ"},
		{LangJa, KindIdleAll, nil, "みんな手が空いたよ"},
		{LangEn, KindWait, one, "csv export needs you"},
		{LangEn, KindWait, two, "csv export and ci need you"},
		{LangEn, KindDone, one, "csv export is done"},
		{LangEn, KindDone, two, "csv export and ci are done"},
		{LangEn, KindError, one, "csv export hit an error"},
		{LangEn, KindError, two, "csv export and ci hit errors"},
		{LangEn, KindAsk, one, "csv export has a question"},
		{LangEn, KindAsk, two, "csv export and ci have questions"},
		{LangEn, KindIdleAll, nil, "everyone's idle"},
		{"fr", KindDone, one, "csv export is done"}, // unknown language: en
	}
	for _, c := range cases {
		if got := NewPhrases(c.lang, nil).Render(c.k, c.ns); got != c.want {
			t.Errorf("%s %s %d: %q, want %q", c.lang, c.k, len(c.ns), got, c.want)
		}
	}
}

// Rule 5.3: an override without its placeholder falls back to the default
// and config check warns about it.
func TestRule5_3Overrides(t *testing.T) {
	over := map[string]string{
		"wait":      "{name} waits",
		"done":      "finished",            // no {name}: default
		"done_many": "{name} all finished", // _many needs {names}: default
		"ask_many":  "{names} ask",
		"idle_all":  "quiet now",
		"nope":      "{name}",
		"wait_many": "  ", // empty: default
	}
	p := NewPhrases(LangEn, over)
	one := []Named{{Slug: "fix-ci"}}
	two := []Named{{Slug: "fix-ci"}, {Slug: "fix-db"}}
	for _, c := range []struct {
		k    Kind
		ns   []Named
		want string
	}{
		{KindWait, one, "ci waits"},
		{KindDone, one, "ci is done"},
		{KindDone, two, "ci and db are done"},
		{KindAsk, two, "ci and db ask"},
		{KindWait, two, "ci and db need you"},
		{KindIdleAll, nil, "quiet now"},
	} {
		if got := p.Render(c.k, c.ns); got != c.want {
			t.Errorf("Render(%s) = %q, want %q", c.k, got, c.want)
		}
	}
	warn := CheckPhrases(over)
	if len(warn) != 4 {
		t.Fatalf("CheckPhrases = %q, want 4 warnings", warn)
	}
	for i, key := range []string{"done:", "done_many:", "nope:", "wait_many:"} {
		if !strings.Contains(warn[i], key) {
			t.Errorf("warning %d = %q, want it about %s", i, warn[i], key)
		}
	}
}

func TestResolveLang(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cases := []struct {
		setting string
		env     map[string]string
		want    string
	}{
		{"ja", nil, LangJa},
		{"en", map[string]string{"LANG": "ja_JP.UTF-8"}, LangEn},
		{"auto", map[string]string{"LANG": "ja_JP.UTF-8"}, LangJa},
		{"auto", map[string]string{"LC_ALL": "en_US.UTF-8", "LANG": "ja_JP.UTF-8"}, LangEn},
		{"auto", map[string]string{"LC_MESSAGES": "ja_JP", "LANG": "C"}, LangJa},
		{"auto", nil, LangEn},
		{"fr", map[string]string{"LANG": "ja_JP"}, LangJa}, // unknown setting: auto
	}
	for _, c := range cases {
		if got := ResolveLang(c.setting, env(c.env)); got != c.want {
			t.Errorf("ResolveLang(%q, %v) = %s, want %s", c.setting, c.env, got, c.want)
		}
	}
}
