package backend

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// CandidatePrefix is how an external backend is usually named.
const CandidatePrefix = "pekoe-voice-"

// Info is one configured backend as `pekoe voice backends` shows it.
type Info struct {
	Name     string   `json:"name"`
	Path     string   `json:"path"`
	Using    bool     `json:"using"`
	Fallback bool     `json:"fallback"`
	Version  string   `json:"version,omitempty"`
	Check    string   `json:"check"` // "ok" or the reason it failed
	Voices   []Voice  `json:"voices,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// Candidate is an executable named pekoe-voice-* in PATH: only shown, never
// run, with the [voice.backends] line that would use it.
type Candidate struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Line string `json:"line"`
}

// List is the bundled say and every [voice.backends] entry, each asked
// version, check and voices (nothing else is run), and the candidates in
// pathEnv (a PATH value), which are not run.
func (e *Engine) List(ctx context.Context, pathEnv string) ([]Info, []Candidate) {
	st := e.Status()
	names := []string{Builtin}
	for n := range e.o.Backends {
		if n != Builtin {
			names = append(names, n)
		}
	}
	slices.Sort(names[1:])
	var out []Info
	for _, n := range names {
		in := Info{Name: n, Using: n == st.Using, Fallback: n == e.o.Fallback && n != ""}
		if n != Builtin {
			in.Path = e.o.Backends[n]
			if !ValidName(n) {
				in.Warnings = append(in.Warnings, strconv.Quote(n)+" is not a backend name (lower-case letters, digits and -)")
			}
		}
		b, err := Resolve(n, e.o)
		if err != nil {
			in.Check = err.Error()
			out = append(out, in)
			continue
		}
		in.Path = b.Path
		if n != Builtin {
			if w := HomeWarning(b.Path); w != "" {
				in.Warnings = append(in.Warnings, w)
			}
		}
		v, err := b.Version(ctx)
		in.Version = v
		if err == nil {
			err = b.CheckUsable(ctx)
		}
		if err != nil {
			in.Check = err.Error()
			out = append(out, in)
			continue
		}
		in.Check = "ok"
		if vs, err := b.Voices(ctx); err == nil {
			in.Voices = vs
		} else {
			in.Warnings = append(in.Warnings, err.Error())
		}
		out = append(out, in)
	}
	return out, Candidates(pathEnv)
}

// Candidates lists the pekoe-voice-* executables in pathEnv's directories
// (the first of each name, as a shell would find it). Nothing is run.
func Candidates(pathEnv string) []Candidate {
	var out []Candidate
	seen := map[string]bool{}
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, en := range ents {
			name, ok := strings.CutPrefix(en.Name(), CandidatePrefix)
			if !ok || !ValidName(name) || seen[name] {
				continue
			}
			p := filepath.Join(dir, en.Name())
			if checkExecutable(p) != nil {
				continue
			}
			seen[name] = true
			out = append(out, Candidate{Name: name, Path: p, Line: name + " = " + strconv.Quote(p)})
		}
	}
	return out
}

// HomeWarning warns when a backend (or what its link points at) is under the
// home directory, where a command in an agent's or pekoe's sandbox may be
// able to replace it. It is only a warning: where to put it is the person's
// choice ("" when not under home).
func HomeWarning(path string) string {
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return ""
	}
	under := func(p string) bool {
		rel, err := filepath.Rel(h, p)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	target := path
	if r, err := filepath.EvalSymlinks(path); err == nil {
		target = r
	}
	rh := h
	if r, err := filepath.EvalSymlinks(h); err == nil {
		rh = r
	}
	underReal := func(p string) bool {
		rel, err := filepath.Rel(rh, p)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	if under(path) || under(target) || underReal(target) {
		return path + " is under the home directory, where a command in an agent's or pekoe's sandbox may be able to replace it; /opt/homebrew/bin or /usr/local/bin is safer"
	}
	return ""
}
