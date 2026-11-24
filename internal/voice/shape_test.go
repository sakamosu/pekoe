package voice

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// Rule 6.2: what is read of each kind of Markdown, with the default skip.
func TestRule6_2Shape(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{"ja sentences", "直しました。テストも通りました!次は?", []string{"直しました。", "テストも通りました!", "次は?"}},
		{"ja full-width marks", "終わり！本当？はい", []string{"終わり！", "本当？", "はい"}},
		{"ja newline ends a sentence", "一行目\n二行目", []string{"一行目", "二行目"}},
		{"en sentences", "Fixed it. Tests pass! Next?", []string{"Fixed it.", "Tests pass!", "Next?"}},
		{"en runs across lines", "This sentence\nspans two lines. Done", []string{"This sentence spans two lines.", "Done"}},
		{"en dot inside a word", "Version 1.2 is out.", []string{"Version 1.2 is out."}},
		{"blank line ends", "no mark\n\nnext", []string{"no mark", "next"}},
		{"fenced code", "前。\n```go\nfunc main() {}\n```\n後。", []string{"前。", "後。"}},
		{"tilde fence", "~~~\ncode here.\n~~~\nafter", []string{"after"}},
		{"longer fence closes only with as long", "````\n```\nnot closed.\n````\nout", []string{"out"}},
		{"fence ends an en sentence", "begin\n```\nx\n```\nend", []string{"begin", "end"}},
		{"indented code", "text.\n    x := 1\nmore.", []string{"text.", "more."}},
		{"nested list is not code", "- a\n    - b", []string{"a", "b"}},
		{"inline code short", "`Shape` を足した", []string{"Shape を足した"}},
		{"inline code of 24 is read", "`aaaaaaaaaaaaaaaaaaaaaaaa` を足した", []string{"aaaaaaaaaaaaaaaaaaaaaaaa を足した"}},
		{"inline code of 24 with marks is read", "`aaaaaaaaaaaaaaaaaaaaaa()` を足した", []string{"aaaaaaaaaaaaaaaaaaaaaa() を足した"}},
		{"inline code longer than 24 is code", "`aaaaaaaaaaaaaaaaaaaaaaa()` を足した", []string{"コード を足した"}},
		{"inline code one long word is read", "`fix-idle-all-not-for-tapi` の再レビューも合格したよ", []string{"fix idle all not for tapi の再レビューも合格したよ"}},
		{"inline code long word with _", "`max_turn_sentences_for_voice` を足した", []string{"max turn sentences for voice を足した"}},
		{"inline code short word with - and _", "`max_sentences` と `feat-csv` を見た", []string{"max sentences と feat csv を見た"}},
		{"inline code command of 24 is read", "`pekoe --json wait --mine` で待つ", []string{"pekoe --json wait --mine で待つ"}},
		{"inline code long command is code", "`pekoe --json wait --mine --since 3` で待つ", []string{"コード で待つ"}},
		{"inline code long with spaces is code", "`go test ./internal/voice/ -run Shape` を回した", []string{"コード を回した"}},
		{"inline code path", "実装は `internal/voice/narrate.go` の activityLocked と idleFire。", []string{"実装は narrate の activityLocked と idleFire。"}},
		{"inline code path without extension", "`internal/voice` を見た", []string{"voice を見た"}},
		{"inline code directory", "`internal/voice/` を見た", []string{"voice を見た"}},
		{"inline code home path", "`~/.config/pekoe/config.toml` に書く", []string{"config に書く"}},
		{"inline code dot directory", "`~/.config/` に置く", []string{"config に置く"}},
		{"inline code with slash", "`a/b` を見た", []string{"b を見た"}},
		{"inline code with slash, not a path", "`x := a / b` を見た", []string{"コード を見た"}},
		{"inline code url", "`https://example.com/x` を開く", []string{"リンク を開く"}},
		{"inline code up only", "`../` に戻る", []string{"に戻る"}},
		{"table", "表です。\n| a | b |\n|---|---|\n| 1 | 2 |\n後", []string{"表です。", "後"}},
		{"url", "詳しくは https://example.com/x を見て。", []string{"詳しくは リンク を見て。"}},
		{"link text only", "[docs](https://x.y/z) を読んで", []string{"docs を読んで"}},
		{"autolink", "<https://x.y> を見て", []string{"リンク を見て"}},
		{"image", "![logo](a.png) 画像", []string{"画像"}},
		{"path with extension", "internal/voice/shape.go を直した", []string{"shape を直した"}},
		{"path relative", "./run と ~/x と ../y と /etc を見た", []string{"run と x と y と etc を見た"}},
		{"path directory", "internal/voice/ と docs/voice.md を見た", []string{"voice と voice を見た"}},
		{"path home", "~/.config/pekoe/config.toml に書く", []string{"config に書く"}},
		{"path up only", "../ に戻る", []string{"に戻る"}},
		{"numbers with a slash are not a path", "1/2.5 倍で v1.2/v1.3 の差", []string{"1/2.5 倍で v1.2/v1.3 の差"}},
		{"slash without extension", "and/or is fine", []string{"and/or is fine"}},
		{"heading", "## 結果 ##\n本文", []string{"結果", "本文"}},
		{"en heading is a sentence", "# Summary\nIt works", []string{"Summary", "It works"}},
		{"list items", "- one\n* two\n+ three\n1. four\n2) five", []string{"one", "two", "three", "four", "five"}},
		{"task list", "- [x] done item", []string{"done item"}},
		{"emphasis and strike", "**太字**と*斜体*と~~消し~~と__下線__", []string{"太字と斜体と消しと下線"}},
		{"quote", "> 引用です\n>> 二重", []string{"引用です", "二重"}},
		{"html tags", "a<br>b <span class=\"x\">c</span>", []string{"a b c"}},
		{"emoji", "できた🎉✨ 👍🏽", []string{"できた"}},
		{"rule", "前\n---\n後", []string{"前", "後"}},
		{"punctuation only dropped", "。\n- \n!!", nil},
		{"crlf", "a.\r\nb.\r\n", []string{"a.", "b."}},
	}
	for _, c := range cases {
		got := Shape(c.text, DefaultSkip, LangJa)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: Shape(%q) = %q, want %q", c.name, c.text, got, c.want)
		}
	}
}

// Rule 6.2 (en): a URL is read as "a link", long inline code as "code", a
// path as its last element.
func TestRule6_2ShapeEn(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{"url", "See https://example.com/x, then stop.", []string{"See a link, then stop."}},
		{"url at the end", "Read https://example.com/x.", []string{"Read a link."}},
		{"autolink", "<https://x.y> here", []string{"a link here"}},
		{"link text only", "Read [the docs](https://x.y/z).", []string{"Read the docs."}},
		{"inline code long", "Run `go test ./internal/voice/ -run Shape` again.", []string{"Run code again."}},
		{"inline code path", "The code is in `internal/voice/narrate.go`.", []string{"The code is in narrate."}},
		{"path at the end of a sentence", "I changed internal/voice/read.go. Then ran tests.", []string{"I changed read.", "Then ran tests."}},
		{"slash without extension", "and/or is fine", []string{"and/or is fine"}},
	}
	for _, c := range cases {
		got := Shape(c.text, DefaultSkip, LangEn)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: Shape(%q) = %q, want %q", c.name, c.text, got, c.want)
		}
	}
}

// pathName: the last element without the extension (6.2).
func TestRule6_2PathName(t *testing.T) {
	for in, want := range map[string]string{
		"internal/voice/narrate.go":   "narrate",
		"README.md":                   "README",
		"docs/voice.md":               "voice",
		"internal/voice/":             "voice",
		"~/.config/pekoe/config.toml": "config",
		"~/.config/":                  "config",
		"a.tar.gz":                    "a.tar",
		"v1.2/v1.3":                   "v1.3",
		"/etc":                        "etc",
		"../":                         "",
		"~/":                          "",
	} {
		if got := pathName(in); got != want {
			t.Errorf("pathName(%q) = %q, want %q", in, got, want)
		}
	}
}

// Rule 6.2: what is not in skip is read.
func TestRule6_2ShapeNoSkip(t *testing.T) {
	text := "```\nfunc main()\n```\n| a | b |\n|---|---|\n`internal/x.go` と https://x.y と ./a.go と `https://x.y/z` と `aaaaaaaaaaaaaaaaaaaaaaa()`"
	for _, c := range []struct {
		skip []string
		want []string
	}{
		{nil, []string{"func main()", "a、b", "internal/x.go と https://x.y と ./a.go と https://x.y/z と aaaaaaaaaaaaaaaaaaaaaaa()"}},
		// code only: a path or a URL in inline code is read as it is
		{[]string{SkipCode}, []string{"a、b", "internal/x.go と https://x.y と ./a.go と https://x.y/z と コード"}},
		{[]string{SkipCode, SkipPath}, []string{"a、b", "x と https://x.y と a と https://x.y/z と コード"}},
		{[]string{SkipCode, SkipURL}, []string{"a、b", "internal/x.go と リンク と ./a.go と リンク と コード"}},
		{[]string{SkipURL, SkipPath}, []string{"func main()", "a、b", "x と リンク と a と リンク と aaaaaaaaaaaaaaaaaaaaaaa()"}},
	} {
		if got := Shape(text, c.skip, LangJa); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Shape(%q) = %q, want %q", c.skip, got, c.want)
		}
	}
	// a URL read as it is keeps its path
	if got, want := Shape("see https://x.y/a/b.md now", []string{SkipPath}, LangEn), []string{"see https://x.y/a/b.md now"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Shape(path) = %q, want %q", got, want)
	}
}

// Rule 6.2: a sentence over 200 characters is cut after 、 or , (hard at
// 200 when it has neither).
func TestRule6_2ShapeLongSentence(t *testing.T) {
	a := strings.Repeat("あ", 150)
	b := strings.Repeat("い", 150)
	got := Shape(a+"、"+b+"。", DefaultSkip, LangJa)
	want := []string{a + "、", b + "。"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cut at 、: %q", got)
	}
	got = Shape(strings.Repeat("x", 450), DefaultSkip, LangEn)
	if len(got) != 3 || utf8.RuneCountInString(got[0]) != 200 || utf8.RuneCountInString(got[2]) != 50 {
		t.Errorf("hard cut: %d parts", len(got))
	}
	for _, s := range Shape(strings.Repeat("word, ", 100), DefaultSkip, LangEn) {
		if n := utf8.RuneCountInString(s); n > MaxSentenceRunes {
			t.Errorf("sentence of %d runes", n)
		}
	}
}

// Rule 6.2: ShapeLines keeps its state across calls, so any split of the
// lines gives what Shape gives.
func TestRule6_2ShapeLinesAnySplit(t *testing.T) {
	text := replyText
	lines := strings.Split(text, "\n")
	want := Shape(text, DefaultSkip, LangJa)
	for size := 1; size <= len(lines); size++ {
		var st ShapeState
		var got []string
		for i := 0; i < len(lines); i += size {
			end := min(i+size, len(lines))
			var out []string
			st, out = ShapeLines(st, lines[i:end], DefaultSkip, LangJa)
			got = append(got, out...)
		}
		_, out := st.Flush()
		got = append(got, out...)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("split by %d lines: %q, want %q", size, got, want)
		}
	}
}

// replyText is a reply with paragraphs, code and a table, as Claude writes
// them.
const replyText = "## 変更点\n" +
	"`Shape` を足しました。テストも書きました!\n" +
	"\n" +
	"```go\n" +
	"func Shape(text string) []string {\n" +
	"\treturn nil // これは読まない。\n" +
	"}\n" +
	"```\n" +
	"\n" +
	"| 項目 | 結果 |\n" +
	"|---|---|\n" +
	"| test | pass |\n" +
	"\n" +
	"The buffer keeps\n" +
	"the partial line. See internal/voice/read.go for details.\n" +
	"- 1 つ目の項目\n" +
	"- 2 つ目の項目\n" +
	"最後の段落です。"

// The sentences of replyText.
var replyWant = []string{
	"変更点",
	"Shape を足しました。",
	"テストも書きました!",
	"The buffer keeps the partial line.",
	"See read for details.",
	"1 つ目の項目",
	"2 つ目の項目",
	"最後の段落です。",
}

func TestReplyTextShape(t *testing.T) {
	if got := Shape(replyText, DefaultSkip, LangJa); !reflect.DeepEqual(got, replyWant) {
		t.Errorf("Shape(replyText) = %q, want %q", got, replyWant)
	}
}
