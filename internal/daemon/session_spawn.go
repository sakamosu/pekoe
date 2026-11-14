package daemon

import (
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"pekoe/internal/engine"
	"pekoe/internal/paths"
	"pekoe/internal/state"
)

// shell runs a command line from the repo's config through $SHELL -c
// (design: ただのシェル), inside the sandbox: it runs code from the
// worktree, which an agent can write. Without a sandbox it runs nothing and
// returns a *sandbox.UnavailableError.
// userShell is $SHELL, or /bin/sh.
func userShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/sh"
}

func (d *Daemon) shell(ctx context.Context, dir string, env []string, cmdline string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	argv, err := d.sbx.Wrap([]string{userShell(), "-c", cmdline})
	if err != nil {
		return "", err
	}
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Dir = dir
	c.Env = append(os.Environ(), env...)
	out, err := c.CombinedOutput()
	return string(out), err
}

// sessionIDRe is what an agent's session id may look like (Claude Code and
// Codex use UUIDs). The id is put on the resume command line.
var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func validSessionID(id string) bool { return sessionIDRe.MatchString(id) }

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// promptArgMax is the longest quoted prompt put on the agent's command line.
// tmux refuses a command over its message size (about 16 KB, less in older
// versions, and the environment shares it); longer prompts go via a file.
const promptArgMax = 4 << 10

// writePromptFile keeps a long initial prompt in pekoe's data directory
// (0600) for the agent's shell to read once.
func writePromptFile(slug, prompt string) (string, error) {
	dir := filepath.Join(paths.DataDir(), "prompts")
	if err := paths.Ensure(dir); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, slug+"-*.txt")
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(prompt); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// spawnAgent starts the agent window. resume = claude session id to resume
// ("continue" = most recent in cwd).
func (d *Daemon) spawnAgent(slug, prompt, resume string, w, h int) error {
	s, err := d.snapshot(slug)
	if err != nil {
		return err
	}
	ag := d.cfg().Agents[s.Agent]
	cmd := ag.Command
	if resume != "" && resume != "continue" && !validSessionID(resume) {
		log.Printf("spawn %s: ignoring malformed session id to resume", slug)
		resume = ""
	}
	if s.Agent == "claude" && resume != "" && resume != "continue" && !claudeConversationExists(s.Path, resume) {
		// Claude Code only stores a conversation once a prompt was sent; an
		// id from SessionStart alone cannot be resumed
		resume = ""
		if len(pastClaudeSessions(s.Path)) > 0 {
			resume = "continue"
		}
	}
	switch {
	case resume == "continue" && s.Agent == "claude":
		cmd = ag.Command + " --continue"
	case resume != "" && ag.Resume != "":
		cmd = strings.ReplaceAll(ag.Resume, "{{session_id}}", shellQuote(resume))
	}
	// before the prompt's "--": every way an agent starts (new, open,
	// restart, resume after a daemon restart) comes through here
	if cmd, err = d.withAgentSandbox(slug, cmd, ag.SandboxOn(), []string{s.RepoDir}, nil); err != nil {
		return err
	}
	pasteLater, promptFile := "", ""
	if prompt != "" {
		if ag.Prompt == "paste" {
			pasteLater = prompt
		} else {
			// after "--": a prompt (or an issue body from --from gh#N) that
			// starts with "-" is the prompt, not options such as
			// --permission-mode or --settings (claude and codex both take
			// "--"; an agent that doesn't can use prompt = "paste")
			arg := shellQuote(prompt)
			if len(arg) > promptArgMax {
				// too long for tmux's command line ("command too long"):
				// the shell reads it from a file, so the agent still gets
				// the same argument
				f, err := writePromptFile(slug, prompt)
				if err != nil {
					return err
				}
				promptFile = f
				arg = `"$(cat ` + shellQuote(f) + `; rm -f ` + shellQuote(f) + `)"`
			}
			cmd += " -- " + arg
		}
	}
	env := append([]string{}, s.Env...)
	env = append(env, "PEKOE_SESSION="+slug, "PEKOE_SOCK="+paths.Socket(), "PEKOE=1")
	env = claudeStayEnv(ag.Command, env)
	if os.Getenv("PEKOE_HOME") != "" {
		env = append(env, "PEKOE_HOME="+os.Getenv("PEKOE_HOME"))
	}
	w, h = d.spawnSize(w, h)
	// Keep the window alive after the agent exits so the user sees why.
	// the pane stays after exit (remain-on-exit) and reports itself dead, so
	// the TUI can offer restart instead of typing into nothing
	full := cmd + `; printf '\n[pekoe] agent exited (%s). ↵ restart · x close\n' "$?"`
	if err := d.Eng.Spawn(engine.Spawn{Slug: slug, Window: engine.WinAgent, Dir: s.Path, Env: env, Command: full, Width: w, Height: h}); err != nil {
		if promptFile != "" {
			os.Remove(promptFile)
		}
		return err
	}
	d.update(slug, func(x *state.Session) {
		x.Stopped = false
		x.Error = ""
		if x.State == state.Creating || x.State == state.Failed || x.State == state.Idle {
			x.State = state.Running
			if prompt == "" && resume == "" {
				x.State = state.Idle
			}
		}
	})
	if pasteLater != "" {
		go func() {
			time.Sleep(2 * time.Second)
			_ = d.Eng.SendText(slug, engine.WinAgent, pasteLater)
			_ = d.Eng.SendKeys(slug, engine.WinAgent, "Enter")
		}()
	}
	return nil
}

func (d *Daemon) hCancel(p slugP) (any, error) {
	d.mu.Lock()
	cancel := d.cancels[p.Slug]
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	if s.State != state.Creating && s.State != state.Failed {
		return nil, errors.New("session is not being created")
	}
	return d.hRm(rmP{Slug: p.Slug, Force: true})
}

// spawnSize picks the pty size for a new agent window. Callers that don't
// know the pane (the CLI run from a non-terminal, e.g. by an agent) pass 0;
// the TUI's last reported pane size is used then, so the agent's first
// render already wraps at the width it will be shown in.
func (d *Daemon) spawnSize(w, h int) (int, int) {
	if w > 0 && h > 0 {
		return w, h
	}
	d.mu.Lock()
	pw, ph := d.paneW, d.paneH
	d.mu.Unlock()
	if pw > 0 && ph > 0 {
		return pw, ph
	}
	return 120, 40
}

type resizeP struct {
	Slug   string `json:"slug"`
	Window string `json:"window"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

func (d *Daemon) hResize(p resizeP) (any, error) {
	if p.Slug == "*" { // resize every agent window (TUI layout change)
		d.mu.Lock()
		d.paneW, d.paneH = p.Width, p.Height
		slugs := make([]string, 0, len(d.sessions))
		for k := range d.sessions {
			slugs = append(slugs, k)
		}
		d.mu.Unlock()
		for _, s := range slugs {
			for _, w := range []string{engine.WinAgent, engine.WinReview, engine.WinDev, engine.WinRun} {
				_ = d.Eng.Resize(s, w, p.Width, p.Height)
			}
		}
		_ = d.Eng.Resize(TapiSlug, engine.WinAgent, p.Width, p.Height) // tapi chat shows in the same pane
		return nil, nil
	}
	return nil, d.Eng.Resize(p.Slug, win(p.Window), p.Width, p.Height)
}

func (d *Daemon) hKill(p slugP) (any, error) {
	if _, err := d.get(p.Slug); err != nil {
		return nil, err
	}
	_ = d.Eng.KillSession(p.Slug)
	return d.update(p.Slug, func(s *state.Session) {
		s.Stopped = true
		s.Dev = false
		if s.State == state.Running || s.State == state.Waiting {
			s.State = state.Idle
		}
	})
}

type restartP struct {
	Slug   string `json:"slug"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// hRestart starts the agent again, resuming its last Claude Code session.
func (d *Daemon) hRestart(p restartP) (any, error) {
	s, err := d.snapshot(p.Slug)
	if err != nil {
		return nil, err
	}
	resume := s.ClaudeSessionID
	if resume == "" && s.Agent == "claude" && len(pastClaudeSessions(s.Path)) > 0 {
		resume = "continue"
	}
	if err := d.spawnAgent(p.Slug, "", resume, p.Width, p.Height); err != nil {
		return nil, err
	}
	return d.snapshot(p.Slug)
}
