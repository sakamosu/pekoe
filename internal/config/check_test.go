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
		`review.surface: "vim" is not one of nvim | tui (the built-in tui surface is used)`,
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

func TestRestartNeeded(t *testing.T) {
	run, _ := ParseGlobal([]byte("[http]\nport = 5000\n"))
	next, _ := ParseGlobal([]byte("[http]\nport = 5001\n[sandbox]\nenable = false\n[theme]\nmode = \"ansi\"\n[web]\nallow_users = [\"a@b\"]\n"))
	if got := strings.Join(RestartNeeded(run, next), ","); got != "http.port,web,sandbox" {
		t.Errorf("RestartNeeded = %s", got)
	}
	k := KeepRunning(run, next)
	if k.HTTP.Port != 5000 || !k.Sandbox.On() || len(k.Web.AllowUsers) != 0 || k.Theme.Mode != "ansi" {
		t.Errorf("KeepRunning: %+v", k)
	}
	same, _ := ParseGlobal([]byte("[http]\nport = 5000\n[cleanup]\nstale_after = \"1h\"\n"))
	if r := RestartNeeded(run, same); r != nil {
		t.Errorf("live settings need a restart: %v", r)
	}
}

// [notify], [mascot] sleep_after and [pr] poll_every: the default when
// unset or unreadable, the minimum below it; check names both cases
func TestDurations(t *testing.T) {
	g, err := ParseGlobal(nil)
	if err != nil {
		t.Fatal(err)
	}
	if g.Notify.Blink() != 30*time.Second || g.Notify.OS() != 2*time.Minute || g.Notify.Toast() != 5*time.Second || g.Mascot.Sleep() != 3*time.Minute || g.PR.Poll() != time.Minute {
		t.Fatalf("defaults: %v %v %v %v %v", g.Notify.Blink(), g.Notify.OS(), g.Notify.Toast(), g.Mascot.Sleep(), g.PR.Poll())
	}
	src := "[notify]\nblink_after = \"10s\"\nos_after = \"1s\"\ntoast_for = \"soon\"\n[mascot]\nsleep_after = \"10m\"\n[pr]\npoll_every = \"5s\"\n"
	if g, err = ParseGlobal([]byte(src)); err != nil {
		t.Fatal(err)
	}
	if g.Notify.Blink() != 10*time.Second || g.Notify.OS() != 10*time.Second || g.Notify.Toast() != 5*time.Second || g.Mascot.Sleep() != 10*time.Minute || g.PR.Poll() != 30*time.Second {
		t.Fatalf("set: %v %v %v %v %v", g.Notify.Blink(), g.Notify.OS(), g.Notify.Toast(), g.Mascot.Sleep(), g.PR.Poll())
	}
	got := problems(Check([]byte(src)))
	for _, want := range []string{
		"notify.os_after: 1s is below the minimum 10s (10s is used)",
		`notify.toast_for: "soon" is not a positive Go duration like 30s or 2m (5s is used)`,
		"pr.poll_every: 5s is below the minimum 30s (30s is used)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "blink_after") || strings.Contains(got, "sleep_after") {
		t.Errorf("good values flagged:\n%s", got)
	}
	b, _ := EncodeTOML(Effective(g))
	for _, want := range []string{`blink_after = "10s"`, `os_after = "10s"`, `toast_for = "5s"`, `sleep_after = "10m"`, `poll_every = "30s"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("show lacks %q", want)
		}
	}
}

// [web] confirm_tapi_send: unset is true, false reads as false, check knows
// the key, show spells it out, and a change applies without a restart.
func TestConfirmTapiSend(t *testing.T) {
	def, err := ParseGlobal(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !def.Web.ConfirmTapiSendOn() {
		t.Error("unset: want true")
	}
	src := "[web]\nconfirm_tapi_send = false\n"
	off, err := ParseGlobal([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if off.Web.ConfirmTapiSendOn() {
		t.Error("confirm_tapi_send = false: want false")
	}
	if ps := Check([]byte(src)); len(ps) != 0 {
		t.Errorf("check: %s", problems(ps))
	}
	b, _ := EncodeTOML(Effective(def))
	if !strings.Contains(string(b), "confirm_tapi_send = true") {
		t.Errorf("show: no confirm_tapi_send = true in\n%s", b)
	}
	if r := RestartNeeded(def, off); r != nil {
		t.Errorf("confirm_tapi_send needs a restart: %v", r)
	}
	if k := KeepRunning(def, off); k.Web.ConfirmTapiSendOn() {
		t.Error("KeepRunning did not take confirm_tapi_send up")
	}
	// the rest of [web] still waits for a restart, and keeps its value
	both, _ := ParseGlobal([]byte("[web]\nconfirm_tapi_send = false\nhostname = \"x\"\n"))
	if r := strings.Join(RestartNeeded(def, both), ","); r != "web" {
		t.Errorf("RestartNeeded = %s", r)
	}
	if k := KeepRunning(def, both); k.Web.Hostname == "x" || k.Web.ConfirmTapiSendOn() {
		t.Errorf("KeepRunning: %+v", k.Web)
	}
}

// [orchestrator] inbox_popup: unset is true, false reads as false, check
// knows the key, show spells it out, and it needs no restart.
func TestInboxPopup(t *testing.T) {
	def, err := ParseGlobal(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !def.Orchestrator.InboxPopupOn() {
		t.Error("unset: want true")
	}
	src := "[orchestrator]\ninbox_popup = false\n"
	off, err := ParseGlobal([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if off.Orchestrator.InboxPopupOn() {
		t.Error("inbox_popup = false: want false")
	}
	if ps := Check([]byte(src)); len(ps) != 0 {
		t.Errorf("check: %s", problems(ps))
	}
	b, _ := EncodeTOML(Effective(def))
	if !strings.Contains(string(b), "inbox_popup = true") {
		t.Errorf("show: no inbox_popup = true in\n%s", b)
	}
	if r := RestartNeeded(def, off); r != nil {
		t.Errorf("inbox_popup needs a restart: %v", r)
	}
}
