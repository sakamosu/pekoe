package errcatch

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Report statuses.
const (
	StatusNew     = "new"     // not dealt with: shown as a card / Picks row
	StatusSent    = "sent"    // sent to the agent
	StatusIgnored = "ignored" // dismissed by the human
)

// Sources.
const (
	SourceDev     = "dev"
	SourceBrowser = "browser"
)

// Report is one distinct error of one session, with its repeats folded in.
type Report struct {
	ID          int64     `json:"id"`
	Slug        string    `json:"slug"`
	Source      string    `json:"source"` // dev | browser
	Port        int       `json:"port,omitempty"`
	URL         string    `json:"url,omitempty"`
	Kind        string    `json:"kind"` // detector rule: node | ts | next | vite | go | python | browser
	Message     string    `json:"message"`
	File        string    `json:"file,omitempty"` // top frame, relative to the worktree when it is inside
	Line        int       `json:"line,omitempty"`
	Col         int       `json:"col,omitempty"`
	InTree      bool      `json:"in_tree,omitempty"` // File is the project's own file in the worktree
	Text        string    `json:"text"`              // the block as printed (ANSI stripped), latest occurrence
	Count       int       `json:"count"`
	First       time.Time `json:"first"`
	Last        time.Time `json:"last"`
	Status      string    `json:"status"`
	HandledAt   time.Time `json:"handled_at,omitempty"`
	Fingerprint string    `json:"fingerprint"`
}

// Loc is "file:line" (or "" when the error has no location).
func (r *Report) Loc() string {
	if r.File == "" {
		return ""
	}
	if r.Line > 0 {
		return fmt.Sprintf("%s:%d", r.File, r.Line)
	}
	return r.File
}

// Change says what Add did.
type Change int

const (
	Unchanged Change = iota // folded into a handled report within Settle
	Counted                 // an open report got one more occurrence
	Fresh                   // a new report, or a handled one that recurred
)

// Store folds detected errors per session by fingerprint.
type Store struct {
	// Settle: repeats arriving this soon after a send/ignore are the same
	// burst (HMR prints an error several times) and do not reopen it.
	Settle time.Duration
	// PerSession caps the reports kept for one session.
	PerSession int

	mu    sync.Mutex
	next  int64
	items []*Report
}

func NewStore() *Store { return &Store{Settle: 3 * time.Second, PerSession: 50} }

const maxText = 8 << 10

// Origin is where an error came from.
type Origin struct {
	Source string // dev | browser
	Port   int
	URL    string
}

// FingerprintIn is the fingerprint of b as Add computes it for a session
// whose worktree is root.
func FingerprintIn(root string, b Block) string {
	pickTop(root, &b)
	return Fingerprint(b)
}

// Add records one occurrence. root is the session's worktree: the top frame
// is moved to the first frame inside it (the project's own code), which is
// also what the fingerprint uses.
func (s *Store) Add(slug string, o Origin, root string, b Block, at time.Time) (Report, Change) {
	file, line, col, inTree := "", 0, 0, false
	if top := pickTop(root, &b); top >= 0 {
		f := b.Frames[top]
		file, line, col = f.File, f.Line, f.Col
		if rel, ok := Resolve(root, f); ok {
			file, inTree = rel, true
		}
	}
	fp := Fingerprint(b)
	text := strings.Join(b.Lines, "\n")
	if len(text) > maxText {
		text = text[:maxText] + "\n…"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.items {
		if r.Slug != slug || r.Fingerprint != fp {
			continue
		}
		switch {
		case r.Status == StatusNew:
			r.Count++
			r.Last, r.Text, r.URL = at, text, o.URL
			r.File, r.Line, r.Col, r.InTree = file, line, col, inTree
			return *r, Counted
		case at.Sub(r.HandledAt) < s.Settle:
			r.Count++
			r.Last = at
			return *r, Unchanged
		default: // recurred after being sent or ignored
			r.Status, r.Count, r.First, r.Last, r.Text = StatusNew, 1, at, at, text
			r.File, r.Line, r.Col, r.InTree = file, line, col, inTree
			r.HandledAt = time.Time{}
			return *r, Fresh
		}
	}
	s.next++
	r := &Report{
		ID: s.next, Slug: slug, Source: o.Source, Port: o.Port, URL: o.URL, Kind: b.Kind,
		Message: b.Message, File: file, Line: line, Col: col, InTree: inTree,
		Text: text, Count: 1, First: at, Last: at, Status: StatusNew, Fingerprint: fp,
	}
	s.items = append(s.items, r)
	s.trimLocked(slug)
	return *r, Fresh
}

// pickTop prefers the first project frame (the last one for rules that
// print the most recent call last) and records it as b.Top.
func pickTop(root string, b *Block) int {
	if len(b.Frames) == 0 {
		b.Top = -1
		return -1
	}
	order := make([]int, len(b.Frames))
	for i := range order {
		order[i] = i
	}
	if b.LastFrame {
		for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
			order[i], order[j] = order[j], order[i]
		}
	}
	for _, i := range order {
		if _, ok := Resolve(root, b.Frames[i]); ok {
			b.Top = i
			return i
		}
	}
	if b.Top < 0 {
		b.Top = 0
	}
	return b.Top
}

func (s *Store) trimLocked(slug string) {
	var mine []*Report
	for _, r := range s.items {
		if r.Slug == slug {
			mine = append(mine, r)
		}
	}
	over := len(mine) - s.PerSession
	if s.PerSession <= 0 || over <= 0 {
		return
	}
	// drop handled ones first, oldest first
	sort.SliceStable(mine, func(i, j int) bool {
		hi, hj := mine[i].Status != StatusNew, mine[j].Status != StatusNew
		if hi != hj {
			return hi
		}
		return mine[i].Last.Before(mine[j].Last)
	})
	drop := map[*Report]bool{}
	for _, r := range mine[:over] {
		drop[r] = true
	}
	kept := s.items[:0]
	for _, r := range s.items {
		if !drop[r] {
			kept = append(kept, r)
		}
	}
	s.items = kept
}

// List returns the reports of slug ("" = every session), oldest first. Only
// open ones unless all is set.
func (s *Store) List(slug string, all bool) []Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Report{}
	for _, r := range s.items {
		if (slug == "" || r.Slug == slug) && (all || r.Status == StatusNew) {
			out = append(out, *r)
		}
	}
	return out
}

func (s *Store) Get(id int64) (Report, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.items {
		if r.ID == id {
			return *r, true
		}
	}
	return Report{}, false
}

// Mark sets a report's status (sent / ignored).
func (s *Store) Mark(id int64, status string, at time.Time) (Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.items {
		if r.ID == id {
			r.Status, r.HandledAt = status, at
			return *r, nil
		}
	}
	return Report{}, fmt.Errorf("no such error: #%d", id)
}

// MarkWhere marks every open report matching f and returns them.
func (s *Store) MarkWhere(f func(*Report) bool, status string, at time.Time) []Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Report
	for _, r := range s.items {
		if r.Status == StatusNew && f(r) {
			r.Status, r.HandledAt = status, at
			out = append(out, *r)
		}
	}
	return out
}

// Forget drops everything of a session (it was removed).
func (s *Store) Forget(slug string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.items[:0]
	for _, r := range s.items {
		if r.Slug != slug {
			kept = append(kept, r)
		}
	}
	s.items = kept
}
