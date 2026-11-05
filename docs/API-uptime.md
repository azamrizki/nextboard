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

Semua: `name`, `intervalSec` (20 sampai 86400, bawaan 60), `retries` (0 sampai 10, bawaan 1), `retryIntervalSec` (bawaan sama dengan interval), `timeoutSec` (1 sampai 60, bawaan 80 % interval maks 30), `active`, `notify`. Maksimal 100 monitor per project; paling banyak 32 cek berjalan bersamaan.

**`machineId`** (`http`, `tcp`, `ping`): dicek dari server lewat koneksi SSH yang sudah ada, tanpa memasang apa pun, jadi bisa menjangkau yang hanya terlihat dari server itu (localhost-nya sendiri, jaringan privatnya). `http` dan `tcp` lewat channel `direct-tcpip`, `ping` lewat exec. Kosong berarti This PC.

## Status

| Status | Arti |
|---|---|
| `up` | Cek terakhir berhasil |
| `pending` | Gagal, masih dalam jatah `retries` |
| `down` | Gagal setelah jatah habis; sekali `down`, gagal berikutnya tetap `down` |
| `paused` | `active:false` |
| `waiting` | Belum ada hasil, atau machine-nya tidak terjangkau. Bukan heartbeat dan tidak dihitung: machine putus tidak berarti target mati |

**Uptime** = `up / (up + down)`. `pending` tidak dihitung ke mana pun (blip yang lewat adalah alasan adanya retry; beda dengan Uptime Kuma 2, yang menghitungnya down). Waktu tanpa cek (hub mati, monitor di-pause) juga tidak dihitung.

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

## Penyimpanan

`<data>/uptime/`: `monitors.json`, `settings.json`, dan satu folder per monitor. File JSON store ditulis ulang seluruhnya, yang tidak pantas untuk heartbeat tiap menit, jadi riwayat append-only:

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
- `GET /api/push/{token}` satu-satunya route tanpa login: token 192 bit dari `crypto/rand`, hanya bisa menambah heartbeat monitor itu, laporan lebih cepat dari satu per detik dihitung satu.
- `ignoreTls` pilihan per monitor, mati secara bawaan.
- Tiap cek HTTP memakai koneksi baru dan mengabaikan proxy dari environment hub, karena pertanyaannya adalah apakah target ini menjawab.

## Belum ada

Halaman status publik, jendela maintenance, tag dan grup monitor, tipe WebSocket, JSON query, Docker, dan Steam. Halaman status publik sengaja belum: itu membuka sesuatu ke publik, jadi keputusan pemilik.

## Verifikasi (2026-11-03)

Hub sungguhan dan Chrome sungguhan: target lokal dimatikan dan dihidupkan, urutan up → pending → down → up, toast dan webhook lokal, restart hub dengan 24 jam riwayat tanpa alert palsu, timer 20 detik sungguhan, hover grafik di zoom board yang berbeda, kartu sempit. `https://example.com` sungguhan untuk sertifikat. `http`, `tcp`, `ping` lewat sshd sungguhan. **Belum diverifikasi:** webhook ke Slack, Discord, dan Telegram yang asli (formatnya dari dokumentasi mereka; yang diuji hanya penerima lokal).
