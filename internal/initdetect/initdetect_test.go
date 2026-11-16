package initdetect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pekoe/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// fakeBin puts an executable named name on a PATH of its own.
func fakeBin(t *testing.T, names ...string) {
	t.Helper()
	bin := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(bin, n), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
}

func repoWith(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func hooksTest(toml string) string {
	for _, l := range strings.Split(toml, "\n") {
		if strings.HasPrefix(l, "test") || strings.HasPrefix(l, "# test") {
			return l
		}
	}
	return ""
}

// A yarn.lock alone no longer drafts `test = "yarn test"`: yarn has to be
// on the PATH the hooks run with, and package.json has to have a test
// script. Otherwise the line is drafted commented out, with the reason.
func TestDetectDraftsTestOnlyWhenItCanRun(t *testing.T) {
	withScript := `{"scripts":{"test":"jest"}}`
	cases := []struct {
		name  string
		bins  []string
		files map[string]string
		want  string // the [hooks] test line
		skip  string // TestSkipped contains
	}{
		{"yarn and a test script", []string{"yarn"}, map[string]string{"yarn.lock": "", "package.json": withScript},
			`test     = "yarn test"`, ""},
		{"no yarn on PATH", nil, map[string]string{"yarn.lock": "", "package.json": withScript},
			`# test   = "yarn test"   # 書かなかった: yarn が PATH に無い`, "yarn が PATH に無い"},
		{"no test script (Laravel)", []string{"yarn"}, map[string]string{"yarn.lock": "", "package.json": `{"scripts":{"dev":"mix"}}`},
			`# test   = "yarn test"   # 書かなかった: package.json に scripts.test が無い`, "scripts.test"},
		{"neither", nil, map[string]string{"yarn.lock": "", "package.json": `{"private":true}`},
			`# test   = "yarn test"   # 書かなかった: package.json に scripts.test が無い・yarn が PATH に無い`, "yarn が PATH に無い"},
		{"npm's placeholder script", []string{"npm"}, map[string]string{"package-lock.json": "", "package.json": `{"scripts":{"test":"echo \"Error: no test specified\" && exit 1"}}`},
			`# test   = "npm test"   # 書かなかった: package.json に scripts.test が無い`, "scripts.test"},
		{"no package.json", []string{"pnpm"}, map[string]string{"pnpm-lock.yaml": ""},
			`# test   = "pnpm test"   # 書かなかった: package.json に scripts.test が無い`, "scripts.test"},
		{"go", []string{"go"}, map[string]string{"go.mod": "module x\n"},
			`test     = "go test ./..."`, ""},
		{"no go", nil, map[string]string{"go.mod": "module x\n"},
			`# test   = "go test ./..."   # 書かなかった: go が PATH に無い`, "go が PATH に無い"},
		{"bun's own runner needs no script", []string{"bun"}, map[string]string{"bun.lock": ""},
			`test     = "bun test"`, ""},
		{"bin/rails is looked for in the repository", []string{"bundle"}, map[string]string{"Gemfile": ""},
			`# test   = "bin/rails test"   # 書かなかった: bin/rails がリポジトリに無い`, "bin/rails"},
		{"nothing detected", nil, map[string]string{},
			`# test   = "make test"`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("PEKOE_HOME", t.TempDir()) // the builtin rules, fresh
			fakeBin(t, c.bins...)
			res, err := Detect(repoWith(t, c.files), "example.com_x_y")
			if err != nil {
				t.Fatal(err)
			}
			if got := hooksTest(res.TOML); got != c.want {
				t.Errorf("test line\n got: %s\nwant: %s", got, c.want)
			}
			if (c.skip == "") != (res.TestSkipped == "") || !strings.Contains(res.TestSkipped, c.skip) {
				t.Errorf("TestSkipped = %q, want it to contain %q", res.TestSkipped, c.skip)
			}
		})
	}
}
