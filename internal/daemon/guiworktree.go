package daemon

// The local GUI's Worktrees view (docs/gui.md 「サイドバーのビュー」の
// Worktrees, docs/web.md の API の表): the worktrees of every repository
// pekoe knows, as the TUI's [2] Worktrees lists the current one's. A thin
// handler on the GUI's mux only: repo.list, then worktree.list for each
// repository whose directory pekoe knows, both as the person (the GUI's
// Caller). It reads only: nothing here removes a worktree or ends a
// session.

import (
	"net/http"

	"pekoe/internal/gitx"
)

// guiWorktreeRepo is one repository and its worktrees (worktree.list's
// rows: the main checkout first, then git's order; a row with a session
// carries its slug, port and state).
type guiWorktreeRepo struct {
	ID        string         `json:"repo_id"`
	Name      string         `json:"name"`
	Dir       string         `json:"dir"`
	Worktrees []WorktreeView `json:"worktrees"`
}

// guiWorktrees is GET /api/worktrees: {repos: [...]} in repo.list's order.
// A repository pekoe knows only by its id (no directory found) has no
// worktrees to list and is left out, as GET /api/repos leaves it out, and
// a directory known under two ids is listed once (guiRepoList). The page
// names nothing: pekoed reads the directories it knows.
func (d *Daemon) guiWorktrees(w http.ResponseWriter, r *http.Request) {
	who := humanCaller(webVia(peerFrom(r)))
	ks, err := d.guiRepoList(who)
	if err != nil {
		webErr(w, err)
		return
	}
	out := []guiWorktreeRepo{}
	for _, k := range ks {
		res, err := d.CallAs(who, "worktree.list", mustJSON(wtListP{Repo: k.Dir}))
		if err != nil {
			webErr(w, err)
			return
		}
		ws, _ := res.([]WorktreeView)
		if ws == nil {
			ws = []WorktreeView{}
		}
		out = append(out, guiWorktreeRepo{ID: k.ID, Name: k.Name, Dir: k.Dir, Worktrees: ws})
	}
	writeJSON(w, map[string]any{"repos": out})
}

// guiRepoList is repo.list as the GUI shows it (GET /api/worktrees and
// /api/repos): the repositories whose directory pekoe knows, each
// directory once, in repo.list's order. One directory can be known under
// two ids (its origin changed, and a landed session keeps the old one
// until realignRepo files it under the new, or names the directory by
// another path to it): the id git gives the directory now wins, else the
// first; the sessions of the others are counted with it.
func (d *Daemon) guiRepoList(who *Caller) ([]KnownRepo, error) {
	res, err := d.CallAs(who, "repo.list", nil)
	if err != nil {
		return nil, err
	}
	ks, _ := res.([]KnownRepo)
	var dirs []string
	same := map[string][]int{} // realpath -> indexes into ks
	for i, k := range ks {
		if k.Dir == "" {
			continue
		}
		rp := realpath(k.Dir)
		if same[rp] == nil {
			dirs = append(dirs, rp)
		}
		same[rp] = append(same[rp], i)
	}
	out := make([]KnownRepo, 0, len(dirs))
	for _, rp := range dirs {
		is := same[rp]
		pick := is[0]
		if len(is) > 1 {
			for _, i := range is {
				// by the path the entry has (a path id is made from it)
				if id, err := gitx.RepoID(ks[i].Dir); err == nil && id == ks[i].ID {
					pick = i
					break
				}
			}
		}
		k := ks[pick]
		for _, i := range is {
			if i != pick {
				k.Sessions += ks[i].Sessions
			}
		}
		out = append(out, k)
	}
	return out, nil
}
