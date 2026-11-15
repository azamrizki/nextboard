# NextBoard — Visual Board for AI Coding Agents (Self-Hosted)

> Name: **NextBoard** (binary / CLI: `nextboard`). An open-source project under the MIT license.
> Inspired by Symvibe (a visual board for agents), but built from scratch.

## 1. Goal

**One system, installed once.** After `nextboard` is installed on a computer or server:

1. The daemon runs right away as a service and serves the **Web UI on port 12312**.
2. From the Web UI, you manage many AI agent terminals (Claude Code, Codex, Gemini CLI, shell) as an **infinite visual board**. Each agent becomes one terminal node.
3. Other devices (another browser, the desktop app, the mobile app) are **paired from the Web UI** with a QR or a code.
4. Besides "This PC", you can **add other machines over SSH, agentlessly**, without installing or changing anything on the target server. Agents on that machine show on the same board.

**Problems it solves:**
- Too many terminal tabs and SSH sessions, which makes it hard to see which agent is working and which is waiting for input.
- Agents stop when the laptop is closed.
- No way to watch agents from a phone.
- Agents spread over several servers with no one place to see them all.

## 2. Core concepts

```
              ┌──────────────── HUB (nextboard daemon) ────────────────┐
Devices  ───► │ Web UI :12312 · Auth & pairing · Board · Session mgr  │ ───► Machines
(clients)     └────────────────────────────────────────────────────────┘      (where agents run)
 • Browser (login password)                                                    • This PC (local PTY)
 • Desktop app (paired)                                                        • Server A (SSH, agentless)
 • Mobile app (paired)                                                         • Server B (SSH, agentless)
```

| Term | Meaning |
|---|---|
| **Hub** | The installed `nextboard` daemon. It serves the UI and API, stores all state, and manages sessions. |
| **Device** | A client that views and controls the board: a browser, the desktop app, the mobile app. |
| **Machine** | A computer where agent processes run. `local` ("This PC") always exists, and other machines are added over SSH. |
| **Session** | One terminal (one agent process or shell) on a machine, shown as one node on the board. |

## 3. Decisions already made

| Aspect | Decision |
|---|---|
| Hub | **Go**, one binary, frontend embedded via `go:embed` |
| Frontend | **React + Vite + TypeScript** |
| Canvas | `@xyflow/react` (React Flow) |
| Terminal | `@xterm/xterm` + `@xterm/addon-fit` (+ `@xterm/addon-webgl` with a fallback) |
| Local PTY | `github.com/creack/pty` |
| SSH | `golang.org/x/crypto/ssh` (+ `knownhosts`, `agent`), `github.com/kevinburke/ssh_config` to read `~/.ssh/config` |
| WebSocket | `github.com/gorilla/websocket` |
| Password hash | argon2id (`golang.org/x/crypto/argon2`) |
| Default port | **12312** |
| Remote machines | **Agentless SSH.** No installation, config files or new keys on the target server |
| Storage | JSON files in `~/.nextboard/`, no database |
| Repo | GitHub, binary releases through GitHub Releases |
| Scope | Personal, one user (admin). No multi-tenancy, billing or cloud relay |

Supporting clients live in sibling folders: `../nextboard-android` (a native Android app, Kotlin + Compose — replacing React Native because pinning self-signed certificates and the Keystore are simpler natively) and `../nextboard-desktop` (Tauri).

## 4. Installation and service

### One-step install

```bash
curl -fsSL https://raw.githubusercontent.com/<user>/nextboard/main/install.sh | sh
```

`install.sh` takes these steps:
1. Detects the OS and architecture (linux/darwin, amd64/arm64).
2. Downloads the binary from GitHub Releases and verifies its SHA-256 checksum.
3. Installs the binary to `~/.local/bin/nextboard`, or to `/usr/local/bin` with `--system`.
4. Runs `nextboard install` (`--instance=<name>` is passed on as `nextboard install --instance <name>`). Instances already running on the binary it replaced are restarted, as `nextboard update` does.

### `nextboard install`

- Creates `~/.nextboard/` (mode 0700) with its default files; with `--instance <name>`, `~/.nextboard-<name>/` with `instance`, a new `hubId` and the instance's own block of ports in `config.json`.
- Registers the service:
  - **Linux:** a systemd **user** service (`~/.config/systemd/user/nextboard.service`, or `nextboard-<name>.service`) plus `loginctl enable-linger $USER`, so that it keeps running after logout and reboot.
  - **macOS:** a LaunchAgent (`~/Library/LaunchAgents/id.nextboard.plist`, or `id.nextboard.<name>.plist`).
- The service runs as **your own user**. This is deliberate, because agents run with that user's rights and use the SSH keys in `~/.ssh`. Agent logins and state are in `<data>/agents/<agent>` (decision 10); `~/.claude` is not touched.
- Starts the service, then prints:
  ```
  NextBoard is running at http://127.0.0.1:12312
  Open this setup link (single use, valid for 30 minutes):
  http://127.0.0.1:12312/setup?code=K7QX-M2PA
  ```

### CLI subcommands

| Command | Purpose |
|---|---|
| `nextboard serve` | Runs in the foreground (used by the service, also for dev) |
| `nextboard install` / `uninstall` | Installs or removes the service (data is kept unless `--purge` is given; `--purge` refuses while the hub runs) |
| `nextboard status` | Service status, address, and the number of sessions, machines and devices; `--all` for every instance |
| `nextboard instances` | Lists every instance on the machine with its state, address, data folder and service |
| `nextboard logs [-f]` | Shows the service logs |
| `nextboard setup-link` | Creates a new setup link (if the old link expired before setup) |
| `nextboard reset-password` | Resets the admin password from the server's terminal |
| `nextboard pair` | Prints a QR + pairing code in the terminal (an alternative to the Web UI) |
| `nextboard update [--version tag]` | Downloads the latest stable release (or the tag named, e.g. a beta) and restarts every instance's service that runs the binary; never downgrades without `--force` |

Every command takes `--instance <name>` as well as `--data-dir`.

### First setup (wizard in the Web UI)

1. The setup link carries a single-use code, so others on the same network cannot "claim" the hub before you do.
2. The wizard shows which instance is being claimed (its name only, until the admin has logged in), asks you to create the admin password and name the hub (for example `devbox`, or `devbox · work` for an instance), then, for a named instance or when other instances exist, how this instance's agents sign in (separate NextBoard logins, your own CLI logins, or an API provider), offers to copy from another instance on this machine with that instance's admin password, and shows the agent CLIs detected on This PC.
3. For those, a summary names the instance, data folder, config file, service, port and addresses, and says that instances of one OS user are not a confidentiality boundary. Then the empty board shows.

### Several instances

One OS user can run several hubs on one machine. Each **instance** has a name (`^[a-z][a-z0-9-]{0,23}$`; `default` is `~/.nextboard`), a data folder `~/.nextboard-<name>`, a block of 100 ports (`12312 + 100·k` for the hub, `12320 + 100·k` to `12339 + 100·k` for previews), a service `nextboard-<name>` (`id.nextboard.<name>`), a cookie `nb_session[_http]_<name>` and an admin of its own. The `default` instance keeps every name it had before instances existed. A hub holds `<data>/hub.lock` while it runs. A new instance comes from `nextboard install --instance <name>` or from **New instance…** in an existing instance's About this hub ([API.md Instances](API.md#instances)). Instances keep data and accounts apart; they are not a confidentiality boundary, since every instance runs as the same OS user.

## 5. Network and HTTPS

> Owner decision (2026-11-02): the hub and the preview listeners serve only **plain HTTP**, both in `make dev` and in release builds. **HTTPS is terminated in nginx** on deploy. The hub no longer creates or loads certificates.

- **Default listen:** `127.0.0.1:12312`, because nginx is on the same host. The hub logs a warning when it is bound to a non-loopback address: without TLS, passwords, tokens and terminal content can be sniffed on that network.
- **Deploy:** `deploy/nginx.conf.example` + `deploy/nextboard-proxy.inc`. The main port and each preview port (`settings.previews.portRange`) each get HTTPS in nginx. Preview ports in nginx must bind to the public IP, not `0.0.0.0`, so that they do not clash with the hub on `127.0.0.1:<port>`.
- **`X-Forwarded-Proto: https`** decides the `Secure` flag on cookies, the CSP `frame-src` scheme, the origins allowed to frame previews, and the pairing URL. This header is **trusted only from a loopback peer**, so nginx must be on the same host.
- **HTTPS ports other than 443:** fill in `extraUrls` in `config.json` (or `nextboard install --public-url https://host:port`) so that the origin may frame previews.
- **Do not open port 12312 to the public internet.** This hub is essentially a shell into your machine.

## 6. Auth and device pairing

### Browser: password login

- Log in with the admin password. The hub then creates a **session cookie** (`HttpOnly`, `SameSite=Strict`, plus `Secure` when the client comes over HTTPS) that is valid for 30 days and extended every time it is used. The cookie name differs per scheme (`nb_session` on HTTPS, `nb_session_http` on HTTP), so that a host once used over HTTPS can still log in over HTTP.
- **CSRF protection:**
  - Every request that changes data must carry the `X-NextBoard: 1` header.
  - WebSocket checks that `Origin` matches the host.
- **Login rate limit:** 5 failed attempts per IP per 10 minutes.

### Desktop and mobile apps: pairing

1. In the Web UI, open **Devices → Pair new device**.
2. The hub creates an 8-character **pairing code** (format `XXXX-XXXX`, an alphabet without ambiguous letters) that is valid for 5 minutes and can be used only once. The code is shown as a QR and as text:
   ```
   nextboard://pair?h=<hub-name>&u=<url1>,<url2>&c=<code>
   ```
   `u` holds the hub's addresses (over HTTPS: only HTTPS origins, no plain HTTP addresses) so that the app can try them one by one. The app trusts nginx's certificate through the system CAs; a fingerprint is pinned only when it is entered by hand during pairing (e.g. nginx with a self-signed certificate).
3. The app creates an **Ed25519 keypair**. The private key is stored in the Keychain or Keystore. The app then sends:
   ```
   POST /api/pair/complete  { code, deviceName, platform, publicKey }
   ```
4. The hub records the device in `devices.json`, and the device shows up in the Web UI right away.
5. **The device's later logins (challenge-response):**
   ```
   POST /api/auth/challenge  { deviceId }              → { nonce }
   POST /api/auth/device     { deviceId, nonce, sig }  → { accessToken (opaque, TTL 1 hour) }
   ```
   `accessToken` is sent as `Authorization: Bearer` for REST and WebSocket. The token is never put in the URL, because nginx records query strings in its access log.
6. In **Devices**, each device shows its name, platform, pairing time and last activity. The **Revoke** button cuts all of that device's connections at once.

## 7. Machines: agentless SSH

### Principle

> **No changes on the target server.** No new binary, service, config file or `authorized_keys` entry. The only requirements are a reachable `sshd`, and agent CLIs (claude, codex, etc.) that are already installed and logged in there. Since decision 10, agent CLIs always run on This PC, so a server needs only SSH.

### Adding a machine

In **Machines → Add machine**, there are two ways:
- **From `~/.ssh/config`.** The hub reads the `Host` list from the service user's config. If you can already run `ssh devbox` from the hub machine, just pick `devbox`. HostName, User, Port, IdentityFile and ProxyJump come from the config.
- **Manual.** Fill in the host, port, user and auth method.

### Authentication methods (order of attempts)

1. **Keys from the service user's `~/.ssh`.** Uses `IdentityFile` from the config, or the default `id_ed25519` and `id_rsa`.
2. **ssh-agent**, if `SSH_AUTH_SOCK` is available in the service's environment.
3. **A private key uploaded through the UI.** Stored encrypted (AES-256-GCM, master key in `~/.nextboard/secret.key` mode 0600). An optional passphrase, also encrypted.
4. **Password**, stored encrypted the same way. Not recommended.

### Host key verification

- The hub checks the service user's `~/.ssh/known_hosts` first.
- **Unknown host:** the UI shows the fingerprint and asks for confirmation (TOFU). Once approved, the fingerprint is saved to `~/.nextboard/known_hosts`.
- **Changed fingerprint:** the connection is **refused** with a red warning, with no auto-accept.

### Probe when a machine is added

```sh
uname -sm; echo "$SHELL"; command -v tmux claude codex gemini 2>/dev/null; echo "$HOME"
```

The result decides the OS/architecture, the default shell, the available presets, whether tmux is available, and the home directory (default cwd). Cached in `machines.json`. A **Re-probe** button is available.

### Running terminals on a remote machine

- **One TCP connection per machine.** Each terminal is a **separate SSH channel**.
- Per terminal: `client.NewSession()` → `RequestPty("xterm-256color", rows, cols, modes)` → run through a login shell:
  ```sh
  $SHELL -lc 'cd <cwd> && NEXTBOARD_SESSION_ID=<id> exec <command> <args...>'
  ```
  Env is inline (because of `AcceptEnv`). All arguments are shell-escaped. Resize via `session.WindowChange(rows, cols)`. Stdin/stdout go into the same mechanism as the local PTY.
- **Keepalive** `keepalive@openssh.com` every 15 seconds, each send waited on for 15 seconds; 3 failures = disconnected. On a silent network (no errors) the hub notices about 60 seconds after data stops, and calls in progress return only then.
- Automatic **reconnect** with a backoff of 1, 2, 5, 10, then 30 seconds. The node has status `reconnecting`.

### Persistence on remote machines

| Mode | When | Behavior |
|---|---|---|
| **tmux** | `tmux` is on the server | `tmux new-session -A -s nb-<id> -x <cols> -y <rows> '<cmd>' \; set-option status off`. The process stays alive even when SSH drops/the hub restarts; the hub attaches again. |
| **direct** | tmux missing / turned off | The process runs directly in the SSH channel; dropped = dead. When connected again it is relaunched with `resumeArgs`. |

Default **Auto** (tmux when available).

### Remote terminal actions

| Action | tmux mode | direct mode |
|---|---|---|
| Kill | `tmux kill-session -t nb-<id>` via exec | `session.Signal(ssh.SIGHUP)`, close the channel after 3 seconds |
| Restart | Kill, then create a new tmux session | Close the channel, open a new channel |
| Hub restart | Attach again to `nb-<id>` | Relaunch with `resumeArgs` |
| Orphan sessions | "Clean up" shows `nb-*` without a node, kills after confirmation | — |

### Supporting features

- **Folder picker (cwd):** read-only exec `ls -1pA -- <path>`.
- **Status:** busy/idle detection based on output.

## 8. Scope per phase

### Phase 1: Core hub (This PC)
A single binary + service; setup wizard; login; board (pan/zoom, drag by the header, resize, saved layout); local PTY with 1 MiB scrollback; presets (Shell, Claude Code, Codex, Gemini, custom via `presets.json`); persist/resume; realtime status via `/api/events`; node actions (rename, restart, restart+resume, close); multiple clients.

### Phase 2: Agentless SSH machines
All of section 7, plus a machine label on nodes and a machine picker in the toolbar.

### Phase 3: Pairing and apps
Pairing/device API + the Devices page; desktop app (Tauri); mobile app (Android): scan QR, a terminal list + a simple terminal view, quick actions (send input, restart, kill).

**Not included yet:** status hooks, handoff, loop/scheduler, bot node, voice, git panel, kanban/notes, studio.

## 9. Internal architecture

```
nextboard (Go)
 ├─ cli/            install, service, setup-link, reset-password, pair, update
 ├─ server/         router, auth middleware (cookie | bearer), CSRF, rate limit
 ├─ auth/           password (argon2id), sessions, setup code, pairing, device challenge
 ├─ machine/        Machine interface; local.go (creack/pty); ssh.go (x/crypto/ssh)
 ├─ session/        Session (Proc + ring buffer + subscribers + busy), Manager, presets
 ├─ secret/         AES-256-GCM for uploaded SSH credentials
 └─ store/          atomic JSON (tmp + rename), mode 0600
```

```go
type Proc interface {
    io.ReadWriteCloser
    Resize(cols, rows uint16) error
    Signal(sig string) error            // "HUP" | "KILL"
    Wait() (exitCode int, err error)
}
```

### Data layout (`~/.nextboard/`, mode 0700)

`~/.nextboard-<name>/` for a named instance. `config.json` holds `instance` and `hubId`; `hub.lock` is held while a hub runs on the folder.

```
config.json  hub.lock  auth.json  secret.key  devices.json  machines.json
creds/<id>.enc  known_hosts  presets.json  sessions.json  board.json  scrollback/<id>.log
```

## 10–11. API & implementation details

See [API.md](API.md) for the full contract (exact JSON shapes).

- Local processes: env + `TERM=xterm-256color`, `COLORTERM=truecolor`, `NEXTBOARD_SESSION_ID`. Kill: `SIGHUP` to the process group, `SIGKILL` after 3 seconds.
- Exit: the node stays, with the message `── process exited (code N) ──`. Subscribers are not closed.
- Command not found: a red error message in the terminal, and the node is still created.
- Frontend: drag only by the header; body `nodrag nowheel nopan`; FitAddon + ResizeObserver; layout saved with a 500 ms debounce; sessions without a layout are placed automatically in a grid.
- Dark theme. Status dot: blinking green = busy, green = idle, yellow = reconnecting, gray = exited, red = error/exit ≠ 0. Node header `Claude Code @ devbox`, an accent per preset, a machine badge.
- Security: files 0600/0700; SSH credentials never go to the client; max body (board ops 2 MB, others 64 KB); logs without passwords/codes/terminal content.

## 12. Done criteria

> The original criteria of phases 1–3. Empty boxes are not a status; the status is in [PROJECT.md §13](PROJECT.md).

### Phase 1
- [ ] `curl … | sh` installs the binary + service and prints the setup link.
- [ ] `https://<nginx domain>` shows the wizard; after setup, login works.
- [ ] Reboot: the service comes back (linger).
- [ ] Spawn Claude Code, realtime output; `vim`, `htop`, Ctrl-C work normally.
- [ ] Browser closed/opened: the terminal is still running, scrollback shows.
- [ ] `systemctl --user restart nextboard`: nodes come back in position, scrollback shows, Claude continues via `--continue`.
- [ ] A request without auth gets 401; the setup link cannot be used twice.

### Phase 2
- [ ] Add a machine from `~/.ssh/config` without touching the target server.
- [ ] A new host asks for fingerprint confirmation; a changed host key is refused.
- [ ] The probe detects presets and tmux.
- [ ] Claude Code on a remote shows as `Claude Code @ <machine>`.
- [ ] tmux mode: drop/hub restart → attaches again.
- [ ] direct mode: after recovery → relaunched with `resumeArgs`.
- [ ] The target server does not change at all.

### Phase 3
- [ ] Pair the app by QR; the device appears in the list.
- [ ] Revoke cuts the connection at once.
- [ ] The app refuses certificates the system does not trust, or that do not match the pin when a pin was entered by hand.

## 13. Roadmap after phase 3
Status via hooks; phone notifications; workspaces; handoff between agents; loop/scheduler; supporting nodes (notes/kanban, web preview, git panel); usage analytics.

## Connection log

The hub always records connection behavior to servers in `<data>/logs/connections.jsonl`, at most about 8 MiB, without commands, output, file contents or paths. Details in `docs/API.md`, section Connection log. What has been tested: drops and silent networks through a local TCP proxy. What has not: a real network, and this log exists to observe it.

## Hub memory limits (measured 2026-11-03)

Per console the hub holds at most about 6 MB: a 1 MiB scrollback ring and a screen model of about 5 MB (the screen model keeps only the visible screen, its history is in the ring). Browser connections that come and go, tool calls to servers, and SSH connections that drop and then recover add no goroutines or heap (`TestNoLeakFromClientsAndServerDrops`, `TestScreenMemoryIsBounded`). A server `bash` command that goes past its time limit leaves no process behind (`TestRemoteBashLeavesNoProcess`). Other histories are capped: jobs 100, orders 200, attention 200, snapshots 24 per board.
