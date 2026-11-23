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
		{"inline code long", "`aaaaaaaaaaaaaaaaaaaaaaaaa` を足した", []string{"を足した"}},
		{"inline code with slash", "`a/b` を見た", []string{"を見た"}},
		{"table", "表です。\n| a | b |\n|---|---|\n| 1 | 2 |\n後", []string{"表です。", "後"}},
		{"url", "See https://example.com/x, then stop.", []string{"See, then stop."}},
		{"link text only", "[docs](https://x.y/z) を読んで", []string{"docs を読んで"}},
		{"autolink", "<https://x.y> here", []string{"here"}},
		{"image", "![logo](a.png) 画像", []string{"画像"}},
		{"path with extension", "internal/voice/shape.go を直した", []string{"を直した"}},
		{"path relative", "./run と ~/x と ../y と /etc を見た", []string{"と と と を見た"}},
		{"path at the end of a sentence", "I changed internal/voice/read.go. Then ran tests.", []string{"I changed.", "Then ran tests."}},
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
		got := Shape(c.text, DefaultSkip)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: Shape(%q) = %q, want %q", c.name, c.text, got, c.want)
		}
	}
}

// Rule 6.2: what is not in skip is read.
func TestRule6_2ShapeNoSkip(t *testing.T) {
	text := "```\nfunc main()\n```\n| a | b |\n|---|---|\n`internal/x.go` と https://x.y と ./a.go"
	got := Shape(text, nil)
	want := []string{"func main()", "a、b", "internal/x.go と https://x.y と ./a.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Shape(nil skip) = %q, want %q", got, want)
	}
	got = Shape(text, []string{SkipCode})
	want = []string{"a、b", "と https://x.y と ./a.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Shape(code) = %q, want %q", got, want)
	}
}

// Rule 6.2: a sentence over 200 characters is cut after 、 or , (hard at
// 200 when it has neither).
func TestRule6_2ShapeLongSentence(t *testing.T) {
	a := strings.Repeat("あ", 150)
	b := strings.Repeat("い", 150)
	got := Shape(a+"、"+b+"。", DefaultSkip)
	want := []string{a + "、", b + "。"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cut at 、: %q", got)
	}
	got = Shape(strings.Repeat("x", 450), DefaultSkip)
	if len(got) != 3 || utf8.RuneCountInString(got[0]) != 200 || utf8.RuneCountInString(got[2]) != 50 {
		t.Errorf("hard cut: %d parts", len(got))
	}
	for _, s := range Shape(strings.Repeat("word, ", 100), DefaultSkip) {
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
	want := Shape(text, DefaultSkip)
	for size := 1; size <= len(lines); size++ {
		var st ShapeState
		var got []string
		for i := 0; i < len(lines); i += size {
			end := min(i+size, len(lines))
			var out []string
			st, out = ShapeLines(st, lines[i:end], DefaultSkip)
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
	"See for details.",
	"1 つ目の項目",
	"2 つ目の項目",
	"最後の段落です。",
}

func TestReplyTextShape(t *testing.T) {
	if got := Shape(replyText, DefaultSkip); !reflect.DeepEqual(got, replyWant) {
		t.Errorf("Shape(replyText) = %q, want %q", got, replyWant)
	}
}
