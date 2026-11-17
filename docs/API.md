# NextBoard Hub API — contract

Base URL: the hub serves plain HTTP on `http://127.0.0.1:12312`; clients use `https://<domain>` through nginx (SPEC §5). All JSON uses camelCase. Times are in RFC 3339 (`2026-10-27T22:46:00Z`).

## General conventions

- **Error:** a non-2xx HTTP status with the body `{"error": "message"}`.
- **CSRF:** every `POST`/`PUT`/`PATCH`/`DELETE` request **must** carry the `X-NextBoard: 1` header (app clients always send it too). Without the header → `403`.
- **Auth:** one of
  - the browser session cookie (`HttpOnly; SameSite=Strict; Path=/`, 30 days, extended on use): `nb_session` + `Secure` when the client comes over HTTPS, `nb_session_http` over HTTP. The names differ because browsers refuse to let an `http://` page overwrite a `Secure` cookie of the same name on that host. A named instance ([Instances](#instances)) adds its name: `nb_session_<name>`, `nb_session_http_<name>`, because cookies ignore the port and two hubs on one host would otherwise overwrite each other's login; the `default` instance keeps the names without a suffix; or
  - `Authorization: Bearer <accessToken>` (a paired device), also for WebSocket. A token in the URL is refused, because reverse proxies record query strings.
- Without auth → `401 {"error":"unauthorized"}`.
- **WebSocket Origin:** if a WS request uses a cookie and carries an `Origin` header, the Origin host must match `Host`. A WS that uses a bearer token does not have its Origin checked.
- **Body limits:** board ops 2 MB, asset upload / file paste 20 MB, file write 20 MB of content (body up to 28 MB, because of base64), everything else 64 KB → `413`.

## Health, hub, setup

| Method | Path | Auth | Request | Response |
|---|---|---|---|---|
| GET | `/api/health` | — | — | `{"ok":true,"name":"devbox","version":"0.9.0","commit":"3f2a1c9","builtAt":"2026-11-10T09:00:00Z","channel":"stable"}` (the build fields as in `Hub`) |
| GET | `/api/setup/status` | — | — | `{"needsSetup":true,"hubName":"","instance":"work","defaultInstance":false,"hubId":"k7qxm2pa9d","suggestedHubName":"devbox · work"}`. `suggestedHubName` only while it needs setup: the hub name New instance… was given, else `<hostname>` (default) or `<hostname> · <instance>`. Never a path: the folders wait for a login |
| POST | `/api/setup` | — | `{"code":"K7QX-M2PA","password":"...","hubName":"devbox"}` | `200 {"ok":true}` + sets a cookie (logged in right away). `400` password < 8 characters / empty hubName. `403` wrong/expired/used code. `409` already set up. |
| GET | `/api/hub` | yes | — | `Hub` |
| GET | `/manifest.json` | — | — | The web manifest named after this hub: `name` "NextBoard · <hub name>", `short_name` the hub name (a named instance before setup: its instance name), so two installed instances look different |

```jsonc
// Hub
{
  "name": "devbox",
  "version": "0.9.0",          // the release, or what the build was given ("0.1.0-dev", a git describe)
  "commit": "3f2a1c9",         // short hash it was built from, "-dirty" with uncommitted changes, "unknown" when not recorded
  "builtAt": "2026-11-10T09:00:00Z", // build time, RFC 3339 UTC; "" when not known
  "channel": "stable",         // "stable" (v0.9.0), "beta" (v0.9.0-beta.2, also alpha.N/rc.N), "dev" (anything else)
  "repo": "azamrizki/nextboard", // the GitHub repository its releases and commits come from
  "urls": ["https://board.example.com"],   // over HTTPS: that origin + the HTTPS extraUrls; over HTTP: the hub's http:// addresses
  "listen": "0.0.0.0:12312",
  // which instance this is (Instances below)
  "instance": { "name": "work", "default": false },
  "hubId": "k7qxm2pa9d",       // 10 random characters, made once; never secret. Names tmux sessions and temp folders on servers
  "statusListen": "",          // the public status listener, "" when off
  "replacedSinceStart": false, // the binary changed on disk after this hub started (an update): restart it to run the new one
  "service": { "kind": "systemd", "name": "nextboard-work.service", "managed": true,
               "unitPath": "/home/alice/.config/systemd/user/nextboard-work.service" },
                               // kind "systemd" | "launchd" | "none" (no service serves this folder: started by hand, by `make dev`);
                               // managed: this process is the one that service runs; unitPath for the admin only
  // the admin only; a paired device sees none of these
  "dataDir": "/home/alice/.nextboard-work",
  "configFile": "/home/alice/.nextboard-work/config.json",
  "binary": { "path": "/home/alice/.local/bin/nextboard", "replacedSinceStart": false },
  "siblings": [                // the other instances on This PC
    { "name": "default", "hubName": "devbox", "listen": "127.0.0.1:12312", "port": 12312, "urls": ["http://127.0.0.1:12312"],
      "running": true, "state": "running", "service": "nextboard.service", "problems": [] }
  ]
}
```

**Where the build fields come from:** a release sets `version`, `commit` and `builtAt` at build time (`-ldflags` in `.goreleaser.yaml`), as `make build` does from git. A binary built without them takes the commit from Go's build info (`vcs.revision`, `-dirty` from `vcs.modified`), else `"unknown"`, and the build time from the binary's own file time. The dev hub (`make dev`) records the commit but no build time, so its binary changes only when the code does. `nextboard version` prints the same: `nextboard 0.9.0 (stable, commit 3f2a1c9, built 2026-11-10T09:00:00Z)`.

### Hub update

The web's way to run `nextboard update` (README, Update): whether a newer release is out, and installing it on a hub that runs as the service `nextboard install` set up.

| Method | Path | Auth | Request | Response |
|---|---|---|---|---|
| GET | `/api/hub/update` | yes | `?fresh=1` (optional) | `HubUpdate`. Asks GitHub at most once an hour (after a failure, again after 10 minutes); a dev build asks nothing. `fresh=1` is About this hub's Check for updates: GitHub is asked now, unless the last check is under 30 seconds old (then that one stands), so pressing it again and again stays within GitHub's limit. |
| POST | `/api/hub/update` | admin | `{"version":"v0.9.1"}` (optional: the release the owner confirmed) | `202 Job` (kind `hubUpdate`, purpose = the tag). `409` `dev_build` · `not_service` (not started by its service, so it cannot restart itself: use `nextboard update` where it runs) · `not_writable` (the binary's folder) · `update_running` (`jobId`) · `restart_pending` (swapped in, not restarted yet) · `up_to_date` · `version_changed` (the newest release is no longer the one asked). `502 check_failed` (GitHub could not be asked). `403` for a device. |
| POST | `/api/hub/update/remind` | admin | `{"action":"skip","version":"v0.9.1"}` or `{"action":"later"}` | `{"skipped"?,"remindAfter"?,"remind":false}`: the owner's answer to the update popup. `skip` never asks for that release again (a newer one asks); `later` keeps it away for 24 hours. Kept by the hub (`<data>/update-reminder.json`), so every browser and device follows it. `400 bad_action`, `400 bad_version` (not a release tag). |

```jsonc
// HubUpdate
{
  "status": "available",     // "current" | "available" | "dev" (no check) | "unknown" (could not ask: offline, rate limited)
  "current": "0.9.0",
  "channel": "stable",
  "latest": "v0.9.1",        // the newest release of the channel
  "url": "https://github.com/azamrizki/nextboard/releases/tag/v0.9.1",
  "command": "nextboard update",   // the same in a terminal; "nextboard update --version vX" for a beta
  "error": "",               // why status is "unknown"
  "checkedAt": "2026-11-15T09:00:00Z",
  "blocked": "",             // why this hub cannot install it from the web (+ "blockedCode", one of the 409 codes above)
  "jobId": "",               // an update job running
  "installed": "",           // a tag swapped in that the hub does not run yet (its restart is pending or failed)
  "restartError": "",        // why that restart failed
  "restarts": [              // when an update can start: every instance whose service runs this binary, this one too
    { "name": "default", "listen": "127.0.0.1:12312", "service": "nextboard.service" },
    { "name": "work", "listen": "127.0.0.1:12412", "service": "nextboard-work.service", "self": true }
  ],
  "skipped": "v0.9.0",       // the release the owner skipped for good (the popup's Skip this version)
  "remindAfter": "2026-11-16T09:00:00Z",   // Skip for now: no popup before then
  "remind": true             // ask now: a newer release is out, not skipped, not put off, nothing installing or swapped in
}
```

- **Update popup** (DESIGN.md §3.4): when the web app opens and `remind` is true, it offers the release to the owner (admin only; a device or a dev build never sees it). Update now starts the update (`POST /api/hub/update`) and shows its progress in the popup; Skip this version and Skip for now (and closing it) answer through `POST /api/hub/update/remind`. An open app asks again every hour and when its tab comes back, so a day that ran out is seen without a reload; one opening never asks twice about the same release.

- **Channel:** a stable build follows the latest stable release; a beta follows the newest release of any kind, beta or stable (`v0.9.0-beta.3`, then `v0.9.0`). Never an older one, never a draft. A dev build follows none: update it with `git pull` and a rebuild.
- **The job** (events `{"type":"job"}`, API-studio.md §1.2) has the phases `download` (with `pct`), `verify` (SHA-256 against the release's `checksums.txt`), `swap`, `restarting`, then `done` with `result` `{"from","to","binary","old","restart":true,"restarts":["default","work"]}`. Assets must come from the repository's own releases (`https://github.com/<repo>/releases/download/…`); nothing from the download runs. The new binary is written next to the old one, which stays as `<binary>.old`, then renamed over it in one step. To go back: `nextboard update --version <old tag> --force`, or move `<binary>.old` back and restart the service.
- **Restart:** about 1.5 s after the job ends, the hub restarts every other instance whose service runs the same binary, then asks its own service manager to restart itself (`systemctl --user --no-block restart nextboard`, `nextboard-<name>` for a named instance, `launchctl kickstart -k`): one binary serves them all, so none keeps running old code. The confirm dialog names them (`restarts`), with their addresses, first. `nextboard update --instance <name>` in a terminal restarts only that one and names the others still on the old build. Consoles reconnect and agents come back asleep or resume, as after any hub restart; data and logins stay. A client waits until `/api/health` answers with the new `version`.
- **Tests only:** `NEXTBOARD_TEST_RELEASE_API=<base URL>` points the hub and `nextboard update` at a fake GitHub (the API and every download from that base).

### Instances

Several hubs on one machine (README, Several instances on one machine): each **instance** has a name, its own data folder, ports, service, cookie and admin. `default` is `~/.nextboard` and keeps every name it had; `<name>` (`^[a-z][a-z0-9-]{0,23}$`) is `~/.nextboard-<name>`. `config.json` records `instance` and `hubId`; a folder without them gets them once (`~/.nextboard` and any other path → `default`, `.nextboard-<x>` → `x`). A hub holds `<data>/hub.lock` (`flock`) while it runs, so a second hub on the same folder stops at start.

| Method | Path | Auth | Request | Response |
|---|---|---|---|---|
| GET | `/api/instances` | admin | — | `{"self":"work","items":[Instance…],"suggest":{"host":"127.0.0.1","loopback":true,"hostname":"devbox","port":12512,"previewRange":[12520,12539]}}`; `items` holds this hub too. Without a free block: no `port`, and `noFreeBlock` says why |
| POST | `/api/instances` | admin | `{"name":"personal","hubName"?:"devbox · personal","listenHost"?:"127.0.0.1","port"?:12512,"allowNetwork"?:false,"password":"<this hub's admin password>"}` | `201 Created` (below). `400 bad_instance_name` · `bad_hub_name` · `bad_listen`. `403 origin` · `bad_password` · `admin only`. `409 instance_exists` · `port_in_use` · `no_free_block` · `network_not_allowed` · `not_set_up`. `429 too_many_attempts` |
| GET | `/api/instances/import` | admin of the **new** hub | — | `{"self":"side","sources":[{"name":"personal","hubName":"devbox · personal","setUp":true}]}`: the other instances a copy can come from (this one left out; `setUp:false` has no admin yet) |
| POST | `/api/instances/import` | admin of the **new** hub | `{"from":"personal","password":"<that instance's admin password>","settings":true,"presets":true,"mcp":true,"mcpSecrets":false,"machines":false,"machineCredentials":false,"providers":false,"providerKeys":false,"vault":false}` | `{"from":"personal","copied":{"settings":true,"agents":2,"layouts":1,"mcp":1,"machines":0,"providers":0,"vault":false,"vaultItems":0,"secretsTaken":false},"notes":["…"]}`. `400 bad_instance_name` · `nothing_to_copy`. `403 origin` · `bad_password`. `404 unknown_instance`. `409 not_set_up` · `ambiguous_instance` · `no_master_key`. `429 too_many_attempts` |

```jsonc
// Instance (an item of GET /api/instances; read from disk, no registry file)
{
  "name": "personal", "hubId": "p3zt8wq1ma",
  "dataDir": "/home/alice/.nextboard-personal", "configFile": "/home/alice/.nextboard-personal/config.json",
  "configured": true,                   // the folder holds a config.json (a unit can name one that does not)
  "listen": "127.0.0.1:12512", "statusListen": "", "previewRange": [12520, 12539],
  "service": { "kind": "systemd", "name": "nextboard-personal.service", "unitPath": "…/nextboard-personal.service", "bin": "/home/alice/.local/bin/nextboard" },
  "running": true, "pid": 4242,         // its hub.lock is held, by that process
  "problems": []                        // e.g. two folders with the same name, overlapping ports
}
// Created (201 of POST /api/instances)
{
  "instance": "personal", "hubId": "p3zt8wq1ma", "dataDir": "…", "configFile": "…",
  "listen": "127.0.0.1:12512", "previewRange": [12520, 12539], "service": { … },
  "started": true, "healthy": true,     // started:false: no user service manager here; run `command`
  "command": "",                        // "nextboard serve --instance personal" when not started
  "urls": ["http://127.0.0.1:12512"],
  "setupUrls": ["http://127.0.0.1:12512/setup?code=K7QX-M2PA"],
  "expiresAt": "…",                     // the setup code's, 30 minutes
  "warnings": [],                       // e.g. loginctl enable-linger failed
  "startError": ""                      // its service did not start: what was made stays, `nextboard logs --instance` says why
}
```

- **Where instances come from:** units `~/.config/systemd/user/nextboard.service` and `nextboard-*.service` (macOS `~/Library/LaunchAgents/id.nextboard.plist`, `id.nextboard.*.plist`), with the data folder from their `ExecStart`, and folders `~/.nextboard`, `~/.nextboard-*` that hold a `config.json`, merged by data folder.
- **Ports:** a block of 100 per instance: the hub on `12312 + 100·k`, previews `12320 + 100·k` to `12339 + 100·k`, the smallest k ≥ 1 (at most 50) whose ports can be bound now and that no other instance claims. Nobody else's hub, status or preview port is accepted, and a preview never opens a sibling's hub port.
- **Who and how:** the admin only (a paired device gets `403 admin only`), with this hub's admin password in the body (rate limited like login, under its own key), `X-NextBoard: 1`, and an `Origin` whose host is the request's `Host`. Sibling instances and preview ports count as the same site, so `SameSite=Strict` alone would not stop them.
- **What the web can create:** an instance on This PC, always in `~/.nextboard-<name>`; the web never names a folder. It listens on `listenHost` (by default the host this hub listens on, so it is reached the same way; `127.0.0.1` for this machine only). A non-loopback host needs `allowNetwork:true`, which the dialog asks for with the plain-HTTP warning. The endpoint never sets `statusListen`, `extraUrls` or a debug listener.
- **Creating** (`internal/instance.Create`, the same as `nextboard install --instance`): the folder (0700), `config.json` with `instance`, a new `hubId`, `listen` and the hub name given, `settings.json` with the preview range, `presets.json`, a setup code (single use, 30 minutes) in the new hub's `auth.json`, then, when the user's service manager answers, the unit or LaunchAgent, `daemon-reload` + `enable` + `restart` (and `loginctl enable-linger`) and a wait of up to 15 s for health. Without one (a hub run by hand or under air) nothing is started: `started:false` and the `command` to run; the setup code works once that hub runs. A failure before the service starts leaves nothing behind. A port given that is not the suggested one keeps that port and takes the preview range of the next free block.
- **The setup link** is only in this answer: never in events, logs or the connection log. The hub logs `instance <name> created on <listen> by instance <self>`, without the code. The dialog's **Start from this instance's settings** only adds `&from=<this instance>` to the link, which preselects the copy step of the new hub's wizard.
- **Import** runs in the new hub and reads the other instance's files from disk (same OS user), picked by name from the sources; nothing in its folder is written or locked. The password is checked against that instance's `auth.json` (rate limited like login), so a new instance's setup link alone cannot pull a sibling's settings. `presets` brings custom agents (`presets.json`) and saved layouts. A secret comes only with its own box ticked, and only with what it belongs to (`mcpSecrets` with `mcp`, `machineCredentials` with `machines`, `providerKeys` with `providers`): opened with the sibling's `secret.key` and sealed again with the new hub's. The vault is copied as ciphertext, so the same master password opens it. An MCP server whose values stay behind comes turned off, SSH machines come disconnected, providers without keys ask for one; `notes` says so. Never copied: `auth.json`, `secret.key`, `hub.lock`, `devices.json`, sessions, boards, projects, memory, scrollback, logs, login profile folders. Settings are remapped: `agents.profiles` and `previews.portRange` stay the new hub's own; `runner.provider`, `lead.runner` and `lead.providers` point at the imported providers or are cleared (with a note).
- **Not on the status listener:** `/api/hub`, `/api/setup/status` and `/api/instances` are absent from `--status-listen`, which knows only status pages; nothing about instances appears there.
- **Shared with every instance of the OS user:** `$HOME`, `~/.ssh`, git and `gh` credentials, the agent CLIs, and `~/.claude`/`~/.codex` for an instance whose agents use your own CLI logins. An agent of one instance can read another's data folder: instances keep data and accounts apart, not secrets. A separate OS user per instance is the way to keep secrets apart.

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
  "sessionCount": 2,
  "archivedAt": "…"              // only while archived
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
| GET | `/api/machines` | — | `Machine[]` of the active machines (local always first). `?archived=1`: the archived ones, most recently archived first |
| GET | `/api/machines/sshconfig` | — | `SSHConfigHost[]` (no wildcards) |
| POST | `/api/machines` | `AddMachine` | `201 Machine` (status can be `hostkey` → ask for confirmation, `connected`, or `error`) |
| POST | `/api/machines/{id}/hostkey` | `{"accept":true,"fingerprint":"SHA256:…"}` | `Machine` (connected + probed). `409` the fingerprint does not match the pending one. |
| POST | `/api/machines/{id}/credentials` | `{"kind":"password"\|"passphrase","value":"…","remember":true}` | `Machine`. See "Password and passphrase". `400` empty, `409 auth_not_needed` when the machine is not waiting for it |
| POST | `/api/machines/{id}/connect` | — | `Machine` (tries to reconnect now) |
| POST | `/api/machines/{id}/probe` | — | `Machine` |
| PATCH | `/api/machines/{id}` | `{"name"?: "...", "persistMode"?: "auto"\|"tmux"\|"direct"}` | `Machine` |
| POST | `/api/machines/{id}/archive` | — | `Machine` with `archivedAt`. See "Archive and delete for good" |
| POST | `/api/machines/{id}/restore` | — | `Machine` without `archivedAt`; it connects again. `409 not_archived` |
| DELETE | `/api/machines/{id}` | — | Without a query: **archives**, the same answer as `POST …/archive` (the safe default for older clients). With `?permanent=1`: deletes the machine, its sealed credentials (`creds/<id>.enc`) and its host keys in the hub's own `known_hosts` when no other machine uses that address (`~/.ssh` is never changed): `{"ok":true}` and `machineRemoved` |
| POST | `/api/machines/bulk` | `{"ids": ["m_…", …], "action": "archive"\|"restore"\|"delete"}` (1–200 ids; `delete` = for good) | `{"results": [{"id","ok":true} \| {"id","ok":false,"error":"…","code"?}]}`; each id on its own (`code`: `in_use`, `local`, `machine_not_found`, `not_archived`), then one `{"type":"machines"}` event: clients refetch the list |
| GET | `/api/machines/{id}/presets` | — | `Preset[]` |
| GET | `/api/machines/{id}/ls?path=` | empty `path` / `~` = home | `DirListing` |
| GET | `/api/machines/{id}/orphans` | — | `{"names":["nb-k7qxm2pa9d-s_abc123"],"legacy":["nb-s_def456"]}`: `names` are this hub's own tmux sessions (`nb-<hubId>-<sid>`) that no console has; `legacy` carry no hub mark (from before hubIds), so they may be another hub's live consoles, and the dialog leaves them unselected. A sibling hub's names are in neither |
| POST | `/api/machines/{id}/cleanup` | `{"names":["nb-k7qxm2pa9d-s_abc123"]}` | `{"killed":["nb-k7qxm2pa9d-s_abc123"]}` |

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

**Archive and delete for good.** A server no longer in use can be archived (restorable at any time, nothing of it is deleted) or deleted for good. Nothing on the server itself is touched.

- **This PC** (`local`) can be neither: `400`.
- **In use:** a machine that a part of any project (an archived project too) or any console uses can be neither archived nor deleted: `409 {"error":"in_use","code":"in_use","projects":[{"id","name"}…],"sessions":n}`. Remove those parts and close those consoles first.
- **While archived** the machine is not connected (its connection ends when it is archived), not reconnected or probed (`POST …/connect` and `…/probe` → `409 archived`), and in no picker: it is not in `GET /api/machines` nor in the snapshot of `/api/events`. The event `{"type":"machine","machine":…}` carries `archivedAt`: clients drop it from the active list. `machineRemoved` is only for a delete for good.

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
{ "type": "machine",  "machine": Machine }   // also when archived (with archivedAt) or restored
{ "type": "machineRemoved", "id": "m_…" }      // deleted for good
{ "type": "machines" }                         // after a bulk action: refetch the list
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

Custom presets are added through `<data>/presets.json` (`~/.nextboard/presets.json` for the default instance) (an array of `Preset` without `available`; the same id overrides the default).
