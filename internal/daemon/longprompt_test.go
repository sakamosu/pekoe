package daemon

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"pekoe/internal/state"
)

// A long initial prompt does not go on tmux's command line (tmux refused
// it: "command too long"); the agent's shell reads it from a file and the
// agent still gets exactly the prompt as its argument.
func TestLongPromptGoesThroughAFile(t *testing.T) {
	d, fe, repo := setup(t)
	prompt := "-starts with a dash, 'quotes' \"double\" $HOME `tick` \\ back\n" + strings.Repeat("長い指示です。", 1000)
	call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new", "prompt": prompt})
	spawn := fe.last()
	if len(spawn) > 2000 {
		t.Fatalf("the prompt is on the command line (%d bytes)", len(spawn))
	}
	cmd := strings.TrimPrefix(spawn, "spawn:agent:")
	cmd = cmd[:strings.Index(cmd, "; printf '\\n[pekoe] agent exited")]
	i := strings.Index(cmd, " -- ")
	if i < 0 {
		t.Fatalf("no prompt argument: %s", cmd)
	}
	arg := cmd[i+4:]
	// the file is private while it waits
	f := strings.Trim(strings.Fields(strings.TrimPrefix(arg, `"$(cat `))[0], "';")
	if st, err := os.Stat(f); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("prompt file %s: %v %v", f, err, st)
	}
	out, err := exec.Command("/bin/sh", "-c", "printf %s "+arg).Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != strings.TrimRight(prompt, "\n") {
		t.Fatalf("the agent would get a different prompt:\n%q", string(out)[:200])
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatal("the prompt file is left after it was read")
	}
}

// A short prompt stays on the command line.
func TestShortPromptStaysAnArgument(t *testing.T) {
	d, fe, repo := setup(t)
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new", "prompt": "fix it"})
	if !strings.Contains(fe.last(), "claude"+settingsFlag(s.Slug)+" -- 'fix it'") {
		t.Fatalf("spawn: %s", fe.last())
	}
}
