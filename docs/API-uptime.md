# Uptime monitoring

Referensi: [Uptime Kuma](https://github.com/louislam/uptime-kuma). Hub yang mengecek, selama hub hidup, ada board terbuka atau tidak. Kartu `uptime` di board hanya menampilkan hasilnya. Paket: `internal/uptime`, handler `internal/server/h_uptime.go`, klien `web/src/uptime/` dan `web/src/board/tiles/UptimeTile.tsx`.

## Model

Monitor milik **project**, bukan milik kartu. Semua kartu Uptime di satu project menampilkan monitor yang sama.

- **Menghapus kartu** tidak menghentikan monitor (dialognya bilang begitu); tambah kartu lagi untuk melihatnya.
- **Menghapus monitor** menghentikan cek dan menghapus riwayatnya.
- **Menghapus project** menghapus semua monitornya.

| Tipe | Yang dicek | Field |
|---|---|---|
| `http` | Status code, kata kunci di 1 MB pertama, sertifikat | `url`, `method` (`GET`/`HEAD`), `accepted` (`"200-299,301"`), `keyword`, `keywordInvert`, `ignoreTls`, `headers` |
| `tcp` | Port bisa dibuka | `host`, `port` |
| `ping` | `ping -c 1` milik machine (ICMP mentah butuh hak yang tidak dimiliki hub) | `host` |
| `dns` | Ada jawaban, dan memuat teks tertentu bila diminta | `host`, `dnsType` (`A AAAA CNAME MX NS TXT`), `dnsServer`, `dnsExpect` |
| `push` | Pihak lain melapor; turun bila laporan berhenti | `pushToken` (dibuat hub) |
| `websocket` | Upgrade WebSocket dikabulkan: `101` dengan `Sec-WebSocket-Accept` yang benar (RFC 6455). Tidak ada pesan yang dikirim | `url` (`ws://`/`wss://`), `headers`, `ignoreTls` |
| `docker` | `docker inspect` di machine: up selama container `running`, dan `healthy` bila punya health check. Nama container jadi argumen, bukan lewat shell | `container` (nama atau id) |
| `steam` | Server game menjawab `A2S_INFO` lewat UDP, termasuk challenge. Pesannya nama, map, dan jumlah pemain. Selalu dari This PC, karena SSH tidak membawa UDP | `host`, `port` (bawaan 27015) |

**JSON query** (`http`): `jsonPath` mengambil satu nilai dari jawaban JSON (`data.items[0].status`, boleh diawali `$.`, kunci berspasi atau bertitik ditulis `["kunci ini"]`), lalu `jsonOp` (`==`, `!=`, `contains`, `<`, `>`, `<=`, `>=`) membandingkannya dengan `jsonExpect`. String dibandingkan apa adanya, nilai lain sebagai JSON (`true`, `3`, `null`). Nilai yang tidak ada selalu gagal. Butuh `GET`. Bila `keyword` juga diisi, keduanya harus lolos.

**Grup dan tag:** `group` (≤ 40 karakter) mengelompokkan monitor di bawah satu judul di kartu, dan `tags` (≤ 10, masing-masing ≤ 24, tanpa koma, tanpa duplikat) menjadi chip filter. Keduanya tidak tampil di halaman publik.

Semua: `name`, `intervalSec` (20 sampai 86400, bawaan 60), `retries` (0 sampai 10, bawaan 1), `retryIntervalSec` (bawaan sama dengan interval), `timeoutSec` (1 sampai 60, bawaan 80 % interval maks 30), `active`, `notify`. Maksimal 100 monitor per project; paling banyak 32 cek berjalan bersamaan.

**`partId`**: part project yang dipantau monitor ini (API-board §1, ROADMAP #49), hanya label: tampil sebagai chip di card Uptime. Harus part project itu (`404 part_not_found`). Tidak pernah sampai ke halaman status publik.

**`machineId`** (`http`, `tcp`, `ping`): dicek dari server lewat koneksi SSH yang sudah ada, tanpa memasang apa pun, jadi bisa menjangkau yang hanya terlihat dari server itu (localhost-nya sendiri, jaringan privatnya). `http` dan `tcp` lewat channel `direct-tcpip`, `ping` lewat exec. Kosong berarti This PC.

## Status

| Status | Arti |
|---|---|
| `up` | Cek terakhir berhasil |
| `pending` | Gagal, masih dalam jatah `retries` |
| `down` | Gagal setelah jatah habis; sekali `down`, gagal berikutnya tetap `down` |
| `paused` | `active:false` |
| `waiting` | Belum ada hasil, atau machine-nya tidak terjangkau. Bukan heartbeat dan tidak dihitung: machine putus tidak berarti target mati |
| `maintenance` | Jendela maintenance project sedang berlaku untuk monitor ini (lihat di bawah). `msg` berisi judul jendelanya |

**Uptime** = `up / (up + down)`. `pending` tidak dihitung ke mana pun (blip yang lewat adalah alasan adanya retry; beda dengan Uptime Kuma 2, yang menghitungnya down). Waktu tanpa cek (hub mati, monitor di-pause) juga tidak dihitung.

## Maintenance

Jendela maintenance milik project, disimpan di `settings.json` project (`settings.maintenance`), paling banyak 20. Selama jendela berlaku, monitor yang dicakupnya tidak dicek, tidak ada heartbeat (uptime tidak menghitung waktu itu), dan tidak ada alert. Push yang datang dicatat sebagai tanda hidup, tetapi tidak dihitung. Monitor melihat lagi paling lambat tiap 1 menit, jadi akhir jendela cepat terasa. Sesudahnya monitor dicek seperti biasa, dan bila ternyata mati, itu alert baru. Jam push mulai lagi dari akhir jendela.

| Field | Arti |
|---|---|
| `id` | Dibuat hub |
| `title` | ≤ 80 karakter; tampil di kartu, tidak di halaman publik |
| `monitorIds` | Monitor project ini yang dicakup; kosong = semua. Monitor yang dihapus keluar dari daftar, dan jendela yang jadi kosong ikut dihapus (bukan berubah mencakup semua) |
| `mode` | `manual` (`active` menyalakan dan mematikan), `once` (`start` sampai `end`), `weekly` (`days` 0 Minggu sampai 6 Sabtu, `from` "HH:MM", `minutes` 1 sampai 10080, `tz` zona IANA; kosong = zona hub). Jendela mingguan boleh melewati tengah malam |

Halaman publik menulis **Maintenance**, dan judul atas `Some services are under maintenance` bila tidak ada yang mati, tanpa judul jendela.

## Alert

Saat turun, saat pulih, dan sertifikat tinggal 14, 7, atau 1 hari (sekali per ambang, tidak diulang oleh restart, mulai lagi bila sertifikat diperbarui). Hanya untuk monitor dengan `notify:true`. Blip dalam jatah retry tidak memberi alert. Restart hub tidak memberi alert.

- **Di aplikasi:** event `attention` (API-agents §12) dengan `kind` `uptimeDown`, `uptimeUp`, `certExpiring` dan `monitorId`, tanpa `sessionId`. Tombol Show membuka monitornya.
- **Webhook** per project, supaya sampai juga saat tidak ada board terbuka: `POST` JSON `{"text","content","kind","monitor","target","message","at"}`. `text` dibaca Slack dan Telegram (`…/sendMessage?chat_id=`), `content` dibaca Discord. Dicoba 3 kali (0, 5, 30 detik) di latar belakang.

URL webhook sering sekaligus rahasianya: tidak pernah masuk log atau pesan error. Kredensial di URL monitor tidak ikut ke alert (`Target()` memakai `Redacted`), dan pesan error tidak mengulang URL.

## API

| Method | Path | Body | Respons |
|---|---|---|---|
| GET | `/api/projects/{id}/uptime` | — | `{"items":[View…],"settings":Settings}` |
| POST | `/api/projects/{id}/uptime` | Monitor | `201 View`; bawaan `active`, `notify`, `retries:1` |
| PUT | `/api/projects/{id}/uptime/settings` | `{"webhook"}` | `Settings`; `""` mematikan |
| PUT | `/api/projects/{id}/uptime/maintenance` | `{"windows":[Window…]}` | `Settings`; menggantikan seluruh daftar. Monitornya langsung melihat lagi |
| POST | `/api/projects/{id}/uptime/settings/test` | — | `{ok}`; `502 webhook_failed` |
| PATCH | `/api/uptime/{id}` | sebagian Monitor | `View`; riwayat tetap, cek berikutnya langsung. `id`, `projectId`, `pushToken` tidak bisa diubah |
| DELETE | `/api/uptime/{id}` | — | `{ok}` |
| POST | `/api/uptime/{id}/check` | — | `202`; hasilnya datang lewat event. `409 paused`; `400` untuk `push` |
| GET | `/api/uptime/{id}/history?range=` | — | `Series`; `1h 24h 7d 30d 1y` |
| GET | `/api/uptime/{id}/events?limit=50` | — | `{"items":[{"t","status","msg","durSec"}]}`, terbaru dulu, maks 200 |
| GET | `/api/push/{token}?status=up\|down&msg=&ping=` | — | **Tanpa login.** `{ok}`; `404` token asing atau monitor di-pause |

`View` = Monitor + `status`, `target`, `since`, `msg`, `ms`, `lastCheck`, `avgMs24h`, `uptime24h/7d/30d/1y` (`null` tanpa cek), `cert {subject,issuer,notAfter}`, `recent` (60 beat terakhir: `{"t","s","ms","m"}`, `s` 0 down, 1 up, 2 pending; `ms` −1 bila tidak terukur).

`Series` = `{range,from,to,stepSec,points:[{t,up,down,pending,avgMs,minMs,maxMs}],stats:{uptime,avgMs,minMs,maxMs,checks}}`. Langkah: 1 menit, 10 menit, 1 jam, 6 jam, 1 hari. Langkah tanpa cek tidak dikirim, dan garis grafik putus di sana.

Event `/api/events`:
- `{"type":"uptimeMonitor","monitor":View}` saat dibuat atau diubah, selalu sebelum heartbeat pertamanya.
- `{"type":"uptimeBeat","projectId","monitorId","beat","monitor":View tanpa recent}` tiap cek.
- `{"type":"uptimeRemoved","projectId","id"}`.

Planner dan command bar: `add an uptime monitor` (`node.add` kind `uptime`) menambah kartunya. Monitor sendiri belum bisa dibuat lewat planner.

## Halaman status publik

Satu halaman per project, tanpa login, di `/status/{token}`. Mati secara bawaan, dan monitor tidak ikut kecuali ditandai satu per satu.

**Yang tampil, dan hanya ini:** judul pilihan pemilik (bawaan `Status`), lalu per monitor bertanda `public`: `publicName`, `Operational` / `Down` / `No data`, persentase uptime 30 hari, satu kotak per hari selama 30 hari, dan gangguan 30 hari terakhir (waktu mulai dibulatkan ke menit, lama gangguan). Waktu dalam UTC. Blip dalam jatah `retries` tidak terlihat sebagai gangguan.

**Yang tidak pernah tampil:** URL, host, port, tipe monitor, machine, header, keyword, pesan error (sering memuat IP internal), waktu respons, sertifikat, id monitor, token push, nama monitor asli, nama project, nama hub, versi, atau tanda bahwa ini NextBoard.

Ini dijamin oleh bentuk kodenya, bukan oleh menyaring keluaran: halaman dibangun dari struct `publicPage` (`internal/uptime/public.go`) yang tidak memegang `Monitor`, dengan `html/template`. `public_test.go` menanam nilai penanda di semua field itu dan memeriksa tidak satu pun muncul.

- `publicName` wajib untuk monitor publik, karena nama monitor sering berisi host. Ditolak (`400`) bila memuat host yang dipantau, alamat IP, atau `://`. Ini menangkap salah ketik, bukan segalanya: isinya tetap tanggung jawab pemilik.
- Tidak ada skrip, tidak ada sumber luar, tidak ada cookie, tidak ada form. `Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'`, `X-Robots-Tag: noindex, nofollow`, `Referrer-Policy: no-referrer`, `Cache-Control: no-store` (browser atau CDN yang menyimpan halaman akan terus menampilkannya sesudah dimatikan). Halaman menyegarkan diri tiap 60 detik lewat `<meta refresh>`.
- Token 192 bit dari `crypto/rand`. Token asing, halaman mati, dan jalur lain di bawah `/status/` mendapat `404` yang sama persis, tanpa HTML aplikasi. `POST …/public/rotate` membuat alamat baru dan alamat lama langsung mati; mematikan lalu menyalakan mempertahankan alamat.
- Halaman dihitung paling sering sekali per 15 detik per token, jadi pengunjung tidak menambah beban cek. Mengubah monitor, mematikan halaman, atau rotate berlaku saat itu juga.

| Method | Path | Body | Respons |
|---|---|---|---|
| PUT | `/api/projects/{id}/uptime/public` | `{"enabled","title"}` | `{"enabled","token","title"}`; token dibuat saat pertama kali menyala |
| POST | `/api/projects/{id}/uptime/public/rotate` | — | seperti di atas |
| GET | `/status/{token}` | — | **Tanpa login.** HTML; `404` selain itu |

`GET /api/projects/{id}/uptime` membawa pengaturannya di `settings.publicPage`. Monitor membawa `public` dan `publicName`.

**Menaruhnya di internet.** Port utama hub pada dasarnya shell di mesin ini, jadi jangan dibuka. `nextboard serve --status-listen 127.0.0.1:12340` (atau `install --status-listen`, disimpan di `config.json` sebagai `statusListen`) membuka alamat kedua yang hanya mengenal `GET /status/{token}`; selain itu `404`, termasuk `/`, `/api/*`, `/setup`, dan aset web. Arahkan Cloudflare Tunnel, `tailscale funnel 12340`, atau nginx (`deploy/nginx-status.conf.example`) ke port itu saja. HTTPS tetap urusan yang di depannya. Alamat yang ditampilkan dialog memakai alamat hub yang sedang dibuka, jadi ganti host-nya dengan domain publik. Gagal bind hanya dicatat di log; hub tetap jalan.

Monitor tetap dicek selama hub hidup, jadi bila hub mati halamannya ikut mati, dan pengunjung tidak bisa membedakannya dari layanan yang mati. Status page yang berdiri sendiri perlu tempat lain.

## Penyimpanan

`<data>/uptime/`: `monitors.json`, `settings.json` (termasuk token halaman publik), dan satu folder per monitor. File JSON store ditulis ulang seluruhnya, yang tidak pantas untuk heartbeat tiap menit, jadi riwayat append-only:

| File | Isi | Disimpan |
|---|---|---|
| `beats.jsonl` | Tiap cek | 3 hari |
| `hours.jsonl` | Rangkuman per jam yang sudah tertutup | 1 tahun |
| `events.jsonl` | Turun dan pulih | 200 terakhir |

Baris yang robek karena crash dilewati. Beat masuk disk sebelum diumumkan; jam yang belum tertulis dihitung ulang dari beat saat load. Dirapikan saat load dan tiap 6 jam. Rentang 24 jam ke bawah tepat dari beat, rentang lebih panjang per jam penuh.

Isi `monitors.json` termasuk `headers` dan URL webhook **tertulis apa adanya** (izin `0600`, ditolak file API seperti file hub lain).

## Keamanan

- Target ditentukan pemilik dan alamat privat memang boleh, karena memantau layanan internal adalah gunanya. Hanya user yang login bisa menambah monitor; agent tidak punya tool untuk itu.
- `host` divalidasi (`hostRe`) sebelum jadi argumen `ping`: tidak bisa diawali `-` dan tidak bisa memuat spasi atau karakter shell. `ping` dijalankan lewat `Argv`, bukan shell.
- `GET /api/push/{token}` dan `GET /status/{token}` satu-satunya route uptime tanpa login. Push: token 192 bit dari `crypto/rand`, hanya bisa menambah heartbeat monitor itu, laporan lebih cepat dari satu per detik dihitung satu. Status: lihat di atas.
- `ignoreTls` pilihan per monitor, mati secara bawaan.
- Tiap cek HTTP memakai koneksi baru dan mengabaikan proxy dari environment hub, karena pertanyaannya adalah apakah target ini menjawab.

## Belum ada

Status page per grup, maintenance yang tampil dengan judul dan jadwal di halaman publik, dan pesan WebSocket (kirim lalu tunggu jawaban tertentu).

## Verifikasi (2026-11-03)

Hub sungguhan dan Chrome sungguhan: target lokal dimatikan dan dihidupkan, urutan up → pending → down → up, toast dan webhook lokal, restart hub dengan 24 jam riwayat tanpa alert palsu, timer 20 detik sungguhan, hover grafik di zoom board yang berbeda, kartu sempit. `https://example.com` sungguhan untuk sertifikat. `http`, `tcp`, `ping` lewat sshd sungguhan. **Belum diverifikasi:** webhook ke Slack, Discord, dan Telegram yang asli (formatnya dari dokumentasi mereka; yang diuji hanya penerima lokal).
