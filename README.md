# NextBoard

A self-hosted visual board for running and directing AI coding agents.

NextBoard is a single Go binary. Run it on a machine you control, open the web UI, and start Claude Code, Codex, Gemini CLI or any other CLI agent as terminal tiles on an infinite canvas. You can see which agents are working, which need you and which are ready without opening each terminal, and they keep running after you close the browser.

> **Status: early development.** All real-world testing so far was on Linux; macOS builds exist but are untested. APIs and data formats can still change.

## Features

- **Board.** Terminals, notes, checklists, kanban, calendar, shapes, pen, images and file cards on one canvas, synced live between clients, with undo and snapshots.
- **Agents.** Live `WORKING` / `NEEDS YOU` / `READY` status, precise conversation resume, and model and effort per console. An agent can be switched in the same tile without losing its memory. Claude Code and Codex are the most tested; Gemini CLI, OpenCode and custom agents are supported with less testing.
- **Orchestration.** A command bar with `@mentions`, and a lead agent that plans with your own local `claude -p`. Groups, handoffs, result forwarding, loops, task boards and a mission map. The built-in parser understands English only.
- **Project memory.** One memory per project, shared by every CLI, with required critical notes and a galaxy view. A memory tile stacks it with memory from other projects for the agents connected to it, and each agent is only ever sent what it does not have yet.
- **Code workbench.** A Monaco editor card with language servers, debug adapters (Delve, debugpy, built-in Node.js), git staging, stash and conflicts, pull requests through `gh`, review of agent changes, and local history. Open VSX extensions work as data only, such as themes and grammars: their code never runs.
- **Git and web.** A git panel and branch cards, localhost previews, including from SSH machines, and a real browser in a tile for sites that refuse iframes.
- **Desktop apps in a tile** (Linux). GUI apps run on a virtual display and stream to the board.
- **SSH machines.** Agentless: nothing is installed on the server. Uses tmux when available, so sessions survive disconnects.
- **Studios.** Documents to PDF, diagrams, a Markdown vault with a link graph, and a skills manager.
- **And more.** A zero-knowledge credentials vault, uptime monitors, usage and token charts, voice input through the Web Speech API, and notifications.

## Quick start

Install at least one agent CLI, then:

```bash
curl -fsSL https://raw.githubusercontent.com/azamrizki/nextboard/main/install.sh | sh
```

This installs the binary to `~/.local/bin`, checks its checksum, sets up a service (systemd user service on Linux, LaunchAgent on macOS) and prints a one-time setup link, valid for 30 minutes. Open it, create the admin password and name your hub. If you lose the link, run `nextboard setup-link`.

NextBoard listens on `http://127.0.0.1:12312` and keeps its data in `~/.nextboard`. `nextboard update` installs newer releases. Add `-s -- --no-service` after `sh` to install the binary only.

### From source

You need Go 1.26+, Node 22+ and git.

```bash
git clone https://github.com/azamrizki/nextboard.git
cd nextboard
make build
./bin/nextboard serve     # prints the setup link in a terminal
./bin/nextboard install   # or run it as a service
```

Do not use `go install`. It builds a hub without the web UI, because the built bundle is not in the repository.

### Optional tools

| Tool | Used for |
|---|---|
| `tmux` on a server | Sessions that survive SSH disconnects and hub restarts |
| `gh` | Publishing to GitHub and pull requests |
| Language servers and debuggers (`gopls`, `pyright`, `typescript-language-server`, `dlv`, `debugpy`, ...) | The code workbench; each is used when found on `PATH` |
| `sway`, `wayvnc`, `wf-recorder`, `xwayland` | Desktop apps in a tile, with GPU |
| `tigervnc-standalone-server`, `openbox` | Desktop apps in a tile, without GPU |
| `ffmpeg` with VA-API, PipeWire | Hardware video and sound in desktop tiles |

## Security

**NextBoard is effectively a shell on your machine. Do not expose its port to the internet.**

- The hub speaks plain HTTP and binds to `127.0.0.1` by default. For remote access, put it behind HTTPS with nginx (`deploy/nginx.conf.example`) or use a private network such as Tailscale. `tailscale serve` only forwards the main port, so web tiles do not work through it.
- `make dev` binds to `0.0.0.0` by default. On an untrusted network, use `make dev LISTEN=127.0.0.1:12312`.
- Agents run as your user and can read everything your user can, including NextBoard's data folder. Critical notes are instructions to agents, not a technical lock. Blocked commands make the CLI itself ask you first, but they are not a sandbox: an agent with a shell can still write a script that does the same.
- The credentials vault encrypts in the browser and the hub stores only ciphertext. If you forget the master password, the contents are gone. See [docs/API-vault.md](docs/API-vault.md) for what it does not protect against.

Please report security problems through a [private security advisory](https://github.com/azamrizki/nextboard/security/advisories/new), not a public issue.

## CLI

| Command | Purpose |
|---|---|
| `nextboard serve [--listen addr]` | Run in the foreground |
| `nextboard install` / `uninstall [--purge]` | Install or remove the service |
| `nextboard status` | Service state, address, and session, machine and device counts |
| `nextboard logs [-f]` | Service logs |
| `nextboard connlog` | Server connection history: drops, reconnects, latency |
| `nextboard setup-link` | Create a new one-time setup link |
| `nextboard reset-password` | Reset the admin password |
| `nextboard pair` | Print a QR code and pairing code for a device |
| `nextboard update [--repo owner/name]` | Download the latest release and restart the service |
| `nextboard version` | Print the version |

All commands accept `--data-dir` (default `~/.nextboard`, or `$NEXTBOARD_HOME`).

## Development

```bash
make dev        # hub and Vite together; needs air (go install github.com/air-verse/air@latest)
                # web http://localhost:15173, hub http://localhost:12312, data in ./.devdata
make dev-hub    # hub only
make dev-web    # Vite only
make test       # go vet + go test -race
make test-web   # web unit tests; needs web/node_modules
make test-e2e   # browser tests with fake agents; needs `cd web && npx playwright install chromium` once
make test-ssh   # SSH tests against a throwaway local sshd
make build      # web + bin/nextboard
make snapshot   # goreleaser build for linux/darwin, amd64/arm64
```

By default the tests never touch a real agent CLI or your agent plan. `NEXTBOARD_REAL_CLI_TEST=1` turns on tests that do, and these use your real account.

While NextBoard is in development, CI runs only as part of a release, so run the tests yourself before you push. A release is started by hand from GitHub Actions: it runs all the tests, works out the version from the commit messages with [semantic-release](https://semantic-release.gitbook.io/), tags it and publishes the binaries with goreleaser. Commits use [Conventional Commits](https://www.conventionalcommits.org/): `fix:` gives a patch release, `feat:` a minor one, and `docs:`, `test:`, `ci:` and `chore:` no release. Until 1.0, a breaking change (`feat!:`) also gives a minor release.

### Layout

```
cmd/nextboard/      entry point
internal/server/    router, auth middleware, REST, WebSocket
internal/session/   terminals, scrollback, resume
internal/machine/   local PTY and agentless SSH
internal/agent/     agent catalog, status, hooks
internal/orch/      command bar, planner, orders, loops, memory
internal/delivery/  typing orders into consoles
internal/studio/    code workbench and studios
internal/gitsvc/    git panel and GitHub
internal/preview/   web tile proxy
internal/gui/       desktop apps in a tile
internal/vault/     credentials vault storage
web/                React + TypeScript + Vite UI
docs/               specification and API contracts
deploy/             nginx, systemd and launchd examples
```

Android and desktop clients exist as separate projects. They are paused while the hub stabilizes and are not part of this repository.

### Documentation

The detailed docs are written in Indonesian. Start with [docs/PROJECT.md](docs/PROJECT.md).

| Document | Contents |
|---|---|
| [PROJECT.md](docs/PROJECT.md) | Overview: how it works, features, limits, status and known gaps |
| [SPEC.md](docs/SPEC.md) | Original specification: network, auth, SSH |
| [ROADMAP.md](docs/ROADMAP.md) | Phases and design decisions |
| [DESIGN.md](docs/DESIGN.md) | Design system |
| [API.md](docs/API.md), [API-board.md](docs/API-board.md), [API-agents.md](docs/API-agents.md), [API-orchestration.md](docs/API-orchestration.md), [API-studio.md](docs/API-studio.md), [API-gui.md](docs/API-gui.md), [API-vault.md](docs/API-vault.md), [API-uptime.md](docs/API-uptime.md) | HTTP and WebSocket contracts |

## Contributing

Issues and pull requests are welcome. Please run `make test` and `make test-web` before opening a pull request, since CI does not run on it, use [Conventional Commits](https://www.conventionalcommits.org/), and keep the matching `docs/API*.md` accurate when a contract changes.

## License

[MIT](LICENSE) © 2026 Azam Rizki

Third-party components keep their own licenses. Most dependencies are MIT, ISC, BSD or Apache-2.0. Others include noVNC (MPL-2.0), DOMPurify (MPL-2.0 or Apache-2.0), and the Geist, Geist Mono, JetBrains Mono and Caveat fonts (OFL-1.1).

Agent names and logos belong to their owners and are used only to identify those tools. NextBoard is not affiliated with or endorsed by any of them.
