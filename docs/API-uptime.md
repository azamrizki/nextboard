# Uptime monitoring

Reference: [Uptime Kuma](https://github.com/louislam/uptime-kuma). The hub does the checking, as long as the hub is running, whether or not a board is open. The `uptime` card on a board only shows the results. Package: `internal/uptime`, handler `internal/server/h_uptime.go`, client `web/src/uptime/` and `web/src/board/tiles/UptimeTile.tsx`.

## Model

Monitors belong to the **project**, not to a card. Every Uptime card in one project shows the same monitors.

- **Deleting a card** does not stop the monitors (its dialog says so); add a card again to see them.
- **Deleting a monitor** stops its checks and deletes its history.
- **Deleting a project** deletes all its monitors.

| Type | What is checked | Fields |
|---|---|---|
| `http` | Status code, a keyword in the first 1 MB, certificate | `url`, `method` (`GET`/`HEAD`), `accepted` (`"200-299,301"`), `keyword`, `keywordInvert`, `ignoreTls`, `headers` |
| `tcp` | The port can be opened | `host`, `port` |
| `ping` | The machine's `ping -c 1` (raw ICMP needs privileges the hub does not have) | `host` |
| `dns` | There is an answer, and it holds a given text when asked | `host`, `dnsType` (`A AAAA CNAME MX NS TXT`), `dnsServer`, `dnsExpect` |
| `push` | Another party reports in; goes down when the reports stop | `pushToken` (made by the hub) |
| `websocket` | The WebSocket upgrade is granted: `101` with a correct `Sec-WebSocket-Accept` (RFC 6455). No message is sent | `url` (`ws://`/`wss://`), `headers`, `ignoreTls` |
| `docker` | `docker inspect` on the machine: up while the container is `running`, and `healthy` when it has a health check. The container name is an argument, not passed through a shell | `container` (name or id) |
| `steam` | The game server answers `A2S_INFO` over UDP, including the challenge. The message is the name, map, and player count. Always from This PC, because SSH does not carry UDP | `host`, `port` (default 27015) |

**JSON query** (`http`): `jsonPath` takes one value from the JSON answer (`data.items[0].status`, may start with `$.`, keys with spaces or dots are written `["this key"]`), then `jsonOp` (`==`, `!=`, `contains`, `<`, `>`, `<=`, `>=`) compares it with `jsonExpect`. Strings are compared as they are, other values as JSON (`true`, `3`, `null`). A missing value always fails. Needs `GET`. When `keyword` is also set, both must pass.

**Groups and tags:** `group` (≤ 40 characters) gathers monitors under one heading on the card, and `tags` (≤ 10, each ≤ 24, no commas, no duplicates) become filter chips. Neither shows on the public page.

All: `name`, `intervalSec` (20 to 86400, default 60), `retries` (0 to 10, default 1), `retryIntervalSec` (default equal to the interval), `timeoutSec` (1 to 60, default 80 % of the interval, at most 30), `active`, `notify`. At most 100 monitors per project; at most 32 checks run at the same time.

**`partId`**: the project part this monitor watches (API-board §1, ROADMAP #49), a label only: shown as a chip on the Uptime card. Must be a part of that project (`404 part_not_found`). Never reaches the public status page.

**`machineId`** (`http`, `tcp`, `ping`): checked from the server over the existing SSH connection, without installing anything, so it can reach what is only visible from that server (its own localhost, its private network). `http` and `tcp` go through a `direct-tcpip` channel, `ping` through exec. Empty means This PC.

## Status

| Status | Meaning |
|---|---|
| `up` | The last check succeeded |
| `pending` | Failed, still within the `retries` allowance |
| `down` | Failed after the allowance ran out; once `down`, later failures stay `down` |
| `paused` | `active:false` |
| `waiting` | No result yet, or its machine cannot be reached. Not a heartbeat and not counted: a disconnected machine does not mean the target is down |
| `maintenance` | A project maintenance window is in effect for this monitor (see below). `msg` holds the window's title |

**Uptime** = `up / (up + down)`. `pending` is not counted anywhere (a passing blip is the reason retries exist; unlike Uptime Kuma 2, which counts it as down). Time without checks (hub down, monitor paused) is not counted either.

## Maintenance

Maintenance windows belong to the project, stored in the project's `settings.json` (`settings.maintenance`), at most 20. While a window is in effect, the monitors it covers are not checked, there are no heartbeats (uptime does not count that time), and there are no alerts. Pushes that arrive are recorded as signs of life, but are not counted. A monitor looks again at least every 1 minute, so the end of a window is noticed quickly. After it, the monitor is checked as usual, and if it turns out to be down, that is a new alert. The push clock starts again from the end of the window.

| Field | Meaning |
|---|---|
| `id` | Made by the hub |
| `title` | ≤ 80 characters; shown on the card, not on the public page |
| `monitorIds` | The monitors of this project that are covered; empty = all. A deleted monitor leaves the list, and a window that becomes empty is deleted too (it does not turn into covering all) |
| `mode` | `manual` (`active` turns it on and off), `once` (`start` to `end`), `weekly` (`days` 0 Sunday to 6 Saturday, `from` "HH:MM", `minutes` 1 to 10080, `tz` an IANA zone; empty = the hub's zone). A weekly window may cross midnight |

The public page writes **Maintenance**, and the top heading `Some services are under maintenance` when nothing is down, without the window title.

## Alert

On going down, on recovering, and when a certificate has 14, 7, or 1 days left (once per threshold, not repeated by a restart, starting over when the certificate is renewed). Only for monitors with `notify:true`. A blip within the retry allowance gives no alert. A hub restart gives no alert.

- **In the app:** an `attention` event (API-agents §12) with `kind` `uptimeDown`, `uptimeUp`, `certExpiring` and `monitorId`, without `sessionId`. The Show button opens the monitor.
- **Webhook** per project, so alerts arrive even when no board is open: `POST` JSON `{"text","content","kind","monitor","target","message","at"}`. `text` is read by Slack and Telegram (`…/sendMessage?chat_id=`), `content` by Discord. Tried 3 times (0, 5, 30 seconds) in the background.

A webhook URL is often its secret too: it never goes into a log or an error message. Credentials in a monitor URL do not go along into alerts (`Target()` uses `Redacted`), and error messages do not repeat the URL.

## API

| Method | Path | Body | Response |
|---|---|---|---|
| GET | `/api/projects/{id}/uptime` | — | `{"items":[View…],"settings":Settings}` |
| POST | `/api/projects/{id}/uptime` | Monitor | `201 View`; defaults `active`, `notify`, `retries:1` |
| PUT | `/api/projects/{id}/uptime/settings` | `{"webhook"}` | `Settings`; `""` turns it off |
| PUT | `/api/projects/{id}/uptime/maintenance` | `{"windows":[Window…]}` | `Settings`; replaces the whole list. Its monitors look again right away |
| POST | `/api/projects/{id}/uptime/settings/test` | — | `{ok}`; `502 webhook_failed` |
| PATCH | `/api/uptime/{id}` | part of a Monitor | `View`; history stays, the next check runs right away. `id`, `projectId`, `pushToken` cannot be changed |
| DELETE | `/api/uptime/{id}` | — | `{ok}` |
| POST | `/api/uptime/{id}/check` | — | `202`; the result comes through an event. `409 paused`; `400` for `push` |
| GET | `/api/uptime/{id}/history?range=` | — | `Series`; `1h 24h 7d 30d 1y` |
| GET | `/api/uptime/{id}/events?limit=50` | — | `{"items":[{"t","status","msg","durSec"}]}`, newest first, at most 200 |
| GET | `/api/push/{token}?status=up\|down&msg=&ping=` | — | **No login.** `{ok}`; `404` for an unknown token or a paused monitor |

`View` = Monitor + `status`, `target`, `since`, `msg`, `ms`, `lastCheck`, `avgMs24h`, `uptime24h/7d/30d/1y` (`null` without checks), `cert {subject,issuer,notAfter}`, `recent` (the last 60 beats: `{"t","s","ms","m"}`, `s` 0 down, 1 up, 2 pending; `ms` −1 when not measured).

`Series` = `{range,from,to,stepSec,points:[{t,up,down,pending,avgMs,minMs,maxMs}],stats:{uptime,avgMs,minMs,maxMs,checks}}`. Steps: 1 minute, 10 minutes, 1 hour, 6 hours, 1 day. Steps without checks are not sent, and the chart line breaks there.

Events on `/api/events`:
- `{"type":"uptimeMonitor","monitor":View}` when created or changed, always before its first heartbeat.
- `{"type":"uptimeBeat","projectId","monitorId","beat","monitor":View without recent}` on each check.
- `{"type":"uptimeRemoved","projectId","id"}`.

Planner and command bar: `add an uptime monitor` (`node.add` kind `uptime`) adds its card. Monitors themselves cannot be created through the planner yet.

## Public status page

One page per project, without login, at `/status/{token}`. Off by default, and monitors are not included unless marked one by one.

**What shows, and only this:** the title the owner chose (default `Status`), then per monitor marked `public`: `publicName`, `Operational` / `Down` / `No data`, the 30-day uptime percentage, one box per day for 30 days, and the incidents of the last 30 days (start time rounded to the minute, incident duration). Times in UTC. A blip within the `retries` allowance does not show as an incident.

**What never shows:** URL, host, port, monitor type, machine, headers, keyword, error messages (they often hold internal IPs), response time, certificate, monitor id, push token, the real monitor name, project name, hub name, version, or any sign that this is NextBoard.

This is guaranteed by the shape of the code, not by filtering the output: the page is built from the `publicPage` struct (`internal/uptime/public.go`), which does not hold a `Monitor`, with `html/template`. `public_test.go` plants marker values in all those fields and checks that none of them appears.

- `publicName` is required for a public monitor, because monitor names often hold a host. Refused (`400`) when it holds the monitored host, an IP address, or `://`. This catches typos, not everything: the content stays the owner's responsibility.
- No scripts, no outside sources, no cookies, no forms. `Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'`, `X-Robots-Tag: noindex, nofollow`, `Referrer-Policy: no-referrer`, `Cache-Control: no-store` (a browser or CDN that stores the page would keep showing it after it is turned off). The page refreshes itself every 60 seconds through `<meta refresh>`.
- 192-bit token from `crypto/rand`. An unknown token, a page that is off, and any other path under `/status/` get exactly the same `404`, without the app's HTML. `POST …/public/rotate` makes a new address and the old address dies right away; turning off and on again keeps the address.
- The page is computed at most once per 15 seconds per token, so visitors do not add to the check load. Changing a monitor, turning the page off, or rotating takes effect at once.

| Method | Path | Body | Response |
|---|---|---|---|
| PUT | `/api/projects/{id}/uptime/public` | `{"enabled","title"}` | `{"enabled","token","title"}`; the token is made the first time it is turned on |
| POST | `/api/projects/{id}/uptime/public/rotate` | — | as above |
| GET | `/status/{token}` | — | **No login.** HTML; `404` otherwise |

`GET /api/projects/{id}/uptime` carries its settings in `settings.publicPage`. Monitors carry `public` and `publicName`.

**Putting it on the internet.** The hub's main port is basically a shell on this machine, so do not open it. `nextboard serve --status-listen 127.0.0.1:12340` (or `install --status-listen`, stored in `config.json` as `statusListen`) opens a second address that knows only `GET /status/{token}`; everything else is `404`, including `/`, `/api/*`, `/setup`, and web assets. Point Cloudflare Tunnel, `tailscale funnel 12340`, or nginx (`deploy/nginx-status.conf.example`) at that port only. HTTPS is still the job of whatever sits in front of it. The address the dialog shows uses the hub address that is currently open, so replace its host with the public domain. A failed bind is only written to the log; the hub keeps running.

Monitors are checked as long as the hub is running, so when the hub is down its page is down too, and visitors cannot tell that apart from a service that is down. A standalone status page needs a different place.

## Storage

`<data>/uptime/`: `monitors.json`, `settings.json` (including the public page token), and one folder per monitor. A JSON store file is rewritten whole, which does not suit a heartbeat every minute, so the history is append-only:

| File | Content | Kept |
|---|---|---|
| `beats.jsonl` | Each check | 3 days |
| `hours.jsonl` | Summaries per closed hour | 1 year |
| `events.jsonl` | Down and recovered | last 200 |

Lines torn by a crash are skipped. A beat reaches the disk before it is announced; hours not yet written are recomputed from the beats on load. Tidied on load and every 6 hours. Ranges of 24 hours and less come exactly from the beats, longer ranges per full hour.

The content of `monitors.json` includes `headers` and the webhook URL **written as they are** (mode `0600`, refused by the files API like other hub files).

## Security

- The owner decides the targets, and private addresses are allowed on purpose, because watching internal services is the point. Only a logged-in user can add a monitor; agents have no tool for it.
- `host` is validated (`hostRe`) before it becomes a `ping` argument: it cannot start with `-` and cannot hold spaces or shell characters. `ping` runs through `Argv`, not a shell.
- `GET /api/push/{token}` and `GET /status/{token}` are the only uptime routes without login. Push: a 192-bit token from `crypto/rand`, which can only add heartbeats to that monitor; reports faster than one per second count as one. Status: see above.
- `ignoreTls` is a per-monitor choice, off by default.
- Each HTTP check uses a new connection and ignores the proxy from the hub's environment, because the question is whether this target answers.

## Not built yet

Status pages per group, maintenance shown with its title and schedule on the public page, and WebSocket messages (send, then wait for a given answer).

## Verification (2026-11-03)

A real hub and real Chrome: a local target turned off and on, the sequence up → pending → down → up, toasts and a local webhook, a hub restart with 24 hours of history and no false alerts, a real 20-second timer, chart hover at different board zooms, a narrow card. The real `https://example.com` for certificates. `http`, `tcp`, `ping` through a real sshd. **Not verified yet:** webhooks to the real Slack, Discord, and Telegram (the formats come from their documentation; only a local receiver was tested).
