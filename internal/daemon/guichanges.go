package daemon

// The local GUI's Changes view (docs/gui.md 「段階 2 の一部: Changes」): the
// files a session changed since its base, with the lines added and
// deleted and the sessions that touch the same file (the TUI's [3]
// Changes), and one file's diff to read. Nothing here writes: no comment,
// no approval (決まったこと 7, the review surface stays the TUI's).
//
//   - changes (review.go): the list, as the TUI reads it
//   - changes.diff: one changed file's diff, cut at diffMaxLines lines or
//     diffMaxBytes bytes of text, each line at diffLineMax bytes; read as
//     git prints it, keeping only that (git is stopped after diffReadMax
//     bytes). Only a path the list has: a pathspec is never built from
//     what the page sent beyond that one literal name
//
// Both are a person's (human.go). The text is the agent's and the
// repository's: the page draws it as text only.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"pekoe/internal/gitx"
	"pekoe/internal/state"
)

const (
	diffMaxLines = 3000      // lines of a file's diff the view shows (context included)
	diffMaxBytes = 256 << 10 // bytes of their text
	diffLineMax  = 2000      // bytes of one line (a minified file); the rest is cut
	diffPathMax  = 4096      // bytes of the path the page asks for
	diffTimeout  = 15 * time.Second
	diffReadMax  = 32 << 20 // bytes of git's output read for one file; then git is stopped
)

var errNotChanged = errors.New("not a changed file of this session")

type diffP struct {
	Slug string `json:"slug"`
	Path string `json:"path"`
}

type diffLine struct {
	Kind string `json:"kind"` // " ", "+", "-"
	Old  int    `json:"old,omitempty"`
	New  int    `json:"new,omitempty"`
	Text string `json:"text"`
	Cut  bool   `json:"cut,omitempty"` // longer than diffLineMax: the rest is not here
}

type diffHunk struct {
	Header string     `json:"header"`
	Lines  []diffLine `json:"lines"`
}

// FileDiffView is changes.diff's answer.
type FileDiffView struct {
	Slug      string     `json:"slug"`
	Path      string     `json:"path"`
	Status    string     `json:"status"`
	Binary    bool       `json:"binary,omitempty"`
	Added     int        `json:"added"`
	Deleted   int        `json:"deleted"`
	Hunks     []diffHunk `json:"hunks"`
	Lines     int        `json:"lines"`               // lines in hunks
	Total     int        `json:"total"`               // lines of the whole diff
	Truncated bool       `json:"truncated,omitempty"` // Lines < Total
}

func (d *Daemon) hChangesDiff(p diffP) (any, error) {
	if p.Path == "" || len(p.Path) > diffPathMax {
		return nil, errNotChanged
	}
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	base, tree, list, err := d.changeList(s)
	if err != nil {
		return nil, err
	}
	ce := changedFile(list, p.Path)
	if ce == nil {
		return nil, errNotChanged
	}
	out := FileDiffView{Slug: s.Slug, Path: ce.Path, Status: ce.Status, Binary: ce.Binary, Added: ce.Added, Deleted: ce.Deleted, Hunks: []diffHunk{}}
	if ce.Binary {
		return out, nil
	}
	gctx, cancel := context.WithTimeout(context.Background(), diffTimeout)
	defer cancel()
	// read as git prints it, keeping only what is sent: the lines past the
	// budget are counted, and after diffReadMax bytes git is stopped
	size, total := 0, 0
	var files []gitx.FileDiff
	var full bool
	keep := func(l gitx.Line) bool {
		total++
		if out.Truncated || out.Lines >= diffMaxLines || size+len(l.Text) > diffMaxBytes {
			out.Truncated = true
			return false
		}
		size += len(l.Text)
		out.Lines++
		return true
	}
	err = gitx.RunStream(gctx, s.Path, nil, func(r io.Reader) error {
		lr := &io.LimitedReader{R: r, N: diffReadMax}
		var rerr error
		files, _, rerr = gitx.ReadDiff(lr, diffLineMax, keep)
		full = lr.N <= 0
		return rerr
	}, "diff", "--no-color", "-U3", "--no-renames", "--end-of-options", base, tree, "--", ":(literal)"+ce.Path)
	if err != nil {
		return nil, err
	}
	out.Total = total
	if full {
		// stopped before the end (the last line read may be a part of
		// one: ReadDiff marks it cut): at least the lines added and
		// deleted, and more than were read
		out.Total = max(total, ce.Added+ce.Deleted)
		out.Truncated = true
	}
	// a type change (a file and a symlink) is a deletion and an addition
	// of the one path: both, in git's order
	for _, f := range files {
		out.Binary = out.Binary || f.Binary
		for _, h := range f.Hunks {
			if len(h.Lines) == 0 {
				continue
			}
			dh := diffHunk{Header: h.Header, Lines: make([]diffLine, 0, len(h.Lines))}
			for _, l := range h.Lines {
				dh.Lines = append(dh.Lines, diffLine{Kind: l.Kind, Old: l.Old, New: l.New, Text: l.Text, Cut: l.Cut})
			}
			out.Hunks = append(out.Hunks, dh)
		}
	}
	return out, nil
}

// changedFile finds the page's path in the list: the same name first,
// else the one name that reads so once its bytes that are not UTF-8 are
// replaced as encoding/json replaces them (the list the page has is JSON,
// so a Latin-1 name from another system comes back with U+FFFD; two such
// names that read the same are neither).
func changedFile(list []changeEntry, path string) *changeEntry {
	var seen *changeEntry
	n := 0
	for i := range list {
		if list[i].Path == path {
			return &list[i]
		}
		if !utf8.ValidString(list[i].Path) && jsonText(list[i].Path) == path {
			seen = &list[i]
			n++
		}
	}
	if n == 1 {
		return seen
	}
	return nil
}

// jsonText is s as encoding/json writes it: each byte that is not UTF-8
// becomes U+FFFD.
func jsonText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && n == 1 {
			b.WriteRune(utf8.RuneError)
		} else {
			b.WriteString(s[i : i+n])
		}
		i += n
	}
	return b.String()
}

// ---- the GUI's handlers (gui.go registers them on the GUI's mux only) ----

type changePeer struct {
	Slug     string `json:"slug"`
	Conflict bool   `json:"conflict,omitempty"`
}

type guiChange struct {
	changeEntry
	Peers []changePeer `json:"peers,omitempty"` // other sessions touching it (the TUI's ⚠ slugs)
}

type guiChangesView struct {
	Slug     string          `json:"slug"`
	Files    []guiChange     `json:"files"`
	Overlaps []state.Overlap `json:"overlaps,omitempty"` // the conflict forecast (the TUI's footer)
}

// guiChanges is GET /api/sessions/{slug}/changes: changes, and the
// forecast of the session (session.get's overlaps) laid on its files.
func (d *Daemon) guiChanges(w http.ResponseWriter, r *http.Request) {
	who := humanCaller(webVia(peerFrom(r)))
	p := mustJSON(slugP{Slug: r.PathValue("slug")})
	res, err := d.CallAs(who, "changes", p)
	if err != nil {
		webErr(w, err)
		return
	}
	sres, err := d.CallAs(who, "session.get", p)
	if err != nil {
		webErr(w, err)
		return
	}
	s, _ := sres.(*state.Session)
	list, _ := res.([]changeEntry)
	peers := map[string][]changePeer{}
	var overlaps []state.Overlap
	if s != nil {
		overlaps = s.Overlaps
		for _, o := range s.Overlaps {
			for _, f := range o.Files {
				peers[f] = append(peers[f], changePeer{Slug: o.Slug, Conflict: slices.Contains(o.ConflictFiles, f)})
			}
		}
	}
	out := guiChangesView{Slug: r.PathValue("slug"), Files: []guiChange{}, Overlaps: overlaps}
	for _, c := range list {
		out.Files = append(out.Files, guiChange{changeEntry: c, Peers: peers[c.Path]})
	}
	writeJSON(w, out)
}

// guiChangesDiff is GET /api/sessions/{slug}/diff?path=: changes.diff.
// 404 when the path is not one the session changed (any more).
func (d *Daemon) guiChangesDiff(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" || len(path) > diffPathMax {
		http.Error(w, "path must be 1 to 4096 bytes", http.StatusBadRequest)
		return
	}
	res, err := d.CallAs(humanCaller(webVia(peerFrom(r))), "changes.diff", mustJSON(diffP{Slug: r.PathValue("slug"), Path: path}))
	if errors.Is(err, errNotChanged) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err != nil {
		webErr(w, err)
		return
	}
	writeJSON(w, res)
}
