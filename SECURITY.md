# Security policy

## Reporting a vulnerability

Please **do not open a public issue** for a security problem. Report it privately through GitHub's private vulnerability reporting instead: open the repository's **Security** tab and choose **Report a vulnerability**.

Please include what you found, how to reproduce it, the version (`pekoe version`) and your OS, and whether the default build or the Web UI build (`-tags web`) is affected.

pekoe is maintained by one person in their spare time, so there is no guaranteed response time, but reports are taken seriously.

## Scope

pekoe runs coding agents that execute commands with your user's permissions, so the interesting boundaries are the ones between those agents (or other local processes, or other devices) and the operations meant for a human. Examples of what is in scope:

- **pekoed's unix socket** (`~/.local/share/pekoe/pekoed.sock`): an agent or another local process getting around the human-only checks. pekoed runs outside the agents' sandbox, so without the human token only the few methods agents need are open (the list is under "人間だけの操作" in [docs/api.md](docs/api.md)); any way to make pekoed run a command or read a file outside the agent's reach through them, or to call another method without the token, is in scope
- **The human token** (`~/.local/share/pekoe/human-token`): ways it leaks to agents (for example through their environment), or human-only operations that work without it
- **The sandbox around commands from worktrees** (`[hooks]` test / setup / dev / teardown and the gatekeeper, which pekoed runs from code an agent can change): a way for such a command to read the tokens or `config.toml`, to write pekoe's data or config directories, or to get a command run outside the sandbox, or pekoe running them without a sandbox while `[sandbox] enable` is on. Writing elsewhere in your home is a known limit (see "Letting agents use pekoe: permissions" in the README)
- **The HTTP endpoint on 127.0.0.1** used by the Chrome extension: requests from web pages or other origins that are accepted, or token checks that can be bypassed
- **The Web UI** (`-tags web` builds only, served over the tailnet with tsnet): access by users or devices not in `allow_users` / `allow_devices`, or actions beyond what it is meant to allow
- Content from outside (web pages, console errors, dev server output, GitHub issues) being passed to an agent in a way that bypasses the "untrusted data" framing
- The self-update (`pekoe update`): installing something that doesn't match the release's checksums or provenance

Out of scope: an agent misbehaving within the permissions you gave it, and anything that requires code running as your user with unrestricted access to your files (such code can read the human token; pekoe doesn't sandbox agents, so keep the token from them as described in the README's "Letting agents use pekoe: permissions").

Design notes and the Web UI's threat model are in [docs/api.md](docs/api.md) and [docs/web.md](docs/web.md) (in Japanese).
