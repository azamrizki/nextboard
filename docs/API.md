# NextBoard Hub API — contract

Base URL: the hub serves plain HTTP on `http://127.0.0.1:12312`; clients use `https://<domain>` through nginx (SPEC §5). All JSON uses camelCase. Times are in RFC 3339 (`2026-10-27T22:46:00Z`).

## General conventions

- **Error:** a non-2xx HTTP status with the body `{"error": "message"}`.
- **CSRF:** every `POST`/`PUT`/`PATCH`/`DELETE` request **must** carry the `X-NextBoard: 1` header (app clients always send it too). Without the header → `403`.
- **Auth:** one of
  - the browser session cookie (`HttpOnly; SameSite=Strict; Path=/`, 30 days, extended on use): `nb_session` + `Secure` when the client comes over HTTPS, `nb_session_http` over HTTP. The names differ because browsers refuse to let an `http://` page overwrite a `Secure` cookie of the same name on that host; or
  - `Authorization: Bearer <accessToken>` (a paired device), also for WebSocket. A token in the URL is refused, because reverse proxies record query strings.
- Without auth → `401 {"error":"unauthorized"}`.
- **WebSocket Origin:** if a WS request uses a cookie and carries an `Origin` header, the Origin host must match `Host`. A WS that uses a bearer token does not have its Origin checked.
- **Body limits:** board ops 2 MB, asset upload / file paste 20 MB, file write 20 MB of content (body up to 28 MB, because of base64), everything else 64 KB → `413`.

## Health, hub, setup

| Method | Path | Auth | Request | Response |
|---|---|---|---|---|
| GET | `/api/health` | — | — | `{"ok":true,"name":"devbox","version":"0.1.0"}` |
| GET | `/api/setup/status` | — | — | `{"needsSetup":true,"hubName":""}` |
| POST | `/api/setup` | — | `{"code":"K7QX-M2PA","password":"...","hubName":"devbox"}` | `200 {"ok":true}` + sets a cookie (logged in right away). `400` password < 8 characters / empty hubName. `403` wrong/expired/used code. `409` already set up. |
| GET | `/api/hub` | yes | — | `Hub` |

```jsonc
// Hub
{
  "name": "devbox",
  "version": "0.1.0",
  "urls": ["https://board.example.com"],   // over HTTPS: that origin + the HTTPS extraUrls; over HTTP: the hub's http:// addresses
  "listen": "0.0.0.0:12312"
}
```

## Auth

| Method | Path | Auth | Request | Response |
|---|---|---|---|---|
| POST | `/api/auth/login` | — | `{"password":"..."}` | `200 {"ok":true}` + cookie. `401` wrong. `429` rate limit (5 failures / IP / 10 minutes). |
| POST | `/api/auth/logout` | yes | — | `200 {"ok":true}`, the cookie is deleted |
| GET | `/api/auth/me` | yes | — | `{"kind":"admin"}` or `{"kind":"device","deviceId":"d_…","deviceName":"Pixel 8"}` |
| POST | `/api/auth/challenge` | — | `{"deviceId":"d_…"}` | `{"nonce":"<base64>"}` (valid for 60 seconds, single use). `404` unknown/revoked device. |
| POST | `/api/auth/device` | — | `{"deviceId":"d_…","nonce":"<exact nonce>","sig":"<base64>"}` | `{"accessToken":"<opaque>","expiresIn":3600,"expiresAt":"…"}`. `401` wrong signature. |

**Device signature:** `sig = base64std( Ed25519.Sign(privateKey, utf8Bytes(nonce)) )` — what is signed is the `nonce` string exactly as received (the UTF-8 bytes of its base64 text).

## Pairing & devices

| Method | Path | Auth | Request | Response |
|---|---|---|---|---|
| POST | `/api/pair/start` | admin | — | `PairStart` |
| POST | `/api/pair/complete` | — (the code must be valid) | `{"code":"K7QX-M2PA","deviceName":"Pixel 8","platform":"android","publicKey":"<base64>"}` | `{"deviceId":"d_…","hubName":"devbox"}`. `403` wrong/expired code. |
| GET | `/api/devices` | yes | — | `Device[]` |
| PATCH | `/api/devices/{id}` | admin | `{"name":"..."}` | `Device` |
| DELETE | `/api/devices/{id}` | admin | — | `{"ok":true}`; all of that device's tokens + WS are cut at once |

- `publicKey`: standard base64 (with padding) of the 32-byte raw Ed25519 public key.
- `platform`: `android` | `ios` | `desktop` | `web` | anything else.
- Pairing code: 8 characters from the alphabet `ABCDEFGHJKMNPQRSTUVWXYZ23456789` in the format `XXXX-XXXX`, valid for 5 minutes, single use. The server also accepts the code without the `-` and in lowercase.

```jsonc
// PairStart
{
  "code": "K7QX-M2PA",
  "expiresAt": "…",
  "uri": "nextboard://pair?h=devbox&u=https%3A%2F%2Fboard.example.com&c=K7QX-M2PA",
  "urls": ["https://board.example.com"],
  "hubName": "devbox"
}
// Device
{
  "id": "d_x1y2z3", "name": "Pixel 8", "platform": "android",
  "createdAt": "…", "lastSeen": "…", "online": true
}
```

**Pairing URI:** `nextboard://pair?h=<hub>&u=<url1>,<url2>&c=<code>` — each URL in `u` is URL-encoded on its own and then joined with commas. Clients: split `u` on `,`, decode each part.

## Machines

```jsonc
// Machine
{
  "id": "local",                 // "local" for This PC, otherwise "m_…"
  "name": "This PC",
  "kind": "local",               // "local" | "ssh"
  "host": "", "port": 22, "user": "", "sshConfigHost": "",
  "authMethod": "",              // "sshconfig" | "agent" | "key" | "password" ("" for local)
  "persistMode": "auto",         // "auto" | "tmux" | "direct"
  "effectivePersist": "tmux",    // the result of resolving auto → "tmux" | "direct" ("" for local)
  "status": "connected",         // "local" | "connecting" | "connected" | "disconnected" | "reconnecting" | "hostkey" | "error"
  "error": "",
  "pendingHostKey": null,        // or { "fingerprint":"SHA256:…", "keyType":"ssh-ed25519", "host":"devbox:22" } when status = "hostkey"
  "hostKeyMismatch": false,      // true = the host key CHANGED, the connection is refused (show a red warning)
  "probe": {
    "os": "Linux", "arch": "x86_64", "shell": "/bin/bash", "home": "/home/alice",
    "tmux": true,
    "commands": { "claude": "/home/alice/.local/bin/claude", "codex": "" },
    "probedAt": "…"
  },
  "sessionCount": 2
}
// SSHConfigHost
{ "host": "devbox", "hostName": "10.0.0.5", "user": "alice", "port": 22, "identityFile": "~/.ssh/id_ed25519", "proxyJump": "" }
// Preset (with availability per machine)
{ "id": "claude", "name": "Claude Code", "command": "claude", "args": [], "resumeArgs": ["--continue"], "color": "#d97757", "available": true }
// DirListing
{ "path": "/home/alice/apps", "parent": "/home/alice", "entries": [ { "name": "nextboard", "dir": true }, { "name": "notes.md", "dir": false } ] }
```

| Method | Path | Request | Response |
|---|---|---|---|
| GET | `/api/machines` | — | `Machine[]` (local always first) |
| GET | `/api/machines/sshconfig` | — | `SSHConfigHost[]` (no wildcards) |
| POST | `/api/machines` | `AddMachine` | `201 Machine` (status can be `hostkey` → ask for confirmation, `connected`, or `error`) |
| POST | `/api/machines/{id}/hostkey` | `{"accept":true,"fingerprint":"SHA256:…"}` | `Machine` (connected + probed). `409` the fingerprint does not match the pending one. |
| POST | `/api/machines/{id}/credentials` | `{"kind":"password"\|"passphrase","value":"…","remember":true}` | `Machine`. See "Password and passphrase". `400` empty, `409 auth_not_needed` when the machine is not waiting for it |
| POST | `/api/machines/{id}/connect` | — | `Machine` (tries to reconnect now) |
| POST | `/api/machines/{id}/probe` | — | `Machine` |
| PATCH | `/api/machines/{id}` | `{"name"?: "...", "persistMode"?: "auto"\|"tmux"\|"direct"}` | `Machine` |
| DELETE | `/api/machines/{id}` | — | `{"ok":true}`; `409` if there are still sessions; `400` for `local` |
| GET | `/api/machines/{id}/presets` | — | `Preset[]` |
| GET | `/api/machines/{id}/ls?path=` | empty `path` / `~` = home | `DirListing` |
| GET | `/api/machines/{id}/orphans` | — | `{"names":["nb-abc123"]}` |
| POST | `/api/machines/{id}/cleanup` | `{"names":["nb-abc123"]}` | `{"killed":["nb-abc123"]}` |

```jsonc
// AddMachine
{
  "name": "devbox",                // optional, default = sshConfigHost or host
  "sshConfigHost": "devbox",       // fill this OR a manual host
  "host": "10.0.0.5", "port": 22, "user": "alice",
  "authMethod": "sshconfig",      // "sshconfig" | "agent" | "key" | "password"
  "privateKey": "-----BEGIN OPENSSH PRIVATE KEY-----…",   // only for "key"
  "passphrase": "",               // optional for "key"
  "password": "",                 // only for "password"
  "persistMode": "auto"
}
```

`authMethod: "sshconfig"` = try in order: IdentityFile from the config / the default `~/.ssh/id_ed25519`, `id_ecdsa`, `id_rsa`, then ssh-agent. Uploaded credentials are never returned to the client.

## Password and passphrase

When authentication fails and there is something a person can type, the machine stops with `status:"auth"` and `needsAuth`, and the client brings up a dialog. The hub does not retry while it waits: a wrong password tried again and again ends in a ban from the server (fail2ban).

```jsonc
"needsAuth": {
  "kind": "passphrase",           // "password" | "passphrase"
  "keyPath": "~/.ssh/id_ed25519", // the key meant, as written in the config; empty = the uploaded key
  "host": "alice@10.0.0.5",         // who is asking; can be a jump host
  "prompt": "",                   // the server's question through keyboard-interactive
  "wrong": false,                 // the last answer was refused
  "canPassword": true             // a passphrase was asked for, but the server also accepts a password
}
```

- **How it knows:** the authentication chain records the methods the server offers, because callbacks run only for methods the server offers. The order of questions: a password that was just refused, then a locked key (if the server accepts keys), then a password. If the server accepts only keys and there is no key, there is nothing to type: `status:"error"` and the hub keeps trying as usual.
- **A locked key** is asked about only when all other ways fail. A key in ssh-agent or another key the server accepts does not bring up the dialog.
- **The passphrase is tried on the key in the hub**, so a typo does not reach the server. A wrong password uses up one attempt.
- **Storage:** the answer is in memory while it is tried. `remember:true` stores it encrypted in `creds/<id>.enc` **after the connection succeeds**, so a wrong answer is never stored. `remember:false` stays in memory for reconnects until the hub restarts, then it is asked again. The answer never comes back in a response or log.
- A password is only for that machine, not for the jump host; a passphrase also applies to a key on the jump host. 2FA is not supported: a one-time code cannot be used to reconnect, and the dialog only shows the server's question through `prompt`.

## Connection log

A permanent record of connection behavior to servers, to study when things happen on a real network. Always on, nothing to turn on.

- **File:** `<data>/logs/connections.jsonl`, one JSON line per event, mode 0600. Rotated once to `connections.1.jsonl` every 4 MiB, so at most about 8 MiB. The old log is lost on rotation.
- **What is recorded:** what happens to the connection: time, server name, the reason for a drop, time connected, time disconnected, latency, the names of tools that failed or were slow, and error messages from the transport (cut at 300 characters).
- **What is not recorded:** commands, output, file contents and paths. A command that fails on its own is not a connection problem and does not go into the log.

`GET /api/logs/connections?machineId=&since=&limit=` (admin) answers `{ "items": [Event], "summary": Summary }`, newest first. `since` is in RFC 3339 format. `limit` default 200, max 2000. The `machineId` filter still includes hub events, because a hub going down also cuts connections.

| `kind` | Meaning | Key fields |
|---|---|---|
| `hub_start`, `hub_stop` | The hub started, stopped normally | `version`, `pid` |
| `hub_unclean_exit` | The previous run ended without stopping (crash, `kill -9`, power) | `lastSeen`, up to 1 minute off |
| `connected` | Connected | `attempt`, `durationMs`, `downtimeMs` |
| `connect_failed` | A failed attempt: the first 5, then every 20th, and always when the host key has a problem | `attempt`, `reason`, `error` |
| `disconnected` | Dropped | `reason`, `uptimeMs`, `error`, `calls`, `failures` |
| `keepalive_miss` | A keepalive got no answer | `misses` |
| `link_stats` | Every 30 minutes while connected, as a baseline from when the connection is healthy | `rttAvgMs`, `rttMaxMs`, `calls`, `failures`, `uptimeMs` |
| `tool_failed` | An agent tool failed because of the connection | `tool`, `reason`, `durationMs` |
| `tool_slow` | A file tool took 5 seconds or more (`bash` excluded) | `tool`, `durationMs` |
| `channel_lost` | A console channel on the server died | `sessionId` |

`reason` on `disconnected`: `keepalive_timeout` (a silent network, with no error at all), `connection_lost` (the connection was closed or reset), `closed` (deliberate, not counted as a drop). On `tool_failed`: `not_connected`, `disconnected`, `no_sftp`, `timeout`.

`calls` and `failures` are counted since the last report, so `link_stats` resets them.

Ways to read it, easiest first:

- **Web:** Machines › server card › **Connection log**.
- **CLI:** `nextboard connlog [--machine <name|id>] [-n 50] [--json] [--data-dir <dir>]`. It reads the file directly, so it still works when the hub is down.
- **File:** `jq` on `<data>/logs/connections.jsonl`.

## Sessions

```jsonc
// Session (Info)
{
  "id": "s_k3j2h1", "name": "Claude Code", "preset": "claude", "machineId": "local",
  "command": "claude", "args": [], "resumeArgs": ["--continue"], "env": [],
  "cwd": "/home/alice/apps/nextboard", "cols": 120, "rows": 32, "tmuxName": "",
  "createdAt": "…",
  "status": "running",          // "starting" | "running" | "exited" | "reconnecting" | "error"
  "exitCode": 0,
  "busy": false,                // output within the last 2 seconds
  "startedAt": "…", "lastOutputAt": "…",
  "error": "",
  "color": "#d97757",           // the preset's accent
  "machineName": "This PC"
}
```

| Method | Path | Request | Response |
|---|---|---|---|
| GET | `/api/presets` | — | `Preset[]` (without `available`) |
| GET | `/api/sessions` | — | `Session[]` |
| POST | `/api/sessions` | `{"machineId":"local","preset":"claude","name"?:"…","cwd"?:"…","cols"?:120,"rows"?:32}` | `201 Session` (command not found → still created, status `exited`/`error`, a red message in the terminal) |
| GET | `/api/sessions/{id}` | — | `Session` |
| PATCH | `/api/sessions/{id}` | `{"name":"…"}` | `Session` |
| DELETE | `/api/sessions/{id}` | — | `{"ok":true}` (kill + delete the node) |
| POST | `/api/sessions/{id}/restart` | `{"resume":true}` | `Session` |
| POST | `/api/sessions/{id}/kill` | — | `Session` (the process is stopped, the node stays, status `exited`) |
| POST | `/api/sessions/{id}/input` | `{"data":"ls -la\r"}` | `{"ok":true}` (a quick action for mobile) |

Desktop apps on the board: see [API-gui.md](API-gui.md). Uptime monitoring: see [API-uptime.md](API-uptime.md). Credentials: see [API-vault.md](API-vault.md).

Boards, projects, workspaces, machine files and assets: see [API-board.md](API-board.md) (board v2 per workspace; the old `GET/PUT /api/board` has been removed). `Session` also has `projectId` and `workspaceId`.

## WebSocket

### `/api/events`

Server → client, JSON text frames:

```jsonc
{ "type": "snapshot", "sessions": [Session…], "machines": [Machine…], "devices": [Device…] } // sent once on connect
{ "type": "session",  "session": Session }   // created / changed (status, busy, name)
{ "type": "removed",  "id": "s_…" }           // session deleted
{ "type": "machine",  "machine": Machine }
{ "type": "machineRemoved", "id": "m_…" }
{ "type": "device",   "device": Device }
{ "type": "deviceRemoved",  "id": "d_…" }
// + project/board v2 events: see API-board.md §3 (project, projectRemoved, boardOps)
```

The client does not need to send anything. The server pings every 30 seconds.

### `/api/sessions/{id}/ws`

- **Server → client**
  - **Binary** frames: raw terminal output. The **first** binary frame is always the scrollback snapshot (may be empty). The client calls `term.reset()` before writing the snapshot.
  - JSON **text** frames: `{"t":"status","status":"exited","exitCode":0,"busy":false}` when the status changes.
- **Client → server** (JSON text frames)
  - `{"t":"i","d":"<input>"}` keyboard input. While the process is still `starting`, keystrokes (without terminal reports, ≤ 4 KiB) are kept and handed over as soon as the process exists; a failed start or reattach → dropped
  - `{"t":"r","c":<cols>,"r":<rows>}` resize (the terminal size follows the last resize from any client)
- Ping every 30 seconds. A slow subscriber (buffer of 512 full, or more than 4 MiB of output waiting) is cut off; the client reconnects and repaints from the snapshot.
- A revoked device → all its WebSockets are closed at once (close code 4401). The token is checked only when the connection opens; a WS that is already connected is not cut when the token expires.
- A browser login that ends → all WebSockets of that login are closed (close code 4401): immediately on logout, within about 2 seconds at most on `nextboard reset-password` from another process or when the cookie expires. Other logins are not disturbed.
- A subscriber that is too slow / a deleted session → close code 1013 (`resubscribe`); the client reconnects.

## Default presets

| id | name | command | args | resumeArgs | color |
|---|---|---|---|---|---|
| shell | Shell | `$SHELL` | `-l` | — | `#8b949e` |
| claude | Claude Code | `claude` | — | `--continue` | `#d97757` |
| codex | Codex | `codex` | — | `resume --last` | `#10a37f` |
| gemini | Gemini CLI | `gemini` | — | — | `#4285f4` |

Custom presets are added through `~/.nextboard/presets.json` (an array of `Preset` without `available`; the same id overrides the default).
