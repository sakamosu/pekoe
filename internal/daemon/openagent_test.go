package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pekoe/internal/state"
)

// open refuses an agent name that is not configured (a typo used to leave
// an idle session whose agent window died at once) and makes nothing.
func TestOpenRefusesUnknownAgent(t *testing.T) {
	d, _, repo := setup(t)
	_, err := d.Call("session.open", mustJSON(map[string]any{"path": repo, "resume": "new", "agent": "codx"}))
	if err == nil || !strings.Contains(err.Error(), "unknown agent: codx") {
		t.Fatalf("want unknown agent, got %v", err)
	}
	if ss, _ := d.DB.Sessions(); len(ss) != 0 {
		t.Fatalf("a session was made: %+v", ss[0])
	}
}

// open without --agent starts the repository's default agent (agent = in
// repos/<id>.toml), as new does.
func TestOpenUsesRepoAgent(t *testing.T) {
	d, fe, repo := setup(t)
	id := call[map[string]any](t, d, "init.detect", map[string]any{"path": repo})["repo_id"].(string)
	os.MkdirAll(filepath.Join(os.Getenv("PEKOE_HOME"), "config", "repos"), 0o755)
	os.WriteFile(filepath.Join(os.Getenv("PEKOE_HOME"), "config", "repos", id+".toml"), []byte("agent = \"codex\"\n"), 0o644)
	s := call[state.Session](t, d, "session.open", map[string]any{"path": repo, "resume": "new"})
	if s.Agent != "codex" || !strings.Contains(fe.last(), "spawn:agent:codex") {
		t.Fatalf("want codex, got %s / %s", s.Agent, fe.last())
	}
}
