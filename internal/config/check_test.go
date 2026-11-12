package config

import (
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

func problems(ps []Problem) string {
	var out []string
	for _, p := range ps {
		out = append(out, p.String())
	}
	return strings.Join(out, "\n")
}

func TestCheckClean(t *testing.T) {
	for _, src := range []string{"", Template, `
animate = false
[http]
port = 47778
[theme]
mode = "truecolor"
icons = "unicode"
[theme.colors]
primary = "#ff87da"
dim = "#888"
[mascot]
palette = "E"
[mascot.colors]
bd = "#e4c49a"
[review]
surface = "tui"
[orchestrator]
notify = "Quiet"
[cleanup]
stale_after = "36h30m"
[tapi]
agent = "claude-ask"
[agents.claude-ask]
command = "claude --permission-mode plan"
[agents.codex]
allow = "y"
[web]
enable = true
allow_users = ["you@github"]
state_dir = "~/ts"
auth_key = "tskey-x"
`} {
		if ps := Check([]byte(src)); len(ps) != 0 {
			t.Errorf("problems in a good file:\n%s\n---\n%s", problems(ps), src)
		}
	}
}

func TestCheckSyntax(t *testing.T) {
	ps := Check([]byte("[theme]\nmode = \"ansi\"\nicons = nerd\n"))
	if len(ps) != 1 || !ps[0].Fatal || !strings.HasPrefix(ps[0].Msg, "syntax error: line 3") {
		t.Fatalf("got %+v", ps)
	}
	ps = Check([]byte("[theme]\nmode = 1\n"))
	if len(ps) != 1 || !ps[0].Fatal || !strings.HasPrefix(ps[0].Msg, "cannot be read: ") {
		t.Fatalf("got %+v", ps)
	}
}

func TestCheckFindsMistakes(t *testing.T) {
	src := `
animat = true
[http]
port = 70000
[theme]
mode = "256"
icons = "emoji"
[theme.colors]
primary = "pink"
[theme.ansi]
dim = 300
[mascot]
shape = "square"
glyphs = "wide"
palette = "blue"
[mascot.colors]
nose = "#000000"
eye = "black"
[review]
surface = "vim"
[orchestrator]
notify = "loud"
[cleanup]
stale_after = "3 days"
[tapi]
agent = "gemini"
[agents.mine]
prompt = "stdin"
[agents.claude]
sandbx = false
[web]
enable = true
state_dir = "rel/dir"
`
	got := problems(Check([]byte(src)))
	for _, want := range []string{
		"animat: unknown key",
		"agents.claude.sandbx: unknown key",
		"http.port: 70000 is not a port",
		`theme.mode: "256" is not one of auto | truecolor | ansi | none`,
		`theme.icons: "emoji"`,
		`theme.colors.primary: "pink" is not a color`,
		"theme.ansi.dim: 300",
		`mascot.shape: "square"`,
		`mascot.glyphs: "wide"`,
		`mascot.palette: "blue"`,
		"mascot.colors.nose: unknown color",
		`mascot.colors.eye: "black" is not a color`,
		`review.surface: "vim"`,
		`orchestrator.notify: "loud"`,
		`cleanup.stale_after: "3 days" is not a positive Go duration`,
		`tapi.agent: "gemini" is not in [agents.*] (claude, codex, mine, opencode)`,
		`agents.mine.prompt: "stdin"`,
		"agents.mine.command: missing",
		"web.allow_users: empty while enable = true",
		`web.state_dir: "rel/dir" must be an absolute path`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

// Effective spells out the defaults and hides the auth key; the result
// reads back as a valid config.toml.
func TestEffective(t *testing.T) {
	g, err := ParseGlobal([]byte("[web]\nauth_key = \"tskey-secret\"\n[agents.mine]\ncommand = \"x\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := EncodeTOML(Effective(g))
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	if strings.Contains(out, "tskey-secret") || !strings.Contains(out, `auth_key = "(hidden)"`) {
		t.Errorf("auth key not hidden:\n%s", out)
	}
	for _, want := range []string{"animate = true", `stale_after = "72h"`, `notify = "escalate"`, `surface = "nvim"`, "port = 47777", `prefix = "ctrl+q"`, "[agents.mine]", "[agents.claude]"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if ps := Check(b); len(ps) != 0 {
		t.Errorf("show's output does not check clean:\n%s\n---\n%s", problems(ps), out)
	}
	if g.Web.AuthKey != "tskey-secret" || g.Animate != nil {
		t.Error("Effective changed its argument")
	}
	var back Global
	if _, err := toml.Decode(out, &back); err != nil {
		t.Fatal(err)
	}
}

func TestFmtDur(t *testing.T) {
	for d, want := range map[string]string{"72h": "72h", "36h30m": "36h30m", "90s": "1m30s", "2m": "2m", "30s": "30s"} {
		if got := fmtDur(mustDur(t, d)); got != want {
			t.Errorf("fmtDur(%s) = %s, want %s", d, got, want)
		}
	}
}

func mustDur(t *testing.T, s string) time.Duration {
	t.Helper()
	d, err := time.ParseDuration(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
