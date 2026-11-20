package daemon

import (
	"encoding/json"
	"os/exec"
	"regexp"
	"testing"
)

// The bubbles' markdown (docs/web.md 「描画」): **bold** is bold on one
// line, around text and code but never inside code (inline or a fence),
// and an unclosed ** stays as written. mdSpans is taken out of app.js as it
// is and run in node (skipped without node); the page builds elements from
// its parts through textContent only (TestWebInbox: no HTML from strings).
func TestWebMarkdownBold(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found: the bubbles' markdown is not run")
	}
	js, err := webFiles.ReadFile("webui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := regexp.MustCompile(`(?s)\nfunction mdSpans\(s\) \{\n.*?\n\}\n`).Find(js)
	if src == nil {
		t.Fatal("mdSpans not found in app.js")
	}
	cases := []struct{ in, want string }{
		{"**画像の流れ**", `[{"bold":[{"text":"画像の流れ"}]}]`},
		{"1. **iPhone の中**: 選んだ画像", `[{"text":"1. "},{"bold":[{"text":"iPhone の中"}]},{"text":": 選んだ画像"}]`},
		{"**a** and **b**", `[{"bold":[{"text":"a"}]},{"text":" and "},{"bold":[{"text":"b"}]}]`},
		{"**see `x`**", `[{"bold":[{"text":"see "},{"code":"x"}]}]`},
		// inside code: not bold
		{"`**not bold**`", `[{"code":"**not bold**"}]`},
		{"`a**` b **c**", `[{"code":"a**"},{"text":" b "},{"bold":[{"text":"c"}]}]`},
		{"**x `a**b` y**", `[{"bold":[{"text":"x "},{"code":"a**b"},{"text":" y"}]}]`},
		// unclosed, or with a blank inside the stars: as written
		{"**open", `[{"text":"**open"}]`},
		{"a ** b ** c", `[{"text":"a ** b ** c"}]`},
		{"**", `[{"text":"**"}]`},
		{"****", `[{"text":"****"}]`},
		{"2 ** 3 = **8**", `[{"text":"2 ** 3 = "},{"bold":[{"text":"8"}]}]`},
		// the words stay words: tags are text
		{"**<b>x</b>**", `[{"bold":[{"text":"<b>x</b>"}]}]`},
		{"plain `code` text", `[{"text":"plain "},{"code":"code"},{"text":" text"}]`},
	}
	ins := make([]string, len(cases))
	for i, c := range cases {
		ins[i] = c.in
	}
	in, _ := json.Marshal(ins)
	prog := string(src) + "\nconsole.log(JSON.stringify(" + string(in) + ".map((s) => JSON.stringify(mdSpans(s)))));\n"
	out, err := exec.Command(node, "-e", prog).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	var got []string
	if err := json.Unmarshal(out, &got); err != nil || len(got) != len(cases) {
		t.Fatalf("node output: %v\n%s", err, out)
	}
	for i, c := range cases {
		if got[i] != c.want {
			t.Errorf("%q:\n got %s\nwant %s", c.in, got[i], c.want)
		}
	}

	// fences are not inline: mdLines keeps a fence's lines as written
	if !regexp.MustCompile(`fence\.append\(txt\(`).Match(js) {
		t.Error("a fence's lines are no longer plain text")
	}
}
