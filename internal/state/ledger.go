package state

// The ledger (docs/orchestration.md "6. 依頼(ledger)"): what a person asked
// tapi chat for, followed until it is done. Every prompt a person types
// into tapi is recorded open (tapi closes the small talk as chat); the
// entries outlive tapi's conversation (tapi.reset does not touch them), so
// a fresh tapi reads what is left. Only a person reopens or deletes one.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Ledger entry statuses.
const (
	LedgerOpen    = "open"
	LedgerDone    = "done"
	LedgerDropped = "dropped" // decided not to do it
	LedgerChat    = "chat"    // not a request (small talk, thanks, a question answered on the spot)
)

// LedgerEntry is one request.
type LedgerEntry struct {
	ID       int64      `json:"id"`
	At       time.Time  `json:"at"`
	Text     string     `json:"text"` // what the person typed
	Status   string     `json:"status"`
	Sessions []string   `json:"sessions"` // the sessions working on it (--request, ledger.link)
	Evidence []string   `json:"evidence"`
	Note     string     `json:"note,omitempty"`
	ClosedAt *time.Time `json:"closed_at,omitempty"`
}

// ErrNoLedgerEntry is returned for an id that is not in the ledger.
var ErrNoLedgerEntry = errors.New("no such ledger entry")

const ledgerCols = `id,at,text,status,sessions,evidence,note,closed_at`

// AddLedger stores e (its At, Text and Status) and fills in its id.
func (d *DB) AddLedger(e *LedgerEntry) error {
	if e.Sessions == nil {
		e.Sessions = []string{}
	}
	if e.Evidence == nil {
		e.Evidence = []string{}
	}
	res, err := d.db.Exec(`INSERT INTO ledger(at,text,status,sessions,evidence,note,closed_at) VALUES(?,?,?,?,?,?,?)`,
		ts(e.At), e.Text, e.Status, jsonText(e.Sessions), jsonText(e.Evidence), e.Note, optTime(e.ClosedAt))
	if err != nil {
		return err
	}
	e.ID, err = res.LastInsertId()
	return err
}

// SaveLedger writes back everything but the id, at and text.
func (d *DB) SaveLedger(e *LedgerEntry) error {
	res, err := d.db.Exec(`UPDATE ledger SET status=?,sessions=?,evidence=?,note=?,closed_at=? WHERE id=?`,
		e.Status, jsonText(e.Sessions), jsonText(e.Evidence), e.Note, optTime(e.ClosedAt), e.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoLedgerEntry
	}
	return nil
}

// DeleteLedger removes one entry.
func (d *DB) DeleteLedger(id int64) error {
	res, err := d.db.Exec(`DELETE FROM ledger WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoLedgerEntry
	}
	return nil
}

// LedgerGet reads one entry.
func (d *DB) LedgerGet(id int64) (*LedgerEntry, error) {
	es, err := d.ledgerQuery(`SELECT `+ledgerCols+` FROM ledger WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	if len(es) == 0 {
		return nil, ErrNoLedgerEntry
	}
	return &es[0], nil
}

// LedgerList lists entries oldest first (the order they were asked in):
// open only unless all, with an id above since.
func (d *DB) LedgerList(all bool, since int64) ([]LedgerEntry, error) {
	q := `SELECT ` + ledgerCols + ` FROM ledger WHERE id>?`
	args := []any{since}
	if !all {
		q += ` AND status=?`
		args = append(args, LedgerOpen)
	}
	return d.ledgerQuery(q+` ORDER BY id`, args...)
}

// LedgerOpenCount is how many entries are open.
func (d *DB) LedgerOpenCount() (int, error) {
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM ledger WHERE status=?`, LedgerOpen).Scan(&n)
	return n, err
}

// InboxOpenTotal is how many inbox items are open, from anyone.
func (d *DB) InboxOpenTotal() (int, error) {
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM inbox WHERE status=?`, InboxOpen).Scan(&n)
	return n, err
}

func optTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return ts(*t)
}

func (d *DB) ledgerQuery(q string, args ...any) ([]LedgerEntry, error) {
	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LedgerEntry{}
	for rows.Next() {
		var e LedgerEntry
		var at, sessions, evidence string
		var closed sql.NullString
		if err := rows.Scan(&e.ID, &at, &e.Text, &e.Status, &sessions, &evidence, &e.Note, &closed); err != nil {
			return nil, err
		}
		e.At, _ = time.Parse(time.RFC3339Nano, at)
		_ = json.Unmarshal([]byte(sessions), &e.Sessions)
		_ = json.Unmarshal([]byte(evidence), &e.Evidence)
		if e.Sessions == nil {
			e.Sessions = []string{}
		}
		if e.Evidence == nil {
			e.Evidence = []string{}
		}
		if closed.Valid && closed.String != "" {
			if t, err := time.Parse(time.RFC3339Nano, closed.String); err == nil {
				e.ClosedAt = &t
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
