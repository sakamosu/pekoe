package daemon

// The local GUI's Worktrees view (docs/gui.md 「サイドバーのビュー」の
// Worktrees, docs/web.md の API の表): the worktrees of every repository
// pekoe knows, as the TUI's [2] Worktrees lists the current one's. A thin
// handler on the GUI's mux only: repo.list, then worktree.list for each
// repository whose directory pekoe knows, both as the person (the GUI's
// Caller). It reads only: nothing here removes a worktree or ends a
// session.

import "net/http"

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
// worktrees to list and is left out, as GET /api/repos leaves it out. The
// page names nothing: pekoed reads the directories it knows.
func (d *Daemon) guiWorktrees(w http.ResponseWriter, r *http.Request) {
	who := humanCaller(webVia(peerFrom(r)))
	res, err := d.CallAs(who, "repo.list", nil)
	if err != nil {
		webErr(w, err)
		return
	}
	ks, _ := res.([]KnownRepo)
	out := []guiWorktreeRepo{}
	for _, k := range ks {
		if k.Dir == "" {
			continue
		}
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
