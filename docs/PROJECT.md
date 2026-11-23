# NextBoard Hub: project document

The complete picture of the NextBoard hub: goals, how it works, every feature, system limits, status, and what is not done yet. Read this document first if you are new to the project. Exact JSON shapes and error codes are in `API*.md` (map in §15).

**State as of 2026-11-05.** The numbers here were checked against the code on that date. "Tested" status comes from the verification notes in the docs; the tests were not rerun when this document was written. If this document differs from the code, the code is right.

## Contents

1. [Summary](#1-summary)
2. [Purpose](#2-purpose)
3. [Concepts and terms](#3-concepts-and-terms)
4. [Architecture](#4-architecture)
5. [Features](#5-features)
6. [Security](#6-security)
7. [System limits](#7-system-limits)
8. [Data and persistence](#8-data-and-persistence)
9. [API and events](#9-api-and-events)
10. [Clients](#10-clients)
11. [Development, testing, deployment](#11-development-testing-deployment)
12. [Owner decisions](#12-owner-decisions)
13. [Status and history](#13-status-and-history)
14. [Unfinished work and risks](#14-unfinished-work-and-risks)
15. [Document map](#15-document-map)

## 1. Summary

NextBoard is a self-hosted visual board for AI coding agents. One Go binary (`nextboard`) is installed once on a computer, runs as a service, and serves the Web UI and API. From there the owner runs many agent CLIs (Claude Code, Codex, Gemini CLI, and others) as terminal tiles on an infinite canvas, sees each agent's status, gives orders through one command bar, and works on git, files, web previews, and desktop apps from the same board.

This is an MIT-licensed open-source project with one maintainer, called the owner in this document. It is not a product for sale. There is no multi-tenancy, billing, or cloud relay.

| | |
|---|---|
| Version | `0.2.0` (latest release; dev builds write `0.1.0-dev`) |
| Technology | Go 1.26, React 19 + TypeScript + Vite |
| Size | 36 Go packages, about 70 thousand lines of code and 39 thousand lines of tests (134 files, 751 `Test` functions); about 65 thousand lines of TS/TSX; about 280 HTTP route patterns |
| Address | `127.0.0.1:12312` (plain HTTP, HTTPS provided by nginx); previews `12320–12339` |
| Storage | JSON files in `~/.nextboard/`, no database |
| Build targets | Linux and macOS, amd64 and arm64. Desktop app tiles are Linux only. All recorded real tests were done on Linux |
| Clients | Web UI (embedded in the binary). Android and desktop exist, but are paused (§10) |

## 2. Purpose

### Problems it solves

- Too many terminal tabs and SSH sessions, so it is hard to see which agent is working and which is waiting for input.
- Agents stop when the laptop is closed.
- Agents cannot be watched from a phone.
- Agents and projects are spread over several servers with no one place to see them all.

### Goals

1. **Install once.** One command installs the binary and the service; a one-time setup link finishes the rest.
2. **One board for every agent.** The `WORKING`, `NEEDS YOU`, `READY` states are visible without opening each terminal.
3. **Agents keep running without a client.** A closed browser or a dropped network does not stop work; loops keep running on the hub 24 hours a day.
4. **Watching from anywhere.** Paired browsers, desktop apps, and Android see the same board, with notifications when an agent needs input.
5. **Directing a team of agents in plain language.** One command bar to give orders, split work, forward results, and schedule.
6. **No need to leave the board.** A VS Code/Cursor-class editor, git, previews, and real browsers are all tiles on the board.
7. **Feature parity with Symvibe**, in NextBoard's own design ("Glass", [DESIGN.md](DESIGN.md)), not a copy of its look. The reference feature inventory is an internal note and is not in this repo.

### Non-goals

- Multi-user, multi-tenant, or a service open to the public.
- A cloud-based planner or TTS. The planner uses a local CLI with the owner's plan; voice uses the browser API.
- Left out of Symvibe on purpose: subscription and credits (F5), macOS-only installation (A1), the iCloud banner, Quick Look/Finder, the native WebKit inspector (replaced by a log card).

### Binding principles

These rules shape many technical decisions. Breaking one takes an owner decision (§12).

1. **Agentless on servers.** No binary or service is installed, and no existing config file on the target server is changed. The only requirement is `sshd`.
2. **Agents always on This PC.** CLI state (login, memory, settings) lives in the NextBoard folder; servers are reached through hub tools.
3. **No CLI config is written.** Hooks, MCP, memory, and instructions are given per launch through flags and the environment. `~/.claude` is not touched.
4. **1 project = 1 memory**, whatever the agent or CLI.
5. **Orchestration text enters only through the delivery engine.** Never into a shell, never into a permission prompt.
6. **Destructive actions need confirmation, and "delete" means move to trash** when user files are involved.
7. **Hidden runs are never yolo.** Their tools are read-only, or there are none at all.
8. **The hub is not exposed to the internet.** The hub is essentially a shell into the machine; HTTPS is terminated by nginx on the same host.

### Current priorities

Since 2026-11-02 the focus is the **core backend** (decision 8). Android and desktop are paused until the core is stable, and the owner decides when to resume. Hub changes that affect clients are still allowed, as long as `API*.md` stays accurate.

## 3. Concepts and terms

| Term | Meaning |
|---|---|
| **Hub** | The `nextboard` daemon. Serves the UI and API, stores all state, manages sessions. |
| **Device** | A client that views and controls the board: browser, desktop app, Android app. |
| **Machine** | A computer where processes run. `local` ("This PC") always exists; the others are reached over SSH. |
| **Project** | A folder on a machine, usually a git repo; it may be a parent folder holding many repos. |
| **Workspace** | One board tab inside a project. |
| **Board** | A per-workspace document of nodes and edges, synced through ops. |
| **Node / tile** | One board element. The node is its data, the tile its view. |
| **Session / console** | One terminal (an agent or shell process), shown as a `terminal` node. |
| **Agent** | An AI CLI in a console. Catalog = presets extended with capabilities. |
| **Handle** | A console's name, unique per hub, such as `@Ash`. |
| **Order** | An instruction to one or more consoles, tracked until a report comes back. |
| **Delivery** | One send of text to one console, through a queue with guards. |
| **Thread** | A history of orders and reports: one per project, one per orchestrator card. |
| **Planner** | A local Claude Code that turns sentences into steps. |
| **Group** | A team of consoles inside a frame, with a name, roles, and a color. |
| **Orchestrator card** | A small command bar on the board, scoped to the consoles wired to that card (and the agents inside an editor card wired to it). |
| **Lead agent** | The orchestrator as a person: the owner's assistant and the agents' leader, with a name (default Aruna, the owner's choice 2026-11-08) and a personality. It talks first; work for the team waits for approval. |
| **Runner / job** | A hidden headless CLI for background work: documents, diagrams, memory scans, the planner. |
| **Preview** | A proxy to a dev server on a machine, one listener per port. |
| **Critical notes** | The owner's rules that no agent may break. |

## 4. Architecture

```
                      ┌────────────────────── HUB (nextboard) ──────────────────────┐
 Browser ──┐          │ Router, auth, CSRF    Project, workspace, board (ops)        │
 Desktop ──┤  nginx   │ Session manager       Status engine, transcript              │      This PC
 Android ──┼─ HTTPS ─►│ Orchestrator          Delivery, planner, loop, memory        │ ───► PTY + agent CLI
           │  :443    │ Git, preview, studio  Job runner, usage, MCP                 │      desktop apps
           └──────────│ WebSocket /api/events, terminal, LSP, DAP, RFB, video, audio │
                      └───────────────┬──────────────────────────────┬───────────────┘
                                      │                              │ SSH (agentless)
                               ~/.nextboard/                         ▼
                               JSON, 0600                     Server: shell, tmux, SFTP,
                                                              git, direct-tcpip
```

The hub holds all state. Clients store nothing except per-browser preferences, so web, desktop, and Android get the same result from the same command.

### 4.1 Technology

| Part | Choice |
|---|---|
| Hub | Go, one binary, frontend through `go:embed`, `CGO_ENABLED=0` |
| PTY and SSH | `creack/pty`, `x/crypto/ssh`, `pkg/sftp`, `kevinburke/ssh_config` |
| Screen model | `charmbracelet/x/vt` behind the `internal/screen` package |
| Transport | `gorilla/websocket` |
| Auth | argon2id, Ed25519, AES-256-GCM |
| Frontend | React 19, Vite, TypeScript |
| Canvas | `@xyflow/react`, `roughjs`, `perfect-freehand` |
| Terminal | `@xterm/xterm` (fit, WebGL with fallback) |
| Editor | Monaco (workbench), CodeMirror 6 (Markdown, Skills), `vscode-textmate` + Oniguruma |
| Other | `@novnc/novnc`, `d3-force`, `markdown-it` + `dompurify`, `highlight.js` |

### 4.2 Go packages (`internal/`)

| Package | Lines | Job |
|---|---:|---|
| `orch` | 11156 | Orchestration: parser, planner, steps, threads, groups, handoff, forward, loops, memory, layouts, Mission Map, board control |
| `server` | 9731 | Router, middleware, REST, WebSocket, SPA, hub MCP tools, linked input |
| `machine` | 4884 | `Host` interface; local PTY, SSH, SFTP, exec, probe, dial, pause |
| `studio` | 3753 | Code (files, search, LSP, DAP, extensions), Documents, Diagrams, Markdown |
| `gui` | 3009 | Desktop apps: virtual display, RFB, video, sound, browser |
| `session` | 2816 | Sessions, scrollback ring, manager, agent wrapper |
| `gitsvc` | 2623 | Git, multi-repo, branches, trash, GitHub publish |
| `usage` | 2020 | Usage pill, chime, token index |
| `agent` | 1660 | Catalog, launch, wrapper, prompts, screen rules |
| `preview` | 1651 | Preview proxy, bridge, logs, URL detection, port probe |
| `board` | 1442 | Board document, ops, snapshots, archive |
| `skills` | 1242 | Skills manager, lint, write confirmation, starters |
| `jobs` | 1219 | Job model, queue, hidden runs |
| `transcript` | 1213 | Transcript reader, last reply, tokens |
| `cli` | 880 | Subcommands, systemd/launchd service, pair, update |
| `auth` | 750 | Password, cookies, setup code, pairing, device challenge, rate limit |
| `delivery` | 690 | Delivery engine |
| `mcpreg` | 681 | MCP server registry and its probes |
| `agentstate` | 673 | Status engine (`agentState`) |
| `settings` | 479 | Settings store and validation |
| `project` | 367 | Projects and workspaces |
| `screen` | 338 | Wrapper around the VT screen model |
| `marker` | 335 | OSC marker stripper |
| `connlog` | 298 | Connection log |
| `profiles` | 237 | CLI login profiles, agent state folder |
| `store`, `secret`, `config`, `webui`, `version` | 264 | Atomic JSON, AES-GCM, config, web embed, build metadata |

### 4.3 Web UI (`web/src/`)

| Folder | TS/TSX lines | Contents |
|---|---:|---|
| `studio/` | 12807 | Code workbench, Documents, Diagrams, Markdown, Skills, printing |
| `board/` | 10566 | Canvas, rail, inspector, tiles, op sync, edge routing |
| `orch/` | 6041 | Command bar, threads, queue, Mission Map, loop panel, memory, layouts, composer |
| `memory/` | 2637 | Galaxy page |
| `components/` | 2339 | Glass base components |
| `settings/` | 2008 | Settings panels |
| `agents/` | 1905 | Agent picker, output viewer, resume, yolo, attention |
| `git/`, `lib/`, `gui/`, `web/`, `machines/`, `pages/`, `usage/`, `home/`, `voice/`, `devices/` | 8470 | The rest, as the folder names say |

Pages: `/` (project list), `/p/:projectId/w/:workspaceId` (board), `/machines`, `/devices`, `/memory`, `/settings`, `/print/…`, `/setup`, `/login`.

### 4.4 Main flows

**Agents and status.** The hub runs a shell wrapper that runs the CLI. Claude hooks and Codex notify are given per launch and write `OSC 777;nb;…` markers to the terminal. The hub strips the markers from the stream before it enters scrollback, then the status engine combines markers, per-CLI screen rules, and output activity into `agentState`. When the agent exits, the wrapper drops to a shell in place, so the tile does not die.

**Orders to agents.** Text enters `POST /api/projects/{id}/commands` → local parser → (optional) planner → steps → confirmation policy → execution → thread. A `send` step creates an order; each target gets a delivery that waits for the agent to be ready, types with bracketed paste, then confirms through a state change. When the turn ends, the hub reads the reply from the transcript and writes a report in the thread.

**Board.** Clients send op batches (`put`, `patch`, `del`, `z`, `putEdge`, `delEdge`). The hub applies them atomically, bumps `rev`, and broadcasts them through `/api/events`. Conflicts are resolved last-writer-wins per node. If `rev` jumps, the client reloads the board.

**Background work.** Network git, publish, usage, and runners run as jobs with progress, cancellation, and events. `POST /api/machines/{id}/exec` does not exist on purpose: every user of exec builds its command from a fixed template.

### 4.5 Realtime

| Channel | Contents |
|---|---|
| `/api/events` | Every state change as JSON; `?topics=` filters it to save data |
| `/api/sessions/{id}/ws` | Binary terminal output (the first frame is a scrollback snapshot), input, resize, status |
| `/api/projects/{id}/lsp/{server}/ws` | Language server JSON-RPC |
| `/api/projects/{id}/dap/{adapter}/ws` | Debug Adapter Protocol |
| `/api/gui/sessions/{id}/ws`, `/video`, `/audio` | RFB, AV1/H.264, PCM |

## 5. Features

### 5.1 Installation, service, CLI

`install.sh` detects the OS and architecture, downloads the release, verifies its SHA-256, installs to `~/.local/bin` (or `/usr/local/bin` with `--system`), then runs `nextboard install`. That command creates `~/.nextboard/` (0700), installs a systemd user service with `enable-linger` (Linux) or a LaunchAgent (macOS), and prints a one-time setup link that is valid for 30 minutes. `--instance <name>` (also `install.sh --instance=<name>`) installs another instance instead: `~/.nextboard-<name>`, its own block of ports, the service `nextboard-<name>` (`id.nextboard.<name>`), and its own setup link (§5.1.1).

Setup in the Web UI: claim the hub with the setup code, the hub name and the admin password (at least 8 characters), choose how its agents sign in, optionally copy from another instance, check the detected CLIs, and read the summary with the data folder, config file, service, port and addresses. A default instance with no other instance on the machine keeps the short first run: the admin, then the detected CLIs.

| Command | Purpose |
|---|---|
| `serve [--listen] [--dev]` | Foreground, used by the service |
| `install [--instance] [--listen] [--public-url]` / `uninstall [--instance] [--purge]` | Install or remove the service of an instance |
| `status [--all]` | Service, health, address, counts of sessions, machines, devices |
| `instances [--json]` | Every instance on the machine: state, address, data folder, service, conflicts |
| `logs [-f]` | Service logs |
| `connlog [--machine] [-n] [--json]` | Connection log, works even while the hub is down |
| `setup-link` | A new setup link |
| `reset-password` | Change the admin password, every browser is logged out |
| `pair` | QR and pairing code in the terminal |
| `update [--repo]` | Download the release, verify its checksum, restart every instance's service that runs the binary |
| `version`, `help` | |

All commands accept `--instance <name>` and `--data-dir` (default `~/.nextboard` or `$NEXTBOARD_HOME`). Which folder a command acts on: `--data-dir`, else `--instance` (the folder in that instance's unit, else `~/.nextboard-<name>`), else `$NEXTBOARD_HOME`, then `$NEXTBOARD_INSTANCE` (set in every console of an instance), else `~/.nextboard`. A flag and a folder that belong to different instances are an error.

#### 5.1.1 Several instances on one machine

One OS user can run several hubs, each an **instance** with a name (`default` is `~/.nextboard`), a `hubId`, a data folder, a port block, a service, a cookie and an admin of its own ([API.md Instances](API.md#instances)). One binary serves them all; `update` restarts every instance that runs it. About this hub shows the instance, data folder, config file, service, the other instances (admin only) and **New instance…**, which creates one from the web with the admin password. Every new instance goes through the setup wizard. What stays apart: data, settings, agent logins (by default), projects, consoles, cookies, browser storage, preview ports, tmux sessions and paste folders on a shared server (named with the `hubId`), usage checks, AppArmor and snap/flatpak browser profiles. What is shared: the OS user with its `$HOME`, `~/.ssh`, git and `gh` credentials and CLI binaries, and `~/.claude`/`~/.codex` for an instance that uses your own CLI logins. Instances are not a confidentiality boundary (§6).

### 5.2 Auth and devices

- **Browser:** admin password, an `HttpOnly; SameSite=Strict` cookie for 30 days that is extended on use. The cookie name differs per scheme: `nb_session` (HTTPS, `Secure`) and `nb_session_http`, and per instance: a named instance adds `_<name>` (`nb_session_http_work`), because cookies ignore the port.
- **CSRF:** every request that changes data must carry the `X-NextBoard: 1` header. Cookie-based WebSockets have their `Origin` checked.
- **Rate limit:** 5 failed logins per IP per 10 minutes.
- **Pairing:** a `XXXX-XXXX` code for 5 minutes, single use, as a `nextboard://pair?…` QR. The app creates an Ed25519 keypair (private key in Keystore/Keychain); later logins go through challenge-response and get an opaque 1-hour access token.
- **Revoke** cuts all of that device's WebSockets at once (close code 4401). Tokens are never put in a URL, because reverse proxies log query strings.
- **Roles:** `admin` (browser) and `device`. Some endpoints are admin-only, such as pairing, device management, MCP, and the connection log.

### 5.3 Machines

Add from `~/.ssh/config` (just pick a `Host`) or by hand. Nothing is installed on the server.

- **Auth in order:** `IdentityFile` or the default key → ssh-agent → an uploaded key → password. Uploaded keys and passwords are encrypted and never go back to the client. `ProxyJump` is supported.
- **Host key:** checked against `~/.ssh/known_hosts`. A new host asks for fingerprint confirmation and is stored in `~/.nextboard/known_hosts`. A **changed** host key **is refused**.
- **Probe:** OS, architecture, shell, home, tmux, and the CLIs present. Stored, with a Re-probe button.
- **Connection:** one TCP connection per machine, one SSH channel per terminal, 15-second keepalive (3 failures = disconnected), reconnect at 1/2/5/10/30 seconds.
- **Persistence:** `tmux` (the process lives on when SSH drops or the hub restarts, then is reattached) or `direct` (launched again). Default `auto`. *Clean up* removes orphaned `nb-*` sessions.
- **Files:** SFTP on the existing connection, with a read-only exec fallback when SFTP is turned off.

Since decision 10, agent CLIs stay on This PC; on a server they work through hub tools (§5.7).

### 5.4 Projects and workspaces

- A project is created from a machine + path; the same path does not make a duplicate. Deleting a project stops its sessions and archives its boards, while files on disk are not touched.
- A workspace is a board tab. The last workspace cannot be deleted. "Close all boards" in the project menu closes every tab at once and leaves one empty board; whatever was running on those boards ends, and tiles can be restored from the archive.
- The home screen shows projects, branch, ahead/behind, and the number of changes.
- **Many repos in one project** (decision 12): the hub scans the project folder up to 4 levels deep, the client picks through a picker, and `?repo=` applies to every git endpoint.

### 5.5 Board

**Interaction.** Pan and zoom 20–300 %, marquee, clipboard, duplicate, undo/redo for 80 steps, guides, left rail (tools), right rail (Code editor, Studios, Memory, Git, Loops, Mission Map, Auto arrange), a right-click ring on empty board (Spawn agent, drawing, web, Paste, shape, Compose group, More), single-letter keys. The viewport is stored per client, not in the board. Every card uses one header layout (mark, title, meta, actions, display button, `…`); the display button resizes the card to Fit tile, three monitors, or five phones. Details are in DESIGN §3.5.

**Tiles.**

| Kind | Contents |
|---|---|
| `terminal` | Console; created and deleted by the hub |
| `note`, `text` | Sticky note in 6 colors; text with font, size, alignment |
| `checklist`, `kanban`, `calendar` | A list with checkboxes; columns and cards (due, priority); monthly events |
| `uptime` | Uptime monitoring like Uptime Kuma: heartbeats, response chart, certificates, alerts; the hub does the checking ([API-uptime.md](API-uptime.md)) |
| `credentials` | A password manager like Bitwarden, encrypted in the browser; the node stores only the title ([API-vault.md](API-vault.md)) |
| `database` | The project's database browser (MySQL, MariaDB, PostgreSQL, Redis, Valkey), or one connection of it (`data.connectionId`); the connections are the hub's ([API-db.md](API-db.md)) |
| `shape`, `line`, `ink` | rough.js-style shapes, lines and arrows, pen strokes |
| `image` | From a hub asset or a machine file, with a markup editor |
| `file` | `code` (snapshot), `markdown` (live), `file`, `folder` (≤ 40 files) |
| `group` | Team frame: name, roles, color, members, linked input |
| `orchestrator` | A directing card with its own thread |
| `memory` | A lego-like memory stack: the project is always on, blocks from all projects or another project are added; wired to a console, group, orchestrator, or editor |
| `web`, `weblog`, `link` | Preview or a real browser; console and network log; link card |
| `branches` | Branches, ahead/behind, switch, create, delete |
| `document` | A document from the Documents studio |
| `editor` | Code workbench, one per board |
| `app` | A This PC desktop app |

A kind the client does not know shows as "Unsupported tile", so a new kind does not break old clients.

**Edges.** `wire` (gray; orchestrator, memory, editor, or a tile with content → console/group; the tile's content is sent once on connecting; an orchestrator → editor card directs the agents opened inside that editor, also from the agent's pane: *Connect to orchestrator*), `forward` (dashed orange; console/group → console/group, no cycles), `preview` (cyan; terminal → web), `log` (web → weblog). Lines route straight when tiles face each other, with one elbow when they do not, and go around when a tile is in the way. An orchestrator wire that is working lights up and carries a subtle animation.

**Durability** (tested with `kill -9` and corrupt files):

- Saves are debounced by 2 seconds, delayed at most 7 seconds; a normal stop saves every change.
- `boards/<wid>.json` is written atomically and has a `.prev`. A corrupt file is archived with the reason `unreadable`.
- Hourly snapshots, the latest 24. A restore always saves the current version first, so a restore can be undone.
- Archives of closed or emptied boards are never pruned.

### 5.6 Consoles and agents

**Catalog.**

| Source | Contents |
|---|---|
| Built-in | `shell`, `claude`, `codex`, `gemini` |
| Catalog (17) | OpenCode, Aider, Amp, Auggie, Cline, Codebuff, Continue CLI, GitHub Copilot CLI, Crush, Cursor Agent, Droid, Goose, Kimi, Kiro, Pi, Qwen Code, Mistral Vibe |
| Custom | Through Settings or `presets.json`; may have a Claude or Codex `base` to inherit capabilities |

Catalog flags not yet verified on a real CLI are not sent. Detection is through `command -v` during the probe, the path can be overridden per machine, and the default agent can be set per machine.

**Handles.** Auto-assigned from `Arkan Bintang Cantika Daffa Elvan Farel Gibran Haikal Ilham Jihan Kayla Luna Maura Naufal Olivia Pandu Rafa Reyhan Salsa Tiara Vania Wildan Yasmin Zahra` (common current Indonesian names), then `Arkan2`, and so on. Unique per hub, case-insensitive. Reserved words such as `everyone`, `all`, `hub` are refused.

**Status.**

| `agentState` | Meaning |
|---|---|
| `starting` | Being launched |
| `working` | Working on a turn, with an `activity` such as `Bash: npm test` |
| `needsYou` | A permission, question, or dialog is waiting for the user |
| `ready` | At the prompt, waiting for an order |
| `shell` | No agent in the foreground |
| `exited`, `reconnecting`, `error` | Process finished, SSH link dropped, failed to start |

Status sources in order: process status → foreground → hooks (Claude) or notify (Codex) → per-CLI screen rules → output activity. The screen rules are tested against real screen dumps in `internal/agent/testdata/`.

**Launch modes.** `fresh`, `resume` (the exact conversation), `continue` (the latest in the folder), `pick` (the CLI's own picker), `shell`. The conversation picker marks conversations that another console has open. A hub restart follows `resumeOnRestart`: `auto`, `ask`, or `never`.

**Other.**

- **Output viewer:** the "See output" button on a console opens its last reply; on a thread bubble it opens the reply that belongs to that bubble. Both are in Markdown, with Copy, Save `.md`, and a reply field.
- **Yolo:** per launch, requires confirmation, with a badge in the header. Off by default.
- **Login profiles:** several accounts per CLI through `CLAUDE_CONFIG_DIR` / `CODEX_HOME`. The default state folder `<data>/agents/<agent>` is created once by the hub. A new folder is not logged in yet, so you sign in once in the console.
- **Launch context:** each launch gets system instructions in `<data>/agents/prompts/<sid>.md`, project memory, critical notes, MCP servers, and hub tools.
- **Terminal panel:** `POST /api/sessions` with `panel: true` creates a session without a node for the workbench's bottom panel. Orders cannot reach this session.

### 5.7 Agents that work on a server

The CLI keeps running on This PC, while its file and shell tools act on the server through the hub's MCP. Status: stable since 2026-11-08, `settings.agents.remoteTools` defaults to `true`. Without it, an agent on an SSH machine is refused with `422 agent_this_pc_only`.

| CLI | Local tools turned off through | Verified |
|---|---|---|
| Claude Code | `--tools Read,Write,Edit,TodoWrite,Skill`, no Bash; local only for the memory folder | Real console, with and without yolo |
| Codex | `--disable shell_tool --disable unified_exec --sandbox read-only` | Real console without yolo |
| OpenCode | `permission` of every local tool `deny` except `skill` | MCP connected; full turn only against a fake model |

- **Hub tools:** `read_file`, `write_file`, `edit_file`, `grep`, `glob`, `bash`, plus `memory_search`, `memory_read`, `memory_save`, `memory_move`, for a project with marked credentials `credential_list`, `credential_run`, `credential_result` (API-vault.md), and for a project with database connections agents may use `db_connections`, `db_schema`, `db_table`, `db_query`, `db_explain`, `db_execute`, `db_result`, `redis_scan`, `redis_get`, `redis_command` (API-db.md).
- **If the tools cannot be set up, the launch fails** rather than falling back to local tools, because the agent would then work on This PC's files.
- **Yolo on a server** does not use the CLI's skip-everything flag, so it opens no access to This PC.

### 5.8 MCP servers

MCP is set up once in Settings and given per launch to Claude Code, Codex, and OpenCode, so switching agents does not change the tools available. Transport `http` or `stdio`, at most 32 servers, can be limited per project. The Try button talks to the server like a CLI does (`initialize` then `tools/list`).

Secrets are sealed in `mcp-servers.json`, never go back to the client, and leave only through the process environment. OAuth login stays per CLI through `/mcp`. A running agent gets the changes at its next restart.

### 5.9 Orchestration

**Command bar.** Below the board (`Ctrl/⌘K`), history with ↑/↓, an `@` menu. It covers every board in the project; other projects are reached only through an explicit handle.

**Local parser.** English and Indonesian (`jeda semua`, `suruh Ash untuk …`, `buat 2 claude`; list in API-orchestration §3.2), no AI, always available:

| Area | Examples |
|---|---|
| Send | `@Ash run the tests`, `tell Bea to …`, `give everyone the same prompt: …` |
| Spawn and groups | `spawn 3 codex reviewers`, `make a group of 4 called Backend`, `add 2 to Backend`, `dissolve Backend` |
| Control | `pause`, `resume`, `restart Ash`, `kill all`, `cancel that` |
| Workflow | `hand off to codex`, `when Ash finishes, pass it to Bea`, `every 15 minutes …` |
| Ask | `status`, `who needs me`, `who finished`, `give me a summary`, `help` |
| Board | `fit`, `go to Ash`, `open git`, `add a kanban`, `open localhost 5173`, `undo` |
| Workspace | `switch to workspace 2`, `new workspace called API`, `deploy Duo` |
| Git and memory | `commit and push`, `repo app-a push`, `push memory`, `scan memory` |

**@mention.** Resolution order: handle → group name → role (`@reviewers`) → agent (`@codex`). Similar names (Damerau-Levenshtein, Double Metaphone) trigger a "did you mean" confirmation.

**Planner.** A local `claude -p` on This PC with schema-bound JSON output, no tools, a 2-minute limit, never yolo.

- Input: the order text, the last 10 thread messages, a snapshot of consoles, groups, tiles (≤ 30 per board), and memory. It does not include the contents of project files or transcripts.
- Output: a reply and at most 40 steps. The output is treated as untrusted: names are resolved by the hub, steps outside the list are dropped, the confirmation policy still applies.
- The planner can control the whole board: add, change, delete tiles, connect them, open files in the editor. `workspace.close` is outside its authority on purpose.
- Without the planner (no Claude, not logged in, cooldown), the parser keeps working and messages with an addressee are sent as they are.

**Delivery engine.** The core of orchestration safety.

| Console condition | Action |
|---|---|
| Shell, the agent has exited | `failed not_agent`; never types into a shell |
| `starting`, `needsYou`, `reconnecting`, `paused` | Waits, each with its own time limit |
| `working` | Queues (default) |
| `ready` | Types |

Text is typed only when the screen is **positively recognized** as the CLI's normal input prompt and has been quiet for ≥ 300 ms. The screen is checked again right before Return, so a dialog that appears between the paste and Return cancels the send. Human input in the last 2 seconds delays the delivery. Control characters are dropped. 24 real and synthetic dialog fixtures are used to test the refusals.

**Confirmation.**

| Step | Confirmation |
|---|---|
| `kill`, `restart`, `node.delete` | Always (`destructive`) |
| `workspace.close` with running sessions | Always |
| `loop.create` | Always, showing the schedule and the estimated prompts per hour |
| `spawn` > 6 consoles | `bulkSpawn` |
| Planner steps that were not asked for: git, web URLs, text with risky patterns (`rm -rf`, `sudo`, `curl \| sh`, `.env`, `--yolo`, force push, `drop table`, …) | `risky` |
| Git typed by the user, pause/resume, handoff, forward, verbatim send, push memory | No |

TTL 60 seconds (30–300). The first decision wins, from any client, and it can be answered by typing `yes` or `cancel`.

**Threads and reports.** A plan note shows what is sent to whom. Each target ends as `finished`, `failed`, `timeout`, or `skipped`, with an excerpt of the reply and a "See output" button. Not finishing within 30 minutes produces a timeout note.

**Groups and the composer.** Roles: `general`, `frontend`, `backend`, `testers`, `reviewers`, `design`, `docs`, `security`, each of which can have a default agent. At most 12 members, and a console can be in only one group. Batch spawn of 1–12 consoles in one broadcast. **Linked input** (`syncInput`) copies one member's keystrokes to the other members, including Ctrl+C, but does not copy the emulator's automatic replies.

**Orchestrator cards.** A small command bar that reaches only the consoles wired to it, and the agents opened inside an editor card wired to it, with its own thread. Undo and workspace commands are refused here. A card is led by a lead agent; the card's name = its lead's name.

**Lead agent.** In the bar, cards, and the editor, the owner talks with the lead (Settings › Lead agent: name, personality, Talk first). Greetings and questions are answered as conversation. Work is handed to agents right away, with no need for Go ahead. Only a plan that could do damage (deleting files or data, resetting a database, rewriting git history, production) waits for a button or a word of approval, and it can be changed while chatting. Orders that name an `@agent` and plain commands still run right away. The lead uses Claude Code on This PC, so every chat turn uses the owner's plan.

**Steering from the thread.** Each bubble has actions on hover: Copy, Reply (a reply to an agent goes straight to that agent, with a quote), Ask lead, Show console, and Edit and send again for your own messages. Text can be selected directly, and the card menu has Copy thread.

**Mission Map** (`X`). A map of director → group → console, with filters All, Working, Needs you, Idle, Done, subagent counts, and running orders.

**Pause and resume.** SIGSTOP/SIGCONT to the foreground process group, local or SSH, with session validation. While paused, deliveries wait and loops skip that console.

**Handoff.** A summary (goal, what is done, state, files, next steps, open questions) through a runner, then sent to a new console. If the summary fails, the last 12 000 characters are sent as they are. If the target is not ready, the result is `manual` and the brief shows in the thread.

**Switching agents in the same tile.** The tile, handle, and folder stay; memory, critical notes, and tools come along. The old agent is paused while the summary is written. Going back to the previous agent continues the same conversation (at most 8 recorded), and the summary explains what happened while it was away.

**Forward.** A permanent edge (`trigger`: `turn` or `order`) or a one-off from a command. The digest is labeled with its source; `forwardDepth` ≤ 3 and no cycles, to prevent chained injection.

**Loops.** A scheduler on the hub, runs without a client.

| Part | Options |
|---|---|
| Trigger | Interval (60 seconds–24 hours), 5-field cron, `agentIdle`, an `output` pattern |
| Target | Handle, group, orchestrator, all agents |
| Stop | `maxRuns`, duration, date, output pattern, `agentJudged` goal, `command` goal |
| Templates | Status ping, Nudge idle agents, Build fixer, Test guard, Checkpoint commit, Review round, Daily summary |

A `needsYou` target is skipped and raises a `loopHeld` attention. Runs missed while the hub was down are not repeated. A `command` goal can only be created by the user, not by the planner.

**Layout templates.** Built-in: Duo, Trio, Quartet, Two quartets, Build & review, Front & back, Review trio, Triage, Preview lab, Docs crew, Solo, Sextet, Three trios, Lead & crew, Full stack, Code & pair, Web builder, Bug hunt, Security audit, Design studio, Release crew, Research desk, Two duos, Octet, Writer & checker, Relay, Editor & agent, Code & crew, Laravel crew, API crew, App team, UI lab, Refactor squad, Migration desk, Incident room, Review board, Planning desk, Content studio, Onboarding, Quality gate, Performance lab, Hackathon, Dependency update, Data crew. The number of consoles in each group can be set before deploying, and a group can be emptied if the layout has more than one. You can save your own arrangement (kind, geometry, agents, groups, wires), without card contents and drawings. Deployment happens entirely on the hub.

**Notifications.** `attention` events: `needsYou`, `finished` (a turn ≥ `finishedMinSec`), `error`, `loopHeld`, `loopEnded`. A needs-you count chip and the `N` key jump to the one that has waited longest. The web uses the Notification API when the tab is hidden; Android uses its own foreground service without FCM.

### 5.10 Memory

The `/memory` page is a galaxy (visual reference: Obsidian's graph), with a list of the same items that can be used from the keyboard.

| Layer | Location | Notes |
|---|---|---|
| Main memory | `memory/<projectId>/MEMORY.md` | Used by the panel, the planner, every CLI; ≤ 64 KB |
| Agent notes | Other `.md` files in that folder | |
| Sub-memory | `memory/subs/<projectId\|global>/<slug>.md` | A page per topic, ≤ 64 per main, ≤ 64 KB |
| Critical notes | The `critical` sub-memory | Always present, ≤ 16 KB |
| Part memory | The `part-<id>` sub-memory | In a project of several parts, one per part: what holds for that part only |
| Global | `memory/global.md` | Applies to every project |

- **Critical notes** always go in whole and first to every agent, are not cut by the memory budget, and only the owner changes them. Even a direct request does not count as permission: the agent must stop and wait for confirmation. **These are instructions, not a technical lock**, because an agent on This PC has its own Bash. Tested for real with Haiku: `npm run db:reset` was not run.
- **How CLIs use memory:** Claude Code through `autoMemoryDirectory` in `--settings`; Codex and OpenCode through instruction injection (≤ 16 000 characters) and hub tools; other agents through push.
- **Topic lookup:** `memory_search` (in-process BM25, no index, ≤ 6 snippets), `memory_read`, `memory_save`. The launch prompt contains only the critical notes, the main memory, and the list of pages; the rest is looked up when needed.
- **Part memories:** in a project of several parts the main memory holds what is true for the whole project and each part has a page of its own, titled with the part's current name. A console in a part gets its part's page right after the main memory at launch and saves to it with `memory_save` and `part`; a root console sees the part pages in its list. Scan and Init say for every fact which part it holds for, and accepting puts it there. A removed part's page stays as an ordinary page; a part split off takes its page along as the new project's main memory (API-orchestration.md §12).
- **Init:** a read-only job that proposes facts, sub-memories, and critical rules from what could damage that project. Its prompt forbids opening `.env`. Nothing is saved until the owner accepts, and applying twice does not duplicate.
- **Scan, refresh, push:** scan proposes 6–14 facts depending on effort; refresh checks that one fact is still true; push sends memory through delivery (≤ 4000 characters) labeled "background only".
- **Memory tile and Send to agents:** the tile is a memory stack that applies to every connected agent, through launch, tools, and delivery. The Send dialog picks which memory and which agents, all of the project's agents or the selected ones, and shows a token estimate before sending. The hub records the facts each console already has, so only what is new is typed, and with no new facts no turn is used. Memory content that changes does not type by itself (decision #37).

### 5.11 Git

The Git panel (`G`), a pill at the bottom left, the branch card, and Source Control in the workbench. Runs on the project's machine, over SSH too.

- **Status and diff:** renames, untracked files, conflicts, an ahead/behind summary without automatic fetch, a warning for files > 100 MiB.
- **Commit:** selected paths or all; the user's own hooks still run; the sparkle button writes a Conventional Commits message: a CLI reads the diff of the changed files, or an instant subject from the file names when the CLI is turned off or fails; `422 git_identity` when the identity is not set.
- **Push, pull, fetch** as jobs. Pull is `--ff-only` only; a divergence is refused with the message "merge in a console". Never opens a prompt: `GIT_TERMINAL_PROMPT=0`, `BatchMode`, no askpass, a 5-minute limit without output.
- **Discard never deletes:** the worktree version is copied or moved to `.git/nextboard-trash/<ts>/`, and restore does not overwrite work.
- **Branches:** create, switch (`--no-guess`), delete (`-d`).
- **GitHub through `gh`:** log in in an ordinary console, publish with `gh repo create`. The token stays in the `gh` keyring; the hub does not read it. Publish is refused if a remote already exists.
- **Multi-repo:** read-only scan (≤ 50 repos, 10 seconds), state per repo, the command bar refuses when no repo is named and there is more than one. Never commits to every repo, and a new repo is not created on top of an existing one.
- **Security:** no prompts, and paths are validated to stay inside the repo. Read commands do not run code from the repo's config: `core.fsmonitor`, the repo's own filter drivers, and `gpg.program` through `log.showSignature` are turned off (filters need git 2.31). Write actions such as commit still run hooks and filters.

### 5.12 Web tile

Two paths in one tile:

**Dev servers and tailnet sites through the preview proxy.** One HTTP listener per preview in the range 12320–12339; nginx provides HTTPS on the same port. The target is only that machine's loopback (local dial, SSH `direct-tcpip`) or an address that resolves to a Tailscale IP, checked again on every connection (decision 19), so there is no SSRF into other networks. Auth through a single-use 60-second token → a per-listener cookie for 12 hours. Frame-blocking headers are dropped, a bridge script is injected, WebSocket/HMR is transparent.

- **Automatic preview:** a localhost URL in a console's output, if its port is open, automatically becomes a web tile with a `preview` edge.
- **Log card:** console, errors, and network from the bridge, a ring of 500 entries per preview.
- **Tile controls:** address bar, back/forward/reload, tabs, device size, zoom 50–250 %.

**Other outside sites through a real browser** (decision 13). A real Chrome on This PC over the same path as app tiles, so sites that forbid iframes, logins, downloads, and DevTools all work. The next address becomes a new tab, or a browser of its own for that tile with a separate profile and login; two sites with one login sit side by side through Chrome's built-in Split View. Supports Chrome, Chromium, Brave, Edge, Vivaldi, Opera, and Firefox, including snap and flatpak packages (API-gui §8).

### 5.13 Desktop apps in a tile

This PC's GUI apps (Chrome, VS Code, and others, from `.desktop` files) show in a tile, do not appear on the physical screen, and do not disturb the desktop session. At most 8 apps at once, one session per app, with a dedicated profile in `<data>/gui/profiles/<appId>/`. Sessions are detached from the hub: a hub restart does not close the apps.

| Path | Contents |
|---|---|
| Main display | Headless `sway` + `wayvnc` + `wf-recorder`, GPU buffers straight to the encoder |
| Fallback without GPU | Xtigervnc + openbox |
| Input, cursor, clipboard, still images | RFB through noVNC, lossless |
| Large motion | AV1 or H.264 VA-API through WebCodecs (needs HTTPS or localhost) |
| Sound | A PipeWire sink per session → 48 kHz PCM |

**Why two paths.** Scrolling text at 1280×800 on a 5 Mbps link: RFB alone gives 1.5 fps with a 1165 ms click response; with AV1 48 fps, 165 ms, 2.2 Mbps (measured on an Intel Arc A380, X11 display). The Wayland display lowers CPU from about 104 % to 50–65 % of one core, at the cost of a click response about 30 ms slower during large motion.

Security and limits: §6 and §14.

### 5.14 Studio

**Hidden runs.** The CLIs' headless mode (`claude -p`, `codex exec`) with read-only tools or no tools, no yolo (guarded by `TestRunnerArgvNeverUnsafe`). Claude and Codex only. Queue ≤ 2 per machine, ≤ 4 per hub. Purposes: `document`, `diagram`, `memory-scan`, `memory-init`, `memory-refresh`, `loop-goal`, `planner`, `handoff-summary`, `commit-subject`, `thread-summary`, `chat-summary`, `code-edit`, `code-complete`, `code-ask`.

**Code workbench.** An `editor` node, one per board, not an overlay. Tabs and unsaved edits belong to the project. The Fill button widens the card to the width of the view.

| Area | Capabilities |
|---|---|
| Editor | Monaco, at most 3 groups, drafts in IndexedDB, files ≤ 1 MB are editable, 1–10 MB read-only, CRLF/BOM preserved, save conflicts through `ifMtime` |
| Navigation | Explorer, Go to file, command palette, bottom panel with a terminal, status bar |
| Search and replace | `rg` → `git grep` → `grep`, ≤ 5000 results; replace skips files that have unsaved edits |
| Source Control | Status, diff, stage, commit, repo picker |
| Language servers | The project machine's own, bridged by the hub (local or SSH). 15 in the catalog: Go, TypeScript/JS, Python, PHP, Rust, C/C++, Java, Ruby, C#, Kotlin, Swift, YAML, Bash, Lua, Dockerfile. None is installed automatically |
| Debugger | DAP: Delve, debugpy, lldb-dap / gdb, php-debug-adapter; `.vscode/launch.json` and automatic Go/Python entries |
| Extensions | Open VSX, data only: themes, snippets, grammars, language definitions. Extension code never runs |
| AI | Edit a selection with a diff and Accept; ghost text (off by default, because every suggestion runs a CLI); an Ask panel that follows the card's wires |
| On save | `editor.onSave` runs a command per pattern, 60 seconds per command |

**Work aids in the editor.** The **Review** panel shows the files each agent changed, with keep and undo per change; **Local History** stores every version of a file. Both use a shadow repo in the hub's data folder (decision 24); projects on a server take part through a copy on This PC. Source Control has a commit graph, a commit detail tab, staging per change, stash, conflict resolution, and pull requests through `gh`. The bottom panel has **Tasks** (project scripts, error locations, sending a failure to an agent) and **Ports**. Laravel projects get a view of their own (routes, logs, artisan), plus completion and Go to Definition for routes, views, and config, from the project's files without running anything (API-studio §7.14). The Node.js debugger works without a download through the hub's built-in adapter (decision 25). Details: [API-studio §3.2, §3.3, §7.2, §7.5, §7.10–§7.13](API-studio.md).

**Documents.** Types `overview`, `onboarding`, `architecture`, `readme`, `api`, `dataModel`, `security`, `testing`, `releaseNotes`, `custom`. Effort `quick`, `balanced`, `thorough` (400–1500 words, 6–25 minutes), with the median duration. Streaming, versions, Stop saves an `incomplete` version. Export to PDF A4/Letter and Markdown, or add to the board.

**Diagrams.** Subjects `architecture`, `userFlow`, `dataFlow`, `components`, `requestLifecycle`, `stateFlow`, `custom`. The `nb.diagram/v1` JSON output is validated by the hub (≤ 40 nodes, 80 edges, 8 groups), then lands as loose shapes and lines on the board. Export to PNG and Excalidraw. Starters without AI: Flow, Steps, Decision, Mind map.

**Markdown** (`D`). A vault of `.md .markdown .mdx` (≤ 2000 files), Reading/Source/Graph modes, wikilinks and Markdown links, backlinks, autosave after 1.5 seconds, changes from agents show up within ≤ 3 seconds, PDF through the print route.

**Skills** (`S`). Claude skills and commands in personal, project, and plugin (read-only) scope. Shadowing, a 10-rule lint without AI, 12 starters, run into a console through delivery. Writing to a config folder on an SSH machine uses a confirmation protocol: `428` with a list of paths, the UI shows the machine and paths, and the old version is backed up to the hub before it is overwritten.

### 5.15 Usage

- **Usage pill:** a hidden PTY session in `/tmp/nb-usage-<uid>-<hubId>` (deleted after use, one per instance so two hubs never delete each other's) types `/usage`, `/status`, or `/stats`, then its screen is parsed. The result is a bar per window, the reset time, and the raw screen when parsing fails. Best-effort, because the screen format changes between versions.
- **Defaults:** background checks off and Codex off, because the side effects are real (§14).
- **Reset chime:** sounds at `resetsAt + 30 seconds`, with the option `after100` or `always`.
- **Token charts:** 30 days per agent from transcripts, an incremental index reads only new bytes, deduplicated per `message.id`. The UI states that this is not a bill, and that the count includes your own CLI's use (`~/.claude`, `~/.codex`), which every instance on the machine reads too.

### 5.16 Voice

Entirely in the client; the hub receives no audio. Opt-in, with a disclosure that the browser may send audio to Google or Microsoft.

- **Push-to-talk:** hold Space, at most 60 seconds, the result goes into the command bar.
- **Dictation:** hold `AltRight`, into the focused field or into a console through the `dictation` delivery guard, at most 5 minutes.
- **TTS:** only short sentences from the hub, never agent replies.

### 5.17 Settings and shortcuts

On the hub (`GET/PATCH /api/settings`, JSON Merge Patch, `settings` event): `agents`, `notifications`, `shortcuts`, `delivery`, `commands`, `memory`, `previews`, `usage`, `runner`, `git`, `editor`. Per browser (localStorage): theme, text size, armed agent, viewport, voice.

Settings panels: Agents, Lead agent, Login profiles, MCP servers, Notifications, Shortcuts, Usage, Previews & studios, Voice, Connections, Appearance, Install app (DESIGN §3.17).

**Theme** (per browser, Settings › Appearance and a button in the header): System, Light, Dark. No reload, other tabs follow. Consoles and Monaco switch right away; consoles can be made always dark. A color theme from an extension chosen in the editor stays in use whatever the app theme. The Memory sky is always dark. The design system is in [DESIGN.md](DESIGN.md).

| Key | Action | Key | Action |
|---|---|---|---|
| `V` | Select | `M` | Memory |
| `T` | Spawn | `S` | Skills |
| `P` | Pen | `L` | Loops |
| `E` | Eraser | `D` | Markdown |
| `G` | Git | `N` | Jump to needs-you |
| `X` | Mission Map (fixed) | `Space` | Push-to-talk (fixed) |

Keys other than the ones marked fixed can be rebound. `Ctrl/⌘K` focuses the command bar.

### 5.18 Connection log

Always on, in `<data>/logs/connections.jsonl`, at most about 8 MiB. Records `connected`, `disconnected` (with the reason `keepalive_timeout`, `connection_lost`, or `closed`), `keepalive_miss`, `link_stats` every 30 minutes, `tool_failed`, `tool_slow`, `hub_unclean_exit`, and others. It does not record commands, output, file contents, or paths.

Read it through Machines › server card › Connection log, `nextboard connlog`, or `jq`.

### 5.19 Credentials

A password manager on the board, modeled on Bitwarden; its contract and security model are in [API-vault.md](API-vault.md). One vault per hub, shown by every `credentials` card and by the key button in the header (a window on any page, no card needed), and deleting a card does not touch the vault.

- **Encryption in the browser:** Argon2id (64 MiB, 3 passes) + AES-256-GCM with a master password that never reaches the hub. The hub stores only ciphertext, because agents on This PC can read `<data>`.
- **Contents:** logins (TOTP, several addresses, password history), cards, identities, notes, SSH keys, custom fields, folders, favorites, a 30-day trash, search.
- **Generator** for passwords and passphrases, copy with automatic clipboard clearing, idle lock (default 15 minutes, counted from work in the card only), import and export in Bitwarden format.
- **6-digit PIN per browser,** needs a blob in the browser and the hub's approval, burned after 5 wrong tries. The master password is at least 8 characters.
- **Filling forms** from the key button in a browser tile, always at the person's choice: web tiles through the bridge with a site check, Chrome on This PC by pasting into the focused field.
- **Works on plain HTTP too,** with AES from `@noble/ciphers` because `crypto.subtle` is not available there, and the password screen names the risk. One vault can be opened over both HTTP and HTTPS.
- **Agents using a credential** (decision #40): an item marked for a project can be requested by that project's agents through `credential_run`. The owner approves each command in the browser, the hub runs it and censors the output, and the agent never receives the value.

### 5.20 Database browser

A database client of the hub's own, like DBeaver or TablePlus; the contract is in [API-db.md](API-db.md). A project's connections show in the Code view's Databases tab and on every `database` card; the hub connects (directly, over a machine of the project, or through an SSH tunnel with jump servers), the browser never does.

- **Connections** are made in DBeaver's flow (Select your database › Connection settings with Main, SSH, SSL, Driver properties › Details), or filled from the project's `.env` files. Passwords and SSH keys are sealed on the hub and never sent back.
- **Tables:** filter chips, a raw WHERE, sort, pages, edits in the grid committed in one transaction after Review SQL, the row's detail with its foreign keys followed both ways, structure, indexes, relations, export to CSV, JSON or INSERTs.
- **SQL editor:** the dialect and the schema's names completed, the statement under the cursor or all of them, Stop, a transaction the owner opens with Commit and Roll back, results as a table, a chart or JSON, saved queries and history.
- **Redis and Valkey:** keys by pattern (SCAN) grouped by prefix, each type's value paged and edited, TTL, a console.
- **Safety:** a connection type (Local, Development, Staging, Production) sets read-only and confirmations; DROP, TRUNCATE and DELETE or UPDATE without WHERE always ask for the connection's name and run in a transaction that waits for Commit.
- **Agents and SmartDB:** agents of the project read, ask to write or write per connection, with masked columns and an activity log; SmartDB turns words into filters and SQL and answers questions about the data with the owner's own agent CLI.

## 6. Security

**Threat model.** One owner. Anyone who can log in to the hub effectively has a shell on the machine. That is why the most important layer is not exposing the hub to the public.

| Layer | Protection |
|---|---|
| Network | Binds to loopback by default, HTTPS in nginx, a warning on a non-loopback bind. `X-Forwarded-Proto` is trusted only from a loopback peer |
| Browser | argon2id, `HttpOnly` + `SameSite=Strict` cookie, CSRF header, WebSocket Origin check, rate limit, CSP with `font-src 'self'`. WebSockets end together with their login: logout and password reset cut open terminals |
| Device | Ed25519, single-use 60-second challenge, opaque 1-hour token, immediate revoke, tokens never in URLs |
| SSH credentials | AES-256-GCM with a 0600 `secret.key`, never sent to the client; a changed host key is refused |
| Credentials | Encrypted in the browser with a master password that never reaches the hub; the board, logs, and agents never see the contents. An agent can only request one approved command per use, and its output is censored |
| File API | `<data>` is blocked for read/raw/write; `fs/raw` does not serve HTML as HTML, with a `sandbox` CSP; assets refuse SVG |
| Exec | No free-form exec, arguments are quoted, paths after `--`, refs are validated |
| Markers | A 128-bit token per session, never sent to the client; markers without a valid token are dropped |
| Preview | An origin per port, single-use tokens, loopback-only targets, `/api` not available, NextBoard cookies stripped from the upstream |
| Desktop apps | VNC only over a Unix socket in a 0700 folder, an X11 cookie or an access list per session |
| Orchestration | Planner output is untrusted, source labels on forwards, depth limit, risky patterns |
| Logs | No passwords, codes, or terminal contents |

**What is not protected** (already stated to the owner):

- An agent on This PC has Bash as the same user, so it can read its environment, including MCP secrets, and the hub's data folder. Do not provide a token that the agent must not see.
- Instances of one OS user (§5.1.1) keep data and accounts apart, not secrets: an agent of one can read another's data folder, its `secret.key` included. A separate OS user per instance is the way to keep secrets apart (README, Several instances on one machine).
- Critical notes are instructions; compliance depends on the model. Blocked commands are enforced by the CLI or the hub, but they are not a sandbox (decision #38).
- Desktop app profiles use `--password-store=basic`, so saved passwords are protected only by the file permissions of `<data>`.
- A browser in a tile can reach everything This PC can reach, including localhost services and the local network.
- With yolo, an agent can do anything without asking.
- Credentials: an agent can read `vault.json` and guess the master password offline, and an agent that can write to the hub binary or the web files can capture it at unlock. Forgetting the master password means the vault's contents are gone. On plain HTTP, anyone who can modify the traffic can capture the master password. The PIN can be guessed if the browser storage and the hub folder are in the same hands. Filling a form releases the secret to that page, and in This PC's Chrome the site cannot be verified. A command approved for an agent runs the project's script as it is, and the agent can change that script or read its process env ([API-vault.md](API-vault.md)).

## 7. System limits

| Area | Limit |
|---|---|
| Request body | 64 KB default; board ops 2 MB; assets and file writes 20 MB |
| Console | Scrollback 1 MiB; total hub memory about 6 MB per console |
| Spawn | ≤ 12 per call, ≤ 64 per workspace |
| Board | ≤ 500 ops per batch, node `data` ≤ 256 KB, board ≤ 8 MB, 24 snapshots |
| Groups | ≤ 12 members, name ≤ 24 characters |
| Commands | Text ≤ 4000 characters; only ≤ 600 go to the planner; 3 queued per planner turn |
| Planner | 2 minutes; ≤ 40 steps; ≤ 30 tiles per board; 60-second cooldown after 2 failures |
| Delivery | ≤ 32 000 characters, ≤ 20 queued per session, default deadline 1 hour, confirmation 8 seconds |
| Delivery waits | starting 60 s, prompt 120 s, reconnect 5 min, needsYou 15 min, working 30 min |
| Confirmation | TTL 60 seconds (30–300) |
| Orders | Timeout report at 30 minutes; 200 kept |
| Handoff | Summary 4 minutes; ready 120 + 120 seconds |
| Forward | 20 000 per reply, 32 000 per digest; depth ≤ 3; each edge 1× per 60 seconds, results within that minute are held and then sent together |
| Loops | 50 per hub; `maxRuns` 1–500; duration 15 minutes–24 hours; a goal not twice within 90 seconds |
| Memory | Main and sub 64 KB; critical 16 KB; 64 subs; push 4000 characters |
| Jobs | 100 kept; runner 10 minutes and idle 200 seconds |
| Exec | Output ≤ 4 MiB |
| Git | Status ≤ 400 files, diff ≤ 512 KB, log ≤ 100, branches ≤ 200; network 15 minutes, idle 5 minutes |
| Multi-repo | 4 levels, ≤ 50 repos, scan 10 seconds |
| Preview | ≤ 20 listeners, idle 30 minutes, log 500 entries |
| Desktop apps | 8 sessions, 16 video/audio streams, 320×200 to 3840×2160 |
| Code | ≤ 200 000 files; search 5000 results, 30 seconds; replace 2000 files; 3 editor groups; `onSave` 50 rules |
| Markdown | 2000 files; 256 KB per file; index 20 MB / 20 seconds |
| Skills | 400 items; contents 512 KB; import 50 files / 5 MB |
| MCP | 32 servers; Try 20 seconds (URL), 45 seconds (command) |
| Attention | Ring of 200 |
| Credentials | ≤ 5000 records; one item ≤ 64 KB base64; vault ≤ 64 MB; master password ≥ 8 characters; 6-digit PIN, 5 wrong tries, ≤ 32 browsers |

## 8. Data and persistence

Everything is in `<data>` (`~/.nextboard/`, or `~/.nextboard-<name>` for a named instance, 0700). Files are 0600, written atomically (tmp + rename). `config.json` holds `instance` and `hubId`; `hub.lock` is held (`flock`) while a hub runs on the folder.

```
config.json  auth.json  secret.key  known_hosts  settings.json  hub.lock
devices.json  machines.json  creds/<id>.enc      identity and machines
projects.json  sessions.json  scrollback/<id>.log  projects and consoles
presets.json  profiles.json  profiles/<id>/      custom agents and profiles
agents/<agent>/  agents/prompts/<sid>.md  agents/remote/…   CLI state per agent
boards/<wid>.json(.prev)  snapshots/<wid>/  archive/    boards
assets/                                          images
threads/<tid>.jsonl  threads/<tid>.chats.json  orders.json  forwards.json   orchestration (lead chats)
loops.json  loops/<id>.jsonl  layouts.json       loops and layouts
memory/<projectId>/MEMORY.md  memory/global.md  memory/subs/…   memory
mcp-servers.json  extensions/                    MCP and extensions
jobs.json  runner/planner/                       jobs
vault.json(.prev)  vault.reset-*.json             credentials, ciphertext only
credential-log.jsonl(.1)                         agent commands with a credential, no values
documents/<projectId>/…  diagrams/<projectId>/…  studio output
usage/reports.json  usage/tokens/  usage/pending-cleanup.json
gui/profiles/<appId>/  gui/sessions/<id>/        desktop apps
backups/skills/<machineId>/<ts>/                 skill backups
logs/connections.jsonl                           connection log
```

**In memory only, lost when the hub restarts:** deliveries, confirmations, handoffs, engine state, the reply cache, the attention ring, previews and their listeners.

**What is written to the target machine**, only on a user action: repo files (git, studio, project skills), `.git/nextboard-trash/` or `.nextboard-trash/`, `<cfg>/skills|commands` with confirmation, and the temporary folders `/tmp/nb-usage-<uid>-<hubId>` and `/tmp/nb-run.*`, which are always deleted. Paste folders on a server are `/tmp/nb-paste-<uid>-<hubId>-<sid>` and new tmux sessions `nb-<hubId>-<sid>`, so two hubs that share a server never clean up each other's.

## 9. API and events

Conventions: camelCase JSON, RFC 3339 times, errors as `{"error","code"}`, the `X-NextBoard: 1` header on requests that change data, a cookie or `Authorization: Bearer`.

| Area | Routes | Contract |
|---|---:|---|
| `/api/projects/*` (git, commands, loop, memory, file, LSP, DAP, studio) | 61 | Several documents below |
| `/api/machines/*` (fs, skills, presets, probe) | 34 | [API.md](API.md), [API-board.md](API-board.md), [API-studio.md](API-studio.md) |
| `/api/sessions/*` | 20 | [API.md](API.md), [API-agents.md](API-agents.md) |
| `/api/workspaces/*` | 11 | [API-board.md](API-board.md), [API-orchestration.md](API-orchestration.md) |
| `/api/gui/*` | 9 | [API-gui.md](API-gui.md) |
| `/api/loops/*` | 8 | [API-orchestration.md](API-orchestration.md) §11 |
| `previews`, `memory`, `documents`, `extensions`, `mcp`, `auth` | 5 per area | By area |
| Others (`agents`, `profiles`, `layouts`, `usage`, `threads`, `jobs`, `devices`, and so on) | 1–4 per area | By area |

Outside `/api`: `/nb/hook`, `/nb/mcp/{sid}`, `/nb/mcp-job` on the loopback hook listener; `/__nb/*` on the preview listeners.

**`/api/events` events:**

| Group | `type` |
|---|---|
| Core | `snapshot`, `session`, `removed`, `machine`, `machineRemoved`, `device`, `deviceRemoved`, `project`, `projectRemoved`, `boardOps` |
| Agents | `attention`, `settings`, `agents`, `profile`, `profileRemoved` |
| Orchestration | `delivery`, `thread`, `threadCleared`, `order`, `confirmation`, `command`, `handoff`, `forward`, `loop`, `loopRemoved`, `loopRun`, `memory`, `layouts` |
| Studio | `job`, `jobOutput`, `git`, `gitRepo`, `github`, `preview`, `previewRemoved`, `previewSuggested`, `previewLog`, `document`, `documentRemoved`, `diagram`, `usage`, `usageReset`, `skills`, `fsChanged` |
| Apps | `guiSession`, `guiSessionRemoved` |
| Credentials | `vaultChanged`, `credentialRequest` |

## 10. Clients

| Client | Location | Status |
|---|---|---|
| Web | `web/`, embedded in the binary | Active, follows the hub |
| Android | A separate project, not published yet (Kotlin, Compose, no Google Play Services) | **Paused.** Covers up to Phase 7, no canvas editing on phones |
| Desktop | A separate project, not published yet (Tauri v2) | **Paused.** Multi-hub, shows the hub's SPA through a local proxy |

Do not change, build, or test the Android and desktop folders unless the owner asks.

**What the clients need to catch up on when work resumes:**

- `app` and `editor` nodes show as "Unsupported tile" in clients that do not know them yet.
- The multi-repo picker does not exist yet; without `?repo=` the old behavior stays.
- `settings.editor.onSave` is not used by Android yet.
- Features after Phase 7 are generally missing from Android: the memory galaxy, MCP, switching agents.

**Contracts that must stay stable,** because the desktop app parses them: `var HUB = <array JSON>;` in `bridge.js`, the `<skema>://<host>:<port>` entries in the CSP `frame-src`, preview objects with an `id` of `pv_…` and a `listenPort`, and a `launchPath` of the form `/__nb/launch?t=…`.

## 11. Development, testing, deployment

Needs Go 1.26+ and Node 22+. Go module: `github.com/azamrizki/nextboard`. `go install` builds a hub without the UI, because the web bundle is not in the repo; its main page then says to use `make build`.

| Command | Purpose |
|---|---|
| `make dev` | Hub + Vite; web `:15173`, hub `:12312`, data `./.devdata`, which `scripts/devinstance` makes the instance `dev` (cookie `nb_session_http_dev`, so it does not log the browser out of an installed hub on the same host; one login again after the upgrade); a changed Go file → the hub restarts through air (`RELOAD=0` turns this off) |
| `make dev-hub`, `make dev-web` | One side only |
| `make test` | `go vet` + `go test -race -timeout 30m ./...` |
| `make test-fast` | The same tests side by side (`scripts/go-test-fast.sh`): `internal/server` and `internal/machine` split by test name (`scripts/ci-shard.sh`, as the release workflow does) and run at the same time as the other packages, after everything is compiled |
| `make test-web` | Web unit tests (Vitest); needs `web/node_modules` |
| `make test-e2e` | Browser tests (Playwright) against a temporary hub with fake agents; needs `cd web && npx playwright install chromium` once. Not part of the release workflow |
| `make test-e2e-fast` | The same suite over several temporary hubs side by side (`scripts/e2e-shards.sh`, `--shard=i/n`, each hub on its own port and folder); `E2E_SHARDS=n`, default a third of the CPUs (at most 6). The whole suite took 9.5 min in 4 shards against ~30 min on one hub. A failed shard's summary is printed at the end, its log is `$TMPDIR/nextboard-e2e-shard-<i>.log` |
| `make test-ssh` | SSH tests against a temporary sshd |
| `make test-db` | The database browser's tests against real MySQL 8.4 and PostgreSQL 18 in throwaway Docker containers it starts and removes (`scripts/db-test.sh`) |
| `make build` | Web + `bin/nextboard` |
| `make snapshot` | Local goreleaser |

`make dev` binds to `0.0.0.0` by default, unlike the release default `127.0.0.1`. Use `LISTEN=127.0.0.1:12312` on an untrusted network.

**Go hot reload (`.air.toml`, `scripts/dev-build.sh`, `scripts/dev-hub.sh`).** Air watches `cmd/` and `internal/`, except `_test.go`. The hub restarts only when its binary changes: a failed build, comments, and a save with the same contents do not touch it. A restart is still a full restart: the hub stops local consoles and then resumes them according to `agents.resumeOnRestart` (a running turn is cut off), and pending deliveries, confirmations, and lead plans are lost (API-orchestration §15). GUI apps keep running because the hub runs outside air's process tree. When an agent on the board works on this repo itself, use `make dev RELOAD=0`, so the agent's saves do not cut off its own turn.

**Test gates.** By default `go test` does not touch real CLIs or the owner's plan.

| Variable | Turns on |
|---|---|
| `NEXTBOARD_SSHD_TEST=1` | SSH tests with a throwaway sshd (also run during a release) |
| `NEXTBOARD_TMUX_SSH`, `_USER`, `_KEY` | tmux mode tests against an sshd with tmux |
| `NEXTBOARD_REAL_CLI_TEST=1` | Real CLIs; **uses the owner's plan** |
| `NEXTBOARD_REAL_ORCH=1`, `NEXTBOARD_REAL_USAGE=1` | Real orchestration and usage, together with the gate above |

**Rules on a shared host.** If This PC also runs other services, the tests use the same memory, and `/tmp` may have a per-user quota. A recursive test helper once spawned hundreds of `Xtigervnc` processes until the OOM killer hit other services. So: tests that spawn processes are run as one small case first with `-timeout`, leftover processes are counted afterwards, and a test must not exec itself without a guard.

**CI and releases (decision #36).** During development, CI does not run on every push or pull request, so run the tests yourself before pushing. A release is started by hand from Actions or with `gh workflow run release.yml`, with `-f dry_run=true` to see the next version without publishing anything. The checks run side by side, each job on its own runner: `make web` + `make test-web`; `go vet` + a trial goreleaser build; and `NEXTBOARD_SSHD_TEST=1 go test -race -count=1 -timeout 30m` of every package, with the web bundle in place. internal/server is dealt out by test name over eight runners and internal/machine over three by `scripts/ci-shard.sh`, which first checks against the test binary that the shards together hold every test once (`scripts/ci-shard.sh ./internal/server 8 -- -race` prints the split); the other packages are dealt out over two. Only once every check passed does the release job start: semantic-release works out the version from the commit messages since the last tag and creates the tag, then goreleaser publishes linux/darwin × amd64/arm64 + `checksums.txt`. In one job, one check after another, a release took about 14 minutes (internal/server alone close to ten); side by side about 4 (the checks about 2½, then the release job). On a branch other than main and beta only the checks run (`gh workflow run release.yml --ref <branch>`). A tag pushed by hand no longer triggers anything. The first release, `v0.1.0`, came out 2026-11-05 through a manual tag.

Commits use Conventional Commits: `fix:`, `perf:`, `revert:` → patch; `feat:` → minor; `docs:`, `test:`, `ci:`, `chore:`, and other commits → no release. Before 1.0, a breaking change (`feat!:`, a `BREAKING CHANGE:` footer) is also minor. The rules are in `.releaserc.json`.

**Beta.** The `beta` branch is semantic-release's prerelease branch. First bring it level with main (`git push origin main:beta`), then `gh workflow run release.yml --ref beta`. The result is a `vX.Y.Z-beta.N` tag, and goreleaser (`release.prerelease: auto`) publishes it as a pre-release on GitHub, so `releases/latest` stays the last stable release: `nextboard update` and `install.sh` do not install a beta unless asked to with a tag (`nextboard update --version v0.4.0-beta.1`, `install.sh --version=…`). `nextboard update` without a tag also never downgrades: a hub running a beta newer than the last stable release is left alone, with the command `--version … --force` to go back.

**Deploy.** `deploy/nginx.conf.example` + `deploy/nextboard-proxy.inc`:

- The main port and each preview port each get HTTPS.
- Preview ports in nginx must bind to the public IP, not `0.0.0.0`, so they do not clash with the hub.
- HTTPS on a port other than 443: `nextboard install --public-url https://host:port`.
- `tailscale serve` only forwards the main port, so web tiles do not work through it.

**Requirements for desktop tiles (optional).**

```sh
sudo apt install --no-install-recommends sway wayvnc wf-recorder xwayland   # main, needs GPU + ffmpeg VA-API
sudo apt install tigervnc-standalone-server openbox                         # fallback without GPU
```

## 12. Owner decisions

Source: [ROADMAP.md](ROADMAP.md).

| # | Date | Decision |
|---|---|---|
| 1 | 10-28 | Live status over SSH through per-launch hooks, without writing config. *The remote agent part is replaced by #10* |
| 2 | 10-28 | Temporary folders in the remote `/tmp` are allowed, always cleaned up |
| 3 | 10-28 | Skills on a remote may be written, only on an explicit action with path confirmation |
| 4 | 10-28 | Remote profiles use only folders that already exist. *The remote agent part is replaced by #10* |
| 5 | 10-28 | Agent exits → the console drops to a shell in place |
| 6 | 10-28 | Planner = local Claude Code on This PC, no cloud planner |
| 7 | 11-02 | The hub is HTTP only, HTTPS in nginx |
| 8 | 11-02 | Core backend first; Android and desktop paused |
| 9 | 11-03 | Desktop app profiles dedicated to NextBoard, one per app |
| 10 | 11-03 | Agents always on This PC, state in the NextBoard folder, servers through hub tools (option A) |
| 11 | 11-03 | 1 project = 1 memory, whatever the agent |
| 12 | 11-03 | 1 project may have many repos; never commit to every repo |
| 13 | 11-03 | Outside sites in a web tile use a real browser, not an iframe or proxy. *Changed by #19 and #26* |
| 14 | 11-03 | Critical notes are mandatory; a direct request does not count as permission |
| 15 | 11-03 | Switch agents in the same tile; the old conversation can be continued |
| 16 | 11-03 | MCP is set up in NextBoard, given per launch, not written to CLI config |
| 17 | 11-03 | A tile's content is sent once when wired, not automatically every time an agent works |
| 18 | 11-03 | Pasted files are stored by the hub outside the project |
| 19 | 11-03 | Addresses that resolve to a Tailscale IP are shown through the hub proxy |
| 20 | 11-03 | Uptime: the hub does the checking, monitors belong to the project, cards only display |
| 21 | 11-03 | Wrong SSH password: the machine stops and asks, does not retry |
| 22 | 11-03 | Turning agents off: one switch for every machine, running consoles are not touched |
| 23 | 11-04 | The right rail holds only what matters, the rest is in the right-click ring |
| 24 | 11-04 | Agent review and Local History through a shadow repo in the hub's data folder |
| 25 | 11-04 | A home-made Node.js debugger in the hub, not `js-debug` |
| 26 | 11-04 | Public sites are tried through the hub proxy first, Chrome for those that cannot work that way |
| 27 | 11-04 | Monochrome light and dark themes, chosen per browser |
| 28 | 11-04 | Model and effort stored per console, sent as flags on every launch |
| 29 | 11-04 | Task boards moved by the hub from order status; automatic assignment only when turned on |
| 30 | 11-04 | Zero-knowledge credentials like Bitwarden, plain HTTP supported too |
| 31 | 11-04 | Master password at least 8; 6-digit PIN needs the browser and the hub; filling a form is always a person's click |
| 32 | 11-04 | Project groups are a tree of their own, not a path in the name |
| 33 | 11-04 | Agents do not sign commits, applies globally |
| 34 | 11-05 | Orders not yet typed when the hub restarts are offered for resending; the hub does not send them by itself |
| 35 | 11-05 | Open source: public repo, MIT license, personal data stays out of the repo |
| 36 | 11-05 | CI runs only during a release; releases are manual, the version is set by semantic-release from commit messages |
| 40 | 11-06 | Agents use a credential through one command approved per use; the value never reaches the agent |

## 13. Status and history

| Phase | Contents | Status |
|---|---|---|
| 1–3 | Core hub, agentless SSH, pairing and apps | Done; the SPEC §12 criteria are in the table below |
| 4 | Projects, workspaces, multi-tile board | Done: hub, web, Android |
| 5 | Agents, status, notifications | Done: hub, web, Android |
| 6 | Orchestration | Done: hub, web, Android |
| 7 | Web, Git, studio, usage, voice | Done: hub, web, Android, desktop |
| 8 | Desktop apps on the board | Done: hub, web |

After Phase 8, not recorded as phases: agents always on This PC and default profiles, agents on servers, MCP, switching agents in the same tile, the memory galaxy with critical notes and Init, multi-repo, real browsers, the Code workbench as a card with LSP, DAP, extensions, AI, agent review and Local History, board control by the planner, the lead agent and task boards, Credentials, uptime, light and dark themes, project groups, the connection log, hub memory limits.

**There is no written plan after this yet.** The candidates for the next work are §14, and their order is the owner's decision.

### Phase 1–3 done criteria (SPEC §12), checked 2026-11-05

The test names are in `internal/cli`, `internal/server`, and `internal/machine`. The tests in `internal/cli` run the real binary with a temporary `HOME` and fake `systemctl`, `loginctl`, and `curl`.

| Criterion | Status | Evidence and its limits |
|---|---|---|
| Install through `curl … \| sh`, print a setup link | Tested | `TestInstallScriptFromReleaseToSetup`, `TestInstallScriptRefusesABadChecksum` with a fake release and a fake `systemctl`. The real release was tried in an Ubuntu 24.04 container with real systemd: the public `install.sh` installs `v0.1.0`, the service is active and comes back up after the container restarts |
| HTTPS in nginx, wizard, login | Tested | `TestBehindTheNginxExample` uses the `deploy/` files in nginx 1.24 and 1.28. The wizard page is checked only when the build has the web bundle. Preview ports are only loaded, not tried |
| Reboot, the service comes back | Partial | The test makes sure `enable-linger` is called. A real reboot has never been tried |
| Terminal: output, `vim`, `htop`, Ctrl-C | Tested | `TestTerminalIsARealOne`: raw mode, alternate screen, Ctrl-C, resize, `vi`, `htop` |
| Browser closed and reopened | Tested | `TestTerminalGoesOnWhileTheBrowserIsGone` |
| Service restart: position, scrollback, continue | Tested, without a real systemctl | `TestHubProcessComesBackFromSIGTERM` uses a real process and real signals. Claude now continues with `--resume <uuid>`, not `--continue` (`TestHubRestartResumeModes`) |
| 401 without auth, single-use setup link | Tested | `TestEveryRouteRefusesAnonymous` walks every route from the source. `TestPublicRoutesAreOnlyThese` locks the list of public routes |
| Machine from `~/.ssh/config` | Tested | `TestAddMachineFromSSHConfig`, `TestSSHConfig*` |
| New host fingerprint, changed host key | Tested | `TestSSHAgentlessEndToEnd`, at the machine layer |
| Preset and tmux probe | Tested | `TestProbeFindsEveryCommandInEachShell` in sh, dash, bash, zsh, with a fake `PATH` |
| Claude Code on a remote | Superseded | Decision #10: agents always on This PC (`TestAgentsStayOnThisPC`) |
| tmux mode comes back after a drop | Partial, not in CI | `TestSSHTmuxPersistence` needs an sshd with tmux. A hub restart in this mode is not tested yet |
| direct mode comes back after a drop | Partly superseded | Only shell consoles remain on servers: `TestServerConsoleComesBackAfterADrop`. `resumeArgs` does not apply to shells |
| Target server unchanged | Checked once, not a test | In a container, after the SSH tests only `.bash_history` changed, because the tests type in an interactive shell. Decisions #2, #3, and #18 allow `/tmp` and skills on command |
| Pair through QR, device registered | Tested without QR | `TestPairingDeviceAuthAndRevoke`, `TestRevokeCutsADeviceOffAtOnce`. Not the QR itself |
| Revoke cuts the connection | Tested | `TestRevokeCutsADeviceOffAtOnce`: terminal and events. GUI, LSP, and IDE use the same socket list, without tests of their own |
| App refuses an untrusted certificate | Outside the repo | Android client |

### Tested for real

| Item | Recorded result |
|---|---|
| Claude Code 2.1.312–2.1.316, hooks, wrapper, delivery | Order typed, confirmed in 0.46 seconds, report from the transcript |
| Planner `claude -p` | 4.4 seconds, `structured_output` filled |
| Codex 0.163–0.165 | Notify, resume by id, console on a server without yolo |
| tmux 3.4 and 3.2a, temporary sshd, zsh login shell | Passthrough, reattach, git, preview, skills |
| Claude and Codex usage | Windows parsed, folder deleted |
| Documents | 1 Haiku run, 24.7 seconds |
| Critical notes | Haiku refuses `npm run db:reset` |
| Memory tile | Haiku receives the stack from the launch, reads a block through `memory_read`, and replies `OK` to a 163-character send; a send without new facts types nothing (`TestRealMemoryTile`) |
| Blocked commands | Claude Code stops at the permission dialog, also through `bash -c`, and the script does not run (`TestRealBlockedCommand`). Skip permissions mode was not tried |
| Chrome 156 on Wayland | github.com, Wikipedia, tabs, hub restart (through Playwright) |
| Firefox 159 on Wayland and X11 | Pages, tabs through the bus, tabs after a hub restart, a second tile (`TestRealFirefox`, official tarball) |
| Video | Intel Arc A380 |
| Workbench | gopls, pyright, typescript-language-server, Vue, Tailwind, ESLint, rust-analyzer, taplo, Delve, debugpy in the browser; two Open VSX extensions |
| Diagrams | Two Claude Code jobs on the dev hub, about 95 seconds each |
| Lead agent | One turn with a real model, 33 seconds |

### Never tested for real

- Gemini CLI (not installed) and the catalog flags marked † in API-agents.
- The workbench's AI paths with a real model (the tests use a fake CLI).
- The lead's `danger` field from a real model: in the 2026-11-11 test (API-orchestration §3.9) the model refused or asked back, and never filled it. Task steps, work splitting, and `split` forwards have passed with a real model.
- `idelink` with a logged-in Claude and a real model.
- Agent review with Codex, and Codex getting past the login screen in the card panel.
- Pull requests to the real GitHub (the tests use a fake `gh`).
- The agent panel in a project on a server over SSH with a real model. With a fake agent and a real sshd (default MaxSessions), the Code card on a server project is tested in `e2e/studio-ssh.spec.ts`: a tree with ignored files, save, search, commit, terminal, and an agent in a tab.
- OpenCode getting as far as answering on a server.
- The connection log on a real network (so far only through a local TCP proxy).
- macOS, NVENC, preview iframes with a self-signed certificate.
- Browsers from snap: the Wayland handshake from inside a real snap AppArmor confinement is done, the snap browser itself is not. Browsers from flatpak: only their command line.
- Passkeys for other sites in Google Chrome and on real internet sites: end-to-end only with Chromium and a page on localhost, verified the way a relying party does.
- A microphone with a real app that records: it reaches the session's PipeWire source, but not yet a site in Chrome.
- Laravel on a real Laravel app: the route and config readers are tested only with fixtures, and `artisan` with real PHP but a made-up artisan file.
- Review and Local History on a real server: only a temporary sshd so far.
- Debug attach on a real server: only a temporary sshd so far.

## 14. Unfinished work and risks

### Main risks

1. **Releases have only been tried in a container.** `v0.2.0` came out through semantic-release. On Ubuntu 24.04 with real systemd inside a container: `install.sh` installs `v0.1.0`, `nextboard update` replaces it with `v0.2.0` with data and login kept, the service comes back after the container restarts, and uninstall is clean. A real machine has not been tried yet, and the macOS binary has never been run.
2. **Command blocking is not a sandbox** (§6); a real project sandbox exists since 2026-11-11 for Codex (tested) and Claude Code (needs an AppArmor step on Ubuntu, API-agents §27). Critical notes remain instructions. Blocked commands are enforced by the CLI or the hub, but an agent with a shell can still write a script that does the same thing. Enforcement in Codex and OpenCode, and in Claude Code with skip permissions, has not been tested with a real model.
3. **Git write actions still use the repo's config.** Stage, commit, pull, and push run the repo's hooks, filters, and `core.sshCommand`, the same as git in a terminal. Read commands no longer do (§5.11), but on git < 2.31 the repo's own filter drivers still run during `status` and `diff`.

### Gaps by area

| Area | Gap |
|---|---|
| Workbench | Node debugging on a server is attach only, without source maps and child processes. Extensions are data only, because Monaco has no extension host. Planner changes do not go into the client's undo; recovery is through snapshots. Agent review and Local History on a server work from a copy on This PC, without credential files and links |
| Agents on servers | Server skills and commands reach all three CLIs in disarmed form, but are tested only against a fake model; a real model choosing its own skill has not been tested. Git uses the server's credentials. OpenCode has never been seen answering with a real provider; its login is still in OpenCode's own folder. Other catalog agents are not supported yet |
| Status and delivery | Agents without screen rules are read generically. Reading the CLI process (`rchar` plus the bytes its TCP socket received, so also CLIs that read through `recv()` such as Python and Rust) holds `working` while the model streams; a model that thinks without sending anything can still read as `ready`: after 3 seconds on a prompt screen, after 15 seconds on other screens. Not tested with a real Aider or Goose. There is no list yet of CLIs that are safe for `whileWorking:"type"` |
| Orchestration | A group frame can be slightly off when two clients move members at the same time |
| Multi-repo | Without ripgrep, Search in a parent folder uses only the prune list |
| Git | Files > 100 MB can only be removed from commits that are not pushed (API-studio §3.7). On a server over SSH, a commit waits for processes left behind by hooks, for at most 2 minutes |
| Preview | Rewriting `Host`/`Origin` can break apps that build absolute URLs, such as an OAuth callback. Needs `AllowTcpForwarding` on the server |
| Desktop apps | VA-API only, tested only on an Arc A380. Shortcuts the browser holds on to (Ctrl+T, Ctrl+W, Ctrl+N) only through the keyboard menu in the tile. The microphone needs HTTPS or localhost. `wayvnc` 0.9.1 occasionally segfaults (the hub starts it again within about 2 seconds). On a machine without a real sound output, the session sink can become the default |
| Browser tiles | Snap and flatpak have never been tried with a real browser ([API-gui §8](API-gui.md)). A second tile has its own profile and login. Firefox is used only when there is no Chromium-family browser; there is no setting to choose yet |
| Usage | Each Claude check adds 2 lines to `history.jsonl` in the config in use. Codex 0.165 writes `[tui]` to `config.toml` once, and the hub cannot prevent it without writing config. The screen format can change at any time |
| Handoff | A summary for a remote source moves transcript contents to This PC |
| Credentials | Passkeys for other sites need an extension in the browser tile: automatic in Chromium, once by hand in Google Chrome, not available in Firefox ([API-vault.md](API-vault.md)). Android and desktop cards do not exist yet. Agents using a credential is tested only with a fake agent and Claude Code; Codex and OpenCode not yet ([API-vault.md](API-vault.md)) |
| Uptime | The public page does not show groups or maintenance schedules. WebSocket checks only the upgrade, without exchanging messages. Steam only from This PC. The public status page goes down with the hub, so it cannot report that its own hub is down ([API-uptime.md](API-uptime.md)) |
| Phone and tablet layout | Done: the frame (header, phone tab bar, `…` menu, sheets), every page, and the board on touch screens (labeled Insert, long-press, two fingers, Focus mode, Select several), the board on phones (Board, Cards, Lead tabs), the console key bar (Esc, Ctrl, arrows, copy text, attachments), the touch path of every card (Kanban and Memory tile grips, tap to edit, Rename and Straighten in the menu, the Calendar day list, charts with tap), the Code card and studios on phones and tablets (side bar drawer, editor key bar, list then contents in studios), and installing as an app (manifest, icons, Settings › Install app, a warning and a way out on plain HTTP) (DESIGN §3.0, §3.5, §3.6, §3.9, §3.17). No service worker, so nothing runs offline. Tested with Chromium emulation; real devices (Poco F5, Xiaomi Pad 6, Galaxy Tab S9) not yet |
| Tests | Web unit tests cover the vault, markdown, sockets, memory blocks, wire rules, and layout choice (148 tests). Browser tests in the repo (`make test-e2e`) so far cover the memory tile, the Send dialog, the blocked command list, the public status page, credential approval for agents, the vault window from the header, phone, tablet, and desktop layouts on every page (projects `phone`, `tablet-portrait`, `tablet-landscape`), the board on touch screens (Insert, long-press, two fingers, Focus mode, lines, Select several), the board on phones (Cards list, swipe, Board and Lead tabs), the console key bar (keys, one-shot Ctrl, copy text, photo attachments), cards by finger (Kanban scrolling and grips, long-press on a card, editing a Checklist with tap, Memory tile grip, Calendar day list, Rename in the menu, Straighten, `?card=` links), the Code card and studios on phones and tablets (side bar drawer, the editor key bar up to a saved file, Find then Esc, the terminal panel key bar, list then contents in studios), an installable manifest and the Install app pane (browser offer, installed, HTTP warning), plus a sweep of every page, the Settings panes, dialogs, every card kind in focus mode, the Laravel view, the debugger process picker, the large file warning, the browser tile header, and the passkey dialog in all five profiles (nothing goes past the screen edge); other UI flows are still tried outside the repo. The remaining SPEC §12 criteria are in §13 |
| Clients | See §10 |
| Instances | Tested in Go (two hubs sharing one cookie jar, `install --instance` with a fake `systemctl`) and in e2e (two hubs in one browser, the info panel, New instance, the wizard). Not automated, tried by hand only or not yet: real Chrome and Firefox over plain HTTP on two ports of a Tailscale IP, two nginx subdomains over HTTPS, macOS LaunchAgents and whether Claude Code keeps its Keychain entry per `CLAUDE_CONFIG_DIR`, a real snap Firefox tile in two instances |

### Questions for the owner

- Android and desktop: resumed only if the owner asks (decision 2026-11-05).
- The order of the next work.

## 15. Document map

| Document | Contents |
|---|---|
| [../README.md](../README.md) | Installation and a short development guide |
| [SPEC.md](SPEC.md) | Original Phase 1–3 specification, network, auth, SSH, memory limits |
| [ROADMAP.md](ROADMAP.md) | Phases, owner decisions, CLI flag verification |
| [DESIGN.md](DESIGN.md) | The NextBoard Glass design system: tokens, components, accessibility |
| [API.md](API.md) | Conventions, auth, machines, sessions, WebSocket, connection log |
| [API-board.md](API-board.md) | Projects, workspaces, boards, files, assets |
| [API-agents.md](API-agents.md) | Catalog, wrapper, status, transcripts, profiles, settings, notifications, agents on servers, MCP |
| [API-orchestration.md](API-orchestration.md) | Delivery, commands, planner, threads, groups, handoff, forward, loops, memory, layouts |
| [API-studio.md](API-studio.md) | Jobs, runner, Git, previews, studios, usage, voice |
| [API-gui.md](API-gui.md) | Desktop apps, real browsers, video, sound |
| [API-uptime.md](API-uptime.md) | Uptime monitors |
| [API-vault.md](API-vault.md) | Credentials: encryption format, API, behavior, what is not protected |
| [API-db.md](API-db.md) | Database browser: connections, SSH tunnels, tables, SQL, Redis, safety, agents, SmartDB |
