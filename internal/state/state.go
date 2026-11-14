// Package state is pekoed's external memory: ~/.local/share/pekoe/state.db.
// Sessions, the port ledger, prompts, review points, hunk approvals,
// comments, picks. Used to restore after a daemon restart.
package state

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type State string

const (
	Creating  State = "creating"
	Running   State = "running"
	Waiting   State = "waiting"
	Idle      State = "idle"
	Done      State = "done"
	Reviewing State = "reviewing"
	Approved  State = "approved"
	Landed    State = "landed"
	Failed    State = "failed"
)

type Test struct {
	Status  string    `json:"status"` // pass | fail | running | none | blocked (no sandbox: not run)
	Summary string    `json:"summary"`
	At      time.Time `json:"at"`
}

type Tool struct {
	Name    string   `json:"name"`
	Input   string   `json:"input"`
	Touches string   `json:"touches,omitempty"` // file the tool will change (Edit/Write)
	AlsoIn  []string `json:"also_in,omitempty"` // other open sessions that already changed Touches
	// Input is a one-line summary of the argument; these say how much of
	// it the summary leaves out, so a reader knows to look at the whole.
	Lines int  `json:"lines,omitempty"` // lines in the argument, when more than one
	Size  int  `json:"size,omitempty"`  // bytes in the argument, when Cut
	Cut   bool `json:"cut,omitempty"`   // Input was cut short
}

// Overlap is a conflict forecast against another open session of the same
// repository: both touch Files. Conflict means their committed contents do
// not merge (git merge-tree); uncommitted work only counts as overlap.
type Overlap struct {
	Slug          string   `json:"slug"`
	Files         []string `json:"files"`
	Conflict      bool     `json:"conflict,omitempty"`
	ConflictFiles []string `json:"conflict_files,omitempty"`
}

type Stat struct {
	Added   int `json:"added"`
	Deleted int `json:"deleted"`
	Files   int `json:"files"`
}

// PRStatus is the summary of a session's GitHub PR that the daemon polls
// (pr.view has the full picture). Attention lists what changed since the
// user last opened the PR view: ci_failed | changes_requested |
// new_comment | mergeable.
type PRStatus struct {
	State          string    `json:"state"` // OPEN | MERGED | CLOSED
	Draft          bool      `json:"draft,omitempty"`
	Pass           int       `json:"pass"`
	Fail           int       `json:"fail"`
	Pending        int       `json:"pending"`
	ReviewDecision string    `json:"review_decision,omitempty"` // APPROVED | CHANGES_REQUESTED | REVIEW_REQUIRED
	Unresolved     int       `json:"unresolved"`                // unresolved review threads
	MergeState     string    `json:"merge_state,omitempty"`     // mergeStateStatus: CLEAN | BLOCKED | DIRTY | …
	HeadSHA        string    `json:"head_sha,omitempty"`        // the PR's head commit on GitHub (headRefOid)
	LastComment    time.Time `json:"last_comment,omitempty"`    // newest comment / review / thread comment
	LastThread     time.Time `json:"last_thread,omitempty"`     // newest comment on an unresolved thread
	Attention      []string  `json:"attention,omitempty"`
	AttentionAt    time.Time `json:"attention_at,omitempty"`
	At             time.Time `json:"at"` // fetched
}

// Same reports whether two summaries show the same thing (fetch time aside).
func (p *PRStatus) Same(q *PRStatus) bool {
	if p == nil || q == nil {
		return p == q
	}
	if len(p.Attention) != len(q.Attention) {
		return false
	}
	for i := range p.Attention {
		if p.Attention[i] != q.Attention[i] {
			return false
		}
	}
	return p.State == q.State && p.Draft == q.Draft && p.Pass == q.Pass && p.Fail == q.Fail &&
		p.Pending == q.Pending && p.ReviewDecision == q.ReviewDecision && p.Unresolved == q.Unresolved &&
		p.MergeState == q.MergeState && p.HeadSHA == q.HeadSHA && p.LastComment.Equal(q.LastComment) && p.LastThread.Equal(q.LastThread)
}

type Session struct {
	Slug            string    `json:"slug"`
	Branch          string    `json:"branch"`
	Repo            string    `json:"repo"`
	RepoID          string    `json:"repo_id"`
	RepoDir         string    `json:"repo_dir"` // main worktree of the repository
	Path            string    `json:"path"`
	Agent           string    `json:"agent"`
	State           State     `json:"state"`
	WaitKind        string    `json:"wait_kind,omitempty"`
	StateSince      time.Time `json:"state_since"`
	CreatedAt       time.Time `json:"created_at"`
	Port            int       `json:"port"`
	LastTool        string    `json:"last_tool,omitempty"`
	PendingTool     *Tool     `json:"pending_tool,omitempty"`
	Test            Test      `json:"test"`
	Diffstat        Stat      `json:"diffstat"`
	Prompts         int       `json:"prompts"`
	ReviewN         int       `json:"review_n"`
	ClaudeSessionID string    `json:"claude_session_id,omitempty"`
	Unread          int64     `json:"unread"`
	Activity        []int     `json:"activity"`
	Tokens          int       `json:"tokens"`
	Dev             bool      `json:"dev"`
	Run             string    `json:"run,omitempty"` // command in the open run window (TUI !)
	PR              int       `json:"pr,omitempty"`
	PRReview        bool      `json:"pr_review,omitempty"` // reviewing someone else's PR
	PRStatus        *PRStatus `json:"pr_status,omitempty"` // small summary of the PR, polled from GitHub
	Managed         bool      `json:"managed"`             // worktree created by pekoe (under ~/worktrees)
	OwnDir          bool      `json:"own_dir,omitempty"`   // this session made the worktree directory (rm may delete what is left of it)
	Base            string    `json:"base"`
	Issue           int       `json:"issue,omitempty"` // GitHub issue the session was started from (--from gh#N)
	Error           string    `json:"error,omitempty"`
	GitBlocked      string    `json:"git_blocked,omitempty"` // why pekoed runs no git in the worktree (its .git was changed; gitx.TamperedError)
	Landing         bool      `json:"landing,omitempty"`
	LastMessage     string    `json:"last_message,omitempty"`
	Stopped         bool      `json:"stopped,omitempty"`    // ended with x; do not auto-resume
	SeenLines       int       `json:"seen_lines,omitempty"` // absolute line offset when the user left
	Env             []string  `json:"env,omitempty"`
	Overlaps        []Overlap `json:"overlaps,omitempty"` // conflict forecast (daemon/conflicts.go)
	// Parent is who created the session with session.new: "+tapi", a
	// session's slug, "human" or "unknown" (daemon/caller.go). wait's mine
	// and, later, the inbox's answers go to it. Empty: not made by session.new.
	Parent string         `json:"parent,omitempty"`
	Report *ReportSummary `json:"report,omitempty"` // the latest report.post
	// Review is the latest independent review (report.post --status
	// review): started (verdict pending), passed or failed, and the commit.
	// It stays when later reports (done, progress) come in.
	Review *ReviewSummary `json:"review,omitempty"`
	// Request is the ledger entry the session works on (pekoe new
	// --request; daemon/ledger.go). 0: none.
	Request int64 `json:"request,omitempty"`
	// After are the slugs of the sessions this one comes after (pekoe new
	// --after, daemon/after.go). Only recorded and shown: nothing waits on
	// them yet.
	After []string `json:"after,omitempty"`
	// Dev (above) is true while the dev hook's process ([hooks] dev) runs;
	// it turns false when the hook ends by itself (DevExit says how) or
	// pekoe stops it. DevLog is the file its output goes to (pekoe dev
	// --log), set from the first start on.
	DevLog string `json:"dev_log,omitempty"`
	// DevExit is how the last dev hook ended; nil while it runs or before
	// it ever ran.
	DevExit *DevExit `json:"dev_exit,omitempty"`
	// Warnings are what pekoe noticed while creating the session that did
	// not stop it ([worktree] copy sources missing or empty).
	Warnings []string `json:"warnings,omitempty"`
}

// DevExit is how a session's dev hook ended.
type DevExit struct {
	// Status is the hook's exit status; -1 when it is unknown, a signal
	// ended it (Signal), or pekoe stopped it (Stopped).
	Status  int       `json:"status"`
	Signal  string    `json:"signal,omitempty"`
	Stopped bool      `json:"stopped,omitempty"` // pekoe stopped it (pekoe dev, rm, x, pekoed stopping its agents)
	At      time.Time `json:"at"`
	// Closed: its window, kept after the hook ended by itself so its last
	// screen can be read, has been closed.
	Closed bool `json:"closed,omitempty"`
	// DevStop is the [hooks] dev_stop run after it (nil: none ran).
	DevStop *HookRun `json:"dev_stop,omitempty"`
}

// HookRun is one run of a hook command that pekoed waited for.
type HookRun struct {
	Command string    `json:"command"`
	Status  int       `json:"status"` // exit status; -1: did not exit by itself (timeout, signal, not started)
	Error   string    `json:"error,omitempty"`
	At      time.Time `json:"at"` // when it ended
}

type DB struct {
	db *sql.DB
	mu sync.Mutex
}

const schema = `
CREATE TABLE IF NOT EXISTS sessions (slug TEXT PRIMARY KEY, data TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS ports (port INTEGER PRIMARY KEY, repo_id TEXT NOT NULL, slug TEXT NOT NULL, UNIQUE(repo_id, slug));
CREATE TABLE IF NOT EXISTS held_ports (port INTEGER PRIMARY KEY, repo_id TEXT NOT NULL, slug TEXT NOT NULL, until INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS prompts (slug TEXT NOT NULL, n INTEGER NOT NULL, text TEXT NOT NULL, ref TEXT NOT NULL, at TEXT NOT NULL, PRIMARY KEY(slug, n));
CREATE TABLE IF NOT EXISTS reviews (slug TEXT NOT NULL, n INTEGER NOT NULL, ref TEXT NOT NULL, at TEXT NOT NULL, PRIMARY KEY(slug, n));
CREATE TABLE IF NOT EXISTS hunks (slug TEXT NOT NULL, hunk_id TEXT NOT NULL, status TEXT NOT NULL, at TEXT NOT NULL, PRIMARY KEY(slug, hunk_id));
CREATE TABLE IF NOT EXISTS comments (id INTEGER PRIMARY KEY AUTOINCREMENT, slug TEXT NOT NULL, path TEXT, line INTEGER, hunk_id TEXT, text TEXT NOT NULL, kind TEXT NOT NULL DEFAULT 'comment', sent INTEGER NOT NULL DEFAULT 0, at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS picks (id INTEGER PRIMARY KEY AUTOINCREMENT, repo_id TEXT, slug TEXT, data TEXT NOT NULL, sent INTEGER NOT NULL DEFAULT 0, at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS risks (slug TEXT NOT NULL, path TEXT, line INTEGER, text TEXT);
CREATE TABLE IF NOT EXISTS kv (k TEXT PRIMARY KEY, v TEXT);
CREATE TABLE IF NOT EXISTS reports (id INTEGER PRIMARY KEY AUTOINCREMENT, slug TEXT NOT NULL, by TEXT NOT NULL, claimed TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, summary TEXT NOT NULL, evidence TEXT NOT NULL DEFAULT '[]', head TEXT NOT NULL DEFAULT '', dirty INTEGER NOT NULL DEFAULT 0, verdict TEXT NOT NULL DEFAULT '', reviewed_sha TEXT NOT NULL DEFAULT '', at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS reports_slug ON reports(slug, id);
CREATE TABLE IF NOT EXISTS sends (id INTEGER PRIMARY KEY AUTOINCREMENT, at TEXT NOT NULL, from_ TEXT NOT NULL, to_ TEXT NOT NULL, kind TEXT NOT NULL, text TEXT NOT NULL, via TEXT NOT NULL DEFAULT '', ok INTEGER NOT NULL, error TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS inbox (id INTEGER PRIMARY KEY AUTOINCREMENT, rev INTEGER NOT NULL, kind TEXT NOT NULL, from_ TEXT NOT NULL, claimed TEXT NOT NULL DEFAULT '', about TEXT NOT NULL DEFAULT '', question TEXT NOT NULL, options TEXT NOT NULL DEFAULT '[]', recommend INTEGER, context TEXT NOT NULL DEFAULT '', links TEXT NOT NULL DEFAULT '[]', facts TEXT, status TEXT NOT NULL, answer TEXT, delivery TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS inbox_from ON inbox(from_, status);
CREATE TABLE IF NOT EXISTS ledger (id INTEGER PRIMARY KEY AUTOINCREMENT, at TEXT NOT NULL, text TEXT NOT NULL, status TEXT NOT NULL, sessions TEXT NOT NULL DEFAULT '[]', evidence TEXT NOT NULL DEFAULT '[]', note TEXT NOT NULL DEFAULT '', closed_at TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS usage_cursors (slug TEXT NOT NULL, born TEXT NOT NULL, path TEXT NOT NULL, off INTEGER NOT NULL DEFAULT 0, last_id TEXT NOT NULL DEFAULT '', PRIMARY KEY(slug, born, path));
CREATE TABLE IF NOT EXISTS usage_days (slug TEXT NOT NULL, born TEXT NOT NULL, repo TEXT NOT NULL, day TEXT NOT NULL, input INTEGER NOT NULL DEFAULT 0, output INTEGER NOT NULL DEFAULT 0, cache_creation INTEGER NOT NULL DEFAULT 0, cache_read INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(slug, born, day));
CREATE INDEX IF NOT EXISTS usage_days_day ON usage_days(day);
CREATE TABLE IF NOT EXISTS session_log (slug TEXT NOT NULL, born TEXT NOT NULL, repo TEXT NOT NULL, repo_id TEXT NOT NULL DEFAULT '', branch TEXT NOT NULL DEFAULT '', pr INTEGER NOT NULL DEFAULT 0, merged_at TEXT NOT NULL DEFAULT '', ended_at TEXT NOT NULL DEFAULT '', PRIMARY KEY(slug, born));
`

func Open(path string) (*DB, error) {
	// SQLite creates a new database with the umask's mode (often 0644) and
	// gives -wal / -shm the main file's mode, so create it owner-only first.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &DB{db: db}, nil
}

func (d *DB) Close() error { return d.db.Close() }

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// ---- sessions ----

func (d *DB) SaveSession(s *Session) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = d.db.Exec(`INSERT INTO sessions(slug,data) VALUES(?,?) ON CONFLICT(slug) DO UPDATE SET data=excluded.data`, s.Slug, string(b))
	return err
}

func (d *DB) DeleteSession(slug string) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	for _, q := range []string{
		`DELETE FROM sessions WHERE slug=?`, `DELETE FROM ports WHERE slug=?`,
		`DELETE FROM prompts WHERE slug=?`, `DELETE FROM reviews WHERE slug=?`,
		`DELETE FROM hunks WHERE slug=?`, `DELETE FROM comments WHERE slug=?`,
		`DELETE FROM risks WHERE slug=?`, `DELETE FROM reports WHERE slug=?`,
	} {
		if _, err := tx.Exec(q, slug); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) Sessions() ([]*Session, error) {
	rows, err := d.db.Query(`SELECT data FROM sessions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Session
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		var ss Session
		if err := json.Unmarshal([]byte(s), &ss); err != nil {
			continue
		}
		out = append(out, &ss)
	}
	return out, rows.Err()
}

// ---- port ledger ----

// AllocPort returns the port recorded for (repoID, slug), or records the
// lowest free port >= base. busy reports ports in use by other processes.
// Ports held (HoldPort) until a time not yet passed are skipped; the ones
// whose time has passed are dropped.
func (d *DB) AllocPort(repoID, slug string, base int, busy func(int) bool) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var p int
	err := d.db.QueryRow(`SELECT port FROM ports WHERE repo_id=? AND slug=?`, repoID, slug).Scan(&p)
	if err == nil {
		return p, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	used := map[int]bool{}
	rows, err := d.db.Query(`SELECT port FROM ports`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var x int
		rows.Scan(&x)
		used[x] = true
	}
	rows.Close()
	if _, err := d.db.Exec(`DELETE FROM held_ports WHERE until<=?`, time.Now().Unix()); err != nil {
		return 0, err
	}
	rows, err = d.db.Query(`SELECT port FROM held_ports`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var x int
		rows.Scan(&x)
		used[x] = true
	}
	rows.Close()
	for p = base; p < base+2000; p++ {
		if used[p] || (busy != nil && busy(p)) {
			continue
		}
		if _, err := d.db.Exec(`INSERT INTO ports(port,repo_id,slug) VALUES(?,?,?)`, p, repoID, slug); err != nil {
			return 0, err
		}
		return p, nil
	}
	return 0, errors.New("no free port")
}

// HeldPort is a port no new session gets until Until: the session that had
// it (Slug) was removed while what its dev hook started outside its own
// process may still hold ports derived from it ([hooks] dev_stop did not
// succeed).
type HeldPort struct {
	Port   int       `json:"port"`
	RepoID string    `json:"repo_id"`
	Slug   string    `json:"slug"`
	Until  time.Time `json:"until"`
}

// HoldPort keeps AllocPort from giving port to a new session until until.
func (d *DB) HoldPort(h HeldPort) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.db.Exec(`INSERT INTO held_ports(port,repo_id,slug,until) VALUES(?,?,?,?) ON CONFLICT(port) DO UPDATE SET repo_id=excluded.repo_id, slug=excluded.slug, until=MAX(until, excluded.until)`,
		h.Port, h.RepoID, h.Slug, h.Until.Unix())
	return err
}

func (d *DB) PortOwner(port int) (repoID, slug string, ok bool) {
	err := d.db.QueryRow(`SELECT repo_id, slug FROM ports WHERE port=?`, port).Scan(&repoID, &slug)
	return repoID, slug, err == nil
}

// MovePort re-files the port of (oldID, slug) under newID, keeping the port
// number, for a session whose repository's id changed (an origin was added
// after it was created). A row already present for (newID, slug) wins.
func (d *DB) MovePort(oldID, newID, slug string) error {
	_, err := d.db.Exec(`UPDATE OR IGNORE ports SET repo_id=? WHERE repo_id=? AND slug=?`, newID, oldID, slug)
	return err
}

// ---- prompts (snapshots) ----

type Prompt struct {
	N    int       `json:"n"`
	Text string    `json:"text"`
	Ref  string    `json:"ref"`
	At   time.Time `json:"at"`
}

func (d *DB) AddPrompt(slug string, n int, text, ref string) error {
	_, err := d.db.Exec(`INSERT OR REPLACE INTO prompts(slug,n,text,ref,at) VALUES(?,?,?,?,?)`, slug, n, text, ref, now())
	return err
}

func (d *DB) Prompts(slug string) ([]Prompt, error) {
	rows, err := d.db.Query(`SELECT n,text,ref,at FROM prompts WHERE slug=? ORDER BY n`, slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Prompt
	for rows.Next() {
		var p Prompt
		var at string
		rows.Scan(&p.N, &p.Text, &p.Ref, &at)
		p.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, p)
	}
	return out, nil
}

func (d *DB) TruncatePrompts(slug string, fromN int) error {
	_, err := d.db.Exec(`DELETE FROM prompts WHERE slug=? AND n>=?`, slug, fromN)
	return err
}

// ---- review points ----

func (d *DB) AddReview(slug string, n int, ref string) error {
	_, err := d.db.Exec(`INSERT OR REPLACE INTO reviews(slug,n,ref,at) VALUES(?,?,?,?)`, slug, n, ref, now())
	return err
}

func (d *DB) ReviewRef(slug string, n int) (string, bool) {
	var r string
	err := d.db.QueryRow(`SELECT ref FROM reviews WHERE slug=? AND n=?`, slug, n).Scan(&r)
	return r, err == nil
}

func (d *DB) ReviewAt(slug string, n int) (time.Time, bool) {
	var at string
	if err := d.db.QueryRow(`SELECT at FROM reviews WHERE slug=? AND n=?`, slug, n).Scan(&at); err != nil {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	return t, err == nil
}

// ---- hunk approvals ----

func (d *DB) SetHunk(slug, id, status string) error {
	if status == "" {
		_, err := d.db.Exec(`DELETE FROM hunks WHERE slug=? AND hunk_id=?`, slug, id)
		return err
	}
	_, err := d.db.Exec(`INSERT OR REPLACE INTO hunks(slug,hunk_id,status,at) VALUES(?,?,?,?)`, slug, id, status, now())
	return err
}

func (d *DB) Hunks(slug string) (map[string]string, error) {
	rows, err := d.db.Query(`SELECT hunk_id,status FROM hunks WHERE slug=?`, slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var id, st string
		rows.Scan(&id, &st)
		m[id] = st
	}
	return m, nil
}

// ClearRejections forgets "rejected" marks (after the agent was asked to fix).
func (d *DB) ClearRejections(slug string) error {
	_, err := d.db.Exec(`DELETE FROM hunks WHERE slug=? AND status='rejected'`, slug)
	return err
}

// ---- comments ----

type Comment struct {
	ID     int64     `json:"id"`
	Path   string    `json:"path"`
	Line   int       `json:"line"`
	HunkID string    `json:"hunk_id,omitempty"`
	Text   string    `json:"text"`
	Kind   string    `json:"kind"` // comment | reject
	Sent   bool      `json:"sent"`
	At     time.Time `json:"at"`
}

func (d *DB) AddComment(slug string, c Comment) (int64, error) {
	if c.Kind == "" {
		c.Kind = "comment"
	}
	r, err := d.db.Exec(`INSERT INTO comments(slug,path,line,hunk_id,text,kind,at) VALUES(?,?,?,?,?,?,?)`, slug, c.Path, c.Line, c.HunkID, c.Text, c.Kind, now())
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

func (d *DB) DeleteComment(slug string, id int64) error {
	_, err := d.db.Exec(`DELETE FROM comments WHERE slug=? AND id=? AND sent=0`, slug, id)
	return err
}

func (d *DB) Comments(slug string, unsentOnly bool) ([]Comment, error) {
	q := `SELECT id,path,line,hunk_id,text,kind,sent,at FROM comments WHERE slug=?`
	if unsentOnly {
		q += ` AND sent=0`
	}
	q += ` ORDER BY path, line, id`
	rows, err := d.db.Query(q, slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Comment
	for rows.Next() {
		var c Comment
		var at string
		var sent int
		var path, hunk sql.NullString
		var line sql.NullInt64
		rows.Scan(&c.ID, &path, &line, &hunk, &c.Text, &c.Kind, &sent, &at)
		c.Path, c.Line, c.HunkID, c.Sent = path.String, int(line.Int64), hunk.String, sent == 1
		c.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, c)
	}
	return out, nil
}

func (d *DB) MarkCommentsSent(slug string) error {
	_, err := d.db.Exec(`UPDATE comments SET sent=1 WHERE slug=? AND sent=0`, slug)
	return err
}

// ---- picks ----

type Pick struct {
	ID          int64           `json:"id"`
	RepoID      string          `json:"repo_id"`
	Slug        string          `json:"slug"`
	URL         string          `json:"url"`
	Title       string          `json:"title,omitempty"`
	Instruction string          `json:"instruction"`
	Selector    string          `json:"selector"`
	Component   json.RawMessage `json:"component,omitempty"`
	Styles      json.RawMessage `json:"styles,omitempty"`
	OuterHTML   string          `json:"outerHTML,omitempty"`
	Rect        json.RawMessage `json:"rect,omitempty"`
	File        string          `json:"file"`  // .md path
	Image       string          `json:"image"` // .png path
	ImageSize   string          `json:"image_size,omitempty"`
	Sent        bool            `json:"sent"`
	SentTo      string          `json:"sent_to,omitempty"`
	At          time.Time       `json:"at"`
}

func (d *DB) AddPick(p *Pick) error {
	p.At = time.Now()
	b, _ := json.Marshal(p)
	r, err := d.db.Exec(`INSERT INTO picks(repo_id,slug,data,at) VALUES(?,?,?,?)`, p.RepoID, p.Slug, string(b), now())
	if err != nil {
		return err
	}
	p.ID, _ = r.LastInsertId()
	return d.SavePick(p)
}

func (d *DB) SavePick(p *Pick) error {
	b, _ := json.Marshal(p)
	sent := 0
	if p.Sent {
		sent = 1
	}
	_, err := d.db.Exec(`UPDATE picks SET repo_id=?, slug=?, data=?, sent=? WHERE id=?`, p.RepoID, p.Slug, string(b), sent, p.ID)
	return err
}

// MovePicks re-files picks from oldID to newID: those addressed to one of
// slugs, or, with all, every pick of oldID (also those not addressed to a
// session). Only the id changes; the .md/.png files stay where they are, as
// picks refer to them by absolute path.
func (d *DB) MovePicks(oldID, newID string, slugs []string, all bool) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	q := `UPDATE picks SET repo_id=?, data=json_set(data, '$.repo_id', ?) WHERE repo_id=?`
	if all {
		if _, err := tx.Exec(q, newID, newID, oldID); err != nil {
			tx.Rollback()
			return err
		}
	} else {
		for _, slug := range slugs {
			if _, err := tx.Exec(q+` AND slug=?`, newID, newID, oldID, slug); err != nil {
				tx.Rollback()
				return err
			}
		}
	}
	return tx.Commit()
}

func (d *DB) Picks(limit int) ([]*Pick, error) {
	rows, err := d.db.Query(`SELECT data FROM picks ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Pick
	for rows.Next() {
		var s string
		rows.Scan(&s)
		var p Pick
		if json.Unmarshal([]byte(s), &p) == nil {
			out = append(out, &p)
		}
	}
	return out, nil
}

func (d *DB) PickByID(id int64) (*Pick, error) {
	var s string
	if err := d.db.QueryRow(`SELECT data FROM picks WHERE id=?`, id).Scan(&s); err != nil {
		return nil, fmt.Errorf("no such pick: %d", id)
	}
	var p Pick
	err := json.Unmarshal([]byte(s), &p)
	return &p, err
}

func (d *DB) DeletePick(id int64) error {
	_, err := d.db.Exec(`DELETE FROM picks WHERE id=?`, id)
	return err
}

// NextPickNumber is the sequence used for picks/0012.md file names.
func (d *DB) NextPickNumber() int {
	var n sql.NullInt64
	d.db.QueryRow(`SELECT MAX(id) FROM picks`).Scan(&n)
	return int(n.Int64) + 1
}

// ---- risks (gatekeeper) ----

type Risk struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

func (d *DB) SetRisks(slug string, rs []Risk) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	tx.Exec(`DELETE FROM risks WHERE slug=?`, slug)
	for _, r := range rs {
		tx.Exec(`INSERT INTO risks(slug,path,line,text) VALUES(?,?,?,?)`, slug, r.Path, r.Line, r.Text)
	}
	return tx.Commit()
}

func (d *DB) Risks(slug string) ([]Risk, error) {
	rows, err := d.db.Query(`SELECT path,line,text FROM risks WHERE slug=?`, slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Risk
	for rows.Next() {
		var r Risk
		rows.Scan(&r.Path, &r.Line, &r.Text)
		out = append(out, r)
	}
	return out, nil
}

// ---- kv ----

func (d *DB) Get(k string) string {
	var v string
	d.db.QueryRow(`SELECT v FROM kv WHERE k=?`, k).Scan(&v)
	return v
}

func (d *DB) Set(k, v string) error {
	_, err := d.db.Exec(`INSERT OR REPLACE INTO kv(k,v) VALUES(?,?)`, k, v)
	return err
}

func (d *DB) Delete(k string) error {
	_, err := d.db.Exec(`DELETE FROM kv WHERE k=?`, k)
	return err
}

// Prefixed returns every kv pair whose key starts with prefix, as {k, v}.
func (d *DB) Prefixed(prefix string) [][2]string {
	// SQLite's length() and substr() both count characters (Go's len
	// counts bytes, which misses non-ASCII prefixes)
	rows, err := d.db.Query(`SELECT k, v FROM kv WHERE substr(k, 1, length(?)) = ? ORDER BY k`, prefix, prefix)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) == nil {
			out = append(out, [2]string{k, v})
		}
	}
	return out
}
