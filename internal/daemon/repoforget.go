package daemon

// repo.forget: a person tells pekoe to stop knowing a repository
// (`pekoe repo forget`). knownRepos knows a repository by its sessions, by
// what the state DB remembers of it (repo:<id>) and by its per-repo config;
// forget takes away all three, so it leaves repo.list, the TUI's w and the
// repositories tapi chat is told about (and reads). It never touches the
// repository, its worktrees or its branches, and the stats history stays:
// a landed session's record goes the way hAgentRm takes it (purgeFiles
// writes what stats keeps first). Opening, initializing or starting a
// session in the repository again makes pekoe know it again.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"pekoe/internal/paths"
	"pekoe/internal/state"
)

type repoForgetP struct {
	Repo   string `json:"repo"`   // a path (relative to cwd), a repo-id or a short name
	Cwd    string `json:"cwd"`    // where a relative path is resolved
	Config bool   `json:"config"` // also delete ~/.config/pekoe/repos/<id>.toml
	DryRun bool   `json:"dry_run"`
}

// RepoForget is what repo.forget did (or, with dry_run, would do).
type RepoForget struct {
	ID         string   `json:"repo_id"`
	Name       string   `json:"name"`
	Dir        string   `json:"dir,omitempty"`
	Landed     []string `json:"landed,omitempty"`     // landed sessions whose records go with it
	Remembered bool     `json:"remembered,omitempty"` // the state DB remembered it
	ConfigFile string   `json:"config_file,omitempty"`
	ConfigGone bool     `json:"config_deleted,omitempty"`
	DryRun     bool     `json:"dry_run,omitempty"`
}

// ErrRepoInUse refuses repo.forget while a session that has not landed is
// left (the message names them).
var ErrRepoInUse = errors.New("the repository still has sessions")

func (d *Daemon) hRepoForget(p repoForgetP) (any, error) {
	r, err := d.resolveRepoRef(p.Repo, p.Cwd)
	if err != nil {
		return nil, err
	}
	res := &RepoForget{ID: r.ID, Name: r.Name, Dir: r.Dir, DryRun: p.DryRun}
	mine := func(s *state.Session) bool {
		return s.RepoID == r.ID || (r.Dir != "" && s.RepoDir == r.Dir)
	}
	var live []string
	d.mu.Lock()
	for slug, s := range d.sessions {
		if !mine(s) {
			continue
		}
		if s.State != state.Landed {
			live = append(live, fmt.Sprintf("%s (%s)", slug, s.State))
			continue
		}
		// a landed session whose worktree is still there (its removal
		// failed): deleting that directory is pekoe rm's, not forget's
		if s.Path != "" && s.Managed {
			if _, err := os.Stat(s.Path); err == nil {
				live = append(live, fmt.Sprintf("%s (landed, worktree %s still there)", slug, s.Path))
				continue
			}
		}
		res.Landed = append(res.Landed, slug)
	}
	d.mu.Unlock()
	if len(live) > 0 {
		sort.Strings(live)
		return nil, fmt.Errorf("%w: %s; remove them first (pekoe rm <slug>): %s", ErrRepoInUse, r.Name, strings.Join(live, ", "))
	}
	sort.Strings(res.Landed)
	keys := d.rememberedKeys(r)
	res.Remembered = len(keys) > 0
	cfg := paths.RepoConfig(r.ID)
	if _, err := os.Lstat(cfg); err == nil {
		res.ConfigFile = cfg
		if !p.Config {
			return nil, fmt.Errorf("%s has a config file you wrote, %s: pekoe keeps knowing the repository while it is there; pass --config to delete it too, or move it away yourself", r.Name, cfg)
		}
	}
	if !res.Remembered && len(res.Landed) == 0 && res.ConfigFile == "" {
		return nil, fmt.Errorf("pekoe does not know %s (%s): nothing to forget", r.Name, r.ID)
	}
	if p.DryRun {
		return res, nil
	}
	for _, slug := range res.Landed {
		var snap *state.Session
		if !d.forgetIf(slug, func(x *state.Session) bool {
			if !mine(x) || x.State != state.Landed {
				return false
			}
			snap = d.copyLocked(x)
			return true
		}) {
			continue // gone or replaced meanwhile: not ours to drop now
		}
		d.holdDevPort(snap)
		d.purgeFiles(snap) // records what stats keeps of it first
		d.removed(snap)
	}
	for _, k := range keys {
		if err := d.DB.Delete(k); err != nil {
			return nil, err
		}
	}
	if res.ConfigFile != "" {
		if err := os.Remove(res.ConfigFile); err != nil {
			return nil, err
		}
		res.ConfigGone = true
	}
	return res, nil
}

// rememberedKeys are the state DB keys that remember r: its own id, and an
// older id of the same directory realignRepo has not moved yet.
func (d *Daemon) rememberedKeys(r *KnownRepo) []string {
	var keys []string
	for _, kv := range d.DB.Prefixed(repoKVPrefix) {
		id := strings.TrimPrefix(kv[0], repoKVPrefix)
		var rr rememberedRepo
		if id == r.ID || (r.Dir != "" && json.Unmarshal([]byte(kv[1]), &rr) == nil && rr.Dir == r.Dir) {
			keys = append(keys, kv[0])
		}
	}
	return keys
}
