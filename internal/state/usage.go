package state

// Usage and the session log (docs/orchestration.md "4. PR の集計"): the
// tokens a session's agent used, summed from its Claude Code transcripts
// day by day, and one row per session that pekoe saw land or removed.
// Both outlive the session (rm drops its sessions row, not these), so
// `pekoe stats` can say what was merged and used on a day after the
// sessions are gone. Session.Tokens stays what it was: the size of the
// last context, not the sum.
//
// A session's life is (slug, born): born is its created_at, so a slug
// used again later starts a record of its own.

import (
	"database/sql"
	"time"
)

// Usage is a sum of the usage records of assistant messages, by kind.
type Usage struct {
	Input         int64 `json:"input"`
	Output        int64 `json:"output"`
	CacheCreation int64 `json:"cache_creation"`
	CacheRead     int64 `json:"cache_read"`
}

// Total is every kind added up.
func (u Usage) Total() int64 { return u.Input + u.Output + u.CacheCreation + u.CacheRead }

// Add adds v to u.
func (u *Usage) Add(v Usage) {
	u.Input += v.Input
	u.Output += v.Output
	u.CacheCreation += v.CacheCreation
	u.CacheRead += v.CacheRead
}

// UsageCursor is how far one transcript of one session's life was read:
// the byte offset of the first line not yet counted, and the id of the
// last message counted (Claude Code writes one line per content block of
// a message, each with the message's usage).
type UsageCursor struct {
	Slug   string
	Born   string
	Path   string
	Offset int64
	LastID string
}

// UsageCursors lists the transcripts known for slug's life born (all of
// slug's lives when born is "*").
func (d *DB) UsageCursors(slug, born string) ([]UsageCursor, error) {
	q, args := `SELECT slug,born,path,off,last_id FROM usage_cursors WHERE slug=?`, []any{slug}
	if born != "*" {
		q += ` AND born=?`
		args = append(args, born)
	}
	rows, err := d.db.Query(q+` ORDER BY path`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageCursor
	for rows.Next() {
		var c UsageCursor
		if err := rows.Scan(&c.Slug, &c.Born, &c.Path, &c.Offset, &c.LastID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AddUsage moves c's cursor and adds byDay (local date → usage) to the
// life's days, in one transaction: a crash between them would count the
// same lines twice or not at all.
func (d *DB) AddUsage(c UsageCursor, repo string, byDay map[string]Usage) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO usage_cursors(slug,born,path,off,last_id) VALUES(?,?,?,?,?)
		ON CONFLICT(slug,born,path) DO UPDATE SET off=excluded.off, last_id=excluded.last_id`,
		c.Slug, c.Born, c.Path, c.Offset, c.LastID); err != nil {
		tx.Rollback()
		return err
	}
	for day, u := range byDay {
		if _, err := tx.Exec(`INSERT INTO usage_days(slug,born,repo,day,input,output,cache_creation,cache_read) VALUES(?,?,?,?,?,?,?,?)
			ON CONFLICT(slug,born,day) DO UPDATE SET input=input+excluded.input, output=output+excluded.output,
			cache_creation=cache_creation+excluded.cache_creation, cache_read=cache_read+excluded.cache_read`,
			c.Slug, c.Born, repo, day, u.Input, u.Output, u.CacheCreation, u.CacheRead); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// DeleteUsageCursors forgets where slug's life born was read (after its
// last count: the days stay).
func (d *DB) DeleteUsageCursors(slug, born string) error {
	_, err := d.db.Exec(`DELETE FROM usage_cursors WHERE slug=? AND born=?`, slug, born)
	return err
}

// UsageDay is one life's usage on one local date.
type UsageDay struct {
	Slug  string `json:"slug"`
	Born  string `json:"born"`
	Repo  string `json:"repo"`
	Day   string `json:"day"` // YYYY-MM-DD, pekoed's local time
	Usage Usage  `json:"tokens"`
}

// UsageDays lists the usage on from (YYYY-MM-DD) and later.
func (d *DB) UsageDays(from string) ([]UsageDay, error) {
	rows, err := d.db.Query(`SELECT slug,born,repo,day,input,output,cache_creation,cache_read FROM usage_days WHERE day>=? ORDER BY day,repo,slug`, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageDay
	for rows.Next() {
		var u UsageDay
		if err := rows.Scan(&u.Slug, &u.Born, &u.Repo, &u.Day, &u.Usage.Input, &u.Usage.Output, &u.Usage.CacheCreation, &u.Usage.CacheRead); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SessionRecord is what is kept of a session after it landed or was
// removed. Tokens is the sum of its usage days.
type SessionRecord struct {
	Slug      string     `json:"slug"`
	Repo      string     `json:"repo"`
	RepoID    string     `json:"repo_id,omitempty"`
	Branch    string     `json:"branch,omitempty"`
	PR        int        `json:"pr,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	MergedAt  *time.Time `json:"merged_at,omitempty"` // landed: when pekoe saw the merge
	EndedAt   *time.Time `json:"ended_at,omitempty"`  // landed, or removed without landing
	Tokens    Usage      `json:"tokens"`
}

// RecordSession stores r. A life already recorded keeps its first merge
// and end times (landed, then removed: the end is the landing) and takes a
// PR number it did not have.
func (d *DB) RecordSession(r SessionRecord) error {
	_, err := d.db.Exec(`INSERT INTO session_log(slug,born,repo,repo_id,branch,pr,merged_at,ended_at) VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(slug,born) DO UPDATE SET
		pr=CASE WHEN session_log.pr=0 THEN excluded.pr ELSE session_log.pr END,
		merged_at=CASE WHEN session_log.merged_at='' THEN excluded.merged_at ELSE session_log.merged_at END,
		ended_at=CASE WHEN session_log.ended_at='' THEN excluded.ended_at ELSE session_log.ended_at END`,
		r.Slug, ts(r.CreatedAt), r.Repo, r.RepoID, r.Branch, r.PR, optTime(r.MergedAt), optTime(r.EndedAt))
	return err
}

// SessionRecords lists the recorded lives that merged or ended at since or
// later, with their tokens, oldest first.
func (d *DB) SessionRecords(since time.Time) ([]SessionRecord, error) {
	s := ts(since)
	rows, err := d.db.Query(`SELECT l.slug,l.born,l.repo,l.repo_id,l.branch,l.pr,l.merged_at,l.ended_at,
		COALESCE(SUM(u.input),0),COALESCE(SUM(u.output),0),COALESCE(SUM(u.cache_creation),0),COALESCE(SUM(u.cache_read),0)
		FROM session_log l LEFT JOIN usage_days u ON u.slug=l.slug AND u.born=l.born
		WHERE l.merged_at>=? OR l.ended_at>=?
		GROUP BY l.slug,l.born ORDER BY CASE WHEN l.merged_at='' THEN l.ended_at ELSE l.merged_at END`, s, s)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRecord
	for rows.Next() {
		var r SessionRecord
		var born, merged, ended string
		if err := rows.Scan(&r.Slug, &born, &r.Repo, &r.RepoID, &r.Branch, &r.PR, &merged, &ended,
			&r.Tokens.Input, &r.Tokens.Output, &r.Tokens.CacheCreation, &r.Tokens.CacheRead); err != nil {
			return nil, err
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339Nano, born)
		r.MergedAt, r.EndedAt = parseOpt(merged), parseOpt(ended)
		out = append(out, r)
	}
	return out, rows.Err()
}

// LifeTokens is the usage of slug's life born so far.
func (d *DB) LifeTokens(slug, born string) (Usage, error) {
	var u Usage
	err := d.db.QueryRow(`SELECT COALESCE(SUM(input),0),COALESCE(SUM(output),0),COALESCE(SUM(cache_creation),0),COALESCE(SUM(cache_read),0)
		FROM usage_days WHERE slug=? AND born=?`, slug, born).Scan(&u.Input, &u.Output, &u.CacheCreation, &u.CacheRead)
	if err == sql.ErrNoRows {
		err = nil
	}
	return u, err
}

func parseOpt(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil
	}
	return &t
}

// Born is the key of s's life in the usage tables.
func Born(s *Session) string { return ts(s.CreatedAt) }
