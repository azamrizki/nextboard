# NextBoard Hub API — kontrak

Base URL: hub melayani HTTP polos di `http://127.0.0.1:12312`; klien memakai `https://<domain>` lewat nginx (SPEC §5). Semua JSON memakai camelCase. Waktu dalam RFC 3339 (`2026-10-27T22:46:00Z`).

## Konvensi umum

- **Error:** status HTTP non-2xx dengan body `{"error": "pesan"}`.
- **CSRF:** semua request `POST`/`PUT`/`PATCH`/`DELETE` **wajib** membawa header `X-NextBoard: 1` (klien app juga selalu mengirimnya). Tanpa header → `403`.
- **Auth:** salah satu dari
  - cookie sesi browser (`HttpOnly; SameSite=Strict; Path=/`, 30 hari, diperpanjang saat dipakai): `nb_session` + `Secure` bila klien datang lewat HTTPS, `nb_session_http` lewat HTTP. Namanya beda karena browser menolak halaman `http://` menimpa cookie `Secure` bernama sama di host itu; atau
  - `Authorization: Bearer <accessToken>` (device yang di-pair), juga untuk WebSocket. Token di URL ditolak, karena reverse proxy mencatat query string.
- Tanpa auth → `401 {"error":"unauthorized"}`.
- **WebSocket Origin:** kalau request WS memakai cookie dan membawa header `Origin`, host Origin harus sama dengan `Host`. WS yang memakai bearer token tidak dicek Origin-nya.
- **Batas body:** board ops 2 MB, upload aset / paste file 20 MB, tulis file 20 MB isi (body sampai 28 MB, karena base64), lainnya 64 KB → `413`.

## Health, hub, setup

| Method | Path | Auth | Request | Response |
|---|---|---|---|---|
| GET | `/api/health` | — | — | `{"ok":true,"name":"devbox","version":"0.1.0"}` |
| GET | `/api/setup/status` | — | — | `{"needsSetup":true,"hubName":""}` |
| POST | `/api/setup` | — | `{"code":"K7QX-M2PA","password":"...","hubName":"devbox"}` | `200 {"ok":true}` + set cookie (langsung login). `400` password < 8 karakter / hubName kosong. `403` kode salah/kedaluwarsa/terpakai. `409` sudah di-setup. |
| GET | `/api/hub` | ya | — | `Hub` |

```jsonc
// Hub
{
  "name": "devbox",
  "version": "0.1.0",
  "urls": ["https://board.example.com"],   // lewat HTTPS: origin itu + extraUrls HTTPS; lewat HTTP: alamat http:// hub
  "listen": "0.0.0.0:12312"
}
```

## Auth

| Method | Path | Auth | Request | Response |
|---|---|---|---|---|
| POST | `/api/auth/login` | — | `{"password":"..."}` | `200 {"ok":true}` + cookie. `401` salah. `429` rate limit (5 gagal / IP / 10 menit). |
| POST | `/api/auth/logout` | ya | — | `200 {"ok":true}`, cookie dihapus |
| GET | `/api/auth/me` | ya | — | `{"kind":"admin"}` atau `{"kind":"device","deviceId":"d_…","deviceName":"Pixel 8"}` |
| POST | `/api/auth/challenge` | — | `{"deviceId":"d_…"}` | `{"nonce":"<base64>"}` (berlaku 60 detik, sekali pakai). `404` device tidak dikenal/di-revoke. |
| POST | `/api/auth/device` | — | `{"deviceId":"d_…","nonce":"<nonce persis>","sig":"<base64>"}` | `{"accessToken":"<opaque>","expiresIn":3600,"expiresAt":"…"}`. `401` signature salah. |

**Signature device:** `sig = base64std( Ed25519.Sign(privateKey, utf8Bytes(nonce)) )` — yang ditandatangani adalah string `nonce` persis seperti diterima (bytes UTF-8 dari teks base64-nya).

## Pairing & devices

| Method | Path | Auth | Request | Response |
|---|---|---|---|---|
| POST | `/api/pair/start` | admin | — | `PairStart` |
| POST | `/api/pair/complete` | — (kode wajib valid) | `{"code":"K7QX-M2PA","deviceName":"Pixel 8","platform":"android","publicKey":"<base64>"}` | `{"deviceId":"d_…","hubName":"devbox"}`. `403` kode salah/kedaluwarsa. |
| GET | `/api/devices` | ya | — | `Device[]` |
| PATCH | `/api/devices/{id}` | admin | `{"name":"..."}` | `Device` |
| DELETE | `/api/devices/{id}` | admin | — | `{"ok":true}`; semua token + WS device itu langsung diputus |

- `publicKey`: base64 standar (dengan padding) dari 32 byte raw public key Ed25519.
- `platform`: `android` | `ios` | `desktop` | `web` | lainnya bebas.
- Kode pairing: 8 karakter dari alfabet `ABCDEFGHJKMNPQRSTUVWXYZ23456789` dengan format `XXXX-XXXX`, berlaku 5 menit, sekali pakai. Server menerima kode tanpa tanda `-` dan huruf kecil juga.

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

**URI pairing:** `nextboard://pair?h=<hub>&u=<url1>,<url2>&c=<kode>` — tiap URL di `u` di-URL-encode sendiri lalu digabung dengan koma. Klien: split `u` dengan `,`, decode tiap bagian.

## Machines

```jsonc
// Machine
{
  "id": "local",                 // "local" untuk This PC, selainnya "m_…"
  "name": "This PC",
  "kind": "local",               // "local" | "ssh"
  "host": "", "port": 22, "user": "", "sshConfigHost": "",
  "authMethod": "",              // "sshconfig" | "agent" | "key" | "password" ("" untuk local)
  "persistMode": "auto",         // "auto" | "tmux" | "direct"
  "effectivePersist": "tmux",    // hasil resolusi auto → "tmux" | "direct" ("" untuk local)
  "status": "connected",         // "local" | "connecting" | "connected" | "disconnected" | "reconnecting" | "hostkey" | "error"
  "error": "",
  "pendingHostKey": null,        // atau { "fingerprint":"SHA256:…", "keyType":"ssh-ed25519", "host":"devbox:22" } saat status = "hostkey"
  "hostKeyMismatch": false,      // true = host key BERUBAH, koneksi ditolak (tampilkan peringatan merah)
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
// Preset (dengan ketersediaan per machine)
{ "id": "claude", "name": "Claude Code", "command": "claude", "args": [], "resumeArgs": ["--continue"], "color": "#d97757", "available": true }
// DirListing
{ "path": "/home/alice/apps", "parent": "/home/alice", "entries": [ { "name": "nextboard", "dir": true }, { "name": "notes.md", "dir": false } ] }
```

| Method | Path | Request | Response |
|---|---|---|---|
| GET | `/api/machines` | — | `Machine[]` (local selalu pertama) |
| GET | `/api/machines/sshconfig` | — | `SSHConfigHost[]` (tanpa wildcard) |
| POST | `/api/machines` | `AddMachine` | `201 Machine` (status bisa `hostkey` → minta konfirmasi, `connected`, atau `error`) |
| POST | `/api/machines/{id}/hostkey` | `{"accept":true,"fingerprint":"SHA256:…"}` | `Machine` (tersambung + di-probe). `409` fingerprint tidak cocok dengan yang pending. |
| POST | `/api/machines/{id}/credentials` | `{"kind":"password"\|"passphrase","value":"…","remember":true}` | `Machine`. Lihat "Password dan passphrase". `400` kosong, `409 auth_not_needed` bila machine tidak sedang menunggu itu |
| POST | `/api/machines/{id}/connect` | — | `Machine` (coba sambung ulang sekarang) |
| POST | `/api/machines/{id}/probe` | — | `Machine` |
| PATCH | `/api/machines/{id}` | `{"name"?: "...", "persistMode"?: "auto"\|"tmux"\|"direct"}` | `Machine` |
| DELETE | `/api/machines/{id}` | — | `{"ok":true}`; `409` kalau masih ada sesi; `400` untuk `local` |
| GET | `/api/machines/{id}/presets` | — | `Preset[]` |
| GET | `/api/machines/{id}/ls?path=` | `path` kosong / `~` = home | `DirListing` |
| GET | `/api/machines/{id}/orphans` | — | `{"names":["nb-abc123"]}` |
| POST | `/api/machines/{id}/cleanup` | `{"names":["nb-abc123"]}` | `{"killed":["nb-abc123"]}` |

```jsonc
// AddMachine
{
  "name": "devbox",                // opsional, default = sshConfigHost atau host
  "sshConfigHost": "devbox",       // isi ini ATAU host manual
  "host": "10.0.0.5", "port": 22, "user": "alice",
  "authMethod": "sshconfig",      // "sshconfig" | "agent" | "key" | "password"
  "privateKey": "-----BEGIN OPENSSH PRIVATE KEY-----…",   // hanya untuk "key"
  "passphrase": "",               // opsional untuk "key"
  "password": "",                 // hanya untuk "password"
  "persistMode": "auto"
}
```

`authMethod: "sshconfig"` = coba berurutan: IdentityFile dari config / default `~/.ssh/id_ed25519`, `id_ecdsa`, `id_rsa`, lalu ssh-agent. Kredensial yang di-upload tidak pernah dikembalikan ke klien.

## Password dan passphrase

Saat autentikasi gagal dan ada yang bisa diketik orang, machine berhenti dengan `status:"auth"` dan `needsAuth`, lalu klien memunculkan dialog. Hub tidak mencoba ulang selama menunggu: password salah yang dicoba berulang berujung ban dari server (fail2ban).

```jsonc
"needsAuth": {
  "kind": "passphrase",           // "password" | "passphrase"
  "keyPath": "~/.ssh/id_ed25519", // key yang dimaksud, seperti tertulis di config; kosong = key yang di-upload
  "host": "alice@10.0.0.5",         // yang bertanya; bisa jump host
  "prompt": "",                   // pertanyaan server lewat keyboard-interactive
  "wrong": false,                 // jawaban terakhir ditolak
  "canPassword": true             // diminta passphrase, tapi server juga menerima password
}
```

- **Cara tahu:** rantai autentikasi mencatat metode yang ditawarkan server, karena callback hanya jalan untuk metode yang server tawarkan. Urutan pertanyaan: password yang baru ditolak, lalu key terkunci (kalau server menerima key), lalu password. Kalau server hanya menerima key dan tidak ada key, tidak ada yang bisa diketik: `status:"error"` dan hub terus mencoba seperti biasa.
- **Key yang terkunci** hanya ditanyakan kalau semua cara lain gagal. Key di ssh-agent atau key lain yang diterima server tidak memunculkan dialog.
- **Passphrase dicoba pada key di hub**, jadi salah ketik tidak sampai ke server. Password salah memakai satu percobaan.
- **Penyimpanan:** jawaban ada di memori selama dicoba. `remember:true` menyimpannya terenkripsi di `creds/<id>.enc` **setelah koneksi berhasil**, jadi jawaban salah tidak pernah tersimpan. `remember:false` tetap di memori untuk reconnect sampai hub restart, lalu ditanya lagi. Jawaban tidak pernah kembali di respons atau log.
- Password hanya untuk machine itu, bukan untuk jump host; passphrase berlaku untuk key di jump host juga. 2FA tidak didukung: kode sekali pakai tidak bisa dipakai reconnect, dan dialog hanya menampilkan pertanyaan server lewat `prompt`.

## Log koneksi

Catatan tetap tentang perilaku koneksi ke server, untuk dipelajari saat kejadian di jaringan sungguhan. Selalu aktif, tidak perlu dinyalakan.

- **File:** `<data>/logs/connections.jsonl`, satu baris JSON per kejadian, mode 0600. Diputar sekali ke `connections.1.jsonl` tiap 4 MiB, jadi paling banyak sekitar 8 MiB. Log lama hilang saat diputar.
- **Yang dicatat:** apa yang terjadi pada koneksi: waktu, nama server, alasan putus, lama tersambung, lama putus, latensi, nama tool yang gagal atau lambat, dan pesan error dari transport (dipotong 300 karakter).
- **Yang tidak dicatat:** perintah, output, isi file, dan path. Perintah yang gagal dengan sendirinya bukan masalah koneksi dan tidak masuk log.

`GET /api/logs/connections?machineId=&since=&limit=` (admin) menjawab `{ "items": [Event], "summary": Summary }`, terbaru lebih dulu. `since` berformat RFC 3339. `limit` default 200, maksimal 2000. Filter `machineId` tetap menyertakan kejadian hub, karena hub mati juga memutus koneksi.

| `kind` | Arti | Field penting |
|---|---|---|
| `hub_start`, `hub_stop` | Hub mulai, berhenti normal | `version`, `pid` |
| `hub_unclean_exit` | Run sebelumnya berakhir tanpa berhenti (crash, `kill -9`, listrik) | `lastSeen`, dengan selisih sampai 1 menit |
| `connected` | Tersambung | `attempt`, `durationMs`, `downtimeMs` |
| `connect_failed` | Percobaan gagal: 5 pertama, lalu tiap ke-20, dan selalu bila host key bermasalah | `attempt`, `reason`, `error` |
| `disconnected` | Putus | `reason`, `uptimeMs`, `error`, `calls`, `failures` |
| `keepalive_miss` | Keepalive tidak dijawab | `misses` |
| `link_stats` | Tiap 30 menit selama tersambung, sebagai pembanding saat koneksi sehat | `rttAvgMs`, `rttMaxMs`, `calls`, `failures`, `uptimeMs` |
| `tool_failed` | Tool agent gagal karena koneksi | `tool`, `reason`, `durationMs` |
| `tool_slow` | Tool file 5 detik atau lebih (`bash` dikecualikan) | `tool`, `durationMs` |
| `channel_lost` | Channel console di server mati | `sessionId` |

`reason` pada `disconnected`: `keepalive_timeout` (jaringan diam, tanpa error apa pun), `connection_lost` (koneksi ditutup atau di-reset), `closed` (disengaja, tidak dihitung sebagai putus). Pada `tool_failed`: `not_connected`, `disconnected`, `no_sftp`, `timeout`.

`calls` dan `failures` dihitung sejak laporan terakhir, jadi `link_stats` mengosongkannya.

Cara membaca, dari yang paling mudah:

- **Web:** Machines › kartu server › **Connection log**.
- **CLI:** `nextboard connlog [--machine <nama|id>] [-n 50] [--json] [--data-dir <dir>]`. Membaca file langsung, jadi tetap jalan saat hub mati.
- **File:** `jq` pada `<data>/logs/connections.jsonl`.

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
  "busy": false,                // ada output dalam 2 detik terakhir
  "startedAt": "…", "lastOutputAt": "…",
  "error": "",
  "color": "#d97757",           // aksen preset
  "machineName": "This PC"
}
```

| Method | Path | Request | Response |
|---|---|---|---|
| GET | `/api/presets` | — | `Preset[]` (tanpa `available`) |
| GET | `/api/sessions` | — | `Session[]` |
| POST | `/api/sessions` | `{"machineId":"local","preset":"claude","name"?:"…","cwd"?:"…","cols"?:120,"rows"?:32}` | `201 Session` (perintah tidak ditemukan → tetap dibuat, status `exited`/`error`, pesan merah di terminal) |
| GET | `/api/sessions/{id}` | — | `Session` |
| PATCH | `/api/sessions/{id}` | `{"name":"…"}` | `Session` |
| DELETE | `/api/sessions/{id}` | — | `{"ok":true}` (kill + hapus node) |
| POST | `/api/sessions/{id}/restart` | `{"resume":true}` | `Session` |
| POST | `/api/sessions/{id}/kill` | — | `Session` (proses dihentikan, node tetap, status `exited`) |
| POST | `/api/sessions/{id}/input` | `{"data":"ls -la\r"}` | `{"ok":true}` (aksi cepat untuk mobile) |

Aplikasi desktop di board: lihat [API-gui.md](API-gui.md). Uptime monitoring: lihat [API-uptime.md](API-uptime.md). Credentials: lihat [API-vault.md](API-vault.md).

Board, project, workspace, file machine, dan aset: lihat [API-board.md](API-board.md) (board v2 per workspace; `GET/PUT /api/board` lama sudah dihapus). `Session` juga punya `projectId` dan `workspaceId`.

## WebSocket

### `/api/events`

Server → klien, frame text JSON:

```jsonc
{ "type": "snapshot", "sessions": [Session…], "machines": [Machine…], "devices": [Device…] } // dikirim sekali saat tersambung
{ "type": "session",  "session": Session }   // dibuat / berubah (status, busy, nama)
{ "type": "removed",  "id": "s_…" }           // sesi dihapus
{ "type": "machine",  "machine": Machine }
{ "type": "machineRemoved", "id": "m_…" }
{ "type": "device",   "device": Device }
{ "type": "deviceRemoved",  "id": "d_…" }
// + event project/board v2: lihat API-board.md §3 (project, projectRemoved, boardOps)
```

Klien tidak perlu mengirim apa pun. Server ping setiap 30 detik.

### `/api/sessions/{id}/ws`

- **Server → klien**
  - Frame **binary**: output mentah terminal. Frame binary **pertama** selalu snapshot scrollback (bisa kosong). Klien memanggil `term.reset()` sebelum menulis snapshot.
  - Frame **text** JSON: `{"t":"status","status":"exited","exitCode":0,"busy":false}` saat status berubah.
- **Klien → server** (frame text JSON)
  - `{"t":"i","d":"<input>"}` input keyboard. Selagi proses masih `starting`, ketikan (tanpa laporan terminal, ≤ 4 KiB) disimpan dan diberikan begitu proses ada; gagal start atau reattach → dibuang
  - `{"t":"r","c":<cols>,"r":<rows>}` resize (ukuran terminal mengikuti resize terakhir dari klien mana pun)
- Ping setiap 30 detik. Subscriber lambat (buffer 512 penuh) diputus; klien reconnect dan repaint dari snapshot.
- Device yang di-revoke → semua WebSocket-nya langsung ditutup (close code 4401). Token hanya dicek saat koneksi dibuka; WS yang sudah tersambung tidak diputus saat token kedaluwarsa.
- Login browser yang berakhir → semua WebSocket login itu ditutup (close code 4401): seketika saat logout, paling lambat sekitar 2 detik saat `nextboard reset-password` dari proses lain atau saat cookie kedaluwarsa. Login lain tidak terganggu.
- Subscriber yang terlalu lambat / sesi dihapus → close code 1013 (`resubscribe`); klien reconnect.

## Preset default

| id | name | command | args | resumeArgs | color |
|---|---|---|---|---|---|
| shell | Shell | `$SHELL` | `-l` | — | `#8b949e` |
| claude | Claude Code | `claude` | — | `--continue` | `#d97757` |
| codex | Codex | `codex` | — | `resume --last` | `#10a37f` |
| gemini | Gemini CLI | `gemini` | — | — | `#4285f4` |

Preset custom ditambahkan lewat `~/.nextboard/presets.json` (array `Preset` tanpa `available`; id sama menimpa default).
