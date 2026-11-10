package daemon

// stats (docs/orchestration.md "4. PR の集計"): how many PRs merged and how
// many tokens the agents used, by local date and repository, kept after
// the sessions are removed (state/usage.go).
//
// Tokens are summed from the Claude Code transcripts the hooks name
// (transcript_path), read on from where the last count stopped, with the
// subagents' transcripts next to them (<transcript>/subagents/*.jsonl). A
// session's lines are counted under the local date of their timestamp, so
// a session that runs past midnight shows on both days. Merges are what
// pekoe saw land (finishLanding); it does not ask GitHub about older ones.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"pekoe/internal/state"
)

// usageChunk is the most read from one transcript in one count; the rest
// waits for the next count.
const usageChunk = 32 << 20

// usageLife names the life whose usage a count adds to.
type usageLife struct {
	Slug, Born, Repo string
}

// lifeOf is the life of a live session, or of tapi chat (born "", repo "":
// tapi's usage is kept under +tapi across resets). ok is false for a slug
// pekoe does not have.
func (d *Daemon) lifeOf(slug string) (usageLife, bool) {
	if slug == TapiSlug {
		return usageLife{Slug: TapiSlug}, true
	}
	s, err := d.snapshot(slug)
	if err != nil {
		return usageLife{}, false
	}
	return usageLife{Slug: s.Slug, Born: state.Born(s), Repo: s.Repo}, true
}

// noteTranscript counts what is new in the transcript a hook named (and
// the others of the same life). Hooks fire often; each count reads only
// the lines added since the last.
func (d *Daemon) noteTranscript(slug, path string) {
	life, ok := d.lifeOf(slug)
	if !ok {
		return
	}
	d.countUsage(life, path, true)
}

// countUsage brings life's usage up to date: path (when a hook just named
// it), every transcript already known for the life, and the subagent
// transcripts beside them. One count at a time (usageMu): two counts of
// the same lines would add them twice. live: count only while the life is
// still pekoe's session (not removed, not replaced by a new session of the
// slug); after recordSession forgot its cursors, a late hook would count
// the transcript again from the start.
func (d *Daemon) countUsage(life usageLife, path string, live bool) {
	d.usageMu.Lock()
	defer d.usageMu.Unlock()
	if live && life.Slug != TapiSlug {
		s, err := d.snapshot(life.Slug)
		if err != nil || state.Born(s) != life.Born {
			return
		}
	}
	cs, err := d.DB.UsageCursors(life.Slug, life.Born)
	if err != nil {
		log.Printf("usage %s: %v", life.Slug, err)
		return
	}
	known := map[string]state.UsageCursor{}
	for _, c := range cs {
		known[c.Path] = c
	}
	if path != "" && strings.HasSuffix(path, ".jsonl") {
		if _, ok := known[path]; !ok {
			known[path] = state.UsageCursor{Slug: life.Slug, Born: life.Born, Path: path}
		}
	}
	for p := range known {
		if strings.Contains(p, string(filepath.Separator)+"subagents"+string(filepath.Separator)) {
			continue
		}
		subs, _ := filepath.Glob(filepath.Join(strings.TrimSuffix(p, ".jsonl"), "subagents", "*.jsonl"))
		for _, sp := range subs {
			if _, ok := known[sp]; !ok {
				known[sp] = state.UsageCursor{Slug: life.Slug, Born: life.Born, Path: sp}
			}
		}
	}
	paths := make([]string, 0, len(known))
	for p := range known {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		c := known[p]
		byDay, next, changed := readUsage(c)
		if !changed {
			continue
		}
		if err := d.DB.AddUsage(next, life.Repo, byDay); err != nil {
			log.Printf("usage %s %s: %v", life.Slug, p, err)
		}
	}
}

// readUsage reads c's transcript from c.Offset: the usage of each new
// assistant message by the local date of its line, and the cursor after
// the last whole line read. changed is false when there is nothing new
// (or nothing readable). The path comes from a hook payload, which any
// process of the user can send, so only a regular file is read (tailFile),
// at most usageChunk bytes at a time.
func readUsage(c state.UsageCursor) (byDay map[string]state.Usage, next state.UsageCursor, changed bool) {
	next = c
	f, err := os.OpenFile(c.Path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, next, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, next, false
	}
	if fi.Size() < c.Offset {
		// not the file that was read (replaced by a shorter one): count
		// from what is there now on, never the same bytes twice
		next.Offset = fi.Size()
		return nil, next, true
	}
	if fi.Size() == c.Offset {
		return nil, next, false
	}
	if _, err := f.Seek(c.Offset, io.SeekStart); err != nil {
		return nil, next, false
	}
	b, _ := io.ReadAll(io.LimitReader(f, usageChunk))
	end := bytes.LastIndexByte(b, '\n')
	if end < 0 {
		if len(b) < usageChunk {
			return nil, next, false // a line still being written
		}
		next.Offset += int64(len(b)) // one line longer than a chunk: no usage record is
		return nil, next, true
	}
	b = b[:end+1]
	next.Offset += int64(len(b))
	byDay = map[string]state.Usage{}
	for _, line := range bytes.Split(b, []byte("\n")) {
		if len(line) == 0 || !bytes.Contains(line, []byte(`"usage"`)) {
			continue
		}
		var e struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			Message   struct {
				ID    string `json:"id"`
				Usage *struct {
					In     int64 `json:"input_tokens"`
					Out    int64 `json:"output_tokens"`
					CacheC int64 `json:"cache_creation_input_tokens"`
					CacheR int64 `json:"cache_read_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &e) != nil || e.Type != "assistant" || e.Message.Usage == nil {
			continue
		}
		// one message, several lines (one per content block), the same usage
		if e.Message.ID != "" {
			if e.Message.ID == next.LastID {
				continue
			}
			next.LastID = e.Message.ID
		}
		u := e.Message.Usage
		day := localDay(e.Timestamp)
		x := byDay[day]
		x.Add(state.Usage{Input: u.In, Output: u.Out, CacheCreation: u.CacheC, CacheRead: u.CacheR})
		byDay[day] = x
	}
	return byDay, next, true
}

// localDay is the local date of a transcript timestamp (today when it has
// none).
func localDay(stamp string) string {
	t, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		t = time.Now()
	}
	return t.Local().Format("2006-01-02")
}

// recordSession keeps what stats needs of s when it lands (merged set) or
// is removed: its last usage is counted first, then the record is written.
// removed also forgets where its transcripts were read: nothing counts for
// that life any more.
func (d *Daemon) recordSession(s *state.Session, merged *time.Time, removed bool) {
	if s == nil || s.Slug == "" {
		return
	}
	life := usageLife{Slug: s.Slug, Born: state.Born(s), Repo: s.Repo}
	d.countUsage(life, "", false)
	if s.PRReview { // someone else's PR under review: its tokens count, no record of a merge
		if removed {
			d.usageMu.Lock()
			_ = d.DB.DeleteUsageCursors(life.Slug, life.Born)
			d.usageMu.Unlock()
		}
		return
	}
	now := time.Now()
	r := state.SessionRecord{Slug: s.Slug, Repo: s.Repo, RepoID: s.RepoID, Branch: s.Branch, PR: s.PR,
		CreatedAt: s.CreatedAt, MergedAt: merged, EndedAt: &now}
	if err := d.DB.RecordSession(r); err != nil {
		log.Printf("record %s: %v", s.Slug, err)
	}
	if removed {
		d.usageMu.Lock()
		_ = d.DB.DeleteUsageCursors(life.Slug, life.Born)
		d.usageMu.Unlock()
	}
}

type statsP struct {
	Days int    `json:"days"` // today and the days before it; 0: 7
	Repo string `json:"repo"` // repository name or id; "": all (tapi chat included)
}

// StatsRow is one local date of one repository ("" is tapi chat).
type StatsRow struct {
	Day    string      `json:"day"`
	Repo   string      `json:"repo"`
	Merged int         `json:"merged"`
	Tokens state.Usage `json:"tokens"`
}

// StatsMerge is one merge in the range, with what its session used in all.
type StatsMerge struct {
	Day      string      `json:"day"`
	Slug     string      `json:"slug"`
	Repo     string      `json:"repo"`
	PR       int         `json:"pr,omitempty"`
	MergedAt time.Time   `json:"merged_at"`
	Tokens   state.Usage `json:"tokens"`
}

// Stats is stats' answer.
type Stats struct {
	From   string       `json:"from"` // first local date counted
	To     string       `json:"to"`   // today
	Rows   []StatsRow   `json:"rows"` // newest day first, then by repository
	Merges []StatsMerge `json:"merges"`
	Merged int          `json:"merged"`
	Tokens state.Usage  `json:"tokens"`
}

const statsMaxDays = 3660

// hStats counts what is new in the live sessions' transcripts (so their
// usage so far is in), then sums the days.
func (d *Daemon) hStats(p statsP) (any, error) {
	if p.Days == 0 {
		p.Days = 7
	}
	if p.Days < 0 || p.Days > statsMaxDays {
		return nil, fmt.Errorf("stats: days must be 1 to %d (got %d)", statsMaxDays, p.Days)
	}
	d.mu.Lock()
	lives := []usageLife{{Slug: TapiSlug}}
	for _, s := range d.sessions {
		lives = append(lives, usageLife{Slug: s.Slug, Born: state.Born(s), Repo: s.Repo})
	}
	d.mu.Unlock()
	for _, l := range lives {
		d.countUsage(l, "", true)
	}
	return d.stats(time.Now(), p.Days, p.Repo)
}

func (d *Daemon) stats(now time.Time, days int, repo string) (*Stats, error) {
	today := now.Local()
	y, m, dd := today.Date()
	start := time.Date(y, m, dd-(days-1), 0, 0, 0, 0, time.Local)
	from := start.Format("2006-01-02")
	match := func(name, id string) bool {
		return repo == "" || name == repo || (id != "" && id == repo)
	}
	st := &Stats{From: from, To: today.Format("2006-01-02"), Rows: []StatsRow{}, Merges: []StatsMerge{}}
	rows := map[[2]string]*StatsRow{}
	row := func(day, repo string) *StatsRow {
		k := [2]string{day, repo}
		if rows[k] == nil {
			rows[k] = &StatsRow{Day: day, Repo: repo}
		}
		return rows[k]
	}
	uds, err := d.DB.UsageDays(from)
	if err != nil {
		return nil, err
	}
	for _, u := range uds {
		if !match(u.Repo, "") {
			continue
		}
		row(u.Day, u.Repo).Tokens.Add(u.Usage)
		st.Tokens.Add(u.Usage)
	}
	recs, err := d.DB.SessionRecords(start)
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		if r.MergedAt == nil || r.MergedAt.Before(start) || !match(r.Repo, r.RepoID) {
			continue
		}
		day := r.MergedAt.Local().Format("2006-01-02")
		row(day, r.Repo).Merged++
		st.Merged++
		st.Merges = append(st.Merges, StatsMerge{Day: day, Slug: r.Slug, Repo: r.Repo, PR: r.PR, MergedAt: *r.MergedAt, Tokens: r.Tokens})
	}
	for _, r := range rows {
		st.Rows = append(st.Rows, *r)
	}
	sort.Slice(st.Rows, func(i, j int) bool {
		if st.Rows[i].Day != st.Rows[j].Day {
			return st.Rows[i].Day > st.Rows[j].Day
		}
		return st.Rows[i].Repo < st.Rows[j].Repo
	})
	return st, nil
}
