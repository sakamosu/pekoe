package state

// The inbox (docs/orchestration.md "4. 判断の受信箱"): what agents ask a
// person to decide, approve or do, with the facts pekoed read itself and,
// once answered, the person's answer. Answered, withdrawn and dismissed
// items stay (the hub agent reads them as precedent); `pekoe rm` keeps
// them too.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Inbox item statuses.
const (
	InboxOpen      = "open"
	InboxAnswered  = "answered"
	InboxWithdrawn = "withdrawn"
	InboxDismissed = "dismissed"
)

// Delivery of an answer to the agent that asked.
const (
	DeliveryPending   = "pending"   // to be typed into the asker's input box when it is free
	DeliveryDelivered = "delivered" // typed into it
	DeliveryWaited    = "waited"    // the asker got it from wait / inbox.get
	DeliveryNone      = "none"      // nothing to deliver (open, fyi, a person or an unknown caller asked)
)

// InboxItem is one question in the inbox. Indexes (Recommend, Answer.Choice)
// count from 0; the CLI and the screens show them from 1.
type InboxItem struct {
	ID int64 `json:"id"`
	// Rev goes up whenever the item changes (its facts included): an answer
	// carries the rev the person looked at.
	Rev     int64  `json:"rev"`
	Kind    string `json:"kind"`              // decision | approve | login | fyi
	From    string `json:"from"`              // the caller pekoed identified
	Claimed string `json:"claimed,omitempty"` // its $PEKOE_SESSION, when that said something else
	About   string `json:"about,omitempty"`   // the session the question is about (facts)

	Question  string   `json:"question"`
	Options   []string `json:"options"`
	Recommend *int     `json:"recommend,omitempty"` // the agent's opinion, never a default
	Context   string   `json:"context,omitempty"`
	Links     []string `json:"links"`

	Facts *InboxFacts `json:"facts,omitempty"`

	Status    string       `json:"status"`
	Answer    *InboxAnswer `json:"answer,omitempty"`
	Delivery  string       `json:"delivery"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
}

// InboxAnswer is how an item was closed: a person's answer or dismissal,
// the asker's withdrawal, or (fyi) what the agent decided itself.
type InboxAnswer struct {
	Choice *int      `json:"choice,omitempty"`
	Note   string    `json:"note,omitempty"`
	By     string    `json:"by"`            // human, or the asker (withdraw, fyi)
	Via    string    `json:"via,omitempty"` // socket | web:<device>
	At     time.Time `json:"at"`
}

// InboxFacts is what pekoed read itself about the session an item is
// about; the agent cannot write any of it.
type InboxFacts struct {
	Slug       string         `json:"slug"`
	State      State          `json:"state"`
	Branch     string         `json:"branch,omitempty"`
	Base       string         `json:"base,omitempty"`
	Head       string         `json:"head,omitempty"`
	Diffstat   Stat           `json:"diffstat"`
	PR         int            `json:"pr,omitempty"`
	PRURL      string         `json:"pr_url,omitempty"`
	PRStatus   *InboxPR       `json:"pr_status,omitempty"`
	Test       Test           `json:"test"`
	Overlaps   []Overlap      `json:"overlaps,omitempty"`
	Report     *InboxReport   `json:"report,omitempty"`
	GitBlocked string         `json:"git_blocked,omitempty"`
	Gone       bool           `json:"gone,omitempty"` // the session no longer exists
}

// InboxReport is the session's latest report without its words (the
// summary is the agent's, not a fact).
type InboxReport struct {
	ID     int64     `json:"id"`
	Status string    `json:"status"`
	At     time.Time `json:"at"`
	Head   string    `json:"head,omitempty"`
}

// InboxPR is the part of PRStatus the inbox shows.
type InboxPR struct {
	State          string `json:"state"`
	Draft          bool   `json:"draft,omitempty"`
	Pass           int    `json:"pass"`
	Fail           int    `json:"fail"`
	Pending        int    `json:"pending"`
	ReviewDecision string `json:"review_decision,omitempty"`
	Unresolved     int    `json:"unresolved"`
	MergeState     string `json:"merge_state,omitempty"`
}

// Same reports whether two sets of facts say the same thing.
func (f *InboxFacts) Same(g *InboxFacts) bool {
	a, _ := json.Marshal(f)
	b, _ := json.Marshal(g)
	return string(a) == string(b)
}

// ErrNoInboxItem is returned for an id that is not in the inbox.
var ErrNoInboxItem = errors.New("no such inbox item")

const inboxCols = `id,rev,kind,from_,claimed,about,question,options,recommend,context,links,facts,status,answer,delivery,created_at,updated_at`

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func optInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// AddInbox stores it and fills in its id.
func (d *DB) AddInbox(it *InboxItem) error {
	if it.Options == nil {
		it.Options = []string{}
	}
	if it.Links == nil {
		it.Links = []string{}
	}
	var ans any
	if it.Answer != nil {
		ans = jsonText(it.Answer)
	}
	var facts any
	if it.Facts != nil {
		facts = jsonText(it.Facts)
	}
	res, err := d.db.Exec(`INSERT INTO inbox(rev,kind,from_,claimed,about,question,options,recommend,context,links,facts,status,answer,delivery,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		it.Rev, it.Kind, it.From, it.Claimed, it.About, it.Question, jsonText(it.Options), optInt(it.Recommend), it.Context, jsonText(it.Links), facts, it.Status, ans, it.Delivery, ts(it.CreatedAt), ts(it.UpdatedAt))
	if err != nil {
		return err
	}
	it.ID, err = res.LastInsertId()
	return err
}

// SaveInbox writes back what can change after an item is made: rev,
// facts, status, answer, delivery and updated_at.
func (d *DB) SaveInbox(it *InboxItem) error {
	var ans any
	if it.Answer != nil {
		ans = jsonText(it.Answer)
	}
	var facts any
	if it.Facts != nil {
		facts = jsonText(it.Facts)
	}
	_, err := d.db.Exec(`UPDATE inbox SET rev=?,facts=?,status=?,answer=?,delivery=?,updated_at=? WHERE id=?`,
		it.Rev, facts, it.Status, ans, it.Delivery, ts(it.UpdatedAt), it.ID)
	return err
}

// SetInboxDelivery moves an item's delivery from one value to another
// and reports whether it was still from (so two deliveries cannot both
// take it).
func (d *DB) SetInboxDelivery(id int64, from, to string) (bool, error) {
	res, err := d.db.Exec(`UPDATE inbox SET delivery=? WHERE id=? AND delivery=?`, to, id, from)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// InboxGet reads one item.
func (d *DB) InboxGet(id int64) (*InboxItem, error) {
	its, err := d.inboxQuery(`SELECT `+inboxCols+` FROM inbox WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	if len(its) == 0 {
		return nil, ErrNoInboxItem
	}
	return &its[0], nil
}

// InboxFilter narrows InboxList. Status "" is every status; From keeps
// one caller's items; Delivery one delivery state.
type InboxFilter struct {
	Status   []string
	From     string
	Delivery string
	Limit    int
}

// InboxList lists items newest first.
func (d *DB) InboxList(f InboxFilter) ([]InboxItem, error) {
	q := `SELECT ` + inboxCols + ` FROM inbox WHERE 1=1`
	var args []any
	if len(f.Status) > 0 {
		q += ` AND status IN (?` + repeatComma(len(f.Status)-1) + `)`
		for _, s := range f.Status {
			args = append(args, s)
		}
	}
	if f.From != "" {
		q += ` AND from_=?`
		args = append(args, f.From)
	}
	if f.Delivery != "" {
		q += ` AND delivery=?`
		args = append(args, f.Delivery)
	}
	q += ` ORDER BY id DESC`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	return d.inboxQuery(q, args...)
}

func repeatComma(n int) string {
	s := ""
	for i := 0; i < n; i++ {
		s += ",?"
	}
	return s
}

// InboxOpenCount is how many open items from has.
func (d *DB) InboxOpenCount(from string) (int, error) {
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM inbox WHERE from_=? AND status=?`, from, InboxOpen).Scan(&n)
	return n, err
}

func (d *DB) inboxQuery(q string, args ...any) ([]InboxItem, error) {
	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InboxItem{}
	for rows.Next() {
		var it InboxItem
		var opts, links, created, updated string
		var rec sql.NullInt64
		var facts, ans sql.NullString
		if err := rows.Scan(&it.ID, &it.Rev, &it.Kind, &it.From, &it.Claimed, &it.About, &it.Question, &opts, &rec, &it.Context, &links, &facts, &it.Status, &ans, &it.Delivery, &created, &updated); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(opts), &it.Options)
		_ = json.Unmarshal([]byte(links), &it.Links)
		if it.Options == nil {
			it.Options = []string{}
		}
		if it.Links == nil {
			it.Links = []string{}
		}
		if rec.Valid {
			n := int(rec.Int64)
			it.Recommend = &n
		}
		if facts.Valid && facts.String != "" {
			it.Facts = &InboxFacts{}
			_ = json.Unmarshal([]byte(facts.String), it.Facts)
		}
		if ans.Valid && ans.String != "" {
			it.Answer = &InboxAnswer{}
			_ = json.Unmarshal([]byte(ans.String), it.Answer)
		}
		it.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		it.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		out = append(out, it)
	}
	return out, rows.Err()
}
