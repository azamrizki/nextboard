# NextBoard — design system

**Intent (satu kalimat):** sistem netral monokrom dengan tema terang dan gelap: warna utama adalah tintanya sendiri, border hairline, font Geist, chrome kaca tipis yang mengambang, dan konten kerja (terminal, form, data) di permukaan padat berkontras tinggi supaya cepat dibaca.

Berlaku untuk web (`nextboard/web`, juga dipakai app desktop lewat proxy). Layar Hubs desktop (`nextboard-desktop/src`) dan Android (`nextboard-android`, Compose) masih memakai palet biru lama dan hanya gelap: belum dipindah karena fokus sekarang di hub (lihat keputusan 27 di ROADMAP). `web/src/styles/tokens.css` adalah sumber kebenaran untuk nilainya; tabel di bawah hanya yang paling sering dipakai. Nilai mentah di luar token **tidak boleh** dipakai di komponen.

## 1. Konteks dan tujuan

- **Dua tema, satu komponen.** Komponen hanya memakai token, tema hanya mengganti nilainya. `tokens.css` punya tiga blok: `:root` (ukuran, tipografi, motion), `:root, [data-theme='dark']`, dan `[data-theme='light']`. Token warna baru wajib ada di kedua blok warna.
- **Pilihan per browser:** System (bawaan), Light, Dark di Settings › Appearance; tombol matahari/bulan di header untuk pindah cepat. `data-theme` dipasang di `<html>` sebelum paint pertama oleh `public/theme-boot.js` (berkas terpisah karena CSP hub melarang script inline), lalu diteruskan `lib/theme.ts`.
- **`data-theme` berlaku di elemen mana pun:** subtree bisa tetap gelap di halaman terang. Dipakai langit Memory (selalu gelap) dan console dengan pilihan "Always dark".
- **Warna hanya untuk makna:** status, agent, kode, dan yang digambar orang. Sisanya abu-abu.
- **Satu metafora saja: kaca.** Tidak dicampur neumorphism, skeuomorphism, atau flat-outline. Kedalaman = blur + transparansi + border cahaya, bukan bayangan tebal.
- **Kaca untuk chrome, padat untuk konten.** Header, toolbar/dock, kartu, modal, menu, toast = kaca. Isi terminal, area teks panjang, dan input = permukaan padat/hampir padat.
- **Kepadatan nyaman** (comfortable): kontrol 40 px, celah antar-field 16 px, padding kartu 20 px.
- **Bento**: halaman daftar (Machines, Devices, langkah Setup) memakai grid kartu dengan ukuran berbeda sesuai bobot informasi.

## 2. Token dan fondasi

### 2.1 Warna dasar

Warna utama adalah tinta: tombol utama, pilihan aktif, dan ring fokus putih di tema gelap, hitam di tema terang.

| Token | Gelap | Terang | Pakai untuk |
|---|---|---|---|
| `--c-primary` | `#F5F5F5` | `#0D0D0D` | isi tombol utama, pilihan aktif (segmented, tool rail) |
| `--c-primary-300` | `#E5E5E5` | `#262626` | garis, titik, dan teks penanda yang aktif |
| `--c-primary-600` | `#D4D4D4` | `#2E2E2E` | hover tombol utama |
| `--c-success` | `#22C55E` | `#15803D` | status running/idle/busy |
| `--c-warning` | `#F59E0B` | `#B45309` | reconnecting/starting, peringatan |
| `--c-danger` | `#EF4444` | `#DC2626` | aksen error, border error |
| `--c-danger-300` | `#F87171` | `#B91C1C` | teks error kecil (≥ 4.5:1) |
| `--c-danger-600` | `#D13030` | `#C52020` | isi tombol destruktif |
| `--c-info` | `#A3A3A3` | `#6B6B6B` | info netral: tidak ada biru |
| `--c-grey` | `#8C8C8C` | `#737373` | status exited (kode 0) |

### 2.2 Teks

| Token | Gelap | Terang | Peran |
|---|---|---|---|
| `--text` | `#F5F5F5` | `#0D0D0D` | judul, isi |
| `--text-2` | `#A8A8A8` | `#525252` | label, teks sekunder |
| `--text-3` | `#8C8C8C` | `#6B6B6B` | hint, placeholder, metadata |
| `--text-on-primary` | `#0A0A0A` | `#FFFFFF` | di atas `--c-primary` saja |
| `--text-on-danger` | `#FFFFFF` | `#FFFFFF` | di atas isi danger |
| `--text-on-color` | `#FFFFFF` | `#FFFFFF` | di atas warna agent atau project |
| `--text-on-warning` | `#1C1204` | `#FFFFFF` | di atas `--c-warning` |

`--text-3` adalah batas bawah untuk teks. Tidak boleh ada teks yang lebih redup dari itu (contoh salah: placeholder `rgba(255,255,255,.3)`).

### 2.3 Latar dan lapisan kaca

| Token | Gelap | Terang |
|---|---|---|
| `--bg` | `#0A0A0A` | `#FAFAFA` |
| `--bg-ambient` | sorot netral lembut di atas | sama, lebih tipis |
| `--page-grid` | grid 64 px samar di belakang halaman, memudar ke bawah; tidak di board (sudah ada titik) dan Memory | sama |
| `--glass-1` | `rgba(17,17,17,.66)` chrome (header, dock, toolbar) | `rgba(255,255,255,.72)` |
| `--glass-2` | `rgba(19,19,19,.86)` kartu, modal | `rgba(255,255,255,.94)` |
| `--glass-3` | `rgba(23,23,23,.9)` menu, popover, toast | `rgba(255,255,255,.96)` |
| `--glass-border` | `rgba(255,255,255,.09)` | `rgba(0,0,0,.09)` |
| `--glass-border-strong` | `rgba(255,255,255,.17)` | `rgba(0,0,0,.17)` |
| `--surface-solid` | `#0F0F0F` | `#FFFFFF` |
| `--tile-bg` | `#111111` | `#FFFFFF` |
| `--surface-hover` | `rgba(255,255,255,.07)` | `rgba(0,0,0,.05)` |
| `--surface-active` | `rgba(255,255,255,.11)` | `rgba(0,0,0,.085)` |
| `--backdrop` | `rgba(0,0,0,.6)` + `blur(6px)` | `rgba(10,10,10,.3)` + `blur(6px)` |

Blur: `--glass-blur-1` `blur(18px) saturate(140%)`, `-2` `blur(24px) saturate(150%)`, `-3` `blur(14px) saturate(130%)`.

Fallback wajib: `@supports not (backdrop-filter: blur(1px))` dan `@media (prefers-reduced-transparency: reduce)` → semua `--glass-*` diganti `--solid-1/2/3` tema itu.

**Warna yang disimpan di board** (`#E6EAF2` "Chalk" dan tujuh swatch lain) tetap hex di data, karena board dibagi antar klien yang temanya bisa beda. Saat digambar `inkColor()` (`board/palette.ts`) memetakannya ke `--ink-*`, jadi Chalk jadi tinta gelap di kanvas putih. Warna lain tampil apa adanya. Kertas sticky note sama di kedua tema.

**Chart:** `--viz-1..4` dipilih terpisah per tema, bukan dibalik, dan lolos validator dataviz di atas `--tile-bg` masing-masing.

### 2.4 Tipografi

- Font: **Geist** (UI & display), **Geist Mono** (path, fingerprint, kode di kartu), **JetBrains Mono** (hanya console dan Monaco, karena cakupan glyph-nya lebih luas untuk TUI). Fitur `ss01` dan `cv11` aktif seperti referensi. Dibundel lokal (web: `@fontsource-variable/*`), tidak dari CDN — CSP hub hanya mengizinkan `font-src 'self' data:`.
- Skala (mobile-first, compact): `--fs-xs 12px` · `--fs-sm 13px` · `--fs-md 14px` (isi) · `--fs-lg 16px` · `--fs-xl 18px` · `--fs-2xl 22px` · `--fs-3xl 28px`.
- Bobot: 400 isi, 500 label/tombol, 600 judul kartu dan halaman, 800 brand. Line-height 1.5 isi, 1.25 judul. Tracking rapat di judul: `--tracking-title -0.025em` (h1), `--tracking-snug -0.012em` (judul kecil).
- Angka tabular (`font-variant-numeric: tabular-nums`) untuk waktu, port, ukuran.

### 2.5 Spasi, radius, elevasi, motion

- Spasi basis 4: `--s-1 4` `--s-2 8` `--s-3 12` `--s-4 16` `--s-5 20` `--s-6 24` `--s-8 32` `--s-10 40` `--s-12 48` (px). Tidak ada nilai di luar skala ini.
- Radius: `--r-sm 8px` (chip kecil, ikon) · `--r-md 10px` (input, tombol) · `--r-lg 14px` (node terminal, menu) · `--r-xl 20px` (kartu, modal) · `--r-pill 999px`.
- Elevasi `--e-1/2/3`: gelap bayangan pekat dan rapat, terang tipis dan lebar (lihat `tokens.css`); selalu dipasangkan dengan `--glass-highlight`.
- Motion: `--t-fast 120ms` (hover, warna) · `--t-base 180ms` (modal, menu) · easing `cubic-bezier(.2,.8,.2,1)`. Hanya untuk umpan balik state; tidak ada animasi dekoratif. Satu-satunya animasi berulang: pulse titik **busy** (1.6 s). `prefers-reduced-motion: reduce` → transisi 0 ms, pulse diganti titik padat dengan ring statis.

## 3. Komponen

Semua komponen interaktif wajib punya state: **default, hover, focus-visible, active, disabled**, plus **loading** dan **error** bila relevan.

### 3.0 Layout: HP, tablet, desktop

Satu tempat memutuskan (`web/src/lib/layout.ts`, dan `public/theme-boot.js` sebelum paint pertama dengan aturan yang sama): `<html data-layout="phone|tablet|desktop" data-input="touch|mouse">`, plus `data-keyboard` selama keyboard layar terbuka. CSS dan kode membaca jawaban yang sama; jangan menambah breakpoint lebar baru untuk memilih layout.

| Layout | Kapan |
|---|---|
| phone | jendela < 600 px, atau layar sentuh yang sisi pendek layarnya < 600 px (tetap HP saat diputar dan saat keyboard terbuka) |
| tablet | jendela < 1200 px, atau layar sentuh lain (Xiaomi Pad 6, Galaxy Tab S9 di kedua arah) |
| desktop | ≥ 1200 px dengan mouse |

- Settings › Appearance › Layout bisa mematok satu layout per browser (Auto, Phone, Tablet, Desktop), misalnya tablet dengan keyboard dan trackpad.
- **Ruang shell:** `--shell-top` dan `--shell-bottom` adalah yang diambil bar aplikasi dari layar (header, tab bar, safe area). Yang menempel ke layar memakai keduanya, bukan menjumlah tinggi header sendiri.
- **Safe area:** `viewport-fit=cover`; header, tab bar, sheet, dan toast memakai `--safe-*`.
- **Keyboard layar:** `interactive-widget=resizes-content`, jadi di Android bar bawah dan sheet naik di atas keyboard; tab bar HP sembunyi selama `data-keyboard`.
- **Sentuh:** `data-input="touch"` menaikkan kontrol ke 40/44 px dan item menu ke 44 px. Isi board tetap ukuran lama sampai board mendapat gilirannya; menu dan dialog yang dibuka dari board sudah ukuran sentuh. Dialog tidak memfokuskan field teks sendiri, supaya keyboard tidak menutupnya.
- Uji: proyek Playwright `phone` (393×873), `tablet-portrait` (800×1280), `tablet-landscape` (1280×800), semuanya sentuh, ditambah desktop terang dan gelap. `e2e/layout.spec.ts` memeriksa tiap halaman: tidak scroll ke samping, header satu baris, kontrol header tidak bertumpuk dan tidak keluar, judul tidak tertutup, target sentuh ≥ 40. `e2e/sweep.spec.ts` memeriksa semua halaman, tiap pane Settings, dialog, dan tiap jenis kartu dalam focus mode: tidak ada elemen atau baris teks yang keluar dari tepi layar atau terpotong kotak yang tidak bisa digulir. Potongan dengan ellipsis dan kotak yang memang menggulir ke samping (tab strip, kolom kanban) boleh. `SWEEP_SHOTS=<folder>` menyimpan screenshot semuanya. `e2e/console-gl.spec.ts` memeriksa console yang digambar GPU pada pixel ratio 1, 2, dan 2,75 (§3.9).
- **Ukur dengan lebar layar, bukan `window.innerWidth`.** Chrome di HP melebarkan halaman yang terlalu lebar lalu menampilkannya lebih kecil, sehingga `innerWidth` ikut membesar dan halaman yang kelebaran terlihat pas di uji.

### 3.1 Tombol

| Varian | Default | Hover | Active | Disabled |
|---|---|---|---|---|
| `primary` | isi `--c-primary`, teks putih, highlight atas `inset 0 1px 0 rgba(255,255,255,.18)` | `--c-primary-600` | turun 1px, highlight hilang | opacity .45, cursor not-allowed |
| `secondary` (kaca) | `--surface-input` + border `--glass-border` | `--surface-hover`, border `--glass-border-strong` | `--surface-active` | sama |
| `ghost` | transparan, teks `--text-2` | `--surface-hover`, teks `--text` | `--surface-active` | sama |
| `danger` | isi `--c-danger-600`, teks putih | lebih gelap 8% | turun 1px | sama |
| `danger-ghost` | teks `--c-danger-300` | bg `rgba(234,33,67,.12)` | bg `.18` | sama |

- Ukuran: `md` tinggi 40, padding-x 16, `--fs-md` 500 · `sm` tinggi 32, padding-x 12, `--fs-sm` · `icon` 36×36 (sm 32×32), ikon 16–18 px, **wajib** `aria-label`.
- Loading: spinner 14 px menggantikan ikon awal, label tetap, `aria-busy="true"`, tidak bisa diklik.
- Label tombol = kata kerja + objek: "Add machine", "Pair device", "Open board". Contoh salah: "OK", "Submit", "Go".
- Focus-visible (semua elemen interaktif): `outline: 2px solid var(--c-primary-300); outline-offset: 2px`. Tidak boleh `outline: none` tanpa pengganti.

### 3.2 Field form

Anatomi tetap, dari atas ke bawah:

1. **Label** — `--fs-sm` 500 `--text-2`, jarak 6 px ke kontrol. Penanda opsional ditulis "(optional)" di label, bukan di placeholder. Field wajib tidak diberi tanda.
2. **Kontrol** — tinggi 40, radius `--r-md`, bg `--surface-input`, border 1px `--glass-border`, padding-x 12, teks `--fs-md` `--text`.
   - hover: border `--glass-border-strong`
   - focus-visible: border `--c-primary`, ring `0 0 0 3px rgba(24,86,255,.30)`, bg `rgba(255,255,255,.06)`
   - error: border `--c-danger`, ring `rgba(234,33,67,.25)` saat fokus, `aria-invalid="true"`
   - disabled: opacity .5
   - placeholder `--text-3`; placeholder hanya contoh nilai, bukan pengganti label.
3. **Hint** (opsional) — `--fs-xs` `--text-3`, jarak 6 px.
4. **Error** — menggantikan hint, `--fs-xs` `--c-danger-300` dengan ikon 12 px, dihubungkan via `aria-describedby`.

Aturan tata letak:
- Celah vertikal antar-field 16 px; antar-kelompok (section) 24 px dengan judul section `--fs-xs` 600 uppercase tracking .06em `--text-3`.
- Field berdampingan memakai grid 12 kolom (contoh: Host 9 / Port 3). Di bawah 560 px semua field satu kolom.
- **Select** kustom: `appearance: none` + chevron SVG 16 px di kanan (padding-right 36).
- **Textarea** (private key): `--surface-solid`, font mono `--fs-sm`, min-height 120, resize vertikal saja.
- **File input**: input asli disembunyikan (tetap fokusable lewat label), tampil sebagai tombol `secondary sm` "Load from file" + nama file `--text-3`.
- **Input group**: ikon depan 16 px (`--text-3`) atau addon belakang (tombol "Browse" menempel, radius kanan saja).
- **Pilihan 2–4 opsi** (metode auth, mode persist, tab sumber host) memakai **segmented control** atau **radio card**, bukan `<select>`. Radio card: kartu kaca 1px border, judul 500 + deskripsi `--fs-xs`; terpilih = border `--c-primary` + bg `rgba(24,86,255,.10)` + titik radio terisi. Keyboard: panah kiri/kanan/atas/bawah memindah pilihan (`role="radiogroup"`).
- Checkbox/switch kustom 18 px / 36×20, fokus mengikuti aturan fokus.
- Footer form (di modal): tombol kanan, urutan `secondary` (Cancel) lalu `primary`; teks bantuan kiri `--fs-xs` `--text-3`. Tombol utama menampilkan loading saat submit dan form dikunci.

### 3.3 Kartu (bento)

- Kaca `--glass-2` + `--glass-blur-2`, border `--glass-border`, radius `--r-xl`, padding 20, `--e-2` + highlight.
- Grid: `repeat(auto-fill, minmax(320px, 1fr))`, gap 16. Kartu penting boleh `grid-column: span 2` (≥ 960 px), contoh: kartu "Pair new device" di Devices.
- Anatomi: header (ikon/status + judul 600 `--fs-lg` + meta `--text-3`) → isi (daftar key-value dua kolom: key `--fs-xs` `--text-3`, value `--fs-sm` `--text`) → footer aksi (tombol `sm`, rata kiri, aksi destruktif paling kanan dipisah spacer).
- Hover kartu non-interaktif: tidak berubah. Kartu yang bisa diklik: border `--glass-border-strong` + naik 1px.
- Teks panjang (hostname, path): `text-overflow: ellipsis` + `title` berisi nilai penuh. Fingerprint: mono, dipecah per 4 karakter, boleh wrap.

### 3.4 Header aplikasi & navigasi

- Header mengambang: margin 12 px dari tepi, tinggi 56, radius `--r-xl`, `--glass-1` + blur-1, border + highlight.
- **Ukuran seragam:** semua kontrol di header tinggi 32 (`--h-control-sm`), ikon 16 (`--size-icon`), teks `--fs-sm`; jarak ke tepi header 12 di semua sisi. Tab strip dan segmented juga 32 di luar (item 26, padding 2, border 1). Jarak: 12 antar blok (brand, navigasi, kanan), 8 antar kontrol, 4 antar tombol ikon dan antara tab strip dengan tombol tambah. Tombol ikon bisa diklik penuh 32×32. Chip nama hub adalah label, bukan kontrol, jadi tetap 22.
- Grup dengan input tertaut: tombol "Input linked" di header frame dan border frame `--tint-warning-*`; tiap console anggota menampilkan ikon link di header-nya, karena Ctrl+C pun ikut ke semua.
- Kiri: brand mark (logo 32 px, terbalik seperti tombol utama) + "NextBoard" 800 + chip nama hub.
- Tengah/kiri: navigasi = segmented control kaca (Board · Machines · Devices); item aktif bg `--surface-active` + teks `--text` + `aria-current="page"`.
- Kanan: indikator koneksi events (titik + "Live"/"Reconnecting…"), tombol kunci Credentials (titik kecil warna teks selama vault terbuka; membuka jendela vault, §3.16), tombol ikon Settings, tema (matahari/bulan), About, Log out (ghost).
- **Tablet:** tetap satu baris. Settings, tema, About, dan Log out (di dalam project juga Projects, Memory, Machines, Devices) pindah ke menu `…`; nama hub hilang; di bawah 1024 px nama NextBoard hilang di luar project, dan di bawah 900 px label navigasi menjadi ikon saja.
- **HP:** bar menempel di tepi atas (56 px + safe area, tanpa sudut membulat) dengan mark, lalu chip usage, needs-you, live, kunci vault, dan `…`; titik koneksi hanya tampil saat tidak live. Di dalam project satu tombol untuk project dan workspace-nya (nama project di atas, workspace di bawah); menunya berisi workspace (pindah, New workspace, Rename, Close) lalu daftar project. Di luar project ada **tab bar** di tepi bawah: Projects, Memory, Machines, Devices, Settings, ikon dalam pil `--surface-active` untuk yang aktif.
- **Tab workspace:** klik kanan (long-press di layar sentuh) membuka Rename, Move left, Move right, Close, jadi tidak ada yang hanya bisa lewat dobel klik atau shortcut.

### 3.5 Board

- **Panah relasi antar tile:** lurus bila dua tile berhadapan, satu siku dengan sudut membulat bila tidak, dan memutar bila ada tile di jalurnya; garis tidak pernah lewat di bawah tile. Coretan di kanvas (shape, garis, tinta, teks) tidak dianggap penghalang. Tiap garis punya titik port di sumber dan panah di target, tebalnya sama di semua zoom. Warna per jenis: wire violet, forward jingga putus-putus, preview sian. Rutenya dihitung `board/wireRoute.ts`.
- **Kabel orchestrator yang sedang bekerja:** selama console di ujungnya `working` atau punya antrean, kabelnya lebih terang (`--wire-flow`) dan dialiri aliran halus serta tiga percikan cahaya searah kerja, memudar di kedua ujung. `prefers-reduced-motion`: tanpa gerak, hanya kabel terang. Kabel kartu memory dan kabel forward tidak ikut.
- Kanvas memakai `--bg-ambient` + pola titik React Flow `rgba(255,255,255,.06)`, gap 24.
- **Menggeser board:** tool Hand (`H`, atau tahan `Space` selama push-to-talk mati) membuat seluruh isi board tembus pointer, jadi drag dan swipe menggeser board dari mana pun, termasuk di atas terminal dan iframe. Tanpa Hand, scroll dan pinch milik board kecuali pointer ada di tile yang sedang fokus: satu-satunya tile yang terpilih, atau tile tempat keyboard berada. Di atas tile lain, scroll ke arah mana pun menggeser board dan pinch men-zoom board, isi tile tidak ikut bergerak. Klik tile untuk memberinya scroll, klik board kosong untuk mengambilnya kembali. Tile di dalam pilihan banyak tile tidak dianggap fokus. Swipe yang menggeser board tetap menggeser walau tile fokus lewat di bawah pointer yang diam, sampai pointer bergerak lebih dari 4 px atau menekan. Klik pertama pada web tile atau app tile hanya memberi fokus; isinya menerima klik berikutnya.
- **Rail kanan:** hanya yang dipakai untuk mengerjakan project: Code editor, Studios, lalu Memory, Git, Loops, Mission Map, lalu Auto arrange (menata ulang board, aktif mulai dua card). Yang ditaruh di board ada di ring klik kanan. Layar sempit tidak punya rail kanan dan tidak punya klik kanan, jadi semuanya tetap ada di flyout `+` rail kiri.
- **Focus mode:** satu kartu mengisi layar, hanya di perangkat ini. Board bersama tidak ditulis: kartu mendapat ukuran area di bawah bar Focus pada zoom 1 (`board/focus.ts`), kartu lain, garis, rail, command bar, zoom, dan minimap disembunyikan. Kartu ada di URL (`?card=<id>`): Back di Android dan Esc (selama tidak ada field yang memegang keyboard) kembali ke board dan viewport sebelumnya; reload mempertahankannya, dan link pendek `/p/<project>?card=<id>` membawanya ke workspace. Bar Focus (kaca `--glass-1`, tinggi kontrol + 8) berisi ‹ Board, nama kartu, nomor urut, dan kartu sebelum/sesudah dalam urutan baca (baris dari atas, lalu kiri ke kanan). Masuk lewat tombol focus di header kartu (di samping tombol display; hover atau terpilih dengan mouse, selalu di layar sentuh) atau Focus di menu kartu. Console mengikuti ukuran yang ditampilkan (resize terakhir menang). Di layar sentuh isi kartu yang di-focus memakai ukuran sentuh dan header 48. Selama di-focus, tombol focus dan display di header kartu disembunyikan (bar Focus sudah memberi jalan keluar, ukuran display tidak berlaku), jadi judul mendapat ruang. Kartu di dekat tepi board (±20000) yang dibesarkan seukuran layar digeser kembali ke dalam board, dan view focus mengikutinya.
- **Layar sentuh** (`data-input="touch"`):
  - tombol Insert di rail kiri selalu ada dan membuka grid berlabel semua jenis kartu (tanpa tooltip, jadi nama tertulis), plus Select several; item rail kanan ikut hanya saat rail kanan tersembunyi;
  - long-press (500 ms, geser < 10 px) = klik kanan di board dan di elemen `data-longpress` (`lib/longpress.ts`); `contextmenu` bawaan Android Chrome yang datang kedua diabaikan, dan angkatan jari sesudahnya bukan tap;
  - dua jari menggeser dan men-zoom board di atas kartu mana pun, termasuk console dan editor; satu jari di dalam kartu tetap milik kartu; halaman board tidak di-zoom browser;
  - tap garis membuka menunya; Select several membuat tap menambah atau membuang kartu dari pilihan, dengan banner jumlah, All, dan Done;
  - kontrol board 40–44 px, handle resize dan knob garis 22 (satuan board), minimap disembunyikan; isi kartu di kanvas tetap ukuran desktop karena ikut di-zoom;
  - **isi kartu:** yang di desktop butuh hover, dobel klik, atau drag and drop punya jalan dengan jari:
    - Kanban: jari men-scroll kolom dan daftar kartu (kolom ber-snap ke samping); kartu dipindah lewat grip ⋮⋮ di kirinya (sebuah `button`, supaya touch targeting Chrome tidak memindahkan sentuhan ke tombol kartu); memegang kartu di tepi men-scroll kolom (juga dengan mouse); `…` kartu selalu tampil; long-press kartu membuka menu kartu itu (klik kanan juga, di semua layout); tap judul kolom = rename;
    - Checklist: tap teks item = edit, × selalu tampil, daftar di-scroll jari dan kartu dipindah lewat header-nya;
    - Memory tile: brick dipindah dengan drag pointer (mouse di mana saja, jari lewat grip), bukan drag and drop HTML yang tidak ada di layar sentuh;
    - grafik (response time, heartbeat, token): tap atau geser jari menampilkan nilainya, dan nilai itu tetap sampai tap berikutnya;
    - Mission Map menulis petunjuk tap, bukan dobel klik dan Ctrl+G.
- **Board di HP** (`board/PhoneBoard.tsx`): tab bar bawah **Board · Cards · Lead** (Lead memakai nama lead, mis. Aruna); tab yang terakhir dipakai diingat per perangkat, bawaannya Cards.
  - Board: kanvas dengan rail pendek di kiri atas (Select, Hand, Spawn, Insert, Undo, Fit). Bentuk, catatan, teks, pen, eraser, gambar, Redo, dan New console ada di grid Insert, yang berhenti di atas tab bar. Command bar dan zoom tidak tampil: perintah di tab Lead, zoom dengan pinch.
  - Cards: daftar kartu dalam urutan baca, satu baris 64 px kaca (ikon, nama, agent dan status console, titik status, "Needs you"). Tap membuka Focus mode dengan tombol kembali "Cards"; swipe ke samping di bar Focus atau header kartu pindah ke kartu sebelum/sesudah; Back kembali ke daftar. Di atas daftar: New console dan Add a card (sheet berisi grid jenis kartu). Badge di tab Cards menghitung console yang butuh kamu.
  - Lead: thread lead mengisi layar di atas command bar, tanpa tombol tutup.
  - Kanvas tetap ter-render di bawah Cards dan Lead (opacity 0), jadi console tetap pada ukurannya dan Focus mode bisa langsung tampil. Console yang baru dibuat atau yang dilompati lewat needs-you langsung dibuka dengan Focus mode, tanpa membuka keyboard sendiri.
- **Command bar dan panel bawah:** tinggi bar diukur (`--cmdbar-h`, dipakai `--h-cmdbar`); di bawah 1380 px zoom, Git pill, dan minimap naik di atas bar (aturannya di `board.css`, karena file itu bisa dimuat sesudah `orch.css`).
- **Ring klik kanan:** klik kanan di board kosong membuka tujuh tombol bulat kaca (`--glass-3`) mengelilingi More, searah jarum jam dari atas: Auto arrange (aktif mulai dua card), Spawn agent, Insert image, Web page, Paste, Shapes and notes, Compose group. Ring berpusat di pointer dan tetap di dalam jendela. Satu caption di bawah ring menyebut nama tombol yang sedang di-hover atau difokus, jadi nama tidak tersebar di sekeliling ring. Panah memindah fokus, Esc menutup. Tombol berpanel menggantikan ring dengan panel kaca berisi tombol kembali, dan Esc di panel kembali ke ring. More adalah ring kedua yang lebih lebar: 12 tombol (Checklist, Kanban, Calendar, Uptime monitor, File from project, Branches card, Memory card, Orchestrator card, Desktop app, Layouts, Snapshots, Select all) mengelilingi Back, dan Esc kembali ke ring pertama. Yang sudah punya tombol sendiri tidak diulang di More: Code editor di rail kanan, Fit di rail kiri, zoom di kiri bawah. Klik kanan pada card dan garis tetap memakai menu biasa.
- **Spawn agent di ring:** tile agent memilih, select model dan effort di bawahnya mulai dari default Settings, tombol Spawn menaruh; klik dua kali pada tile langsung menaruh.
- **Compose group:** bentuk yang sama di panel ring dan di dialog chip Group: titik warna + nama, chip role, slot 4 kolom berwarna agent, baris Fill all, satu baris model dan effort per agent yang dipakai slot, stepper + Spawn N. Klik slot mengganti agent, klik kanan membuka katalog. Dari ring, tim mendarat di titik yang diklik. Tanda agent (`AgentBadge`): logo asli (`agents/logos/`, dari `@lobehub/icons-static-svg` berlisensi MIT, mereknya tetap milik pemiliknya) di atas warna agent itu; yang bentuknya satu warna dicat warna agent lewat mask. Agent tanpa logo mendapat dua huruf nama di atas gradien warnanya, shell mendapat ikon terminal. Usage pill memakai logo yang sama.
- **Code card menurut lebarnya** (`studio/code/CodeStudio.tsx`), bukan menurut layar, jadi kartu kecil di desktop ikut: di bawah 1000 px side bar agent menutupi editor sebagai laci dari kanan (selebar kartu, paling lebar 460), dengan scrim yang menutupnya bila disentuh; di bawah 640 px (HP) side bar kiri juga menjadi laci di atas editor (paling lebar 360), tertutup sendiri begitu ia membuka file, dan hanya grup editor yang aktif tampil; di antara 640 dan 1000 side bar kiri paling lebar 40 %. Panel bawah tidak pernah lebih dari 60 % tinggi editor; di HP tab panelnya bergulir ke samping dan daftar terminal menjadi baris di atas terminal. Title bar sempit menampilkan Go to file sebagai ikon; status bar sempit hanya branch, problems, posisi kursor, dan bahasa. Tombol Fill disembunyikan di focus mode. Di layar sentuh tombol tutup tab selalu tampil dan sash lebih lebar untuk jari.
- **Header card:** satu susunan untuk semua card (`TileHead`, dan `NodeHeader` untuk console, ukuran sama): mark 20 px, judul, meta, spacer, aksi `sm` ghost, tombol display, `…`, tinggi `--h-node-header`, padding kiri 12, kanan 4. Header yang lebih sempit dari 440 px (HP, atau kartu kecil) menampilkan tombol aksinya sebagai ikon saja, dan meta mengalah sebelum judul. Kartu yang judulnya bisa diganti punya **Rename** di menunya (dan di long-press / klik kanan); dobel klik pada judul tetap jalan dengan mouse. Garis atau panah yang bengkok punya **Straighten** di menunya. **Calendar** menghitung event per hari dari tinggi sel yang tampil (focus mode menampilkan lebih banyak), "+N more" membuka daftar event hari itu (tap = edit, plus New event), dan kalender yang lebih sempit dari 520 px menaruh kontrol bulan di baris sendiri dengan nama bulan pendek. Jenis card hanya menentukan warna aksen (`--tile-accent`: garis atas dan ikon): biru default, violet orchestrator dan memory, sian web. Menu `…` berisi aksi card itu lalu aksi yang sama dengan klik kanan. Bingkai group tetap 32 px karena hub menghitung geometrinya dengan angka itu (API-orchestration §1).
- **Ukuran card:** tombol display di header membuka Fit tile, Displays (24 in 1920×1080, 27 in 2560×1440, 32 in 4K 3840×2160), dan Phones (iPhone SE 375×667, iPhone 14 Pro 393×852, iPhone 14 Pro Max 430×932, Pixel 7 412×915, Galaxy S8+ 360×740). Web tile selalu menampilkan tombol ini; card lain saat hover atau terpilih, atau selama memakai salah satu ukuran itu. Memilihnya mengubah ukuran card dari sudut kiri atas, bisa di-undo, dan view bergeser bila card tidak muat. Fit tile mengembalikan ukuran awal jenis card itu. Web tile dev server: ukuran itu menjadi viewport halamannya dan card membungkusnya. Gambar tetap menjaga proporsinya. Opsi yang lebih sempit dari batas card dinonaktifkan (editor 560, kanban dan kalender 420). Card tanpa header, atau beberapa card sekaligus, memiliki daftar ini di menu klik kanan. Group, teks, shape, garis, dan tinta tidak punya daftar ini.
- **Layouts:** popup di atas board, sama seperti pemilih aplikasi desktop: daftar layout di kiri, lane dan simpan layout di kanan, masing-masing bergulir sendiri, tombol Deploy di footer supaya selalu terlihat. Deploy yang berhasil menutup popup; yang gagal membiarkannya terbuka dengan pilihan lane tetap utuh.
- **Lead agent:** wajah lead adalah huruf pertama namanya dalam lingkaran gradasi `--wire`→`--c-primary`, satu-satunya wajah di thread. Pesannya bertanda wajah dan nama, rencananya kartu `proposal` tanpa hitung mundur karena tidak mendesak. Aksi pesan (`.msg-act`) muncul saat disorot, selalu terlihat di layar sentuh; balasan memakai garis `--wire` di kiri, di atas kotak (`.orch-reply`) maupun di atas gelembung (`.msg-quote`).
- **Orchestrator:** kartu dan tab editor memakai bahan yang sama. Tim tampil sebagai chip per grup supaya thread mendapat ruang; titiknya menunjukkan status, klik menyisipkan `@handle` atau `@Grup` ke kotak perintah tanpa mengirim, klik kanan membuka aksi agent itu. Chevron membuka daftar lengkap (diingat per browser), dengan aksi baris yang muncul saat kursor di atasnya dan selalu tampil di layar sentuh. Header kartu menampilkan jumlah agent dan status terpenting, tooltip menghitung semuanya, dan Pause all berubah menjadi Resume all bila semua agent sudah berhenti; console yang sedang di shell tidak ikut dihitung. Perintah panjang di thread ringkas terlipat dengan Show all.
- **Dock "New terminal"**: kaca `--glass-1` mengambang di **tengah bawah** (bottom 16), radius `--r-xl`, padding 8, berisi kontrol `sm` (tinggi 32): machine select (dengan titik status), preset select (titik warna preset), cwd input group (ikon folder + tombol Browse), nama (optional), tombol primary "New terminal". < 900 px: dock jadi tombol bulat "+" yang membuka sheet/modal form yang sama.
- Kontrol zoom kiri bawah & minimap kanan bawah: kaca `--glass-1`, radius `--r-lg`.
- **Node terminal**:
  - Bingkai radius `--r-lg`, border `--glass-border`, `--e-3`.
  - Header 38 px kaca `--glass-1` (tanpa backdrop-filter per node demi performa pan/zoom — pakai bg `rgba(16,21,36,.92)`), garis aksen 2 px di tepi atas dengan warna preset (`session.color`) memudar ke transparan.
  - Isi header: titik status → judul 600 `--fs-sm` → "@ machine" `--text-3` → spacer → badge machine (bila remote) → kode exit (bila ≠ 0, chip danger) → tombol ikon menu `sm`.
  - Body padat `--surface-solid`, padding 8, font JetBrains Mono 13 px; warna xterm dari token `--term-*` (lihat 3.9).
  - Terpilih: ring `0 0 0 1px var(--c-primary-300), 0 0 0 4px rgba(24,86,255,.25)`.
  - Handle resize hanya tampil saat hover/terpilih, 10 px, warna `--c-primary-300`.
- Empty state board: kartu kaca kecil di tengah, ikon, "No terminals yet", satu kalimat, panah/teks yang menunjuk dock.

**Lapisan kartu.** Kartu yang terakhir disentuh naik ke atas seperti jendela. Ini hanya `zIndex` di klien itu: tidak dikirim ke hub, tidak memindah DOM (iframe dan fokus terminal tidak terganggu), dan reload kembali ke urutan tersimpan. Bring to front dan Send to back tetap mengubah urutan tersimpan dan mengalahkannya. Frame grup, shape, line, dan ink tidak ikut naik, karena mereka latar. 

**Memberi ruang saat expand.** Editor yang mengisi layar tidak menimpa kartu lain: kartu di kanannya bergeser ke kanan bersama-sama dan kartu di bawahnya bergeser ke bawah, seperti menyisipkan kolom dan baris, jadi susunan antar-kartu tetap dan tidak ada tumpang tindih baru. Geserannya sejauh yang dibutuhkan kartu terdekat. Kartu di kiri dan di atas diam. Kartu yang sudah menimpa editor pindah ke tempat kosong terdekat. Grup pindah bersama frame-nya. Kartu yang di-lock tetap diam (editor berada di atasnya), juga shape atau garis yang mengelilingi editor; coretan pena ikut bergeser tapi tidak pernah mendorong. Collapse mengembalikan semuanya dengan jarak yang sama, jadi kartu yang dipindah manual selama itu tetap di tempat barunya. `data.fill` menyimpan ukuran asal dan daftar geserannya, jadi ini tetap bisa dikembalikan setelah reload.

### 3.6 Modal, menu, popover, toast

- Modal: `--glass-2` + blur-2, radius `--r-xl`, `--e-3`; lebar `sm 440` / `md 560` / `lg 720`; max-height `calc(100dvh - 48px)`, body scroll, header & footer sticky dengan pemisah `--glass-border`. Masuk: fade + scale .98→1 dalam `--t-base`. Fokus terkunci di dalam; Esc menutup; fokus kembali ke pemicu.
- Menu/popover: `--glass-3` + blur-3, radius `--r-lg`, padding 6, item 34 px radius `--r-sm`, item danger `--c-danger-300`, navigasi panah atas/bawah.
- Toast: kanan bawah (di atas minimap, bottom 16 + tinggi minimap), `--glass-3`, garis kiri 3 px warna jenis (success/danger/info), auto hilang 4 s (error 7 s), `role="status"` / `role="alert"` untuk error.
- **Studio** (Documents, Markdown, Skills): daftar dan isinya berdampingan, bertumpuk di bawah 900 px, dan di HP satu per satu: memilih dari daftar menampilkan isinya dengan tombol kembali bernama daftar itu (‹ Library, ‹ Notes, ‹ Skills) di atasnya (`studio/StudioPane.tsx`). Studio yang dibuka untuk satu item (catatan dari file card) langsung menampilkan item itu. Menu di HP tidak menampilkan shortcut keyboard.
- **HP:** modal adalah sheet dari tepi bawah selebar layar dengan pegangan 36×4 di kepalanya; menyeret kepala turun lebih dari 96 px menutupnya. Tombol footer selebar sheet. Menu ditutup oleh `pointerdown` di luar, jadi tap di layar sentuh juga menutupnya. Esc di menu yang dibuka dari modal hanya menutup menunya. Toast di atas tab bar, selebar layar.

### 3.7 Status & badge

| Status sesi | Titik | Label |
|---|---|---|
| agen dalam giliran | `--c-success` + ring pulse | "Working" |
| shell menjalankan perintah | `--c-success` + ring pulse | "Running" + nama perintah |
| agen di prompt, shell tanpa perintah | `--c-grey` | "Idle" |
| starting / reconnecting | `--c-warning` | "Starting" / "Reconnecting" |
| exited (0) | `--c-grey` | "Exited" |
| exited ≠ 0 / error | `--c-danger` | "Exited (N)" / "Error" |

- Titik 8 px; selalu disertai teks/`aria-label` (warna bukan satu-satunya pembeda).
- **Penghitung header, tab board, dan kartu project** menghitung yang benar-benar memproses (`session.active`), bukan semua proses yang hidup. Yang idle hanya disebut jumlahnya, dan daftarnya baru tampil lewat "Show N idle".
- Badge/chip: tinggi 22, radius pill, padding-x 8, `--fs-xs` 500, bg warna 14% + teks warna tint (contoh success: bg `rgba(7,202,107,.14)`, teks `#3EE08F`).

### 3.8 Halaman auth (Setup, Login)

- Latar `--bg-ambient` penuh layar; kartu kaca tengah lebar 420 (Setup 520), padding 32, radius `--r-xl`.
- Brand di atas kartu (logo 40 px + "NextBoard" 800 `--fs-2xl`), subjudul `--text-2`.
- Setup: indikator langkah (1 Admin · 2 Agents) sebagai stepper horizontal; langkah 2 menampilkan CLI terdeteksi sebagai grid bento kecil (2 kolom) dengan chip "Found"/"Not found".
- Tombol utama `btn-block` tinggi 44.

### 3.10 Halaman Memory

Referensi visual: graph view Obsidian (node dan link, hover menerangi tetangga dan meredupkan sisanya) di dalam sebuah galaxy. Langit selalu gelap; panel di atasnya memakai glass biasa.

- **Struktur:** memory semua project di inti, tiap project bintang di lengan spiral, sub-memory planet yang mengorbit bintangnya, heading bulan kecil, critical notes di orbit terdekat. Lengan, debu, dan bintang memakai kurva yang sama.
- **Bentuk dulu, warna kemudian:** bintang berpaku, critical notes berlian merah dengan denyut pelan, sub-memory bola, catatan agent cincin, heading titik. Selalu ada label dan legenda, jadi jenisnya tidak hanya dibedakan warna.
- **Warna:** per project mengikuti urutan tetap (`PROJECT_HUES`, project terlama dulu) dan tidak berubah saat project lain dibuka. Melewati enam project, warnanya netral. Merah hanya untuk critical notes.
- **Gerak:** orbit dan rotasi galaxy sangat pelan dan berhenti saat pointer di atas node. `prefers-reduced-motion`: semua diam dan hanya menggambar saat ada perubahan.
- **Daftar di kiri berisi hal yang sama** dengan kanvas dan bisa dipakai dengan keyboard: `/` mencari, Esc mundur. Kanvas sendiri hanya tambahan visual (`role="img"`).
- **Layar sempit:** kontrol di bawah dalam jangkauan jempol, panel menjadi sheet.

### 3.11 Daftar project (Home)

- **Dua tampilan, satu markup** (`home/ProjectCard.tsx`): kartu bento, atau baris dalam satu panel kaca (tanpa blur per baris, §6). Di bawah 960 px baris menumpuk seperti kartu ringkas. Pilihan tampilan dan urutan disimpan per browser.
- **Toolbar** (filter nama/folder/group/tag, susunan Host/Group, urutan Recent/Name, Cards/List) muncul mulai 4 project atau begitu ada group atau tag: di bawah itu semuanya sudah terlihat.
- **Host atau Group:** Host mengelompokkan per machine, Group mengikuti tree group (referensi pemilik, 2026-11-04). Tiap susunan menyebut di kartu apa yang tidak dikatakan judulnya: machine di Group, group di Host. Selama belum ada pilihan, Group menjadi tampilan begitu ada group. Pilihan disimpan per browser.
- **Group bertingkat:** judul group adalah tombol buka-tutup, dan yang tertutup diingat per browser. Sub-group menggantung pada garis tipis induknya. Isi group didahulukan, lalu sub-group. Project tanpa group ada di bagian terakhir, **All projects**. Menu ⋯ pada judul: New sub-group, Edit group, Delete group; menghapus group tidak menghapus project, isinya naik satu tingkat. Filter membuka semua group yang cocok dan menyembunyikan sisanya.
- **Tag** berwarna netral karena hanya pembeda (§2.5). Bar di bawah toolbar menyaring satu tag, begitu juga chip di kartu; aktif = tinta penuh. Kartu menampilkan 3 tag dan `+N`. Menu kartu **Group and tags…** menyimpan group dan tag sekaligus, dan bisa membuat group baru di dalam group terpilih.
- **Menu project di header** mengikuti tree yang sama: Home, group bertingkat dengan jumlah project (→ buka, ← tutup, tetap terbuka saat diklik), lalu **All projects** untuk yang tanpa group, baris machine di kanan. Group berisi project yang sedang dibuka selalu terbuka, fokus mulai di project itu. Menu menggantung dari tombolnya dan bergulir, jadi tidak pernah naik menutupi tombol.
- **Anatomi:** monogram + nama + folder → satu baris git → kaki (terminal, board, terakhir dibuka). Kartu dalam satu baris grid sama tinggi, jadi kakinya sejajar. Baris git tidak pernah wrap: branch terpotong, status tetap di kanan.
- **Monogram:** 1–2 huruf dari nama, warna dari id (`--mono-N-*`) supaya tetap saat di-rename. Warnanya hanya pembeda, jadi tidak memakai hijau, oranye, atau merah status.
- **Folder** kehilangan bagian tengahnya, bukan ujungnya: bagian terakhirlah yang membedakan project.
- **Seluruh kartu adalah tombol**, jadi tooltip tombol itu memuat nama, folder penuh, dan branch. Tidak ada area mati di dalam kartu selain menu.
- **Status git:** folder berisi beberapa repo menulis "3 repositories" dan total perubahannya, bukan "No git repository". Machine yang tidak tersambung menulis "Machine offline" tanpa request yang pasti gagal.

### 3.12 Command bar dan usage (referensi pemilik, 2026-11-03)

Dua pengecualian sadar atas §2.5 dan §6, hanya di sini: outline gradien pada chip command bar, dan animasi berulang saat mendengarkan. Sisa aplikasi tetap mengikuti aturan aslinya.

- **Panel input** (referensi pemilik, 2026-11-04) adalah satu kotak komposer, dari atas: baris yang dibawa order (target, balasan, lampiran) bila ada, dipisah garis selebar panel; teks selebar penuh tanpa border sendiri; toolbar berisi tombol thread dan pill agent di kiri, mic dan Send di kanan. Fokus ada di kotaknya: border `--c-primary` dan ring input. Tinggi tetap dua baris walau kosong, jadi tidak melompat saat file dipaste, dan toolbar tidak bergeser karena panel tumbuh ke atas.
- **Chip:** kata saja, tanpa ikon, mengambang di luar panel input. Isi hampir padat tanpa blur (§6: tidak ada blur per elemen di atas board), outline dari `--cmd-ring-*`. Warna hanya pembeda: `Kill all` dan chip yang aktif tetap memakai warna status.
- **Mendengarkan:** panel input berganti jadi orb, status, waveform, Batal, dan Stop; caption transkrip menggantung di bawah panel. Tombol mic dan Stop satu elemen yang sama, supaya tekan-tahan tetap menerima lepasnya walau baris berubah. `prefers-reduced-motion` membuat orb dan waveform diam.
- **Level mikrofon** diukur di browser hanya selama mendengarkan, lalu mikrofonnya ditutup; audio tidak ke mana-mana karena ini. Android menyerahkan mikrofon ke speech recognition, jadi di sana gerakannya mengikuti kata yang masuk.
- **Usage:** satu aksen per CLI (`--usage-claude`, `--usage-codex`) lewat `data-agent`; amber dan merah (§10.1 API-studio) tetap menang saat kuota menipis, dan selalu disertai teks. Window pertama jadi ring, sisanya bar. Heatmap memakai kuartil hari aktif, supaya satu hari besar tidak memudarkan semua hari lain.

### 3.13 Paste file

Kontrak lengkap di API-studio §7.9. Yang terlihat:

- Console: path muncul di input program seperti diketik; agent CLI mengubah gambar jadi `[Image #n]` sendiri. Tidak ada UI tambahan; toast hanya untuk gagal dan untuk upload di atas 2 MB.
- Input order (command bar, kartu orchestrator, pane orchestrator di Code): file yang dipaste jadi chip di baris atas kotak, dipisah garis tipis selebar kotak dari teksnya, bukan path di teks: thumbnail 18 px, nama, ukuran gambar atau file. Tombol × menggantikan ukuran di tempat yang sama saat pointer di atas chip atau fokus ada di dalamnya; layar sentuh selalu menampilkan ×. Klik ruang kosong di baris chip menaruh kursor di teks. Selama upload ukuran diganti `Attaching…` dan Send nonaktif; order boleh hanya berisi file. Klik thumbnail membuka gambarnya. Order yang ditolak hub tetap menyimpan teks dan chip.
- Thread: gambar tampil di atas teks order setinggi 120 px dan membuka ukuran penuh bila diklik, file lain jadi chip, path tidak ditampilkan. Bila file sudah terhapus (7 hari), gambar berubah jadi chip. *Edit and send again* mengembalikan chip-nya.
- Explorer: baris yang di-cut (dan isinya) meredup (`.wb-row.is-cut`) sampai dipaste atau Esc. Copy tanpa tanda, seperti VS Code.

### 3.14 Kartu Uptime

Kontrak di [API-uptime.md](API-uptime.md). Daftar monitor di kiri, halaman monitor terpilih di kanan; kartu di bawah 620 px menampilkan satu sisi saja, lewat container query, dengan tombol kembali.

- **Warna status** hanya `--c-success`, `--c-warning`, `--c-danger`, untuk up, pending, down, dan tidak pernah berdiri sendiri: badge berikon dan berlabel, dan heartbeat yang gagal juga lebih tinggi (60, 80, 100 %), jadi bisa dibedakan tanpa warna.
- **Heartbeat:** sel tipis dengan gap 2 px di satu garis dasar; terbaru di kanan, slot kosong tetap terlihat. 24 di daftar, 60 di halaman monitor, dengan keterangan cek di bawah pointer.
- **Grafik respons:** satu seri, jadi tanpa legenda: garis 2 px `--viz-1`, wash 10 %, titik di nilai terbaru, pita merah di langkah yang punya cek gagal, garis putus di waktu tanpa cek. Hover dan panah kiri/kanan membaca titik; tombol tabel menampilkan angka yang sama. Posisi pointer dihitung sebagai bagian dari lebar, karena zoom board mengubah ukuran di layar.
- Yang paling mendesak di atas daftar: down, pending, waiting, up, paused. Kotak cari mulai 8 monitor.

### 3.15 Kartu Machines

Kartu sama lebar dan sama tinggi per baris, tanpa `span 2`: yang dibandingkan antar mesin harus sejajar. This PC selalu pertama, sisanya menurut nama, jadi kartu tidak berpindah saat koneksinya berubah.

- **Warna status** mewarnai kepala kartu (`--tint-*-bg` memudar dalam 96 px) dan tile ikon. Badge tetap menyebutkannya dengan kata, jadi warna bukan satu-satunya pembeda.
- **Anatomi:** kepala → tiga angka ringkas (Terminals, Agents, Probed, di atas `--surface-input`, bukan kaca di atas kaca) → spesifikasi (label kiri, nilai kanan, garis hairline) → Agent CLIs → Persistence → kaki. Persistence dan kaki menempel di bawah, jadi sejajar antar kartu.
- **Belum diprobe:** satu panel keterangan, bukan deretan tanda strip.
- **`curl` bukan agen,** jadi tidak ikut chip Agent CLIs. Server tanpa CLI itu wajar (agent selalu di This PC), dan kartunya mengatakan begitu.
- **Aksi:** kaki memuat Connect (hanya bila belum tersambung; primary hanya saat terputus atau error) dan Re-probe. Rename, Orphans, Connection log, dan Delete ada di menu More, dengan Delete terpisah di bawah.

### 3.16 Kartu Credentials

Kontrak di [API-vault.md](API-vault.md). Tiga kolom: bagian, daftar, item. Di bawah 820 px bagian menjadi select, dan di bawah 560 px hanya daftar atau item yang tampil, dengan tombol kembali; semuanya lewat container query.

- **Monokrom.** Warna hanya untuk kekuatan master password (`--c-danger`, `--c-warning`, `--c-success`), selalu bersama bar dan label, dan merah untuk aksi hapus.
- **Tanpa ikon situs:** kotak ber-hairline dengan ikon tipe, karena mengambil favicon membocorkan domain.
- **Nilai rahasia:** 12 titik tetap, tidak mengikuti panjang asli, dan tidak ada di DOM sampai Show ditekan. Kunci privat di form kabur sampai difokus.
- **Baris daftar:** salin username dan password muncul saat hover atau fokus, dan teks memberi ruang, jadi tidak menimpa. Di layar sentuh selalu terlihat.
- **Kekuatan master password** memberi peringatan, tidak menolak; hanya kurang dari 8 karakter yang ditolak.
- **PIN** satu field bertopeng dengan jarak antarhuruf lebar, numpad di ponsel, terkirim di digit keenam. Salah: field kosong lagi dan sisa percobaan disebut.
- **Dialog isi login:** yang cocok dengan situs di atas, sisanya terpisah dengan "saved for …". Login untuk situs lain butuh klik kedua, dan tombolnya merah. Di Chrome This PC ada peringatan tetap bahwa situsnya tidak bisa dipastikan.
- **Jendela dari header:** modal 960 px, tinggi sampai 760, isi sama dengan kartu (`vault/Panel.tsx`). New, Lock, dan `…` ada di kepala modal sebelum tombol tutup; jumlah item hilang di bawah 560 px. Menutup jendela tidak mengunci vault. Esc di menu `…` hanya menutup menu.
- **Layar kunci dan buat vault** tanpa bingkai tambahan, di tengah kartu, dan menggulir kalau lebih tinggi dari kartu. Peringatan "tidak bisa dipulihkan" pakai `.alert.warning` dan centang wajib. Di HTTP biasa ada satu baris catatan kecil di bawah kolom password, tidak menghalangi.

### 3.17 Pasang sebagai aplikasi (PWA)

NextBoard bisa dipasang ke layar utama dan berjalan di jendela sendiri, tanpa bar browser. Hub tetap wajib: tidak ada yang berjalan offline.

- **Manifest:** `public/manifest.json` (bukan `.webmanifest`, supaya hub menyajikannya sebagai JSON tanpa tabel MIME tambahan). Isinya: `display: standalone`, `start_url` dan `scope` `/`, serta latar dan `theme_color` `#0a0a0a`. Warna bar tetap mengikuti tema lewat `<meta name="theme-color">` (`lib/theme.ts`).
- **Ikon:** `public/icons/icon.svg` (kotak membulat, sama dengan favicon) dan `maskable.svg` (latar penuh, tanda di dalam lingkaran 80 %). PNG 192, 512, maskable 512, dan apple-touch 180 dibuat dari keduanya dengan `node scripts/icons.mjs`; jangan disunting tangan.
- **Tanpa service worker.** Chrome tidak lagi memintanya untuk install. Cache offline berisiko menyajikan bundle lama setelah hub diperbarui, dan hub yang mati tetap tidak bisa dipakai.
- **Settings › Install app** (per perangkat), dengan empat keadaan (`lib/install.ts`):
  - **Terpasang:** halaman berjalan sebagai aplikasi.
  - **Siap:** browser menawarkan install (`beforeinstallprompt`), jadi tombol Install membuka dialognya. Selama tawaran itu ada, menu `…` di HP dan tablet juga punya "Install app".
  - **Lewat menu:** alamatnya aman tetapi browser belum menawarkan, jadi pane menulis langkahnya per browser.
  - **HTTP biasa:** `.alert.warning` menjelaskan bahwa *Add to Home screen* hanya membuat shortcut yang terbuka di tab browser. Caranya: buka hub lewat HTTPS (nginx atau `tailscale serve`), atau di Chrome perangkat sendiri tambahkan alamat hub ke `chrome://flags` › *Insecure origins treated as secure*. Hub tetap jalan penuh lewat HTTP.
- Tawaran install dari browser tidak dicegah: Chrome tetap boleh menampilkan ajakannya sendiri.

### 3.9 Palet terminal (xterm)

Dari token `--term-*` di `tokens.css`, dibaca `useTerminal` dari elemen console, jadi mengikuti `data-theme` di sekitarnya. Ganti tema tidak membuat ulang terminal; isi layar tetap.

```
gelap   background #111111  foreground #E5E5E5  cursor #F5F5F5  selection rgba(255,255,255,.24)
        black #262626  red #F87171  green #4ADE80  yellow #FBBF24  blue #60A5FA  magenta #C084FC  cyan #22D3EE  white #D4D4D4
        brightBlack #737373  brightRed #FCA5A5  brightGreen #86EFAC  brightYellow #FDE68A  brightBlue #93C5FD  brightMagenta #D8B4FE  brightCyan #67E8F9  brightWhite #FFFFFF
terang  background #FFFFFF  foreground #1F1F1F  cursor #0D0D0D  selection rgba(0,0,0,.14)
        black #171717  red #B91C1C  green #15803D  yellow #A16207  blue #1D4ED8  magenta #7E22CE  cyan #0E7490  white #525252
        brightBlack #737373  brightRed #DC2626  brightGreen #16A34A  brightYellow #CA8A04  brightBlue #2563EB  brightMagenta #9333EA  brightCyan #0891B2  brightWhite #262626
```

**Console di layar sentuh** (`board/KeyBar.tsx`, `board/consoleKeys.ts`): selama console di-Focus, atau selama keyboard ada di console, bar tombol kaca `--glass-3` menempel di atas tab bar (dan di atas keyboard layar, yang menggeser semuanya). Urutan: Esc, ↑, ↓, ⏎, Tab, Ctrl, ^C, ←, →, ⇧Tab, Alt, `/ | ~ -` (bergulir ke samping), lalu terpisah garis: Copy text, Attach, dan keyboard. Tombol 44×40 font mono; menekannya tidak mengambil fokus, jadi keyboard tetap terbuka. Ctrl dan Alt sekali pakai: menyala (`aria-pressed`, putih di atas gelap) sampai tombol berikutnya yang diketik, lalu padam. Panah mengikuti mode kursor program (`ESC O` atau `ESC [`). Copy text membuka sheet berisi layar dan scrollback (2000 baris terakhir) sebagai teks biasa yang bisa dipilih dengan jari, plus Copy all. Attach mengunggah file atau foto ke machine console dan mengetik path-nya, sama seperti paste. Command bar juga punya tombol Attach. Terminal di panel Code card mendapat bar yang sama.

**Editor kode di layar sentuh** (`studio/code/EditorKeyBar.tsx`): selama keyboard ada di Monaco, bar dengan bentuk yang sama berisi Esc, Tab, ⇧Tab, ←, ↑, ↓, →, Sel, Undo, Redo, Find, `//` (comment), lalu Save, Go to file, dan keyboard. Esc, Tab, dan panah dikirim ke editor sebagai tekanan tombol (dengan `keyCode`, yang dibaca Monaco), jadi melakukan apa yang tombol itu lakukan di sana: menerima saran, menutup widget, mode normal vim. Sel membuat panah memilih teks sampai dimatikan. Undo, Redo, Find, dan comment memanggil perintah editor. Fokus Monaco bisa textarea atau elemen EditContext, tergantung browser; keduanya dihitung.

**Console di tema terang.** CLI yang disetel untuk terminal gelap mencetak teks sangat terang, kadang truecolor yang tidak lewat palet. `--term-min-contrast: 4.5` (xterm `minimumContrastRatio`) menggelapkan teks seperti itu per sel. Settings › Appearance › Terminals bisa memilih "Always dark" untuk CLI yang tetap kurang enak dibaca.

**Pointer di bawah zoom board.** xterm mengukur pointer dalam piksel layar dan selnya dalam piksel layout, tanpa opsi untuk mengubahnya, jadi di luar zoom 100 % seleksi jatuh di samping pointer. `followBoardZoom` (`board/useTerminal.ts`) membungkus method privatnya: `_mouseService.getCoords` dan `getMouseReportCoords` (seleksi, laporan mouse ke program, link), `_selectionService._getMouseEventScrollAmount` (auto-scroll saat drag), dan `_sliderPointerPosition` (thumb scrollbar). Berlaku juga untuk terminal di Code card. Setelah upgrade xterm, periksa konsol browser: bila nama method berubah muncul peringatan dan zoom meleset lagi. Monaco sudah menghitung skalanya sendiri.

**Renderer GPU.** Dengan GPU hardware, console digambar WebGL; GPU software (SwiftShader, llvmpipe) memakai renderer DOM karena lebih cepat. Renderer WebGL mengukur canvas-nya dari piksel device yang dilaporkan browser (`devicePixelContentBoxSize`). Di mode device Chrome DevTools dan browser uji, pixel ratio diemulasikan dan yang dilaporkan piksel CSS, jadi canvas hanya separuh ukurannya: teks jatuh di luar canvas dan console tampak kosong. `tryWebgl` mengamati elemen terminal dengan ukuran yang sama; bila tidak cocok dengan `contentRect × devicePixelRatio`, WebGL dilepas dan terminal digambar ulang dengan DOM, termasuk bila mode device dinyalakan setelah halaman terbuka. `e2e/console-gl.spec.ts` memaksa WebGL di browser uji: console board dan agent di tab editor harus menampilkan teks di kelima profil.

**Double click** di dalam isi card (`.nodrag`: console, editor) milik isinya, misalnya memilih kata, dan board tidak bergerak. Frame ke card lewat header.

## 4. Aksesibilitas (kriteria uji)

- [ ] Teks isi ≥ 4.5:1, teks besar (≥ 18 px atau ≥ 14 px bold) & komponen UI ≥ 3:1 terhadap latar efektifnya (ukur di atas glow ambient paling terang).
- [ ] Setiap elemen interaktif terjangkau Tab, urutan logis, dan menampilkan ring fokus 2 px `--c-primary-300`.
- [ ] Modal mengunci fokus, Esc menutup, fokus kembali ke pemicu. Menu bisa dinavigasi panah.
- [ ] Segmented control & radio card memakai `role="radiogroup"`/`radio` dengan `aria-checked`, panah memindah pilihan.
- [ ] Setiap input punya `<label>` terhubung; error memakai `aria-invalid` + `aria-describedby`.
- [ ] Target sentuh ≥ 40×40 di `data-input="touch"` (dicek `e2e/layout.spec.ts` untuk header dan tab bar; dock board boleh 32 dengan jarak ≥ 8 sampai board mendapat gilirannya).
- [ ] `prefers-reduced-motion` mematikan transisi & pulse; `prefers-reduced-transparency` / tanpa `backdrop-filter` → permukaan padat.
- [ ] Status tidak hanya dibedakan warna (ada teks/aria-label).
- [ ] Zoom browser 200% tidak memotong form atau menyembunyikan tombol aksi.

## 5. Nada & penulisan

- Ringkas, yakin, ramah, profesional. Kalimat aktif.
- Label = kata benda jelas ("Host", "Private key"). Tombol = kata kerja + objek ("Add machine").
- Error = apa yang salah + cara memperbaiki: "Host key changed for devbox:22. Connection refused — verify the server before trusting it." Contoh salah: "Error 409".
- Empty state = apa ini + satu aksi: "No machines yet. Add one from your ~/.ssh/config."
- Bahasa UI web saat ini Inggris; jangan mencampur dua bahasa dalam satu layar.

## 6. Anti-pola (dilarang)

- Kaca di atas kaca di atas kaca (> 2 lapisan blur bertumpuk) — keterbacaan turun dan GPU berat. Contoh: menu kaca di dalam modal kaca boleh; kartu kaca di dalam modal kaca tidak (pakai `--surface-input`).
- `backdrop-filter` pada setiap node terminal (pan/zoom jadi patah-patah).
- Teks di bawah `--text-3`, placeholder sebagai label, `outline: none` tanpa pengganti.
- Warna/spasi/radius mentah di komponen (`#333`, `margin: 7px`, `border-radius: 5px`).
- Animasi dekoratif (shimmer, floating, parallax), gradien pelangi, glow neon di teks. Pengecualian: §3.12.
- Bayangan hitam tebal gaya material bercampur kaca.

## 7. Migrasi dari UI lama

1. Ganti `:root` lama dengan token di atas (`styles/tokens.css`); petakan: `--panel*` → `--glass-*`/`--surface-*`, `--accent` → `--c-primary-300` (teks) / `--c-primary` (isi), `--green/--yellow/--red` → `--c-success/--c-warning/--c-danger`.
2. Header + toolbar menyatu di satu baris → pisah: header mengambang + dock board di bawah.
3. `<select>` untuk metode auth / persist / tab sumber → radio card / segmented.
4. Tabel Devices → daftar kartu bento.
5. Font sistem → Plus Jakarta Sans + JetBrains Mono lokal.

## 8. QA checklist (code review)

- [ ] Tidak ada warna/spasi/radius mentah di luar file token (grep `#[0-9a-f]{3,6}` di komponen = 0, kecuali warna preset dari API, warna isi board, dan langit Memory).
- [ ] Dicek di tema terang dan gelap, pada build produksi: minifier memendekkan nilai token (`#ffffff` jadi `#fff`), jadi kode yang membaca token harus menerima keduanya (Monaco menolak bentuk pendek).
- [ ] Semua state (hover, focus-visible, active, disabled, loading, error) ada untuk tombol & input.
- [ ] Screenshot 1440×900 dan 390×844: Setup, Login, Board kosong, Board 2 terminal, menu node, Folder browser, Machines, Add machine (dua tab, auth key), Host key confirm, Host key changed, Devices, Pair, About, toast error.
- [ ] Halaman, pane, dialog, atau jenis kartu baru masuk daftar `e2e/sweep.spec.ts`. Lihat screenshot kelima profilnya: `SWEEP_SHOTS=<folder> npx playwright test -c e2e/playwright.config.ts sweep`.
- [ ] Fallback tanpa `backdrop-filter` terlihat rapi (uji dengan menonaktifkan di DevTools / class `no-glass`).
- [ ] Lighthouse a11y ≥ 95; tidak ada error konsol.
- [ ] Pan/zoom board dengan 6 terminal tetap halus (tidak ada blur per node).
