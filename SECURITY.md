# Security policy

## Reporting a vulnerability

Please **do not open a public issue** for a security problem. Report it privately through GitHub's private vulnerability reporting instead: open the repository's **Security** tab and choose **Report a vulnerability**.

Please include what you found, how to reproduce it, the version (`pekoe version`) and your OS, and whether the default build or the Web UI build (`-tags web`) is affected.

pekoe is maintained by one person in their spare time, so there is no guaranteed response time, but reports are taken seriously.

## Scope

pekoe runs coding agents that execute commands with your user's permissions, so the interesting boundaries are the ones between those agents (or other local processes, or other devices) and the operations meant for a human. Examples of what is in scope:

- **pekoed's unix socket** (`~/.local/share/pekoe/pekoed.sock`): an agent or another local process getting around the human-only checks. pekoed runs outside the agents' sandbox, so without the human token only the few methods agents need are open (the list is under "人間だけの操作" in [docs/api.md](docs/api.md)). `land` is one of them on purpose: an agent whose sandbox allows GitHub can push and open a PR by itself, and merging stays with a person (pekoed merges a PR only when a person answers "merge" to pekoed's own merge-evidence item in the inbox, with the token; an agent making pekoed merge without that answer — by forging such an item, answering it, or getting a different head merged — is in scope), so an agent pushing its session's branch and opening a PR through `land` is not a finding (landing in a repository without an origin, which fast-forwards the base branch, still needs the token). So is `worktree.rm` of a landed session: pekoed already removed its worktree when it saw the merge, so an agent removing it only clears pekoe's record (any other state, `force`, or a landed session whose worktree is still on disk needs the token); any way to make pekoed run a command or read a file outside the agent's reach through them, or to call another method without the token, is in scope
- **The human token** (`~/.local/share/pekoe/human-token`): ways it leaks to agents (for example through their environment, or past the sandbox settings pekoe passes to Claude Code agents while `[agents.claude] sandbox` is on and the settings in your own `~/.claude/settings.json` don't loosen them), or human-only operations that work without it
- **The sandbox around commands from worktrees** (`[hooks]` test / setup / dev / dev_stop / teardown and the gatekeeper, which pekoed runs from code an agent can change): a way for such a command to read the tokens or `config.toml`, to write pekoe's data or config directories, or to get a command run outside the sandbox, or pekoe running them without a sandbox while `[sandbox] enable` is on. Writing elsewhere in your home, writing the repository's own `.git/hooks` and `.git/config` (left open for husky and the like; pekoed reports changes to them in the inbox) and connecting to other unix sockets (Docker, ssh-agent) are known limits (see "Letting agents use pekoe: permissions" in the README)
- **The git pekoed runs** (diffstat, snapshots, review, undo, the conflict forecast, land, rm, PR reviews, and the `gh` it starts): a command an agent wrote into git's configuration — through the worktree's `.git` file, the repository's `.git/config` or `config.worktree`, `.gitattributes`, or a repository passed to `pekoe new --repo` by a person (agents can start sessions only in repositories pekoe already knows) — running outside the sandbox, or reading the tokens, while a sandbox is available. Without a sandbox, filter and merge drivers, git-lfs settings and the hooks of a local-path remote are a known limit (see "pekoed が動かす git" in [docs/api.md](docs/api.md)); git you run yourself in a worktree is out of scope
- **The HTTP endpoint on 127.0.0.1** used by the Chrome extension: requests from web pages or other origins that are accepted, or token checks that can be bypassed
- **The Web UI** (`-tags web` builds only, served over the tailnet with tsnet): access by users or devices not in `allow_users` / `allow_devices`, or actions beyond what it is meant to allow
- Content from outside (web pages, console errors, dev server output, GitHub issues) being passed to an agent in a way that bypasses the "untrusted data" framing
- The self-update (`pekoe update`): installing something that doesn't match the release's checksums or provenance

Out of scope: an agent misbehaving within the permissions you gave it, and anything that requires code running as your user with unrestricted access to your files (such code can read the human token). pekoe sandboxes only Claude Code agents, through Claude Code's own sandbox; other agents, Claude Code with `[agents.claude] sandbox = false`, and settings you add to `~/.claude/settings.json` that reopen the paths (`excludedCommands`, `allowRead`) are up to you (see the README's "Letting agents use pekoe: permissions").

Design notes and the Web UI's threat model are in [docs/api.md](docs/api.md) and [docs/web.md](docs/web.md) (in Japanese).
