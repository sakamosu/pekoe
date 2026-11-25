# Configuration

The long form of [Configuration](../README.md#configuration) in the README: the per-repository settings and every section of `config.toml`, with notes.

`~/.config/pekoe/repos/<repo-id>.toml` (the repo-id is the normalized origin URL, so the same settings apply after a fresh clone; `pekoe init` can draft it):

```toml
agent = "claude"                  # default agent

[worktree]
copy  = [".env*", ".envrc"]
clone = ["node_modules"]          # APFS clone (cp -c)
base  = "main"                    # default: origin's HEAD or main

[hooks]
setup    = ["mise install", "pnpm install --offline", "createdb app_{{slug}}"]
dev      = "pnpm dev --port {{port}}"
dev_stop = "docker compose down"  # when pekoe stops dev (pekoe dev, rm, closing the dev window, --kill-agents) or it ends by itself
test     = "pnpm test"
teardown = ["dropdb app_{{slug}}"]

[env]
PORT = "{{port}}"
DATABASE_URL = "postgres:///app_{{slug}}"

[review]
gatekeeper = "claude -p 'list risky spots as path:line: text'"   # gets the diff on stdin

[merge]
method = "merge"                  # how pekoed merges when you answer "merge" in the inbox: merge | squash | rebase
```

Variables are `{{slug}}` `{{branch}}` `{{port}}` `{{path}}` `{{repo}}`. setup runs in order and stops at the first failure, shown in red in the left pane. When a value is put into a `[hooks]` command, values containing characters the shell treats specially (spaces, `$`, `` ` ``, `;` and so on) are single-quoted and passed as one word (ordinary values like `feat/x` are left as they are). pekoe adds the quotes when needed, so **do not put `{{…}}` inside quotes of your own** (`createdb app_{{branch}}`, not `createdb "app_{{branch}}"`: inside double quotes the single quotes do nothing). `pekoe new` refuses branch names with characters the shell treats specially (`$` `(` `)` `;` `|` `{` `}`, spaces and so on). Values in `[env]` are never quoted.

The files in `[worktree] copy` / `clone` come from the main checkout. A pattern that matches nothing there, or a file there that is empty (0 bytes), is a warning: under the env step of `pekoe new` and the New session dialog, in `warnings` of `pekoe --json ls`, and as "N warnings at creation" in the agent panel's title. The session is created all the same.

`dev` runs in a tmux window of its own, in pekoed's sandbox. Its output also goes to a file (`dev_log` in `pekoe --json ls`; `pekoe dev --log` prints its end), without escape sequences; output printed while pekoed is down is only in the window. `dev` in `pekoe --json ls` is true while the hook's process runs: when it ends by itself it turns false, and `dev_exit` says how (`status`, `signal`, `at`; `stopped` when pekoe stopped it; `dev_stop` with the result of `[hooks] dev_stop`). `dev_stop` is for what `dev` started outside its own process (`docker compose up -d` in a Makefile): pekoed runs it as it runs `dev` (same worktree, environment and sandbox) when it stops a running hook (`pekoe dev`, closing the dev window, `x`, rm, the teardown after a merge, `pekoe daemon stop --kill-agents`) and when the hook ends by itself (with any exit status: a `make dev` that failed halfway may have started containers; so `dev` should stay in the foreground, not start things and exit). rm, the teardown after a merge and `--kill-agents` run it again for a session whose dev hook ran and whose last `dev_stop` did not succeed (failed, or never ran: a hook that ended while an older pekoed ran, the worktree gone); a session that never started `dev` runs none. When a session goes with its `dev_stop` still not succeeded, pekoe keeps its port from new sessions for 7 days (what `dev` left may hold ports derived from it, such as a Postgres on port + 20000); `pekoe rm` says so, and the hold ends by itself after the 7 days (clean up what was left, e.g. `docker compose -p <project> down`, before then). A repository with no `dev_stop` holds no port. A plain `pekoe daemon stop` leaves the dev servers running, so it runs no `dev_stop`. `teardown` stays for the worktree (rm and after a merge). When `pekoe dev` finds no `[hooks] dev`, the error says what the file holds: missing, unparsable, no `[hooks]` table, or the keys `[hooks]` has.

`~/.config/pekoe/config.toml` (global; `pekoe config edit` or `^q ,` in the TUI opens it, `pekoe config show` prints every setting with the value in use):

```toml
animate = true              # animate state icons
[http]
port = 47777                # for the Chrome extension (127.0.0.1)
[ports]
base = 3001                 # first port of the port ledger
[keys]
prefix = "ctrl+q"           # key to leave input: ctrl+ a letter (not i / m), ctrl+@ \ ] ^ _, F1-F20 or alt+ a key; C-a / Ctrl+A / ^a also read as ctrl+a
[review]
surface = "nvim"            # nvim | tui
[picks]
auto_send = true            # send picks with an instruction right away to the session that owns the page's port
[web]                       # Web UI (make install WEB=1 builds only, docs/web.md)
enable        = true
allow_users   = ["you@github"]      # Tailscale login names
allow_devices = ["iphone-15"]       # optional: only these devices
hostname      = "pekoe"             # machine name on the tailnet
confirm_tapi_send = true            # confirmation sheet before a send to tapi; false sends at once (no restart needed)
[gui]                       # local GUI on 127.0.0.1 (pekoe gui, docs/gui.md)
enable      = true          # false refuses pekoe gui; nothing listens before the first pekoe gui either way
port        = 0             # 0: a free port each time it starts listening; read when it starts (pekoe gui --stop, then pekoe gui)
session_ttl = "12h"         # a browser's sign-in lapses after this long with no action (the screen refreshing doesn't count)
browser     = ""            # macOS app to open it with (open -a), e.g. "Safari"; empty: the default browser
[theme]
mode  = "auto"              # truecolor | ansi | none | auto
icons = "nerd"              # nerd | unicode | ascii
ambiguous = "auto"          # how the terminal counts ambiguous-width glyphs (● · ─): auto (measured when the TUI opens) | narrow (one cell) | wide (two cells: frames + - |, symbols ... -> <-, ascii icons and tapi; read when the TUI opens; docs/ambiguous-width.md)
[theme.colors]              # override by role (tokens in docs/ui-guidelines.md)
primary = "#ff87da"
[theme.ansi]                # with mode = "ansi": a color number (0-255) per role (docs/ui-guidelines.md)
primary = 5
[mascot]                    # the mascot tapi (docs/mascot.md)
shape   = "auto"            # auto | round (sextant blocks) | block (quadrant blocks)
glyphs  = "auto"            # auto (ascii when [theme] ambiguous is wide) | unicode | ascii (narrow, a former value, is no longer needed: it reads as auto)
palette = "dark"            # dark (milky pearl, for dark backgrounds) | light (caramel pearl, for light backgrounds)
splash  = true              # splash screen at startup
sleep_after = "3m"          # tapi falls asleep when every session is idle and no key was pressed for this long (at least 10s)
[mascot.colors]             # override hi / bd / sh / eye
bd = "#e4c49a"
[agents.claude]
sandbox = true              # pass pekoe's sandbox settings (--settings) to Claude Code; false to not
model   = "claude-opus-5-5" # passed as --model on every start (new, resume, R, tapi); unset follows ~/.claude/settings.json
effort  = "medium"          # passed as --effort: low | medium | high | xhigh | max
[agents.codex]              # agents other than Claude Code (get no sandbox settings)
command = "codex"
allow   = "y"
deny    = "n"
[tapi]                      # tapi chat (t in the TUI)
agent = "claude"            # a name from [agents.*]; agents other than claude get no --add-dir (cwd only)
[orchestrator]              # tapi as the orchestrator (docs/orchestration.md)
notify = "escalate"         # tapi waiting and the inbox's questions escalate like a session's wait (toast → blink → desktop notification) | "quiet": bottom line / message line only
inbox_popup = true          # a new question an agent waits on comes up in front as the answer dialog (once; skip with esc / ↵ and it stays in the inbox for ]a). Not with notify = "quiet"
deliver = "off"             # "idle": when tapi is free, pekoed types one line into its input box with its sessions' reports, failed, waiting and ci and the inbox's answers to it (for an agent without pekoe wait). Not while you type into tapi chat or a permission dialog is up, and not what tapi's pekoe wait takes (or took); at most once per 30 s
max_running = 0             # at most this many sessions at work at once (creating, running, waiting); more pekoe new are queued and start oldest first. 0: no cap
[notify]                    # when the TUI tells you something waits (a session's wait; tapi's and the inbox's too unless notify = "quiet")
toast_for   = "5s"          # how long a toast shows (PR reactions too; at least 1s)
blink_after = "30s"         # the bottom line's speech blinks after a wait this long (at least 1s)
os_after    = "2m"          # a desktop notification after a wait this long, while the terminal is not focused (at least 10s)
[pr]                        # pekoed's polling of the sessions' PRs
poll_every  = "1m"          # ask GitHub about each open PR at most this often (at least 30s)
[cleanup]                   # pekoe cleanup / :cleanup
stale_after = "72h"         # list a stopped session (done, idle, failed …) as stale after this long without a change (a Go duration)
[tips]                      # tips (4's empty view and tapi chat's greeting)
enable = true               # false hides both
[sandbox]                   # commands from worktrees ([hooks], gatekeeper) and pekoed's git run in a sandbox
enable = true               # false runs them without one (where none works, they are not run; git runs anyway)
[voice]                     # tapi says state changes aloud (macOS only; docs/voice.md)
enable = false              # off by default: while off nothing is said, no backend runs and nothing plays
lang = "auto"               # auto (LC_ALL, LC_MESSAGES, LANG) | ja | en
backend = "say"             # "say" (bundled, macOS's say) or a name in [voice.backends]; PATH is never searched
fallback = "say"            # used when backend cannot be; "" for none
focus_mode = "read"         # the session shown in the TUI: read | narrate | mute (read also reads Claude Code's replies aloud)
background_mode = "narrate" # the other sessions: read | narrate | mute
[voice.read]                # how much of a reply read says (code, tables, URLs and paths are skipped)
max_sentences = 5           # of one message (1-50)
max_turn_sentences = 10     # of one turn (1-100; 800 characters at most whatever this says)
skip = ["code", "table", "url", "path"]   # what read skips (these four by default; docs/voice.md)
[voice.phrases]             # override what tapi says: wait done error ask idle_all and wait_many … ({name} / {names})
wait = "{name} needs you"
[voice.backends]            # external backends: name = absolute (or ~/) path; only these are run
voicevox = "/opt/homebrew/bin/pekoe-voice-voicevox"
[voice.say]                 # the bundled say's voices; "" is Kyoko (or the first ja_JP voice) / the system's voice
ja = "Kyoko"
```

The durations (`[notify]`, `[mascot] sleep_after`, `[pr] poll_every`, `[cleanup] stale_after`, `[gui] session_ttl`) are Go durations (`30s`, `2m`, `1h30m`); an empty or unreadable value is the default, one below the minimum is the minimum.

Saved changes apply without a restart: pekoed reads `config.toml` again within a second of a change, and the TUI follows. `[voice]` is taken up as a whole: a change stops what is being said and starts voice again from the states as they are. `[http] port`, `[web]` and `[sandbox]` are the exception: they keep the value pekoed started with until `pekoe daemon restart` (the TUI's message line and `pekoe daemon status` name them). A file that doesn't read leaves the settings as they were, and the TUI says why. `pekoe config check` lists unknown keys and values pekoe doesn't take ([Commands](commands.en.md)).

`[agents.<name>]` takes `command` `resume` `prompt` (`arg` | `paste`) `allow` `always` `deny` `hooks` `sandbox` `model` `effort`. `sandbox` (default `true`) applies only when `command` starts with `claude` ([Letting agents use pekoe: permissions](agent-permissions.en.md)). `arg` puts the initial prompt on the command line after `--` (`claude -- '<prompt>'`), so a prompt starting with `-` is never read as an option; use `paste` for an agent that doesn't accept `--`. Answers to Claude Code's permission menu send `1` (allow) / `2` (always allow) / `Esc` (deny) by default; if its behavior changes, fix them in `[agents.claude]`.

`model` and `effort` pin the model of a Claude Code agent (`command` starting with `claude`; ignored for others): pekoe adds `--model <model> --effort <effort>` every time it starts one (new sessions, `open`, resume, restarts with R, resuming after a daemon restart, and tapi chat). **Set them.** Unset, Claude Code uses its own default, the `"model"` in `~/.claude/settings.json`, and `/model` in any Claude Code session rewrites that file: switching one session to a bigger model would make every session pekoe starts afterwards use it too. With `model` set, `/model` in a session changes only that session until it is restarted (R or resume passes the configured model again). pekoe never writes `~/.claude/settings.json`. Unset stays unset on purpose: pekoe has no model of its own to fall back to (which models an account can use, and what they cost, is the user's call, and a model name built into pekoe would go stale), so it leaves Claude Code's default alone rather than guess. Put the model in `model`, not in `command` (`command = "claude --model …"` with `model` set passes `--model` twice). `pekoe new --model M --effort E` overrides both for one session (kept across resume and restart). A repository picks its agent with `agent` in its `repos/<name>.toml`, so a repository that should run another model gets an agent of its own (`[agents.claude-big] command = "claude"`, `model = "…"`, then `agent = "claude-big"`), and tapi follows `[tapi] agent`. `pekoe --json ls` shows the model each session last answered with (`last_model`, from the conversation's last assistant message) next to the override (`model` / `effort`).
