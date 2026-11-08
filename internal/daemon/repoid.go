package daemon

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"

	"pekoe/internal/gitx"
	"pekoe/internal/paths"
	"pekoe/internal/state"
)

// A repository's id (gitx.RepoID) is its origin URL, or its path when it has
// none, so the same directory changes id when an origin is added after its
// sessions were created (or was recorded before origins were looked at).
// Sessions keep the id they were created with, and a stale one splits the
// repository in two: `pekoe ls` in the repository leaves them out and the
// TUI shows two headings. realignRepoIDs (on start) and resolved (whenever
// a repository is resolved from a path) bring them to the current id.

// resolved records a repository just resolved from a path (resolveRepo) and
// re-files sessions of the same directory still under another id.
func (d *Daemon) resolved(r *repoInfo) {
	d.remember(r)
	d.realignRepo(r)
}

// realignRepoIDs re-derives the id of every repository that has a session
// or is remembered, from its main worktree directory.
func (d *Daemon) realignRepoIDs() {
	dirs := map[string]bool{}
	d.mu.Lock()
	for _, s := range d.sessions {
		if s.RepoDir != "" && s.RepoID != "" {
			dirs[s.RepoDir] = true
		}
	}
	d.mu.Unlock()
	for _, kv := range d.DB.Prefixed(repoKVPrefix) {
		var rr rememberedRepo
		if json.Unmarshal([]byte(kv[1]), &rr) == nil && rr.Dir != "" {
			dirs[rr.Dir] = true
		}
	}
	for dir := range dirs {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			continue // gone: nothing to derive the id from
		}
		id, err := gitx.RepoID(dir)
		if err != nil {
			continue
		}
		d.realignRepo(&repoInfo{Dir: dir, ID: id, Name: gitx.RepoName(dir)})
	}
}

// realignRepo moves what is filed under an old id of r.Dir to r.ID:
//   - sessions (RepoID, Repo), saved;
//   - their ports (the ports table is keyed by repo_id+slug; the number is
//     kept, so dev servers and PEKOE_PORT stay the same);
//   - their picks, and the repository's other picks when no session is left
//     on the old id (another clone with the same origin may still use it);
//   - their preview directory (<data>/<id>/preview/<slug>: remembered path);
//   - the remembered repository (kv repo:<id>), the step durations of
//     session creation (kv dur:<id>:<step>) and the per-repo config
//     ~/.config/pekoe/repos/<id>.toml when the new id has none.
//
// Left where they are, by design: picks' .md/.png files and error
// attachments (<data>/<old>/picks, errors) — they were handed to agents and
// are referred to by absolute path, so moving them would break those
// references (rm then leaves the old attachments behind, which is harmless);
// test logs (<data>/<old>/logs) are history only.
func (d *Daemon) realignRepo(r *repoInfo) {
	if r == nil || r.ID == "" || r.Dir == "" {
		return
	}
	type stale struct{ slug, old string }
	var moved []stale
	d.mu.Lock()
	for _, s := range d.sessions {
		if s.RepoDir == r.Dir && s.RepoID != "" && s.RepoID != r.ID {
			moved = append(moved, stale{s.Slug, s.RepoID})
		}
	}
	d.mu.Unlock()
	olds := map[string][]string{} // old id -> slugs moved off it
	for _, m := range moved {
		if err := d.DB.MovePort(m.old, r.ID, m.slug); err != nil {
			log.Printf("realign %s: port: %v", m.slug, err)
		}
		moveDir(paths.PreviewDir(m.old, m.slug), paths.PreviewDir(r.ID, m.slug))
		d.update(m.slug, func(x *state.Session) {
			if x.RepoID == m.old {
				x.RepoID, x.Repo = r.ID, r.Name
			}
		})
		olds[m.old] = append(olds[m.old], m.slug)
		log.Printf("realign %s: repo id %s -> %s", m.slug, m.old, r.ID)
	}
	for _, kv := range d.DB.Prefixed(repoKVPrefix) {
		id := strings.TrimPrefix(kv[0], repoKVPrefix)
		var rr rememberedRepo
		if id == r.ID || json.Unmarshal([]byte(kv[1]), &rr) != nil || rr.Dir != r.Dir {
			continue
		}
		_ = d.DB.Delete(kv[0])
		if _, ok := olds[id]; !ok {
			olds[id] = nil
		}
	}
	if len(olds) == 0 {
		return
	}
	for old, slugs := range olds {
		left := false
		d.mu.Lock()
		for _, s := range d.sessions {
			left = left || s.RepoID == old
		}
		d.mu.Unlock()
		if err := d.DB.MovePicks(old, r.ID, slugs, !left); err != nil {
			log.Printf("realign %s: picks: %v", old, err)
		}
		if !left {
			if _, err := os.Stat(paths.RepoConfig(r.ID)); os.IsNotExist(err) {
				moveDir(paths.RepoConfig(old), paths.RepoConfig(r.ID))
			}
			// step durations of session creation (gauges): the new id's own win
			for _, kv := range d.DB.Prefixed("dur:" + old + ":") {
				step := strings.TrimPrefix(kv[0], "dur:"+old+":")
				if d.DB.Get("dur:"+r.ID+":"+step) == "" {
					_ = d.DB.Set("dur:"+r.ID+":"+step, kv[1])
				}
				_ = d.DB.Delete(kv[0])
			}
		}
	}
	d.remember(r)
}

// moveDir renames src to dst when src exists and dst does not.
func moveDir(src, dst string) {
	if _, err := os.Stat(src); err != nil {
		return
	}
	if _, err := os.Stat(dst); err == nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return
	}
	if err := os.Rename(src, dst); err != nil {
		log.Printf("move %s -> %s: %v", src, dst, err)
	}
}
