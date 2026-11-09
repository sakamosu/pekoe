package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"pekoe/internal/tips"
)

func TestWebStateDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PEKOE_HOME", root)
	t.Setenv("HOME", filepath.Join(root, "home"))
	g, err := LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if g.Web.Enable || g.Web.Hostname != "pekoe" || g.Web.StateDir != filepath.Join(root, "share", "tsnet") {
		t.Fatalf("defaults: %+v", g.Web)
	}
	os.MkdirAll(filepath.Join(root, "config"), 0o755)
	os.WriteFile(filepath.Join(root, "config", "config.toml"), []byte("[web]\nstate_dir = \"~/ts\"\n"), 0o644)
	if g, err = LoadGlobal(); err != nil {
		t.Fatal(err)
	}
	if g.Web.StateDir != filepath.Join(root, "home", "ts") {
		t.Fatalf("~ not expanded: %s", g.Web.StateDir)
	}
}

// [tapi] agent names an [agents.*] entry; claude by default.
func TestTapiAgent(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PEKOE_HOME", root)
	g, err := LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if g.Tapi.Agent != "claude" {
		t.Fatalf("default: %q", g.Tapi.Agent)
	}
	os.MkdirAll(filepath.Join(root, "config"), 0o755)
	os.WriteFile(filepath.Join(root, "config", "config.toml"), []byte("[tapi]\nagent = \"claude-ask\"\n[agents.claude-ask]\ncommand = \"claude --permission-mode plan\"\n"), 0o644)
	if g, err = LoadGlobal(); err != nil {
		t.Fatal(err)
	}
	if g.Tapi.Agent != "claude-ask" || g.Agents["claude-ask"].Command != "claude --permission-mode plan" {
		t.Fatalf("configured: %q %+v", g.Tapi.Agent, g.Agents["claude-ask"])
	}
}

// Values put into a shell command line are passed as one literal word, and
// plain values stay as they were (configs that quote them keep working).
func TestExpandShell(t *testing.T) {
	for _, v := range []string{"fix$(echo pwned)", "x`id`", "a;b|c&d", "it's", "a b", "~", ""} {
		got, err := exec.Command("/bin/sh", "-c", Vars{Branch: v}.ExpandShell("printf '%s|' {{branch}}")).Output()
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != v+"|" {
			t.Errorf("branch %q came out as %q", v, got)
		}
	}
	v := Vars{Slug: "feat-x", Branch: "feat/x", Path: "/Users/me/worktrees/app/feat-x", Repo: "my.app", Port: 3000}
	in := `createdb app_{{slug}} && cd "{{path}}" && echo '{{branch}}' {{repo}} {{port}}`
	if got, want := v.ExpandShell(in), `createdb app_feat-x && cd "/Users/me/worktrees/app/feat-x" && echo 'feat/x' my.app 3000`; got != want {
		t.Errorf("plain values changed:\n got %s\nwant %s", got, want)
	}
	// [env] values are not for a shell: no quotes there
	if got := (Vars{Branch: "a b"}).Expand("{{branch}}"); got != "a b" {
		t.Errorf("Expand quoted: %q", got)
	}
}

// [tips] enable is on unless set to false.
func TestTipsEnable(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PEKOE_HOME", root)
	g, err := LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if !g.TipsOn() {
		t.Fatal("tips should be on by default")
	}
	os.MkdirAll(filepath.Join(root, "config"), 0o700)
	os.WriteFile(filepath.Join(root, "config", "config.toml"), []byte("[tips]\nenable = false\n"), 0o600)
	if g, err = LoadGlobal(); err != nil {
		t.Fatal(err)
	}
	if g.TipsOn() {
		t.Fatal("[tips] enable = false should turn tips off")
	}
}

// Every section of config.toml has a tip or a reason not to (internal/tips
// NoTip), and every section a tip names exists.
func TestTipsCoverSections(t *testing.T) {
	have := map[string]string{}
	for _, s := range Sections() {
		have["["+s+"]"] = "config.toml"
	}
	if have["[tips]"] == "" || have["[animate]"] == "" {
		t.Fatalf("sections: %v", Sections())
	}
	for _, e := range tips.Coverage(tips.KindConfig, have) {
		t.Error(e)
	}
}

// A config.toml from before the [notify] section was removed still loads:
// unknown sections are ignored, not an error.
func TestLegacyNotifyIgnored(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PEKOE_HOME", root)
	os.MkdirAll(filepath.Join(root, "config"), 0o755)
	os.WriteFile(filepath.Join(root, "config", "config.toml"), []byte("[notify]\nurl = \"https://example.invalid/topic\"\n"), 0o644)
	if _, err := LoadGlobal(); err != nil {
		t.Fatal(err)
	}
}
