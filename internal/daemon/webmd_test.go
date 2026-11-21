package daemon

import (
	"encoding/json"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// runWebModule runs prog as an ES module in node, with the page's files
// (webui/, as embedded) written to a temp dir: prog imports them by name,
// e.g. import { mdSpans } from "lib/md.js" (or "gui/views/sessions.js").
// Skipped without node.
func runWebModule(t *testing.T, prog string) []byte {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found: the page's modules are not run")
	}
	dir := t.TempDir()
	err = fs.WalkDir(webFiles, "webui", func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		b, err := webFiles.ReadFile(p)
		if err != nil {
			return err
		}
		to := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		return os.WriteFile(to, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	base := (&url.URL{Scheme: "file", Path: filepath.ToSlash(filepath.Join(dir, "webui")) + "/"}).String()
	prog = regexp.MustCompile(`from "((?:lib|gui|gui/views)/[a-z]+\.js)"`).ReplaceAllStringFunc(prog, func(m string) string {
		return `from "` + base + strings.TrimSuffix(strings.TrimPrefix(m, `from "`), `"`) + `"`
	})
	out, err := exec.Command(node, "--input-type=module", "-e", prog).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	return out
}

// The bubbles' markdown (docs/web.md 「描画」): **bold** is bold on one
// line, around text and code but never inside code (inline or a fence),
// and an unclosed ** stays as written. mdSpans (lib/md.js, shared by both
// pages) is imported as it is and run in node (skipped without node); the
// page builds elements from its parts through textContent only
// (TestWebInboxScriptIsText: no HTML from strings).
func TestWebMarkdownBold(t *testing.T) {
	js, err := webFiles.ReadFile("webui/lib/md.js")
	if err != nil {
		t.Fatal(err)
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
	prog := `import { mdSpans } from "lib/md.js";` + "\nconsole.log(JSON.stringify(" + string(in) + ".map((s) => JSON.stringify(mdSpans(s)))));\n"
	out := runWebModule(t, prog)
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
