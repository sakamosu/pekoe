// Package initdetect drafts ~/.config/pekoe/repos/<repo-id>.toml for
// `pekoe init`. It is only used to write the draft; pekoe never consults it at
// runtime. Rules live in ~/.config/pekoe/defaults/*.toml and can be edited.
package initdetect

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"pekoe/internal/gitx"
	"pekoe/internal/paths"
)

type Rule struct {
	Name  string   `toml:"name"`
	Files []string `toml:"files"` // any of these existing triggers the rule
	Setup []string `toml:"setup"`
	Dev   string   `toml:"dev"`
	Test  string   `toml:"test"`
	Clone []string `toml:"clone"`
	Order int      `toml:"order"`
}

type ruleFile struct {
	Rule []Rule `toml:"rule"`
}

const builtin = `# pekoe init の検出ルール。先頭から順に評価し、files のどれかが存在すれば採用する。
# 自由に書き換えてよい(pekoe は実行時にこのファイルを読まない)。

[[rule]]
name  = "mise"
files = [".tool-versions", "mise.toml", ".mise.toml", ".nvmrc", ".node-version", ".python-version", ".ruby-version"]
setup = ["mise install"]
order = 0

[[rule]]
name  = "direnv"
files = [".envrc"]
setup = ["direnv allow"]
order = 1

[[rule]]
name  = "pnpm"
files = ["pnpm-lock.yaml"]
setup = ["pnpm install --offline || pnpm install"]
dev   = "pnpm dev --port {{port}}"
test  = "pnpm test"
order = 10

[[rule]]
name  = "bun"
files = ["bun.lock", "bun.lockb"]
setup = ["bun install"]
dev   = "bun run dev --port {{port}}"
test  = "bun test"
order = 10

[[rule]]
name  = "yarn"
files = ["yarn.lock"]
clone = ["node_modules"]
setup = ["yarn install --prefer-offline"]
dev   = "yarn dev --port {{port}}"
test  = "yarn test"
order = 10

[[rule]]
name  = "npm"
files = ["package-lock.json"]
clone = ["node_modules"]
setup = ["npm install --prefer-offline"]
dev   = "npm run dev -- --port {{port}}"
test  = "npm test"
order = 10

[[rule]]
name  = "uv"
files = ["uv.lock"]
setup = ["uv sync"]
test  = "uv run pytest"
order = 10

[[rule]]
name  = "poetry"
files = ["poetry.lock"]
setup = ["poetry install"]
test  = "poetry run pytest"
order = 10

[[rule]]
name  = "cargo"
files = ["Cargo.toml"]
setup = ["cargo fetch"]
test  = "cargo test"
order = 10

[[rule]]
name  = "go"
files = ["go.mod"]
setup = ["go mod download"]
test  = "go test ./..."
order = 10

[[rule]]
name  = "bundler"
files = ["Gemfile"]
setup = ["bundle install"]
dev   = "bin/rails server -p {{port}}"
test  = "bin/rails test"
order = 10
`

// Rules loads ~/.config/pekoe/defaults/*.toml, writing the builtin set the
// first time so the user has something to edit.
func Rules() ([]Rule, error) {
	dir := paths.DefaultsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.toml"))
	if len(files) == 0 {
		p := filepath.Join(dir, "init.toml")
		if err := os.WriteFile(p, []byte(builtin), 0o644); err != nil {
			return nil, err
		}
		files = []string{p}
	}
	var rules []Rule
	for _, f := range files {
		var rf ruleFile
		if _, err := toml.DecodeFile(f, &rf); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		rules = append(rules, rf.Rule...)
	}
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].Order < rules[j].Order })
	return rules, nil
}

// common heavy/generated dirs that should not be offered for copy
var skip = []string{"node_modules", "dist", "build", "out", ".next", ".nuxt", ".svelte-kit", "target",
	"vendor", ".venv", "venv", "__pycache__", ".cache", "coverage", ".turbo", "tmp", "log", "logs",
	".DS_Store", ".idea", ".vscode", ".pytest_cache", ".mypy_cache", ".ruff_cache", ".gradle", ".parcel-cache"}

func skipped(p string) bool {
	first := strings.SplitN(strings.TrimSuffix(p, "/"), "/", 2)[0]
	base := filepath.Base(strings.TrimSuffix(p, "/"))
	for _, s := range skip {
		if first == s || base == s {
			return true
		}
	}
	return strings.HasSuffix(p, ".log") || strings.HasSuffix(p, ".pyc")
}

type Result struct {
	RepoID     string   `json:"repo_id"`
	ConfigPath string   `json:"config_path"`
	Exists     bool     `json:"exists"`
	Matched    []string `json:"matched"`
	Ignored    []string `json:"ignored"`  // candidates for [worktree] copy
	Selected   []string `json:"selected"` // pre-selected (env-like)
	TOML       string   `json:"toml"`
}

func exists(dir, f string) bool {
	_, err := os.Stat(filepath.Join(dir, f))
	return err == nil
}

func Detect(dir, repoID string) (*Result, error) {
	rules, err := Rules()
	if err != nil {
		return nil, err
	}
	res := &Result{RepoID: repoID, ConfigPath: paths.RepoConfig(repoID)}
	if _, err := os.Stat(res.ConfigPath); err == nil {
		res.Exists = true
	}
	var setup, clone []string
	dev, test := "", ""
	pm := false
	for _, r := range rules {
		hit := false
		for _, f := range r.Files {
			if exists(dir, f) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		if r.Order >= 10 && pm {
			continue // one package manager per stack level
		}
		if r.Order >= 10 {
			pm = true
		}
		res.Matched = append(res.Matched, r.Name)
		setup = append(setup, r.Setup...)
		clone = append(clone, r.Clone...)
		if dev == "" {
			dev = r.Dev
		}
		if test == "" {
			test = r.Test
		}
	}
	ign, _ := gitx.IgnoredFiles(dir)
	for _, p := range ign {
		if p == "" || skipped(p) {
			continue
		}
		res.Ignored = append(res.Ignored, strings.TrimSuffix(p, "/"))
		b := filepath.Base(p)
		if strings.HasPrefix(b, ".env") || b == ".envrc" || strings.Contains(p, ".local.") || strings.HasSuffix(b, ".local") {
			res.Selected = append(res.Selected, strings.TrimSuffix(p, "/"))
		}
	}
	res.TOML = Render(res.Selected, clone, setup, dev, test)
	return res, nil
}

func q(s string) string {
	var b bytes.Buffer
	toml.NewEncoder(&b).Encode(map[string]string{"v": s})
	return strings.TrimSpace(strings.TrimPrefix(b.String(), "v = "))
}

func list(xs []string) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = q(x)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// Render produces the config draft. Commands are plain shell lines.
func Render(copyFiles, clone, setup []string, dev, test string) string {
	var b strings.Builder
	b.WriteString("# pekoe repo config (pekoe init が下書き。実行時に検出はしない)\n")
	b.WriteString("# 変数: {{slug}} {{branch}} {{port}} {{path}} {{repo}}\n\n")
	b.WriteString("[worktree]\n")
	fmt.Fprintf(&b, "copy  = %s\n", list(copyFiles))
	fmt.Fprintf(&b, "clone = %s          # APFS クローン(cp -c)\n", list(clone))
	b.WriteString("# base = \"main\"\n\n")
	b.WriteString("[hooks]\n")
	fmt.Fprintf(&b, "setup    = %s\n", list(setup))
	if dev != "" {
		fmt.Fprintf(&b, "dev      = %s\n", q(dev))
	} else {
		b.WriteString("# dev    = \"pnpm dev --port {{port}}\"\n")
	}
	if test != "" {
		fmt.Fprintf(&b, "test     = %s\n", q(test))
	} else {
		b.WriteString("# test   = \"make test\"\n")
	}
	b.WriteString("teardown = []\n\n")
	b.WriteString("[env]\n")
	b.WriteString("PORT = \"{{port}}\"\n")
	b.WriteString("COMPOSE_PROJECT_NAME = \"{{slug}}\"\n\n")
	b.WriteString("[review]\n")
	b.WriteString("# 読む順序を決める門番。diff を stdin で受け取り path:line: text を出す読み取り専用エージェント\n")
	b.WriteString("# gatekeeper = \"claude -p 'この diff の危ない箇所だけを path:line: 理由 の形式で1行ずつ列挙して。他は何も書かないで。' --allowedTools ''\"\n")
	return b.String()
}
