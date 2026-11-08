// Package migrate moves a boba installation (pekoe's old name) to pekoe's
// names, once: `pekoe migrate`. It is the only code that knows the old
// names on purpose; nothing else reads them.
//
//	~/.config/boba            → ~/.config/pekoe
//	~/.local/share/boba       → ~/.local/share/pekoe (bobad.* → pekoed.*)
//	state.db                  old paths and refs/boba/ → new ones,
//	                          sessions' env BOBA_* → PEKOE_*
//	refs/boba/* in each repo  → copied to refs/pekoe/* (--prune-old deletes the old)
//	~/.claude/settings.json   `boba event` hooks → `pekoe event`
package migrate

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite"

	"pekoe/internal/gitx"
	"pekoe/internal/hooks"
	"pekoe/internal/paths"
)

const (
	oldName   = "boba"
	oldDaemon = "bobad"
	newDaemon = "pekoed"
	oldRefs   = "refs/boba/"
	newRefs   = "refs/pekoe/"
)

// Layout is where the old and the new installation live.
type Layout struct {
	OldConfig, OldData string
	NewConfig, NewData string
	// Sockets and Locks are checked for a running daemon (old and new).
	Sockets, Locks []string
}

// DefaultLayout: the old directories as boba resolved them (XDG or ~), the
// new ones as pekoe resolves them now ($PEKOE_HOME included).
func DefaultLayout() Layout {
	home, _ := os.UserHomeDir()
	oldConfig := filepath.Join(home, ".config", oldName)
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		oldConfig = filepath.Join(x, oldName)
	}
	oldData := filepath.Join(home, ".local", "share", oldName)
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		oldData = filepath.Join(x, oldName)
	}
	l := Layout{
		OldConfig: oldConfig, OldData: oldData,
		NewConfig: paths.ConfigDir(), NewData: paths.DataDir(),
		Sockets: []string{filepath.Join(oldData, oldDaemon+".sock"), paths.Socket()},
		Locks:   []string{filepath.Join(oldData, oldDaemon+".lock"), paths.LockFile()},
	}
	if s := os.Getenv("BOBA_SOCK"); s != "" {
		l.Sockets = append(l.Sockets, s)
	}
	return l
}

type Options struct {
	DryRun   bool
	PruneOld bool   // delete refs/boba/* after copying them
	Bin      string // pekoe executable, for the hooks
	Out      io.Writer
}

// installLinks are what `make install` puts in the data directory
// (PREFIX/share/pekoe): a destination holding only these is not "in use".
var installLinks = map[string]bool{"nvim": true, "extension": true}

type runner struct {
	l   Layout
	o   Options
	out io.Writer
}

func (r *runner) step(format string, a ...any) {
	prefix := "  "
	if r.o.DryRun {
		prefix = "  (dry-run) "
	}
	fmt.Fprintf(r.out, prefix+format+"\n", a...)
}

// Run migrates. Nothing is changed when a daemon is running or a
// destination directory is already in use; with DryRun nothing is changed
// at all. Running it again after a migration is harmless: with nothing to
// move it still rewrites the new state.db (old env names left by an
// earlier pekoe migrate included).
func Run(l Layout, o Options) error {
	r := &runner{l: l, o: o, out: o.Out}
	if r.out == nil {
		r.out = os.Stdout
	}
	if s := runningDaemon(l); s != "" {
		return fmt.Errorf("a daemon is running (%s) · stop it first: boba daemon stop / pekoe daemon stop (agents keep running in tmux)", s)
	}
	moves := []struct{ name, from, to string }{
		{"config", l.OldConfig, l.NewConfig},
		{"data", l.OldData, l.NewData},
	}
	for _, m := range moves {
		if err := checkMove(m.from, m.to); err != nil {
			return err
		}
	}
	if o.DryRun {
		fmt.Fprintln(r.out, "pekoe migrate --dry-run: nothing is changed")
	} else {
		fmt.Fprintln(r.out, "pekoe migrate")
	}

	for _, m := range moves {
		if err := r.move(m.from, m.to); err != nil {
			return err
		}
	}
	dataDir := l.NewData
	if o.DryRun && exists(l.OldData) {
		dataDir = l.OldData
	}
	if err := r.renameDaemonFiles(dataDir); err != nil {
		return err
	}
	db := filepath.Join(dataDir, "state.db")
	repos, err := r.rewriteDB(db)
	if err != nil {
		return err
	}
	r.copyRefs(repos)
	if err := r.hooks(); err != nil {
		return err
	}
	configDir := l.NewConfig
	if o.DryRun && exists(l.OldConfig) {
		configDir = l.OldConfig
	}
	r.scanConfig(configDir)
	r.todo()
	return nil
}

// ---- daemon ----

func runningDaemon(l Layout) string {
	for _, s := range l.Sockets {
		if c, err := net.DialTimeout("unix", s, 300*time.Millisecond); err == nil {
			c.Close()
			return s
		}
	}
	for _, p := range l.Locks {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		}
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return p + " is locked"
		}
	}
	return ""
}

// ---- directories ----

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// checkMove fails when both from and to exist and to holds anything but
// what `make install` links there (or a link of the same name in from is
// not a link).
func checkMove(from, to string) error {
	if !exists(from) || !exists(to) {
		return nil
	}
	ents, err := os.ReadDir(to)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if !installLinks[e.Name()] || e.Type()&os.ModeSymlink == 0 {
			return fmt.Errorf("both %s and %s exist · %s is already in use (%s); move or remove one of them first", from, to, to, e.Name())
		}
		if fi, err := os.Lstat(filepath.Join(from, e.Name())); err == nil && fi.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("%s exists in both %s and %s · move or remove one of them first", e.Name(), from, to)
		}
	}
	return nil
}

func (r *runner) move(from, to string) error {
	if !exists(from) {
		return nil
	}
	if !exists(to) {
		r.step("move %s → %s", from, to)
		if r.o.DryRun {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		return os.Rename(from, to)
	}
	// to holds only `make install` links: move the rest in next to them
	r.step("move the contents of %s → %s (keeping %s's nvim/extension links)", from, to, to)
	if r.o.DryRun {
		return nil
	}
	ents, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	for _, e := range ents {
		src, dst := filepath.Join(from, e.Name()), filepath.Join(to, e.Name())
		if exists(dst) { // an install link (checkMove): the old one is a link too
			if err := os.Remove(src); err != nil {
				return err
			}
			continue
		}
		if err := os.Rename(src, dst); err != nil {
			return err
		}
	}
	return os.Remove(from)
}

// renameDaemonFiles: bobad.log → pekoed.log; the socket, pid and lock of
// the stopped bobad are dropped (pekoed makes its own).
func (r *runner) renameDaemonFiles(dir string) error {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range ents {
		suffix, ok := strings.CutPrefix(e.Name(), oldDaemon+".")
		if !ok {
			continue
		}
		src := filepath.Join(dir, e.Name())
		switch suffix {
		case "sock", "pid", "lock":
			r.step("remove %s", src)
			if !r.o.DryRun {
				if err := os.Remove(src); err != nil {
					return err
				}
			}
		default:
			dst := filepath.Join(dir, newDaemon+"."+suffix)
			if exists(dst) {
				r.step("keep %s (%s already exists)", src, dst)
				continue
			}
			r.step("rename %s → %s", src, filepath.Base(dst))
			if !r.o.DryRun {
				if err := os.Rename(src, dst); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// ---- state.db ----

// userText are columns holding what the user or an agent wrote: left as is.
var userText = map[string]bool{"prompts.text": true, "comments.text": true, "risks.text": true}

func (r *runner) replacer() *strings.Replacer {
	pairs := []string{oldRefs, newRefs}
	if r.l.OldData != r.l.NewData {
		pairs = append(pairs, r.l.OldData+"/", r.l.NewData+"/")
	}
	if r.l.OldConfig != r.l.NewConfig {
		pairs = append(pairs, r.l.OldConfig+"/", r.l.NewConfig+"/")
	}
	return strings.NewReplacer(pairs...)
}

// rewriteDB replaces old refs and paths in state.db and returns the
// repositories it knows (main worktrees, else session worktrees).
func (r *runner) rewriteDB(path string) ([]string, error) {
	if !exists(path) {
		return nil, nil
	}
	if r.o.DryRun {
		// even a read-only open leaves a -shm next to a WAL database:
		// count on a throwaway copy instead
		tmp, err := os.MkdirTemp("", "pekoe-migrate-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(tmp)
		for _, suffix := range []string{"", "-wal"} {
			b, err := os.ReadFile(path + suffix)
			if err != nil {
				if suffix != "" && os.IsNotExist(err) {
					continue
				}
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(tmp, "state.db"+suffix), b, 0o600); err != nil {
				return nil, err
			}
		}
		path = filepath.Join(tmp, "state.db")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	type col struct{ table, name string }
	var cols []col
	tables, err := queryStrings(db, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, t := range tables {
		rows, err := db.Query(`SELECT name, type FROM pragma_table_info(?)`, t)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var name, typ string
			if err := rows.Scan(&name, &typ); err != nil {
				rows.Close()
				return nil, err
			}
			if strings.Contains(strings.ToUpper(typ), "TEXT") && !userText[t+"."+name] {
				cols = append(cols, col{t, name})
			}
		}
		rows.Close()
	}

	rep := r.replacer()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	changed := 0
	for _, c := range cols {
		q := fmt.Sprintf(`SELECT rowid, %q FROM %q WHERE %q IS NOT NULL`, c.name, c.table, c.name)
		rows, err := tx.Query(q)
		if err != nil {
			return nil, err
		}
		type upd struct {
			id int64
			v  string
		}
		var ups []upd
		for rows.Next() {
			var id int64
			var v string
			if err := rows.Scan(&id, &v); err != nil {
				rows.Close()
				return nil, err
			}
			if nv := rep.Replace(v); nv != v {
				ups = append(ups, upd{id, nv})
			}
		}
		rows.Close()
		for _, u := range ups {
			if !r.o.DryRun {
				if _, err := tx.Exec(fmt.Sprintf(`UPDATE %q SET %q = ? WHERE rowid = ?`, c.table, c.name), u.v, u.id); err != nil {
					return nil, err
				}
			}
		}
		if len(ups) > 0 {
			r.step("state.db: %s.%s · %d rows (refs/boba/ and old paths → new)", c.table, c.name, len(ups))
		}
		changed += len(ups)
	}
	n, err := r.rewriteEnv(tx)
	if err != nil {
		return nil, err
	}
	changed += n
	if r.o.DryRun {
		err = tx.Rollback()
	} else {
		err = tx.Commit()
	}
	if err != nil { // the one connection is free again only after this
		return nil, err
	}
	if changed == 0 {
		r.step("state.db: nothing to rewrite")
	}
	return knownRepos(db)
}

// rewriteEnv renames the old variables in each session's env (the JSON
// "env" of sessions.data, given to its agent, dev and run windows):
// BOBA_<NAME>=v → PEKOE_<NAME>=v and BOBA=v → PEKOE=v. Only the env array
// is touched; the rest of the JSON is left as it is.
func (r *runner) rewriteEnv(tx *sql.Tx) (int, error) {
	rows, err := tx.Query(`SELECT rowid, data FROM sessions WHERE data IS NOT NULL`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return 0, nil
		}
		return 0, err
	}
	type upd struct {
		id int64
		v  string
	}
	var ups []upd
	for rows.Next() {
		var id int64
		var v string
		if err := rows.Scan(&id, &v); err != nil {
			rows.Close()
			return 0, err
		}
		if nv, ok := renameEnv(v); ok {
			ups = append(ups, upd{id, nv})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, u := range ups {
		if !r.o.DryRun {
			if _, err := tx.Exec(`UPDATE sessions SET data = ? WHERE rowid = ?`, u.v, u.id); err != nil {
				return 0, err
			}
		}
	}
	if len(ups) > 0 {
		r.step("state.db: sessions.data · %d rows (env BOBA_* → PEKOE_*)", len(ups))
	}
	return len(ups), nil
}

// renameEnv returns data with the old keys in its "env" renamed, and
// whether anything changed. An old entry whose new key is already set is
// dropped (the new one wins). Data that is not a JSON object is left alone.
func renameEnv(data string) (string, bool) {
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(data), &obj) != nil {
		return data, false
	}
	raw, ok := obj["env"]
	if !ok {
		return data, false
	}
	var env []string
	if json.Unmarshal(raw, &env) != nil {
		return data, false
	}
	has := map[string]bool{}
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		has[k] = true
	}
	out := make([]string, 0, len(env))
	changed := false
	for _, e := range env {
		k, v, eq := strings.Cut(e, "=")
		nk := newEnvKey(k)
		if nk == k {
			out = append(out, e)
			continue
		}
		changed = true
		if has[nk] {
			continue
		}
		has[nk] = true
		if eq {
			nk += "=" + v
		}
		out = append(out, nk)
	}
	if !changed {
		return data, false
	}
	b, err := json.Marshal(out)
	if err != nil {
		return data, false
	}
	obj["env"] = b
	nd, err := json.Marshal(obj)
	if err != nil {
		return data, false
	}
	return string(nd), true
}

// newEnvKey: BOBA → PEKOE, BOBA_<NAME> → PEKOE_<NAME>, others unchanged.
func newEnvKey(k string) string {
	if k == "BOBA" {
		return "PEKOE"
	}
	if rest, ok := strings.CutPrefix(k, "BOBA_"); ok {
		return "PEKOE_" + rest
	}
	return k
}

func queryStrings(db *sql.DB, q string, args ...any) ([]string, error) {
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// knownRepos: the directories of every session and remembered repository
// (kv "repo:<id>"), as pekoed itself finds them.
func knownRepos(db *sql.DB) ([]string, error) {
	var dirs []string
	if datas, err := queryStrings(db, `SELECT data FROM sessions`); err == nil {
		for _, d := range datas {
			var s struct {
				RepoDir string `json:"repo_dir"`
				Path    string `json:"path"`
			}
			if json.Unmarshal([]byte(d), &s) == nil {
				dirs = append(dirs, s.RepoDir, s.Path)
			}
		}
	}
	if vs, err := queryStrings(db, `SELECT v FROM kv WHERE k LIKE 'repo:%'`); err == nil {
		for _, v := range vs {
			var rr struct {
				Dir string `json:"dir"`
			}
			if json.Unmarshal([]byte(v), &rr) == nil {
				dirs = append(dirs, rr.Dir)
			}
		}
	}
	return dirs, nil
}

// ---- refs ----

// copyRefs copies refs/boba/* to refs/pekoe/* once per repository (the
// worktrees of one repository share its refs).
func (r *runner) copyRefs(dirs []string) {
	seen := map[string]bool{}
	var repos []string
	for _, d := range dirs {
		if d == "" || !exists(d) {
			continue
		}
		common, err := gitx.Run(d, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil || seen[common] {
			continue
		}
		seen[common] = true
		repos = append(repos, d)
	}
	sort.Strings(repos)
	for _, d := range repos {
		out, err := gitx.Run(d, "for-each-ref", "--format=%(objectname) %(refname)", oldRefs)
		if err != nil {
			r.step("%s: cannot list refs: %v", d, err)
			continue
		}
		if out == "" {
			continue
		}
		lines := strings.Split(out, "\n")
		verb := "copy"
		if r.o.PruneOld {
			verb = "move"
		}
		r.step("%s: %s %d refs refs/boba/* → refs/pekoe/*", d, verb, len(lines))
		if r.o.DryRun {
			continue
		}
		for _, l := range lines {
			sha, ref, ok := strings.Cut(l, " ")
			if !ok {
				continue
			}
			nref := newRefs + strings.TrimPrefix(ref, oldRefs)
			if err := gitx.UpdateRef(d, nref, sha); err != nil {
				r.step("%s: %s: %v", d, nref, err)
				continue
			}
			if r.o.PruneOld {
				if _, err := gitx.Run(d, "update-ref", "-d", ref, sha); err != nil {
					r.step("%s: delete %s: %v", d, ref, err)
				}
			}
		}
	}
}

// ---- hooks ----

func (r *runner) hooks() error {
	if !hooks.HasLegacy() {
		return nil
	}
	path, before, after, changed, err := hooks.Plan(r.o.Bin, false)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	r.step("%s: replace the `boba event` hooks with `%s event`", path, r.o.Bin)
	if r.o.DryRun {
		return nil
	}
	backup, err := hooks.Apply(path, before, after)
	if err != nil {
		return err
	}
	if backup != "" {
		r.step("backup: %s", backup)
	}
	return nil
}

// ---- what is left ----

// scanConfig points at config lines that still say boba (paths the user
// wrote, [web] hostname …): they are the user's, so only shown. Comments
// (the old `boba init` drafts say boba) are left alone.
func (r *runner) scanConfig(dir string) {
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for i, line := range strings.Split(string(b), "\n") {
			if l := strings.TrimSpace(line); !strings.HasPrefix(l, "#") && strings.Contains(strings.ToLower(l), oldName) {
				r.step("check by hand: %s:%d: %s", p, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
}

func (r *runner) todo() {
	home, _ := os.UserHomeDir()
	var items []string
	items = append(items, "MCP:    claude mcp remove boba && claude mcp add pekoe -- pekoe mcp")
	if exists(filepath.Join(home, "Library", "LaunchAgents", "dev.boba.bobad.plist")) {
		items = append(items, "launchd: make uninstall-launchd && make install-launchd   (in the pekoe repository; drops dev.boba.bobad)")
	}
	var bins []string
	for _, b := range []string{oldName, oldDaemon} {
		if p, err := exec.LookPath(b); err == nil {
			bins = append(bins, p)
		}
	}
	if len(bins) > 0 {
		items = append(items, "old binaries: rm "+strings.Join(bins, " "))
	}
	if exists(filepath.Join(r.l.NewData, "tsnet")) || exists(filepath.Join(r.l.OldData, "tsnet")) {
		items = append(items, `web:    the tailnet hostname now defaults to "pekoe" (restart pekoed; set [web] hostname = "boba" to keep the old URL)`)
	}
	items = append(items,
		"nvim:   require('boba') → require('pekoe'), :Boba* → :Pekoe* in your config",
		"Chrome: reload the extension at chrome://extensions",
		"tmux:   once the old agents are done: tmux -L boba kill-server",
	)
	if r.o.DryRun {
		fmt.Fprintln(r.out, "\nafter the migration, by hand:")
	} else {
		fmt.Fprintln(r.out, "\ndone. Left to do by hand:")
	}
	for _, it := range items {
		fmt.Fprintln(r.out, "  - "+it)
	}
}
