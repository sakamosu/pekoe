package state

// The orchestration records (docs/orchestration.md): reports the sessions
// post (report.post) and the log of text pekoed passed between callers
// (session.new prompts, session.send).

import (
	"encoding/json"
	"strings"
	"time"
)

// Report is what a session said about its work (report.post).
type Report struct {
	ID int64 `json:"id"`
	// Slug is the session the report is about; By is the caller pekoed
	// identified (the same session, for now), Claimed its $PEKOE_SESSION
	// when that said something else.
	Slug     string   `json:"slug"`
	By       string   `json:"by"`
	Claimed  string   `json:"claimed,omitempty"`
	Status   string   `json:"status"` // done | blocked | progress | review
	Summary  string   `json:"summary"`
	Evidence []string `json:"evidence"`
	// Head and Dirty are the worktree's HEAD and whether it had
	// uncommitted changes when the report came in (read by pekoed).
	Head        string    `json:"head,omitempty"`
	Dirty       bool      `json:"dirty"`
	Verdict     string    `json:"verdict,omitempty"` // pass | fail (status review)
	ReviewedSHA string    `json:"reviewed_sha,omitempty"`
	At          time.Time `json:"at"`
}

// ReportSummary is Session.report: the latest report, its summary cut to
// its first line.
type ReportSummary struct {
	ID      int64     `json:"id"`
	Status  string    `json:"status"`
	Summary string    `json:"summary"`
	At      time.Time `json:"at"`
	Head    string    `json:"head,omitempty"`
}

// Brief is r as Session.report.
func (r *Report) Brief() *ReportSummary {
	line, _, _ := strings.Cut(strings.TrimSpace(r.Summary), "\n")
	if rs := []rune(line); len(rs) > 200 {
		line = string(rs[:200]) + "…"
	}
	return &ReportSummary{ID: r.ID, Status: r.Status, Summary: line, At: r.At, Head: r.Head}
}

// AddReport stores r and fills in its id.
func (d *DB) AddReport(r *Report) error {
	ev, _ := json.Marshal(r.Evidence)
	if r.Evidence == nil {
		ev = []byte("[]")
	}
	dirty := 0
	if r.Dirty {
		dirty = 1
	}
	res, err := d.db.Exec(`INSERT INTO reports(slug,by,claimed,status,summary,evidence,head,dirty,verdict,reviewed_sha,at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		r.Slug, r.By, r.Claimed, r.Status, r.Summary, string(ev), r.Head, dirty, r.Verdict, r.ReviewedSHA, r.At.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	r.ID, err = res.LastInsertId()
	return err
}

// Reports lists reports newest first: of slug ("" every session), with an
// id above since, at most limit.
func (d *DB) Reports(slug string, since int64, limit int) ([]Report, error) {
	q := `SELECT id,slug,by,claimed,status,summary,evidence,head,dirty,verdict,reviewed_sha,at FROM reports WHERE id>?`
	args := []any{since}
	if slug != "" {
		q += ` AND slug=?`
		args = append(args, slug)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Report{}
	for rows.Next() {
		var r Report
		var ev, at string
		var dirty int
		if err := rows.Scan(&r.ID, &r.Slug, &r.By, &r.Claimed, &r.Status, &r.Summary, &ev, &r.Head, &dirty, &r.Verdict, &r.ReviewedSHA, &at); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(ev), &r.Evidence)
		if r.Evidence == nil {
			r.Evidence = []string{}
		}
		r.Dirty = dirty != 0
		r.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SendRecord is one text pekoed passed on: a session.new prompt (kind
// new) or a session.send (kind send), from the caller to a session.
type SendRecord struct {
	ID    int64     `json:"id"`
	At    time.Time `json:"at"`
	From  string    `json:"from"`
	To    string    `json:"to"`
	Kind  string    `json:"kind"`
	Text  string    `json:"text"`
	Via   string    `json:"via"` // socket | web
	OK    bool      `json:"ok"`
	Error string    `json:"error,omitempty"`
}

// SendFilter narrows Sends. Party keeps the records it is the sender or
// the recipient of (a caller without the human token sees only those).
type SendFilter struct {
	From, To, Party string
	Since           int64
	Limit           int
}

func (d *DB) AddSend(r *SendRecord) error {
	ok := 0
	if r.OK {
		ok = 1
	}
	res, err := d.db.Exec(`INSERT INTO sends(at,from_,to_,kind,text,via,ok,error) VALUES(?,?,?,?,?,?,?,?)`,
		r.At.UTC().Format(time.RFC3339Nano), r.From, r.To, r.Kind, r.Text, r.Via, ok, r.Error)
	if err != nil {
		return err
	}
	r.ID, err = res.LastInsertId()
	return err
}

// Sends lists send records newest first.
func (d *DB) Sends(f SendFilter) ([]SendRecord, error) {
	q := `SELECT id,at,from_,to_,kind,text,via,ok,error FROM sends WHERE id>?`
	args := []any{f.Since}
	if f.From != "" {
		q += ` AND from_=?`
		args = append(args, f.From)
	}
	if f.To != "" {
		q += ` AND to_=?`
		args = append(args, f.To)
	}
	if f.Party != "" {
		q += ` AND (from_=? OR to_=?)`
		args = append(args, f.Party, f.Party)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SendRecord{}
	for rows.Next() {
		var r SendRecord
		var at string
		var ok int
		if err := rows.Scan(&r.ID, &at, &r.From, &r.To, &r.Kind, &r.Text, &r.Via, &ok, &r.Error); err != nil {
			return nil, err
		}
		r.At, _ = time.Parse(time.RFC3339Nano, at)
		r.OK = ok != 0
		out = append(out, r)
	}
	return out, rows.Err()
}
