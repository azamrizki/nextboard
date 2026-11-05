# NextBoard API — Fase 5: agen, status & notifikasi

Tambahan atas [API.md](API.md) dan [API-board.md](API-board.md); konvensi (auth, CSRF `X-NextBoard: 1`, camelCase, RFC 3339) tetap. Fitur: [ROADMAP.md](ROADMAP.md) Fase 5. Keputusan pemilik #1, #4, #5 mengikat dokumen ini.

**Error:** body tetap `{"error":"pesan"}`, kini boleh membawa `"code"` (snake_case, stabil untuk klien). Klien lama cukup membaca `error`. Kode yang dipakai hub Fase 5: `bad_agent`, `agent_exists`, `agent_builtin`, `agent_not_found`, `bad_handle`, `handle_taken`, `unknown_agent`, `bad_mode`, `profile_not_found`, `profile_mismatch`, `profile_unsupported`, `profile_dir_missing`, `remote_create_forbidden`, `bad_profile`, `not_agent`, `agent_running`, `shell_busy`, `wrap_missing`, `machine_offline`, `no_reply`, `no_transcript`, `bad_setting`, `bad_shortcut`, `job_not_found`.

## 0. Ringkasan desain

- **Katalog agen** = preset yang diperluas dengan kapabilitas (yolo, resume, live status, transcript, profil). Endpoint `/api/presets` tetap ada (superset).
- Setiap console punya **handle** unik per hub (`Ash`, `Bea`, …) untuk @mention (Fase 6).
- Console agen berjalan di **wrapper shell**: agen keluar → console jatuh ke shell user di tempat. Hub tahu agen sedang di foreground lewat **marker OSC** + cek proses.
- **Kanal event tanpa jaringan:** wrapper dan hooks menulis escape `OSC 777;nb;…` ke `/dev/tty`, atau — karena hook Claude Code berjalan tanpa controlling TTY (terverifikasi, §20) — langsung ke terminal proses CLI induknya; hub membuangnya dari stream sebelum scrollback/fanout. Cadangan: HTTP lewat reverse port-forward SSH / loopback lokal.
- **Engine status** menggabungkan hooks Claude, notify Codex, aturan layar VT per CLI, dan aktivitas output menjadi `agentState`.
- Tidak ada file config CLI yang ditulis, di machine mana pun (termasuk This PC): semua injeksi per-launch lewat flag.

## 1. Katalog agen (P2, C7, C9)

```jsonc
// Agent (= Preset v2). Field lama Preset tetap ada dan bermakna sama.
{
  "id": "claude", "name": "Claude Code",
  "kind": "agent",                 // "agent" | "shell"
  "source": "builtin",             // "builtin" | "catalog" | "custom"
  "base": "",                      // custom saja: "claude" | "codex" | "" — mewarisi kapabilitas base
  "command": "claude", "args": [], "env": [], "color": "#d97757",
  "detect": "claude",              // nama perintah untuk `command -v` saat probe; "" = tidak dideteksi
  "yoloArgs": ["--dangerously-skip-permissions"], "yoloEnv": [],
  "resume": {
    "kind": "session-id",          // "session-id" | "continue" | "picker" | "none"
    "launchArgs":   ["--session-id", "{agentSessionId}"],   // dipakai saat mode fresh (hub membuat UUID)
    "resumeArgs":   ["--resume", "{agentSessionId}"],
    "continueArgs": ["--continue"],                        // "terakhir di folder ini"
    "pickerArgs":   ["--resume"]                           // pemilih bawaan CLI
  },
  "liveStatus": "claude-hooks",    // "claude-hooks" | "codex-notify" | "screen" | "none"
  "transcript": "claude",          // "claude" | "codex" | "none"
  "profileEnv": "CLAUDE_CONFIG_DIR", // "" = tidak mendukung profil
  "screenRules": "claude",         // id aturan layar (§6.4); "" = generik (aktivitas output)
  "resumeArgs": ["--continue"],    // legacy = resume.continueArgs (klien lama)
  "available": true                // hanya di endpoint per machine
}
```

- Placeholder template hanya `{agentSessionId}`. Hub menolak template lain.
- `kind:"shell"` tidak memakai wrapper, tidak pernah menerima delivery, `agentState` selalu `shell`.
- Custom tanpa `base` hanya boleh `liveStatus` `"screen"|"none"`, `transcript:"none"`, `profileEnv:""`, `resume.kind` `"continue"|"none"`.

### 1.1 Built-in dan katalog

Built-in (`source:"builtin"`):

| id | command | yolo | resume | liveStatus | transcript | profileEnv |
|---|---|---|---|---|---|---|
| `shell` | `$SHELL -l` (kind shell) | — | none | none | none | — |
| `claude` | `claude` | `--dangerously-skip-permissions` | session-id (seperti contoh di atas) | claude-hooks | claude | `CLAUDE_CONFIG_DIR` |
| `codex` | `codex` | `--dangerously-bypass-approvals-and-sandbox` | session-id: launch `[]` (id ditemukan, §8.1), resume `resume {agentSessionId}`, continue `resume --last`, picker `resume` | codex-notify | codex | `CODEX_HOME` |
| `gemini` | `gemini` | `--yolo` | continue `--resume latest`† | screen | none | — |

Katalog (`source:"catalog"`, semua `kind:"agent"`, `liveStatus:"screen"`, `transcript:"none"`, deteksi = nama perintah):

| id | name | command / detect | yolo (kandidat) | resume (kandidat) |
|---|---|---|---|---|
| `opencode` | OpenCode | `opencode` | — | continue `--continue` |
| `aider` | Aider | `aider` | `--yes-always` | — |
| `amp` | Amp | `amp` | `--dangerously-allow-all` | — |
| `auggie` | Auggie | `auggie` | — | — |
| `cline` | Cline CLI | `cline` | `--auto-approve true`† | — |
| `codebuff` | Codebuff | `codebuff` | — | — |
| `cn` | Continue CLI | `cn` | `--allow *`† | — |
| `copilot` | GitHub Copilot CLI | `copilot` | `--allow-all-tools`† | continue `--continue`† |
| `crush` | Crush | `crush` | `--yolo` | — |
| `cursor-agent` | Cursor Agent | `cursor-agent` | `--force`† | — |
| `droid` | Droid | `droid` | — | — |
| `goose` | Goose | `goose` | env `GOOSE_MODE=auto` | — |
| `kimi` | Kimi CLI | `kimi` | `--yolo`† | — |
| `kiro` | Kiro CLI | `kiro-cli` args `chat` | `--trust-all-tools` | — |
| `pi` | Pi | `pi` | — | continue `--continue`† |
| `qwen` | Qwen Code | `qwen` | `--approval-mode yolo` | — |
| `vibe` | Mistral Vibe | `vibe` | —† | — |

† = kandidat yang **belum diverifikasi**: dikirim sebagai `yoloArgs: []` / `resume.kind:"none"` sampai diuji pada versi CLI nyata. Entri riset lain (Antigravity, Ante, Autohand, Command Code, Devin, Grok, Hermes, Kilocode, MiMo, OpenClaude, OpenClaw, Prime Agent, Rovo Dev, Trae) **tidak** dimasukkan sampai terbukti CLI publik dengan nama perintah itu; user bisa menambahkannya sebagai custom.

### 1.2 Deteksi dan default agent

- Probe (`machine.probeScript`) memeriksa `detect` semua entri (built-in + katalog + custom). `available` = `probe.commands[detect] != ""` atau ada override path di `settings.agents.paths[machineId][agentId]`.
- Override path menggantikan `command` saat launch dan saat probe (`command -v <path>`).
- **Default agent per machine:** `settings.agents.defaults[machineId]`; bila kosong → entri `available` pertama dengan urutan `claude, codex, gemini`, lalu katalog alfabetis, lalu custom; bila tidak ada → `shell`. Field turunan `defaultAgent` ada di `GET /api/machines/{id}/agents`.
- Agent "armed" untuk tool Spawn (`T`) adalah state per klien (localStorage), bukan di hub.
- **Mematikan agent:** `settings.agents.disabled[agentId] = true` (hapus dengan `null`), satu switch untuk semua machine. Agent itu tetap terdeteksi tapi tidak ditawarkan: hilang dari `GET /api/machines/{id}/agents` dan `/presets` kecuali dengan `?all=1` (ditandai `disabled:true`, untuk halaman Settings), tidak pernah jadi default (`defaults` tetap tersimpan dan berlaku lagi saat dinyalakan), dan runner beralih ke Claude Code atau Codex yang lain. `POST /api/sessions` dan ganti agent lewat restart menjawab `409 agent_disabled`. Lane layout dan role agent jatuh ke default. **Console yang sedang jalan tidak disentuh** dan bisa di-restart. `GET /api/agents` tetap mengembalikan semuanya, karena console lama masih perlu nama dan kemampuannya. Shell bukan agent dan tidak bisa dimatikan.

### 1.3 Endpoint

| Method | Path | Request | Response / error |
|---|---|---|---|
| GET | `/api/agents` | — | `Agent[]` (tanpa `available`); urut built-in, katalog, custom |
| GET | `/api/machines/{id}/agents` | — | `{"defaultAgent":"claude","agents":[Agent…]}` (dengan `available`) |
| GET | `/api/presets`, `/api/machines/{id}/presets` | — | tetap: `Agent[]` (superset Preset lama) |
| POST | `/api/agents` | `{id?, name, command, args?, env?, color?, kind?, base?, yoloArgs?, yoloEnv?, resume?}` | `201 Agent`. `400 bad_agent` (id tidak cocok `^[a-z][a-z0-9-]{1,23}$`, command kosong, field terlarang untuk custom), `409 agent_exists` (id built-in/katalog/custom) |
| PATCH | `/api/agents/{id}` | field yang sama (tanpa `id`) | `Agent`. `403 agent_builtin` untuk built-in/katalog |
| DELETE | `/api/agents/{id}` | — | `{"ok":true}`; console yang sedang jalan tidak disentuh (Spec menyimpan command sendiri). `403 agent_builtin` |

`id` default = slug dari `name`. Entri lama di `presets.json` dengan id built-in tetap dihormati sebagai override (tampil `source:"custom"`, `base` = id itu) demi kompatibilitas, tapi API tidak bisa membuatnya lagi.

## 2. Handle console (C1)

- `handle`: `^[A-Za-z][A-Za-z0-9-]{0,19}$`, unik **per hub** (lintas machine/project), dibandingkan case-insensitive.
- Kata cadangan (ditolak): `everyone`, `all`, `everybody`, `me`, `bar`, `hub`, `orchestrator`, `group`, `loop`, `memory`.
- Auto-assign saat create: nama pertama yang belum dipakai dari daftar bawaan NextBoard, urut:
  `Arkan Bintang Cantika Daffa Elvan Farel Gibran Haikal Ilham Jihan Kayla Luna Maura Naufal Olivia Pandu Rafa Reyhan Salsa Tiara Vania Wildan Yasmin Zahra`.
  Bila habis, sufiks angka: `Arkan2`, `Bintang2`, … (angka terkecil yang bebas).
- Rename lewat `PATCH /api/sessions/{id} {"handle":"Rex"}` → `400 bad_handle` / `409 handle_taken`. Fase 6 juga menolak handle yang sama dengan nama grup di workspace console itu.
- Handle dilepas saat sesi dihapus. Sesi lama tanpa handle diberi handle saat hub start (migrasi, urut `createdAt`).
- Header node: `@Ash · Claude Code @ devbox` + badge YOLO + pill status (DESIGN.md).

## 3. Peluncuran dengan wrapper (C4, C10, keputusan #5)

### 3.1 Mode peluncuran

| mode | Arti | Argumen agen |
|---|---|---|
| `fresh` | percakapan baru | `args` + `resume.launchArgs` (Claude: UUID baru dari hub) |
| `resume` | lanjutkan `agentSessionId` persis | `args` + `resume.resumeArgs`; tanpa id → jatuh ke `continue` |
| `continue` | terakhir di folder | `args` + `resume.continueArgs` |
| `pick` | pemilih bawaan CLI | `args` + `resume.pickerArgs` |
| `shell` | jangan jalankan agen, langsung shell | — |

Argumen live status (§4.3/§4.4) dan `yoloArgs` (bila `yolo`) ditambahkan di akhir. `kind:"shell"` tidak memakai wrapper (perilaku lama: `exec $SHELL -l`).

### 3.2 Wrapper

Hub menyiapkan env per launch lalu menjalankan, di bawah login shell (POSIX: `$SHELL -lc`, non-POSIX seperti fish: `/bin/sh -lc`, sama dengan `loginWrap` sekarang):

```sh
export NEXTBOARD_SESSION_ID=<sid> NB_TOKEN=<token> NB_WRAP=<skrip> NB_HOOK_SH=<skrip hook> NB_HOOKS=<json> \
       [NB_HOOK_URL=<url>] [CLAUDE_CONFIG_DIR|CODEX_HOME=<dir profil>] TERM_PROGRAM=nextboard COLORTERM=truecolor
exec /bin/sh -c "$NB_WRAP" nb <mode> <agentSessionId|-> <yolo 0|1>
```

Lokal: env lewat `exec.Cmd.Env` (bukan baris perintah). Remote: inline seperti sekarang (`cdExpr && export … && exec …`), di-quote dengan `machine.Quote`. Panjang total baris perintah ≤ 12 KiB (batas pesan tmux 16 KiB).

Isi `NB_WRAP` (normatif; hub membangun blok `case` dari katalog):

```sh
nb_m() { p=$(printf '%s' "$2" | base64 | tr -d '\n=' | tr '+/' '-_')
  if [ -n "$TMUX" ]; then printf '\033Ptmux;\033\033]777;nb;%s;%s\007\033\\' "$1" "$p"
  else printf '\033]777;nb;%s;%s\007' "$1" "$p"; fi >/dev/tty 2>/dev/null; }
[ "$1" = shell ] && exec "${SHELL:-/bin/sh}" -l
trap : INT QUIT                      # wrapper tahan Ctrl-C; anak tetap disposisi default
nb_m agent-start "{\"k\":\"$NB_TOKEN\",\"pid\":$$,\"mode\":\"$1\",\"sid\":\"$2\"}"
set -- "$1" "$2" "$3" claude         # command + args katalog
case "$1" in fresh) set -- "$@" --session-id "$2";; resume) set -- "$@" --resume "$2";;
  continue) set -- "$@" --continue;; pick) set -- "$@" --resume;; esac
set -- "$@" --settings "$NB_HOOKS"   # §4.3, hanya bila live status aktif
[ "$3" = 1 ] && set -- "$@" --dangerously-skip-permissions
command -v "$4" >/dev/null 2>&1 || printf '\r\n\033[1;31mnextboard: perintah tidak ditemukan: %s\033[0m\r\n' "$4"
shift 3; "$@"; nb_rc=$?
trap - INT QUIT
nb_m agent-exit "{\"k\":\"$NB_TOKEN\",\"rc\":$nb_rc}"
exec "${SHELL:-/bin/sh}" -l
```

- `pid` = PID wrapper; setelah `exec $SHELL -l` PID yang sama menjadi shell interaktif user. Hub menyimpannya sebagai `wrapperPid` (runtime, juga untuk pause Fase 6).
- `Session.status` tetap semantik proses: agen keluar → `status` tetap `running`, `agentState:"shell"`. `exited` hanya bila shell fallback juga keluar.
- Env yang diwarisi service dan dibuang sebelum launch: `CLAUDECODE`, `CLAUDE_CODE_*`, `NB_*`, `NEXTBOARD_*` (lalu diisi ulang oleh hub).
- Implementasi: blok `case` memakai `:` untuk mode tanpa argumen, `pick` jatuh ke `continueArgs` bila agen tanpa picker, `yoloEnv` diekspor dengan `[ "$3" = 1 ] && export K=V`. Baris `command -v` hanya mencetak pesan merah (agen tetap dijalankan, rc 127 → `agent-exit`); di This PC hub juga memeriksa perintah sebelum launch (status `error` seperti Fase 1–4). `NB_HOOK_CH` (`tty`/`http`) diisi dari `settings.agents.hookChannel`.

### 3.3 Restart / resume dari shell

`POST /api/sessions/{id}/agent {"mode":"fresh"|"resume"|"continue"|"pick","agentSessionId"?}`:

1. Syarat: `agentKind:"agent"`, `foreground:"shell"`, shell **idle di prompt** (cek proses §6.5: foreground pgrp = `wrapperPid`) dan tidak ada input klien ke sesi itu dalam 2 dtk terakhir.
2. Hub menulis `\x03` (Ctrl-C, membatalkan baris di semua shell dan mode; hanya shell yang menerimanya karena syarat 1), menunggu 200 ms, lalu mengetik satu baris yang diawali spasi: ` exec /bin/sh -c "$NB_WRAP" nb <mode> <id|-> <yolo>`. `\r` hanya ditekan bila layar menampilkan persis baris itu sampai posisi kursor. Bila tidak muncul dalam 3 dtk, baris dibatalkan lagi, tidak ada yang jalan, dan responsnya `412 wrap_missing`. Untuk `fresh` Claude, hub membuat UUID baru dulu.

   Ctrl-U tidak cukup: ia hanya mengosongkan sampai kursor, sehingga sisa setelah kursor menempel ke baris (`> file` ikut jalan dan mengosongkan file itu), dan di mode perintah vi tombol yang diketik menjadi perintah edit. Teruji di bash 5.2, zsh 5.9, dan fish 3.7, mode biasa dan vi (`TestStartingAnAgentInAnyShell`).
3. Respons `200 Session` (state `starting`). Error: `409 agent_running`, `409 shell_busy` (ada job di foreground), `409 not_agent` (kind shell), `412 wrap_missing` (marker `agent-start` tidak datang dalam 10 dtk → UI menawarkan restart penuh), `503 machine_offline`.

`POST /api/sessions/{id}/restart` tetap = **restart penuh** (kill wrapper + launch baru, scrollback hub tetap). Body: `{"resume"?:bool, "mode"?, "agentSessionId"?, "agent"?, "profileId"?, "yolo"?}`; `resume:true` lama = mode `resume` bila `agentSessionId` ada, selain itu `continue`. `agentSessionId` (tambahan atas permintaan klien) memindahkan console ke percakapan itu dalam satu panggilan: mode default `resume`, id disimpan sebagai `agentSessionId` baru (tanpa harus restart mode `shell` lalu §3.3). Ganti agen/profil selalu restart penuh.

### 3.4 Hub restart dan reconnect

| Situasi | Perilaku |
|---|---|
| Hub restart, sesi lokal / SSH direct (proses mati) | `settings.agents.resumeOnRestart`: `auto` (default) → mode `resume` bila agen terakhir di foreground, `shell` bila terakhir di shell; `ask` → mode `shell` + `resumeOffer:true` di Info (UI: "Resume conversation?" per node + banner "Resume all"; aksi = §3.3 mode `resume`); `never` → `shell`. Console Claude yang belum menyimpan percakapan (belum pernah diberi pesan, transcript tidak ada) tidak punya yang bisa dilanjutkan: `auto` → mode `fresh` dengan id baru, `ask` → tanpa `resumeOffer`. Bila pengecekan gagal, `resume` tetap dipakai. CLI yang berakhir karena hub sendiri berhenti tidak dihitung sebagai keluar dari agen |
| SSH tmux re-attach (proses hidup) | marker selama putus hilang; state dihitung ulang dari layar + cek proses (`stateSource:"screen"`) sampai event hook berikutnya |

## 4. Kanal event dari terminal

### 4.1 Format marker

```
ESC ] 777 ; nb ; <type> ; <payload> BEL        (ST = ESC \ juga diterima sebagai terminator)
payload = base64url(JSON) tanpa padding (decoder hub juga menerima alfabet standar dan padding)
```

| type | Pengirim | JSON |
|---|---|---|
| `agent-start` | wrapper | `{"k":token,"pid":int,"mode":"fresh…shell","sid":"uuid atau -"}` |
| `agent-exit` | wrapper | `{"k":token,"rc":int}` |
| `hook` | hooks Claude | `{"k":token,"ev":"PreToolUse","raw":"<base64 std dari ≤3072 byte pertama stdin hook>"}` |
| `notify` | notify Codex | `{"k":token,"ev":"turn","raw":"<base64 std dari ≤3072 byte argumen JSON>"}` |

`k` = `hookToken` per sesi (128 bit acak, base64url), dibuat saat sesi dibuat, disimpan di `sessions.json`, **tidak pernah** dikirim ke klien. Marker dengan token salah dibuang dan diabaikan (dihitung di log debug). Token mencegah pemalsuan dari konten yang sekadar ditampilkan (`cat file`, output web).

### 4.2 Stripper di hub

- Berjalan di `Session.output` **sebelum** `ring.Write`, fanout, dan model VT. Marker tidak pernah masuk scrollback, WebSocket, atau file `scrollback/`.
- Mesin state streaming: deteksi prefiks `ESC ] 777 ; nb ;` lintas batas `Read`; tahan maksimal 16 KiB. Tanpa terminator dalam 16 KiB atau 2 dtk → byte dilepas apa adanya (bukan marker).
- Semua OSC `777;nb;` dibuang dari stream, valid atau tidak. OSC lain (judul, hyperlink) diteruskan.
- Rate: > 50 marker/dtk per sesi → kelebihan dibuang.
- Dekode `raw` toleran: hub mencari field yang dikenal (`session_id`, `transcript_path`, `hook_event_name`, `tool_name`, `notification_type`, `message`, `type`, `thread-id`, `last-assistant-message`) dengan pemindai string, karena JSON bisa terpotong di 3072 byte.

### 4.3 Hooks Claude Code (injeksi per-launch, keputusan #1)

`NB_HOOKS` = JSON untuk `--settings` (string inline, tidak ada file):

```jsonc
{ "hooks": {
  "SessionStart":     [{ "hooks": [{ "type": "command", "timeout": 5, "command": "set -- hook SessionStart; eval \"$NB_HOOK_SH\"" }] }],
  "UserPromptSubmit": [{ "hooks": [ …ev UserPromptSubmit… ] }],
  "PreToolUse":       [{ "matcher": "*", "hooks": [ …ev PreToolUse… ] }],
  "PostToolUse":      [{ "matcher": "*", "hooks": [ …ev PostToolUse… ] }],
  "Notification":     [{ "hooks": [ …ev Notification… ] }],
  "Stop":             [{ "hooks": [ …ev Stop… ] }],
  "SubagentStop":     [{ "hooks": [ …ev SubagentStop… ] }],
  "SessionEnd":       [{ "hooks": [ …ev SessionEnd… ] }] } }
```

`NB_HOOK_SH` (normatif; argumen `$1`=type, `$2`=event, `$3`=JSON dari argv bila ada). **Diubah setelah verifikasi (§20):** Claude Code menjalankan hook di sesi baru tanpa controlling TTY, jadi `/dev/tty` gagal; kanal kedua menulis ke terminal proses CLI induk (`$PPID`):

```sh
if [ -n "$3" ]; then r=$(printf %s "$3" | head -c 3072 | base64 | tr -d '\n')
else r=$(head -c 3072 | base64 | tr -d '\n'); cat >/dev/null; fi
p=$(printf '{"k":"%s","ev":"%s","raw":"%s"}' "$NB_TOKEN" "$2" "$r" | base64 | tr -d '\n=' | tr '+/' '-_')
if [ -n "$TMUX" ]; then f='\033Ptmux;\033\033]777;nb;%s;%s\007\033\\'; else f='\033]777;nb;%s;%s\007'; fi
nb_t() { { printf "$f" "$1" "$p" >/dev/tty; } 2>/dev/null && return 0
  t=$(readlink "/proc/$PPID/fd/0" 2>/dev/null)
  case "$t" in /dev/pts/*|/dev/tty?*) ;; *) t=/dev/$(ps -o tty= -p "$PPID" 2>/dev/null | tr -d ' ');; esac
  case "$t" in /dev/pts/*|/dev/tty?*) ;; *) return 1;; esac
  [ -c "$t" ] && { printf "$f" "$1" "$p" >"$t"; } 2>/dev/null; }
nb_h() { [ -n "$NB_HOOK_URL" ] && curl -fsS -m 2 --data-binary "$1;$p" "$NB_HOOK_URL" >/dev/null 2>&1; }
case "$NB_HOOK_CH" in http) nb_h "$1";; tty) nb_t "$1";; *) nb_t "$1" || nb_h "$1";; esac
exit 0
```

Menulis ke terminal induk aman walau menyela tulisan TUI: stripper membuang byte marker persis, sehingga stream asli tersusun kembali (escape TUI yang terpotong marker tetap utuh setelah strip).

Aturan: hook tidak pernah menulis ke stdout (output `SessionStart`/`UserPromptSubmit` masuk ke konteks model), selalu exit 0, sisa stdin dikuras. Hook user sendiri tetap berjalan (`--settings` menambah sumber; terverifikasi §20). `--bare` tidak pernah dipakai.

### 4.4 Codex notify

Argumen tambahan saat live status aktif: `-c 'notify=["sh","-c","eval \"$NB_HOOK_SH\"","nb","notify","turn"]'` (nilai TOML; Codex menambahkan JSON notifikasi sebagai argv terakhir → `$3`). Notify milik user di `config.toml` tertimpa selama sesi ini (lihat Open issues).

### 4.5 Kanal cadangan HTTP

| Machine | Listener hub | `NB_HOOK_URL` |
|---|---|---|
| This PC | `127.0.0.1:<acak>` (HTTP, loopback) | `http://127.0.0.1:<port>/nb/hook` |
| SSH | `ssh.Client.Listen("tcp","127.0.0.1:0")` (permintaan `tcpip-forward` di koneksi hub; port dipilih server) | `http://127.0.0.1:<portRemote>/nb/hook` |

- `POST /nb/hook`, body `"<type>;<payload>"` (≤ 16 KiB) → `204`; diproses sama persis dengan marker (token wajib). Bukan bagian `/api`.
- Saat reconnect hub meminta port yang sama dulu; gagal → port baru (sesi tmux lama tetap memakai URL basi → hanya kanal tty).
- `settings.agents.hookChannel`: `auto` (default: tty, lalu HTTP bila forward tersedia), `tty`, `http`. Machine Info mendapat `hookForward: {"port":41234}` atau `{"error":"…"}`.
- **Kanal primer (terverifikasi §20): OSC ke terminal** — `/dev/tty` bila ada, selain itu terminal proses CLI induk. HTTP tetap cadangan (bukan primer) karena kanal terminal induk bekerja tanpa jaringan, juga di SSH dan tmux. Probe selalu menambah `curl` ke daftar `command -v` (`probe.commands.curl`).
- `hookChannel`: `auto` = terminal lalu HTTP; `tty` = tanpa HTTP (hub tidak mengisi `NB_HOOK_URL` dan tidak membuka forward); `http` = hanya HTTP (`NB_HOOK_CH=http`, hanya bila URL tersedia).

### 4.6 tmux

- Mode tmux: perintah `new-session` ditambah `\; set-option -wq allow-passthrough on` (opsi window sesi `nb-<id>` saja; `-q` agar tmux < 3.3 tidak error). Opsi global dan `~/.tmux.conf` tidak disentuh.
- Marker dari dalam pane dibungkus `ESC P tmux; … ESC \` dengan ESC digandakan; tmux meneruskan OSC polos ke klien (hub). Hub hanya perlu mengenali bentuk polos.
- Probe menambah `tmux -V` → `probe.tmuxVersion`.

## 5. Model layar VT (P3)

- Pustaka: **`github.com/charmbracelet/x/vt`** (pure Go). Alasan: dirawat aktif; mendukung alt-screen, scroll region, mode privat (termasuk DECSET 2004 bracketed paste), lebar karakter Unicode/grapheme (TUI Claude/Codex penuh box-drawing dan emoji). `hinshun/vt10x` tidak aktif dirawat dan dukungan lebar Unicode/mode privatnya kurang. Modul `x` belum stabil → pin pseudo-version di `go.mod`, bungkus di paket `internal/screen` dengan antarmuka sempit (`Write`, `Resize`, `Lines`, `Cursor`, `AltScreen`, `BracketedPaste`, `Reset`) supaya bisa diganti.
- Satu model per sesi, ukuran = cols×rows sesi (ikut resize), hanya layar terlihat (tanpa scrollback). Diberi stream yang sudah di-strip, lewat goroutine dengan antrean ≤ 4 MiB; antrean penuh → reset model + `screenStale:true` sampai redraw berikutnya.
- Hub start: replay 256 KiB terakhir scrollback ke model.
- Model **tidak** membalas query terminal (DSR/DA/OSC 10/11) untuk console biasa (xterm.js klien yang membalas). Untuk sesi tersembunyi (Fase 7) model memakai mode `respond` dan membalas.
- Dipakai oleh: aturan layar (§6.4), `promptOnScreen` (guard delivery Fase 6), balasan dari layar untuk agen tanpa transcript, parser usage (Fase 7).

## 6. Engine status (P4, C3, C11)

### 6.1 State

| `agentState` | Arti | Pill |
|---|---|---|
| `starting` | proses/agen sedang diluncurkan | STARTING |
| `working` | agen sedang mengerjakan giliran | WORKING (pulse) |
| `needsYou` | izin, pertanyaan, atau dialog menunggu user | NEEDS YOU |
| `ready` | agen di prompt, menunggu order | IDLE |
| `shell` | tidak ada agen di foreground (console shell, atau agen sudah keluar) | IDLE, atau RUNNING + nama perintah bila ada perintah berjalan |
| `exited` | proses sesi berakhir | DONE |
| `reconnecting` | link SSH putus | RECONNECTING |
| `error` | gagal start / perintah tidak ada | ERROR |

**`active` dan `job`** (ada di sesi dan frame status). `active` berarti console benar-benar sedang memproses, dan itulah yang dihitung penghitung di UI: agen `working`, atau shell dengan perintah di foreground. `needsYou`, `ready`, dan console yang dijeda tidak termasuk. `job` adalah nama perintah itu, kosong saat shell di prompt.

| Console | Sumber | Jeda cek |
|---|---|---|
| Lokal | foreground group terminal (`TIOCGPGRP`) | 2 dtk |
| SSH dengan tmux | `ps -o tpgid` atas pid pane tmux, read-only | 3 dtk selama ada output atau input, selain itu 30 dtk |
| SSH tanpa tmux | hub tidak punya pid-nya, jadi memakai `busy` (output 2 dtk terakhir) | tanpa request |

Yang tidak terhitung: perintah latar belakang (`npm run dev &`, `nohup`), karena shell sudah kembali ke prompt. Sebaliknya program interaktif yang menunggu (`vim`, `less`) terhitung aktif, karena memang proses di foreground.

Field pendamping: `stateSource` (`hooks|notify|screen|activity|process|wrapper`; T14 dilaporkan `activity`), `stateSince`, `activity` (≤ 120 karakter, mis. `Bash: npm test`, `Permission: Write(nb.txt)`; hanya terisi saat `working`/`needsYou`), `needsYouSince` (null bila tidak needsYou).

### 6.2 Prioritas sumber

1. Status proses menang: `error` > `reconnecting` > `exited` > `starting` (dari `status`).
2. `kind:"shell"` atau `foreground:"shell"` → `shell`.
3. Bila `liveStatus:"hooks"` (sudah menerima ≥ 1 marker `hook` di run ini) → hanya event hook yang mengubah state; layar hanya mengisi `promptOnScreen`.
4. Codex (`notify`): `ready` dari notify; `working`/`needsYou` dari layar dan input.
5. Selain itu: aturan layar CLI; tanpa aturan → aktivitas output.

### 6.3 Tabel transisi

| # | Pemicu | Sumber | Dari | Ke | activity |
|---|---|---|---|---|---|
| T1 | marker `agent-start` | wrapper | apa pun | `starting` | — |
| T2 | hook `SessionStart` | hooks | `starting` | `ready` | — |
| T3 | hook `UserPromptSubmit` | hooks | `ready`, `needsYou`, `starting` | `working` | `Thinking` |
| T4 | hook `PreToolUse` (tool selain di T5) | hooks | apa pun kecuali `shell` | `working` | `<tool>: <ringkasan input ≤ 80>` |
| T5 | hook `PreToolUse` tool `AskUserQuestion` / `ExitPlanMode` | hooks | apa pun | `needsYou` | `Question` / `Plan approval` |
| T6 | hook `PostToolUse` | hooks | `needsYou`, `working` | `working` | — |
| T7 | hook `Notification` jenis izin (`notification_type` permission / pesan "needs your permission") | hooks | apa pun | `needsYou` | `Permission: <message>` |
| T8 | hook `Notification` jenis idle | hooks | `working` → `ready`; state lain tetap | — | — |
| T9 | hook `Stop` | hooks | apa pun | `ready` | — (`lastTurn` ditutup, balasan dibaca §8) |
| T10 | hook `SubagentStop` | hooks | — | tetap | `Subagent finished` |
| T11 | hook `SessionEnd` | hooks | apa pun | tetap (menunggu `agent-exit`) | — |
| T12 | marker `agent-exit` | wrapper | apa pun | `shell` | `agentExitCode` = rc |
| T13 | notify `agent-turn-complete` | notify | `working`, `needsYou` | `ready` | — |
| T14 | `\r` dari klien/delivery saat `ready`, sumber bukan hooks | input | `ready` | `working` | — |
| T15 | pola needsYou cocok, stabil 300 ms | screen | `working`, `ready` | `needsYou` | baris yang cocok |
| T16 | pola needsYou hilang | screen | `needsYou` | `working` | — |
| T17 | pola ready cocok dan output diam ≥ 1,5 dtk | screen | `working`, `starting` | `ready` | — |
| T18 | output tercetak dalam 2 dtk terakhir, atau layar memuat petunjuk kerja / diam ≥ 3 dtk di prompt input, ≥ 15 dtk di layar lain | activity | — | `working` / `ready` | — |
| T19 | `starting` > 60 dtk tanpa sinyal ready | timer | `starting` | `ready` (`stateSource:"activity"`) | — |

Bila hooks aktif tapi `Stop` terlewat, T8 menyelamatkan state. `session_id` dan `transcript_path` dari payload hook memperbarui `agentSessionId`/`transcriptPath` (sumber otoritatif, termasuk setelah resume yang membuat id baru).

**Penyesuaian setelah verifikasi (§20):**

- T7: Claude 2.1.312 hanya mengirim `message:"Claude needs your permission"`; `activity` = `Permission: <Tool>(<ringkasan>)` dari `PreToolUse` terakhir (path file → nama file), mis. `Permission: Write(nb.txt)`.
- **T15h** (hooks aktif): pola needsYou layar stabil 300 ms saat `working` → `needsYou` (`stateSource:"screen"`), karena Notification izin baru datang ±6 dtk setelah dialog tampil. Hook berikutnya mengambil alih.
- **T17h** (hooks aktif): `needsYou`, atau `working` yang di-set layar, + prompt kosong (pola ready) + output diam ≥ 1,5 dtk → `ready` (`screen`), karena izin yang ditolak dengan Esc tidak memicu hook apa pun (tanpa `PostToolUse`/`Stop`).
- Pola **working** layar (tidak ada di tabel awal): stabil 300 ms saat `ready`/`starting` → `working` (`screen`); di mode notify hanya bila notify belum pernah datang atau `working` di-set layar. Penanda working stale sesaat setelah notify tidak membuka giliran lagi (penghitung stabil di-reset tiap transisi).
- T15 juga berlaku dari `starting` (dialog trust/update saat start adalah needsYou).
- T13: notify dari thread internal Codex (judul: `last-assistant-message` berupa objek JSON `{"title":…}`) dan notify dengan `thread-id` lain dari percakapan yang sudah dikenal (rollout ditemukan) **diabaikan**.

### 6.4 Aturan layar per CLI

Data di binary (bukan file user), per id `screenRules`:

```jsonc
{ "id": "claude", "scanLines": 14,                       // baris non-kosong terbawah layar terlihat
  "needsYou": [{ "re": "Do you want to (proceed|make this edit|create|run)" }, { "re": "❯\\s*1\\.\\s*Yes" }],
  "working":  [{ "re": "esc to interrupt" }],
  "ready":    [{ "re": "^\\s*[>❯]\\s", "where": "cursorLine" }] }
```

Pola di atas hanya ilustrasi. Pola nyata ada di `internal/agent/rules.go`, diuji terhadap dump layar versi terpasang di `internal/agent/testdata/screens/<cli>-<versi>-<state>[-ket].txt` (baris pertama `cursor=x,y alt=…`; path disanitasi). RE2, dievaluasi ≤ 2×/dtk atau 150 ms setelah output diam; NO-BREAK SPACE dinormalisasi ke spasi sebelum evaluasi (Claude menggambar `❯`+U+00A0). Agen katalog/custom memakai pembacaan generik (`agent.GenericEvaluate`), diuji terhadap dump Claude dan Codex yang sama:

- **needsYou:** penanda dialog TUI di mana saja, `(y/n)` dan sejenisnya hanya di baris kursor, dan pertanyaan seperti `Do you want…` hanya bila kursor tidak di prompt input. Penanda harus diam ≥ 1,5 dtk, supaya prosa balasan yang sedang mengalir tidak memicunya. `\r` dianggap menjawab: teks yang sama tidak dibaca sebagai pertanyaan baru sampai layar berubah.
- **working:** `esc to interrupt`, `esc to cancel`, `ctrl+c to stop` dan sejenisnya di 6 baris terbawah.
- **ready:** diam ≥ 3 dtk bila kursor di prompt input. Di layar lain ≥ 15 dtk, karena agen mungkin berpikir tanpa mencetak apa pun dan `ready` palsu mengakhiri order lebih awal. Hitungan diam juga mulai dari `\r` terakhir.

OpenCode tidak punya aturan state, hanya bentuk prompt input (`┃  …` di baris kursor) dari dump 1.17.15. Saat diam, status 1.17.15 di This PC menjadi `ready` sekitar 7 dtk sesudah start (tanpa provider, tanpa prompt). Giliran sungguhan belum pernah diamati.

| CLI | needsYou | working | ready (baris kursor) |
|---|---|---|---|
| claude 2.1.312 | `Do you want to (proceed\|make this edit\|create\|run\|allow\|use)`, `Do you trust the files…`, `Is this a project you created or one you trust`, `❯ 1. (Yes\|Allow)`, `Enter to confirm · Esc to (cancel\|exit)` | `esc to interrupt`, `^[·✢✳✶✻✽*] <kata>…` (mis. `✽ Elucidating…`; selesai = `✻ Cogitated for 2s · done`) | `^[>❯](\s\|$)` |
| codex 0.163.0 | `Would you like to (run\|make\|apply\|allow)`, `Allow (command\|Codex)`, `Do you trust the contents of this directory`, `› 1. Yes`, `Press enter to confirm or esc to cancel`, `Update available`, `enter continue · esc skip` | `esc to interrupt`, `Working (Ns` (belum tertangkap di dump) | `^›\s`, `^▌\s` |
| gemini | **belum diverifikasi** (tidak terpasang): `Allow execution`, `Apply this change?`, `Do you want to proceed`, `Waiting for user confirmation` | `esc to cancel` | `^│?\s*>\s`, `Type your message` |

### 6.5 Agen di foreground

`foreground`: `agent` | `shell` | `unknown`. Sumber utama marker (`agent-start` → agent, `agent-exit` / mode shell → shell). Cek proses dipakai sebelum §3.3, sebelum setiap delivery (Fase 6), dan tiap 10 dtk saat `shell` di lokal:

| Machine | Cek |
|---|---|
| Lokal | `TIOCGPGRP` pada master PTY → pgid foreground; `== wrapperPid` dan marker terakhir `agent-exit` → shell idle; pgid lain → ada job (`/proc/<pgid>/comm`, macOS `ps -o comm=`) |
| SSH | exec read-only `ps -o tpgid= -p <wrapperPid>` lalu `ps -o comm= -p <tpgid>`; hasil di-cache 3 dtk |

Job di foreground yang nama perintahnya cocok dengan `detect` katalog → `foreground:"agent"`, `manualAgent:true` (agen yang diketik user sendiri; status hanya dari layar/aktivitas, tanpa resume presisi).

## 7. Perubahan Session API

Field baru di `Session` (Info). `status`/`busy` tidak berubah maknanya, jadi klien lama tetap jalan.

```jsonc
{
  "handle": "Ash", "agent": "claude",            // agent = id katalog (sama dengan "preset")
  "agentKind": "agent",
  "agentState": "working", "stateSource": "hooks", "stateSince": "…",
  "activity": "Bash: npm test", "needsYouSince": null,
  "foreground": "agent", "manualAgent": false,
  "liveStatus": "hooks",                         // "hooks" | "notify" | "pending" | "screen" | "off"
  "agentSessionId": "0b5e…-uuid", "agentExitCode": null,
  "yolo": false, "profileId": "",
  "resumeOffer": false,
  "lastTurn": { "startedAt": "…", "endedAt": "…", "durationMs": 81234 },
  "reply": { "at": "…", "chars": 1840, "paragraphs": 4 }   // null bila belum ada; dasar tombol "See output"
}
```

| Method | Path | Request | Response / error |
|---|---|---|---|
| POST | `/api/sessions` | tambahan: `agent` (alias `preset`), `handle?`, `yolo?` (default `settings.agents.yoloDefault`; diabaikan bila agen tanpa yolo), `profileId?` (default `settings.agents.profiles[machine][agent]`), `launch?: {"mode","agentSessionId"?}` (default `fresh`) | `201 Session`. `400 bad_handle`, `409 handle_taken`, `404 profile_not_found`, `400 profile_mismatch` |
| PATCH | `/api/sessions/{id}` | `{"name"?, "handle"?}` | `Session` |
| POST | `/api/sessions/{id}/restart` | §3.3 | `Session` |
| POST | `/api/sessions/{id}/agent` | §3.3 | `Session` |
| GET | `/api/sessions/{id}/reply` | — | `Reply` (§8). `404 no_reply` |
| POST | `/api/sessions/{id}/reply/save` | `{"dir"?: "outputs"}` | `Stat` (tulis `<project>/outputs/<YYYY-MM-DD-HHMM>-<handle>.md` via fs/write `unique:true`; baris pertama komentar HTML berisi tanggal + handle; folder `dir` dibuat bila belum ada — lokal `mkdir`, SSH lewat SFTP — sebagai bagian aksi eksplisit ini; `dir` relatif, tanpa `..`) |
| GET | `/api/sessions/{id}/transcript?last=20` | `last` 1–200 | `{"agentSessionId","messages":[TranscriptMessage…],"truncated":bool}` |
| GET | `/api/sessions/{id}/tokens?since=` | — | `{"items":[TokenUsage…]}` |

Frame status WS terminal diperluas (field lama tetap): `{"t":"status","status":"running","exitCode":0,"busy":true,"agentState":"needsYou","activity":"Permission: Bash(rm -rf dist)","needsYouSince":"…","foreground":"agent"}`. Dikirim saat `agentState`/`foreground` berubah; perubahan `activity` saja dibatasi 1×/dtk.

## 8. Transcript (P5) dan output viewer (C5)

Read-only lewat file API Fase 4 (lokal langsung, SSH via SFTP, fallback exec). File API ditambah parameter **`offset`** (byte ≥ 0) dan **`tail`** (N byte terakhir) pada `GET /api/machines/{id}/fs/read`; tanpa keduanya perilaku lama. Dengan salah satunya respons menambah `"offset"` (byte pertama yang dikembalikan; digeser maju bila jatuh di tengah karakter UTF-8), `truncated` = masih ada byte setelahnya; keduanya sekaligus → `400`.

### 8.1 Lokasi

| Agen | Path | Penemuan |
|---|---|---|
| Claude | `<cfg>/projects/<slug>/<agentSessionId>.jsonl`, `<cfg>` = `$CLAUDE_CONFIG_DIR` profil atau `~/.claude`; `<slug>` = cwd dengan tiap karakter non `[A-Za-z0-9]` diganti `-` (mis. `/home/alice/apps/nextboard` → `-home-alice-apps-nextboard`) | `transcript_path` dari hook bila ada; slug; terakhir glob `<cfg>/projects/*/<id>.jsonl` |
| Codex | `<home>/sessions/YYYY/MM/DD/rollout-*.jsonl`, `<home>` = `$CODEX_HOME` atau `~/.codex` | `agentSessionId` dari notify (`thread-id`) bila cocok; atau dalam 60 dtk setelah `agent-start`: file di folder hari ini (dan kemarin) dengan mtime ≥ start−5 dtk, baris pertama `session_meta.cwd == cwd`, belum diklaim sesi lain. Ambigu → kosong sampai notify |

### 8.2 Model

```jsonc
// Reply
{ "sessionId": "s_…", "agentSessionId": "…", "source": "transcript",   // "transcript" | "screen"
  "text": "## Done\n…", "at": "…", "chars": 1840, "truncated": false, "messageId": "msg_…" }
// TranscriptMessage
{ "id": "…", "role": "user"|"assistant"|"tool", "at": "…", "text": "…", "tool"?: "Bash", "sidechain": false }
// TokenUsage (satu per pesan asisten)
{ "messageId": "…", "at": "…", "model": "…", "input": 1200, "output": 340, "cacheRead": 50000, "cacheWrite": 900 }
// Conversation (pemilih resume)
{ "agentSessionId": "…", "title": "Fix the login redirect…", "startedAt": "…", "updatedAt": "…",
  "messages": 42, "sizeBytes": 183422, "path": "/home/alice/.claude/projects/…/….jsonl", "attachedTo": "s_…"|null }
```

- **Balasan terakhir Claude:** gabungan blok `text` pesan `assistant` (bukan sidechain) yang datang **setelah** `tool_result` terakhir pada giliran terakhir (setelah prompt user terakhir yang berisi teks). Baca `tail` 2 MiB; tidak ketemu → perbesar sampai 8 MiB.
- **Codex:** `response_item` `message` `role:"assistant"` terakhir (`output_text`); token dari `event_msg` `token_count` (`last_token_usage`).
- Token Claude: `message.usage`, **dedupe per `message.id`** (beberapa baris JSONL berbagi id dan usage yang sama).
- Agen tanpa transcript: balasan dari layar = teks 200 baris terakhir scrollback (ANSI dibuang) sejak giliran dimulai; `source:"screen"`.
- Hub menyimpan cache `Reply` terakhir per sesi di memori; diperbarui pada `Stop`/notify/`ready`, lalu `session` event membawa `reply.chars`.

| Method | Path | Request | Response |
|---|---|---|---|
| GET | `/api/machines/{id}/conversations?agent=claude&cwd=&profileId=&limit=50` | `limit` ≤ 200 | `{"items":[Conversation…],"scanned":N,"truncated":bool}` (urut `updatedAt` desc; Claude: file di folder slug; Codex: maks 500 file rollout terbaru, filter cwd). `400 no_transcript` untuk agen tanpa transcript. `Conversation.attachedHandle` (tambahan) = handle console yang memegang percakapan (`null` bila tidak ada), di samping `attachedTo`. `messages` dihitung dari 64 KiB pertama file (batas bawah untuk file besar) |

**Output viewer:** tombol "See output" di header muncul bila `reply.chars ≥ 80` atau `reply.paragraphs ≥ 3` dan state bukan `working`. Isi: render Markdown, Copy, "Save .md" (`reply/save`), dan kolom "Reply to @Ash" yang memakai delivery Fase 6 (`POST /api/sessions/{id}/deliveries`); sebelum Fase 6 tersedia, kolom itu nonaktif.

## 9. Yolo (C8)

- `yolo` tersimpan di Spec; hanya berpengaruh bila agen punya `yoloArgs`/`yoloEnv` (UI menyembunyikan toggle bila tidak). Berlaku saat launch; mengubahnya = restart.
- Default dari `settings.agents.yoloDefault` (default `false`). Runner tersembunyi (Fase 7) **tidak pernah** memakai yolo.
- Badge `YOLO` (warna warning) di header, chip di Android.
- Teks konfirmasi (UI berbahasa Inggris), wajib sebelum mengaktifkan per spawn atau default:
  > **Run without permission prompts?** @Ash (Claude Code) on devbox will edit and delete files and run any command without asking you first. This applies from the next launch. — tombol `Run with Yolo` / `Cancel`.
  Untuk default: "…every new console of an agent that supports it…". Untuk machine SSH ditambah nama host.

## 10. Profil login CLI (keputusan #4)

```jsonc
// Profile
{ "id": "pf_ab12cd", "machineId": "local", "agent": "claude", "name": "Work",
  "dir": "/home/alice/.nextboard/profiles/pf_ab12cd", "createdByHub": true, "createdAt": "…", "exists": true }
```

| Method | Path | Request | Response / error |
|---|---|---|---|
| GET | `/api/profiles?machineId=` | — | `Profile[]` (`exists` dicek via fs/stat) |
| POST | `/api/profiles` | `{machineId, agent, name, dir?, create?: bool}` | `201 Profile`. Remote: `dir` wajib dan harus sudah ada (fs/stat) → `400 profile_dir_missing`; `create:true` di remote → `403 remote_create_forbidden`. Lokal `create:true` → hub membuat `<data>/profiles/<id>/` (0700). `400 profile_unsupported` bila agen tanpa `profileEnv` |
| PATCH | `/api/profiles/{id}` | `{name?}` | `Profile` |
| DELETE | `/api/profiles/{id}` | — | `{"ok":true}`; hanya menghapus referensi. Folder buatan hub di lokal dihapus hanya bila `?purge=1`. Folder remote tidak pernah disentuh |

**Folder state NextBoard (keputusan #10):** hub membuat satu profil `"home": true` per agen bawaan yang punya `profileEnv` di `<data>/agents/<agent>` (0700) dan menjadikannya default **sekali**; default yang dimatikan user tetap mati setelah restart. Console, planner, dan runner lain di This PC memakai profil default ini, jadi login dan memory-nya satu, sedangkan `~/.claude` dan CLI yang dijalankan user sendiri tidak terpengaruh. Folder baru belum login: sign-in dilakukan sekali di console. `DELETE` profil home → `409 profile_home`. Agen custom ber-`base` mengikuti default agen dasarnya.

Ganti agent di console (`restart` dengan `agent`, dialog Switch agent, handoff in-place) juga memakai default agen barunya. `profileId: ""` di `restart` berarti default, kecuali saat kembali ke percakapan lama (`agentSessionId`), yang tetap memakai login tempat percakapan itu dibuat. Login CLI sendiri (`~/.claude`) hanya dipakai bila default-nya dimatikan. Semua pilihan akun di UI menampilkan default-nya sebagai `NextBoard (default)`.

Sesi dengan `profileId` mendapat env `<profileEnv>=<dir>`. "Connect new account" (lokal) = buat profil `create:true` lalu spawn console agen itu dengan profil tersebut; CLI menjalankan login-nya sendiri. Badge header menampilkan inisial nama profil.

## 11. Settings store

`GET /api/settings` → dokumen lengkap (default terisi). `PATCH /api/settings` → JSON Merge Patch (RFC 7386): objek di-merge, `null` = kembali ke default. Key top-level tak dikenal → `400 bad_setting`. Respons = dokumen lengkap, lalu event `settings`. Body ≤ 64 KB.

```jsonc
{
  "schemaVersion": 1,
  "agents": {
    "defaults": { "local": "claude" },          // machineId → agentId
    "paths": { "local": { "claude": "/opt/claude/bin/claude" } },
    "profiles": { "local": { "claude": "pf_ab12cd" } },   // profil default console baru
    "roleAgents": { "frontend": "claude", "backend": "codex", "testers": "gemini", "reviewers": "claude",
                    "design": "claude", "docs": "claude", "security": "codex", "general": "" },  // Fase 6
    "models": { "codex": { "model": "gpt-6.1-sol", "effort": "high" } },  // default console baru, §25
    "roleModels": { "testers": { "model": "gpt-6-astra" } },              // untuk agent role itu, §25
    "yoloDefault": false, "liveStatus": true, "hookChannel": "auto", "resumeOnRestart": "auto"
  },
  "notifications": { "needsYou": true, "finished": true, "error": true, "finishedMinSec": 30 },
  "shortcuts": { "select": "v", "hand": "h", "spawn": "t", "pen": "p", "eraser": "e", "git": "g", "memory": "m",
                 "skills": "s", "loops": "l", "markdown": "d", "needsYou": "n" }
  // "delivery", "commands", "memory": Fase 6 (API-orchestration.md) · "previews", "usage", "runner", "git", "editor": Fase 7 (API-studio.md)
  // Pengaturan suara per klien (localStorage), tidak di hub
}
```

- Shortcut: satu karakter tercetak (huruf kecil, angka, tanda baca), unik antar aksi, bukan `x` (Mission Map, tetap) dan bukan spasi (push-to-talk) → `400 bad_shortcut`.
- Preferensi per klien (tema, ukuran teks, agen armed, viewport) **tidak** di sini; tetap localStorage.
- `liveStatus:false` → tidak ada `--settings`/`-c notify`; state dari layar saja.

## 12. Notifikasi (P13)

```jsonc
{ "type": "attention", "attention": {
  "id": "at_…", "seq": 1042, "kind": "needsYou",            // "needsYou" | "finished" | "error" (Fase 6 menambah "loopHeld" | "loopEnded"; uptime menambah "uptimeDown" | "uptimeUp" | "certExpiring", dengan "monitorId" dan tanpa sessionId, API-uptime.md)
  "sessionId": "s_…", "handle": "Ash", "agent": "claude", "machineName": "devbox",
  "projectId": "p_…", "workspaceId": "w_…",
  "title": "Ash needs you", "body": "Permission: Bash(npm install)", "at": "…" } }
```

| kind | Kapan | Dedupe |
|---|---|---|
| `needsYou` | masuk `needsYou` | tidak diulang untuk sesi yang sama selama belum keluar dari `needsYou` |
| `finished` | `working` → `ready` dengan `lastTurn.durationMs ≥ finishedMinSec` | satu per giliran |
| `error` | masuk `error`, atau `agent-exit` dengan rc ≠ 0 yang bukan akibat kill/restart user | satu per kejadian |

Filter `settings.notifications` diterapkan di hub (event tidak dikirim bila jenisnya mati).

| Method | Path | Response |
|---|---|---|
| GET | `/api/attention?since=<seq>&limit=50` | `{"bootId":"b_…","seq":1042,"items":[Attention…],"needsYou":[{"sessionId","handle","since"}]}`. Ring 200 di memori; `bootId` berubah saat hub restart (klien membuang `since` lama) |

`/api/events` menerima `?topics=attention,session,…` (daftar `type` yang diinginkan; snapshot ikut difilter) supaya service Android hemat data.

- **Android:** opt-in "Notify me when an agent needs me" → foreground service (notifikasi permanen "Watching N agents") yang memegang WS `/api/events?topics=attention,session`, reconnect backoff 1–30 dtk, saat tersambung lagi memanggil `/api/attention?since=`. Notifikasi lokal per sesi (tag = sessionId, diganti saat state berubah, dihapus saat keluar `needsYou`); ketuk → buka terminal sesi itu. Minta pengecualian optimasi baterai secara opt-in. Tanpa FCM / push cloud.
- **Web:** Notification API hanya selama tab terbuka dan `document.hidden`; izin diminta dari tombol di Settings (bukan otomatis). Chip "needs you" + jumlah di chrome atas; tombol `N` (bisa di-rebind) melompat ke sesi `needsYouSince` tertua, lalu berikutnya.
- **Desktop (Tauri):** sama dengan web, memakai notifikasi native Tauri; badge jumlah needs-you di ikon bila didukung OS.

## 13. Ringkasan event `/api/events`

```jsonc
{ "type": "session", "session": Session }               // kini termasuk field §7
{ "type": "attention", "attention": Attention }
{ "type": "settings", "settings": Settings }
{ "type": "agents" }                                    // katalog berubah → GET /api/agents
{ "type": "profile", "profile": Profile }
{ "type": "profileRemoved", "id": "pf_…" }
```

## 14. Persistensi

| File | Isi |
|---|---|
| `presets.json` | `Agent[]` custom (format diperluas; entri lama tanpa field baru tetap valid: `kind` = `agent` kecuali command `$SHELL`, `resume.kind` = `continue` bila `resumeArgs` ada) |
| `settings.json` | §11 (0600, tulis atomik) |
| `profiles.json` | `Profile[]` |
| `profiles/<id>/` | folder profil lokal buatan hub (0700) |
| `sessions.json` | Spec + `handle`, `agent`, `agentKind`, `yolo`, `profileId`, `agentSessionId`, `transcriptPath`, `hookToken`, `lastForeground` |

State engine, cache balasan, dan ring attention hanya di memori.

## 15. Batas dan timeout

| Hal | Nilai |
|---|---|
| Marker | ≤ 16 KiB; ≤ 50/dtk/sesi; raw hook ≤ 3072 byte |
| `activity` | ≤ 120 karakter |
| Baris perintah launch | ≤ 12 KiB |
| Tunggu `agent-start` setelah §3.3 | 10 dtk |
| `starting` tanpa sinyal | 60 dtk (T19) |
| Cek proses SSH | timeout 3 dtk, cache 3 dtk |
| Baca transcript | tail 2 MiB (maks 8 MiB); `Reply.text` ≤ 200 KB (`truncated`) |
| Pemindaian percakapan | ≤ 500 file, ≤ 10 dtk, header 64 KiB per file |
| Hook | `timeout: 5` dtk di config; `curl -m 2` |
| Ring attention | 200 item |

## 16. Keamanan

- Tidak ada penulisan ke `~/.claude/settings.json`, `~/.codex/config.toml`/`hooks.json`, atau file config apa pun, di machine mana pun. Satu-satunya tulisan di remote akibat fase ini: `reply/save` (aksi eksplisit, ke repo) dan opsi tmux per window.
- `hookToken` tidak pernah keluar dari hub ke klien (tidak ada di Info/JSON API, tidak di log). Di remote token sempat terlihat di argumen proses shell launch (`ps`) sesaat; risiko diterima (lihat Open issues).
- Marker hanya mengubah state tampilan; `pid` dari marker hanya dipakai setelah divalidasi (PID ada, satu sesi terminal dengan proses sesi; Fase 6 pause).
- Listener HTTP cadangan: hanya loopback, wajib token, body ≤ 16 KiB, tidak melayani `/api`.
- Profil remote hanya referensi; hub tidak `mkdir` di remote (tes audit exec).
- Yolo hanya per launch dengan konfirmasi; tidak pernah untuk runner tersembunyi.

## 17. UX per klien (yang wajib ada)

- **Web:** header node dengan handle (double-click rename), badge agen/profil, badge YOLO, pill state (+ tooltip `activity`), "See output"; menu agen di header ("Switch agent", "Restart agent", "Resume…", "New conversation"); saat `shell`: bar tipis "Agent exited (code N) — Restart · Resume · Pick conversation"; pemilih percakapan (list `conversations`, tanda "open in @Bo" bila `attachedTo`); flyout spawn (armed agent, ≤ 3 custom, built-in terpasang, Shell, "All agents" dengan grup Detected / Your agents / Not installed); Settings › Agents (default per machine, path override, custom CRUD, yolo default dengan konfirmasi, live status, resume on restart), Settings › Notifications, Settings › Shortcuts (rebind, Esc batal, Delete = default); chip live & needs-you.
- **Android:** daftar sesi dengan pill state, needs-you di atas; chip ringkas di header; output viewer (Markdown, Copy); aksi Restart agent / Resume; toggle notifikasi + foreground service; spawn memilih agen dan profil.
- **Desktop (Tauri):** sama dengan web + notifikasi native.

## 18. Skenario tes wajib

1. Stripper: marker dipotong di setiap offset byte antar-`Read` tetap terdeteksi; marker tidak pernah ada di ring, `scrollback/`, atau frame WS; token salah dibuang tanpa efek; prefiks tanpa terminator dilepas setelah 16 KiB / 2 dtk; OSC lain lolos utuh.
2. Wrapper lokal & SSH (direct dan tmux): `/exit` di Claude → `agentState:"shell"`, prompt shell tampil, `status:"running"`; Ctrl-C di agen tidak membunuh wrapper; `exit` di shell → `exited`.
3. §3.3: berhasil saat shell idle; `409 shell_busy` saat `vim` berjalan; `409 agent_running` saat agen hidup; Claude `fresh` memakai UUID baru, `resume` memakai id lama.
4. Hooks Claude: SessionStart → ready, prompt → working, izin → needsYou dengan activity, Stop → ready + `reply` terisi; checksum `~/.claude/settings.json` sebelum = sesudah (lokal & remote).
5. tmux ≥ 3.3: marker sampai; `tmux show -gv allow-passthrough` tidak berubah; tmux 3.2: perintah tidak gagal.
6. Kanal HTTP: hook dijalankan tanpa controlling TTY (mis. dibungkus `setsid`) → event tetap tiba lewat `NB_HOOK_URL`; token salah → 204 tanpa efek.
7. Codex: notify → ready; `agentSessionId` ditemukan; dua sesi Codex di cwd sama tidak saling klaim; resume picker hanya cwd itu.
8. Fallback layar: Gemini menampilkan konfirmasi → needsYou; selesai → ready; fixture layar per CLI lulus.
9. Hub restart lokal: `auto` → resume presisi; `ask` → shell + `resumeOffer`; `never` → shell.
10. Handle: auto-assign unik lintas machine, sufiks setelah 24, rename bentrok case-insensitive `409`, kata cadangan `400`.
11. Yolo: flag hanya untuk agen dengan `yoloArgs`; custom tanpa yolo tidak pernah dapat flag; badge tampil.
12. Profil: POST remote dengan folder tidak ada → `400` dan log exec tidak berisi `mkdir`.
13. Settings: merge patch, `null` reset, shortcut duplikat `400`, event `settings` ke semua klien.
14. Attention: dedupe needsYou, `finishedMinSec`, catch-up `since`, `bootId` berganti setelah restart; `topics` memfilter.
15. Transcript: fixture JSONL Claude dengan teks–tool_use–teks → balasan = teks akhir; dedupe token per `message.id`; Codex rollout terpotong di tengah baris tidak membuat parser gagal.
16. Kompatibilitas: klien Fase 4 (hanya `status`/`busy`, `/api/presets`) tetap berfungsi.

## 19. Open issues / untuk diverifikasi

Status per butir setelah verifikasi ada di §20; butir 1, 2, 3, 4, 5, 11 terjawab.

1. **Hooks dari `--settings` di mode interaktif** (Claude 2.1.312): pastikan terpanggil, digabung (bukan menggantikan) dengan hooks user, dan tidak terblokir kebijakan `disableAllHooks`/managed settings.
2. **`/dev/tty` dari proses hook** (Claude dan notify Codex): bila hook dijalankan tanpa controlling TTY (setsid/detached), kanal primer pindah ke HTTP. Tentukan primer setelah uji ini.
3. **Codex `-c notify=[…]`**: nama key, bentuk payload (`type`, `thread-id` = id untuk `codex resume`?), apakah ada jenis notify untuk approval. Notify milik user tertimpa; opsi: hub membaca `config.toml` (read-only) dan merangkai argv notify user setelah marker.
4. **Pemetaan `Notification` idle** (T8): keputusan lead memetakan idle → needsYou. Dokumen ini membatasinya (idle hanya menutup giliran yang `Stop`-nya terlewat), karena notifikasi idle muncul setiap kali agen menunggu di prompt dan akan membuat semua console READY menjadi NEEDS YOU. Bila lead tetap ingin literal, ubah baris T8.
5. **tmux passthrough:** perilaku tmux < 3.3 (passthrough selalu aktif?) dan apakah `-wq` cukup bila user menimpa opsi itu di config; marker yang menyela escape milik TUI di dalam tmux.
6. **Reverse forward:** `AllowTcpForwarding` di remote, `curl` mungkin tidak ada, port loopback terlihat user lokal lain (karena itu token wajib), port berganti setelah reconnect.
7. **Token terlihat di `ps`** di remote selama launch (env inline). Mitigasi opsional: kirim lewat `LC_*` (sering ada di `AcceptEnv`) — perlu diuji.
8. **Ctrl-Z** di agen tidak menghentikannya: wrapper memimpin sesi terminal, jadi grup prosesnya yatim dan kernel mengabaikan SIGTSTP (`TestCtrlZDoesNotStopAnAgent`). Program yang menghentikan diri dengan SIGSTOP sungguhan tetap berhenti; pemulihannya `POST …/resume`. Di console shell, job control milik shell dan Ctrl-Z berjalan seperti biasa.
9. **Mengetik ke shell** (§3.3): `NB_WRAP` bisa di-`unset` user, shell non-POSIX untuk `exec`; fallback = restart penuh. Key binding yang mengubah ketikan berakhir dengan `412 wrap_missing`, bukan baris yang termutasi jalan.
10. **Pustaka VT:** stabilitas API `charmbracelet/x/vt`, performa replay 256 KiB, memori per sesi; fallback `hinshun/vt10x` (perlu fork untuk mode 2004).
11. **Slug Claude** untuk karakter titik/garis bawah/non-ASCII dan cwd yang sangat panjang; perilaku `--resume <id>` (id sama atau baru).
12. **`base64` / `head -c`** di target (busybox/macOS) dan format output `base64` macOS (baris panjang).
13. **Flag katalog bertanda †** dan perintah `detect` setiap entri katalog.
14. **Notification API** di browser untuk origin dengan sertifikat self-signed; jenis foreground service Android 14+ (`specialUse` vs `remoteMessaging`) untuk app sideload.

## 20. Hasil verifikasi (This PC, 2026-10-28)

Claude Code **2.1.312**, Codex **0.163.0** (lihat catatan update di bawah), tmux 3.4 dan 3.2a (container Alpine sekali pakai, port 127.0.0.1 saja, dihapus setelahnya). Uji CLI nyata ada di `internal/server/realcli_test.go` (gerbang `NEXTBOARD_REAL_CLI_TEST=1`) dan `internal/jobs` `TestRealCLIRunner`; total 6 prompt kecil ("Reply with the single word OK." dan sejenisnya), Claude dengan `--model haiku`, tanpa yolo/skip-permissions. Folder kerja di scratchpad. Checksum `~/.claude/settings.json` sama sebelum/sesudah setiap run.

| # | Butir | Hasil | Bukti |
|---|---|---|---|
| 1 | Hooks dari `--settings` inline JSON di mode interaktif | **Terkonfirmasi.** Semua event terpakai datang: `SessionStart` (`source` `startup`/`resume`), `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `Notification`, `Stop`, `SubagentStop`, `SessionEnd`. Digabung dengan hooks lain: hook proyek di `.claude/settings.local.json` folder uji dan hooks plugin user (warp, caveman) tetap jalan bersamaan | debug log Claude: `Hooks: SessionStart:startup [set -- hook SessionStart; eval "$NB_HOOK_SH"] finished with status 0`; marker diterima hub |
| 1a | Folder belum di-trust | Dialog trust 2.1.312 **default "No, exit"** (Enter = keluar rc 1); setelah "Yes, I trust" sesi jalan. Dialog terdeteksi needsYou dari layar | fixture `claude-2.1.312-needsYou-trust.txt` |
| 2 | `/dev/tty` dari proses hook | **Ditolak untuk Claude.** Hook berjalan di sesi baru (`sid` = pid sendiri, `tty` = `?`): `cannot create /dev/tty: No such device or address`. Proses induk (`claude`) punya `pts/N`. **Solusi:** kanal kedua ke terminal induk (`/proc/$PPID/fd/0`, cadangan `ps -o tty=`) → marker sampai tanpa jaringan (diuji dengan `hookChannel:"tty"`, hooks → `ready/working/ready` + balasan). Notify Codex juga sampai lewat kanal terminal (`hookChannel:"tty"`). HTTP loopback lokal juga terbukti dengan Claude nyata (`hookChannel:"auto"`, sebelum kanal induk ada) | hook diagnostik di folder uji; `TestHookScriptTTYAndHTTPFallback` (setsid + tty induk + HTTP) |
| 3 | Codex `-c notify=[…]` | **Terkonfirmasi**, key `notify`, argv terakhir = JSON: `{"type":"agent-turn-complete","thread-id","turn-id","cwd","client":"codex-tui","input-messages":[…],"last-assistant-message"}`. `thread-id` = id `codex resume <id>` = UUID di nama `rollout-…-<id>.jsonl` = `session_meta.id`. Tidak ada jenis notify untuk approval (approval hanya dari layar). **Codex juga mengirim notify untuk thread judul internal** (`thread-id` lain, `input-messages` "Generate a concise, single-line task title…", balasan `{"title":"…"}`) — hub mengabaikannya (§6.3) | log marker run Codex |
| 4 | `Notification` idle (T8) | Keputusan lead dipertahankan (idle ≠ needsYou). Terverifikasi: Notification idle datang ±60 dtk setelah `Stop` dan state tetap `ready` | log marker (14:50:23 Stop → 14:51:23 Notification) |
| 4a | `Notification` izin (T7) | `notification_type:"permission_prompt"`, `message:"Claude needs your permission"` (tanpa nama tool), datang ±6 dtk setelah `PreToolUse`. Izin ditolak dengan Esc: **tidak ada hook** (tanpa `PostToolUse`/`Stop`) → penyesuaian T15h/T17h | fixture `claude-2.1.312-needsYou-permission.txt`, `…-ready-after-deny.txt` |
| 5 | tmux passthrough | **Terkonfirmasi.** tmux 3.4: `set-option -wq allow-passthrough on` di window sesi → marker (DCS `tmux;` dibuka tmux) sampai sebagai OSC polos; `tmux show -gv allow-passthrough` tetap `off`; re-attach tidak memutar ulang marker. tmux 3.2a: perintah tidak gagal (`-q`) dan marker tetap sampai (passthrough selalu aktif sebelum 3.3) | `TestSSHTmuxAgentPassthrough` (gerbang `NEXTBOARD_TMUX_SSH`) |
| 6 | Reverse forward | `tcpip-forward` 127.0.0.1:0 dari koneksi hub bekerja (sshd OpenSSH lokal); hook tanpa TTY (setsid) di remote sampai lewat `curl` → forward → hub. `AllowTcpForwarding no` → `hookForward.error`, `NB_HOOK_URL` kosong. `curl` tidak ada di image Alpine uji → `probe.commands.curl` kosong (karena itu kanal terminal induk penting) | `TestSSHWrapperMarkersForwardAndForeground`, `TestSSHForwardRefused` |
| 11 | `--session-id` + path transcript; resume by id | **Terkonfirmasi.** `--session-id <uuid>` dipakai apa adanya; `transcript_path` dari hook = `<cfg>/projects/<slug(cwd)>/<uuid>.jsonl` dengan slug = tiap karakter non `[A-Za-z0-9]` → `-` (cwd di `/tmp/claude-1000/-home-…` → `-tmp-claude-1000--home-…`). `claude --resume <uuid>` **mempertahankan id yang sama** (`SessionStart source:"resume"`, `session_id` sama). Resume id yang belum punya percakapan → rc 1. `codex resume <thread-id>` membuka percakapan yang sama (id sama) | log run nyata; `computed slug path … match=true` |
| 12 | `base64`/`head -c` | GNU coreutils (This PC) dan busybox (Alpine 3.15/3.20) lulus; macOS tidak diuji di sini | uji tmux/SSH |
| — | `/exit` → shell, Ctrl-C tidak membunuh wrapper, §3.3 | `/exit` Claude dan `/quit` Codex → `agent-exit rc 0` → shell di tempat (`status:"running"`); §3.3 resume/fresh dari shell bekerja; `409 shell_busy` saat job di foreground (TIOCGPGRP lokal, `ps` remote) | `TestAgentConsoleHooksEndToEnd`, `TestWrapperSurvivesCtrlC`, run nyata |
| — | Pola layar | Diambil dari dump nyata (tabel §6.4). Claude 2.1.312 memakai alt-screen, prompt `❯`+NBSP, indikator kerja `✽ <Kata>…` (tanpa "esc to interrupt"). Codex 0.163.0: prompt `› Ask Codex to do anything`. Gemini: **tidak dapat diuji** (tidak terpasang) | `TestScreenFixtures` (9 fixture) |
| 13 | Flag katalog † | **Tidak dapat diuji** (CLI tidak terpasang, kecuali `opencode` yang tidak diuji flag-nya); tetap dikirim tanpa yolo/resume | — |
| 8, 9 | Ctrl-Z, mengetik ke shell | Diuji 2026-11-05, lihat butir 8 dan 9 di atas dan §3.3 | — |
| 7, 10, 14 | Token di `ps`, stabilitas pustaka VT, Notification API browser | Tidak diuji (di luar lingkup backend / tidak tersedia). Pustaka VT dipin ke `github.com/charmbracelet/x/vt v0.1.0` (tag rilis, bukan pseudo-version); `Emulator.Close` punya data race dengan `Read` → hub menutup pipe input langsung. Replay 256 KiB < 3 dtk (uji) | `internal/screen` |

**Temuan samping yang perlu diketahui pemilik:**

- **Codex menulis `~/.codex/config.toml` sendiri** saat pertama kali jalan lewat hub: menambah `[tui] animations = false` dan `screen_reader_detection_done = true` (deteksi screen reader; kemungkinan karena query terminal tidak dijawab saat tidak ada klien xterm.js tersambung). Hub tidak menulis file itu; blok tersebut dihapus lagi setelah uji. Trust folder Codex diberikan per-launch dengan `-c 'projects."<cwd>".trust_level="trusted"'` (tanpa menulis config).
- **Dialog update Codex** ("Update available · 0.163.0 → 0.165.0 · 1. Update now") muncul saat start; Enter memilih *Update now*. Dalam uji, input yang dikirim ke console menjalankan updater sehingga Codex di This PC kini **0.165.0** (paket 0.163.0 masih ada di `~/.codex/packages/standalone/releases/`). Karena itu dialog ini kini needsYou di aturan layar, dan delivery Fase 6 wajib menghormati needsYou/`promptOnScreen` sebelum mengetik.
- Claude menyimpan trust folder di `~/.claude.json` (bukan `settings.json`) saat user menyetujui dialog trust.
- Di folder config buatan hub, `.claude.json` diberi `lspRecommendationDisabled: true`. Begitu language server ada di PATH, CLI menawarkan plugin LSP-nya lewat dialog yang menelan ketikan berikutnya: order yang diketik NextBoard ke console terpotong, dan angka di dalamnya bisa memasang plugin. `~/.claude.json` milik user tidak disentuh.

**Penyimpangan lain dari kontrak (implementasi):**

- `NB_HOOK_SH` diganti (kanal terminal induk + `NB_HOOK_CH`), §4.3/§4.5.
- Engine: T15h, T17h, pola working, T7 activity, filter notify Codex (§6.3).
- `Session.activity` hanya terisi saat `working`/`needsYou`.
- `POST /api/sessions/{id}/restart` menerima `agentSessionId`; `Conversation.attachedHandle` (keduanya atas permintaan klien).
- `fs/read` dengan `offset`/`tail` menambah field `offset`.
- `reply/save` membuat folder `dir` bila belum ada.
- `Host.Mkdir` dan `Host.ReadAt` ditambah ke lapisan machine; `machine.Exec` (API-studio §1.1) dipakai runner.
- Runner: lihat API-studio.md §2 (flag yang terverifikasi).

## 21. Agen dan server (keputusan #10, opsi A)

CLI agen selalu berjalan di This PC. Tanpa `settings.agents.remoteTools`, server hanya mendapat shell biasa: `POST /api/sessions` dan `restart {agent}` dengan agen berkind `agent` di machine SSH → `422 agent_this_pc_only`, dan `GET /api/machines/{id}/presets` menandainya tidak tersedia.

**Status: preview, `settings.agents.remoteTools` default `false`** (Settings › Agents, "Agents on servers"). Bila aktif, CLI berikut (dan agen custom ber-`base` CLI itu) bisa bekerja di server; yang lain tetap `422`. Sebuah CLI hanya masuk daftar ini setelah terbukti tool lokalnya bisa dimatikan, karena tanpa itu agen akan mengerjakan file This PC.

| CLI | Tool hub | Tool lokal | Yolo di server | Terverifikasi |
|---|---|---|---|---|
| Claude Code | `--mcp-config "$NB_MCP" --strict-mcp-config` | `--tools Read,Write,Edit,TodoWrite`, tanpa Bash | `--allowedTools` tool hub saja | Console nyata, dengan dan tanpa yolo |
| Codex | `-c mcp_servers.nextboard.*` | `--disable shell_tool --disable unified_exec --sandbox read-only` | `default_tools_approval_mode="approve"` | Console nyata 0.165.0 tanpa yolo; argumen yolo lewat test |
| OpenCode | `OPENCODE_CONFIG_CONTENT` (`mcp`) | `permission` semua tool lokal `deny`, tool hub `ask` | tidak ada | MCP terhubung dan tool lokal terbukti mati (1.17.15); belum sampai menjawab karena provider di mesin uji gagal |

Yolo di server tidak pernah memakai flag lewati-semua milik CLI, sehingga tidak membuka akses ke This PC. Memory project tetap satu (API-orchestration §12): Claude Code lewat folder memory lokal, Codex dan OpenCode lewat `memory_read`/`memory_save`; `read-only` Codex dan `deny` OpenCode menutup tulis lokal lainnya. Instruksi project: `CLAUDE.md` lalu `AGENTS.md` untuk Claude Code, `AGENTS.md` lalu `CLAUDE.md` untuk yang lain.

**Console**
- Sesi: `machineId:"local"`, `remoteMachineId`, `remoteCwd`, dan `cwd` lokal `<data>/agents/remote/<machine>/<hash folder>` yang kosong dan stabil, sehingga memory agen mengikuti folder di server.
- Kalau tool tidak bisa disiapkan, peluncuran gagal, bukan jatuh ke tool lokal.
- Prompt sistem menyebut server dan folder, dan menyertakan instruksi project dari server (≤ 16 KB). Read/Write/Edit lokal Claude Code untuk folder memory; prompt melarangnya untuk file project.
- Dialog trust Codex dilewati per launch dengan `-c projects={…}` untuk folder lokal itu. Dialog trust Claude Code: `<data>/agents/remote` dicatat sebagai `hasTrustDialogAccepted` di `.claude.json` folder config, hanya bila folder itu buatan hub. CLI mewarisinya ke subfolder. `~/.claude.json` tidak pernah ditulis; dengan login default dialog itu muncul sekali per folder, dan opsi default-nya "No, exit".
- Handoff dari console seperti ini tetap bekerja di server dan folder yang sama.

**Job tersembunyi** (`submitRunner`, satu jalur untuk semua job): job untuk project di server berjalan di This PC dengan Claude Code dan login console-nya. Job yang perlu membaca project (`document`, `diagram`, `memory-scan`, `memory-refresh`) memakai `--tools "" --mcp-config … --strict-mcp-config --allowedTools mcp__nextboard__read_file,mcp__nextboard__grep,mcp__nextboard__glob`; yang lain tidak mendapat tool karena semua isinya ada di prompt.

**Endpoint** (di listener hook loopback, MCP JSON lewat POST, `GET` → 405, `Authorization: Bearer ${NB_TOKEN}` yang diekspansi CLI sehingga tidak muncul di argv)
- `POST /nb/mcp/{sid}`: console, token per console. Tool file untuk console yang bekerja di server; `memory_read` dan `memory_save` untuk Codex dan OpenCode, juga di This PC. Claude Code di This PC tidak memakai endpoint ini.
- `POST /nb/mcp-job`: job, token acak per job yang dicabut saat job selesai, hanya tool baca.

**Tool:** `read_file{path,offset?,limit?}`, `write_file{path,content}`, `edit_file{path,old_string,new_string,replace_all?}` (lewat SFTP, edit gagal bila file berubah di tengah jalan), `grep{pattern,path?,include?,ignore_case?}` dan `glob{pattern,path?}` (`rg` bila ada, selain itu `grep`/`find`; glob ≤ 500), `bash{command,timeout_ms?}` (shell login baru di `remoteCwd`, 2 menit, maks 10 menit; `cd` tidak terbawa antar panggilan). Path relatif dari `remoteCwd`. Jawaban ≤ 30.000 karakter (awal + akhir). Izin mengikuti mode CLI: prompt normal per tool.

**Hasil uji (Claude Code 2.1.316, Haiku, sshd sementara di host yang sama, 2026-11-03):** order lewat command bar sampai; edit, `pwd`, dan `uname` terjadi di server; `CLAUDE.md` server dipatuhi; memory tertulis lokal tanpa prompt; status hub (`working` dengan nama tool, `needsYou` per izin) dan laporan thread jalan; memory scan membaca file server lewat tool baca.

Codex 0.165.0 di server yang sama: order lewat command bar sampai, tiga prompt izin MCP terbaca `needsYou`, edit dan `pwd` terjadi di server, `AGENTS.md` server dipatuhi, folder lokal tetap kosong; percobaan langsung lewat `codex exec` menunjukkan shell lokal tidak tersedia dan `apply_patch` lokal ditolak sandbox. Membuka Codex menaikkan penghitung pemberitahuan model di `config.toml` folder config yang dipakai.

**Batas yang diketahui:** skills dan `.claude/commands` di server tidak dimuat; instruksi subfolder tidak dimuat; perintah panjang tanpa keluaran bertahap; git di server memakai kredensial server; OpenCode belum pernah diamati saat menjalankan giliran, dan login-nya masih di folder OpenCode sendiri karena config-nya tersebar di beberapa variabel XDG. Agen katalog lain belum didukung karena belum terpasang di mesin uji.

## 22. Konteks peluncuran

Setiap peluncuran agen di This PC yang CLI-nya menerima konteks (`claude`, `codex`, `opencode`, dan turunannya) mendapat dari hub, tanpa menulis config CLI: instruksi sistem di `<data>/agents/prompts/<sid>.md` (dihapus bersama console), memory project (API-orchestration §12), dan untuk kerja di server, tool hub (§21). Kalau konteks gagal disiapkan, console di This PC tetap jalan dengan catatan di log; console di server gagal, karena tanpa tool itu agen akan bertindak di This PC.

## 23. MCP server di NextBoard

Server MCP diatur sekali di hub (Settings › MCP servers) dan diberikan ke tiap agent saat launch, apa pun CLI-nya, jadi berganti agent tidak mengubah tool yang tersedia. **Tidak ada yang ditulis ke config CLI**, dan server milik pengguna di config CLI tetap ada, kecuali console di server yang memakai `--strict-mcp-config`.

| CLI | Cara menerima | Rahasia |
|---|---|---|
| Claude Code | `--mcp-config "$NB_MCP"` | `${NB_MCP_…}` di header dan `env`, diekspansi CLI |
| Codex | `-c "$NB_CODEX_MCP_n"`: `mcp_servers.<nama>.url` dan `env_http_headers`, atau `command`, `args`, `env_vars` | Codex hanya meneruskan variabel dengan namanya sendiri, jadi `sh` kecil memberi nama aslinya; dua server boleh sama-sama memakai `API_KEY` |
| OpenCode | `OPENCODE_CONFIG_CONTENT`: `mcp.<nama>`, izin `<nama>_*: ask` | `{env:NB_MCP_…}` |
| Agen lain | tidak menerima | — |

**Rahasia.** Nilai header dan environment disegel dengan kunci hub (`secret.Box`) di `<data>/mcp-servers.json`, tidak pernah dikirim balik ke klien (hanya `set: true`), dan keluar dari hub hanya lewat environment proses CLI, tidak pernah lewat argumen. Yang **tidak** dilindungi: agent di This PC yang punya Bash berjalan sebagai user yang sama, jadi bisa membaca environment-nya sendiri dan folder data hub. Ini sama seperti MCP di `~/.claude.json`: jangan beri token yang tidak boleh dilihat agent.

**Izin pakai tool** mengikuti CLI masing-masing; hub tidak menyetujuinya atas nama pengguna.

**Login OAuth** tidak dilakukan hub. Bila server menjawab 401 atau 403, Try memberi tahu: pakai token sebagai header kalau server menerimanya, atau login sekali di tiap CLI lewat perintah `/mcp`.

**Di server (§21).** CLI tetap berjalan di This PC, jadi server berbasis perintah akan bertindak di This PC, bukan di server tujuan. `onServers` default mati untuk perintah dan nyala untuk URL.

Agent yang sedang berjalan mendapat perubahan saat restart berikutnya.

| Method | Path | Request | Response / error |
|---|---|---|---|
| GET | `/api/mcp/servers` | — | `{"items":[Server]}`, nilai disembunyikan |
| POST | `/api/mcp/servers` (admin) | `Server` | `201 Server`; `400 bad_mcp`; `409 mcp_exists`; `409 too_many_mcp` (maksimal 32) |
| PUT | `/api/mcp/servers/{id}` (admin) | `Server` | `Server`. Variabel tanpa `value` mempertahankan nilainya; yang tidak dikirim dihapus |
| DELETE | `/api/mcp/servers/{id}` (admin) | — | `{"ok":true}`; `404 mcp_not_found` |
| POST | `/api/mcp/servers/{id}/test` (admin) | — | `{"ok","tools":["…"],"needsLogin"?,"error"?,"tookMs"}`. Hub berbicara dengan server seperti CLI: `initialize` lalu `tools/list`. Batas 20 detik untuk URL, 45 detik untuk perintah; proses dan anaknya dimatikan setelah selesai |

```jsonc
// Server
{ "id": "tickets",            // ^[a-z][a-z0-9_-]{0,31}$; "nextboard" dicadangkan
  "description"?: "…", "transport": "http" | "stdio",
  "url": "https://…", "headers": [{ "name": "Authorization", "value"?: "tulis saja", "set": true }],
  "command": "npx", "args": ["-y", "…"], "env": [{ "name": "API_KEY", "value"?: "…", "set": true }],
  "projects": [],             // kosong = semua project
  "onServers": true, "enabled": true }
```

## 24. Editor sebagai IDE Claude Code

Extension VS Code Claude Code tidak bisa berjalan di Monaco (kode, webview). Yang dibuat hub adalah sisi lain dari sambungannya: CLI `claude` mencari server MCP bernama `ide`, dan hub menjadi server itu, sehingga console Claude di board memakai card editor sebagai IDE-nya tanpa extension apa pun. Kode: `internal/idelink`, `internal/server/h_ide.go`, `web/src/studio/code/ideLink.ts`, `IdeProposal.tsx`.

**Siapa dapat:** console agent berbasis Claude di This PC dengan project di This PC, pada board, selama `settings.agents.ideLink` (default `true`). Console yang bekerja di server lewat tool hub (§21) belum. Setelan berlaku saat console diluncurkan.

**Sambungan:** tiap console punya server sendiri di `127.0.0.1:<port acak>`, jadi hub tahu console mana yang meminta sesuatu. Env peluncuran: `CLAUDE_CODE_SSE_PORT`, `ENABLE_IDE_INTEGRATION=true`, `CLAUDE_CODE_IDE_SKIP_AUTO_INSTALL=1` (tanpa ini CLI mencoba memasang extension-nya ke IDE yang ia temukan). Lock: `<config CLI>/ide/<port>.lock`, mode `0600` di folder `0700`, berisi `pid`, `workspaceFolders`, `ideName: "NextBoard"`, `transport: "ws"`, `authToken` (32 hex). Dihapus saat console dihapus dan saat hub berhenti; sisa hub yang mati dibersihkan pada penulisan lock berikutnya. Koneksi ditolak bila `X-Claude-Code-Ide-Authorization` salah (`401`) atau ada header `Origin` (`403`): CLI tidak mengirimnya, halaman web selalu mengirimnya. Transport `ws://` polos, sama seperti server milik extension: siapa pun yang bisa menyadap loopback juga bisa membaca lock.

**Browser:** `GET (WebSocket) /api/workspaces/{id}/ide/ws`, cookie dan origin seperti WebSocket lain, `404` untuk workspace yang tidak ada. Satu pesan JSON per text frame.

| Arah | Pesan |
|---|---|
| browser → hub | `{"t":"selection","selection":{text,filePath,fileUrl,selection:{start,end,isEmpty}}}`, `{"t":"tabs","tabs":[…]}`, `{"t":"mention","session","mention":{filePath,lineStart?,lineEnd?}}`, `{"t":"reply","id","result"}` |
| hub → browser | `{"t":"call","id","tool","args","session":{id,name}}`, `{"t":"cancel","id"}`, `{"t":"consoles","items":[{id,name}]}` |

Bila beberapa browser membuka card yang sama, yang terakhir mengirim selection yang menjawab.

**Tool:** `openDiff`, `close_tab`, `closeAllDiffTabs`, `getDiagnostics`, `getCurrentSelection`, `getLatestSelection`, `getOpenEditors`, `getWorkspaceFolders`, `checkDocumentDirty`, `saveDocument`, `openFile`. `executeCode` (Jupyter) tidak ada. Tool selain `openDiff` menunggu browser paling lama 8 detik. Tanpa card editor yang terbuka setiap tool tetap menjawab: `openDiff` dengan error supaya CLI bertanya di terminal, sisanya dengan jawaban kosong.

**Selection:** ikut ke model pada tiap prompt, oleh CLI. Tidak dikirim bila preferensi editor *Share selected text* mati, bila file diabaikan git, atau bila namanya `.env`, `.env.*`, `*.pem`, `*.key`. Selection dipotong di 256 KB.

**Usulan perubahan:** `openDiff` membuka tab `ide:<tab_name>`: kiri isi di disk, kanan usulan yang masih bisa diedit, tiap perubahan punya panah batal di tengah. Accept atau Ctrl+S menjawab `FILE_SAVED` dengan teks akhirnya, dan CLI yang menulis file. Reject atau menutup tab menjawab `DIFF_REJECTED`. Menjawab di terminal membuat CLI memanggil `close_tab`, jadi tabnya tertutup sendiri. Diff hanya muncul bila CLI meminta izin: pada permission mode `auto`, `acceptEdits`, dan yolo CLI menulis langsung.

**Mention:** Alt+K, menu editor, atau palette menaruh `@file#Lx-y` di prompt console; bila ada beberapa console, editor bertanya yang mana.

**Sumber protokol:** halaman *The built-in IDE MCP server* di dokumentasi Claude Code (lock, token, nama tool model), `PROTOCOL.md` claudecode.nvim (bentuk tool internal), dan pengamatan CLI 2.1.317. Bagian internal tidak dijanjikan stabil oleh Anthropic: bila CLI berubah, console tetap jalan dan hanya kehilangan sambungan editornya.

**Verifikasi (This PC, 2026-11-03):** `go test -race ./internal/idelink` (CLI dan browser palsu). CLI asli 2.1.317 lawan server model palsu di hub tes terisolasi, tanpa token: selection sampai ke model, `.env` tidak, Accept, editan tangan lalu Ctrl+S, Reject, jawab di terminal, file baru, `mcp__ide__getDiagnostics` membawa error dari Monaco, Alt+K menjadi `@src/app.ts#L1-2`, card tertutup jatuh ke prompt terminal, lock hilang bersama console. Belum diuji: Claude yang login dengan model sungguhan.

## 25. Model dan effort per console

**Daftar model.** `GET /api/machines/{id}/agents/{agent}/models?profileId=` → `{"models":[{"id","name","description"?,"efforts"?,"defaultEffort"?}],"efforts":[…],"current"?,"currentEffort"?,"source"}`. Agent selalu jalan di This PC, jadi daftarnya dibaca di sana, dari folder config login yang dipakai. `current` dan `currentEffort` adalah yang dipakai CLI bila tidak diberi tahu apa-apa.

| Agent | Sumber (`source`) | Isi |
|---|---|---|
| Codex | `cache`: `models_cache.json` di `CODEX_HOME`; kalau belum ada, `command`: `codex debug models` | Model dengan `visibility: list`, urut `priority`, efforts per model. `current` dari `config.toml` |
| Claude Code | `known`: alias `opus`, `sonnet`, `haiku`, plus `additionalModelOptionsCache` di `.claude.json` | CLI ini tidak punya perintah daftar model. Alias mengikuti model terbaru. Efforts sesuai `claude --help`. `current` dari `settings.json` |
| OpenCode | `command`: `opencode models` (≤ 400 baris) | Tanpa effort |
| Gemini CLI | `none` | Nama model diketik |
| Agent kustom | Sama dengan `base`-nya | |
| Lainnya | `none`, `models: false` di katalog | Flag-nya belum diketahui, jadi tidak ada picker |

Field katalog `models` dan `efforts` menyatakan apakah peluncuran bisa menyebutnya. Picker selalu punya "Other…" untuk nama yang tidak terdaftar. Nama divalidasi (`^[A-Za-z0-9][A-Za-z0-9._:/@\[\]-]{0,127}$`, effort `^[a-z][a-z0-9_-]{0,23}$`); yang lain `400`.

**Flag.** Claude `--model X --effort Y`, Codex `-m X -c model_reasoning_effort=Y`, Gemini dan OpenCode `-m X`. Nilainya masuk per peluncuran lewat `$NB_MODEL` dan `$NB_EFFORT` di depan baris wrapper (§3.2), sehingga agent yang distart ulang dari shell (§3.3) juga membawanya. Tidak ada config CLI yang ditulis.

**Default.** `settings.agents.models[agentId]` dan `settings.agents.roleModels[role]`, masing-masing `{"model"?,"effort"?}`, dihapus dengan `null`. Console baru memakai pilihan role bila agent slot-nya adalah `roleAgents[role]`, lalu pilihan agent, per field. Di `POST /api/sessions`, slot `spawn` dan `deploy`, handoff, dan `restart`: `model`/`effort` tidak dikirim berarti default (di `restart`: yang sedang dipakai console), `""` berarti pilihan CLI sendiri. Form spawn menampilkan default itu sebagai nilai awal dan mengirim pilihannya; opsi "Default" di form berarti pilihan CLI, sedangkan di Settings berarti tidak ada default.

**Bertahan saat restart.** `/model` dan `/effort` di Claude Code menulis default ke `settings.json` folder config, yang dipakai bersama semua console login itu: console A jadi mengikuti pilihan terakhir console B. Maka `Spec.Model` dan `Spec.Effort` milik console, dikirim sebagai flag di setiap peluncuran, termasuk resume, restart hub, dan start dari shell.

Pilihan di dalam CLI diambil dari transcript sebelum setiap peluncuran dan di akhir setiap giliran:

- Claude Code: pasangan `<command-name>/model</command-name>` + `<local-command-stdout>Set model to …`. Argumen perintah dipakai bila ada; kalau dari picker, nama tampilan diubah (`Sonnet 5.5` → `claude-sonnet-5-5`, `Default (…)` → kosong). Nama yang tidak terbaca menunggu `message.model` balasan berikutnya. `/effort` sama, dari `Set effort level to …`.
- Codex: `turn_context` terakhir (`model`, `effort` atau `collaboration_mode.settings.reasoning_effort`).
- Console yang tidak memilih apa pun di-pin ke model balasan terakhirnya, jadi tidak ikut berubah saat default bersama berubah. Fallback otomatis CLI saat limit tidak mengubah model console yang sudah punya pilihan.

Isi transcript sebelum `ModelAt` (kapan hub terakhir diberi tahu pilihan, tersimpan di `sessions.json`, tidak dikirim ke klien) diabaikan, sehingga pilihan lewat dialog tidak ditimpa `/model` lama. Ganti agent mengambil default agent itu; kembali ke percakapan lama mengambil model yang tersimpan di `PastRun`.

**UI.** Rail Spawn (flyout tetap terbuka untuk memilih model, klik board menaruh), ring Spawn agent, form New console, Compose group (satu baris per agent di slot), dan **Agent and model…** di menu console. Bila agent dan login sama, dialog itu me-restart console dengan percakapan yang sama. Header console menampilkan model dan effort-nya. Settings › Agents › **Default models** mengatur default per agent dan per role.

## 26. Agen tidak menandatangani commit (keputusan #33)

Default `settings.agents.coAuthor: false`: commit dari console agen tidak membawa baris `Co-authored-by` atau "Generated with" untuk tool AI, di semua project dan semua CLI. Berlaku untuk console yang dimulai setelah setting berubah. Claude Code yang dijalankan langsung di mesin, di luar NextBoard, tidak tersentuh.

| Lapis | Berlaku untuk | Cara |
|---|---|---|
| Setting CLI | Claude Code | `attribution` kosong dan `includeCoAuthoredBy:false` lewat `--settings` per peluncuran, tidak menulis file. Terbukti lewat prompt yang tertangkap: instruksi `End git commit messages with: Co-Authored-By` hilang |
| Instruksi | CLI yang menerima konteks (Claude Code, Codex, OpenCode) | `NoCoAuthorRule` di system prompt. Mencakup pull request, tapi hanya instruksi |
| Hook git | semua agen di This PC | env `GIT_CONFIG_*` mengarahkan `core.hooksPath` ke `<data>/agents/git-hooks` |

- **Kenapa perlu hook:** Codex 0.165 mengatur atribusinya lewat flag akun/workspace (`commit_attribution_enabled`), dan `commit_attribution` di config tidak terbaca di versi ini. CLI lain di katalog punya caranya sendiri. Hook adalah satu-satunya lapis yang tidak bergantung pada CLI.
- **Hook meneruskan semua hook** ke milik repo (`.git/hooks` atau `core.hooksPath` repo, misalnya husky), dengan argumen, stdin, dan kode keluarnya. `commit-msg` milik repo jalan dulu, baru baris agen dibuang. Hook tidak pernah memanggil dirinya sendiri.
- **Yang dibuang:** `Co-authored-by` dengan alamat vendor, atau nama agen dengan alamat bot; dan baris "Generated with/by <agen>". Nama saja tidak cukup: `Claude Dupont <claude.dupont@acme.com>` tetap ada.
- **Bukan kunci:** `git commit --no-verify`, commit lewat GitHub API, dan console shell biasa melewati hook. Agen yang bekerja di server lewat tool hub hanya kena setting dan instruksi, karena tidak ada yang dipasang di server. Pull request hanya kena setting dan instruksi. Riwayat commit lama tidak diubah.
- Generator commit NextBoard sendiri sudah membuang baris ini (`gitsvc/suggest.go`).
