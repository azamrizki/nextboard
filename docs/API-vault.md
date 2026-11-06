# Credentials (vault)

Referensi: [Bitwarden](https://bitwarden.com). Password manager di board: kartu `credentials` menampilkan satu vault milik hub. Paket: `internal/vault`, handler `internal/server/h_vault.go`, klien `web/src/vault/` dan `web/src/board/tiles/CredentialsTile.tsx`.

## Kenapa enkripsi di browser

Agent di This PC jalan sebagai user yang sama dengan hub, jadi bisa membaca `<data>` (PROJECT §6). Kalau kuncinya di `secret.key` seperti kredensial SSH, setiap agent bisa membaca setiap password. Karena itu vault mengikuti cara Bitwarden: **master password tidak pernah keluar dari browser**, dan hub hanya menyimpan ciphertext.

```
master password --Argon2id--> kunci pembungkus --AES-256-GCM--> kunci vault --AES-256-GCM--> item
```

| Bagian | Nilai |
|---|---|
| KDF | Argon2id, 64 MiB, 3 pass, paralelisme 4 (bawaan Bitwarden), salt 16 byte, keluaran 32 byte. Password dinormalisasi NFKC dulu. Jalan di worker (`hash-wasm`) |
| Kunci vault | 32 byte acak, tidak pernah berganti. Setelah dibuka hanya di memori: `CryptoKey` non-extractable di HTTPS dan localhost, byte biasa di HTTP biasa (lihat di bawah) |
| Bungkus kunci | AES-256-GCM, AAD `nextboard-vault/v1/key`. Bentuk: nonce 12 + ciphertext 32 + tag 16, base64 |
| Item | AES-256-GCM, AAD `nextboard-vault/v1/item/<id>`, jadi blob tidak bisa dipindah ke id lain. JSON diberi padding spasi sampai kelipatan 512 byte, supaya ukuran blob tidak membocorkan panjang isinya |

Password salah ditandai oleh tag GCM yang gagal: hub tidak menyimpan hash apa pun dan tidak pernah tahu ada percobaan unlock.

Formatnya bisa dibuka tanpa kode NextBoard: `crypto.argon2Sync` Node + AES-GCM sudah cukup, dan itu yang dicek tes end-to-end.

## HTTP biasa

Hub ini local-first dan sering dibuka lewat `http://` di jaringan sendiri, jadi vault jalan di sana juga (keputusan pemilik). Browser menyembunyikan `crypto.subtle` dan `navigator.clipboard` di luar HTTPS dan localhost, sehingga:

| Bagian | HTTPS, localhost | HTTP biasa |
|---|---|---|
| Argon2id | `hash-wasm` di worker | Sama |
| AES-256-GCM | WebCrypto, kunci non-extractable | `@noble/ciphers`, kunci berupa byte di memori, ditimpa nol saat terkunci. Dimuat hanya di sini |
| Acak | `crypto.getRandomValues` | Sama |
| TOTP | `@noble/hashes` | Sama |
| Salin | `navigator.clipboard` | `execCommand`, langsung dari klik |
| Menghapus clipboard | Tepat waktu | Browser hanya mengizinkannya sekitar 5 detik setelah input, jadi lewat dari itu terjadi pada klik atau tombol berikutnya di halaman ini. Diisi satu spasi. Toast dan dialog pengaturan menyebutnya |

Byte hasilnya sama persis, jadi satu vault bisa dibuka dari dua sisi; tes end-to-end menulis di satu sisi dan membuka di sisi lain, ke dua arah. Layar buat vault dan unlock menyebut risikonya di bawah kolom password.

## Isi blob

Tipe mengikuti Bitwarden: `login`, `card`, `identity`, `note`, `ssh`. Field per tipe ada di `TYPE_SCHEMA` (`web/src/vault/model.ts`).

```jsonc
{ "k": "item", "type": "login", "name": "GitHub", "notes": "", "favorite": false, "folderId": "",
  "values": { "username": "", "password": "", "totp": "" }, "uris": [""],
  "fields": [{ "name": "", "value": "", "hidden": true }],
  "history": [{ "password": "", "at": "…" }],   // ≤ 5, password login sebelumnya
  "createdAt": "…", "updatedAt": "…", "deletedAt": "…" }   // deletedAt = di trash
```

Folder juga record terenkripsi: `{ "k": "folder", "name": "…" }`. Hub tidak bisa membedakan item dari folder, dan tidak tahu nama, tipe, folder, atau status trash.

Trash ada di dalam blob, jadi browser yang menghapus: item yang lebih dari 30 hari di trash dihapus saat unlock berikutnya.

## API

Semua lewat login biasa, ber-header `X-NextBoard: 1` untuk yang mengubah data. Yang bisa mengunci pemilik keluar hanya untuk admin, bukan token device.

| Route | Akses | Isi |
|---|---|---|
| `GET /api/vault` | login | `{"exists","header","items":[{"id","blob","rev","updatedAt"}],"rev"}` |
| `POST /api/vault` | admin | Buat vault. Body `{"kdf":{"name":"argon2id","m","t","p"},"salt","wrappedKey"}` → 201 header. 409 `vault_exists` |
| `PUT /api/vault/key` | admin | Ganti master password: body sama + `baseKeyRev`. Item tidak ditulis ulang. 409 `conflict` |
| `PUT /api/vault/items/{id}` | login | `{"blob","baseRev"}`, `baseRev` 0 untuk item baru. 409 `conflict` bila sudah berubah |
| `DELETE /api/vault/items/{id}?baseRev=` | login | Hapus permanen. 404 `item_not_found`, 409 `conflict` |
| `POST /api/vault/batch` | login | `{"items":[{"id","blob"}]}`, untuk import, semua atau tidak sama sekali |
| `POST /api/vault/reset` | admin | `{"confirm":"RESET"}` → `{"ok","kept"}` |
| `POST /api/vault/pins` | login | `{"proof"}` → 201 `{"id","secret"}` (lihat PIN) |
| `POST /api/vault/pins/{id}/open` | login | `{"proof"}` → `{"secret"}`. Salah: 403 `wrong_pin` + `left`. Habis: 410 `pin_gone` |
| `DELETE /api/vault/pins/{id}` | login | Matikan PIN satu browser |
| `DELETE /api/vault/pins` | admin | Matikan semua PIN, untuk browser yang hilang |

Validasi di hub hanya bentuk: id `^[A-Za-z0-9_-]{8,64}$`, blob base64 ≥ 28 byte, parameter KDF di dalam batas (m 32 MiB–1 GiB, t 2–16, p 1–16). Blob yang bukan ciphertext ditolak sebagai base64, tapi hub tidak bisa memastikan isinya memang terenkripsi: itu tugas klien.

Event `/api/events`: `{"type":"vaultChanged","rev","vaultId"}`, tanpa isi. Tab lain mengambil ulang dan membuka yang berubah saja. `vaultId` berganti saat reset, dan tab yang masih memegang kunci lama terkunci.

Kode error lain: `vault_absent` 404, `vault_full` 413, `vault_unreadable` 500.

## PIN

Unlock dengan 6 digit, per browser. Satu juta kemungkinan tidak boleh melindungi kunci sendirian: kalau hub menyimpan bungkusannya, agent yang membaca `<data>` tinggal mencoba semua; kalau browser saja, siapa pun yang menyalin storage-nya bisa. Jadi butuh keduanya:

```
browser (localStorage nb.vault.pin):  salt, kunci vault dibungkus secret
hub (vault.json, pins):               kunci acak per PIN, jumlah salah
proof  = Argon2id(PIN, salt)          parameter sama dengan master password
secret = HMAC-SHA256(kunci hub, proof)
```

- Hub menyerahkan `secret` hanya untuk `proof` yang benar, dan menghitung yang salah. **5 kali salah berturut-turut: PIN hangus**, master password tetap bisa. Hitungannya disimpan sebelum dijawab, jadi restart hub tidak mengembalikan jatah.
- Hub tidak pernah melihat PIN, tidak menyimpan `secret` maupun `proof`, dan tanpa salt dari browser tidak bisa menguji tebakan. Browser tidak punya apa-apa yang bisa diuji tanpa hub.
- Menyiapkan PIN meminta master password, karena kunci yang sedang terbuka tidak bisa dibaca balik.
- PIN seperti `123456`, `111111`, `121212` ditolak.
- **Ganti master password dan reset mematikan semua PIN.** Browser lain tahu saat mencoba berikutnya. "Turn off everywhere" untuk browser yang hilang: bagiannya sendiri tidak membuka apa pun lagi.

## Mengisi form

Tombol kunci di browser tile. **Selalu atas pilihan orangnya**, tidak pernah saat halaman dimuat: halaman yang terisi sendiri bisa mengumpulkan tanpa ada yang melihat. Hanya login; kartu dan identitas belum. Tidak pernah submit. Vault terkunci: dialognya meminta PIN atau password di tempat.

**Web tile (proxy hub).** Pesan `fill` ke bridge ([API-studio §4.5](API-studio.md)), yang mengisi field dengan setter asli lalu mengirim `input` dan `change`, jadi React dan sejenisnya melihatnya seperti ketikan.

- Situsnya dari target preview, bukan dari yang dilaporkan halaman. Login yang disimpan untuk host lain butuh klik kedua, karena begitulah wujud phishing. Cocok berarti host sama atau subdomainnya, dan port sama kalau alamatnya menyebut port. `localhost`, `127.0.0.1`, `::1` dianggap sama.
- Field yang tidak terlihat tidak diisi: `display:none`, `opacity` nyaris 0, di luar layar, ukuran di bawah 4 px.
- Login dua langkah, kode TOTP (satu field atau kotak per digit), dan shadow DOM terbuka didukung.

**Chrome di This PC, dan tile aplikasi lain.** Hub tidak bisa melihat ke dalamnya, jadi nilainya masuk ke field yang sedang fokus: username, Tab, password. Dialog menyebut judul jendela dan meminta memeriksa address bar, karena dari sini **situsnya tidak bisa dipastikan**; judul hanya dipakai untuk mengurutkan.

- Lewat clipboard display itu lalu Ctrl+V, bukan diketik: wayvnc diam-diam membuang karakter yang tidak ada di keymap-nya (`$`, `ä`, `я`), dan password yang kurang satu huruf salah tanpa terlihat. Tempel berhasil utuh atau gagal terlihat. Clipboard display ditimpa setelah sekitar 400 ms, dan tidak kembali ke clipboard browser pengguna.
- CDP tidak dipakai. `--remote-debugging-port` membuka port lokal yang bisa dipakai user lain di host bersama untuk mengendalikan browser yang sudah login, dan `--remote-debugging-pipe` menutup Chrome saat hub restart. `--load-extension` sudah dicabut dari Chrome bermerek.

## Batas

≤ 5000 record (item + folder), satu blob ≤ 64 KB base64, seluruh vault ≤ 64 MB, batch ≤ 1000 item dan body ≤ 16 MB (klien memecah per 500 item / 8 MB), body item 128 KB. ≤ 32 PIN; lewat dari itu yang paling lama hilang.

## Penyimpanan

`<data>/vault.json` 0600, ditulis utuh dan atomik di setiap perubahan, disimpan dulu baru berlaku. File sebelumnya tetap sebagai `vault.json.prev`. Terbaca rusak: file rusak dipindah ke `vault.corrupt-<waktu>.json` dan `.prev` dipakai. Dua-duanya rusak: vault menjawab `vault_unreadable` dan **menolak menulis di atasnya**; hub tetap jalan, dan hanya reset yang mengeluarkannya.

Reset memindahkan file ke `vault.reset-<waktu>.json`, tidak menghapusnya: kalau password teringat lagi, file itu masih bisa dibuka (taruh kembali sebagai `vault.json`).

File API sudah menolak `<data>`. Cadangan hub cukup menyalin `vault.json`: isinya aman selama master password aman.

## Perilaku di browser

- **Kunci hanya di memori tab.** Tidak ada localStorage atau sessionStorage untuk apa pun yang rahasia. Reload, logout, `pagehide`, dan idle mengunci. Tiap tab unlock sendiri. Kunci menolak dipakai setelah terkunci, dan form atau dialog yang terbuka ikut hilang, tidak muncul lagi saat unlock. Import yang sedang jalan berhenti di situ; yang sudah terkirim tetap ada.
- **Idle:** bawaan 15 menit tanpa menyentuh kartu Credentials (1 menit sampai 4 jam, per browser). Kerja di console tidak dihitung, jadi berjam-jam di terminal tidak membiarkan vault terbuka. Lock lewat tombol mengunci semua tab (`BroadcastChannel`).
- **Clipboard:** rahasia dihapus dari clipboard 30 detik setelah disalin (bisa diatur, atau "Never"), dan saat vault terkunci. Browser tidak memberi izin membaca clipboard tanpa bertanya, jadi yang disalin dari aplikasi lain dalam waktu itu ikut terhapus; dialog pengaturannya menyebut ini. Username dan alamat tidak dihapus.
- **Tertutup berarti tidak ada di DOM:** nilai yang dimask tidak dirender sama sekali, dan mask tidak mengikuti panjang aslinya.
- **Master password** minimal 8 karakter (keputusan pemilik). Skor zxcvbn di bawah 3 hanya diberi peringatan, tidak ditolak, walau yang punya `vault.json` bisa menebak secara offline. Tombol di form mengusulkan 6 kata BIP-39 (66 bit).
- **Tidak ada permintaan keluar:** tidak ada ikon situs (Bitwarden mengambilnya dari server ikon, yang membocorkan domain). Hanya `http` dan `https` yang menjadi tautan, jadi alamat `javascript:` dari file import tetap teks.

## Board dan agent

Node `credentials` hanya menyimpan `{"title": "…"}`. Board disinkronkan, di-snapshot, dan bisa dibaca agent, jadi tidak ada satu pun rahasia di sana. Kartu ini sengaja **tidak** ada di `contentTiles` dan `CONTENT_SOURCES` (tidak punya knob kabel), `layoutNodeKinds`, atau `node.add` planner; `internal/orch/credentials_test.go` menjaganya.

Menghapus kartu tidak menyentuh vault, dan semua kartu di semua board menampilkan vault yang sama.

Agent hanya bisa memakai item lewat satu jalan, yang dijelaskan di bagian berikut.

## Agent memakai credential

Keputusan #40. Agent tidak pernah menerima nilai. Ia meminta hub menjalankan **satu perintah** yang mendapat nilai dari vault, pemilik menyetujuinya di browser, dan output yang kembali ke agent sudah disensor.

```
agent    credential_run{command, why, secrets}  →  hub: permintaan "waiting", notifikasi
browser  buka item, cek blob, kirim nilai        →  hub: jalankan perintah itu
hub      output dengan nilai diganti ••••        →  agent
```

### Menandai item

- Di form item: **Agents may ask to use it**, lalu pilih project. Pilihan ini tersimpan di blob item (`"agents": {"projects": […]}`), jadi hub tidak bisa mengubahnya.
- Browser juga menulis **katalog** ke `vault.json`: nama item, tipe, project, dan nama nilainya, tanpa nilai. Item yang tidak ditandai tetap tidak terlihat, termasuk namanya.
- Nilai yang bisa diberikan: login → `username`, `password`; catatan → `note`; kunci SSH → `privateKey`, `publicKey`; semua tipe → field custom menurut namanya. Field bawaan kartu dan identitas tidak, begitu juga kunci TOTP.
- Katalog hanya petunjuk untuk agent. Setiap unlock, browser menyamakannya dengan blob, dan entri yang tidak didukung blob dihapus.

### Tool agent

Console di project yang punya item tertanda mendapat tiga tool hub. Project tanpa item tertanda tidak mendapat tool apa pun, jadi definisinya tidak memakan token. CLI membaca daftar tool saat mulai, sehingga console yang sudah jalan sebelum item pertama ditandai perlu dibuka ulang.

| Tool | Isi |
|---|---|
| `credential_list` | Nama, tipe, id, dan nama nilai item untuk project ini |
| `credential_run{command, why, secrets[{item, value, env}], stdin?{item, value}, timeout_ms?}` | Membuat permintaan dan menunggu ≤ 45 detik. Selesai: output. Belum: id untuk `credential_result` |
| `credential_result{id}` | Menunggu lagi ≤ 45 detik. Hanya untuk console yang meminta |

- Menunggu dibatasi 45 detik karena Codex memutus panggilan MCP setelah 60 detik.
- Claude Code dan OpenCode tidak meminta izin untuk tiga tool ini (`--allowedTools`, `permission`), karena persetujuan ada di dialog hub. Codex mengikuti aturan izinnya sendiri.
- Ditolak sebelum sampai ke pemilik: item atau nilai yang tidak ada di katalog project; nama env yang bukan nama variabel atau yang mengubah perilaku shell (`PATH`, `HOME`, `SHELL`, `IFS`, `ENV`, `BASH_ENV`, `LD_*`, `DYLD_*`, `NB_*`, dan sejenisnya); env ganda; tanpa `why`; perintah > 4000 byte; > 8 nilai; dan perintah di daftar blokir (keputusan #38).
- Paling banyak 3 permintaan menunggu per console, 20 untuk seluruh hub.

### Persetujuan

- Notifikasi `attention` dengan `kind: "credential"`, dan pill di atas halaman. Dialog menampilkan agent, project, machine dan folder, alasan, batas waktu, perintah persis, dan nilai apa masuk ke env mana.
- Dialog membuka item **dari blob**, bukan dari katalog. Kalau item tidak ditandai untuk project itu, ada di trash, atau nilainya sudah tidak ada, tombol **Allow once** mati. Entri katalog palsu tidak membuka apa pun.
- Vault terkunci: dialog meminta PIN atau master password di tempat.
- Satu persetujuan berlaku untuk perintah itu, sekali. Yang tidak dijawab dalam 10 menit kedaluwarsa.

### Menjalankan

- Hub menjalankan perintah sebagai prosesnya sendiri, di machine dan folder console (server untuk console `remoteTools`), dalam login shell baru. Batas waktu bawaan 2 menit, maksimal 10.
- Nilai untuk env dikirim lewat **stdin** ke skrip pembuka yang mengekspornya: tiap byte ditulis `\0ooo`, lalu dikembalikan dengan `printf %b`. Nilai tidak pernah ada di baris perintah, karena `ps` di server menampilkannya ke semua user. Terbukti utuh di sh, dash, bash, zsh, dan busybox, termasuk kutip, `$`, baris baru, dan unicode.
- Nilai untuk `stdin` diteruskan apa adanya ke input perintah.
- Nilai < 4 karakter ditolak, karena tidak bisa disembunyikan tanpa merusak output.

### Sensor

Sebelum sampai ke agent, stdout dan stderr diganti `••••` di setiap tempat ada:

- nilainya, juga tanpa baris baru di akhir;
- tiap baris ≥ 8 karakter dari nilai multi-baris;
- bentuk base64 (empat varian), hex (huruf kecil dan besar), URL-encoded (query dan path), dan isi string JSON.

Output disimpan paling banyak 1 MB per aliran. Kalau terpotong, awal nilai yang tertinggal di ujung ikut dibuang. Setelah itu output dipotong ke 30 000 karakter seperti tool `bash` hub.

### Yang disimpan

- Permintaan hanya ada di memori: hilang saat hub restart, dan agent diminta mengulang. Nilai hanya ada di memori hub selama satu run.
- `<data>/credential-log.jsonl` (0600, diputar ke `.1` di 1 MB): siapa, kapan, perintah, item, nama nilai, env, hasil, exit code, durasi, dan siapa yang menyetujui. Tanpa nilai dan tanpa output.
- Event `credentialRequest` `{id, state}`, tanpa isi.

### API

Semua lewat login biasa. Menghapus item menghapus entrinya, dan reset vault menghapus katalog.

| Route | Isi |
|---|---|
| `GET /api/vault/agents` | Katalog |
| `PUT /api/vault/agents/{id}` | `{name, type, projects, values}`; `projects` kosong menghapus. Item harus ada (404), project harus ada (400) |
| `GET /api/vault/requests` | Permintaan yang menunggu, sedang jalan, atau selesai < 15 menit lalu |
| `POST /api/vault/requests/{id}/approve` | `{values}`, urut seperti `uses`. 409 `request_decided`, 404 `request_not_found` |
| `POST /api/vault/requests/{id}/deny` | `{note?}` ≤ 300 karakter, dibacakan ke agent |
| `GET /api/vault/agent-log?limit=` | Log, terbaru dulu, ≤ 500 |

### Batas

**Ini melindungi dari kecelakaan, bukan dari agent yang sengaja mencuri:**

- Perintah yang menjalankan script di project (`npm run`, `make`, `./deploy.sh`) menjalankan isi script saat itu, dan agent bisa mengubahnya. Yang disetujui adalah nama perintahnya.
- Selama perintah jalan, agent dengan Bash sebagai user yang sama bisa membaca env proses itu dari `/proc`.
- Sensor tidak mengenali nilai yang diubah bentuknya: dibalik, dipotong, atau dienkripsi.
- Nilai lewat jaringan dari browser ke hub. Di HTTP biasa, siapa pun yang bisa membaca lalu lintas bisa melihatnya.
- Nama item yang ditandai terlihat oleh agent project itu, dan oleh siapa pun yang membaca `<data>`.

Mengisi form browser untuk agent tidak dibuat. Agent tidak bisa mengendalikan browser tile, jadi mengisi untuknya sama saja dengan tombol kunci yang sudah ada.

## Import dan export

Import: JSON tidak terenkripsi dan CSV dari Bitwarden, CSV dari Chrome dan Firefox. Dibaca dan dienkripsi di browser sebelum dikirim. Export terenkripsi Bitwarden ditolak dengan penjelasan. Passkey tidak ikut.

Export: format JSON Bitwarden tanpa enkripsi, setelah master password dimasukkan lagi. Trash tidak ikut.

## Yang tidak dilindungi

- **Lupa master password = semua hilang.** Tidak ada pemulihan.
- **HTTP biasa.** Siapa pun yang bisa mengubah lalu lintas antara browser dan hub bisa menyuntikkan skrip dan menangkap master password, lalu membuka vault. Password login hub lewat jalur yang sama. Aman di localhost dan di dalam Tailscale atau VPN; berisiko di Wi-Fi bersama dan melalui internet tanpa HTTPS.
- **Tebakan offline.** Agent atau siapa pun yang membaca `<data>` mendapat `vault.json` dan bisa mencoba menebak. Argon2id memperlambatnya (sekitar 0,3 detik per tebakan per inti), kekuatan password yang menentukan: daftar password umum selesai dalam hitungan menit, dan password 8 karakter yang lemah termasuk di dalamnya.
- **PIN kalau kedua sisinya jatuh ke tangan yang sama.** Siapa pun yang memegang storage browser dan folder hub bisa mencoba sejuta PIN tanpa batas hitungan hub, sekitar 3–4 hari satu inti dan lebih cepat dengan banyak inti. Ini terjadi kalau browsernya di This PC, karena agent bisa membaca keduanya.
- **Mengisi form melepas rahasianya.** Halaman yang diisi bisa membaca nilainya, itu sifat semua autofill. Lewat proxy, password yang dikirim situs melewati proses hub. Di Chrome This PC, sesi yang login tersimpan di profilnya, dan agent bisa membacanya. Jangan simpan password di Chrome itu (`--password-store=basic`); dialog mengingatkan.
- **Kode aplikasinya sendiri.** Agent dengan Bash sebagai user yang sama bisa mengganti binary hub atau file web, lalu menangkap master password pada unlock berikutnya. Enkripsi di browser melindungi dari membaca file, bukan dari orang yang bisa mengganti programnya. Web vault Bitwarden yang di-host sendiri punya batas yang sama. Untuk rahasia yang tidak boleh terjangkau agent yolo, pakai perangkat lain.
- **Saat vault terbuka,** isinya ada di memori tab, dan di HTTP biasa kuncinya juga bisa dibaca dari sana. XSS di app hub, dengan CSP yang ketat sekalipun, atau ekstensi browser bisa membacanya.
- **Hub bisa mengembalikan versi lama.** Blob lama tetap sah secara kriptografi, jadi hub yang jahat bisa menyembunyikan perubahan terbaru. Sama seperti Bitwarden.
- **Metadata yang terlihat hub:** jumlah record, ukuran blob per 512 byte, dan waktu perubahan. Untuk item yang ditandai untuk agent, juga nama, tipe, project, dan nama nilainya.
- **Agent memakai credential** melindungi dari kecelakaan, bukan dari agent yang sengaja mencuri (lihat batasnya di atas).

## Belum ada

Passkey, mengisi kartu dan identitas, kartu Android dan desktop, kode Steam (kuncinya tersimpan, tapi tidak menjadi kode), dan pemeriksaan kebocoran password.
