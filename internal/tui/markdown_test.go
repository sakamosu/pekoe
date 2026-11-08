package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"pekoe/internal/daemon"
	"pekoe/internal/theme"
)

func mdTheme(t *testing.T, mode string) *theme.Theme {
	t.Helper()
	t.Setenv("PEKOE_COLOR", mode)
	return theme.Load(nil)
}

func mdPlain(lines []string) string {
	return ansi.Strip(strings.Join(lines, "\n"))
}

// spanAt returns the style of the span holding sub.
func spanAt(t *testing.T, spans []mdSpan, sub string) mdStyle {
	t.Helper()
	for _, sp := range spans {
		if strings.Contains(sp.text, sub) {
			return sp.st
		}
	}
	t.Fatalf("no span holds %q: %+v", sub, spans)
	return mdStyle{}
}

func TestInlineEmphasis(t *testing.T) {
	cases := []struct {
		in, text string
		want     mdStyle
	}{
		{"a **bold** b", "bold", mdStyle{bold: true}},
		{"a __bold__ b", "bold", mdStyle{bold: true}},
		{"a *it* b", "it", mdStyle{italic: true}},
		{"a _it_ b", "it", mdStyle{italic: true}},
		{"a ***both*** b", "both", mdStyle{bold: true, italic: true}},
		{"a ~~gone~~ b", "gone", mdStyle{strike: true}},
		{"これは**太字**です", "太字", mdStyle{bold: true}},
		{"**outer *inner* x**", "inner", mdStyle{bold: true, italic: true}},
		{"run `go test ./...` now", "go test ./...", mdStyle{code: true}},
		{"``a ` b``", "a ` b", mdStyle{code: true}},
	}
	for _, c := range cases {
		spans := parseInline(c.in, mdStyle{})
		if got := spanAt(t, spans, c.text); got != c.want {
			t.Errorf("%q: %q styled %+v, want %+v", c.in, c.text, got, c.want)
		}
		var all strings.Builder
		for _, sp := range spans {
			all.WriteString(sp.text)
		}
		if strings.ContainsAny(strings.ReplaceAll(all.String(), "`", ""), "*~") || (strings.Contains(all.String(), "_") && !strings.Contains(c.in, "`")) {
			t.Errorf("%q: markers left in %q", c.in, all.String())
		}
	}
}

func TestInlineLeavesPlainTextAlone(t *testing.T) {
	for _, in := range []string{
		"snake_case_name and file_name.py",
		"2 * 3 * 4",
		"a ~ b ~ c",
		"**unclosed",
		`\*not italic\*`,
		"`code with **stars** and [x](https://e.x)`",
	} {
		spans := parseInline(in, mdStyle{})
		var got strings.Builder
		for _, sp := range spans {
			if sp.st.bold || sp.st.italic || sp.st.strike || sp.st.url != "" {
				t.Errorf("%q: unexpected styled span %+v", in, sp)
			}
			got.WriteString(sp.text)
		}
		want := in
		switch in {
		case `\*not italic\*`:
			want = "*not italic*"
		case "`code with **stars** and [x](https://e.x)`":
			want = "code with **stars** and [x](https://e.x)"
		}
		if got.String() != want {
			t.Errorf("%q rendered as %q, want %q", in, got.String(), want)
		}
	}
}

func TestInlineLinks(t *testing.T) {
	spans := parseInline("see [the docs](https://e.x/docs \"title\"), ![shot](https://e.x/a.png) or https://e.x/raw.", mdStyle{})
	if st := spanAt(t, spans, "the docs"); st.url != "https://e.x/docs" {
		t.Errorf("link target %q", st.url)
	}
	if st := spanAt(t, spans, "shot"); st.url != "https://e.x/a.png" {
		t.Errorf("image target %q", st.url)
	}
	if st := spanAt(t, spans, "https://e.x/raw"); st.url != "https://e.x/raw" {
		t.Errorf("bare URL target %q (the closing period stays text)", st.url)
	}
	if st := spanAt(t, parseInline("[**b** text](https://e.x)", mdStyle{}), "b"); !st.bold || st.url != "https://e.x" {
		t.Errorf("markup inside link text: %+v", st)
	}
	// only web links are handed to the OS
	for _, in := range []string{"[x](file:///Applications/Calculator.app)", "[x](javascript:alert(1))", "[x](./docs/a.md)"} {
		if st := spanAt(t, parseInline(in, mdStyle{}), "x"); st.url != "" {
			t.Errorf("%q must not be clickable: %+v", in, st)
		}
	}
}

func TestRenderLinksAreClickable(t *testing.T) {
	th := mdTheme(t, "truecolor")
	lines := renderMarkdown(th, theme.Icons("unicode"), "go to [the issue tracker](https://e.x/i) now", 12)
	if strings.Contains(mdPlain(lines), "](") {
		t.Fatalf("link syntax shown: %q", mdPlain(lines))
	}
	found := 0
	for _, l := range lines {
		s := ansi.Strip(l)
		for x := range ansi.StringWidth(s) {
			if linkAt(l, x) == "https://e.x/i" {
				found++
				break
			}
		}
	}
	if found < 2 {
		t.Fatalf("the wrapped link text should be clickable on each of its lines, got %d: %q", found, lines)
	}
	if got := linkAt(lines[0], 0); got != "" {
		t.Fatalf("plain text is not a link: %q", got)
	}
}

func TestRenderTaskList(t *testing.T) {
	th := mdTheme(t, "none")
	body := "- [ ] todo\n- [x] done\n* [X] also"
	got := mdPlain(renderMarkdown(th, theme.Icons("unicode"), body, 40))
	if got != "☐ todo\n☑ done\n☑ also" {
		t.Fatalf("unicode task list:\n%s", got)
	}
	got = mdPlain(renderMarkdown(th, theme.Icons("ascii"), body, 40))
	if got != "[ ] todo\n[x] done\n[x] also" {
		t.Fatalf("ascii task list:\n%s", got)
	}
}

func TestRenderTable(t *testing.T) {
	th := mdTheme(t, "none")
	body := "| name | count |\n|:-----|------:|\n| `a\\|b` | 1 |\n| 日本語 | 1234 |"
	got := renderMarkdown(th, theme.Icons("unicode"), body, 40)
	want := []string{
		"name   │ count",
		"───────┼──────",
		"`a|b`  │     1",
		"日本語 │  1234",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("table:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// narrow: columns shrink and never overflow
	long := "| a very long header | another long header |\n|---|---|\n| some long cell text | 日本語のとても長いセル |"
	for _, cw := range []int{30, 12, 5, 2} {
		for _, l := range renderMarkdown(th, theme.Icons("unicode"), long, cw) {
			if ansi.StringWidth(l) > cw {
				t.Fatalf("width %d: %q is %d wide", cw, l, ansi.StringWidth(l))
			}
		}
	}
}

func TestNoMarkupInCode(t *testing.T) {
	th := mdTheme(t, "truecolor")
	body := "```go\nx := **p** // [l](https://e.x) | a |\n|---|\n- [ ] no\n```\nafter `**x**`"
	got := renderMarkdown(th, theme.Icons("unicode"), body, 60)
	p := mdPlain(got)
	for _, want := range []string{"x := **p** // [l](https://e.x) | a |", "|---|", "- [ ] no", "**x**"} {
		if !strings.Contains(p, want) {
			t.Errorf("code must stay as written: %q missing in\n%s", want, p)
		}
	}
	for _, l := range got {
		if strings.Contains(l, "\x1b]8;") {
			t.Errorf("no links inside code: %q", l)
		}
	}
}

// Every line closes what it opens, so the panel border and padding after it
// keep their own colors, also when wrapping or clipping cuts a span.
func TestStylesStayBalanced(t *testing.T) {
	th := mdTheme(t, "truecolor")
	body := "# 見出し **太字**\n\n本文に **とても長い太字の文章が入っていて折り返しをまたぐ** 場合と ~~取り消し~~ と `インラインコード` と [リンクの文字列](https://e.x/long/path) を混ぜる。\n\n> 引用の *斜体* が https://e.x/bare/url/that/is/long に続く\n\n- 項目 **太字** の中身がとても長くて折り返しが必要になる"
	for _, cw := range []int{8, 13, 20, 41} {
		lines := renderMarkdown(th, theme.Icons("unicode"), body, cw)
		for _, l := range lines {
			if got := ansi.StringWidth(l); got > cw {
				t.Fatalf("cw=%d: %q is %d wide", cw, ansi.Strip(l), got)
			}
			for _, cut := range []string{l, clip(l, cw/2), fit(l, cw/2)} {
				assertClosed(t, cut)
			}
		}
		if p := mdPlain(lines); strings.ContainsAny(p, "*~`") {
			t.Fatalf("cw=%d: markers left:\n%s", cw, p)
		}
	}
}

func assertClosed(t *testing.T, l string) {
	t.Helper()
	var (
		p     = ansi.NewParser()
		state byte
		open  bool
		link  bool
	)
	for s := l; len(s) > 0; {
		seq, width, n, ns := ansi.DecodeSequence(s, state, p)
		state = ns
		s = s[n:]
		if width > 0 {
			continue
		}
		switch {
		case strings.HasPrefix(seq, "\x1b]8;"):
			link = osc8Target(seq) != ""
		case seq == "\x1b[0m" || seq == "\x1b[m":
			open = false
		case strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m"):
			open = true
		}
	}
	if open || link {
		t.Fatalf("line leaves a style (%v) or link (%v) open: %q", open, link, l)
	}
}

func TestRenderNoneModeIsPlain(t *testing.T) {
	th := mdTheme(t, "none")
	got := renderMarkdown(th, theme.Icons("unicode"), "a **b** `c` ~~d~~", 40)
	if len(got) != 1 || ansi.Strip(got[0]) != "a b `c` d" {
		t.Fatalf("none mode: %q", got)
	}
}

func TestMdFirstLine(t *testing.T) {
	if got := mdFirstLine("<!-- template -->\n\n## Summary **now**\n\nbody"); got != "Summary now" {
		t.Fatalf("got %q", got)
	}
}

// PR descriptions, reviews and comments use the same renderer.
func TestPRBodyAndCommentsAreMarkdown(t *testing.T) {
	m := prModel(t, "truecolor", 140, 50)
	v := samplePR()
	v.Body = "## Summary\n\n- [x] **stream** rows via [the API](https://e.x/api)"
	v.Comments[0].Body = "LGTM after `fixes`"
	m.onPRMsg(prMsg{slug: "csv", view: v})
	items := prItems(m.pr.view)
	if len(items) == 0 || items[0].kind != "body" {
		t.Fatalf("the description is the first item: %+v", items)
	}
	if line := ansi.Strip(m.prItemLine(items[0], 60)); !strings.Contains(line, "Summary") || strings.Contains(line, "#") {
		t.Fatalf("description row: %q", line)
	}
	m.pr.open["b"] = true
	exp := m.prExpansion(items[0], 60)
	if p := mdPlain(exp); !strings.Contains(p, "☑ stream rows via the API") {
		t.Fatalf("description expansion:\n%s", p)
	}
	var cm prItem
	for _, it := range items {
		if it.kind == "comment" {
			cm = it
		}
	}
	if p := mdPlain(m.prExpansion(cm, 60)); p != "LGTM after fixes" {
		t.Fatalf("comment expansion: %q", p)
	}
	// the link in the opened description is clickable where it is drawn
	m.pr.top = 0
	viewLines(t, m)
	aw, ah := m.agentSize()
	rows, _, _ := m.prRows(aw)
	hit := false
	for y := 0; y < min(len(rows), ah) && !hit; y++ {
		for x := range aw {
			if m.prLinkAt(x, y) == "https://e.x/api" {
				hit = true
				break
			}
		}
	}
	if !hit {
		t.Fatal("the description's link should be clickable")
	}
	// the description is not something to send to an agent
	m.pr.sel = 0
	if cmd := m.prSend(); cmd != nil {
		t.Fatal("sending only the description should do nothing")
	}
}

func TestIssueLinkClick(t *testing.T) {
	m := issueModel(t, "truecolor", 160)
	m.focus, m.sub2 = pAgent, subIssues
	m.iss.open = true
	m.iss.ensure()
	cur := m.curIssue()
	m.iss.details[cur.Number] = &daemon.IssueDetail{Issue: *cur, State: "OPEN", Body: "see [the log](https://e.x/log)"}
	aw, ah := m.agentSize()
	for y := range ah {
		for x := range aw {
			if m.issueLinkAt(x, y) == "https://e.x/log" {
				return
			}
		}
	}
	t.Fatal("the issue body's link should be clickable")
}
