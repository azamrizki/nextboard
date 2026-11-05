# NextBoard — Visual Board untuk AI Coding Agents (Self-Hosted)

> Nama: **NextBoard** (binary / CLI: `nextboard`). Proyek open source berlisensi MIT.
> Terinspirasi oleh Symvibe (board visual untuk agen), tapi dibangun dari nol.

## 1. Tujuan

**Satu sistem yang dipasang sekali.** Setelah `nextboard` di-install di sebuah komputer atau server:

1. Daemon langsung berjalan sebagai service dan menyajikan **Web UI di port 12312**.
2. Dari Web UI, Anda mengelola banyak terminal agen AI (Claude Code, Codex, Gemini CLI, shell) dalam bentuk **board visual tak terbatas**. Setiap agen menjadi satu node terminal.
3. Device lain (browser lain, app desktop, app mobile) **di-pair dari Web UI** lewat QR atau kode.
4. Selain "This PC", Anda bisa **menambah machine lain lewat SSH secara agentless**, tanpa memasang atau mengubah apa pun di server tujuan. Agen di machine itu tampil di board yang sama.

**Masalah yang diselesaikan:**
- Terlalu banyak tab terminal dan sesi SSH, sehingga sulit melihat agen mana yang bekerja dan mana yang menunggu input.
- Agen berhenti ketika laptop ditutup.
- Tidak bisa memantau agen dari HP.
- Agen tersebar di beberapa server tanpa satu tempat untuk melihat semuanya.

## 2. Konsep inti

```
              ┌──────────────── HUB (nextboard daemon) ────────────────┐
Devices  ───► │ Web UI :12312 · Auth & pairing · Board · Session mgr  │ ───► Machines
(klien)       └────────────────────────────────────────────────────────┘      (tempat agen jalan)
 • Browser (login password)                                                    • This PC (PTY lokal)
 • Desktop app (paired)                                                        • Server A (SSH, agentless)
 • Mobile app (paired)                                                         • Server B (SSH, agentless)
```

| Istilah | Arti |
|---|---|
| **Hub** | Daemon `nextboard` yang di-install. Menyajikan UI dan API, menyimpan semua state, dan mengatur sesi. |
| **Device** | Klien yang melihat dan mengontrol board: browser, app desktop, app mobile. |
| **Machine** | Komputer tempat proses agen berjalan. `local` ("This PC") selalu ada, dan machine lain ditambahkan lewat SSH. |
| **Session** | Satu terminal (satu proses agen atau shell) di sebuah machine, tampil sebagai satu node di board. |

## 3. Keputusan yang sudah diambil

| Aspek | Keputusan |
|---|---|
| Hub | **Go**, satu binary, frontend di-embed via `go:embed` |
| Frontend | **React + Vite + TypeScript** |
| Canvas | `@xyflow/react` (React Flow) |
| Terminal | `@xterm/xterm` + `@xterm/addon-fit` (+ `@xterm/addon-webgl` dengan fallback) |
| PTY lokal | `github.com/creack/pty` |
| SSH | `golang.org/x/crypto/ssh` (+ `knownhosts`, `agent`), `github.com/kevinburke/ssh_config` untuk membaca `~/.ssh/config` |
| WebSocket | `github.com/gorilla/websocket` |
| Password hash | argon2id (`golang.org/x/crypto/argon2`) |
| Port default | **12312** |
| Machine remote | **Agentless SSH.** Tidak ada instalasi, file config, atau key baru di server tujuan |
| Penyimpanan | File JSON di `~/.nextboard/`, tanpa database |
| Repo | GitHub, rilis binary lewat GitHub Releases |
| Lingkup | Pribadi, satu user (admin). Tidak ada multi-tenant, billing, atau relay cloud |

Klien pendukung tinggal di folder saudara: `../nextboard-android` (app Android native, Kotlin + Compose — menggantikan React Native karena pinning sertifikat self-signed dan Keystore lebih sederhana secara native) dan `../nextboard-desktop` (Tauri).

## 4. Instalasi dan service

### Install sekali jalan

```bash
curl -fsSL https://raw.githubusercontent.com/<user>/nextboard/main/install.sh | sh
```

`install.sh` melakukan langkah berikut:
1. Mendeteksi OS dan arsitektur (linux/darwin, amd64/arm64).
2. Mengunduh binary dari GitHub Releases dan memverifikasi checksum SHA-256.
3. Memasang binary ke `~/.local/bin/nextboard`, atau ke `/usr/local/bin` dengan `--system`.
4. Menjalankan `nextboard install`.

### `nextboard install`

- Membuat `~/.nextboard/` (mode 0700) beserta file default.
- Mendaftarkan service:
  - **Linux:** systemd **user** service (`~/.config/systemd/user/nextboard.service`) ditambah `loginctl enable-linger $USER`, supaya tetap jalan setelah logout dan reboot.
  - **macOS:** LaunchAgent (`~/Library/LaunchAgents/id.nextboard.plist`).
- Service berjalan sebagai **user Anda sendiri**. Ini disengaja, karena agen butuh login CLI milik user tersebut (`~/.claude`, `~/.codex`, dll) dan kunci SSH di `~/.ssh`.
- Menyalakan service, lalu mencetak:
  ```
  NextBoard berjalan di http://127.0.0.1:12312
  Buka link setup ini (sekali pakai, berlaku 30 menit):
  http://127.0.0.1:12312/setup?code=K7QX-M2PA
  ```

### Subcommand CLI

| Perintah | Fungsi |
|---|---|
| `nextboard serve` | Jalan di foreground (dipakai service, juga untuk dev) |
| `nextboard install` / `uninstall` | Pasang atau lepas service (data dipertahankan kecuali ada `--purge`) |
| `nextboard status` | Status service, alamat, jumlah sesi, machine, dan device |
| `nextboard logs [-f]` | Menampilkan log service |
| `nextboard setup-link` | Membuat link setup baru (kalau link lama kedaluwarsa sebelum setup) |
| `nextboard reset-password` | Reset password admin dari terminal server |
| `nextboard pair` | Mencetak QR + kode pairing di terminal (alternatif dari Web UI) |
| `nextboard update` | Unduh rilis terbaru lalu restart service |

### Setup pertama (wizard di Web UI)

1. Link setup berisi kode sekali pakai, sehingga orang lain di jaringan yang sama tidak bisa "mengklaim" hub sebelum Anda.
2. Wizard meminta: buat password admin, beri nama hub (misalnya `devbox`), lalu cek CLI agen yang terdeteksi di This PC.
3. Selesai, dan board kosong langsung tampil.

## 5. Jaringan dan HTTPS

> Keputusan pemilik (2026-11-02): hub dan listener preview hanya melayani **HTTP polos**, baik saat `make dev` maupun di build rilis. **HTTPS diakhiri di nginx** saat deploy. Hub tidak lagi membuat atau memuat sertifikat.

- **Listen default:** `127.0.0.1:12312`, karena nginx ada di host yang sama. Hub mencatat peringatan bila di-bind ke alamat non-loopback: tanpa TLS, password, token, dan isi terminal bisa disadap di jaringan itu.
- **Deploy:** `deploy/nginx.conf.example` + `deploy/nextboard-proxy.inc`. Port utama dan tiap port preview (`settings.previews.portRange`) masing-masing diberi HTTPS di nginx. Port preview di nginx wajib bind ke IP publik, bukan `0.0.0.0`, agar tidak bentrok dengan hub di `127.0.0.1:<port>`.
- **`X-Forwarded-Proto: https`** menentukan flag `Secure` pada cookie, skema CSP `frame-src`, origin yang boleh membingkai preview, dan URL pairing. Header ini **hanya dipercaya dari peer loopback**, jadi nginx harus di host yang sama.
- **Port HTTPS non-443:** isi `extraUrls` di `config.json` (atau `nextboard install --public-url https://host:port`) supaya origin itu boleh membingkai preview.
- **Jangan buka port 12312 ke internet publik.** Hub ini pada dasarnya adalah shell ke mesin Anda.

## 6. Auth dan pairing device

### Browser: login password

- Login dengan password admin. Hub lalu membuat **session cookie** (`HttpOnly`, `SameSite=Strict`, ditambah `Secure` bila klien datang lewat HTTPS) yang berlaku 30 hari dan diperpanjang setiap kali dipakai. Nama cookie beda per skema (`nb_session` di HTTPS, `nb_session_http` di HTTP), supaya host yang pernah dipakai lewat HTTPS tetap bisa login lewat HTTP.
- **Perlindungan CSRF:**
  - Semua request yang mengubah data wajib membawa header `X-NextBoard: 1`.
  - WebSocket memeriksa `Origin` sama dengan host.
- **Rate limit login:** 5 percobaan gagal per IP per 10 menit.

### App desktop dan mobile: pairing

1. Di Web UI, buka **Devices → Pair new device**.
2. Hub membuat **kode pairing** 8 karakter (format `XXXX-XXXX`, alfabet tanpa huruf ambigu) yang berlaku 5 menit dan hanya bisa dipakai sekali. Kode ditampilkan sebagai QR dan teks:
   ```
   nextboard://pair?h=<nama-hub>&u=<url1>,<url2>&c=<kode>
   ```
   `u` berisi alamat hub (lewat HTTPS: hanya origin HTTPS, tanpa alamat HTTP polos) supaya app bisa mencoba satu per satu. App memercayai sertifikat nginx lewat CA sistem; pin fingerprint hanya bila diisi manual saat pairing (mis. nginx dengan sertifikat self-signed).
3. App membuat **keypair Ed25519**. Private key disimpan di Keychain atau Keystore. App lalu mengirim:
   ```
   POST /api/pair/complete  { code, deviceName, platform, publicKey }
   ```
4. Hub mencatat device di `devices.json`, dan device langsung muncul di Web UI.
5. **Login device berikutnya (challenge-response):**
   ```
   POST /api/auth/challenge  { deviceId }              → { nonce }
   POST /api/auth/device     { deviceId, nonce, sig }  → { accessToken (opaque, TTL 1 jam) }
   ```
   `accessToken` dikirim sebagai `Authorization: Bearer` untuk REST dan WebSocket. Token tidak pernah ditaruh di URL, karena nginx mencatat query string di access log.
6. Di **Devices**, setiap device menampilkan nama, platform, waktu pairing, dan terakhir aktif. Tombol **Revoke** langsung memutus semua koneksi device tersebut.

## 7. Machines: SSH agentless

### Prinsip

> **Tidak ada perubahan di server tujuan.** Tidak ada binary, service, file config, atau entri `authorized_keys` baru. Syaratnya hanya `sshd` yang bisa diakses, dan CLI agen (claude, codex, dll) yang memang sudah terpasang dan login di sana.

### Menambah machine

Di **Machines → Add machine**, ada dua cara:
- **Dari `~/.ssh/config`.** Hub membaca daftar `Host` dari config milik user service. Kalau Anda sudah bisa menjalankan `ssh devbox` dari mesin hub, cukup pilih `devbox`. HostName, User, Port, IdentityFile, dan ProxyJump ikut dari config.
- **Manual.** Isi host, port, user, dan metode auth.

### Metode autentikasi (urutan percobaan)

1. **Key dari `~/.ssh` milik user service.** Pakai `IdentityFile` dari config, atau `id_ed25519` dan `id_rsa` default.
2. **ssh-agent**, kalau `SSH_AUTH_SOCK` tersedia di lingkungan service.
3. **Private key yang di-upload lewat UI.** Disimpan terenkripsi (AES-256-GCM, master key di `~/.nextboard/secret.key` mode 0600). Passphrase opsional, juga terenkripsi.
4. **Password**, disimpan terenkripsi dengan cara yang sama. Tidak dianjurkan.

### Verifikasi host key

- Hub memeriksa `~/.ssh/known_hosts` milik user service terlebih dahulu.
- **Host belum dikenal:** UI menampilkan fingerprint dan meminta konfirmasi (TOFU). Setelah disetujui, fingerprint disimpan ke `~/.nextboard/known_hosts`.
- **Fingerprint berubah:** koneksi **ditolak** dengan peringatan merah, tanpa auto-accept.

### Probe saat machine ditambahkan

```sh
uname -sm; echo "$SHELL"; command -v tmux claude codex gemini 2>/dev/null; echo "$HOME"
```

Hasilnya menentukan OS/arsitektur, shell default, preset yang tersedia, ketersediaan tmux, dan home directory (default cwd). Di-cache di `machines.json`. Tombol **Re-probe** tersedia.

### Menjalankan terminal di machine remote

- **Satu koneksi TCP per machine.** Setiap terminal adalah **channel SSH terpisah**.
- Per terminal: `client.NewSession()` → `RequestPty("xterm-256color", rows, cols, modes)` → jalankan lewat login shell:
  ```sh
  $SHELL -lc 'cd <cwd> && NEXTBOARD_SESSION_ID=<id> exec <command> <args...>'
  ```
  Env inline (karena `AcceptEnv`). Semua argumen di-shell-escape. Resize via `session.WindowChange(rows, cols)`. Stdin/stdout masuk ke mekanisme yang sama dengan PTY lokal.
- **Keepalive** `keepalive@openssh.com` tiap 15 detik, tiap kirim ditunggu 15 detik; 3 kali gagal = putus. Pada jaringan diam (tanpa error) hub sadar sekitar 60 detik setelah data berhenti, dan panggilan yang sedang berjalan baru kembali saat itu.
- **Reconnect** otomatis dengan backoff 1, 2, 5, 10, lalu 30 detik. Node berstatus `reconnecting`.

### Persistensi di machine remote

| Mode | Kapan | Perilaku |
|---|---|---|
| **tmux** | `tmux` ada di server | `tmux new-session -A -s nb-<id> -x <cols> -y <rows> '<cmd>' \; set-option status off`. Proses tetap hidup walau SSH putus/hub restart; hub attach ulang. |
| **direct** | tmux tidak ada / dimatikan | Proses langsung di channel SSH; putus = mati. Saat tersambung lagi diluncurkan ulang dengan `resumeArgs`. |

Default **Auto** (tmux kalau ada).

### Aksi terminal remote

| Aksi | Mode tmux | Mode direct |
|---|---|---|
| Kill | `tmux kill-session -t nb-<id>` lewat exec | `session.Signal(ssh.SIGHUP)`, tutup channel setelah 3 detik |
| Restart | Kill, lalu buat sesi tmux baru | Tutup channel, buka channel baru |
| Hub restart | Attach ulang ke `nb-<id>` | Luncurkan ulang dengan `resumeArgs` |
| Sesi yatim | "Clean up" menampilkan `nb-*` tanpa node, kill setelah konfirmasi | — |

### Fitur pendukung

- **Pemilih folder (cwd):** exec read-only `ls -1pA -- <path>`.
- **Status:** deteksi busy/idle berbasis output.

## 8. Lingkup per fase

### Fase 1: Hub inti (This PC)
Binary tunggal + service; wizard setup; login; board (pan/zoom, drag via header, resize, layout tersimpan); PTY lokal dengan scrollback 1 MiB; preset (Shell, Claude Code, Codex, Gemini, custom via `presets.json`); persist/resume; status realtime via `/api/events`; aksi node (rename, restart, restart+resume, close); multi-klien.

### Fase 2: Machines SSH agentless
Seluruh bagian 7, plus label machine di node dan pemilih machine di toolbar.

### Fase 3: Pairing dan app
API pairing/device + halaman Devices; app desktop (Tauri); app mobile (Android): scan QR, daftar terminal + tampilan terminal sederhana, aksi cepat (kirim input, restart, kill).

**Belum termasuk:** status hooks, handoff, loop/scheduler, bot node, voice, git panel, kanban/notes, studio.

## 9. Arsitektur internal

```
nextboard (Go)
 ├─ cli/            install, service, setup-link, reset-password, pair, update
 ├─ server/         router, auth middleware (cookie | bearer), CSRF, rate limit
 ├─ auth/           password (argon2id), sessions, setup code, pairing, device challenge
 ├─ machine/        Machine interface; local.go (creack/pty); ssh.go (x/crypto/ssh)
 ├─ session/        Session (Proc + ring buffer + subscribers + busy), Manager, presets
 ├─ secret/         AES-256-GCM untuk kredensial SSH yang di-upload
 └─ store/          JSON atomik (tmp + rename), mode 0600
```

```go
type Proc interface {
    io.ReadWriteCloser
    Resize(cols, rows uint16) error
    Signal(sig string) error            // "HUP" | "KILL"
    Wait() (exitCode int, err error)
}
```

### Layout data (`~/.nextboard/`, mode 0700)

```
config.json  auth.json  secret.key  devices.json  machines.json
creds/<id>.enc  known_hosts  presets.json  sessions.json  board.json  scrollback/<id>.log
```

## 10–11. API & detail implementasi

Lihat [API.md](API.md) untuk kontrak lengkap (bentuk JSON persis).

- Proses lokal: env + `TERM=xterm-256color`, `COLORTERM=truecolor`, `NEXTBOARD_SESSION_ID`. Kill: `SIGHUP` ke process group, `SIGKILL` setelah 3 detik.
- Exit: node tetap ada, pesan `── proses selesai (kode N) ──`. Subscriber tidak ditutup.
- Perintah tidak ditemukan: pesan error merah di terminal, node tetap dibuat.
- Frontend: drag hanya lewat header; body `nodrag nowheel nopan`; FitAddon + ResizeObserver; simpan layout debounce 500 ms; sesi tanpa layout ditaruh otomatis dalam grid.
- Tema gelap. Titik status: hijau berkedip = busy, hijau = idle, kuning = reconnecting, abu = exited, merah = error/exit ≠ 0. Header node `Claude Code @ devbox`, aksen per preset, badge machine.
- Keamanan: file 0600/0700; kredensial SSH tidak pernah ke klien; body maks (board 1 MB, lainnya 64 KB); log tanpa password/kode/isi terminal.

## 12. Kriteria selesai

### Fase 1
- [ ] `curl … | sh` memasang binary + service dan mencetak link setup.
- [ ] `https://<domain nginx>` menampilkan wizard; setelah setup, login berhasil.
- [ ] Reboot: service hidup lagi (linger).
- [ ] Spawn Claude Code, output realtime; `vim`, `htop`, Ctrl-C normal.
- [ ] Browser ditutup/dibuka: terminal masih jalan, scrollback tampil.
- [ ] `systemctl --user restart nextboard`: node kembali di posisi, scrollback tampil, Claude lanjut via `--continue`.
- [ ] Request tanpa auth 401; link setup tidak bisa dipakai dua kali.

### Fase 2
- [ ] Tambah machine dari `~/.ssh/config` tanpa menyentuh server tujuan.
- [ ] Host baru minta konfirmasi fingerprint; host key berubah ditolak.
- [ ] Probe mendeteksi preset dan tmux.
- [ ] Claude Code di remote tampil sebagai `Claude Code @ <machine>`.
- [ ] Mode tmux: putus/restart hub → attach kembali.
- [ ] Mode direct: setelah pulih → diluncurkan ulang dengan `resumeArgs`.
- [ ] Server tujuan tidak berubah sama sekali.

### Fase 3
- [ ] Pair app lewat QR; device muncul di daftar.
- [ ] Revoke langsung memutus koneksi.
- [ ] App menolak sertifikat yang tidak dipercaya sistem, atau yang tidak cocok dengan pin bila pin diisi manual.

## 13. Roadmap setelah fase 3
Status via hooks; notifikasi HP; workspaces; handoff antar agen; loop/scheduler; bot node; node pendukung (notes/kanban, web preview, git panel); usage analytics.

## Log koneksi

Hub selalu mencatat perilaku koneksi ke server di `<data>/logs/connections.jsonl`, paling banyak sekitar 8 MiB, tanpa perintah, output, isi file, atau path. Detail di `docs/API.md`, bagian Log koneksi. Yang sudah diuji: putus dan jaringan diam lewat proxy TCP lokal. Yang belum: jaringan sungguhan, dan log ini ada untuk mengamatinya.

## Batas memori hub (diukur 2026-11-03)

Per console hub menahan paling banyak sekitar 6 MB: ring scrollback 1 MiB dan model layar sekitar 5 MB (model layar hanya menyimpan layar yang terlihat, riwayatnya ada di ring). Koneksi browser yang datang dan pergi, panggilan tool ke server, dan koneksi SSH yang putus lalu pulih tidak menambah goroutine atau heap (`TestNoLeakFromClientsAndServerDrops`, `TestScreenMemoryIsBounded`). Perintah `bash` server yang melewati batas waktu tidak meninggalkan proses (`TestRemoteBashLeavesNoProcess`). Riwayat lain dibatasi: job 100, order 200, attention 200, snapshot 24 per board.
