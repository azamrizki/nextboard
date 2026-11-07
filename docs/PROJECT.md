# NextBoard Hub: dokumen proyek

Gambaran utuh hub NextBoard: tujuan, cara kerja, seluruh fitur, batas sistem, status, dan yang belum selesai. Baca dokumen ini dulu kalau baru masuk ke proyek. Bentuk JSON dan kode error persis ada di `API*.md` (peta di §15).

**Keadaan per 2026-11-05.** Angka di sini dicocokkan ke kode pada tanggal itu. Status "teruji" berasal dari catatan verifikasi di docs; tes tidak dijalankan ulang saat dokumen ini ditulis. Kalau dokumen ini berbeda dengan kode, kode yang benar.

## Daftar isi

1. [Ringkasan](#1-ringkasan)
2. [Tujuan](#2-tujuan)
3. [Konsep dan istilah](#3-konsep-dan-istilah)
4. [Arsitektur](#4-arsitektur)
5. [Fitur](#5-fitur)
6. [Keamanan](#6-keamanan)
7. [Batas sistem](#7-batas-sistem)
8. [Data dan persistensi](#8-data-dan-persistensi)
9. [API dan event](#9-api-dan-event)
10. [Klien](#10-klien)
11. [Pengembangan, test, deploy](#11-pengembangan-test-deploy)
12. [Keputusan pemilik](#12-keputusan-pemilik)
13. [Status dan riwayat](#13-status-dan-riwayat)
14. [Yang belum selesai dan risiko](#14-yang-belum-selesai-dan-risiko)
15. [Peta dokumen](#15-peta-dokumen)

## 1. Ringkasan

NextBoard adalah board visual self-hosted untuk AI coding agent. Satu binary Go (`nextboard`) dipasang sekali di sebuah komputer, berjalan sebagai service, lalu menyajikan Web UI dan API. Dari sana pemilik menjalankan banyak agent CLI (Claude Code, Codex, Gemini CLI, dan lain-lain) sebagai tile terminal di kanvas tak terbatas, melihat status tiap agent, memberi perintah lewat satu command bar, dan mengerjakan git, file, preview web, serta aplikasi desktop dari board yang sama.

Ini proyek open source berlisensi MIT dengan satu pengelola, yang di dokumen ini disebut pemilik. Bukan produk untuk dijual. Tidak ada multi-tenant, billing, atau relay cloud.

| | |
|---|---|
| Versi | `0.2.0` (rilis terakhir; build dev menulis `0.1.0-dev`) |
| Teknologi | Go 1.26, React 19 + TypeScript + Vite |
| Ukuran | 36 paket Go, sekitar 70 ribu baris kode dan 39 ribu baris tes (134 file, 751 fungsi `Test`); sekitar 65 ribu baris TS/TSX; sekitar 280 pola route HTTP |
| Alamat | `127.0.0.1:12312` (HTTP polos, HTTPS diberikan nginx); preview `12320–12339` |
| Penyimpanan | File JSON di `~/.nextboard/`, tanpa database |
| Target build | Linux dan macOS, amd64 dan arm64. Tile aplikasi desktop hanya Linux. Semua uji nyata yang tercatat dilakukan di Linux |
| Klien | Web UI (tertanam di binary). Android dan desktop ada, tetapi ditunda (§10) |

## 2. Tujuan

### Masalah yang diselesaikan

- Terlalu banyak tab terminal dan sesi SSH, sehingga sulit melihat agent mana yang bekerja dan mana yang menunggu input.
- Agent berhenti ketika laptop ditutup.
- Agent tidak bisa dipantau dari HP.
- Agent dan project tersebar di beberapa server tanpa satu tempat untuk melihat semuanya.

### Goal

1. **Pasang sekali.** Satu perintah memasang binary dan service; link setup sekali pakai menyelesaikan sisanya.
2. **Satu board untuk semua agent.** Status `WORKING`, `NEEDS YOU`, `READY` terlihat tanpa membuka tiap terminal.
3. **Agent tetap jalan tanpa klien.** Browser ditutup atau jaringan putus tidak menghentikan kerja; loop tetap berjalan di hub 24 jam.
4. **Pemantauan dari mana saja.** Browser, app desktop, dan Android yang di-pair melihat board yang sama, dengan notifikasi saat agent butuh input.
5. **Mengarahkan tim agent dengan bahasa biasa.** Satu command bar untuk menyuruh, membagi kerja, meneruskan hasil, dan menjadwalkan.
6. **Tidak perlu keluar dari board.** Editor sekelas VS Code/Cursor, git, preview, dan browser sungguhan semuanya tile di board.
7. **Paritas fitur dengan Symvibe**, dalam desain NextBoard sendiri ("Glass", [DESIGN.md](DESIGN.md)), bukan salinan tampilan. Inventaris fitur acuan adalah catatan internal dan tidak ikut di repo ini.

### Bukan tujuan

- Multi-user, multi-tenant, atau layanan yang dibuka ke publik.
- Planner atau TTS berbasis cloud. Planner memakai CLI lokal dengan plan milik pemilik; suara memakai API browser.
- Dari Symvibe yang sengaja tidak diambil: subscription dan credits (F5), instalasi khusus macOS (A1), banner iCloud, Quick Look/Finder, inspector WebKit native (diganti kartu log).

### Prinsip yang mengikat

Aturan ini membentuk banyak keputusan teknis. Melanggarnya butuh keputusan pemilik (§12).

1. **Agentless di server.** Tidak memasang binary atau service dan tidak mengubah file config yang sudah ada di server tujuan. Syaratnya hanya `sshd`.
2. **Agent selalu di This PC.** State CLI (login, memory, setting) ada di folder NextBoard; server dijangkau lewat tool hub.
3. **Tidak ada config CLI yang ditulis.** Hooks, MCP, memory, dan instruksi diberikan per launch lewat flag dan environment. `~/.claude` tidak disentuh.
4. **1 project = 1 memory**, apa pun agent atau CLI-nya.
5. **Teks orkestrasi hanya masuk lewat delivery engine.** Tidak pernah ke shell, tidak pernah ke prompt izin.
6. **Tindakan merusak butuh konfirmasi, dan "hapus" berarti pindah ke trash** bila menyangkut file pengguna.
7. **Hidden runner tidak pernah yolo.** Tool-nya baca-saja atau tidak ada sama sekali.
8. **Hub tidak dibuka ke internet.** Hub pada dasarnya shell ke mesin; HTTPS diakhiri nginx di host yang sama.

### Prioritas sekarang

Sejak 2026-11-02 fokusnya **core backend** (keputusan 8). Android dan desktop ditunda sampai core stabil, dan pemilik yang memutuskan kapan lanjut. Perubahan hub yang berdampak ke klien tetap boleh, asalkan `API*.md` dijaga akurat.

## 3. Konsep dan istilah

| Istilah | Arti |
|---|---|
| **Hub** | Daemon `nextboard`. Menyajikan UI dan API, menyimpan semua state, mengatur sesi. |
| **Device** | Klien yang melihat dan mengontrol board: browser, app desktop, app Android. |
| **Machine** | Komputer tempat proses berjalan. `local` ("This PC") selalu ada; lainnya lewat SSH. |
| **Project** | Folder di sebuah machine, biasanya repo git, boleh folder induk berisi banyak repo. |
| **Workspace** | Satu tab board di dalam project. |
| **Board** | Dokumen per workspace berisi node dan edge, disinkronkan lewat op. |
| **Node / tile** | Satu elemen board. Node adalah datanya, tile tampilannya. |
| **Session / console** | Satu terminal (proses agent atau shell), tampil sebagai node `terminal`. |
| **Agent** | CLI AI di console. Katalog = preset yang diperluas dengan kapabilitas. |
| **Handle** | Nama unik console per hub, seperti `@Ash`. |
| **Order** | Perintah ke satu atau beberapa console, dilacak sampai ada laporan. |
| **Delivery** | Satu pengiriman teks ke satu console, lewat antrean dengan guard. |
| **Thread** | Riwayat perintah dan laporan: satu per project, satu per kartu orchestrator. |
| **Planner** | Claude Code lokal yang menerjemahkan kalimat menjadi langkah. |
| **Group** | Tim console di dalam bingkai, dengan nama, peran, dan warna. |
| **Orchestrator card** | Command bar kecil di board, lingkupnya console yang di-wire ke kartu itu. |
| **Lead agent** | Orchestrator sebagai orang: asisten owner dan pemimpin agent, punya nama (default Nara) dan kepribadian. Berbincang dulu; kerja untuk tim menunggu persetujuan. |
| **Runner / job** | CLI headless tersembunyi untuk pekerjaan latar: dokumen, diagram, scan memory, planner. |
| **Preview** | Proxy dev server di machine, satu listener per port. |
| **Critical notes** | Aturan pemilik yang tidak boleh dilanggar agent mana pun. |

## 4. Arsitektur

```
                      ┌────────────────────── HUB (nextboard) ──────────────────────┐
 Browser ──┐          │ Router, auth, CSRF    Project, workspace, board (ops)        │
 Desktop ──┤  nginx   │ Session manager       Status engine, transcript              │      This PC
 Android ──┼─ HTTPS ─►│ Orchestrator          Delivery, planner, loop, memory        │ ───► PTY + agent CLI
           │  :443    │ Git, preview, studio  Job runner, usage, MCP                 │      aplikasi desktop
           └──────────│ WebSocket /api/events, terminal, LSP, DAP, RFB, video, audio │
                      └───────────────┬──────────────────────────────┬───────────────┘
                                      │                              │ SSH (agentless)
                               ~/.nextboard/                         ▼
                               JSON, 0600                     Server: shell, tmux, SFTP,
                                                              git, direct-tcpip
```

Hub memegang semua state. Klien tidak menyimpan apa pun selain preferensi per browser, jadi web, desktop, dan Android mendapat hasil yang sama dari perintah yang sama.

### 4.1 Teknologi

| Bagian | Pilihan |
|---|---|
| Hub | Go, satu binary, frontend lewat `go:embed`, `CGO_ENABLED=0` |
| PTY dan SSH | `creack/pty`, `x/crypto/ssh`, `pkg/sftp`, `kevinburke/ssh_config` |
| Model layar | `charmbracelet/x/vt` di balik paket `internal/screen` |
| Transport | `gorilla/websocket` |
| Auth | argon2id, Ed25519, AES-256-GCM |
| Frontend | React 19, Vite, TypeScript |
| Kanvas | `@xyflow/react`, `roughjs`, `perfect-freehand` |
| Terminal | `@xterm/xterm` (fit, WebGL dengan fallback) |
| Editor | Monaco (workbench), CodeMirror 6 (Markdown, Skills), `vscode-textmate` + Oniguruma |
| Lain-lain | `@novnc/novnc`, `d3-force`, `markdown-it` + `dompurify`, `highlight.js` |

### 4.2 Paket Go (`internal/`)

| Paket | Baris | Tugas |
|---|---:|---|
| `orch` | 11156 | Orkestrasi: parser, planner, step, thread, group, handoff, forward, loop, memory, layout, Mission Map, kontrol board |
| `server` | 9731 | Router, middleware, REST, WebSocket, SPA, tool MCP hub, input tertaut |
| `machine` | 4884 | Interface `Host`; PTY lokal, SSH, SFTP, exec, probe, dial, pause |
| `studio` | 3753 | Code (file, search, LSP, DAP, extension), Documents, Diagrams, Markdown |
| `gui` | 3009 | Aplikasi desktop: display virtual, RFB, video, suara, browser |
| `session` | 2816 | Session, ring scrollback, manager, wrapper agent |
| `gitsvc` | 2623 | Git, multi-repo, branch, trash, publish GitHub |
| `usage` | 2020 | Usage pill, chime, indeks token |
| `agent` | 1660 | Katalog, launch, wrapper, prompt, aturan layar |
| `preview` | 1651 | Proxy preview, bridge, log, deteksi URL, probe port |
| `board` | 1442 | Dokumen board, op, snapshot, arsip |
| `skills` | 1242 | Skills manager, lint, konfirmasi tulis, starter |
| `jobs` | 1219 | Model job, antrean, hidden runner |
| `transcript` | 1213 | Pembaca transcript, balasan terakhir, token |
| `cli` | 880 | Subcommand, service systemd/launchd, pair, update |
| `auth` | 750 | Password, cookie, setup code, pairing, challenge device, rate limit |
| `delivery` | 690 | Delivery engine |
| `mcpreg` | 681 | Registri MCP server dan probe-nya |
| `agentstate` | 673 | Engine status (`agentState`) |
| `settings` | 479 | Store pengaturan dan validasi |
| `project` | 367 | Project dan workspace |
| `screen` | 338 | Pembungkus model layar VT |
| `marker` | 335 | Stripper marker OSC |
| `connlog` | 298 | Log koneksi |
| `profiles` | 237 | Profil login CLI, folder state agent |
| `store`, `secret`, `config`, `webui`, `version` | 264 | JSON atomik, AES-GCM, config, embed web, metadata build |

### 4.3 Web UI (`web/src/`)

| Folder | Baris TS/TSX | Isi |
|---|---:|---|
| `studio/` | 12807 | Code workbench, Documents, Diagrams, Markdown, Skills, cetak |
| `board/` | 10566 | Kanvas, rail, inspector, tile, sinkronisasi op, rute garis |
| `orch/` | 6041 | Command bar, thread, antrean, Mission Map, panel loop, memory, layout, composer |
| `memory/` | 2637 | Halaman galaxy |
| `components/` | 2339 | Komponen dasar Glass |
| `settings/` | 2008 | Panel pengaturan |
| `agents/` | 1905 | Pemilih agent, output viewer, resume, yolo, attention |
| `git/`, `lib/`, `gui/`, `web/`, `machines/`, `pages/`, `usage/`, `home/`, `voice/`, `devices/` | 8470 | Sisanya, sesuai nama folder |

Halaman: `/` (daftar project), `/p/:projectId/w/:workspaceId` (board), `/machines`, `/devices`, `/memory`, `/settings`, `/print/…`, `/setup`, `/login`.

### 4.4 Alur utama

**Agent dan status.** Hub menjalankan wrapper shell yang menjalankan CLI. Hooks Claude dan notify Codex diberikan per launch dan menulis marker `OSC 777;nb;…` ke terminal. Hub membuang marker dari stream sebelum masuk scrollback, lalu engine status menggabungkan marker, aturan layar per CLI, dan aktivitas output menjadi `agentState`. Saat agent keluar, wrapper jatuh ke shell di tempat, sehingga tile tidak mati.

**Perintah ke agent.** Teks masuk `POST /api/projects/{id}/commands` → parser lokal → (opsional) planner → steps → kebijakan konfirmasi → eksekusi → thread. Step `send` membuat order; tiap target mendapat delivery yang menunggu agent siap, mengetik dengan bracketed paste, lalu mengonfirmasi lewat perubahan state. Saat giliran selesai, hub membaca balasan dari transcript dan menulis laporan di thread.

**Board.** Klien mengirim batch op (`put`, `patch`, `del`, `z`, `putEdge`, `delEdge`). Hub menerapkannya secara atomik, menaikkan `rev`, dan menyiarkannya lewat `/api/events`. Konflik diselesaikan last-writer-wins per node. Kalau `rev` melompat, klien memuat ulang board.

**Pekerjaan latar.** Git jaringan, publish, usage, dan runner berjalan sebagai job dengan progres, pembatalan, dan event. `POST /api/machines/{id}/exec` sengaja tidak ada: setiap pemakai exec membangun perintah dari template tetap.

### 4.5 Realtime

| Kanal | Isi |
|---|---|
| `/api/events` | Semua perubahan state dalam JSON; `?topics=` menyaring supaya hemat data |
| `/api/sessions/{id}/ws` | Output terminal biner (frame pertama snapshot scrollback), input, resize, status |
| `/api/projects/{id}/lsp/{server}/ws` | JSON-RPC language server |
| `/api/projects/{id}/dap/{adapter}/ws` | Debug Adapter Protocol |
| `/api/gui/sessions/{id}/ws`, `/video`, `/audio` | RFB, AV1/H.264, PCM |

## 5. Fitur

### 5.1 Instalasi, service, CLI

`install.sh` mendeteksi OS dan arsitektur, mengunduh rilis, memverifikasi SHA-256, memasang ke `~/.local/bin` (atau `/usr/local/bin` dengan `--system`), lalu menjalankan `nextboard install`. Perintah itu membuat `~/.nextboard/` (0700), memasang systemd user service dengan `enable-linger` (Linux) atau LaunchAgent (macOS), dan mencetak link setup sekali pakai yang berlaku 30 menit.

Setup di Web UI: buat password admin (minimal 8 karakter), beri nama hub, cek CLI yang terdeteksi.

| Perintah | Fungsi |
|---|---|
| `serve [--listen] [--dev]` | Foreground, dipakai service |
| `install [--listen] [--public-url]` / `uninstall [--purge]` | Pasang atau lepas service |
| `status` | Service, health, alamat, jumlah sesi, machine, device |
| `logs [-f]` | Log service |
| `connlog [--machine] [-n] [--json]` | Log koneksi, tetap jalan saat hub mati |
| `setup-link` | Link setup baru |
| `reset-password` | Ganti password admin, semua browser logout |
| `pair` | QR dan kode pairing di terminal |
| `update [--repo]` | Unduh rilis, verifikasi checksum, restart service |
| `version`, `help` | |

Semua perintah menerima `--data-dir` (default `~/.nextboard` atau `$NEXTBOARD_HOME`).

### 5.2 Auth dan device

- **Browser:** password admin, cookie `HttpOnly; SameSite=Strict` 30 hari yang diperpanjang saat dipakai. Nama cookie beda per skema: `nb_session` (HTTPS, `Secure`) dan `nb_session_http`.
- **CSRF:** semua request yang mengubah data wajib header `X-NextBoard: 1`. WebSocket berbasis cookie dicek `Origin`-nya.
- **Rate limit:** 5 login gagal per IP per 10 menit.
- **Pairing:** kode `XXXX-XXXX` 5 menit, sekali pakai, sebagai QR `nextboard://pair?…`. App membuat keypair Ed25519 (private key di Keystore/Keychain), lalu login berikutnya lewat challenge-response dan mendapat access token opaque 1 jam.
- **Revoke** langsung memutus semua WebSocket device itu (close code 4401). Token tidak pernah ditaruh di URL, karena reverse proxy mencatat query string.
- **Peran:** `admin` (browser) dan `device`. Sebagian endpoint khusus admin, seperti pairing, kelola device, MCP, dan log koneksi.

### 5.3 Machines

Tambah dari `~/.ssh/config` (cukup pilih `Host`) atau manual. Tidak ada yang dipasang di server.

- **Auth berurutan:** `IdentityFile` atau key default → ssh-agent → key yang di-upload → password. Key dan password yang di-upload dienkripsi dan tidak pernah kembali ke klien. `ProxyJump` didukung.
- **Host key:** dicek ke `~/.ssh/known_hosts`. Host baru minta konfirmasi fingerprint dan disimpan di `~/.nextboard/known_hosts`. Host key yang **berubah ditolak**.
- **Probe:** OS, arsitektur, shell, home, tmux, dan CLI yang ada. Disimpan, dengan tombol Re-probe.
- **Koneksi:** satu TCP per machine, satu channel SSH per terminal, keepalive 15 detik (3 gagal = putus), reconnect 1/2/5/10/30 detik.
- **Persistensi:** `tmux` (proses hidup walau SSH putus atau hub restart, lalu di-attach ulang) atau `direct` (diluncurkan ulang). Default `auto`. *Clean up* membersihkan sesi `nb-*` yatim.
- **File:** SFTP di koneksi yang ada, dengan fallback exec read-only bila SFTP dimatikan.

Setelah keputusan 10, server hanya mendapat console shell biasa, kecuali preview pada §5.7.

### 5.4 Project dan workspace

- Project dibuat dari machine + path; path yang sama tidak membuat duplikat. Menghapus project menghentikan sesinya dan mengarsipkan board, sedangkan file di disk tidak disentuh.
- Workspace adalah tab board. Workspace terakhir tidak bisa dihapus. "Close all boards" di menu project menutup semua tab sekaligus dan menyisakan satu board kosong; yang sedang berjalan di board itu berakhir, dan tile bisa dipulihkan dari arsip.
- Home screen menampilkan project, branch, ahead/behind, dan jumlah perubahan.
- **Banyak repo dalam satu project** (keputusan 12): hub memindai folder project sampai 4 level, klien memilih lewat picker, `?repo=` berlaku di semua endpoint git.

### 5.5 Board

**Interaksi.** Pan dan zoom 20–300 %, marquee, clipboard, duplikat, undo/redo 80 langkah, guide, rail kiri (tools), rail kanan (Code editor, Studios, Memory, Git, Loops, Mission Map, Auto arrange), ring klik kanan di board kosong (Spawn agent, gambar, web, Paste, shape, Compose group, More), tombol satu huruf. Viewport disimpan per klien, bukan di board. Semua card memakai satu susunan header (mark, judul, meta, aksi, tombol display, `…`); tombol display mengubah ukuran card ke Fit tile, tiga monitor, atau lima ponsel. Rinciannya di DESIGN §3.5.

**Tile.**

| Kind | Isi |
|---|---|
| `terminal` | Console; dibuat dan dihapus hub |
| `note`, `text` | Sticky note 6 warna; teks dengan font, ukuran, perataan |
| `checklist`, `kanban`, `calendar` | Daftar centang; kolom dan kartu (due, prioritas); event bulanan |
| `uptime` | Uptime monitoring seperti Uptime Kuma: heartbeat, grafik respons, sertifikat, alert; hub yang mengecek ([API-uptime.md](API-uptime.md)) |
| `credentials` | Password manager seperti Bitwarden, terenkripsi di browser; node hanya menyimpan judul ([API-vault.md](API-vault.md)) |
| `shape`, `line`, `ink` | Bentuk gaya rough.js, garis dan panah, coretan pena |
| `image` | Dari aset hub atau file machine, dengan editor markup |
| `file` | `code` (snapshot), `markdown` (live), `file`, `folder` (≤ 40 file) |
| `group` | Bingkai tim: nama, peran, warna, anggota, input tertaut |
| `orchestrator` | Kartu pengarah dengan thread sendiri |
| `memory` | Tumpukan memory seperti lego: project selalu aktif, blok dari semua project atau project lain ditambahkan; di-wire ke console, grup, orchestrator, atau editor |
| `web`, `weblog`, `link` | Preview atau browser sungguhan; log konsol dan jaringan; kartu tautan |
| `branches` | Branch, ahead/behind, switch, buat, hapus |
| `document` | Dokumen dari studio Documents |
| `editor` | Code workbench, satu per board |
| `app` | Aplikasi desktop This PC |

Kind yang tidak dikenal klien tampil sebagai "Unsupported tile", jadi kind baru tidak memecah klien lama.

**Edge.** `wire` (abu-abu; orchestrator, memory, editor, atau tile berisi → console/grup; isi tile dikirim sekali saat tersambung), `forward` (jingga putus-putus; console/grup → console/grup, tanpa siklus), `preview` (sian; terminal → web), `log` (web → weblog). Rute garis lurus bila tile berhadapan, satu siku bila tidak, dan memutar kalau ada tile di jalurnya. Kabel orchestrator yang bekerja menyala dan dialiri animasi halus.

**Keawetan** (teruji dengan `kill -9` dan file rusak):

- Simpan dengan debounce 2 detik, maksimal tertunda 7 detik; berhenti normal menyimpan semua perubahan.
- `boards/<wid>.json` ditulis atomik dan punya `.prev`. File rusak diarsipkan dengan alasan `unreadable`.
- Snapshot per jam, 24 terbaru. Restore selalu menyimpan versi saat ini lebih dulu, jadi restore bisa dibatalkan.
- Arsip board yang ditutup atau dikosongkan tidak pernah dipangkas.

### 5.6 Console dan agent

**Katalog.**

| Sumber | Isi |
|---|---|
| Built-in | `shell`, `claude`, `codex`, `gemini` |
| Katalog (17) | OpenCode, Aider, Amp, Auggie, Cline, Codebuff, Continue CLI, GitHub Copilot CLI, Crush, Cursor Agent, Droid, Goose, Kimi, Kiro, Pi, Qwen Code, Mistral Vibe |
| Custom | Lewat Settings atau `presets.json`; boleh `base` Claude atau Codex untuk mewarisi kapabilitas |

Flag katalog yang belum diverifikasi pada CLI nyata tidak dikirim. Deteksi lewat `command -v` saat probe, path bisa di-override per machine, dan default agent bisa diatur per machine.

**Handle.** Auto-assign dari `Arkan Bintang Cantika Daffa Elvan Farel Gibran Haikal Ilham Jihan Kayla Luna Maura Naufal Olivia Pandu Rafa Reyhan Salsa Tiara Vania Wildan Yasmin Zahra` (nama Indonesia yang umum sekarang), lalu `Arkan2`, dan seterusnya. Unik per hub, tidak peka huruf besar. Kata cadangan seperti `everyone`, `all`, `hub` ditolak.

**Status.**

| `agentState` | Arti |
|---|---|
| `starting` | Sedang diluncurkan |
| `working` | Mengerjakan giliran, dengan `activity` seperti `Bash: npm test` |
| `needsYou` | Izin, pertanyaan, atau dialog menunggu pengguna |
| `ready` | Di prompt, menunggu order |
| `shell` | Tidak ada agent di foreground |
| `exited`, `reconnecting`, `error` | Proses selesai, link SSH putus, gagal start |

Sumber status berurutan: status proses → foreground → hooks (Claude) atau notify (Codex) → aturan layar per CLI → aktivitas output. Aturan layar diuji terhadap dump layar nyata di `internal/agent/testdata/`.

**Mode peluncuran.** `fresh`, `resume` (percakapan persis), `continue` (terakhir di folder), `pick` (pemilih bawaan CLI), `shell`. Pemilih percakapan menandai percakapan yang sedang dibuka console lain. Hub restart mengikuti `resumeOnRestart`: `auto`, `ask`, atau `never`.

**Lainnya.**

- **Output viewer:** tombol "See output" di console membuka balasan terakhirnya; di bubble thread membuka balasan milik bubble itu. Keduanya dalam Markdown, dengan Copy, Save `.md`, dan kolom balas.
- **Yolo:** per launch, wajib konfirmasi, dengan badge di header. Default mati.
- **Profil login:** beberapa akun per CLI lewat `CLAUDE_CONFIG_DIR` / `CODEX_HOME`. Folder state default `<data>/agents/<agent>` dibuat hub sekali. Folder baru belum login, jadi sign-in dilakukan sekali di console.
- **Konteks peluncuran:** tiap launch mendapat instruksi sistem di `<data>/agents/prompts/<sid>.md`, memory project, critical notes, MCP server, dan tool hub.
- **Terminal panel:** `POST /api/sessions` dengan `panel: true` membuat sesi tanpa node untuk panel bawah workbench. Sesi ini tidak bisa dijangkau order.

### 5.7 Agent yang bekerja di server (preview)

CLI tetap berjalan di This PC, sedangkan tool file dan shell-nya bertindak di server lewat MCP hub. Status **preview**: `settings.agents.remoteTools` default `false`. Tanpa itu, agent di machine SSH ditolak dengan `422 agent_this_pc_only`.

| CLI | Tool lokal dimatikan lewat | Terverifikasi |
|---|---|---|
| Claude Code | `--tools Read,Write,Edit,TodoWrite`, tanpa Bash; lokal hanya untuk folder memory | Console nyata, dengan dan tanpa yolo |
| Codex | `--disable shell_tool --disable unified_exec --sandbox read-only` | Console nyata tanpa yolo |
| OpenCode | `permission` semua tool lokal `deny` | MCP terhubung; belum sampai menjawab karena provider di mesin uji gagal |

- **Tool hub:** `read_file`, `write_file`, `edit_file`, `grep`, `glob`, `bash`, ditambah `memory_search`, `memory_read`, `memory_save`, dan untuk project yang punya credential tertanda `credential_list`, `credential_run`, `credential_result` (API-vault.md).
- **Kalau tool tidak bisa disiapkan, peluncuran gagal**, bukan jatuh ke tool lokal, karena agent akan mengerjakan file This PC.
- **Yolo di server** tidak memakai flag lewati-semua milik CLI, sehingga tidak membuka akses ke This PC.

### 5.8 MCP server

MCP diatur sekali di Settings dan diberikan per launch ke Claude Code, Codex, dan OpenCode, jadi berganti agent tidak mengubah tool yang tersedia. Transport `http` atau `stdio`, maksimal 32 server, bisa dibatasi per project. Tombol Try berbicara dengan server seperti CLI (`initialize` lalu `tools/list`).

Rahasia disegel di `mcp-servers.json`, tidak pernah kembali ke klien, dan keluar hanya lewat environment proses. Login OAuth tetap per CLI lewat `/mcp`. Agent yang sedang jalan mendapat perubahan saat restart berikutnya.

### 5.9 Orkestrasi

**Command bar.** Di bawah board (`Ctrl/⌘K`), riwayat ↑/↓, menu `@`. Mencakup semua board dalam project; project lain hanya tercapai lewat handle eksplisit.

**Parser lokal.** Bahasa Inggris, tanpa AI, selalu tersedia:

| Area | Contoh |
|---|---|
| Kirim | `@Ash run the tests`, `tell Bea to …`, `give everyone the same prompt: …` |
| Spawn dan grup | `spawn 3 codex reviewers`, `make a group of 4 called Backend`, `add 2 to Backend`, `dissolve Backend` |
| Kontrol | `pause`, `resume`, `restart Ash`, `kill all`, `cancel that` |
| Alur kerja | `hand off to codex`, `when Ash finishes, pass it to Bea`, `every 15 minutes …` |
| Tanya | `status`, `who needs me`, `who finished`, `give me a summary`, `help` |
| Board | `fit`, `go to Ash`, `open git`, `add a kanban`, `open localhost 5173`, `undo` |
| Workspace | `switch to workspace 2`, `new workspace called API`, `deploy Duo` |
| Git dan memory | `commit and push`, `repo app-a push`, `push memory`, `scan memory` |

**@mention.** Urutan resolusi: handle → nama grup → role (`@reviewers`) → agent (`@codex`). Nama yang mirip (Damerau-Levenshtein, Double Metaphone) menimbulkan konfirmasi "did you mean".

**Planner.** `claude -p` lokal di This PC dengan output JSON ber-schema, tanpa tool, batas 2 menit, tidak pernah yolo.

- Input: teks order, 10 pesan thread terakhir, snapshot console, grup, tile (≤ 30 per board), dan memory. Tidak memuat isi file project atau transcript.
- Output: balasan dan maksimal 40 langkah. Output dianggap tidak tepercaya: nama diterjemahkan hub, step di luar daftar dibuang, kebijakan konfirmasi tetap berlaku.
- Planner bisa mengontrol seluruh board: menambah, mengubah, menghapus tile, menghubungkan, membuka file di editor. `workspace.close` sengaja di luar kewenangannya.
- Tanpa planner (Claude tidak ada, belum login, cooldown), parser tetap jalan dan pesan dengan addressee dikirim apa adanya.

**Delivery engine.** Inti keamanan orkestrasi.

| Kondisi console | Aksi |
|---|---|
| Shell, agent sudah keluar | `failed not_agent`; tidak pernah mengetik ke shell |
| `starting`, `needsYou`, `reconnecting`, `paused` | Menunggu, dengan batas waktu masing-masing |
| `working` | Mengantre (default) |
| `ready` | Mengetik |

Teks hanya diketik kalau layar **dikenali positif** sebagai prompt input normal CLI dan sudah diam ≥ 300 ms. Layar dicek ulang tepat sebelum Return, jadi dialog yang muncul di antara paste dan Return membatalkan pengiriman. Input manusia dalam 2 detik terakhir menunda delivery. Karakter kontrol dibuang. 24 fixture dialog nyata dan sintetis dipakai untuk menguji penolakan.

**Konfirmasi.**

| Step | Konfirmasi |
|---|---|
| `kill`, `restart`, `node.delete` | Selalu (`destructive`) |
| `workspace.close` dengan sesi berjalan | Selalu |
| `loop.create` | Selalu, menampilkan jadwal dan estimasi prompt per jam |
| `spawn` > 6 console | `bulkSpawn` |
| Step planner yang tidak diminta: git, web URL, teks berisi pola berisiko (`rm -rf`, `sudo`, `curl \| sh`, `.env`, `--yolo`, force push, `drop table`, …) | `risky` |
| Git yang diketik pengguna, pause/resume, handoff, forward, send verbatim, push memory | Tidak |

TTL 60 detik (30–300). Keputusan pertama menang, dari klien mana pun, dan bisa dijawab dengan mengetik `yes` atau `cancel`.

**Thread dan laporan.** Plan note menunjukkan apa dikirim ke siapa. Setiap target berakhir `finished`, `failed`, `timeout`, atau `skipped`, dengan cuplikan balasan dan tombol "See output". Tanpa selesai 30 menit menghasilkan catatan timeout.

**Grup dan composer.** Peran: `general`, `frontend`, `backend`, `testers`, `reviewers`, `design`, `docs`, `security`, masing-masing bisa punya agent default. Maksimal 12 anggota, dan satu console di satu grup. Spawn batch 1–12 console dalam satu siaran. **Input tertaut** (`syncInput`) menyalin ketikan satu anggota ke anggota lain, termasuk Ctrl+C, tetapi tidak menyalin balasan otomatis emulator.

**Kartu orchestrator.** Command bar kecil yang hanya menjangkau console ter-wire, dengan thread sendiri. Undo dan perintah workspace ditolak di sini. Kartu dipimpin lead agent; nama kartu = nama lead-nya.

**Lead agent.** Di bar, kartu, dan editor, owner berbicara dengan lead (Settings › Lead agent: nama, kepribadian, Talk first). Sapaan dan pertanyaan dijawab sebagai obrolan. Pekerjaan langsung dibagikan ke agent tanpa perlu Go ahead. Hanya rencana yang bisa merusak (menghapus file atau data, reset database, menimpa riwayat git, production) yang menunggu tombol atau kata setuju, dan bisa diubah sambil mengobrol. Order yang menyebut `@agent` dan perintah polos tetap langsung jalan. Lead memakai Claude Code di This PC, jadi tiap giliran obrolan memakai plan owner.

**Mengendalikan dari thread.** Tiap gelembung punya aksi saat disorot: Copy, Reply (balasan ke agent langsung ke agent itu, dengan kutipan), Ask lead, Show console, dan Edit and send again untuk pesan sendiri. Teks bisa dipilih langsung, menu kartu punya Copy thread.

**Mission Map** (`X`). Peta pengarah → grup → console, dengan filter All, Working, Needs you, Idle, Done, jumlah subagent, dan order berjalan.

**Pause dan resume.** SIGSTOP/SIGCONT ke foreground process group, lokal maupun SSH, dengan validasi sesi. Selama paused, delivery menunggu dan loop melewati console itu.

**Handoff.** Ringkasan (tujuan, yang selesai, kondisi, file, langkah berikut, pertanyaan terbuka) lewat runner, lalu dikirim ke console baru. Bila ringkasan gagal, 12 000 karakter terakhir dikirim apa adanya. Bila target tidak siap, hasilnya `manual` dan brief tampil di thread.

**Switch agent di tile yang sama.** Tile, handle, dan folder tetap; memory, critical notes, dan tool ikut. Agent lama dijeda selama ringkasan ditulis. Kembali ke agent sebelumnya melanjutkan percakapan yang sama (maksimal 8 tercatat), dan ringkasan menjelaskan apa yang terjadi selama ia pergi.

**Forward.** Edge permanen (`trigger`: `turn` atau `order`) atau sekali jalan dari perintah. Digest diberi label sumber; `forwardDepth` ≤ 3 dan tanpa siklus untuk mencegah injeksi berantai.

**Loop.** Scheduler di hub, jalan tanpa klien.

| Bagian | Pilihan |
|---|---|
| Pemicu | Interval (60 detik–24 jam), cron 5 field, `agentIdle`, pola `output` |
| Target | Handle, grup, orchestrator, semua agent |
| Berhenti | `maxRuns`, durasi, tanggal, pola output, goal `agentJudged`, goal `command` |
| Template | Status ping, Nudge idle agents, Build fixer, Test guard, Checkpoint commit, Review round, Daily summary |

Target `needsYou` dilewati dan menimbulkan attention `loopHeld`. Run yang terlewat saat hub mati tidak diulang. Goal `command` hanya bisa dibuat pengguna, tidak oleh planner.

**Template layout.** Built-in: Duo, Trio, Quartet, Two quartets, Build & review, Front & back, Review trio, Triage, Preview lab, Docs crew, Solo, Sextet, Three trios, Lead & crew, Full stack, Code & pair, Web builder, Bug hunt, Security audit, Design studio, Release crew, Research desk. Bisa menyimpan susunan sendiri (kind, geometri, agent, grup, wire), tanpa isi kartu dan gambar. Deploy sepenuhnya di hub.

**Notifikasi.** Event `attention`: `needsYou`, `finished` (giliran ≥ `finishedMinSec`), `error`, `loopHeld`, `loopEnded`. Chip jumlah needs-you dan tombol `N` melompat ke yang paling lama menunggu. Web memakai Notification API saat tab tersembunyi; Android memakai foreground service sendiri tanpa FCM.

### 5.10 Memory

Halaman `/memory` berupa galaxy (referensi visual graph Obsidian), dengan daftar berisi hal yang sama yang bisa dipakai lewat keyboard.

| Lapisan | Lokasi | Catatan |
|---|---|---|
| Main memory | `memory/<projectId>/MEMORY.md` | Dipakai panel, planner, semua CLI; ≤ 64 KB |
| Catatan agent | `.md` lain di folder itu | |
| Sub-memory | `memory/subs/<projectId\|global>/<slug>.md` | Halaman per topik, ≤ 64 per main, ≤ 64 KB |
| Critical notes | Sub-memory `critical` | Wajib ada, ≤ 16 KB |
| Global | `memory/global.md` | Berlaku semua project |

- **Critical notes** selalu masuk utuh dan paling depan ke tiap agent, tidak terpotong anggaran memory, dan hanya pemilik yang mengubahnya. Permintaan langsung pun tidak dihitung sebagai izin: agent harus berhenti dan menunggu konfirmasi. **Ini instruksi, bukan kunci teknis**, karena agent di This PC punya Bash sendiri. Teruji nyata dengan Haiku: `npm run db:reset` tidak dijalankan.
- **Cara CLI memakai memory:** Claude Code lewat `autoMemoryDirectory` di `--settings`; Codex dan OpenCode lewat injeksi instruksi (≤ 16 000 karakter) dan tool hub; agent lain lewat push.
- **Pencarian topik:** `memory_search` (BM25 dalam proses, tanpa indeks, ≤ 6 potongan), `memory_read`, `memory_save`. Prompt launch hanya berisi critical notes, main memory, dan daftar halaman; sisanya dicari saat perlu.
- **Init:** job baca-saja yang mengusulkan fakta, sub-memory, dan critical rules dari apa yang bisa merusak project itu. Prompt-nya melarang membuka `.env`. Tidak ada yang tersimpan sampai pemilik menerima, dan menerapkan dua kali tidak menggandakan.
- **Scan, refresh, push:** scan mengusulkan 6–14 fakta menurut effort; refresh mengecek satu fakta masih benar; push mengirim memory lewat delivery (≤ 4000 karakter) dengan label "background only".
- **Memory tile dan Send to agents:** tile adalah tumpukan memory yang berlaku untuk semua agen yang terhubung, lewat launch, tool, dan delivery. Dialog Send memilih memory mana dan agen mana, semua agen project atau yang dipilih, dan menampilkan perkiraan token sebelum mengirim. Hub mencatat fakta yang sudah dimiliki tiap console, jadi yang diketik hanya yang baru, dan tanpa fakta baru tidak ada turn terpakai. Isi memory yang berubah tidak mengetik sendiri (keputusan #37).

### 5.11 Git

Panel Git (`G`), pill di kiri bawah, kartu branch, dan Source Control di workbench. Berjalan di machine project, juga lewat SSH.

- **Status dan diff:** rename, untracked, konflik, ringkasan ahead/behind tanpa fetch otomatis, peringatan file > 100 MiB.
- **Commit:** path terpilih atau semua; hook milik pengguna tetap jalan; tombol sparkle menulis pesan Conventional Commits: CLI membaca diff file yang berubah, atau subjek instan dari nama file bila CLI dimatikan atau gagal; `422 git_identity` bila identitas belum diatur.
- **Push, pull, fetch** sebagai job. Pull hanya `--ff-only`; divergen ditolak dengan pesan "merge in a console". Tidak pernah membuka prompt: `GIT_TERMINAL_PROMPT=0`, `BatchMode`, tanpa askpass, batas 5 menit tanpa output.
- **Discard tidak pernah menghapus:** versi worktree disalin atau dipindah ke `.git/nextboard-trash/<ts>/`, dan restore tidak menimpa pekerjaan.
- **Branch:** buat, switch (`--no-guess`), hapus (`-d`).
- **GitHub lewat `gh`:** login di console biasa, publish `gh repo create`. Token tetap di keyring `gh`, hub tidak membacanya. Publish ditolak kalau sudah ada remote.
- **Multi-repo:** scan read-only (≤ 50 repo, 10 detik), state per repo, command bar menolak bila repo tidak disebut dan ada lebih dari satu. Tidak pernah commit ke semua repo, dan repo baru tidak dibuat di atas repo yang ada.
- **Keamanan:** tidak ada prompt, dan path divalidasi di dalam repo. Perintah baca tidak menjalankan kode dari config repo: `core.fsmonitor`, filter driver milik repo, dan `gpg.program` lewat `log.showSignature` dimatikan (filter butuh git 2.31). Aksi tulis seperti commit tetap menjalankan hook dan filter.

### 5.12 Web tile

Dua jalur dalam satu tile:

**Dev server dan situs tailnet lewat preview proxy.** Satu listener HTTP per preview di rentang 12320–12339; nginx memberi HTTPS di port yang sama. Target hanya loopback machine itu (lokal dial, SSH `direct-tcpip`) atau alamat yang resolve ke IP Tailscale, dicek ulang tiap koneksi (keputusan 19), jadi tidak ada SSRF ke jaringan lain. Auth lewat token sekali pakai 60 detik → cookie per listener 12 jam. Header pemblokir frame dibuang, skrip bridge disuntikkan, WebSocket/HMR transparan.

- **Preview otomatis:** URL localhost di output console, kalau port-nya terbuka, otomatis menjadi tile web dengan edge `preview`.
- **Kartu log:** konsol, error, dan jaringan dari bridge, ring 500 entri per preview.
- **Kontrol tile:** address bar, back/forward/reload, tab, ukuran device, zoom 50–250 %.

**Situs luar lain lewat browser sungguhan** (keputusan 13). Chrome asli di This PC dengan jalur yang sama seperti tile aplikasi, sehingga situs yang melarang iframe, login, unduhan, dan DevTools semuanya jalan. Satu browser dan satu profil; alamat berikutnya jadi tab baru; dua situs berdampingan lewat Split View bawaan Chrome. Mendukung Chrome, Chromium, Brave, Edge, Vivaldi, Opera. Firefox dan paket snap/flatpak belum.

### 5.13 Aplikasi desktop di tile

Aplikasi GUI This PC (Chrome, VS Code, dan lain-lain, dari file `.desktop`) tampil di tile, tidak muncul di layar fisik, dan tidak mengganggu sesi desktop. Maksimal 8 aplikasi bersamaan, satu sesi per aplikasi, dengan profil khusus di `<data>/gui/profiles/<appId>/`. Sesi lepas dari hub: restart hub tidak menutup aplikasi.

| Jalur | Isi |
|---|---|
| Display utama | `sway` headless + `wayvnc` + `wf-recorder`, buffer GPU langsung ke encoder |
| Cadangan tanpa GPU | Xtigervnc + openbox |
| Input, kursor, clipboard, gambar diam | RFB lewat noVNC, lossless |
| Gerakan besar | AV1 atau H.264 VA-API lewat WebCodecs (butuh HTTPS atau localhost) |
| Suara | Sink PipeWire per sesi → PCM 48 kHz |

**Kenapa dua jalur.** Scroll teks 1280×800 pada link 5 Mbps: RFB saja 1,5 fps dengan respons klik 1165 ms; dengan AV1 48 fps, 165 ms, 2,2 Mbps (diukur di Intel Arc A380, display X11). Display Wayland menurunkan CPU dari sekitar 104 % menjadi 50–65 % satu core, dengan harga respons klik sekitar 30 ms lebih lambat saat gerakan besar.

Keamanan dan batasan: §6 dan §14.

### 5.14 Studio

**Hidden runner.** Mode headless CLI (`claude -p`, `codex exec`) dengan tool baca-saja atau tanpa tool, tanpa yolo (dijaga `TestRunnerArgvNeverUnsafe`). Hanya Claude dan Codex. Antrean ≤ 2 per machine, ≤ 4 per hub. Purpose: `document`, `diagram`, `memory-scan`, `memory-init`, `memory-refresh`, `loop-goal`, `planner`, `handoff-summary`, `commit-subject`, `thread-summary`, `code-edit`, `code-complete`, `code-ask`.

**Code workbench.** Node `editor`, satu per board, bukan overlay. Tab dan edit belum tersimpan milik project. Tombol Fill membesarkan card selebar tampilan.

| Area | Kemampuan |
|---|---|
| Editor | Monaco, maksimal 3 grup, draft di IndexedDB, file ≤ 1 MB bisa diedit, 1–10 MB read-only, CRLF/BOM dijaga, konflik simpan lewat `ifMtime` |
| Navigasi | Explorer, Go to file, command palette, panel bawah dengan terminal, status bar |
| Search dan replace | `rg` → `git grep` → `grep`, ≤ 5000 hasil; replace melewati file yang punya edit belum tersimpan |
| Source Control | Status, diff, stage, commit, picker repo |
| Language server | Milik machine project, dijembatani hub (lokal atau SSH). 15 katalog: Go, TypeScript/JS, Python, PHP, Rust, C/C++, Java, Ruby, C#, Kotlin, Swift, YAML, Bash, Lua, Dockerfile. Tidak ada yang diinstal otomatis |
| Debugger | DAP: Delve, debugpy, lldb-dap / gdb, php-debug-adapter; `.vscode/launch.json` dan entri otomatis Go/Python |
| Extension | Open VSX, hanya data: tema, snippet, grammar, definisi bahasa. Kode extension tidak pernah jalan |
| AI | Edit terpilih dengan diff dan Accept; ghost text (mati secara default, karena tiap saran menjalankan CLI); panel Ask yang mengikuti wire card |
| On save | `editor.onSave` menjalankan perintah per pola, 60 detik per perintah |

**Bantuan kerja di editor.** Panel **Review** menampilkan file yang diubah tiap agent, dengan keep dan undo per perubahan; **Local History** menyimpan semua versi file. Keduanya memakai repo bayangan di folder data hub, hanya untuk project di This PC (keputusan 24). Source Control punya graph commit, tab detail commit, stage per perubahan, stash, penyelesaian konflik, dan pull request lewat `gh`. Panel bawah punya **Tasks** (skrip project, lokasi error, kirim kegagalan ke agent) dan **Ports**. Debugger Node.js jalan tanpa unduhan lewat adapter bawaan hub (keputusan 25). Detail: [API-studio §3.2, §3.3, §7.2, §7.5, §7.10–§7.13](API-studio.md).

**Documents.** Tipe `overview`, `onboarding`, `architecture`, `readme`, `api`, `dataModel`, `security`, `testing`, `releaseNotes`, `custom`. Effort `quick`, `balanced`, `thorough` (400–1500 kata, 6–25 menit), dengan median durasi. Streaming, versi, Stop menyimpan versi `incomplete`. Ekspor PDF A4/Letter dan Markdown, atau tambah ke board.

**Diagrams.** Subjek `architecture`, `userFlow`, `dataFlow`, `components`, `requestLifecycle`, `stateFlow`, `custom`. Output JSON `nb.diagram/v1` divalidasi hub (≤ 40 node, 80 edge, 8 grup), lalu mendarat sebagai shape dan garis lepas di board. Ekspor PNG dan Excalidraw. Starter tanpa AI: Flow, Steps, Decision, Mind map.

**Markdown** (`D`). Vault `.md .markdown .mdx` (≤ 2000 file), mode Reading/Source/Graph, wikilink dan link Markdown, backlinks, autosave 1,5 detik, perubahan dari agent muncul ≤ 3 detik, PDF lewat rute cetak.

**Skills** (`S`). Skill dan command Claude di scope personal, project, plugin (read-only). Shadowing, lint 10 aturan tanpa AI, 12 starter, jalankan ke console lewat delivery. Menulis ke folder config di machine SSH memakai protokol konfirmasi: `428` dengan daftar path, UI menampilkan machine dan path, dan versi lama dicadangkan ke hub sebelum ditimpa.

### 5.15 Usage

- **Usage pill:** sesi PTY tersembunyi di `/tmp/nb-usage-<uid>` (dihapus setelah dipakai) mengetik `/usage`, `/status`, atau `/stats`, lalu layarnya di-parse. Hasilnya bar per window, waktu reset, dan layar mentah bila parse gagal. Best-effort, karena format layar berubah antar versi.
- **Default:** pemeriksaan latar mati dan Codex mati, karena efek sampingnya nyata (§14).
- **Chime reset:** bunyi di `resetsAt + 30 detik`, dengan opsi `after100` atau `always`.
- **Grafik token:** 30 hari per agent dari transcript, indeks inkremental hanya membaca byte baru, dedupe per `message.id`. UI menyatakan ini bukan tagihan.

### 5.16 Suara

Sepenuhnya di klien; hub tidak menerima audio. Opt-in dengan pengungkapan bahwa browser bisa mengirim audio ke Google atau Microsoft.

- **Push-to-talk:** tahan Spasi, maksimal 60 detik, hasilnya masuk ke command bar.
- **Dikte:** tahan `AltRight`, ke field yang fokus atau ke console lewat delivery guard `dictation`, maksimal 5 menit.
- **TTS:** hanya kalimat pendek dari hub, tidak pernah balasan agent.

### 5.17 Pengaturan dan shortcut

Di hub (`GET/PATCH /api/settings`, JSON Merge Patch, event `settings`): `agents`, `notifications`, `shortcuts`, `delivery`, `commands`, `memory`, `previews`, `usage`, `runner`, `git`, `editor`. Per browser (localStorage): tema, ukuran teks, agent armed, viewport, suara.

Panel Settings: Agents, Lead agent, Login profiles, MCP servers, Notifications, Shortcuts, Usage, Previews & studios, Voice, Connections, Appearance, Install app (DESIGN §3.17).

**Tema** (per browser, Settings › Appearance dan tombol di header): System, Light, Dark. Tanpa reload, tab lain ikut. Console dan Monaco ikut berganti langsung; console bisa dibuat selalu gelap. Tema warna dari extension yang dipilih di editor tetap dipakai apa pun tema aplikasi. Langit Memory selalu gelap. Sistem desainnya di [DESIGN.md](DESIGN.md).

| Tombol | Aksi | Tombol | Aksi |
|---|---|---|---|
| `V` | Select | `M` | Memory |
| `T` | Spawn | `S` | Skills |
| `P` | Pen | `L` | Loops |
| `E` | Eraser | `D` | Markdown |
| `G` | Git | `N` | Lompat ke needs-you |
| `X` | Mission Map (tetap) | `Space` | Push-to-talk (tetap) |

Tombol selain yang bertanda tetap bisa di-rebind. `Ctrl/⌘K` memfokuskan command bar.

### 5.18 Log koneksi

Selalu aktif, di `<data>/logs/connections.jsonl`, paling banyak sekitar 8 MiB. Mencatat `connected`, `disconnected` (dengan alasan `keepalive_timeout`, `connection_lost`, atau `closed`), `keepalive_miss`, `link_stats` tiap 30 menit, `tool_failed`, `tool_slow`, `hub_unclean_exit`, dan lainnya. Tidak mencatat perintah, output, isi file, atau path.

Dibaca lewat Machines › kartu server › Connection log, `nextboard connlog`, atau `jq`.

### 5.19 Credentials

Password manager di board, mengikuti Bitwarden; kontrak dan model keamanannya di [API-vault.md](API-vault.md). Satu vault per hub, ditampilkan oleh semua kartu `credentials` dan oleh tombol kunci di header (jendela di halaman mana pun, tanpa kartu), dan menghapus kartu tidak menyentuh vault.

- **Enkripsi di browser:** Argon2id (64 MiB, 3 pass) + AES-256-GCM dengan master password yang tidak pernah sampai ke hub. Hub hanya menyimpan ciphertext, karena agent di This PC bisa membaca `<data>`.
- **Isi:** login (TOTP, beberapa alamat, riwayat password), kartu, identitas, catatan, kunci SSH, field custom, folder, favorit, trash 30 hari, cari.
- **Generator** password dan passphrase, salin dengan penghapusan clipboard otomatis, kunci idle (bawaan 15 menit, dihitung dari kerja di kartu saja), import dan export format Bitwarden.
- **PIN 6 digit per browser,** butuh blob di browser dan persetujuan hub, 5 kali salah hangus. Master password minimal 8 karakter.
- **Mengisi form** dari tombol kunci di browser tile, selalu atas pilihan orangnya: web tile lewat bridge dengan cek situs, Chrome di This PC lewat tempel ke field yang fokus.
- **Jalan di HTTP biasa juga,** dengan AES dari `@noble/ciphers` karena `crypto.subtle` tidak ada di sana, dan layar password menyebut risikonya. Satu vault bisa dibuka dari HTTP maupun HTTPS.
- **Agent memakai credential** (keputusan #40): item yang ditandai untuk sebuah project bisa diminta agent project itu lewat `credential_run`. Pemilik menyetujui tiap perintah di browser, hub menjalankannya dan menyensor output, dan agent tidak pernah menerima nilai.

## 6. Keamanan

**Model ancaman.** Satu pemilik. Siapa pun yang bisa login ke hub setara punya shell ke mesin. Karena itu lapisan terpentingnya adalah tidak membuka hub ke publik.

| Lapisan | Perlindungan |
|---|---|
| Jaringan | Default bind loopback, HTTPS di nginx, peringatan bila bind non-loopback. `X-Forwarded-Proto` hanya dipercaya dari peer loopback |
| Browser | argon2id, cookie `HttpOnly` + `SameSite=Strict`, header CSRF, cek Origin WebSocket, rate limit, CSP dengan `font-src 'self'`. WebSocket berakhir bersama loginnya: logout dan reset password memutus terminal yang terbuka |
| Device | Ed25519, challenge sekali pakai 60 detik, token opaque 1 jam, revoke seketika, token tidak di URL |
| Kredensial SSH | AES-256-GCM dengan `secret.key` 0600, tidak pernah ke klien; host key berubah ditolak |
| Credentials | Dienkripsi di browser dengan master password yang tidak pernah ke hub; board, log, dan agent tidak pernah melihat isinya. Agent hanya bisa meminta satu perintah yang disetujui per pemakaian, dan output-nya disensor |
| File API | `<data>` diblokir untuk read/raw/write; `fs/raw` tidak menyajikan HTML sebagai HTML, dengan CSP `sandbox`; aset menolak SVG |
| Exec | Tidak ada exec bebas, argumen di-quote, path setelah `--`, ref divalidasi |
| Marker | Token per sesi 128 bit, tidak pernah ke klien; marker tanpa token valid dibuang |
| Preview | Origin per port, token sekali pakai, target hanya loopback, `/api` tidak tersedia, cookie NextBoard dibuang dari upstream |
| Aplikasi desktop | VNC hanya Unix socket di folder 0700, cookie X11 atau daftar akses per sesi |
| Orkestrasi | Output planner tidak tepercaya, label sumber pada forward, batas kedalaman, pola berisiko |
| Log | Tanpa password, kode, atau isi terminal |

**Yang tidak dilindungi** (sudah dinyatakan ke pemilik):

- Agent di This PC punya Bash sebagai user yang sama, jadi bisa membaca environment-nya, termasuk rahasia MCP, dan folder data hub. Jangan beri token yang tidak boleh dilihat agent.
- Critical notes adalah instruksi, kepatuhan bergantung pada model. Perintah terblokir ditegakkan CLI atau hub, tetapi bukan sandbox (keputusan #38).
- Profil aplikasi desktop memakai `--password-store=basic`, jadi password tersimpan hanya dilindungi izin file `<data>`.
- Browser di tile bisa menjangkau semua yang bisa dijangkau This PC, termasuk layanan localhost dan jaringan lokal.
- Dengan yolo, agent bisa melakukan apa saja tanpa bertanya.
- Credentials: agent bisa membaca `vault.json` dan menebak master password secara offline, dan agent yang bisa menulis ke binary hub atau file web bisa menangkapnya saat unlock. Lupa master password berarti isi vault hilang. Di HTTP biasa, siapa pun yang bisa mengubah lalu lintas bisa menangkap master password. PIN bisa ditebak kalau storage browser dan folder hub ada di tangan yang sama. Mengisi form melepas rahasianya ke halaman itu, dan di Chrome This PC situsnya tidak bisa dipastikan. Perintah yang disetujui untuk agent menjalankan script project apa adanya, dan agent bisa mengubah script itu atau membaca env prosesnya ([API-vault.md](API-vault.md)).

## 7. Batas sistem

| Area | Batas |
|---|---|
| Body request | 64 KB default; board ops 2 MB; aset dan tulis file 20 MB |
| Console | Scrollback 1 MiB; total memori hub sekitar 6 MB per console |
| Spawn | ≤ 12 per panggilan, ≤ 64 per workspace |
| Board | ≤ 500 op per batch, node `data` ≤ 256 KB, board ≤ 8 MB, snapshot 24 |
| Grup | ≤ 12 anggota, nama ≤ 24 karakter |
| Perintah | Teks ≤ 4000 karakter; ke planner hanya ≤ 600; 3 antre per giliran planner |
| Planner | 2 menit; ≤ 40 langkah; ≤ 30 tile per board; cooldown 60 detik setelah 2 gagal |
| Delivery | ≤ 32 000 karakter, ≤ 20 antre per sesi, deadline default 1 jam, konfirmasi 8 detik |
| Tunggu delivery | starting 60 dtk, prompt 120 dtk, reconnect 5 mnt, needsYou 15 mnt, working 30 mnt |
| Konfirmasi | TTL 60 detik (30–300) |
| Order | Laporan timeout 30 menit; simpan 200 |
| Handoff | Ringkas 4 menit; siap 120 + 120 detik |
| Forward | 20 000 per balasan, 32 000 per digest; depth ≤ 3; tiap edge 1× per 60 detik, hasil di dalam menit itu ditahan lalu dikirim bersama |
| Loop | 50 per hub; `maxRuns` 1–500; durasi 15 menit–24 jam; goal tidak dua kali dalam 90 detik |
| Memory | Main dan sub 64 KB; critical 16 KB; 64 sub; push 4000 karakter |
| Job | 100 tersimpan; runner 10 menit dan idle 200 detik |
| Exec | Output ≤ 4 MiB |
| Git | Status ≤ 400 file, diff ≤ 512 KB, log ≤ 100, branch ≤ 200; jaringan 15 menit, idle 5 menit |
| Multi-repo | 4 level, ≤ 50 repo, scan 10 detik |
| Preview | ≤ 20 listener, idle 30 menit, log 500 entri |
| Aplikasi desktop | 8 sesi, 16 aliran video/audio, 320×200 sampai 3840×2160 |
| Code | ≤ 200 000 file; search 5000 hasil, 30 detik; replace 2000 file; 3 grup editor; `onSave` 50 aturan |
| Markdown | 2000 file; 256 KB per file; indeks 20 MB / 20 detik |
| Skills | 400 item; isi 512 KB; import 50 file / 5 MB |
| MCP | 32 server; Try 20 detik (URL), 45 detik (perintah) |
| Attention | Ring 200 |
| Credentials | ≤ 5000 record; satu item ≤ 64 KB base64; vault ≤ 64 MB; master password ≥ 8 karakter; PIN 6 digit, 5 kali salah, ≤ 32 browser |

## 8. Data dan persistensi

Semua di `<data>` (`~/.nextboard/`, 0700). File 0600, tulis atomik (tmp + rename).

```
config.json  auth.json  secret.key  known_hosts  settings.json
devices.json  machines.json  creds/<id>.enc      identitas dan machine
projects.json  sessions.json  scrollback/<id>.log  project dan console
presets.json  profiles.json  profiles/<id>/      agent custom dan profil
agents/<agent>/  agents/prompts/<sid>.md  agents/remote/…   state CLI per agent
boards/<wid>.json(.prev)  snapshots/<wid>/  archive/    board
assets/                                          gambar
threads/<tid>.jsonl  orders.json  forwards.json  orkestrasi
loops.json  loops/<id>.jsonl  layouts.json       loop dan layout
memory/<projectId>/MEMORY.md  memory/global.md  memory/subs/…   memory
mcp-servers.json  extensions/                    MCP dan extension
jobs.json  runner/planner/                       job
vault.json(.prev)  vault.reset-*.json             credentials, hanya ciphertext
credential-log.jsonl(.1)                         perintah agent dengan credential, tanpa nilai
documents/<projectId>/…  diagrams/<projectId>/…  hasil studio
usage/reports.json  usage/tokens/  usage/pending-cleanup.json
gui/profiles/<appId>/  gui/sessions/<id>/        aplikasi desktop
backups/skills/<machineId>/<ts>/                 cadangan skill
logs/connections.jsonl                           log koneksi
```

**Hanya di memori, hilang saat hub restart:** delivery, konfirmasi, handoff, state engine, cache balasan, ring attention, preview dan listener-nya.

**Yang ditulis ke machine target**, hanya atas aksi pengguna: file repo (git, studio, skill project), `.git/nextboard-trash/` atau `.nextboard-trash/`, `<cfg>/skills|commands` dengan konfirmasi, dan folder sementara `/tmp/nb-usage-<uid>` serta `/tmp/nb-run.*` yang selalu dihapus.

## 9. API dan event

Konvensi: JSON camelCase, waktu RFC 3339, error `{"error","code"}`, header `X-NextBoard: 1` untuk request yang mengubah data, cookie atau `Authorization: Bearer`.

| Area | Route | Kontrak |
|---|---:|---|
| `/api/projects/*` (git, commands, loop, memory, file, LSP, DAP, studio) | 61 | Beberapa dokumen di bawah |
| `/api/machines/*` (fs, skills, presets, probe) | 34 | [API.md](API.md), [API-board.md](API-board.md), [API-studio.md](API-studio.md) |
| `/api/sessions/*` | 20 | [API.md](API.md), [API-agents.md](API-agents.md) |
| `/api/workspaces/*` | 11 | [API-board.md](API-board.md), [API-orchestration.md](API-orchestration.md) |
| `/api/gui/*` | 9 | [API-gui.md](API-gui.md) |
| `/api/loops/*` | 8 | [API-orchestration.md](API-orchestration.md) §11 |
| `previews`, `memory`, `documents`, `extensions`, `mcp`, `auth` | 5 per area | Sesuai area |
| Lainnya (`agents`, `profiles`, `layouts`, `usage`, `threads`, `jobs`, `devices`, dan seterusnya) | 1–4 per area | Sesuai area |

Di luar `/api`: `/nb/hook`, `/nb/mcp/{sid}`, `/nb/mcp-job` di listener loopback hook; `/__nb/*` di listener preview.

**Event `/api/events`:**

| Kelompok | `type` |
|---|---|
| Inti | `snapshot`, `session`, `removed`, `machine`, `machineRemoved`, `device`, `deviceRemoved`, `project`, `projectRemoved`, `boardOps` |
| Agent | `attention`, `settings`, `agents`, `profile`, `profileRemoved` |
| Orkestrasi | `delivery`, `thread`, `threadCleared`, `order`, `confirmation`, `command`, `handoff`, `forward`, `loop`, `loopRemoved`, `loopRun`, `memory`, `layouts` |
| Studio | `job`, `jobOutput`, `git`, `gitRepo`, `github`, `preview`, `previewRemoved`, `previewSuggested`, `previewLog`, `document`, `documentRemoved`, `diagram`, `usage`, `usageReset`, `skills`, `fsChanged` |
| Aplikasi | `guiSession`, `guiSessionRemoved` |
| Credentials | `vaultChanged`, `credentialRequest` |

## 10. Klien

| Klien | Lokasi | Status |
|---|---|---|
| Web | `web/`, tertanam di binary | Aktif, mengikuti hub |
| Android | Proyek terpisah, belum dipublikasikan (Kotlin, Compose, tanpa Google Play Services) | **Ditunda.** Mencakup sampai Fase 7, tanpa edit kanvas di HP |
| Desktop | Proyek terpisah, belum dipublikasikan (Tauri v2) | **Ditunda.** Multi-hub, menampilkan SPA hub lewat proxy lokal |

Jangan mengubah, membangun, atau menguji folder Android dan desktop kecuali diminta pemilik.

**Yang perlu dikejar klien saat dilanjutkan:**

- Node `app` dan `editor` tampil sebagai "Unsupported tile" di klien yang belum mengenalnya.
- Picker multi-repo belum ada; tanpa `?repo=` perilaku lama tetap.
- `settings.editor.onSave` belum dipakai Android.
- Fitur sesudah Fase 7 pada umumnya belum ada di Android: memory galaxy, MCP, switch agent.

**Kontrak yang harus tetap stabil,** karena di-parse app desktop: `var HUB = <array JSON>;` di `bridge.js`, entri `<skema>://<host>:<port>` di `frame-src` CSP, objek preview ber-`id` `pv_…` dengan `listenPort`, dan `launchPath` berbentuk `/__nb/launch?t=…`.

## 11. Pengembangan, test, deploy

Butuh Go 1.26+ dan Node 22+. Module Go: `github.com/azamrizki/nextboard`. `go install` membangun hub tanpa UI, karena bundle web tidak ada di repo; halaman utamanya lalu menyuruh memakai `make build`.

| Perintah | Fungsi |
|---|---|
| `make dev` | Hub + Vite; web `:15173`, hub `:12312`, data `./.devdata`; file Go berubah → hub restart lewat air (`RELOAD=0` mematikannya) |
| `make dev-hub`, `make dev-web` | Satu sisi saja |
| `make test` | `go vet` + `go test -race ./...` |
| `make test-web` | Test unit web (Vitest); butuh `web/node_modules` |
| `make test-e2e` | Uji browser (Playwright) terhadap hub sementara dengan agent palsu; butuh `cd web && npx playwright install chromium` sekali. Tidak ikut di workflow rilis |
| `make test-ssh` | Uji SSH terhadap sshd sementara |
| `make build` | Web + `bin/nextboard` |
| `make snapshot` | goreleaser lokal |

`make dev` bind ke `0.0.0.0` secara default, berbeda dengan default rilis `127.0.0.1`. Pakai `LISTEN=127.0.0.1:12312` di jaringan yang tidak dipercaya.

**Hot reload Go (`.air.toml`, `scripts/dev-build.sh`, `scripts/dev-hub.sh`).** Air memantau `cmd/` dan `internal/`, kecuali `_test.go`. Hub hanya restart bila binernya berubah: build gagal, komentar, dan simpanan yang isinya sama tidak menyentuhnya. Restart tetap restart penuh: konsol lokal dihentikan hub lalu dilanjutkan sesuai `agents.resumeOnRestart` (turn yang berjalan terputus), dan delivery, konfirmasi, serta rencana lead yang menunggu hilang (API-orchestration §15). Aplikasi GUI tetap jalan karena hub berjalan di luar pohon proses air. Saat agent di board mengerjakan repo ini sendiri, pakai `make dev RELOAD=0`, supaya simpanan agent tidak memutus turn-nya sendiri.

**Gerbang test.** Default `go test` tidak menyentuh CLI asli atau plan pemilik.

| Variabel | Membuka |
|---|---|
| `NEXTBOARD_SSHD_TEST=1` | Uji SSH dengan sshd sekali pakai (juga jalan saat rilis) |
| `NEXTBOARD_TMUX_SSH`, `_USER`, `_KEY` | Uji mode tmux terhadap sshd dengan tmux |
| `NEXTBOARD_REAL_CLI_TEST=1` | CLI nyata; **memakai plan pemilik** |
| `NEXTBOARD_REAL_ORCH=1`, `NEXTBOARD_REAL_USAGE=1` | Orkestrasi dan usage nyata, bersama gerbang di atas |

**Aturan di host bersama.** Kalau This PC juga menjalankan layanan lain, test ikut memakai memori yang sama, dan `/tmp` bisa punya kuota per user. Helper test rekursif pernah memunculkan ratusan `Xtigervnc` sampai OOM killer menghantam layanan lain. Jadi: test yang memunculkan proses dijalankan satu kasus kecil dulu dengan `-timeout`, proses sisa dihitung sesudahnya, dan test tidak boleh meng-exec dirinya sendiri tanpa guard.

**CI dan rilis (keputusan #36).** Selama pengembangan, CI tidak jalan tiap push atau pull request, jadi test dijalankan sendiri sebelum push. Rilis dipicu manual dari Actions atau `gh workflow run release.yml`, dengan `-f dry_run=true` untuk melihat versi berikutnya tanpa menerbitkan apa pun. Workflow menjalankan `make web`, `make test-web`, `go vet`, `NEXTBOARD_SSHD_TEST=1 go test -race ./...`, dan build goreleaser percobaan. Sesudah semuanya lolos, semantic-release menentukan versi dari pesan commit sejak tag terakhir, membuat tag, lalu goreleaser menerbitkan linux/darwin × amd64/arm64 + `checksums.txt`. Tag yang di-push manual tidak lagi memicu apa pun. Rilis pertama, `v0.1.0`, terbit 2026-11-05 lewat tag manual.

Commit memakai Conventional Commits: `fix:`, `perf:`, `revert:` → patch; `feat:` → minor; `docs:`, `test:`, `ci:`, `chore:`, dan commit lain → tidak merilis. Sebelum 1.0, perubahan breaking (`feat!:`, footer `BREAKING CHANGE:`) juga minor. Aturannya di `.releaserc.json`.

**Deploy.** `deploy/nginx.conf.example` + `deploy/nextboard-proxy.inc`:

- Port utama dan tiap port preview masing-masing diberi HTTPS.
- Port preview di nginx wajib bind ke IP publik, bukan `0.0.0.0`, supaya tidak bentrok dengan hub.
- HTTPS di port selain 443: `nextboard install --public-url https://host:port`.
- `tailscale serve` hanya meneruskan port utama, jadi web tile tidak berfungsi lewat sana.

**Syarat tile desktop (opsional).**

```sh
sudo apt install --no-install-recommends sway wayvnc wf-recorder xwayland   # utama, butuh GPU + ffmpeg VA-API
sudo apt install tigervnc-standalone-server openbox                         # cadangan tanpa GPU
```

## 12. Keputusan pemilik

Sumber: [ROADMAP.md](ROADMAP.md).

| # | Tanggal | Keputusan |
|---|---|---|
| 1 | 10-28 | Status live di SSH lewat hooks per-launch, tanpa menulis config. *Bagian agent remote digantikan #10* |
| 2 | 10-28 | Folder sementara di `/tmp` remote boleh, selalu dibersihkan |
| 3 | 10-28 | Skill di remote boleh ditulis, hanya atas aksi eksplisit dengan konfirmasi path |
| 4 | 10-28 | Profil remote hanya memakai folder yang sudah ada. *Bagian agent remote digantikan #10* |
| 5 | 10-28 | Agent keluar → console jatuh ke shell di tempat |
| 6 | 10-28 | Planner = Claude Code lokal di This PC, tanpa planner cloud |
| 7 | 11-02 | Hub hanya HTTP, HTTPS di nginx |
| 8 | 11-02 | Core backend dulu; Android dan desktop ditunda |
| 9 | 11-03 | Profil aplikasi desktop khusus NextBoard, satu per aplikasi |
| 10 | 11-03 | Agent selalu di This PC, state di folder NextBoard, server lewat tool hub (opsi A) |
| 11 | 11-03 | 1 project = 1 memory, apa pun agent-nya |
| 12 | 11-03 | 1 project boleh banyak repo; tidak pernah commit ke semua repo |
| 13 | 11-03 | Situs luar di web tile memakai browser sungguhan, bukan iframe atau proxy. *Diubah #19 dan #26* |
| 14 | 11-03 | Critical notes wajib; permintaan langsung tidak dihitung sebagai izin |
| 15 | 11-03 | Ganti agent di tile yang sama; percakapan lama bisa dilanjutkan |
| 16 | 11-03 | MCP diatur di NextBoard, diberikan per launch, tidak ditulis ke config CLI |
| 17 | 11-03 | Isi tile dikirim sekali saat dikabelkan, tidak otomatis tiap agent bekerja |
| 18 | 11-03 | File yang dipaste disimpan hub di luar project |
| 19 | 11-03 | Alamat yang resolve ke IP Tailscale tampil lewat proxy hub |
| 20 | 11-03 | Uptime: hub yang mengecek, monitor milik project, kartu hanya menampilkan |
| 21 | 11-03 | Password SSH salah: machine berhenti dan bertanya, tidak mencoba ulang |
| 22 | 11-03 | Mematikan agent: satu switch untuk semua machine, console yang jalan tidak disentuh |
| 23 | 11-04 | Rail kanan hanya yang penting, sisanya di ring klik kanan |
| 24 | 11-04 | Review agent dan Local History lewat repo bayangan di folder data hub |
| 25 | 11-04 | Debugger Node.js buatan sendiri di hub, bukan `js-debug` |
| 26 | 11-04 | Situs publik dicoba lewat proxy hub dulu, Chrome untuk yang tidak bisa |
| 27 | 11-04 | Tema terang dan gelap monokrom, pilihan per browser |
| 28 | 11-04 | Model dan effort disimpan per console, dikirim sebagai flag tiap peluncuran |
| 29 | 11-04 | Papan tugas digerakkan hub dari status order; pembagian otomatis hanya bila dinyalakan |
| 30 | 11-04 | Credentials zero-knowledge seperti Bitwarden, HTTP biasa juga didukung |
| 31 | 11-04 | Master password minimal 8; PIN 6 digit butuh browser dan hub; isi form selalu klik orang |
| 32 | 11-04 | Group project adalah tree sendiri, bukan path di nama |
| 33 | 11-04 | Agent tidak menandatangani commit, berlaku global |
| 34 | 11-05 | Order yang belum diketik saat hub restart ditawarkan untuk dikirim ulang, hub tidak mengirim sendiri |
| 35 | 11-05 | Open source: repo publik, lisensi MIT, data pribadi tidak masuk repo |
| 36 | 11-05 | CI hanya jalan saat rilis; rilis manual, versi ditentukan semantic-release dari pesan commit |
| 40 | 11-06 | Agent memakai credential lewat satu perintah yang disetujui per pemakaian; nilai tidak pernah sampai ke agent |

## 13. Status dan riwayat

| Fase | Isi | Status |
|---|---|---|
| 1–3 | Hub inti, SSH agentless, pairing dan app | Selesai; kriteria SPEC §12 ada di tabel di bawah |
| 4 | Project, workspace, board multi-tile | Selesai: hub, web, Android |
| 5 | Agent, status, notifikasi | Selesai: hub, web, Android |
| 6 | Orkestrasi | Selesai: hub, web, Android |
| 7 | Web, Git, studio, usage, suara | Selesai: hub, web, Android, desktop |
| 8 | Aplikasi desktop di board | Selesai: hub, web |

Sesudah Fase 8, tidak tercatat sebagai fase: agent selalu di This PC dan profil default, agent di server (preview), MCP, switch agent di tile yang sama, memory galaxy dengan critical notes dan Init, multi-repo, browser sungguhan, Code workbench sebagai card dengan LSP, DAP, extension, AI, review agent dan Local History, kontrol board oleh planner, lead agent dan papan tugas, Credentials, uptime, tema terang dan gelap, group project, log koneksi, batas memori hub.

**Belum ada rencana tertulis sesudah ini.** Kandidat pekerjaan berikutnya adalah §14, dan urutannya keputusan pemilik.

### Kriteria selesai fase 1–3 (SPEC §12), dicek 2026-11-05

Nama test ada di `internal/cli`, `internal/server`, dan `internal/machine`. Test di `internal/cli` menjalankan binary asli dengan `HOME` sementara dan `systemctl`, `loginctl`, serta `curl` palsu.

| Kriteria | Status | Bukti dan batasnya |
|---|---|---|
| Pasang lewat `curl … \| sh`, cetak link setup | Teruji | `TestInstallScriptFromReleaseToSetup`, `TestInstallScriptRefusesABadChecksum` dengan rilis palsu dan `systemctl` palsu. Rilis asli dicoba di container Ubuntu 24.04 dengan systemd sungguhan: `install.sh` publik memasang `v0.1.0`, service aktif dan menyala lagi setelah container restart |
| HTTPS di nginx, wizard, login | Teruji | `TestBehindTheNginxExample` memakai file `deploy/` di nginx 1.24 dan 1.28. Halaman wizard hanya dicek bila build punya bundle web. Port preview hanya dimuat, tidak dicoba |
| Reboot, service hidup lagi | Sebagian | Test memastikan `enable-linger` dipanggil. Reboot sungguhan belum pernah dicoba |
| Terminal: output, `vim`, `htop`, Ctrl-C | Teruji | `TestTerminalIsARealOne`: raw mode, alternate screen, Ctrl-C, resize, `vi`, `htop` |
| Browser ditutup dan dibuka | Teruji | `TestTerminalGoesOnWhileTheBrowserIsGone` |
| Restart service: posisi, scrollback, lanjut | Teruji, tanpa systemctl asli | `TestHubProcessComesBackFromSIGTERM` memakai proses dan sinyal asli. Claude kini lanjut dengan `--resume <uuid>`, bukan `--continue` (`TestHubRestartResumeModes`) |
| Tanpa auth 401, link setup sekali pakai | Teruji | `TestEveryRouteRefusesAnonymous` menelusuri semua route dari sumbernya. `TestPublicRoutesAreOnlyThese` mengunci daftar route publik |
| Machine dari `~/.ssh/config` | Teruji | `TestAddMachineFromSSHConfig`, `TestSSHConfig*` |
| Fingerprint host baru, host key berubah | Teruji | `TestSSHAgentlessEndToEnd`, di lapisan machine |
| Probe preset dan tmux | Teruji | `TestProbeFindsEveryCommandInEachShell` di sh, dash, bash, zsh, dengan `PATH` palsu |
| Claude Code di remote | Tergantikan | Keputusan #10: agent selalu di This PC (`TestAgentsStayOnThisPC`) |
| Mode tmux kembali setelah putus | Sebagian, tidak di CI | `TestSSHTmuxPersistence` butuh sshd dengan tmux. Restart hub dalam mode ini belum diuji |
| Mode direct kembali setelah putus | Tergantikan sebagian | Di server tinggal console shell: `TestServerConsoleComesBackAfterADrop`. `resumeArgs` tidak berlaku untuk shell |
| Server tujuan tidak berubah | Dicek sekali, bukan test | Di container, sesudah test SSH hanya `.bash_history` yang berubah, karena test mengetik di shell interaktif. Keputusan #2, #3, dan #18 mengizinkan `/tmp` dan skill atas perintah |
| Pair lewat QR, device terdaftar | Teruji tanpa QR | `TestPairingDeviceAuthAndRevoke`, `TestRevokeCutsADeviceOffAtOnce`. QR-nya sendiri tidak |
| Revoke memutus koneksi | Teruji | `TestRevokeCutsADeviceOffAtOnce`: terminal dan event. GUI, LSP, dan IDE memakai daftar socket yang sama, tanpa test sendiri |
| App menolak sertifikat tak dipercaya | Di luar repo | Klien Android |

### Yang pernah diuji nyata

| Hal | Hasil tercatat |
|---|---|
| Claude Code 2.1.312–2.1.316, hooks, wrapper, delivery | Order diketik, dikonfirmasi 0,46 detik, laporan dari transcript |
| Planner `claude -p` | 4,4 detik, `structured_output` terisi |
| Codex 0.163–0.165 | Notify, resume by id, console di server tanpa yolo |
| tmux 3.4 dan 3.2a, sshd sementara, login shell zsh | Passthrough, attach ulang, git, preview, skill |
| Usage Claude dan Codex | Window ter-parse, folder terhapus |
| Documents | 1 run Haiku, 24,7 detik |
| Critical notes | Haiku menolak `npm run db:reset` |
| Memory tile | Haiku menerima tumpukan dari launch, membaca blok lewat `memory_read`, dan membalas `OK` untuk kiriman 163 karakter; kiriman tanpa fakta baru tidak mengetik apa pun (`TestRealMemoryTile`) |
| Perintah terblokir | Claude Code berhenti di dialog izin, juga lewat `bash -c`, dan skripnya tidak jalan (`TestRealBlockedCommand`). Mode skip permissions tidak dicoba |
| Chrome 156 di Wayland | github.com, Wikipedia, tab, restart hub (lewat Playwright) |
| Video | Intel Arc A380 |
| Workbench | gopls, pyright, typescript-language-server, Vue, Tailwind, ESLint, rust-analyzer, taplo, Delve, debugpy di browser; dua extension Open VSX |
| Diagram | Dua job Claude Code di hub dev, masing-masing sekitar 95 detik |
| Lead agent | Satu giliran model asli, 33 detik |

### Belum pernah diuji nyata

- Gemini CLI (tidak terpasang) dan flag katalog bertanda † di API-agents.
- Jalur AI di workbench dengan model sungguhan (tes memakai CLI palsu).
- Field `danger` milik lead, langkah tugas planner, dan pembagian forward `split` dari model sungguhan.
- `idelink` dengan Claude yang login dan model sungguhan.
- Review agent dengan Codex, dan Codex melewati layar login di panel card.
- Pull request ke GitHub sungguhan (tes memakai `gh` palsu).
- Panel agent di project yang ada di server lewat SSH dengan model sungguhan. Dengan agent palsu dan sshd sungguhan (MaxSessions bawaan), Code card di project server sudah diuji di `e2e/studio-ssh.spec.ts`: tree dengan file ignored, simpan, search, commit, terminal, dan agent di tab.
- OpenCode sampai menjawab di server.
- Log koneksi pada jaringan sungguhan (baru lewat proxy TCP lokal).
- macOS, NVENC, iframe preview dengan sertifikat self-signed.

## 14. Yang belum selesai dan risiko

### Risiko utama

1. **Rilis baru dicoba di container.** `v0.2.0` keluar lewat semantic-release. Di Ubuntu 24.04 dengan systemd sungguhan di dalam container: `install.sh` memasang `v0.1.0`, `nextboard update` menggantinya ke `v0.2.0` dengan data dan login tetap, service kembali setelah container restart, dan uninstall bersih. Mesin sungguhan belum dicoba, dan binary macOS belum pernah dijalankan.
2. **Blokir perintah bukan sandbox** (§6). Critical notes tetap instruksi. Perintah terblokir ditegakkan CLI atau hub, tetapi agent dengan shell masih bisa menulis skrip yang melakukan hal sama. Penegakan di Codex dan OpenCode, serta di Claude Code dengan skip permissions, belum diuji dengan model sungguhan.
3. **Aksi tulis git tetap memakai config repo.** Stage, commit, pull, dan push menjalankan hook, filter, dan `core.sshCommand` dari repo, sama seperti git di terminal. Perintah baca sudah tidak (§5.11), tetapi di git < 2.31 filter driver milik repo masih jalan saat `status` dan `diff`.

### Celah per area

| Area | Celah |
|---|---|
| Workbench | Debug Node belum punya source map dan proses anak. Extension hanya data, karena Monaco tidak punya extension host. Perubahan planner tidak masuk undo klien; pemulihannya lewat snapshot. Review agent dan Local History hanya untuk project di This PC |
| Agent di server | Preview. Skill dan `.claude/commands` di server tidak dimuat; instruksi subfolder tidak dimuat; perintah panjang tanpa keluaran bertahap; git memakai kredensial server. OpenCode belum pernah diamati saat menjalankan giliran; login-nya masih di folder OpenCode sendiri. Agent katalog lain belum didukung |
| Status dan delivery | Agent tanpa aturan layar dibaca secara generik. Bacaan proses CLI (`rchar`) menahan `working` selama model mengirim stream, tetapi CLI yang membaca socket lewat `recv()` (Python, Rust) masih bisa terbaca `ready` saat berpikir tanpa output: sesudah 3 detik di layar prompt, sesudah 15 detik di layar lain. Daftar CLI aman untuk `whileWorking:"type"` belum ada |
| Orkestrasi | Bingkai grup bisa sedikit tidak akurat saat dua klien memindahkan anggota bersamaan. Parser hanya bahasa Inggris |
| Multi-repo | Tanpa ripgrep, Search di folder induk hanya memakai daftar prune |
| Git | Menulis ulang riwayat untuk file > 100 MB yang sudah ter-commit belum dicakup. Di server lewat SSH, commit menunggu proses yang ditinggalkan hook, paling lama 2 menit |
| Preview | Menulis ulang `Host`/`Origin` bisa merusak app yang membangun URL absolut, seperti OAuth callback. Butuh `AllowTcpForwarding` di server |
| Aplikasi desktop | VA-API saja, baru teruji di Arc A380. Shortcut yang ditahan browser (Ctrl+T, Ctrl+W, Ctrl+N) tidak sampai. Tanpa mikrofon. `wayvnc` 0.9.1 sesekali segfault (hub menyalakannya ulang dalam sekitar 2 detik). Di mesin tanpa output suara asli, sink sesi bisa menjadi default |
| Browser di tile | Firefox dan snap/flatpak belum. Satu tile per board |
| Usage | Tiap pemeriksaan Claude menambah 2 baris ke `history.jsonl` pada config yang dipakai. Codex 0.165 menulis `[tui]` ke `config.toml` sekali, dan hub tidak bisa mencegahnya tanpa menulis config. Format layar bisa berubah kapan saja |
| Handoff | Ringkasan untuk sumber remote memindahkan isi transcript ke This PC |
| Credentials | Passkey, mengisi kartu dan identitas, kode Steam, pemeriksaan kebocoran password, dan kartu Android dan desktop belum ada. Agent memakai credential hanya teruji dengan agent palsu dan Claude Code; Codex dan OpenCode belum ([API-vault.md](API-vault.md)) |
| Uptime | Jendela maintenance, tag dan grup monitor, tipe WebSocket, JSON query, Docker, dan Steam belum ada. Halaman status publik mati bersama hub, jadi tidak bisa memberi tahu hub-nya sendiri mati ([API-uptime.md](API-uptime.md)) |
| Layout HP dan tablet | Kerangka (header, tab bar HP, menu `…`, sheet), semua halaman, dan board di layar sentuh (Insert berlabel, long-press, dua jari, Focus mode, Select several), board di HP (tab Board, Cards, Lead), bar tombol console (Esc, Ctrl, panah, salin teks, lampiran), jalur sentuh tiap kartu (grip Kanban dan Memory tile, tap untuk edit, Rename dan Straighten di menu, daftar hari Calendar, grafik dengan tap), Code card serta studio di HP dan tablet (laci side bar, bar tombol editor, daftar lalu isi di studio), dan pemasangan sebagai aplikasi (manifest, ikon, Settings › Install app, peringatan dan jalan keluar di HTTP biasa) sudah (DESIGN §3.0, §3.5, §3.6, §3.9, §3.17). Tanpa service worker, jadi tidak ada yang berjalan offline. Diuji dengan emulasi Chromium; perangkat asli (Poco F5, Xiaomi Pad 6, Galaxy Tab S9) belum |
| Test | Test unit web mencakup vault, markdown, socket, blok memory, aturan wire, dan pilihan layout (148 test). Uji browser di repo (`make test-e2e`) baru mencakup memory tile, dialog Send, daftar perintah terblokir, halaman status publik, persetujuan credential untuk agent, jendela vault dari header, layout HP, tablet, dan desktop di setiap halaman (proyek `phone`, `tablet-portrait`, `tablet-landscape`), board di layar sentuh (Insert, long-press, dua jari, Focus mode, garis, Select several), board di HP (daftar Cards, swipe, tab Board dan Lead), bar tombol console (tombol, Ctrl sekali pakai, salin teks, lampiran foto), kartu dengan jari (scroll dan grip Kanban, long-press kartu, edit Checklist dengan tap, grip Memory tile, daftar hari Calendar, Rename di menu, Straighten, link `?card=`), Code card serta studio di HP dan tablet (laci side bar, bar tombol editor sampai file tersimpan, Find lalu Esc, bar tombol terminal panel, daftar lalu isi di studio), manifest yang bisa di-install dan pane Install app (tawaran browser, terpasang, peringatan HTTP), serta sapuan semua halaman, pane Settings, dialog, dan tiap jenis kartu dalam focus mode di kelima profil (tidak ada yang keluar dari tepi layar); alur UI lain masih dicoba di luar repo. Sisa kriteria SPEC §12 ada di §13 |
| Klien | Lihat §10 |

### Pertanyaan untuk pemilik

- Kapan `remoteTools` keluar dari preview.
- Android dan desktop: dilanjutkan hanya bila pemilik memintanya (keputusan 2026-11-05).
- Runner headless yang menyimpang dari Symvibe (API-studio §19 butir 6): disahkan atau tidak.
- Default lead yang belum dibahas: nama Nara, dan order `@Agent` yang langsung jalan.
- Urutan kerja berikutnya.

## 15. Peta dokumen

| Dokumen | Isi |
|---|---|
| [../README.md](../README.md) | Instalasi dan pengembangan singkat |
| [SPEC.md](SPEC.md) | Spesifikasi awal Fase 1–3, jaringan, auth, SSH, batas memori |
| [ROADMAP.md](ROADMAP.md) | Fase, keputusan pemilik, verifikasi flag CLI |
| [DESIGN.md](DESIGN.md) | Design system NextBoard Glass: token, komponen, aksesibilitas |
| [API.md](API.md) | Konvensi, auth, machine, session, WebSocket, log koneksi |
| [API-board.md](API-board.md) | Project, workspace, board, file, aset |
| [API-agents.md](API-agents.md) | Katalog, wrapper, status, transcript, profil, settings, notifikasi, agent di server, MCP |
| [API-orchestration.md](API-orchestration.md) | Delivery, command, planner, thread, grup, handoff, forward, loop, memory, layout |
| [API-studio.md](API-studio.md) | Job, runner, Git, preview, studio, usage, suara |
| [API-gui.md](API-gui.md) | Aplikasi desktop, browser sungguhan, video, suara |
| [API-uptime.md](API-uptime.md) | Monitor uptime |
| [API-vault.md](API-vault.md) | Credentials: format enkripsi, API, perilaku, yang tidak dilindungi |
