package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"pekoe/internal/gitx"
	"pekoe/internal/paths"
	"pekoe/internal/state"
)

// KnownRepo is a repository pekoed knows about: one that has a session, one
// pekoe resolved before (remembered in the state DB), or one with
// ~/.config/pekoe/repos/<repo-id>.toml.
type KnownRepo struct {
	ID       string `json:"repo_id"`
	Name     string `json:"name"`          // short name (~/worktrees/<name>)
	Dir      string `json:"dir,omitempty"` // main worktree; empty when only the config is known and it was not found
	Sessions int    `json:"sessions"`      // sessions that are not landed
	Config   bool   `json:"config"`        // has a per-repo config file
	Cwd      bool   `json:"cwd,omitempty"` // repo.list: the cwd passed is inside it
}

const repoKVPrefix = "repo:"

type rememberedRepo struct {
	Dir  string `json:"dir"`
	Name string `json:"name"`
}

// remember records a resolved repository, so knownRepos still knows where
// it is after its last session is gone (the TUI offers it with w).
func (d *Daemon) remember(r *repoInfo) {
	if r == nil || r.ID == "" || r.Dir == "" {
		return
	}
	key := repoKVPrefix + r.ID
	b, _ := json.Marshal(rememberedRepo{Dir: r.Dir, Name: r.Name})
	if d.DB.Get(key) == string(b) {
		return
	}
	_ = d.DB.Set(key, string(b))
}

// knownRepos lists the known repositories ordered by name, then id.
func (d *Daemon) knownRepos() []KnownRepo {
	by := map[string]*KnownRepo{}
	newest := map[string]time.Time{}
	d.mu.Lock()
	for _, s := range d.sessions {
		if s.RepoID == "" {
			continue
		}
		r := by[s.RepoID]
		if r == nil {
			r = &KnownRepo{ID: s.RepoID}
			by[s.RepoID] = r
		}
		if s.State != state.Landed {
			r.Sessions++
		}
		// the newest session wins: a repository may have moved
		if s.CreatedAt.After(newest[s.RepoID]) || r.Name == "" {
			newest[s.RepoID] = s.CreatedAt
			if s.Repo != "" {
				r.Name = s.Repo
			}
			if s.RepoDir != "" {
				r.Dir = s.RepoDir
			}
		}
	}
	d.mu.Unlock()
	for _, kv := range d.DB.Prefixed(repoKVPrefix) {
		var rr rememberedRepo
		if json.Unmarshal([]byte(kv[1]), &rr) != nil {
			continue
		}
		if fi, err := os.Stat(rr.Dir); err != nil || !fi.IsDir() {
			continue // moved or deleted since
		}
		id := strings.TrimPrefix(kv[0], repoKVPrefix)
		r := by[id]
		if r == nil {
			r = &KnownRepo{ID: id}
			by[id] = r
		}
		if r.Dir == "" { // a session's directory wins
			r.Dir = rr.Dir
		}
		if r.Name == "" {
			r.Name = rr.Name
		}
	}
	if es, err := os.ReadDir(paths.ReposConfigDir()); err == nil {
		for _, e := range es {
			id, ok := strings.CutSuffix(e.Name(), ".toml")
			if !ok || e.IsDir() || id == "" {
				continue
			}
			r := by[id]
			if r == nil {
				r = &KnownRepo{ID: id}
				by[id] = r
			}
			r.Config = true
		}
	}
	// a repo-id alone says nothing about where the repository lives
	var unlocated []string
	for id, r := range by {
		if r.Dir == "" {
			unlocated = append(unlocated, id)
		}
	}
	for id, dir := range d.locateRepos(unlocated) {
		ri := &repoInfo{ID: id, Dir: dir, Name: gitx.RepoName(dir)}
		d.remember(ri)
		by[id].Dir = dir
		if by[id].Name == "" {
			by[id].Name = ri.Name
		}
	}
	out := make([]KnownRepo, 0, len(by))
	for _, r := range by {
		if r.Name == "" {
			r.Name = nameFromID(r.ID)
		}
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// nameFromID guesses the short name of a repository known only by its id
// (gitx.RepoID): host_owner_name → name, path_Users_me_app → app. Best
// effort: a path with "_" in its last element comes out short.
func nameFromID(id string) string {
	if rest, ok := strings.CutPrefix(id, "path_"); ok {
		return rest[strings.LastIndex(rest, "_")+1:]
	}
	if parts := strings.SplitN(id, "_", 3); len(parts) == 3 {
		return parts[2]
	}
	return id
}

// looksLikePath: a repo-id or a short name never contains "/" (gitx.RepoID
// replaces it), so anything with one, or starting with . or ~, is a path.
func looksLikePath(q string) bool {
	return strings.Contains(q, "/") || strings.HasPrefix(q, ".") || strings.HasPrefix(q, "~")
}

func absPath(q, cwd string) string {
	if q == "~" || strings.HasPrefix(q, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			q = filepath.Join(h, strings.TrimPrefix(q, "~"))
		}
	}
	if !filepath.IsAbs(q) && cwd != "" {
		q = filepath.Join(cwd, q)
	}
	return filepath.Clean(q)
}

// matchRepos returns the known repositories whose id or short name is q:
// an id match alone when there is one, else every name match.
func matchRepos(known []KnownRepo, q string) []KnownRepo {
	for _, r := range known {
		if r.ID == q {
			return []KnownRepo{r}
		}
	}
	var out []KnownRepo
	for _, r := range known {
		if strings.EqualFold(r.Name, q) {
			out = append(out, r)
		}
	}
	return out
}

func ambiguous(q string, rs []KnownRepo) error {
	lines := make([]string, len(rs))
	for i, r := range rs {
		where := r.Dir
		if where == "" {
			where = "config only"
		}
		lines[i] = fmt.Sprintf("  %s  (%s)", r.ID, where)
	}
	return fmt.Errorf("repo %q matches %d repositories; pass the repo-id or a path:\n%s", q, len(rs), strings.Join(lines, "\n"))
}

// resolveRepoRef is the one place a --repo value becomes a repository: a
// path (relative to cwd), a repo-id, or a short name. A short name shared
// by several repositories is an error that lists them. The repository of a
// path does not have to be known yet.
func (d *Daemon) resolveRepoRef(q, cwd string) (*KnownRepo, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, errors.New("repo required")
	}
	fromPath := func(p string) (*KnownRepo, error) {
		ri, err := resolveRepo(p)
		if err != nil {
			return nil, err
		}
		r := &KnownRepo{ID: ri.ID, Name: ri.Name, Dir: ri.Dir}
		for _, k := range d.knownRepos() {
			if k.ID == ri.ID {
				r.Sessions, r.Config = k.Sessions, k.Config
			}
		}
		return r, nil
	}
	if looksLikePath(q) {
		return fromPath(absPath(q, cwd))
	}
	known := d.knownRepos()
	switch ms := matchRepos(known, q); {
	case len(ms) == 1:
		return &ms[0], nil
	case len(ms) > 1:
		return nil, ambiguous(q, ms)
	}
	// a bare directory name relative to cwd ("pekoe --repo app" next to app/)
	if cwd != "" {
		if fi, err := os.Stat(filepath.Join(cwd, q)); err == nil && fi.IsDir() {
			return fromPath(filepath.Join(cwd, q))
		}
	}
	var names []string
	for _, r := range known { // sorted by name: drop repeats
		if len(names) == 0 || names[len(names)-1] != r.Name {
			names = append(names, r.Name)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("unknown repo: %s (no known repositories; pass a path)", q)
	}
	return nil, fmt.Errorf("unknown repo: %s (known: %s; or pass a path)", q, strings.Join(names, ", "))
}

// resolveSessionRef turns "<repo>/<slug>" into a slug. <repo> is a repo-id
// or a short name; <slug> is the session's slug, or the slug without the
// "<name>-" that a collision with another repository put in front of it
// (app/feat-x finds app-feat-x). A plain slug is returned unchanged.
func (d *Daemon) resolveSessionRef(ref string) (string, error) {
	q, slug, ok := strings.Cut(ref, "/")
	if !ok {
		return ref, nil
	}
	if q == "" || slug == "" || strings.Contains(slug, "/") {
		return "", fmt.Errorf("bad session %q: use <slug> or <repo>/<slug>", ref)
	}
	repos := matchRepos(d.knownRepos(), q)
	if len(repos) == 0 {
		return "", fmt.Errorf("no such session: %s (unknown repo %s)", ref, q)
	}
	ids := map[string]string{} // repo id -> slug of its name (the prefix before "-")
	for _, r := range repos {
		ids[r.ID] = paths.Slug(r.Name)
	}
	// per repository the exact slug wins over the prefixed one
	exact, prefixed := map[string]string{}, map[string]string{}
	d.mu.Lock()
	for k, s := range d.sessions {
		pre, ok := ids[s.RepoID]
		switch {
		case !ok:
		case k == slug:
			exact[s.RepoID] = k
		case strings.HasSuffix(k, "-"+slug) && paths.Slug(strings.TrimSuffix(k, "-"+slug)) == pre:
			// also a slug made before '.' was dropped (my.app-feat)
			prefixed[s.RepoID] = k
		}
	}
	d.mu.Unlock()
	var hits []string
	for id := range ids {
		if k, ok := exact[id]; ok {
			hits = append(hits, k)
		} else if k, ok := prefixed[id]; ok {
			hits = append(hits, k)
		}
	}
	switch len(hits) {
	case 0:
		return "", fmt.Errorf("no such session: %s", ref)
	case 1:
		return hits[0], nil
	}
	sort.Strings(hits)
	return "", fmt.Errorf("session %s is ambiguous (%s); use the slug or <repo-id>/<slug>", ref, strings.Join(hits, ", "))
}

// normalizeSlugParam rewrites a "<repo>/<slug>" slug param to the slug, so
// every method that takes {slug} also takes the qualified form.
func (d *Daemon) normalizeSlugParam(params json.RawMessage) (json.RawMessage, error) {
	if !strings.Contains(string(params), `"slug"`) {
		return params, nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(params, &m) != nil {
		return params, nil
	}
	var slug string
	if json.Unmarshal(m["slug"], &slug) != nil || !strings.Contains(slug, "/") {
		return params, nil
	}
	s, err := d.resolveSessionRef(slug)
	if err != nil {
		return nil, err
	}
	m["slug"], _ = json.Marshal(s)
	return json.Marshal(m)
}

type repoResolveP struct {
	Repo string `json:"repo"`
	Cwd  string `json:"cwd"`
}

type repoListP struct {
	Cwd string `json:"cwd"`
}

// hRepoList lists the known repositories. With cwd, the repository it is in
// is remembered (so it is listed) and marked cwd.
func (d *Daemon) hRepoList(p repoListP) (any, error) {
	cwdID := ""
	if p.Cwd != "" {
		if ri, err := resolveRepo(p.Cwd); err == nil {
			d.resolved(ri)
			cwdID = ri.ID
		}
	}
	rs := d.knownRepos()
	for i := range rs {
		rs[i].Cwd = cwdID != "" && rs[i].ID == cwdID
	}
	return rs, nil
}

// locateMissTTL: how long a repo-id that could not be located is not
// searched for again (knownRepos runs on every --repo and <repo>/<slug>).
const locateMissTTL = time.Minute

// locateRepos finds where repositories known only by their id live: a path_
// id spells its path (underscores are ambiguous, so the guess is checked),
// any other id is matched against the worktrees pekoe created under
// ~/worktrees/<name>.
func (d *Daemon) locateRepos(ids []string) map[string]string {
	found := map[string]string{}
	want := map[string]bool{}
	d.mu.Lock()
	for _, id := range ids {
		if t, ok := d.locateMiss[id]; !ok || time.Since(t) > locateMissTTL {
			want[id] = true
		}
	}
	d.mu.Unlock()
	if len(want) == 0 {
		return found
	}
	for id := range want {
		if rest, ok := strings.CutPrefix(id, "path_"); ok {
			if dir := pathFromID(rest); dir != "" {
				if got, err := gitx.RepoID(dir); err == nil && got == id {
					found[id] = dir
					delete(want, id)
				}
			}
		}
	}
	root := paths.WorktreesRoot()
	groups, _ := os.ReadDir(root)
	for _, g := range groups {
		if len(want) == 0 {
			break
		}
		if !g.IsDir() {
			continue
		}
		wts, _ := os.ReadDir(filepath.Join(root, g.Name()))
		for _, wt := range wts {
			if !wt.IsDir() {
				continue
			}
			main, err := gitx.MainWorktree(filepath.Join(root, g.Name(), wt.Name()))
			if err != nil {
				continue
			}
			if id, err := gitx.RepoID(main); err == nil && want[id] {
				found[id] = main
				delete(want, id)
			}
			break // every worktree of a group shares its repository
		}
	}
	d.mu.Lock()
	if d.locateMiss == nil {
		d.locateMiss = map[string]time.Time{}
	}
	for id := range want {
		d.locateMiss[id] = time.Now()
	}
	d.mu.Unlock()
	return found
}

// pathFromID turns "Users_me_my_app" back into an existing directory: each
// underscore is either a separator or part of a name, so the choices are
// checked against the filesystem, one directory level at a time.
func pathFromID(rest string) string {
	parts := strings.Split(rest, "_")
	var walk func(dir string, i int) string
	walk = func(dir string, i int) string {
		for j := i; j < len(parts); j++ {
			p := filepath.Join(dir, strings.Join(parts[i:j+1], "_"))
			fi, err := os.Stat(p)
			if err != nil || !fi.IsDir() {
				continue
			}
			if j == len(parts)-1 {
				return p
			}
			if got := walk(p, j+1); got != "" {
				return got
			}
		}
		return ""
	}
	return walk("/", 0)
}

func (d *Daemon) hRepoResolve(p repoResolveP) (any, error) { return d.resolveRepoRef(p.Repo, p.Cwd) }
