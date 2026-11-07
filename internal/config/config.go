// Package config loads ~/.config/boba/config.toml (global) and
// ~/.config/boba/repos/<repo-id>.toml (per repository).
// boba knows nothing about package managers at runtime: hooks are plain
// shell command lists with {{var}} substitution (design: ただのシェル).
package config

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"boba/internal/paths"
)

type Worktree struct {
	Copy  []string `toml:"copy"`
	Clone []string `toml:"clone"`
	Base  string   `toml:"base"` // base branch, default: origin HEAD or main
}

type Hooks struct {
	Setup    []string `toml:"setup"`
	Dev      string   `toml:"dev"`
	Test     string   `toml:"test"`
	Teardown []string `toml:"teardown"`
}

type Review struct {
	// Gatekeeper is a read-only agent command that receives the diff on stdin
	// and prints "path:line: text" lines. Empty disables it.
	Gatekeeper string `toml:"gatekeeper"`
}

type Repo struct {
	Worktree Worktree          `toml:"worktree"`
	Hooks    Hooks             `toml:"hooks"`
	Env      map[string]string `toml:"env"`
	Review   Review            `toml:"review"`
	Agent    string            `toml:"agent"`
}

type Agent struct {
	Command string `toml:"command"` // e.g. "claude"
	Resume  string `toml:"resume"`  // e.g. "claude --resume {{session_id}}"
	Prompt  string `toml:"prompt"`  // how to pass an initial prompt: "arg" | "paste"
	Allow   string `toml:"allow"`   // keys answering a permission prompt with yes
	Always  string `toml:"always"`
	Deny    string `toml:"deny"`
	Hooks   bool   `toml:"hooks"` // agent reports state through `boba event`
}

type Theme struct {
	Mode   string            `toml:"mode"`  // truecolor | ansi | none | auto
	Icons  string            `toml:"icons"` // nerd | unicode | ascii
	Colors map[string]string `toml:"colors"`
	ANSI   map[string]int    `toml:"ansi"`
}

// Mascot is [mascot]: tapi, the pearl (docs/mascot.md).
type Mascot struct {
	Shape   string            `toml:"shape"`   // auto | round | block
	Glyphs  string            `toml:"glyphs"`  // auto | unicode | narrow | ascii
	Palette string            `toml:"palette"` // dark (D) | light (E)
	Colors  map[string]string `toml:"colors"`  // hi bd sh eye
	Splash  *bool             `toml:"splash"`
}

func (m Mascot) SplashOn() bool { return m.Splash == nil || *m.Splash }

// LightPalette: palette = "light" | "E".
func (m Mascot) LightPalette() bool {
	p := strings.ToLower(m.Palette)
	return p == "light" || p == "e"
}

type Global struct {
	HTTP struct {
		Port int `toml:"port"`
	} `toml:"http"`
	Keys struct {
		Prefix string            `toml:"prefix"`
		Map    map[string]string `toml:"map"`
	} `toml:"keys"`
	Theme  Theme            `toml:"theme"`
	Mascot Mascot           `toml:"mascot"`
	Agents map[string]Agent `toml:"agents"`
	Notify struct {
		Ntfy string `toml:"ntfy"` // e.g. https://ntfy.sh/my-topic
	} `toml:"notify"`
	Review struct {
		Surface string `toml:"surface"` // nvim | tui
	} `toml:"review"`
	Ports struct {
		Base int `toml:"base"`
	} `toml:"ports"`
	Animate *bool `toml:"animate"`
	Picks   struct {
		AutoSend *bool `toml:"auto_send"`
	} `toml:"picks"`
}

func DefaultAgents() map[string]Agent {
	return map[string]Agent{
		"claude": {
			Command: "claude",
			Resume:  "claude --resume {{session_id}}",
			Prompt:  "arg",
			Allow:   "1", Always: "2", Deny: "Escape",
			Hooks: true,
		},
		"codex": {
			Command: "codex", Prompt: "arg",
			Allow: "y", Always: "a", Deny: "n",
		},
		"opencode": {
			Command: "opencode", Prompt: "paste",
			Allow: "y", Always: "a", Deny: "n",
		},
	}
}

func LoadGlobal() (*Global, error) {
	g := &Global{}
	if b, err := os.ReadFile(paths.GlobalConfig()); err == nil {
		if _, err := toml.Decode(string(b), g); err != nil {
			return nil, fmt.Errorf("%s: %w", paths.GlobalConfig(), err)
		}
	}
	if g.HTTP.Port == 0 {
		g.HTTP.Port = 47777
	}
	if g.Ports.Base == 0 {
		g.Ports.Base = 3001
	}
	if g.Keys.Prefix == "" {
		g.Keys.Prefix = "ctrl+q" // reachable on JIS keyboards; unused by Claude Code
	}
	agents := DefaultAgents()
	for k, v := range g.Agents {
		d := agents[k]
		if v.Command != "" {
			d.Command = v.Command
		}
		if v.Resume != "" {
			d.Resume = v.Resume
		}
		if v.Prompt != "" {
			d.Prompt = v.Prompt
		}
		if v.Allow != "" {
			d.Allow = v.Allow
		}
		if v.Always != "" {
			d.Always = v.Always
		}
		if v.Deny != "" {
			d.Deny = v.Deny
		}
		d.Hooks = d.Hooks || v.Hooks
		agents[k] = d
	}
	g.Agents = agents
	if g.Review.Surface == "" {
		g.Review.Surface = "nvim"
	}
	return g, nil
}

func (g *Global) AnimateOn() bool  { return g.Animate == nil || *g.Animate }
func (g *Global) AutoSendOn() bool { return g.Picks.AutoSend == nil || *g.Picks.AutoSend }

// LoadRepo reads the per-repo config. A missing file is not an error.
func LoadRepo(repoID string) (*Repo, bool, error) {
	r := &Repo{}
	b, err := os.ReadFile(paths.RepoConfig(repoID))
	if os.IsNotExist(err) {
		return r, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if _, err := toml.Decode(string(b), r); err != nil {
		return nil, true, fmt.Errorf("%s: %w", paths.RepoConfig(repoID), err)
	}
	return r, true, nil
}

// Vars are the five template variables: slug, branch, port, path, repo.
type Vars struct {
	Slug, Branch, Path, Repo string
	Port                     int
	Extra                    map[string]string
}

func (v Vars) Expand(s string) string {
	r := strings.NewReplacer(
		"{{slug}}", v.Slug,
		"{{branch}}", v.Branch,
		"{{port}}", fmt.Sprint(v.Port),
		"{{path}}", v.Path,
		"{{repo}}", v.Repo,
	)
	s = r.Replace(s)
	for k, val := range v.Extra {
		s = strings.ReplaceAll(s, "{{"+k+"}}", val)
	}
	return s
}

// EnvList expands [env] into KEY=VALUE pairs, sorted for determinism.
func (r *Repo) EnvList(v Vars) []string {
	keys := make([]string, 0, len(r.Env))
	for k := range r.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+v.Expand(r.Env[k]))
	}
	return out
}
