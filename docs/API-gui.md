# NextBoard — aplikasi desktop di board

Kontrak fitur **tile aplikasi desktop**: aplikasi GUI yang terpasang di This PC (Chrome, VS Code, dan lain-lain) tampil dan bisa dikendalikan dari tile di board. Klien lain (Android, desktop) bisa menyusul dengan kontrak ini; sampai saat itu mereka menampilkan node `app` sebagai "Unsupported tile".

## 1. Cara kerja

Setiap sesi aplikasi punya display virtual sendiri:

```
tile (iframe viewer + noVNC) ⇄ WebSocket hub ⇄ Unix socket ⇄ wayvnc ⇄ sway headless ⇄ aplikasi     (§8)
tile (WebCodecs)             ⇠ WebSocket hub ⇠ wf-recorder AV1/H.264 (GPU) ⇠ sway headless     (gerakan besar, §7)
tile (AudioContext)          ⇠ WebSocket hub ⇠ pw-record ⇠ sink PipeWire sesi ⇠ aplikasi     (suara, §7)
```

- Aplikasi **tidak muncul di layar fisik**. Input dari tile tidak mengganggu sesi desktop yang sedang dipakai di This PC.
- Satu aplikasi hanya punya **satu sesi**, dengan **satu profil khusus NextBoard** di `<data>/gui/profiles/<appId>/`. Login, extension, dan riwayat di profil asli (`~/.config/google-chrome`, `~/.config/Code`) tidak dipakai dan tidak disentuh.
- Sesi berjalan terlepas dari hub: hub restart atau berhenti tidak menutup aplikasi, dan hub berikutnya mengambilnya lagi lewat `<data>/gui/sessions/<id>/state.json`. Di bawah systemd, proses dipindah ke scope sendiri (`systemd-run --user --scope`) karena `KillMode=mixed` di unit hub akan ikut membunuhnya.
- Sesi berakhir kalau jendela terakhir ditutup di dalam aplikasi, tile dihapus, sesi dihentikan lewat API, atau display mati. Menutup workspace atau project juga mengakhirinya, kecuali aplikasi itu masih tampil di board lain.

**Syarat di This PC** (bukan dependensi hub; `GET /api/gui/apps` melaporkan yang belum ada). Satu dari dua jenis display:

```sh
# jalur utama, butuh GPU dan ffmpeg dengan VA-API (§8)
sudo apt install --no-install-recommends sway wayvnc wf-recorder xwayland
# cadangan tanpa GPU
sudo apt install tigervnc-standalone-server openbox
```

Video hardware butuh `ffmpeg` dengan `h264_vaapi` (dan `av1_vaapi` untuk AV1) dan GPU yang bisa dibuka hub (`/dev/dri/renderD*`). Suara butuh PipeWire (`pw-loopback`, `pw-record`). Tanpa keduanya tile tetap jalan, hanya tanpa fitur itu. `NEXTBOARD_GUI_GPU=off` di environment hub mematikan pemakaian GPU, `NEXTBOARD_GUI_BACKEND=x11` memaksa display X11.

Hanya Linux, dan hanya This PC. Machine SSH tidak didukung karena aturan agentless melarang memasang paket di server tujuan.

**Keamanan.** Siapa pun yang punya akses ke hub bisa mengendalikan aplikasi ini, setara dengan akses terminal yang sudah ada.

- Server VNC hanya mendengarkan Unix socket di folder `0700`, tanpa port TCP. Karena itu lapisan RFB tidak memakai password.
- Socket Wayland ada di folder `0700` per sesi. Xtigervnc memakai cookie `MIT-MAGIC-COOKIE-1` per sesi. Xwayland tidak punya cookie, jadi daftar aksesnya dikunci ke user hub sebelum aplikasi jalan (§8).
- WebSocket memakai autentikasi dan cek origin yang sama dengan terminal.
- Profil Chromium dan VS Code memakai `--password-store=basic` karena display privat tidak punya keyring (keyring sungguhan akan memunculkan prompt di layar fisik). Password dan token yang disimpan di profil itu hanya dilindungi izin file `<data>`.

## 2. Katalog aplikasi

Sumbernya file `.desktop` di direktori data XDG (termasuk export flatpak dan snap). Yang disembunyikan: `NoDisplay`, `Hidden`, `Terminal=true`, entri dengan `OnlyShowIn` (panel pengaturan sesi desktop), dan program yang tidak ada di `PATH`. `Exec` tidak pernah dijalankan lewat shell.

| Method | Path | Response |
|---|---|---|
| GET | `/api/gui/apps` | `{ "available": bool, "missing": ["openbox"], "hint": "sudo apt install …", "gpu": "/dev/dri/renderD128", "video": bool, "audio": bool, "apps": [App…] }` |
| GET | `/api/gui/apps/{id}/icon` | PNG atau SVG; `404 icon_not_found` kalau tidak ada file yang bisa dilayani |

```jsonc
// App
{
  "id": "google-chrome",           // ID desktop file
  "name": "Google Chrome",
  "comment": "Access the Internet",
  "categories": ["Network", "WebBrowser"],
  "acceptsFolder": false           // true: path folder boleh dikirim saat launch
}
```

`acceptsFolder` berlaku untuk keluarga VS Code dan aplikasi yang mendeklarasikan `inode/directory`, selama `Exec`-nya menerima file.

## 3. Sesi

| Method | Path | Request | Response |
|---|---|---|---|
| GET | `/api/gui/sessions` | — | `[Session…]` |
| POST | `/api/gui/sessions` | `{ "appId", "path"?, "width"?, "height"? }` | `201 Session` |
| POST | `/api/gui/browser/open` | `{ "url", "width"?, "height"?, "here"?, "instance"? }` | `201` browser baru dibuka, `200` jadi tab baru: `{ "session": Session, "url", "newTab": bool }`, lihat §8 |
| DELETE | `/api/gui/sessions/{id}` | — | `{ "ok": true }`; penghentian berjalan di belakang, lihat event |
| GET | `/api/gui/sessions/{id}/ws` | WebSocket | bridge RFB, lihat §4 |
| GET | `/api/gui/sessions/{id}/video?w=&h=&mbps=&codec=` | WebSocket | AV1 atau H.264, lihat §7 |
| GET | `/api/gui/sessions/{id}/audio` | WebSocket | PCM, lihat §7 |

```jsonc
// Session
{
  "id": "g_8dcf242a7b1c",
  "appId": "google-chrome",
  "name": "Google Chrome",
  "title": "New Tab - Google Chrome",   // judul jendela aktif, ikut berubah
  "state": "running",                   // starting | running | stopping
  "display": "wayland",                 // wayland | x11
  "audio": true,                        // punya sink suara sendiri
  "instance": 2,                        // browser ke berapa dari jenis ini; tidak ada = yang pertama
  "startedAt": "2026-11-03T00:24:07Z"
}
```

`path` diabaikan kalau aplikasi tidak menerima folder. Kalau diterima, harus folder absolut yang ada (`~` boleh). Ukuran dibatasi 320×200 sampai 3840×2160, default 1280×800.

Kode error (`{ "error", "code" }`):

| Status | `code` | Arti |
|---|---|---|
| 409 | `already_running` | Aplikasi sudah terbuka; body juga berisi `"session": Session`. Klien memakai sesi itu |
| 409 | `too_many_sessions` | Maksimal 8 aplikasi bersamaan |
| 409 | `still_stopping` | Sesi sebelumnya belum selesai berhenti setelah ditunggu 15 detik |
| 404 | `app_not_found`, `session_not_found` | |
| 400 | `bad_url` | Bukan alamat `http` atau `https` |
| 404 | `browser_not_found` | Tidak ada browser yang didukung di This PC |
| 400 | `folder_not_found` | |
| 503 | `gui_unavailable` | Paket belum terpasang atau bukan Linux |
| 503 | `video_unavailable`, `audio_unavailable` | Encoder hardware atau suara tidak ada; viewer tetap memakai RFB |
| 409 | `too_many_encoders` | Maksimal 16 aliran video dan audio bersamaan |
| 500 | `gui_start_failed` | Display tidak bisa dibuat |

Event di `/api/events`:

```jsonc
{ "type": "guiSession", "session": Session }
{ "type": "guiSessionRemoved", "id": "g_…", "appId": "…", "reason": "closed" | "stopped" | "failed", "message": "…" }
```

`message` hanya ada kalau `reason` = `failed` (aplikasi keluar tanpa jendela, atau display mati).

**Satu sesi per aplikasi.** Chrome dan aplikasi Electron mengunci profil: membuka profil yang sama di display kedua hanya memunculkan jendela baru di display pertama, dan tile kedua tetap kosong. Karena itu hub menolak sesi kedua dan menunjuk sesi yang ada. Pengecualiannya browser yang diminta tile untuk dirinya sendiri (§8): profilnya terpisah, jadi tidak saling mengunci.

## 4. Tile dan viewer

Node kind `app`, `data` = `{ "appId", "name", "sessionId" }` (lihat [API-board.md](API-board.md)). Hub tidak memvalidasi node ini, jadi `sessionId` bisa menunjuk sesi yang sudah berakhir; tile lalu menawarkan membuka aplikasi lagi. Menghapus tile menghentikan aplikasinya, dan undo mengembalikan tile dalam keadaan tertutup.

`GET /api/gui/sessions/{id}/ws` meneruskan aliran RFB 3.8 apa adanya lewat frame biner. Klien mana pun yang bisa RFB bisa memakainya, walau tanpa §7; web memakai noVNC. Beberapa klien boleh menonton satu sesi, tapi ukuran display mengikuti klien yang terakhir meminta resize.

Web memuat noVNC di halaman terpisah `/gui-viewer.html?session=<id>&display=<wayland|x11>` di dalam iframe same-origin, karena koordinat pointer di dalam iframe tidak terpengaruh zoom board. Halaman ini satu-satunya yang boleh di-frame (`X-Frame-Options: SAMEORIGIN`, `frame-ancestors 'self'`). Viewer dan tile bicara lewat `postMessage` dengan `nb: "gui"`:

- Viewer → tile: `{ "type": "ready" }` saat listener-nya siap. noVNC memakai top-level await, jadi event `load` iframe bisa datang lebih dulu dan tidak bisa dipakai sebagai tanda.
- Viewer → tile: `{ "type": "state", "state": "connected" | "disconnected", "video": bool }`. `video` false berarti browser tidak punya WebCodecs.
- Tile → viewer: `{ "type": "audio", "on": bool }`.
- Tile → viewer: `{ "type": "mic", "on": bool }`; viewer → tile: `{ "type": "mic", "state": "on" | "off" | "insecure" | "denied" | "missing" | "failed" }`.
- Tile → viewer: `{ "type": "keys", "keys": [{ "keysym", "code" }] }`: modifier turun, tombol ditekan, modifier naik. Dipakai menu keyboard di header tile: Ctrl+T, Ctrl+W, Ctrl+Shift+T, Ctrl+Tab, Ctrl+Shift+Tab, Ctrl+N, Ctrl+Shift+N, Ctrl+Shift+W.

Clipboard:

- **Browser → aplikasi:** Ctrl+V di tile mengirim teks clipboard browser lalu menekan Ctrl+V di aplikasi. Tidak butuh izin.
- **Login dari Credentials:** pesan `{nb:'gui',type:'enter',id,parts}` ke viewer menempel tiap teks dan menekan Tab di antaranya, lalu menimpa clipboard display; jawabannya `entered {id,ok}`, dan clipboard yang kembali dari display diabaikan selama itu ([API-vault.md](API-vault.md)).
- **Aplikasi → browser:** teks yang disalin di aplikasi langsung masuk clipboard browser kalau halaman memakai HTTPS atau localhost. Selain itu muncul tombol "Copy text from the app".

## 5. Profil

Tiap aplikasi selalu memakai foldernya sendiri di `<data>/gui/profiles/<appId>/`. Folder ini tidak dihapus saat sesi berakhir.

| Aplikasi | Cara memilih profil |
|---|---|
| Keluarga Chromium | `--user-data-dir`, `--password-store=basic` |
| Keluarga VS Code | `--user-data-dir`, `--extensions-dir`, `--shared-data-dir`, `--password-store=basic`, `COPILOT_HOME` |
| Firefox | `--new-instance --profile` |
| Lainnya | `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_CACHE_HOME`, `XDG_STATE_HOME` |

Tiap sesi juga punya bus D-Bus privat (`dbus-run-session`), supaya aplikasi GTK/GNOME tidak mengirim jendelanya ke instance yang sudah berjalan di layar fisik. Aplikasi yang tidak mengikuti XDG dan sudah terbuka di layar fisik bisa tetap menolak dibuka; hub melaporkannya sebagai `failed` setelah aplikasi keluar tanpa jendela.

Environment aplikasi dibersihkan dari penanda sesi lain (`VSCODE_*`, `ELECTRON_*`, `CLAUDE*`, `SSH_*`, `DISPLAY`, `DBUS_SESSION_BUS_ADDRESS`, dan lain-lain). Tanpa ini, launcher VS Code akan meneruskan permintaan ke VS Code Server.

## 6. Batasan

- Shortcut yang ditahan browser (Ctrl+T, Ctrl+W, Ctrl+N, Ctrl+Tab, dan sejenisnya) tidak sampai ke aplikasi dari keyboard. Halaman tidak bisa menangkapnya di HTTP biasa, jadi tile punya menu keyboard yang mengirimnya (§4). `navigator.keyboard.lock()` hanya jalan di HTTPS dan fullscreen, dan tidak dipakai.
- Di Wayland, jendela kedua memunculkan baris tab di atas tile untuk berpindah; dialog mengambang. Di X11 tidak ada pengalih selain Alt+\`.
- Video hardware baru mendukung VA-API (Intel dan AMD), dan hanya teruji di Intel Arc A380. NVENC belum.
- `wayvnc` masih menyalin gambar ke CPU selama layar bergerak, walau hanya satu piksel yang diminta viewer (sekitar 5–14 % satu core): ia tidak bisa dijeda.
- Di mesin tanpa output suara asli, seperti This PC yang hanya punya Dummy Output, sink sesi bisa menjadi default selama sesi itu hidup. Aplikasi di luar tile lalu berbunyi di tile itu.
- Aplikasi yang mengabaikan `PULSE_SINK` dan `PIPEWIRE_NODE` tidak terdengar di tile. Yang mengabaikan `PULSE_SOURCE` tidak mendengar mikrofon tile. Aplikasi di luar tile bisa memilih sumber mikrofon sesi sendiri selama tile itu mengirim suara.
- Mikrofon butuh HTTPS atau localhost, karena browser tidak memberikannya pada HTTP biasa (§7).
- Path `<data>` harus cukup pendek untuk Unix socket: `<data>/gui/sessions/<id>/rfb` maksimal 100 byte. Default `~/.nextboard` aman.

## 7. Video dan suara

**Kenapa dua jalur gambar.** Render aplikasi sudah memakai GPU, dan latensi lokal RFB hanya 32 ms. Yang berat adalah pengiriman: scroll satu halaman teks butuh lebih dari 100 Mbps lewat RFB, jauh di atas kapasitas link jarak jauh. Encoder GPU butuh sekitar 6 Mbps dengan H.264 atau 3,5 Mbps dengan AV1 untuk gambar yang sama.

Hasil ukur scroll teks 1280×800 di Arc A380, pada display X11 (link diemulasi, klik diukur saat layar bergerak; biaya CPU display Wayland ada di §8):

| Link | RFB saja | RFB + H.264 | RFB + AV1 |
|---|---|---|---|
| 20 Mbps, RTT 30 ms | 5,6 fps, respons klik 392 ms | 47 fps, 147 ms, 6,2 Mbps | 48 fps, 132 ms, 3,4 Mbps |
| 5 Mbps, RTT 60 ms | 1,5 fps, respons klik 1165 ms | 43 fps, 180 ms, 3,6 Mbps | 48 fps, 165 ms, 2,2 Mbps |

**Format.** Pada scroll teks yang sama, AV1 3,6 Mbps mendapat SSIM 0,994 sedangkan H.264 6,3 Mbps 0,991; encoder Arc menghasilkan keduanya lebih dari 150 fps. Web meminta AV1 kalau browser punya hardware decoder-nya (`isConfigSupported` dengan `prefer-hardware`), selain itu H.264. Saat hub mengirim `slow`, web beralih ke AV1 walau tanpa hardware decoder, sebelum menurunkan bitrate. `localStorage["nb.guiCodec"]` = `h264` atau `av1` memaksa satu format.

**Kapan jalur mana.**

- RFB selalu tersambung untuk input, kursor, clipboard, dan resize. Perubahan kecil seperti mengetik dan semua tampilan saat diam memakai RFB, yang pada akhirnya lossless, jadi teks tetap tajam.
- Gerakan besar memakai canvas video yang menutupi gambar RFB, dan RFB hanya mengamati satu piksel. Gerakan besar berarti 3 frame berturut-turut melebihi 25% anggaran bitrate per frame.
- Setelah 350 ms tenang, RFB melukis ulang seluruh gambar dan canvas video disembunyikan.
- Setelah 2 detik diam, encoder dimatikan sehingga layar diam tidak memakai CPU. Encoder baru dimulai saat RFB mengirim lebih dari 100 KB dalam 500 ms; stream barunya dimulai dengan key frame di socket yang sama.
- Tile di luar layar atau tab tersembunyi menghentikan video dan membatasi RFB ke satu piksel.

WebCodecs hanya ada di HTTPS dan localhost. Di HTTP biasa viewer tetap memakai RFB saja, dan header tile menuliskannya.

**Protokol video.** Query `w` dan `h` adalah ukuran display (dibulatkan ke genap), `mbps` opsional (default dari luas frame dan format, 1–30), `codec` opsional (`av1` atau `h264`, default `h264`).

- Frame teks pertama: `{"t":"hello","mbps":6.1,"codec":"h264"}`. `codec` adalah format yang benar-benar dikirim: kalau GPU tidak punya AV1, hub mengirim H.264.
- Frame biner: 1 byte flag (bit 0: key frame) + satu picture, H.264 Annex B atau AV1 low overhead (satu temporal unit).
- Klien membalas tiap picture yang sudah didekode dengan satu pesan biner apa saja. Tanpa balasan, hub menahan encoder: picture tertua yang belum dibalas tidak boleh lebih tua dari delay terkecil yang pernah teramati + 150 ms. Link penuh menurunkan frame rate, bukan menambah delay.
- `{"t":"slow"}` berarti lebih dari separuh waktu habis menunggu. Klien sebaiknya tersambung ulang dengan `mbps` lebih rendah; web memakai 60% dari nilai sekarang, minimal 1,5 Mbps.
- Socket ditutup kalau encoder berhenti, misalnya karena display mengecil. Klien tersambung lagi dengan ukuran baru.

Encoder: `av1_vaapi` atau `h264_vaapi`, tanpa B-frame. Di display X11 warna ditandai BT.709 dan konversinya sengaja di CPU, karena mengunggah RGB lalu `scale_vaapi` di Arc membuat gambar miring pada lebar seperti 992 dan menggeser warna. Di Wayland buffer GPU dikonversi langsung (§8).

**Mikrofon.** `GET /api/gui/sessions/{id}/mic`, WebSocket: frame biner PCM 48 kHz mono s16le dari viewer ke aplikasi, 20 ms per frame. Sesi yang punya suara juga punya sumber sendiri (`Session.mic`), lewat `pw-loopback` kedua, dan aplikasinya dimulai dengan `PULSE_SOURCE`. Tombol mikrofon di header tile mati sampai diklik, tidak diingat, dan header menulis "microphone on" selama aktif. Hanya satu viewer sekaligus (`409 mic_busy`); `503 mic_unavailable` bila sesi tidak punya, misalnya sesi lama yang diambil lagi sesudah hub restart. Pada HTTP biasa `getUserMedia` tidak ada, jadi tombolnya memberi tahu itu, bukan diam.

**Protokol suara.** Frame biner PCM 48 kHz stereo s16le, 20 ms per frame. Keheningan tidak dikirim, jadi aplikasi yang diam tidak memakan bandwidth; saat ada suara sekitar 1,5 Mbps. Di link lambat hub membuang frame, karena suara yang telat lebih buruk daripada jeda pendek. Tile menahan suara sampai ada klik di board, sesuai kebijakan autoplay browser; tombol suara di header tile menutup socket-nya.

Tiap sesi punya sink PipeWire sendiri (`pw-loopback`, `priority.session=0`, jadi tidak pernah menjadi output default mesin), dan aplikasi diarahkan ke sana lewat `PULSE_SINK` dan `PIPEWIRE_NODE`. Sink hilang bersama sesi.

**GPU untuk aplikasi.** Xtigervnc dijalankan dengan `-rendernode`, sehingga aplikasi GLX mendapat DRI3. Chrome dan Electron sudah memakai GPU lewat ANGLE dengan atau tanpa flag ini, termasuk decode video hardware: video 1080p60 hanya memakan sekitar 31 % satu core di proses Chrome, jadi tidak ada flag Chrome tambahan.

**Penutupan.** Saat sesi dihentikan, program teratas dimintai `SIGTERM` lebih dulu dan ditunggu sampai 4 detik sebelum bus sesi, helper, dan terakhir display dimatikan. Proses sesi dikenali dari penanda environment dan dari silsilah prosesnya, karena Chrome menimpa memori yang dibaca `/proc/<pid>/environ`. Chrome tercatat keluar normal, jadi tidak ada tawaran "Restore pages?" saat dibuka lagi.

## 8. Situs luar di web tile

Web tile (node `web`) menampilkan dua hal: dev server dan situs luar yang ringan lewat proxy preview ([API-studio.md §4](API-studio.md)), dan sisanya lewat **browser sungguhan di This PC** di jalur yang sama dengan tile aplikasi.

**Kenapa tetap perlu browser.** Proxy cukup untuk situs satu host, tapi tetap rusak di login lewat host lain, cookie pihak ketiga, dan cek origin, dan membuat semua situs berbagi satu hostname dengan hub. Browser sungguhan tidak punya masalah itu: tab, address bar, login, unduhan, DevTools, suara, dan video semuanya miliknya sendiri.

**Alur.** Alamat yang resolve ke IP Tailscale atau publik dicoba lewat proxy hub, tanpa browser ([API-studio §4.9](API-studio.md)), kecuali tile atau pilihan yang diingat untuk host itu meminta browser. Alamat lain, dan situs yang tidak lolos uji awal, memanggil `POST /api/gui/browser/open`:

- Browser belum terbuka: hub memulainya dengan alamat itu, ukuran dari tile (`201`).
- Browser sudah terbuka: alamat jadi tab baru (`200`, `newTab: true`). Hub menjalankan program browser sekali lagi pada profil yang sama; program itu menyerahkan alamat ke instance yang berjalan lewat kunci profil lalu keluar. Cara ini juga berlaku untuk sesi yang diambil lagi setelah hub restart.

`session` adalah sesi biasa (§3), jadi berbagi sesi dan profil dengan tile Desktop app untuk aplikasi yang sama.

**Lebih dari satu tile.** Kalau alamat diketik di tile baru sedangkan board sudah menampilkan browser, tile itu bertanya: **New tab there** (tile kosong dihapus, tile lama dipilih dan di-frame) atau **Browser here**. `here: true` memulai browser sendiri untuk tile itu, paling banyak 4 per jenis (`409 too_many_sessions`). Tiap browser memakai profil sendiri (`<data>/gui/profiles/<appId>~2/`), jadi **login tidak dibagi antar-tile** dan masing-masing butuh login sendiri. Ini juga berarti dua akun bisa berdampingan. Tile mengingat `instance` di node, jadi saat dibuka lagi ia mendapat profilnya sendiri; kalau nomor itu sedang dipakai, ia mendapat nomor bebas terkecil. Satu Chrome dengan dua jendela di dua output memang berbagi login dan hemat sekitar 200 MB (lihat Memori), tapi butuh RFB dan video per output, dan tidak ada padanannya di display X11.

**Pilihan browser.** Sesi browser yang sedang berjalan dipakai lebih dulu. Selain itu entri desktop dengan kategori `WebBrowser`, urutan Chrome, Chromium, Brave, Edge, Vivaldi, Opera, Firefox, dan paket snap atau flatpak setelah semua yang dipasang biasa. Web app yang dipasang dari browser tidak ikut karena tidak berkategori `WebBrowser`.

**Firefox.** Menyerahkan alamat ke instance yang berjalan lewat bus sesi, jadi tiap sesi Firefox punya `dbus-daemon` sendiri di `<sesi>/bus`, yang juga ditemukan lagi sesudah hub restart. Dimulai dengan `--new-instance --profile`, dan tab baru dengan `--profile --new-tab`. `user.js` di profil baru mematikan pertanyaan browser default dan halaman sambutan.

**Snap.** AppArmor snap hanya mengizinkan `connect` ke `/run/user/<uid>/wayland-<angka>…`, dan yang dinilai adalah path tempat socket di-bind, jadi link ke socket privat ditolak (dicoba pada snap sungguhan). Karena itu compositor sesi ini memasang socket di folder runtime asli, dengan nama pilihan libwayland yang dilaporkan lewat `exec` sway; `WAYLAND_DISPLAY` berisi namanya saja karena `snap run` membuat link-nya di folder runtime snap. Profil di `~/snap/<nama>/common/nextboard`, tetap ada saat snap diperbarui. Bus memakai bus sesi pengguna, satu-satunya yang boleh dijangkau. Pada display X11, cookie disalin ke folder profil.

**Flatpak.** Flag masuk sesudah id aplikasi, penanda `--file-forwarding` dan `@@` dibuang, profil di `~/.var/app/<id>/nextboard`.

**Status verifikasi.** Keluarga Chromium biasa dan Firefox teruji dengan browser sungguhan. Firefox 159 dari tarball resmi, lewat `NEXTBOARD_REAL_FIREFOX=<program> go test ./internal/gui -run TestRealFirefox` (tidak di CI), di display Wayland dan X11: halaman pertama, tab baru lewat bus, tab baru sesudah hub restart, tile kedua, dan tidak ada proses tersisa. Tiap langkah dibuktikan oleh request yang benar-benar masuk ke server uji. Tarball itu di Ubuntu tidak mendapat user namespace (`unshare(CLONE_NEWPID): EPERM`), jadi sandbox prosesnya lebih lemah daripada Firefox dari paket; ini sifat tarball dan AppArmor Ubuntu, bukan hub. Snap teruji sampai jabat tangan Wayland antara sway sungguhan dan proses di dalam kurungan AppArmor snap sungguhan, tanpa browser snap. Flatpak hanya baris perintahnya. Di dua jalur terakhir, serah-terima tab ke browser yang berjalan belum pernah dicoba.

**Firefox bukan pilihan pertama.** Kalau ada keluarga Chromium di This PC, itu yang dimulai. Belum ada pengaturan untuk memilih browser tile.

**Keamanan alamat.** Alamat masuk ke command line browser, jadi hanya `http` dan `https` dengan host yang lolos; flag, `file:`, `javascript:`, `chrome:`, dan `about:` ditolak (`400 bad_url`). URL dibentuk ulang dan ditaruh setelah field code `Exec` diselesaikan, jadi `%` di dalamnya tidak terbaca sebagai field code. Browser bisa menjangkau semua yang bisa dijangkau This PC, termasuk layanan `localhost` dan jaringan lokal, setara akses terminal yang sudah ada (§1).

**Node.** `data` web tile mendapat `sessionId`, `appId`, `name` di samping `url`. `url` adalah alamat yang diketik, bukan halaman saat ini: halaman saat ini ada di address bar browser dan judul sesi.

- Tile baru memulai browser hanya setelah alamat diketik. Board yang dibuka lagi, node lama, dan node dari planner menampilkan tombol **Open here**, tidak pernah memulai browser sendiri.
- Tombol power, atau "Close and clear page" di menu, menutup browser. Menghapus tile juga menutupnya, undo mengembalikan tile dalam keadaan tertutup. Salinan tile tidak membawa `sessionId`.
- Gagal dibuka (paket belum ada, bukan Linux): pesan hub tampil di tile dengan pilihan **Browser tab** dan **Link card**.
- Klien yang belum mengenal jalur ini tetap menampilkan node dari `url`-nya sebagai situs eksternal.

**Batasan.** §6 berlaku, terutama Ctrl+T, Ctrl+W, dan Ctrl+N yang ditahan browser pengguna: pakai tombol `+` dan `×` di Chrome. Situs melihat alamat IP This PC. Login tersimpan di `<data>/gui/profiles/<appId>/` (§1). Dua situs dengan satu login tetap bisa berdampingan dalam satu tile lewat Split View bawaan Chrome (klik kanan tab, "New split view with current tab"; teruji di Chrome 156).

**Memori.** Ukur di This PC, median 3 kali, GitHub dan Wikipedia, termasuk `sway` dan `wayvnc`, belum termasuk encoder: Split View dalam satu tile 740 MB; satu Chrome dengan dua jendela di dua output headless 771 MB (spike, belum ada di produk); dua Chrome dengan dua profil 971 MB. Selisih antar-run sekitar 100 MB.

**Verifikasi (This PC, 2026-11-03):** `go test ./internal/gui -run 'TestWebURL|TestBrowserChoice|TestOpenURL'`, `internal/server TestGUIBrowserOpen`. Chrome 156 sungguhan di display Wayland, dikendalikan Playwright lewat hub dan build web sementara: github.com, Wikipedia, example.com, iana.org; mengetik di address bar Chrome dari tile; tab kedua; hub restart lalu tab baru; tutup, reload board tanpa Chrome baru, buka lagi; hapus tile.

## 9. Display tanpa salinan

Dengan Xtigervnc, gambar berjalan GPU → CPU → GPU: aplikasi merender di GPU, X server membacanya keluar, ffmpeg menyalin dan mengonversinya, lalu mengunggahnya ke encoder. Hub memilih display Wayland kalau `sway`, `wayvnc`, `wf-recorder`, GPU, dan encoder hardware tersedia, dan X11 kalau tidak. Tiap sesi punya compositor sendiri:

- `sway` headless (`WLR_BACKENDS=headless`) merender di GPU. Aplikasi menyerahkan buffer GPU kepadanya.
- `wf-recorder` meneruskan buffer itu ke encoder tanpa keluar dari GPU, dan hanya saat layar berubah.
- `wayvnc` melayani RFB untuk input, kursor, clipboard, resize, dan gambar saat diam, lewat bridge dan viewer yang sama.

Hasil ukur scroll teks 1280×800 dengan AV1, dalam persen satu core:

| | X11 | Wayland |
|---|---|---|
| Aplikasi (Chrome) | 42 | 30–43 |
| Display (Xtigervnc / sway) | 18 | 6 |
| Encoder (ffmpeg / wf-recorder) | 44 | 8 |
| RFB saat mengamati satu piksel (wayvnc) | – | 6 |
| **Total** | **~104** | **50–65** |
| GPU render / video-enhance | 28 % / 16 % | 20 % / 4 % |
| Respons klik di halaman tenang | 32 ms | 32 ms |
| Respons klik saat scroll, localhost | 83–98 ms | 110–130 ms |

Harga display ini ada di baris terakhir: saat gerakan besar, respons klik sekitar 30 ms lebih lambat, karena gambar menunggu giliran frame compositor. Mengetik dan klik biasa tidak terpengaruh. `max_render_time 4` sudah menghemat 9 ms dari selisih itu; `wf-recorder --no-damage` tidak terbukti membantu. Kalau ini lebih penting daripada CPU, `NEXTBOARD_GUI_BACKEND=x11` kembali ke display lama.

Di link terbatas, display Wayland tetap jauh di atas RFB saja:

| Link | AV1 | H.264 |
|---|---|---|
| 20 Mbps, RTT 30 ms | 42 fps, 167 ms, 3,7 Mbps | 46 fps, 163 ms, 6,3 Mbps |
| 5 Mbps, RTT 60 ms | 42 fps, 195 ms, 2,2 Mbps | 42 fps, 215 ms, 3,8 Mbps |

**Yang berbeda dari X11:**

- **Aplikasi:** keluarga Chromium dan VS Code berjalan native (`--ozone-platform=wayland`) tanpa X server sama sekali. Aplikasi lain mendapat Xwayland sebagai cadangan.
- **Xwayland** dari wlroots tidak memakai cookie, jadi user lokal mana pun bisa menyambung. Hub menjalankannya sejak awal (`xwayland force`) dan menyetel daftar aksesnya ke `localuser:<user hub>` lewat `ChangeHosts` sebelum aplikasi jalan. Kalau gagal, sesi tidak dibuka. Daftar itu akan hilang kalau Xwayland restart, makanya tidak memakai mode lazy.
- **Warna:** konversi GPU selalu menghasilkan BT.601, jadi stream ditandai `smpte170m`, dan warnanya tepat.
- **Rate control:** `wf-recorder` memakai time base mikrodetik, dan encoder VA-API akan mengira 1 juta fps tanpa `-r 60`, sehingga teks jadi kotak-kotak.
- **Latensi pipe:** avio menahan data sampai 32 KB, jadi output memakai muxer `tee` dengan `flush_packets=1`.
- **RFB:** viewer mengutamakan ZRLE, karena `wayvnc` tidak punya lossless refresh seperti Xtigervnc. Teks saat diam tetap persis. Tombol dikirim sebagai keysym (tanpa `QEMUExtendedKeyEvent`) supaya layout keyboard selain US benar. Teks paste dikirim sebagai `Provide` tanpa `Notify`, karena alur `Notify` butuh satu RTT dan tombol Ctrl+V akan sampai duluan.
- **Crash `wayvnc`:** `wayvnc` 0.9.1 sesekali segfault. Hub menyalakannya ulang dalam ~2 detik, viewer tersambung lagi sendiri, dan aplikasi tidak terganggu.
- **`XDG_RUNTIME_DIR`:** compositor memakai folder privat `$XDG_RUNTIME_DIR/nb-<id>`. Aplikasi tetap memakai folder aslinya karena butuh PipeWire, dengan `WAYLAND_DISPLAY` berisi path absolut.
