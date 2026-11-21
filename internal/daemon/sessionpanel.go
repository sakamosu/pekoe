package daemon

// The agent facts of the local GUI's Session panel (docs/gui.md 「段階 2 の
// 一部: Session の右パネルのモデル・モード・Context・更新」):
//
//   - the model: the last assistant message's model in the transcript
//     (Session.LastModel, readTokens), shown as Claude Code names it
//     ("claude-opus-5-5" → "Opus 5.5")
//   - the effort: the hooks' common input effort.level (Session.RunEffort),
//     else what pekoe started the agent with (pekoe new --effort, the
//     agent's effort in config.toml)
//   - the permission mode: the hooks' common input permission_mode
//     (Session.PermissionMode)
//   - the context: Session.Tokens (the last usage record; no %: the window's
//     size is not in the transcript)
//   - "Update installed": the Claude Code version the transcript's lines
//     carry (Session.AgentVersion) against `<agent command> --version`,
//     which pekoed runs at most once per agentVersionTTL per command
//
// Every one of them is the agent's own word (hooks need no token; the
// transcript is a file the agent can write): shown as text, never used to
// decide anything (an answer, a merge).

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"pekoe/internal/config"
	"pekoe/internal/state"
)

// PanelInfo is session.panel's answer. Supported: false for an agent that
// is not Claude Code (nothing of it is read; Why says so).
type PanelInfo struct {
	Agent     string `json:"agent"`
	Supported bool   `json:"supported"`
	Why       string `json:"why,omitempty"`
	Model     string `json:"model,omitempty"`      // the id, as the transcript has it
	ModelName string `json:"model_name,omitempty"` // "Opus 5.5"; "" when the id is not claude-<family>-<n>-<n>
	Effort    string `json:"effort,omitempty"`
	// EffortFrom: "hook" (what ran) or "start" (what pekoe asked for; no
	// hook told yet)
	EffortFrom  string `json:"effort_from,omitempty"`
	Mode        string `json:"mode,omitempty"`
	Tokens      int    `json:"tokens,omitempty"`
	TokensShort string `json:"tokens_short,omitempty"` // state.ShortTokens, as the TUI shows it
	Running     string `json:"running_version,omitempty"`
	Installed   string `json:"installed_version,omitempty"`
	// UpdateInstalled: Running < Installed (restart the agent to use it)
	UpdateInstalled bool `json:"update_installed,omitempty"`
}

// permissionModes are the hooks' permission_mode values (Claude Code's
// hooks docs); anything else is not kept.
var permissionModes = map[string]bool{
	"default": true, "plan": true, "acceptEdits": true, "auto": true, "dontAsk": true, "bypassPermissions": true,
}

// hookFacts are the common input fields the panel shows.
type hookFacts struct {
	PermissionMode string `json:"permission_mode"`
	Effort         struct {
		Level string `json:"level"`
	} `json:"effort"`
}

// noteHookFacts keeps a hook's permission_mode and effort.level when they
// are among the documented values (not every hook carries them: an absent
// one leaves the last). The session is only written when one changed.
func (d *Daemon) noteHookFacts(slug string, raw []byte) {
	var f hookFacts
	if err := json.Unmarshal(raw, &f); err != nil {
		return
	}
	mode, effort := f.PermissionMode, f.Effort.Level
	if !permissionModes[mode] {
		mode = ""
	}
	if !config.ValidEffort(effort) {
		effort = ""
	}
	if mode == "" && effort == "" {
		return
	}
	d.mu.Lock()
	s, ok := d.sessions[slug]
	same := ok && (mode == "" || s.PermissionMode == mode) && (effort == "" || s.RunEffort == effort)
	d.mu.Unlock()
	if !ok || same {
		return
	}
	d.update(slug, func(s *state.Session) {
		if mode != "" {
			s.PermissionMode = mode
		}
		if effort != "" {
			s.RunEffort = effort
		}
	})
}

var (
	// a version as Claude Code writes it on the transcript's lines
	versionRe = regexp.MustCompile(`^(\d{1,4})\.(\d{1,4})\.(\d{1,6})$`)
	// `claude --version`'s line: "2.1.330 (Claude Code)"
	installedRe = regexp.MustCompile(`^(\d{1,4}\.\d{1,4}\.\d{1,6}) \(Claude Code\)$`)
	modelRe     = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,80}$`)
	modelNameRe = regexp.MustCompile(`^claude-([a-z]{2,16})-(\d{1,2})-(\d{1,2})(?:-\d{8})?$`)
)

// validVersion: a plain x.y.z (no pre-release: those are not compared).
func validVersion(v string) bool { return versionRe.MatchString(v) }

// olderVersion: a < b, both plain x.y.z; false when either is not.
func olderVersion(a, b string) bool {
	ma, mb := versionRe.FindStringSubmatch(a), versionRe.FindStringSubmatch(b)
	if ma == nil || mb == nil {
		return false
	}
	for i := 1; i <= 3; i++ {
		x, _ := strconv.Atoi(ma[i])
		y, _ := strconv.Atoi(mb[i])
		if x != y {
			return x < y
		}
	}
	return false
}

// parseInstalled takes the version out of `claude --version`'s output: its
// first line must be "x.y.z (Claude Code)" and nothing else.
func parseInstalled(out []byte) string {
	line, _, _ := bytes.Cut(out, []byte("\n"))
	m := installedRe.FindSubmatch(bytes.TrimSpace(line))
	if m == nil {
		return ""
	}
	return string(m[1])
}

// modelName is Claude Code's name of a model id: "claude-opus-5-5" →
// "Opus 5.5" (a dated id too). "" for any other id: the page shows the id.
func modelName(id string) string {
	m := modelNameRe.FindStringSubmatch(id)
	if m == nil {
		return ""
	}
	return strings.ToUpper(m[1][:1]) + m[1][1:] + " " + m[2] + "." + m[3]
}

// ---- the installed version ----

// agentVersionTTL: how long pekoed keeps what `<command> --version` said
// (a failure too). An update installed meanwhile shows after it; a pekoed
// restart reads it again.
const agentVersionTTL = 10 * time.Minute

type versionCache struct {
	mu sync.Mutex
	by map[string]versionEntry // by the command's program
}

type versionEntry struct {
	v  string
	at time.Time
}

// runVersion runs `<bin> --version` (a var: tests replace it).
var runVersion = func(bin string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, bin, "--version")
	var out capped
	c.Stdout = &out
	// a child left holding stdout does not keep Wait (and the cache's
	// lock) past the timeout
	c.WaitDelay = time.Second
	err := c.Run()
	return out.b, err
}

// capped keeps the first 4KB written to it.
type capped struct{ b []byte }

func (w *capped) Write(p []byte) (int, error) {
	if n := 4096 - len(w.b); n > 0 {
		w.b = append(w.b, p[:min(n, len(p))]...)
	}
	return len(p), nil
}

var agentVersions versionCache

// installedVersion is what the agent's program says it is, kept for
// agentVersionTTL. One run at a time (a slow one holds the others back,
// at most its timeout). "" when it does not say x.y.z (Claude Code).
func installedVersion(command string) string {
	f := strings.Fields(command)
	if len(f) == 0 {
		return ""
	}
	bin := f[0]
	c := &agentVersions
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.by[bin]; ok && time.Since(e.at) < agentVersionTTL {
		return e.v
	}
	out, err := runVersion(bin)
	v := ""
	if err == nil {
		v = parseInstalled(out)
	}
	if c.by == nil {
		c.by = map[string]versionEntry{}
	}
	c.by[bin] = versionEntry{v: v, at: time.Now()}
	return v
}

// ---- session.panel ----

// hSessionPanel is session.panel: the agent facts above, for a Claude
// Code session. A person's (human.go): it runs the agent's program.
func (d *Daemon) hSessionPanel(p slugP) (any, error) {
	s, err := d.chatSession(p.Slug)
	if err != nil {
		return nil, err
	}
	out := PanelInfo{Agent: s.Agent}
	if !d.claudeAgent(s.Agent) {
		out.Why = "not Claude Code: pekoe reads no model, mode or version of this agent"
		return out, nil
	}
	out.Supported = true
	if modelRe.MatchString(s.LastModel) {
		out.Model, out.ModelName = s.LastModel, modelName(s.LastModel)
	}
	ag := d.cfg().Agents[s.Agent]
	switch {
	case config.ValidEffort(s.RunEffort):
		out.Effort, out.EffortFrom = s.RunEffort, "hook"
	case config.ValidEffort(s.Effort):
		out.Effort, out.EffortFrom = s.Effort, "start"
	case config.IsClaude(ag.Command) && config.ValidEffort(ag.Effort):
		out.Effort, out.EffortFrom = ag.Effort, "start"
	}
	if permissionModes[s.PermissionMode] {
		out.Mode = s.PermissionMode
	}
	if s.Tokens > 0 {
		out.Tokens, out.TokensShort = s.Tokens, state.ShortTokens(int64(s.Tokens))
	}
	if validVersion(s.AgentVersion) {
		out.Running = s.AgentVersion
		// only a program named claude is asked (a wrapper's --version may
		// be anything)
		cmd := ag.Command
		if cmd == "" {
			cmd = "claude"
		}
		if config.IsClaude(cmd) {
			out.Installed = installedVersion(cmd)
		}
		out.UpdateInstalled = olderVersion(out.Running, out.Installed)
	}
	return out, nil
}

// guiSessionPanel is GET /api/sessions/{slug}/panel: session.panel (the
// GUI's mux only).
func (d *Daemon) guiSessionPanel(w http.ResponseWriter, r *http.Request) {
	res, err := d.CallAs(humanCaller(webVia(peerFrom(r))), "session.panel", mustJSON(slugP{Slug: r.PathValue("slug")}))
	if err != nil {
		webErr(w, err)
		return
	}
	writeJSON(w, res)
}
