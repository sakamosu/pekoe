package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pekoe/internal/engine"
	"pekoe/internal/paths"
	"pekoe/internal/state"
	"pekoe/internal/testenv"
)

// review.surface must not take a runtime path from the caller: nvim reads
// "|" in -c as a command separator, so "/x | !cmd" ran cmd outside the
// agent's sandbox. The plugin directory is pekoed's own (PEKOE_NVIM_RTP or
// next to the binary), and it reaches nvim through the environment, never
// spliced into an Ex command.
func TestReviewSurfaceIgnoresCallerRTP(t *testing.T) {
	d, fe, repo := setup(t)
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "nvim"), []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	plug := filepath.Join(t.TempDir(), "odd | dir")
	t.Setenv("PEKOE_NVIM_RTP", plug)
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	call[any](t, d, "review.surface", map[string]any{"slug": s.Slug, "rtp": "/x | !touch /tmp/pwned", "width": 80, "height": 24})
	sp := fe.spawned(s.Slug, engine.WinReview)
	if sp.Command == "" {
		t.Fatalf("nvim not spawned: %s", fe.last())
	}
	for _, bad := range []string{"touch", "/x", "odd", "|"} {
		if strings.Contains(sp.Command, bad) {
			t.Errorf("command carries %q: %s", bad, sp.Command)
		}
	}
	found := false
	for _, e := range sp.Env {
		if e == "PEKOE_NVIM_PLUGIN="+plug {
			found = true
		}
	}
	if !found {
		t.Errorf("plugin dir not in env: %v", sp.Env)
	}
}

// A prompt that starts with "-" must reach the agent as its prompt, not as
// options (--permission-mode=bypassPermissions, --settings={hooks…}).
func TestPromptIsNotAnOption(t *testing.T) {
	d, fe, repo := setup(t)
	evil := "--permission-mode=bypassPermissions"
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new", "prompt": evil})
	got := fe.spawned(s.Slug, engine.WinAgent).Command
	if !strings.HasPrefix(got, "claude"+settingsFlag(s.Slug)+" -- '"+evil+"'") {
		t.Fatalf("prompt not after --: %s", got)
	}
}

func TestPromptAfterResumeIsNotAnOption(t *testing.T) {
	d, fe, repo := setup(t)
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	// resume templates and --continue get the prompt after "--" as well
	ag := d.Global.Agents["claude"]
	ag.Resume = "claude --resume {{session_id}}"
	d.Global.Agents["claude"] = ag
	if err := d.spawnAgent(s.Slug, "-x", "continue", 0, 0); err != nil {
		t.Fatal(err)
	}
	if got := fe.spawned(s.Slug, engine.WinAgent).Command; !strings.HasPrefix(got, "claude --continue"+settingsFlag(s.Slug)+" -- '-x'") {
		t.Fatalf("continue: %s", got)
	}
}

// The review surface's nvim listens in pekoe's data directory (NvimDir,
// where the hooks sandbox cannot connect), not in $TMPDIR; a socket left
// by an nvim that is gone does not stop the next one.
func TestReviewSurfaceNvimSocket(t *testing.T) {
	d, fe, repo := setup(t)
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "nvim"), []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	// a short data directory: socket paths are limited (~104 bytes)
	t.Setenv("PEKOE_HOME", testenv.ShortDir(t, "pk"))
	sock := nvimSocket(s.Slug)
	if sock == "" || filepath.Dir(sock) != paths.NvimDir() {
		t.Fatalf("socket %q not in %s", sock, paths.NvimDir())
	}
	if fi, err := os.Stat(paths.NvimDir()); err != nil || fi.Mode().Perm() != paths.DirPerm {
		t.Fatalf("NvimDir: %v %v", fi, err)
	}
	if nvimSocket("other") == sock {
		t.Fatal("two sessions share a socket")
	}
	call[any](t, d, "review.surface", map[string]any{"slug": s.Slug, "width": 80, "height": 24})
	sp := fe.spawned(s.Slug, engine.WinReview)
	if !strings.HasPrefix(sp.Command, "nvim --listen "+shellQuote(sock)) {
		t.Fatalf("command: %s", sp.Command)
	}
	// too long a path: nvim picks its own, as before
	t.Setenv("PEKOE_HOME", filepath.Join(t.TempDir(), strings.Repeat("x", 100)))
	if got := nvimSocket(s.Slug); got != "" {
		t.Fatalf("over-long socket path used: %s", got)
	}
}
