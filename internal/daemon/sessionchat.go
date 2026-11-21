package daemon

// A session's chat and the helpers of its input box, for the local GUI
// (docs/gui.md 「段階 2 の一部: セッションの吹き出しと送信欄の / @」):
//
//   - session.transcript: the session's Claude Code conversation as chat
//     items, read as tapi chat's is (transcript.go: the same items, the same
//     things never returned). The file is never one a hook named: hooks need
//     no token, so any process of the user can send one for any session.
//     pekoed takes the conversation id the hooks reported (a bare id,
//     validSessionID) and looks for it only in the Claude Code project
//     directory of the session's own worktree (sessionTranscript).
//   - session.commands: the slash commands the session's Claude Code
//     offers (built in, the user's and the worktree's .claude/commands and
//     skills): names and one-line descriptions, to put a "/name" into the
//     input box. Nothing is sent.
//   - session.files: the files of the session's worktree (git ls-files,
//     .gitignore kept), filtered, to put an "@path" into the input box.
//
// All three are a person's (human.go): they return what agents and the
// repository wrote, which the page draws as text only.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"pekoe/internal/config"
	"pekoe/internal/gitx"
	"pekoe/internal/state"
)

type sessionTranscriptP struct {
	Slug   string `json:"slug"`
	After  string `json:"after"`
	Before string `json:"before,omitempty"`
}

// errNotClaude: the agent writes no transcript pekoe can read (codex,
// opencode, …): the page keeps to the screen.
var errNotClaude = fmt.Errorf("%w: the agent is not Claude Code", errNoTranscript)

// hSessionTranscript is session.transcript: as tapi.transcript, for a
// session.
func (d *Daemon) hSessionTranscript(p sessionTranscriptP) (any, error) {
	s, err := d.chatSession(p.Slug)
	if err != nil {
		return nil, err
	}
	if !d.claudeAgent(s.Agent) {
		return nil, errNotClaude
	}
	path := sessionTranscript(s)
	if path == "" {
		return nil, errNoTranscript
	}
	return chatPage(path, p.After, p.Before)
}

// chatSession is the session a chat call names (tapi has its own calls).
func (d *Daemon) chatSession(slug string) (*state.Session, error) {
	if slug == TapiSlug {
		return nil, fmt.Errorf("no such session: %s", slug)
	}
	return d.snapshot(slug)
}

// claudeAgent: the agent writes Claude Code's transcripts and reads its
// commands: [agents.claude] (whatever its command: a wrapper), or another
// agent whose command is claude.
func (d *Daemon) claudeAgent(name string) bool {
	ag, ok := d.cfg().Agents[name]
	return ok && (name == "claude" || config.IsClaude(ag.Command))
}

// sessionProjectDirs are where Claude Code keeps the conversations started
// in the session's worktree (by its path and its symlink-resolved path).
func sessionProjectDirs(s *state.Session) []string {
	if s.Path == "" {
		return nil
	}
	a, b := claudeProjectDir(s.Path), claudeProjectDir(realpath(s.Path))
	if a == b {
		return []string{a}
	}
	return []string{a, b}
}

// sessionTranscript is the session's conversation: the one its hooks last
// reported (ClaudeSessionID) when that is in the worktree's project
// directory, else the newest there (pekoe adopted the agent, or no hook
// yet). The id alone comes from the hooks; the directory is the
// worktree's, so a hook that names another session's conversation, or a
// path elsewhere, reads nothing outside it. "" when there is none.
func sessionTranscript(s *state.Session) string {
	dirs := sessionProjectDirs(s)
	if len(dirs) == 0 {
		return ""
	}
	named := ""
	if id := s.ClaudeSessionID; validSessionID(id) {
		for _, dir := range dirs {
			p := filepath.Join(dir, id+".jsonl")
			if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() {
				return p
			}
		}
		named = filepath.Join(dirs[0], id+".jsonl")
	}
	var best string
	var bestAt time.Time
	for _, dir := range dirs {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			name := e.Name()
			if !e.Type().IsRegular() || !strings.HasSuffix(name, ".jsonl") || !validSessionID(strings.TrimSuffix(name, ".jsonl")) {
				continue
			}
			if fi, err := e.Info(); err == nil && fi.ModTime().After(bestAt) {
				best, bestAt = filepath.Join(dir, name), fi.ModTime()
			}
		}
	}
	if best != "" {
		return best
	}
	// reported, not written yet (a conversation just cleared): empty
	return named
}

// ---- slash commands ----

const (
	cmdMaxEach = 300 // commands read from one directory
	cmdMaxDesc = 200 // bytes of a description
	cmdHead    = 8 << 10
)

// SlashCommand is one entry of the "/ Commands" list.
//   - source: builtin | project | user | skill (project or user)
//     | plugin is not read (推測では届かないもの: docs/gui.md)
type SlashCommand struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	Description string `json:"description,omitempty"`
}

// CommandList is session.commands's answer. Supported: false for an
// agent that is not Claude Code (no list: the page says so).
type CommandList struct {
	Agent     string         `json:"agent"`
	Supported bool           `json:"supported"`
	Commands  []SlashCommand `json:"commands"`
}

// claudeBuiltins are Claude Code's own commands (2.1.x, from its /help;
// an approximation: versions add and drop some, and /help in the agent's
// window is the truth).
var claudeBuiltins = []SlashCommand{
	{Name: "add-dir", Description: "Add a working directory"},
	{Name: "agents", Description: "Manage subagents"},
	{Name: "clear", Description: "Clear the conversation and start a new one"},
	{Name: "compact", Description: "Summarize the conversation to free context"},
	{Name: "config", Description: "Open the settings"},
	{Name: "context", Description: "Show what fills the context window"},
	{Name: "cost", Description: "Show the cost and length of this session"},
	{Name: "doctor", Description: "Check the installation"},
	{Name: "export", Description: "Export the conversation"},
	{Name: "help", Description: "Show help and the commands"},
	{Name: "hooks", Description: "Manage hooks"},
	{Name: "init", Description: "Write a CLAUDE.md for the codebase"},
	{Name: "mcp", Description: "Manage MCP servers"},
	{Name: "memory", Description: "Edit the memory files"},
	{Name: "model", Description: "Choose the model"},
	{Name: "permissions", Description: "Manage the permission rules"},
	{Name: "plugin", Description: "Manage plugins"},
	{Name: "pr-comments", Description: "Read the comments of a pull request"},
	{Name: "release-notes", Description: "Show the release notes"},
	{Name: "resume", Description: "Resume a conversation"},
	{Name: "review", Description: "Review a pull request"},
	{Name: "rewind", Description: "Go back to an earlier point of the conversation"},
	{Name: "security-review", Description: "Review the pending changes for security"},
	{Name: "status", Description: "Show the version, model and account"},
	{Name: "todos", Description: "Show the todo list"},
	{Name: "usage", Description: "Show the plan's usage limits"},
}

var cmdNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// hSessionCommands is session.commands: the built-in commands, then the
// worktree's (.claude/commands, .claude/skills), then the user's
// (~/.claude or $CLAUDE_CONFIG_DIR). The worktree's files are the
// repository's and an agent can write them: they are read only as regular
// files inside .claude (no symlink is followed out), and only the name and
// the description leave pekoed.
func (d *Daemon) hSessionCommands(p slugP) (any, error) {
	s, err := d.chatSession(p.Slug)
	if err != nil {
		return nil, err
	}
	out := CommandList{Agent: s.Agent, Commands: []SlashCommand{}}
	if !d.claudeAgent(s.Agent) {
		return out, nil
	}
	out.Supported = true
	seen := map[string]bool{}
	add := func(c SlashCommand) {
		if seen[c.Name] {
			return
		}
		seen[c.Name] = true
		out.Commands = append(out.Commands, c)
	}
	for _, c := range claudeBuiltins {
		c.Source = "builtin"
		add(c)
	}
	var own []SlashCommand
	// the worktree's .claude itself may not be a symlink either: it would
	// lead the descriptions out of the worktree
	if s.Path != "" && dirOK(filepath.Join(s.Path, ".claude"), true) {
		own = append(own, readCommands(filepath.Join(s.Path, ".claude", "commands"), "project", true)...)
		own = append(own, readSkills(filepath.Join(s.Path, ".claude", "skills"), "project skill", true)...)
	}
	base := claudeConfigDir()
	own = append(own, readCommands(filepath.Join(base, "commands"), "user", false)...)
	own = append(own, readSkills(filepath.Join(base, "skills"), "user skill", false)...)
	for _, c := range own {
		add(c)
	}
	return out, nil
}

// readCommands lists dir's *.md (and one level of subdirectories, as
// Claude Code names them: the file's name, the directory in the source).
// strict: the worktree's, where no symlink is followed (dir itself
// included); the user's own directories may be symlinks (dotfiles).
func readCommands(dir, source string, strict bool) []SlashCommand {
	if !dirOK(dir, strict) {
		return nil
	}
	var out []SlashCommand
	var walk func(dir, sub string, depth int)
	walk = func(dir, sub string, depth int) {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if len(out) >= cmdMaxEach {
				return
			}
			p := filepath.Join(dir, e.Name())
			if e.IsDir() {
				if depth < 1 && cmdNameRe.MatchString(e.Name()) {
					walk(p, e.Name(), depth+1)
				}
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".md")
			if name == e.Name() || !cmdNameRe.MatchString(name) || !fileOK(p, e, strict) {
				continue
			}
			src := source
			if sub != "" {
				src += ":" + sub
			}
			out = append(out, SlashCommand{Name: name, Source: src, Description: mdDescription(p, strict)})
		}
	}
	walk(dir, "", 0)
	return out
}

// readSkills lists dir/<name>/SKILL.md (a skill is a command too:
// /<name>).
func readSkills(dir, source string, strict bool) []SlashCommand {
	if !dirOK(dir, strict) {
		return nil
	}
	ents, _ := os.ReadDir(dir)
	var out []SlashCommand
	for _, e := range ents {
		if len(out) >= cmdMaxEach {
			break
		}
		if !cmdNameRe.MatchString(e.Name()) {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		if !dirOK(sub, strict) {
			continue
		}
		p := filepath.Join(sub, "SKILL.md")
		fi, err := os.Lstat(p)
		if err != nil || (strict && !fi.Mode().IsRegular()) {
			continue
		}
		out = append(out, SlashCommand{Name: e.Name(), Source: source, Description: mdDescription(p, strict)})
	}
	return out
}

func dirOK(dir string, strict bool) bool {
	if strict {
		fi, err := os.Lstat(dir)
		return err == nil && fi.IsDir()
	}
	fi, err := os.Stat(dir)
	return err == nil && fi.IsDir()
}

func fileOK(p string, e os.DirEntry, strict bool) bool {
	if strict {
		return e.Type().IsRegular()
	}
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// mdDescription is a command file's description: the frontmatter's
// description:, else its first line of text; one line, cut at cmdMaxDesc
// bytes, no control characters. Only the head of the file is read, not
// blocking on a FIFO; strict (the worktree's): never through a symlink at
// the end (the user's own may be symlinks: dotfiles).
func mdDescription(p string, strict bool) string {
	flags := os.O_RDONLY | syscall.O_NONBLOCK
	if strict {
		flags |= syscall.O_NOFOLLOW
	}
	f, err := os.OpenFile(p, flags, 0)
	if err != nil {
		return ""
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	head, _ := io.ReadAll(io.LimitReader(f, cmdHead))
	sc := bufio.NewScanner(bytes.NewReader(head))
	inFront, first := false, true
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if first {
			first = false
			if line == "---" {
				inFront = true
				continue
			}
		}
		if inFront {
			if line == "---" {
				inFront = false
				continue
			}
			if v, ok := strings.CutPrefix(line, "description:"); ok {
				return descLine(strings.Trim(strings.TrimSpace(v), `"'`), cmdMaxDesc)
			}
			continue
		}
		if line != "" {
			return descLine(strings.TrimLeft(line, "# "), cmdMaxDesc)
		}
	}
	return ""
}

// descLine: s without control characters (each becomes a space), cut at n
// bytes on a rune boundary.
func descLine(s string, n int) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return cutBytes(strings.TrimSpace(s), n)
}

// claudeConfigDir is Claude Code's directory ($CLAUDE_CONFIG_DIR, else
// ~/.claude).
func claudeConfigDir() string { return filepath.Dir(claudeProjectsDir()) }

// ---- files ----

// filesMaxRead: paths kept from git ls-files (a var: the tests lower it)
var filesMaxRead = 50000

const (
	filesMaxShown = 50   // matches one call returns
	filesMaxPath  = 1024 // bytes of a path shown
	filesEvery    = 5 * time.Second
	filesTimeout  = 10 * time.Second
)

type sessionFilesP struct {
	Slug  string `json:"slug"`
	Query string `json:"q"`
}

// FileList is session.files's answer: the paths matching q (at most
// filesMaxShown, the best first), how many matched, and whether the
// worktree has more files than pekoed lists (Truncated) or names it leaves
// out (Skipped: control characters, not UTF-8, longer than filesMaxPath).
type FileList struct {
	Files     []string `json:"files"`
	Matches   int      `json:"matches"`
	Total     int      `json:"total"`
	Truncated bool     `json:"truncated,omitempty"`
	Skipped   int      `json:"skipped,omitempty"`
}

type fileListing struct {
	at        time.Time
	path      string
	files     []string
	truncated bool
	skipped   int
}

// hSessionFiles is session.files: git ls-files in the session's worktree
// (tracked, and untracked but not ignored), run as pekoed runs every git
// (gitx: no hook, no filter or fsmonitor of the repository's), at most
// every filesEvery per worktree. git lists paths inside the worktree only
// and does not go into a symlink, and pekoed reads none of the files.
func (d *Daemon) hSessionFiles(p sessionFilesP) (any, error) {
	s, err := d.chatSession(p.Slug)
	if err != nil {
		return nil, err
	}
	if s.Path == "" {
		return nil, fmt.Errorf("%s has no worktree", s.Slug)
	}
	if s.GitBlocked != "" {
		return nil, fmt.Errorf("pekoed runs no git in %s: %s", s.Slug, s.GitBlocked)
	}
	if len(p.Query) > 256 {
		return nil, errors.New("q is too long")
	}
	l, err := d.listFiles(s)
	if err != nil {
		return nil, err
	}
	out := FileList{Files: []string{}, Total: len(l.files), Truncated: l.truncated, Skipped: l.skipped}
	ranked := rankFiles(l.files, p.Query)
	out.Matches = len(ranked)
	out.Files = append(out.Files, ranked[:min(len(ranked), filesMaxShown)]...)
	return out, nil
}

var fileListMu sync.Mutex

func (d *Daemon) listFiles(s *state.Session) (*fileListing, error) {
	fileListMu.Lock()
	defer fileListMu.Unlock()
	if v, ok := d.fileLists.Load(s.Slug); ok {
		if l := v.(*fileListing); l.path == s.Path && time.Since(l.at) < filesEvery {
			return l, nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), filesTimeout)
	defer cancel()
	// the tracked first, then the untracked fill what is left: a big
	// untracked tree (a build, node_modules not ignored) never pushes the
	// tracked files out of the list
	l := &fileListing{at: time.Now(), path: s.Path}
	for _, which := range [][]string{{"--cached", "--deduplicate"}, {"--others", "--exclude-standard"}} {
		if l.truncated {
			break
		}
		raw, err := gitx.RunRaw(ctx, s.Path, nil, nil, append([]string{"ls-files", "-z"}, which...)...)
		if err != nil {
			return nil, err
		}
		for _, name := range strings.Split(string(raw), "\x00") {
			if name == "" {
				continue
			}
			if len(l.files) >= filesMaxRead {
				l.truncated = true
				break
			}
			if !shownPath(name) {
				l.skipped++
				continue
			}
			l.files = append(l.files, name)
		}
	}
	sort.Strings(l.files)
	// a session removed while git ran: keep nothing (forget dropped its list)
	if _, err := d.snapshot(s.Slug); err == nil {
		d.fileLists.Store(s.Slug, l)
	}
	return l, nil
}

// shownPath: a path the page may show and put into the input box: UTF-8,
// no control characters (the send refuses them), not absurdly long, and
// relative inside the worktree. git prints the index's paths as they are,
// and an agent can write its worktree's index: no part may be "..", "."
// or empty (but the "/" that ends a nested repository's "sub/").
func shownPath(p string) bool {
	if len(p) > filesMaxPath || !utf8.ValidString(p) || filepath.IsAbs(p) {
		return false
	}
	for _, part := range strings.Split(strings.TrimSuffix(p, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	for _, r := range p {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return false
		}
	}
	return true
}

// rankFiles: the paths that hold every word of q (case folded), best
// first: the file's name starts with the last word, then the name holds
// it, then anywhere in the path; shorter paths first among equals. An
// empty q is every path, shortest first.
func rankFiles(files []string, q string) []string {
	words := strings.Fields(strings.ToLower(q))
	type hit struct {
		p    string
		rank int
	}
	var hits []hit
	for _, f := range files {
		lf := strings.ToLower(f)
		ok := true
		for _, w := range words {
			if !strings.Contains(lf, w) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		rank := 2
		if len(words) > 0 {
			base := strings.ToLower(filepath.Base(f))
			last := words[len(words)-1]
			switch {
			case strings.HasPrefix(base, last):
				rank = 0
			case strings.Contains(base, last):
				rank = 1
			}
		}
		hits = append(hits, hit{f, rank})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].rank != hits[j].rank {
			return hits[i].rank < hits[j].rank
		}
		if len(hits[i].p) != len(hits[j].p) {
			return len(hits[i].p) < len(hits[j].p)
		}
		return hits[i].p < hits[j].p
	})
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.p
	}
	return out
}

// ---- the GUI's handlers (gui.go registers them on the GUI's mux only) ----

// guiSessionTranscript is GET /api/sessions/{slug}/transcript?after= or
// ?before=: session.transcript. 404 when the agent has no transcript pekoe
// reads (not Claude Code, nothing written yet): the page keeps the screen.
func (d *Daemon) guiSessionTranscript(w http.ResponseWriter, r *http.Request) {
	after, before, ok := chatQuery(w, r)
	if !ok {
		return
	}
	res, err := d.CallAs(humanCaller(webVia(peerFrom(r))), "session.transcript", mustJSON(sessionTranscriptP{Slug: r.PathValue("slug"), After: after, Before: before}))
	if err != nil {
		chatErr(w, err)
		return
	}
	writeJSON(w, res)
}

// guiSessionCommands is GET /api/sessions/{slug}/commands: session.commands.
func (d *Daemon) guiSessionCommands(w http.ResponseWriter, r *http.Request) {
	res, err := d.CallAs(humanCaller(webVia(peerFrom(r))), "session.commands", mustJSON(slugP{Slug: r.PathValue("slug")}))
	if err != nil {
		webErr(w, err)
		return
	}
	writeJSON(w, res)
}

// guiSessionFiles is GET /api/sessions/{slug}/files?q=: session.files.
func (d *Daemon) guiSessionFiles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if len(q) > 256 {
		http.Error(w, "q is too long", http.StatusBadRequest)
		return
	}
	res, err := d.CallAs(humanCaller(webVia(peerFrom(r))), "session.files", mustJSON(sessionFilesP{Slug: r.PathValue("slug"), Query: q}))
	if err != nil {
		webErr(w, err)
		return
	}
	writeJSON(w, res)
}
