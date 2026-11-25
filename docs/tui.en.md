# TUI

The long form of [TUI](../README.md#tui) in the README: the keys, tapi chat and its dashboard, the tutorial, tips and review in detail.

Numbered panels on the left ([1] Sessions, [2] Worktrees ⇄ Browser ⇄ Issues, [3] Changes ⇄ Picks), [4] Agent on the right (the agent's screen itself; it switches to the preview, dev, run and PR windows), and a powerline status with key hints on the bottom line. Below 100 columns the left column becomes a single tab. Sessions are grouped under a heading per repository (a name and a rule). Above them, under its own `orchestrator ━━━` heading, is always tapi (the [tapi row](#tapi-chat)). Click a row to select it.

Session state is shown with icons and colors (tokens in [docs/ui-guidelines.md](ui-guidelines.md)). The mascot tapi at the left of the bottom line shows the most urgent state of all sessions with its face, and the line next to it says who is doing what:

| Face | State | Meaning |
| --- | --- | --- |
| `⨯ ⨯` | error | The agent exited abnormally or a command failed |
| `◕ ◕` | wait | Waiting for input or permission |
| `◉ ◉` | work | Producing output or running a command |
| `^ ^` | done | The turn ended and you haven't looked yet |
| `∙ ∙` | idle | Idle at the prompt |
| `- -` | sleep | Every session has been idle for 3 minutes or more |

A toast appears when a session you aren't looking at goes to wait / error. tapi also reacts once to a PR being merged, CI failing and reviews (details in [docs/mascot.md](mascot.md)).

| Key | Action |
| --- | --- |
| `j` `k` / `↵` | Select a session or the tapi row (wraps at the ends; `g` goes to the tapi row at the top) / enter the right pane and type (every key except `^q` goes to the agent). `↵` on the tapi row opens tapi chat |
| `^q` / `^q` `h` `l` | Leave input and go back to Sessions (change with `[keys] prefix`). Clicking a panel on the left also leaves / focus the left panels / the right pane |
| `i` / `h` `Esc` | Start typing in the Agent panel / go back left |
| `^q` `z` | Make the right pane (4) full width and hide the left / again to restore. Pressed while typing, you return to typing |
| `^q` `<` `>` / `^q` `=` | Narrow / widen the left pane by 4 columns / reset to the default (40%). Width and zoom are kept in `~/.local/share/pekoe/layout.json`, so the TUI after a restart and agents started from the CLI get the same width. Has no effect below 100 columns (stacked layout) |
| `^q` `,` | Edit `config.toml` (`pekoe config edit` in this terminal; the TUI comes back when the editor closes) |
| `^q` `o` | tapi's dashboard in one move (even while typing into tapi chat or an agent, whichever session is selected: it selects the tapi row and focuses [4]; [tapi chat](#tapi-chat)) |
| `^q` `m` | Mute voice / unmute it (`pekoe voice`; stops what is sounding and the queue). Works from any panel and while typing, and you keep typing. The bar shows `voice` while tapi speaks and `muted ^q m` while muted |
| `n` | New worktree + session |
| `t` / `T` | Open tapi chat and talk to it ([tapi chat](#tapi-chat)) / reset tapi chat (asks first) |
| `w` | Choose a repository |
| `]a` `[a` | Next waiting session (including PRs with activity, and tapi when it waits for permission or an answer; the longest waiting first) / back to where you were |
| `]r` | Next session waiting for review |
| `r` | Review surface (the right pane becomes nvim; the agent keeps running behind it) |
| `p` | PR view (checks, reviews, threads; select with `space`, send to the agent with `s`) |
| `v` | Preview window (screenshots of the dev server inside the TUI; `m` mobile width, `c` before/after) → dev window → agent |
| `b` `l` `d` | Open the port in Chrome / land (confirmation with conflict forecast) / dev server |
| `!` | Run a command once in that worktree (run window). A `! <cmd>` or code on the agent's screen becomes the initial value. You can answer prompts and passwords directly; when it ends, `↵` goes back to the agent and `y` tells the agent the result (exit code, or interrupted on Ctrl-C). `x` closes without asking |
| `u` `R` | Undo the last prompt / restart the agent (resume) |
| `s` `i` `↵` | Send / ignore / show details of an error card (errors pekoed found in the dev server or browser). While a card is shown, typing is `a` |
| `x` `D` | End the session (keep the worktree) / delete the worktree (asks first) |
| `/` `?` `:` | Filter / key list (select and Enter to run) / command (`new` `send` `land` `rm` `dev` `undo` `restart` `review` `inbox` `voice` `cleanup` `config` `ext` `tutorial` `q`) |
| `:voice` | What voice said, newest 50 first (`pekoe voice log`): time, result (`spoken` `merged` `muted` …), kind and the sentence; the selected line adds the backend, latency and reason. Grows while open. `j` `k` move, `esc` `q` close. `:voice mute` / `:voice unmute` too |
| `:cleanup` | The cleanup list (`pekoe cleanup`): landed, merged and long-stopped sessions with their warnings (uncommitted files, unpushed commits, an open PR). `space` marks one, `a` marks every landed one, `D` removes the marked ones (or the selected one) after a confirm that names each worktree it deletes and repeats the warnings |
| `H` | Tutorial again (also `:tutorial`, see [Tutorial](#tutorial)) |
| `.` | Next tip (only while a tip is shown in 4's empty view, see [Tips](#tips)) |
| `1`–`4` `Tab` `[` `]` | Move between panels / switch subtabs. Below 100 columns the left tab follows: `2` shows Worktrees (or Issues), `3` Changes |
| `q` | Quit |

- When any session, or tapi chat, waits for a permission, the Permission modal comes up on top of whatever you are looking at, naming the repository and the session (or tapi chat): `y` allow, `n` deny, `A` always allow, `g` go to that session (or open tapi chat), `esc` later. Answering doesn't change what is selected or shown. Several waits come up one after another, the oldest first. It holds back while you type text (into [4], the `/` filter, the Issues search, a pick's instruction: your keys would answer it) and comes up as soon as you leave. A prompt dismissed with `esc` stays down until that session waits again or asks for another tool
- In [2] Issues, `f` cycles assigned to me → all open → by label, and `n` creates a session from the issue
- [2] Browser ties localhost tabs open in Chrome to sessions by port. `E` sends the console errors
- When an element arrives from Chrome, a card appears in the Agent pane (`↵` send, `^s` another session, `^n` new session, `esc` discard). A pick with an instruction is sent right away to the session that owns the page's port (`[picks] auto_send`)
- Sessions touching the same files get a warning icon and the count in the list (`! 2` with unicode icons)
- Passed through to the agent: OSC 52 (clipboard), OSC 8 (links), OSC 9/99/777 (notifications), mouse (SGR), Kitty keyboard protocol (so Shift+Enter and friends arrive; disable with `PEKOE_KITTY=0`)

All keys are listed in [docs/design.md](design.md#cli-コマンドとキーバインド).

## tapi chat

Press `t` in any panel and [4] switches to a conversation with the mascot tapi (tapi chat), ready for typing. tapi is the **orchestrator**: it doesn't write the changes you ask for itself, but creates sessions with `pekoe new` to do them, waits for their reports with `pekoe wait`, and gives you one report back. What it brings to you is only what needs your decision or approval (the inbox). One-off questions that belong to no session and research across repositories ("any dependabot alerts in my repositories?") it answers itself. You can talk to it while the main session is busy. Creating sessions in the TUI and typing into them directly, without tapi, works as before. If tapi's agent does not use `pekoe wait`, `[orchestrator] deliver = "idle"` has pekoed type what piled up (reports and the like) into tapi's input box as one line (`[pekoe] 3 件の変化(…)。詳しくは pekoe --json wait --mine --since …`); it is off by default.

- `^q` (the prefix) closes it. The conversation stays; `t` again continues it. `Esc` goes to the agent (Claude Code's interrupt). `^q` `z` / `^q` `<` `>` `=` work as they do for agents and return you to typing in tapi chat (opened while zoomed it is full width, and the pty is sized to the pane). When a click on the left panels only stopped the typing (tapi chat still shows), `^q` closes it too
- `^q` `o` closes tapi chat and goes straight to the dashboard (whichever session was selected: it selects the tapi row and focuses [4]). From the dashboard, `t` takes you back to tapi chat, typing
- The agent starts the first time you open it (not when pekoed starts). It runs in `~/.local/share/pekoe/tapi/`, where pekoed puts a `CLAUDE.md` describing its role and the commands it uses. The first time, Claude Code asks you to trust the folder
- With Claude Code, the repositories pekoe knows and `~/worktrees` are passed with `--add-dir`, so reading across them doesn't ask for permission each time (added or removed repositories apply from the next start)
- The first row of Sessions is tapi's: its state icon, `tapi`, how long it has been in that state, and the first line of its last reply (e.g. `◉ tapi  wait 2m  Start a session for #198?`). It is always there, under the `orchestrator` heading above the repository headings, and a `/` filter leaves it in place. Select it with `j` `k` (or `g`): `↵` opens tapi chat, `l` / `→` go into the dashboard in [4]. Session keys (`/` `x` `D` `r` `p` `d` `!` `v` …) do nothing on it; the bottom line shows `↵ open tapi` `l dashboard` `T reset tapi` instead
- While the tapi row is selected, [4] is the **dashboard**: what pekoed holds, on one screen, without spending model tokens. From the top: an overview (first what is your turn — open questions and PRs ready to merge — then running, waiting, to merge, open PRs, open requests, then the numbers of `pekoe stats`: today's merges and tokens by kind — output and cache write plain, cache read and input (most of the sum) quiet — `📊 today  2 merged · out 85.0k · cache write 410.0k · cache read 9.80M · in 1.2k`, and the last 7 days' merges as a ▅▁ bar a day like the pipeline's, `📅 7 days  ▁▁ ▁▁ ▁▁ ▅▁ ▁▁ ▁▁ ▅▅  3 merged` (each day's count, `0 0 0 1 0 0 2`, with ascii); `stats unavailable` and why when they cannot be read. Stats read the transcripts, so they are read when the dashboard opens and every 30 seconds only), the inbox by kind (login, approve, decide, merge ready), the pipeline of every session (a colored bar of implement → review → PR → CI → merge (the order the work goes in: the independent review comes before the PR), `▅▅▅▅▅▅▅▅▁▁`, where it is now, `⏳ CI running` or `🔍 in review`, and why a merge condition is missing — `👀 needs re-review` when the PR's head changed what was reviewed, while a head that only rebased the reviewed change keeps the pass; grouped by who asked and which request, merged ones folded quietly at the end, a repository's own checkout left out), your open requests from the ledger (and under them, quietly, what pekoed closed itself as no request in the last 24 hours), the send log (newest first) and what the agents decided themselves (`fyi`). Go in with `4`; `j` `k` choose a row, `↵` answers an inbox item (the same modal as `]a`) or unfolds a row (the full text, each merge condition), `o` opens the item's link (on a pipeline row it lists the evidence of the session's reports — screenshots, videos, logs, the PR's URL; `j` `k` choose one and `↵` opens it: a PNG in place when the terminal draws Kitty graphics, other images, videos, PDFs and text in an app outside (on macOS Preview, QuickTime Player and TextEdit, named; on Linux `xdg-open`'s default), a URL in the browser. Any other file (a script …) is not opened, only shown where it is, and one that is gone says `missing`), `h` / `esc` go back. tapi chat still opens with `↵` on the row (or `t`). `^q` `o` gets you here from anywhere (typing into tapi chat too). The agents' words are drawn as plain text only. Section headings carry an emoji (only ones every terminal draws two cells wide; none with `icons = "ascii"` or `[mascot] glyphs = "ascii"`, where the bar becomes marks like `vv~..`)
- tapi's face is in the title and changes while it thinks or waits for permission. If it starts waiting for permission or an answer while closed, it is treated like a session waiting for you: a toast, `tapi needs you · t` on the bottom line (blinking after 30 seconds), and a desktop notification after 2 minutes while the terminal is not focused. `[orchestrator] notify = "quiet"` brings back the old way (only `chat waits · t` on the bottom line, below a session's wait)
- What you type into tapi is recorded in the ledger of requests (`pekoe ledger`); tapi ties the sessions it starts to it and closes it when done (small talk closes itself as "no request" when tapi's reply ends, and the same text sent twice in a row stays one entry)
- tapi reads the repositories and `~/worktrees` but can't write them (not with Bash, Edit or Write; see [agent-permissions.en.md](agent-permissions.en.md)). Only its own directory is writable
- `T` (or `pekoe tapi reset`) stops it and also deletes Claude Code's record of the conversation. The ledger and the inbox stay in pekoed, and the next tapi reads them and picks up where things are. The Web UI shows its last words on the Now tab, and you can talk to it on the tapi tab (see [web.md](web.md))
- Change the agent with `[tapi] agent` ([Configuration](config.en.md))

## Tutorial

The first time the TUI starts, a short tour walks through the screen in two parts, tapi first: asking tapi (the tapi row, tapi chat, the dashboard, the inbox, and the pipeline up to a PR ready to merge, which you merge yourself), then driving sessions yourself (Sessions and their states, the agent, the new-session dialog, Changes and review, the PR view and land, the key list). One part at a time is put in focus with a card in which tapi explains it; the rest of the screen dims. `↵` / `→` / `l` / `space` next, `←` / `h` / `backspace` back, `esc` / `q` skip. The cards come in English and Japanese: Japanese when the first of `LC_ALL`, `LC_MESSAGES` and `LANG` that is set starts with `ja`, English otherwise. `tab` switches between them during the tour (staying on the step), and the language you pick is used from then on. It runs on sample sessions inside the TUI only: nothing is created in pekoed, and no key reaches an agent while it runs. Your sessions come back when it ends, including what changed meanwhile. That it was seen (or skipped), and the language you picked if you did, is recorded in `~/.local/share/pekoe/tutorial.json`; `H` or `:tutorial` shows it again.

## Tips

pekoe introduces its features one at a time ("this is handy when…"). They appear in two places:

- 4's empty view (no session selected, or a worktree being created; not on the tapi row, which shows the dashboard). Under tapi's face and caption, it shows a `tip` heading, a one-line summary and a longer explanation. `.` shows the next tip. If there isn't enough height or width, the longer explanation goes first, and if it still doesn't fit nothing is shown
- tapi chat's greeting in its first reply. Ask "give me a tip" for the next one, or "tell me more" and tapi reads the doc the tip points to and explains (tapi uses `pekoe tip` and `pekoe tip --doc`)

Shown tips are recorded in `~/.local/share/pekoe/tips.json`, and none repeats until all have been shown (shared by the TUI and tapi chat). `[tips] enable = false` turns both off. List them with `pekoe tip --list`; the content is in `internal/tips/tips.toml`. Tip texts are in Japanese (the tutorial's cards in English or Japanese, everything else in the UI in English).

## Review

- A session that becomes `done` enters the review queue, and the Stop hook runs `test` automatically. A test command that isn't there (the shell's exit 127, "command not found") shows `test could not run` with a warning mark instead of ✗: the test didn't run, it didn't fail. Remove (or comment out) `test` in `repos/<id>.toml` and the last result goes away at the next Stop
- Approval is per hunk (identified by path + a hash of the change, so it survives shifted line numbers)
- `S` (`:PekoeSend` in nvim) sends all unsent comments as one message and records a review point `refs/pekoe/<slug>/review-<n>`. The next review shows only the diff from there
- Each hunk shows which prompt it came from (`← prompt #3: …`)
- `?` asks the agent about a hunk's intent; the answer is cut out by the Stop hook and shown
- `pekoe review gh#123` reviews someone else's PR in the same view. Comments go to a GitHub review through `gh api`
- Put a read-only agent command in `[review] gatekeeper`, and a list of risky spots shows up in the Comments panel and in nvim's `:PekoeRisks` (quickfix)

