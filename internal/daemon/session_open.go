package daemon

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"pekoe/internal/config"
	"pekoe/internal/gitx"
	"pekoe/internal/paths"
	"pekoe/internal/state"
)

// ---- open / adopt ----

type openP struct {
	Path   string `json:"path"`
	Resume string `json:"resume"` // ask | resume | new
	Agent  string `json:"agent"`
	Prompt string `json:"prompt"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// claudeProjectDir mirrors how Claude Code stores transcripts per cwd.
func claudeProjectDir(path string) string {
	return filepath.Join(claudeProjectsDir(), claudeProjectName(path))
}

// claudeProjectMax is where Claude Code cuts a long project name.
const claudeProjectMax = 200

// claudeProjectName is Claude Code's name for the project directory of a
// cwd (2.1.326): every UTF-16 code unit that is not [A-Za-z0-9] becomes
// "-" (a character outside the BMP gives two), and a name longer than 200
// units is cut there and gets "-" and a hash of the path in base 36
// (Math.abs of the 32-bit h = h*31 + unit over the code units).
func claudeProjectName(path string) string {
	units := utf16.Encode([]rune(path))
	enc := make([]byte, len(units))
	var h int32
	for i, u := range units {
		h = h*31 + int32(u)
		if u < utf8.RuneSelf && (u >= 'a' && u <= 'z' || u >= 'A' && u <= 'Z' || u >= '0' && u <= '9') {
			enc[i] = byte(u)
		} else {
			enc[i] = '-'
		}
	}
	if len(enc) <= claudeProjectMax {
		return string(enc)
	}
	abs := int64(h)
	if abs < 0 {
		abs = -abs
	}
	return string(enc[:claudeProjectMax]) + "-" + strconv.FormatInt(abs, 36)
}

// claudeProjectsDir holds one directory per starting cwd.
func claudeProjectsDir() string {
	base := os.Getenv("CLAUDE_CONFIG_DIR")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".claude")
	}
	return filepath.Join(base, "projects")
}

type pastSession struct {
	ID       string    `json:"id"`
	Modified time.Time `json:"modified"`
}

func claudeConversationExists(path, id string) bool {
	_, err := os.Stat(filepath.Join(claudeProjectDir(path), id+".jsonl"))
	return err == nil
}

func pastClaudeSessions(path string) []pastSession {
	ents, err := os.ReadDir(claudeProjectDir(path))
	if err != nil {
		return nil
	}
	var out []pastSession
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, pastSession{ID: strings.TrimSuffix(e.Name(), ".jsonl"), Modified: fi.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out
}

func (d *Daemon) hOpen(p openP) (any, error) {
	switch p.Resume {
	case "", "ask", "resume", "new", "continue":
	default:
		if !validSessionID(p.Resume) {
			return nil, fmt.Errorf("invalid session id to resume: %q", p.Resume)
		}
	}
	top, err := gitx.Toplevel(p.Path)
	if err != nil {
		return nil, fmt.Errorf("%s is not a git worktree", p.Path)
	}
	repo, err := resolveRepo(top)
	if err != nil {
		return nil, err
	}
	d.resolved(repo)
	// by the resolved path: top is git's (symlinks resolved, /private/tmp
	// on macOS) while a session made by new keeps PEKOE_WORKTREES as given
	if slug := d.sessionAt(top); slug != "" {
		return nil, fmt.Errorf("already managed as %s", slug)
	}
	agent := newAgentName(p.Agent, repo)
	if _, ok := d.cfg().Agents[agent]; !ok {
		return nil, fmt.Errorf("unknown agent: %s", agent)
	}
	past := []pastSession{}
	if agent == "claude" {
		past = pastClaudeSessions(top)
	}
	if (p.Resume == "" || p.Resume == "ask") && len(past) > 0 {
		return map[string]any{"needs_choice": true, "past": past}, nil
	}
	branch := gitx.CurrentBranch(top)
	name := branch
	if name == "HEAD" || name == "" {
		name = filepath.Base(top)
	}
	slug := d.uniqueSlug(paths.Slug(name), repo.ID, repo.Name)
	if _, err := d.get(slug); err == nil {
		slug = d.uniqueSlug(paths.Slug(filepath.Base(top)), repo.ID+"#", repo.Name)
	}
	port, err := d.DB.AllocPort(repo.ID, slug, d.cfg().Ports.Base, d.busyPort)
	if err != nil {
		return nil, err
	}
	base := repo.Config.Worktree.Base
	if base == "" {
		base = gitx.DefaultBase(repo.Dir)
	}
	vars := config.Vars{Slug: slug, Branch: branch, Path: top, Repo: repo.Name, Port: port}
	s := &state.Session{
		Slug: slug, Branch: branch, Repo: repo.Name, RepoID: repo.ID, RepoDir: repo.Dir, Path: top,
		Agent: agent, State: state.Idle, StateSince: time.Now(), CreatedAt: time.Now(), Port: port,
		Managed: paths.UnderWorktrees(top), Base: base, Test: state.Test{Status: "none"},
		Env: append(repo.Config.EnvList(vars), "PEKOE_PORT="+strconv.Itoa(port)),
	}
	d.add(s)
	if sha, err := gitx.SnapshotCommit(top, "pekoe snapshot #0"); err == nil {
		ref := fmt.Sprintf("refs/pekoe/%s/prompt-0", slug)
		_ = gitx.UpdateRef(top, ref, sha)
		_ = d.DB.AddPrompt(slug, 0, "(open)", ref)
	}
	resume := ""
	if p.Resume == "resume" && len(past) > 0 {
		resume = past[0].ID
	} else if p.Resume != "" && p.Resume != "new" && p.Resume != "ask" && p.Resume != "resume" {
		resume = p.Resume // explicit session id (checked before anything is made)
	}
	if err := d.spawnAgent(slug, p.Prompt, resume, p.Width, p.Height); err != nil {
		d.update(slug, func(x *state.Session) { x.State = state.Failed; x.Error = err.Error() })
		return nil, err
	}
	return d.snapshot(slug)
}

type adoptP struct {
	Path    string `json:"path"`
	Confirm bool   `json:"confirm"` // stop the original process and resume inside pekoe
}

type adoptCandidate struct {
	PID       int    `json:"pid"`
	Cwd       string `json:"cwd"`
	Where     string `json:"where"` // tty or tmux target
	SessionID string `json:"session_id"`
	Skip      string `json:"skip,omitempty"` // why it is left alone (not stopped)
}

// findAgents lists agent processes (swapped in tests).
var findAgents = findAgentProcesses

// hAdopt finds agents started by hand in other tabs (or tmux sessions on the
// default server). A process cannot move between ptys, so adopting means
// stopping it and resuming the same Claude Code session inside pekoe.
func (d *Daemon) hAdopt(p adoptP) (any, error) {
	cands := findAgents()
	if p.Path != "" {
		top, err := gitx.Toplevel(p.Path)
		if err != nil {
			// an empty top would match every agent on the machine
			return nil, fmt.Errorf("%s is not inside a git repository", p.Path)
		}
		var f []adoptCandidate
		for _, c := range cands {
			if c.Cwd == top || strings.HasPrefix(c.Cwd, top+"/") {
				f = append(f, c)
			}
		}
		cands = f
	}
	var free, skipped []adoptCandidate
	byTop := map[string][]adoptCandidate{}
	var tops []string
	for _, c := range cands {
		top, err := gitx.Toplevel(c.Cwd)
		if err != nil || d.managedPath(top) {
			continue
		}
		c.Cwd = top
		if byTop[top] == nil {
			tops = append(tops, top)
		}
		byTop[top] = append(byTop[top], c)
	}
	for _, top := range tops {
		cs := byTop[top]
		if len(cs) > 1 {
			// one worktree holds one session, and which conversation each
			// process has cannot be told apart: stop none of them
			for _, c := range cs {
				c.Skip = fmt.Sprintf("%d agents in this worktree: quit all but one first", len(cs))
				skipped = append(skipped, c)
			}
			continue
		}
		c := cs[0]
		if ps := pastClaudeSessions(top); len(ps) > 0 {
			c.SessionID = ps[0].ID
		}
		free = append(free, c)
	}
	if !p.Confirm {
		return map[string]any{"candidates": free, "skipped": skipped}, nil
	}
	var adopted []*state.Session
	for _, c := range free {
		if d.managedPath(c.Cwd) { // opened meanwhile: leave the process running
			log.Printf("adopt %s: already managed", c.Cwd)
			continue
		}
		if proc, err := os.FindProcess(c.PID); err == nil {
			_ = proc.Signal(os.Interrupt)
			time.Sleep(500 * time.Millisecond)
			_ = proc.Signal(os.Interrupt)
		}
		time.Sleep(time.Second)
		res, err := d.hOpen(openP{Path: c.Cwd, Resume: orNew(c.SessionID)})
		if err != nil {
			log.Printf("adopt %s: %v", c.Cwd, err)
			continue
		}
		if s, ok := res.(*state.Session); ok {
			adopted = append(adopted, s)
		}
	}
	return adopted, nil
}

// managedPath reports whether a session already lives in the worktree.
func (d *Daemon) managedPath(top string) bool { return d.sessionAt(top) != "" }

// sessionAt is the session whose worktree is path (compared with symlinks
// resolved), "" if none. The smallest slug when two share it.
func (d *Daemon) sessionAt(path string) string {
	d.mu.Lock()
	at := make(map[string]string, len(d.sessions))
	for slug, s := range d.sessions {
		at[slug] = s.Path
	}
	d.mu.Unlock()
	// resolved outside mu: EvalSymlinks per session must not hold it
	want := realpath(path)
	best := ""
	for slug, p := range at {
		if samePath(p, want) && (best == "" || slug < best) {
			best = slug
		}
	}
	return best
}

func orNew(id string) string {
	if id == "" {
		return "new"
	}
	return id
}

func findAgentProcesses() []adoptCandidate {
	out, err := exec.Command("ps", "-axo", "pid=,tty=,comm=").Output()
	if err != nil {
		return nil
	}
	self := os.Getpid()
	var res []adoptCandidate
	for _, ln := range strings.Split(string(out), "\n") {
		f := strings.Fields(ln)
		if len(f) < 3 {
			continue
		}
		pid, _ := strconv.Atoi(f[0])
		comm := filepath.Base(f[len(f)-1])
		if pid == self || comm != "claude" {
			continue
		}
		if os.Getenv("PEKOE_TEST") == "" && f[1] == "??" {
			continue // no tty: not interactive
		}
		cwd := processCwd(pid)
		if cwd == "" {
			continue
		}
		if env := processHasPekoeEnv(pid); env {
			continue // already inside pekoe
		}
		res = append(res, adoptCandidate{PID: pid, Cwd: cwd, Where: f[1]})
	}
	return res
}

func processCwd(pid int) string {
	out, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn").Output()
	if err != nil {
		return ""
	}
	for _, ln := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(ln, "n") {
			return ln[1:]
		}
	}
	return ""
}

func processHasPekoeEnv(pid int) bool {
	out, err := exec.Command("ps", "eww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	return err == nil && strings.Contains(string(out), "PEKOE_SESSION=")
}
