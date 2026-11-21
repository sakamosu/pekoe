package daemon

// The local GUI's Changes view (docs/gui.md 「段階 2 の一部: Changes」): the
// files a session changed since its base, with the lines added and
// deleted and the sessions that touch the same file (the TUI's [3]
// Changes), and one file's diff to read. Nothing here writes: no comment,
// no approval (決まったこと 7, the review surface stays the TUI's).
//
//   - changes (review.go): the list, as the TUI reads it
//   - changes.diff: one changed file's diff, cut at diffMaxLines lines or
//     diffMaxBytes bytes of text, each line at diffLineMax bytes. Only a
//     path the list has: a pathspec is never built from what the page
//     sent beyond that one literal name
//
// Both are a person's (human.go). The text is the agent's and the
// repository's: the page draws it as text only.

import (
	"context"
	"errors"
	"net/http"
	"slices"
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
	var ce *changeEntry
	for i := range list {
		if list[i].Path == p.Path {
			ce = &list[i]
			break
		}
	}
	if ce == nil {
		return nil, errNotChanged
	}
	out := FileDiffView{Slug: s.Slug, Path: ce.Path, Status: ce.Status, Binary: ce.Binary, Added: ce.Added, Deleted: ce.Deleted, Hunks: []diffHunk{}}
	if ce.Binary {
		return out, nil
	}
	gctx, cancel := context.WithTimeout(context.Background(), diffTimeout)
	defer cancel()
	raw, err := gitx.RunRaw(gctx, s.Path, nil, nil, "diff", "--no-color", "-U3", "--no-renames", "--end-of-options", base, tree, "--", ":(literal)"+ce.Path)
	if err != nil {
		return nil, err
	}
	files := gitx.ParseDiff(string(raw))
	if len(files) == 0 {
		return out, nil
	}
	f := files[0]
	out.Binary = f.Binary
	size := 0
	for _, h := range f.Hunks {
		out.Total += len(h.Lines)
		if out.Truncated {
			continue
		}
		dh := diffHunk{Header: h.Header, Lines: []diffLine{}}
		for _, l := range h.Lines {
			dl := diffLine{Kind: l.Kind, Old: l.Old, New: l.New, Text: l.Text}
			if len(dl.Text) > diffLineMax {
				dl.Text, dl.Cut = cutUTF8(dl.Text, diffLineMax), true
			}
			// the budget counts what is sent: a line already cut
			if out.Lines >= diffMaxLines || size+len(dl.Text) > diffMaxBytes {
				out.Truncated = true
				break
			}
			size += len(dl.Text)
			out.Lines++
			dh.Lines = append(dh.Lines, dl)
		}
		if len(dh.Lines) > 0 {
			out.Hunks = append(out.Hunks, dh)
		}
	}
	return out, nil
}

// cutUTF8 keeps at most n bytes of s, never half a character.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
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
