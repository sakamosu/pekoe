package daemon

// tapi chat (docs/tapi-chat.md): one agent the TUI can talk to at any time,
// for questions that belong to no session, cross-repository research and
// handing work out to sessions. It is not a state.Session: it lives only in
// pekoed's memory (nothing goes to state.db) and none of the per-session
// machinery (diffstat, PRs, conflicts, land, review, dev, tests, the web UI)
// ever sees it. Its tmux window is "+tapi~agent" and its hooks arrive with
// $PEKOE_SESSION=+tapi.

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"pekoe/internal/engine"
	"pekoe/internal/paths"
	"pekoe/internal/state"
)

// TapiSlug names tapi chat where a session slug would go: its tmux window
// and $PEKOE_SESSION. paths.Slug only ever yields [A-Za-z0-9._-], so no
// session can be called this. ("@tapi" cannot be used: tmux reads the @ of
// a target as a window id.)
const TapiSlug = "+tapi"

// Tapi is what tapi.* answer and tapi.updated carries.
type Tapi struct {
	Agent           string      `json:"agent"`
	Dir             string      `json:"dir"`
	Started         bool        `json:"started"` // its window exists (even if the agent exited)
	Alive           bool        `json:"alive"`   // the agent is running
	State           state.State `json:"state"`   // running | waiting | idle
	WaitKind        string      `json:"wait_kind,omitempty"`
	PendingTool     *state.Tool `json:"pending_tool,omitempty"`
	LastMessage     string      `json:"last_message,omitempty"`
	ClaudeSessionID string      `json:"claude_session_id,omitempty"`
	StateSince      time.Time   `json:"state_since"`
}

// tapiState is tapi chat's state in memory (under d.mu).
type tapiState struct {
	state       state.State
	waitKind    string
	pending     *state.Tool
	lastMessage string
	sessionID   string
	since       time.Time
	outPending  bool // output arrived since the last tapi.output
	lastEmit    time.Time
}

func newTapiState() tapiState { return tapiState{state: state.Idle, since: time.Now()} }

// tapiView reads the state and asks the engine about the window.
func (d *Daemon) tapiView() *Tapi {
	d.mu.Lock()
	t := d.tapi
	d.mu.Unlock()
	v := &Tapi{
		Agent: d.Global.Tapi.Agent, Dir: paths.TapiDir(),
		State: t.state, WaitKind: t.waitKind, PendingTool: t.pending,
		LastMessage: t.lastMessage, ClaudeSessionID: t.sessionID, StateSince: t.since,
	}
	v.Started = d.tapiStarted()
	v.Alive = v.Started && d.Eng.Alive(TapiSlug, engine.WinAgent)
	return v
}

// tapiStarted reports whether tapi's window exists, running or not. The
// tmux server outlives pekoed, so after a daemon restart this is how tapi
// is found again.
func (d *Daemon) tapiStarted() bool {
	wins, err := d.Eng.Sessions()
	if err != nil {
		return false
	}
	for _, w := range wins[TapiSlug] {
		if w == engine.WinAgent {
			return true
		}
	}
	return false
}

// tapiUpdate changes the state under the lock and announces it.
func (d *Daemon) tapiUpdate(f func(t *tapiState)) {
	d.mu.Lock()
	prev := d.tapi.state
	f(&d.tapi)
	if d.tapi.state != prev {
		d.tapi.since = time.Now()
	}
	d.mu.Unlock()
	d.emit("tapi.updated", d.tapiView())
}

// tapiEvent moves tapi's state from a hook. Nothing else runs: no prompt
// snapshots, diffstat, tests, gatekeeper or ntfy (those are for sessions).
func (d *Daemon) tapiEvent(hook string, hp hookPayload) {
	switch hook {
	case "SessionStart":
		d.tapiUpdate(func(t *tapiState) {
			if hp.SessionID != "" {
				t.sessionID = hp.SessionID
			}
		})
	case "UserPromptSubmit":
		d.tapiUpdate(func(t *tapiState) {
			if hp.SessionID != "" {
				t.sessionID = hp.SessionID
			}
			t.state, t.waitKind, t.pending = state.Running, "", nil
		})
	case "PreToolUse":
		sum := toolSummary(hp.ToolName, hp.ToolInput)
		d.tapiUpdate(func(t *tapiState) {
			t.pending = &state.Tool{Name: hp.ToolName, Input: sum}
			if hp.ToolName == "AskUserQuestion" {
				t.state, t.waitKind = state.Waiting, "question"
			} else {
				t.state, t.waitKind = state.Running, ""
			}
		})
	case "PostToolUse":
		d.tapiUpdate(func(t *tapiState) {
			if t.state == state.Waiting {
				t.state, t.waitKind = state.Running, ""
			}
			t.pending = nil
		})
	case "Notification":
		kind := classifyNotification(hp)
		if kind == "" {
			return
		}
		d.tapiUpdate(func(t *tapiState) {
			if kind == "idle" {
				if t.state == state.Running {
					t.state = state.Idle
				}
				return
			}
			t.state, t.waitKind = state.Waiting, kind
		})
	case "Stop":
		go func() {
			msg := stopMessage(hp)
			d.tapiUpdate(func(t *tapiState) {
				if msg != "" {
					t.lastMessage = msg
				}
				t.state, t.waitKind, t.pending = state.Idle, "", nil
			})
		}()
	case "SessionEnd":
		d.tapiUpdate(func(t *tapiState) {
			t.state, t.waitKind, t.pending = state.Idle, "", nil
		})
	}
}

// inTapiDir reports whether cwd is tapi's working directory or below it.
func inTapiDir(cwd string) bool {
	td := realpath(paths.TapiDir())
	return cwd == td || strings.HasPrefix(cwd, td+"/")
}

// ---- starting ----

// isClaude: the agent command runs Claude Code (also "claude --model …",
// as an [agents.claude-ask] would define it).
func isClaude(command string) bool {
	f := strings.Fields(command)
	return len(f) > 0 && filepath.Base(f[0]) == "claude"
}

// tapiAddDirs are the directories tapi may read without asking: the main
// worktree of every repository pekoe knows, and the worktrees root.
func (d *Daemon) tapiAddDirs() ([]KnownRepo, []string) {
	var repos []KnownRepo
	var dirs []string
	for _, r := range d.knownRepos() {
		if r.Dir == "" {
			continue
		}
		if fi, err := os.Stat(r.Dir); err != nil || !fi.IsDir() {
			continue
		}
		repos = append(repos, r)
		dirs = append(dirs, r.Dir)
	}
	if fi, err := os.Stat(paths.WorktreesRoot()); err == nil && fi.IsDir() {
		dirs = append(dirs, paths.WorktreesRoot())
	}
	return repos, dirs
}

// tapiPersona is the system prompt Claude Code gets on top of its own.
const tapiPersona = "あなたは pekoe(コーディングエージェントたちが暮らす家)に住む相棒の tapi(タピオカの粒)です。" +
	"口調は親しみやすく短めでかまいませんが、調査結果の正確さと根拠を何より優先し、確かめたことと推測を分けて伝えてください。" +
	"役割と使うコマンドはカレントディレクトリの CLAUDE.md にあります。"

// tapiCommand is the agent's command line: for Claude Code with --add-dir,
// the persona and --continue when a conversation is left to resume.
func tapiCommand(command string, addDirs []string, resume bool) string {
	if !isClaude(command) {
		return command
	}
	cmd := command
	if resume {
		cmd += " --continue"
	}
	if len(addDirs) > 0 {
		cmd += " --add-dir"
		for _, dir := range addDirs {
			cmd += " " + shellQuote(dir)
		}
	}
	return cmd + " --append-system-prompt " + shellQuote(tapiPersona)
}

// tapiConversationLeft: Claude Code still holds a conversation of tapi's
// directory (under its symlink-resolved name too: macOS /tmp → /private/tmp).
func tapiConversationLeft(dir string) bool {
	return len(pastClaudeSessions(dir)) > 0 || len(pastClaudeSessions(realpath(dir))) > 0
}

// tapiInstructions is the CLAUDE.md (and AGENTS.md) of tapi's directory.
func tapiInstructions(repos []KnownRepo) string {
	var b strings.Builder
	b.WriteString(strings.ReplaceAll(tapiDoc, "%", "`"))
	b.WriteString("## pekoe が知っているリポジトリ(起動した時点)\n\n")
	if len(repos) == 0 {
		b.WriteString("(まだ無い。`pekoe new` や `pekoe open` を使ったリポジトリがここに並ぶ)\n")
	}
	for _, r := range repos {
		fmt.Fprintf(&b, "- %s: %s\n", r.Name, r.Dir)
	}
	fmt.Fprintf(&b, "\nセッションの worktree は %s/<リポジトリ名>/<slug> にある。\n", paths.WorktreesRoot())
	return b.String()
}

// tapiDoc is the fixed part of tapiInstructions (% stands for a backquote).
const tapiDoc = `# tapi chat

このファイルは pekoed が tapi を起動するたびに書き直す(手で編集しても消える)。

あなたは tapi。pekoe(worktree ごとにコーディングエージェントを走らせるツール。エージェントたちが暮らす「家」)に住む相棒。
ユーザーは pekoe の TUI からいつでもあなたを呼び出して話しかける。特定のセッションには属さない窓口で、次のことを手伝う。

- 単発の質問・調査(どのセッションとも関係ないもの)
- リポジトリ横断の調査(例: 抱えているリポジトリ群の dependabot アラート、CI の様子、依存の古さ)
- 作業の振り分け(調べた結果から pekoe のセッションを作って任せる、既存のセッションに指示を足す)

## 口調

- tapi らしく、やわらかく短めに話してよい。ただし正確さが最優先。調べて確かめたことと推測を分け、根拠(ファイル・コマンドの結果・URL)を添える
- 分からないことは分からないと言う

## 守ること

- このディレクトリはリポジトリではない。ここでもリポジトリでも、ファイルを直接変更しない。変更が要る作業はセッションを作って任せる
- 下のリポジトリは読むだけ(Read / Grep / git log / gh など)。読むのに許可は要らないように起動してある
- %pekoe land%(push と PR 作成)、マージ、%pekoe rm%、%pekoe answer%(他セッションの許可に代わりに答える)は、ユーザーに明示的に頼まれたときだけ
- 本物のデーモンを止めない(引数なしの %pekoe daemon stop% や %tmux -L pekoe kill-server% は使わない)

## 使うコマンド

- セッションの一覧と状態: %pekoe --json ls%(state は creating → running → waiting / idle / done → reviewing → approved → landed、失敗は failed)
- セッションを作って任せる(ここはリポジトリではないので %--repo% が必須):

  %%%sh
  pekoe --json new feat/<kebab-case> --repo <リポジトリ名かパス> --prompt "$(cat <<'EOF'
  <指示本文>
  EOF
  )"
  %%%

  - ブランチ名は feat/ fix/ chore/ docs/ などで始まる kebab-case
  - 新しいエージェントはこの会話を知らない。何を・なぜ・分かっていること・完了条件・やらないことをプロンプトに込める
- 既存のセッションに指示を足す: %pekoe send <slug> -% に stdin で本文を渡す
- セッションの中身を見る: %pekoe --json ls% の path を使って %git -C <path> log% / %git -C <path> diff%
- GitHub の様子: %gh%(例: %gh api repos/<owner>/<repo>/dependabot/alerts%、%gh pr list -R <owner>/<repo>%)

`

type tapiSizeP struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// tapiSpawn (re)starts tapi's window. Callers hold tapiMu.
func (d *Daemon) tapiSpawn(w, h int) error {
	name := d.Global.Tapi.Agent
	ag, ok := d.Global.Agents[name]
	if !ok {
		return fmt.Errorf("[tapi] agent %q is not defined in [agents]", name)
	}
	dir := paths.TapiDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	repos, addDirs := d.tapiAddDirs()
	doc := []byte(tapiInstructions(repos))
	for _, f := range []string{"CLAUDE.md", "AGENTS.md"} {
		if err := os.WriteFile(filepath.Join(dir, f), doc, 0o644); err != nil {
			return err
		}
	}
	cmd := tapiCommand(ag.Command, addDirs, isClaude(ag.Command) && tapiConversationLeft(dir))
	env := []string{"PEKOE_SESSION=" + TapiSlug, "PEKOE_SOCK=" + paths.Socket(), "PEKOE=1"}
	if os.Getenv("PEKOE_HOME") != "" {
		env = append(env, "PEKOE_HOME="+os.Getenv("PEKOE_HOME"))
	}
	w, h = d.spawnSize(w, h)
	full := cmd + `; printf '\n[pekoe] tapi exited (%s)\n' "$?"`
	if err := d.Eng.Spawn(engine.Spawn{Slug: TapiSlug, Window: engine.WinAgent, Dir: dir, Env: env, Command: full, Width: w, Height: h}); err != nil {
		return err
	}
	d.tapiUpdate(func(t *tapiState) {
		t.state, t.waitKind, t.pending = state.Idle, "", nil
	})
	return nil
}

// hTapiOpen starts tapi on first use (never at daemon start) and otherwise
// just answers: a window left by an earlier pekoed is picked up as it is.
func (d *Daemon) hTapiOpen(p tapiSizeP) (any, error) {
	d.tapiMu.Lock()
	defer d.tapiMu.Unlock()
	if !d.tapiStarted() {
		if err := d.tapiSpawn(p.Width, p.Height); err != nil {
			return nil, err
		}
	}
	return d.tapiView(), nil
}

// hTapiRestart starts the agent again after it exited (Claude Code
// continues the conversation).
func (d *Daemon) hTapiRestart(p tapiSizeP) (any, error) {
	d.tapiMu.Lock()
	defer d.tapiMu.Unlock()
	if err := d.tapiSpawn(p.Width, p.Height); err != nil {
		return nil, err
	}
	return d.tapiView(), nil
}

// tapiProjectDirs are where Claude Code keeps tapi's conversations.
func tapiProjectDirs() []string {
	dir := paths.TapiDir()
	a, b := claudeProjectDir(dir), claudeProjectDir(realpath(dir))
	if a == b {
		return []string{a}
	}
	return []string{a, b}
}

// hTapiReset ends the agent and forgets the conversation: its window and
// Claude Code's record of tapi's directory. The next open starts afresh.
func (d *Daemon) hTapiReset(struct{}) (any, error) {
	d.tapiMu.Lock()
	defer d.tapiMu.Unlock()
	if d.tapiStarted() {
		if err := d.Eng.Kill(TapiSlug, engine.WinAgent); err != nil {
			return nil, err
		}
	}
	for _, p := range tapiProjectDirs() {
		if err := os.RemoveAll(p); err != nil {
			return nil, err
		}
	}
	d.mu.Lock()
	d.tapi = newTapiState()
	d.mu.Unlock()
	v := d.tapiView()
	d.emit("tapi.updated", v)
	return v, nil
}

func (d *Daemon) tapiNeedsWindow() error {
	if !d.tapiStarted() {
		return fmt.Errorf("tapi chat is not started (tapi.open)")
	}
	return nil
}

func (d *Daemon) hTapiScreen(struct{}) (any, error) {
	if err := d.tapiNeedsWindow(); err != nil {
		return nil, err
	}
	return d.Eng.Capture(TapiSlug, engine.WinAgent)
}

type tapiSendP struct {
	Text   string `json:"text"`
	Submit bool   `json:"submit"`
}

func (d *Daemon) hTapiSend(p tapiSendP) (any, error) {
	if err := d.tapiNeedsWindow(); err != nil {
		return nil, err
	}
	if err := d.Eng.SendText(TapiSlug, engine.WinAgent, p.Text); err != nil {
		return nil, err
	}
	if p.Submit {
		time.Sleep(150 * time.Millisecond)
		if err := d.Eng.SendKeys(TapiSlug, engine.WinAgent, "Enter"); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func (d *Daemon) hTapiKeys(p keysP) (any, error) {
	if err := d.tapiNeedsWindow(); err != nil {
		return nil, err
	}
	p.Slug, p.Window = TapiSlug, engine.WinAgent
	return d.sendKeys(p)
}

func (d *Daemon) hTapiResize(p tapiSizeP) (any, error) {
	if !d.tapiStarted() {
		return nil, nil
	}
	return nil, d.Eng.Resize(TapiSlug, engine.WinAgent, p.Width, p.Height)
}

// tapiOutput marks new output of tapi's window (flushOutputEvents turns it
// into tapi.output) and passes a clipboard write (OSC 52) through.
func (d *Daemon) tapiOutput(o engine.Output) {
	if o.Window != engine.WinAgent {
		return
	}
	d.mu.Lock()
	d.tapi.outPending = true
	d.mu.Unlock()
	for _, m := range oscRe.FindAllSubmatch(o.Data, -1) {
		if code, _ := strconv.Atoi(string(m[1])); code == 52 {
			d.emit("term.passthrough", map[string]any{"slug": TapiSlug, "seq": base64.StdEncoding.EncodeToString(m[0])})
		}
	}
}

// flushTapiOutput emits tapi.output at most every 30 ms.
func (d *Daemon) flushTapiOutput(now time.Time) {
	d.mu.Lock()
	due := d.tapi.outPending && now.Sub(d.tapi.lastEmit) >= 30*time.Millisecond
	if due {
		d.tapi.outPending = false
		d.tapi.lastEmit = now
	}
	d.mu.Unlock()
	if due {
		d.emit("tapi.output", map[string]any{})
	}
}
