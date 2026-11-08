# NextBoard API — Fase 6: orkestrasi

Tambahan atas [API.md](API.md), [API-board.md](API-board.md) dan [API-agents.md](API-agents.md) (handle, `agentState`, `foreground`, transcript, settings). Fitur: [ROADMAP.md](ROADMAP.md) Fase 6. Keputusan pemilik #5 dan #6 mengikat.

**Ketergantungan lintas fase:** planner, ringkasan handoff, goal "agent judged", dan memory scan memakai **runner tersembunyi** yang didefinisikan di [API-studio.md](API-studio.md) §2. Inti runner (job `claude -p` di This PC + Job API) harus dibangun di awal Fase 6. Langkah git di command bar memakai layanan git Fase 7; sebelum itu hub membalas "Git actions are not available yet".

## 0. Ringkasan desain

- Satu "otak" di hub. Web, desktop, dan Android mengirim teks yang sama ke `POST /api/projects/{id}/commands`; hasilnya identik di semua klien.
- Pipeline: **parser lokal** → (opsional) **planner** Claude Code di This PC → **steps** → **kebijakan konfirmasi** → **eksekusi** → **thread**.
- Teks masuk ke agen **hanya** lewat **delivery engine** (§2): bracketed paste + Enter, hanya bila agen di foreground, mengantre saat `working`/`needsYou`, tidak pernah ke shell.
- Grup, kartu orchestrator, kartu memory adalah node board yang **dipahami hub**; wire dan forward adalah edge.
- Loop berjalan di hub 24/7 tanpa klien.

## 1. Board: kind dan edge baru

| kind | `data` | Validasi hub |
|---|---|---|
| `group` | `{ "name": "Backend", "role": "general"\|"frontend"\|"backend"\|"testers"\|"reviewers"\|"design"\|"docs"\|"security", "color": "amber"\|"sage"\|"sky"\|"violet"\|"rose"\|"teal", "members": ["n_…"], "syncInput"?: true }` | nama 1–24 karakter, unik per workspace (case-insensitive) dan tidak sama dengan handle mana pun di hub (`409 name_conflict`); `members` = node `terminal` di board yang sama, maks 12; satu terminal di ≤ 1 grup (`400 member_conflict`) |
| `orchestrator` | `{ "title": "Orchestrator", "personality"?: "…" }` | title ≤ 40; thread kartu = id node; judul selain `Orchestrator[ n]` = nama lead kartu itu (§3.9) |
| `memory` | `{ "blocks"?: [{ "projectId"?, "name", "lookup"? }], "manual"?: false, "lastPush"?: { "at", "delivered", "busy", "skipped", "upToDate" } }` | maks. 32 blok; `400 bad_memory` untuk blok tanpa nama atau `critical`. Tile lama dengan `scope: "global"` dibaca sebagai satu blok memory semua project (§12) |

| Edge kind | source → target | `data` |
|---|---|---|
| `wire` | `orchestrator`, `memory`, `editor`, atau tile berisi → `terminal` atau `group`; `editor` dan `memory` juga → `orchestrator`; `memory` juga → `editor`; `orchestrator` juga → `kanban` atau `checklist` (§3.10) | `{}`; unik per pasangan; kombinasi lain → `400 bad_edge` |
| `forward` | `terminal`/`group` → `terminal`/`group` | `{ "mode": "digest"\|"split", "trigger": "turn"\|"order" }`; edge forward tidak boleh membentuk siklus (`400 forward_cycle`) |
| `preview` | `terminal` → `web` | `{}`; garis penanda, dibuat hub saat URL terdeteksi atau digambar tangan |
| `log` | `web` → `weblog` | `{}`; kartu log mengikuti halaman ini |

**Tile berisi** (`note`, `checklist`, `kanban`, `calendar`, `file`, `image`, `link`, `document`, `branches`, `web`, `weblog`): isinya dikirim ke agen di ujung wire **sekali saat wire dibuat**, dan lagi hanya kalau diminta (menu "Send to wired consoles", tombol "Send errors" di kartu log, atau `POST /api/workspaces/{id}/tiles/{nodeId}/push`). Tile tidak pernah memakai giliran agen dengan sendirinya. Teksnya ditandai sebagai referensi, bukan tugas; isi halaman ditandai sebagai data, bukan instruksi. ≤ 20 000 karakter.

- `file`: path dan salinan milik kartu. `image`: path file gambar (agen ada di This PC, jadi aset hub ikut terbaca). `document`: versi yang ditampilkan kartu, atau versi selesai terbaru. `branches`: daftar branch project.
- `weblog` dan `web`: ≤ 50 masalah terbaru dari log halaman (error, `console.error`, request gagal), tanpa query string URL. Wire baru mendapat semuanya; permintaan ulang hanya yang baru sejak kiriman terakhir. `web` juga mengirim alamatnya.
- `app`, `shape`, `line`, `ink`, dan `text` tidak punya isi yang berarti sebagai teks, jadi tidak bisa jadi sumber wire.

**Console tanpa agen.** Klien dan planner menolak wire atau forward ke console tanpa agen (plain shell, tool), dan forward darinya, dengan alasan yang jelas: console itu tidak punya giliran dan NextBoard hanya mengetik ke agen. Grup ditolak hanya kalau tidak ada agen di antara anggotanya. Aturan ini memakai status console saat itu, jadi `board/ops` tetap memeriksa bentuk edge saja (undo, snapshot, dan deploy layout tidak boleh gagal karena console sudah berubah). Garis yang ujungnya tanpa agen tampil redup putus-putus dengan keterangan, dan kembali normal begitu agen berjalan di sana.

- Terminal dihapus → hub menghapus id-nya dari `members` semua grup (op hub, `clientId:""`). Bila penghapusan dari klien web menghabiskan semua anggota grup, grupnya ikut terhapus walau bingkainya tidak terpilih (marquee memang tidak memilih bingkai). Grup yang masih punya anggota mengecil mengikuti sisanya. Undo menyalakan lagi console-nya, lalu memulihkan grup, edge, dan node lain dengan id baru; console yang gagal menyala dilewati. Grup yang kosong karena sebab lain (console dihapus lewat API atau planner) tetap ada.
- **Bingkai grup:** x/y/w/h node grup = bounding box anggota + padding 24 + header 32. Klien yang memindahkan anggota mengirim patch geometri grup dalam satu request ops; drag header grup memindahkan semua anggota (satu request). Drag terminal masuk/keluar bingkai = patch `members`. Hub menghitung ulang bingkai hanya saat ia sendiri menambah anggota (spawn).
- Warna default per role: general sky, frontend violet, backend teal, testers amber, reviewers rose, design violet, docs sage, security rose.
- Eraser tidak pernah menghapus `terminal` dan `orchestrator` (aturan klien); `del` lewat menu tetap boleh dengan konfirmasi klien.

## 2. Delivery engine (P6, D6)

```jsonc
// Delivery
{ "id": "dl_…", "sessionId": "s_…", "handle": "Ash",
  "text": "Run the tests and fix failures", "submit": true,
  "source": "order",            // "order"|"loop"|"memory"|"handoff"|"forward"|"reply"|"skill"|"dictation"|"api"
  "guard": "agent",             // "agent" (default) | "dictation"
  "orderId": "o_…", "forwardDepth": 0,
  "state": "waiting",           // lihat state machine
  "waitReason": "working",      // "starting"|"needsYou"|"working"|"paused"|"reconnecting"|"humanTyping"|""
  "createdAt": "…", "deadline": "…", "sentAt": null, "confirmedAt": null,
  "error": "", "code": "" }
```

State machine:

```
queued ──► waiting(reason) ──► typing ──► sent ──► confirmed
   │            │                 │         └────► (tetap sent bila tidak terkonfirmasi 8 dtk)
   └────────────┴─────────────────┴──► failed | expired | cancelled
```

| Kondisi sesi saat dicek | Aksi |
|---|---|
| `agentKind:"shell"`, `foreground:"shell"` (agen sudah keluar), `manualAgent` tanpa izin | `failed` `not_agent` — **tidak pernah** mengetik ke shell |
| `exited`, `error`, sesi dihapus | `failed` `session_gone` |
| `starting` | tunggu ≤ `startingTimeoutSec` (60) |
| `needsYou` | tunggu ≤ `needsYouTimeoutSec` (900); tidak pernah mengetik ke prompt izin |
| `working` | `whileWorking:"queue"` (default) tunggu ≤ `workingTimeoutSec` (1800); `"type"` → kirim sekarang (CLI yang mendukung antrean input) |
| `paused` | tunggu sampai resume atau deadline |
| `reconnecting` | tunggu ≤ `reconnectTimeoutSec` (300) |
| `ready` | ketik |

Prosedur mengetik (satu delivery aktif per sesi, FIFO):

1. Cek ulang tepat sebelum mengetik: `foreground` dengan cek proses (API-agents §6.5), `agentState`, `promptOnScreen` kosong, tidak ada input manusia ke sesi ini dalam `humanTypingGuardSec` (2 dtk; tunggu ≤ 10 dtk), tidak `paused`.
2. Sanitasi: buang C0 kecuali `\t` dan `\n`, buang ESC dan DEL, `\r\n` → `\n`.
3. Mode bracketed paste aktif di model VT (DECSET 2004) → tulis `ESC[200~` + teks + `ESC[201~`. Tidak aktif → `\n` diganti spasi (satu baris).
4. `submit:true` → tunggu `submitDelayMs` (200; 100–1000) lalu tulis `\r`.
5. Konfirmasi: `agentState` menjadi `working` dalam 8 dtk (hook `UserPromptSubmit`, T14, atau layar) → `confirmed`; selain itu tetap `sent` (UI: "sent, not confirmed").
6. Agen keluar saat antre → `failed` `agent_exited`. Deadline lewat → `expired` + catatan thread bila ada `orderId`.

`guard:"dictation"`: `submit:false`; ditolak `409 target_busy` bila agen tidak di foreground, `starting`, atau `needsYou` (klien lalu memasukkan teks ke command bar).

| Method | Path | Request | Response / error |
|---|---|---|---|
| POST | `/api/sessions/{id}/deliveries` | `{text, submit?: true, source?: "api", guard?, deadlineSec?}` | `202 Delivery`. `409 not_agent` (langsung bila jelas shell), `409 target_busy` (dictation), `413 text_too_long` (> 32 000 karakter), `429 queue_full` (> 20 antre) |
| GET | `/api/sessions/{id}/deliveries` | — | `{"items":[Delivery…]}` (antre + 20 terakhir selesai) |
| GET | `/api/deliveries?projectId=&active=1` | — | `{"items":[Delivery…]}` |
| DELETE | `/api/deliveries/{id}` | — | `{"ok":true}` → `cancelled`; `409 already_sent` bila sudah `typing`/`sent` |

Settings baru (`settings.json`, PATCH seperti API-agents §11):

```jsonc
"delivery": { "whileWorking": "queue", "startingTimeoutSec": 60, "needsYouTimeoutSec": 900, "workingTimeoutSec": 1800,
              "reconnectTimeoutSec": 300, "submitDelayMs": 200, "humanTypingGuardSec": 2 }
```

Session Info mendapat `queue` (jumlah delivery aktif).

## 3. Command pipeline (P7, D1–D9)

### 3.1 Endpoint

`POST /api/projects/{id}/commands`

```jsonc
{ "text": "@Ash run the tests", "source": "typed",      // "typed" | "voice"
  "workspaceId": "w_…", "cardId": null,                 // cardId = node orchestrator → lingkup kartu
  "clientId": "c_…", "selection": ["n_…"], "planner": "auto",    // "auto" | "off"
  "replyTo": "tm_…" }                                   // opsional: pesan thread yang dijawab (§3.9)
```

Respons `202`:

```jsonc
{ "commandId": "cmd_…", "threadId": "p_…",             // "p_…" thread project, "n_…" thread kartu
  "handledBy": "parser",                                // "parser" | "planner" | "pending" (planner berjalan; hasil via event)
  "orderId": "o_…", "uiActions": [UiAction…],           // uiActions hanya untuk klien pengirim
  "confirmation": null, "messages": [ThreadMessage…] }
```

Error: `400 empty_text`, `413 text_too_long` (> 4000 karakter), `404 workspace_not_found`, `429 thread_busy` (sudah 3 perintah antre di belakang satu giliran planner).

```jsonc
// UiAction — dijalankan klien pengirim; juga disiarkan di event "command" dengan clientId
{ "action": "fit"|"zoomIn"|"zoomOut"|"zoomReset"|"focus"|"openPanel"|"closePanel"|"tool"|"undo"|"redo"|"goWorkspace",
  // "focus" dengan "workspaceId": node ada di board lain; klien membuka board itu dulu, baru memfokuskan
  "target"?: "n_…", "panel"?: "loops"|"memory"|"mission"|"settings"|"git"|"usage"|"documents"|"code"|"markdown"|"skills",
  "tool"?: "select"|"pen"|"eraser", "workspaceId"?: "w_…" }
```

### 3.2 Parser lokal (D4)

Normalisasi: trim, spasi tunggal, pencocokan case-insensitive (teks asli dipakai untuk isi order), kata bilangan `one`…`twelve`, `a`/`an` = 1. `<who>` diselesaikan oleh §3.3. Fase 6: bahasa Inggris; alias bahasa Indonesia boleh ditambah kemudian di tabel yang sama.

| Area | Pola (contoh) | Step | Konfirmasi |
|---|---|---|---|
| Langsung | `@Ash <teks>` | `send` verbatim (mention dibuang) | — |
| Semua | `@everyone/@all/@everybody <teks>` | diperlakukan seperti tanpa mention | — |
| Spawn | `spawn\|spin up\|start\|launch [<n>] [<agent>] [<role>]`, `new console`, `open a shell\|terminal` | `spawn` (≤ 12) | > 6 → `bulkSpawn` |
| Grup | `make\|create a group of <n> [<agent>] [<role>] [called <name>]`, `group <who…>`, `group these` (selection) | `group.create` | — |
| Tambah | `add <n> [<agent>\|<role>] to <group>` | `group.add` | — |
| Bubar | `ungroup\|dissolve <group>` | `group.dissolve` | — |
| Pause | `pause [<who>]`, `wait`, `hold on` / `resume\|continue\|keep going [<who>]` | `pause` / `resume` | — |
| Restart | `restart\|reset <who>` | `restart` | selalu |
| Kill | `kill\|close\|stop <who>`, `delete <group>`, `kill all` | `kill` | selalu |
| Tell | `tell <who> to <teks>`, `ask <who> to <teks>`, `<handle>, <teks>` | planner (tulis ulang); tanpa planner → `send` verbatim bagian `<teks>` | — |
| Sama | `give everyone the same prompt: <teks>`, `tell everyone: <teks>` | `send` ke semua agen di lingkup | — |
| Handoff | `hand off\|pass [this task] to <agent> [from <handle>]` | `handoff` | — |
| Forward | `when <who> finish(es), pass\|send\|forward (it\|everything) to <who>` | `forward` | — |
| Batal | `cancel that`, `never mind`, `scratch that` | `cancel` (order terakhir di thread) | — |
| Tanya | `status`, `how's it going`, `who finished`, `what happened`, `who needs me`, `give me a summary`, `help`, `what can you do` | `ask` | — |
| Kamera | `show me everything`, `fit`, `zoom in\|out`, `reset zoom`, `go to <handle>`, `focus on <group>` | `ui` | — |
| Panel | `open loops\|memory\|mission map\|settings\|git\|usage\|documents\|code\|markdown\|skills`, `close that` | `ui` | — |
| Node | `add a note [that says <teks>]`, `add a checklist\|kanban\|calendar\|uptime monitor`, `add an orchestrator [for <group>]`, `open localhost <port>`, `preview <port>` | `node.add` | — |
| Wire | `connect <who> to [the] orchestrator [<title>]` | `wire` | — |
| Gambar | `pen`, `I want to draw`, `eraser` | `ui` tool | — |
| Undo | `undo`, `redo` (hanya bar utama) | `ui` | — |
| Workspace | `go to\|switch to workspace <n\|nama>`, `new workspace [called <nama>]`, `rename this workspace to <nama>`, `close workspace <n>` | `workspace.*` | close dengan sesi berjalan: selalu |
| Loop | `every <n> minute(s)\|hour(s) <teks>` (waktu di awal kalimat, 1 mnt–24 j) | `loop.create` | selalu |
| Git | `commit and push`, `commit [with the message] <msg>`, `push`, `pull` | `git` | — (diminta user) |
| Memory | `push memory [to <who>]`, `scan memory` | `memory.push` / `memory.scan` | — |
| Layout | `deploy <nama layout>` | `layout.deploy` | — |
| Basa-basi | `thanks`, `ok`, `nice`, `cool` | balasan pendek hub, tidak dikirim ke agen | — |

Teks yang tidak cocok dan tanpa addressee → planner bila tersedia; tanpa planner → §3.11.

### 3.3 @mention dan resolusi nama (D2)

- **Lingkup:** command bar utama menjangkau **semua board (workspace) project**; `workspaceId` request hanya menentukan board yang sedang dibuka (tempat `spawn`, `node.add`, loop mendarat, dan grup yang menang bila namanya kembar). Kartu orchestrator tetap hanya board-nya sendiri. "Semua"/`@everyone`, `pause`, `kill all`, `status`, `push memory` berlaku untuk semua board project; project lain hanya tercapai lewat handle eksplisit. Prompt konfirmasi kill menyebut jumlah board bila lebih dari satu.
- Grup tetap satu board: anggota dari board berbeda ditolak (`consoles on different boards cannot share a group`), wire kartu hanya ke console di board-nya, dan loop `allAgents` hanya agen di board tempat loop dibuat.
- Urutan resolusi: handle (hub-wide; dalam lingkup kartu hanya yang ter-wire) → nama grup (board yang dibuka dulu, lalu board lain urut tab) → role (`@reviewers` = anggota semua grup ber-role itu) → agen (`@codex` = semua console agen itu dalam lingkup).
- Cocok persis setelah normalisasi (huruf kecil, buang non-alfanumerik) → target langsung. Tidak persis → kandidat dengan jarak Damerau-Levenshtein ≤ 1 (nama ≤ 4 huruf) atau ≤ 2 (lebih panjang), atau kunci primer Double Metaphone sama, atau ejaan Indonesia yang sama (Nopal/Naufal, Raihan/Reyhan, Djihan/Jihan) → konfirmasi `didYouMean` (satu kandidat) atau `sendToAll` (beberapa).
- Mention tanpa teks → balasan hub "What should @Ash do?" tanpa order.
- Satu mention + teks → verbatim. Beberapa target atau grup/role → planner (membagi kerja); tanpa planner → §3.11.

| Method | Path | Response |
|---|---|---|
| GET | `/api/projects/{id}/mentions?workspaceId=&cardId=&q=` | `[{ "kind": "everyone"\|"console"\|"group"\|"role"\|"agent", "id", "label", "handle"?, "agentState"?, "board"? }]` (`board` = nama tab bila bukan board yang dibuka) (≤ 30; urut: everyone, console, group) |

### 3.4 Step

```jsonc
{ "op": "send", "targets": ["s_…"], "text": "…", "mode": "verbatim"|"rewritten" }
{ "op": "spawn", "count": 3, "agent"?: "codex", "role"?: "reviewers", "groupId"?: "n_…", "newGroup"?: { "name", "role" }, "machineId"? }
{ "op": "group.create", "members": ["s_…"], "name"?, "role"? }      { "op": "group.add", "groupId", "count", "agent"?, "role"? }
{ "op": "group.dissolve", "groupId" }
{ "op": "pause"|"resume"|"restart"|"kill", "targets": ["s_…"] }
{ "op": "handoff", "source": "s_…", "agent": "codex", "machineId"? }
{ "op": "forward", "sources": ["s_…"], "targets": ["s_…"], "mode": "digest"|"split" }
{ "op": "cancel", "orderId"? }      { "op": "ask", "query": "status"|"finished"|"needsYou"|"summary"|"help" }
{ "op": "ui", "uiAction": UiAction }
{ "op": "node.add", "kind": "note"|"checklist"|"kanban"|"calendar"|"uptime"|"orchestrator"|"web", "data"?: {…}, "wireTo"?: ["n_…"] }
{ "op": "wire", "orchestratorId": "n_…", "targets": ["n_…"] }
{ "op": "workspace.go"|"workspace.create"|"workspace.rename"|"workspace.close", "workspaceId"?, "name"? }
{ "op": "loop.create", "loop": LoopDef }      { "op": "git", "action": "commitPush"|"commit"|"push"|"pull", "message"? }
{ "op": "memory.push", "targets"?: ["s_…"] }  { "op": "memory.scan" }      { "op": "layout.deploy", "layoutId" }
{ "op": "reply", "text": "…" }                // jawaban ke user, tidak dikirim ke agen
```

### 3.5 Planner (D5, keputusan #6)

- Hanya di **This PC**, lewat runner (API-studio §2): `claude -p --output-format json --json-schema '<PlanSchema>'`, tanpa tool (`--tools ""` bila didukung, selain itu `--disallowedTools` semua tool + `--max-turns 1`), cwd `<data>/runner/planner`, timeout 45 dtk, tidak pernah yolo, memakai profil default Claude di This PC.
- Tersedia bila `claude` terdeteksi di This PC dan cek login terakhir sukses; `settings.commands.planner: "auto"|"off"`.
- **Input** (JSON di stdin, tanpa isi file project atau transcript): teks order (> 600 karakter → planner dilewati, kecuali Talk first menyala: sampai batas 4000), 20 pesan thread terakhir (per pesan ≤ 600, total ≤ 6000 karakter), `pendingPlan` bila ada rencana terbuka (§3.9), snapshot: console (`handle`, agen, role, grup, `board`, `agentState`, `activity` ≤ 80, order berjalan ≤ 120), grup, orchestrator + wire, `boards` (semua board dalam lingkup: nama, `current`, ≤ 30 tile non-console per board berisi `id`, `kind`, posisi, ukuran, dan isi seperti dijelaskan di bawah), nama agen custom, nama project/workspace, nama loop, selection, lingkup kartu, `memory` (memory project + catatan + global, ≤ 4000 karakter; latar belakang, bukan perintah).
- **Output** (`PlanSchema`): `{ "reply": string ≤ 2000, "danger"?: string ≤ 300, "steps": PlannerStep[] ≤ 40 }`. `PlannerStep` = subset op `send, spawn, group.create, group.add, pause, resume, restart, kill, handoff, forward, loop.create, node.add, git, ask, memory.push, reply`; target ditulis sebagai handle/nama grup, hub yang menerjemahkan ke id (nama tak dikenal → step dibuang + catatan). `spawn`, `group.create`, `node.add` boleh membawa `workspace` (nama board tujuan). Jawaban ke user ada di `reply` puncak; step `reply` yang mengulang teks sama dibuang hub, jadi jawaban tampil sekali.
- Satu giliran planner per thread; pesan "Still thinking…" setelah 3 dtk (Talk first: klien menampilkan "`nama` is thinking", tanpa pesan ini) (pesan thread yang sama diperbarui). Gagal (timeout, JSON tidak valid, belum login) → fallback parser + catatan "Planner unavailable: <alasan>"; dua kegagalan beruntun → cooldown 60 dtk. Kegagalan login (termasuk `OAuth session expired`) membuat `reason: "not_logged_in"` selama 1 menit, karena refresh token Claude bisa pulih sendiri.

| Method | Path | Response |
|---|---|---|
| GET | `/api/planner` | `{ "available": true, "reason"?: "claude_missing"\|"not_logged_in"\|"disabled"\|"cooldown", "lastLatencyMs": 6200 }` |


### 3.6 Kontrol board oleh planner

Input planner memuat setiap tile: `id`, `kind`, posisi, ukuran, dan isi (`data`, atau `contentCut` kalau melebihi 700 karakter; `points`, `strokes`, `snapshot`, `markup` tidak dikirim), plus `edges` tiap board. Maksimal 40 langkah per rencana. Tile bisa disebut dengan id, `@handle`, nama grup, judul, atau kind kalau hanya ada satu (`editor`); nama yang cocok ke lebih dari satu tile membuat langkahnya dibuang.

| Op planner | Field | Perilaku |
|---|---|---|
| `node.add` | `kind` (`note text checklist kanban calendar uptime orchestrator web shape branches editor memory file`), `x y w h`, `title text color`, `data`, `path` | `editor` kedua hanya memfokuskan card yang ada |
| `node.update` | `tile`, `x y w h`, `title text color`, `data` | `data` di-merge per key, list diganti utuh; `sessionId previewId assetId appId points strokes` tidak bisa diubah; isi console tidak bisa diedit |
| `node.delete` | `tile`/`tiles` | selalu lewat konfirmasi `destructive`; console harus `kill` |
| `connect`, `disconnect` | `source`, `targets` | card (orchestrator, memory, editor) atau tile berisi → console/grup = wire, editor dan memory juga ke orchestrator, memory juga ke editor; console ↔ console = forward; console → web = preview; web → kartu log = log. Tile berisi dan memory tile mengirim isinya sekali saat tersambung. Console tanpa agen ditolak dengan catatan di thread. Urutan terbalik ("Bea ke editor") dibalik otomatis |
| `ui` | `action` (`fit zoomIn zoomOut zoomReset focus openPanel closePanel undo redo`), `tile`, `panel` | dijalankan klien yang mengirim perintah |
| `task.add`, `task.update`, `task.delete` | `tile`, `card` (id atau judul), `title`, `text`, `column`, `assignee`, `priority`, `due`, `done` | kartu di kanban atau checklist, §3.10; `send` dengan `tile` dan `card` membagikan kartu yang sudah ada |
| `editor.open` | `path`, `line` | menjadi uiAction `openFile`: card editor muncul dan membuka file |
| `console.rename`, `group.dissolve`, `workspace.create`, `workspace.rename`, `workspace.go` | | seperti namanya |

`workspace.close` sengaja tetap di luar kewenangan planner. Perubahan planner tidak masuk undo klien; pemulihannya lewat snapshot board. Klien lain menerima hasilnya seperti op board biasa.
### 3.7 Konfirmasi (D8)

```jsonc
// Confirmation
{ "id": "cf_…", "threadId": "p_…", "commandId": "cmd_…",
  "kind": "destructive",         // "destructive"|"didYouMean"|"sendToAll"|"risky"|"loop"|"bulkSpawn"|"proposal"
  "plan"?: [{ "handle": "Ash", "text": "…" }],   // proposal: rencananya dalam kata-kata
  "prompt": "Kill @Ash and @Bea? Their processes stop; the tiles stay on the board.",
  "options": [{ "id": "confirm", "label": "Kill 2 consoles" }, { "id": "cancel", "label": "Cancel" }],
  "candidates"?: [{ "sessionId", "handle" }],      // didYouMean / sendToAll
  "steps": [Step…], "expiresAt": "…", "state": "pending",   // "pending"|"confirmed"|"cancelled"|"expired"
  "decidedBy"?: "admin" | "d_…" }
```

| Step | Konfirmasi |
|---|---|
| `kill`, `restart` (console, grup, `kill all`) | selalu `destructive` |
| `workspace.close` dengan sesi berjalan | selalu |
| `loop.create` dari order | selalu `loop` (tampilkan jadwal + estimasi prompt/jam × target) |
| `spawn` > 6 console sekaligus | `bulkSpawn` |
| Step planner yang tidak diminta: `git`, `node.add web` ke URL, `send` berisi pola berisiko | `risky` (Talk first: tampil sebagai `proposal`, §3.9) |
| `send` yang diketik user (`@Arkan …`, `tell Arkan …`, `@everyone …`) berisi pola berisiko | `risky`, sebagai pertanyaan biasa ("Your order for @Arkan says \"delete\"… Send it?"), bukan proposal lead (keputusan pemilik, 2026-11-08) |
| `git` yang diketik user, `pause`/`resume`, `handoff`, `forward`, `send` tanpa pola berisiko, `memory.push` | tidak |

Pola berisiko (RE2, case-insensitive; `settings.commands.riskyPatterns` menambah): `\brm\s+(-\S+\s+)*\S+`, `\bsudo\b`, `curl[^|\n]*\|\s*(ba)?sh`, `\.env\b`, `--dangerously`, `--yolo`, `\b(delete|deletes|deleting|hapus\w*|menghapus\w*|dihapus\w*|penghapusan)\b`, `\bdrop\s+(table|database|schema|collection)\b`, `\btruncate\b`, `\b(migrate:(fresh|reset)|db:(drop|reset|wipe))\b`, `\bgit\s+push\b[^\n]*\s(-f|--force)`, `\bgit\s+reset\s+--hard\b`, `\bgit\s+clean\s+-`, `\bgit\s+branch\s+-d\b`, `\bmkfs\b`, `\bdd\s+if=`. Pola ini sengaja condong bertanya, karena order lead lainnya langsung jalan: kalimat "jangan hapus file lain" pun ikut ditanyakan.

- `POST /api/confirmations/{id}` `{"decision":"confirm"|"cancel","choice"?: "s_…"}` → `200 Confirmation`; `409 already_decided`, `410 expired`. Keputusan pertama menang (klien mana pun, termasuk Android).
- TTL `settings.commands.confirmTtlSec` (default 60, 30–300). Selama ada konfirmasi pending di thread itu, teks `yes|confirm|do it|go ahead` = confirm, `no|cancel` = cancel; teks lain membatalkan yang pending lalu diproses sebagai perintah baru.

### 3.8 Eksekusi

Step dijalankan berurutan; kegagalan satu step dicatat dan step independen berikutnya tetap jalan. Step `send` membuat **order** (§4). Step `ui` dikembalikan sebagai `uiActions`. Hasil planner yang datang belakangan dikirim lewat event `command` (`state:"done"`, `uiActions`, `clientId`); klien lain mengabaikan `uiActions`.

### 3.9 Lead agent

Orchestrator adalah **lead agent**: asisten owner sekaligus pemimpin agent lain, punya nama dan kepribadian. Ia tetap planner yang sama (§3.5); yang berubah cara bicara dan kapan kerja dilepas.

```jsonc
// settings.lead (PATCH /api/settings menggabungkan per kolom)
{ "name": "Aruna",         // 1–24 karakter, satu baris; bawaan "Aruna" sejak 2026-11-08 (sebelumnya "Nara", yang tersimpan ikut pindah)
  "personality": "",       // ≤ 800 karakter; ditulis owner, aturan hub setelahnya tetap berlaku
  "talkFirst": true }
```

- **Identitas.** Prompt planner dibuka dengan nama dan kepribadian. Kartu punya lead sendiri bila judulnya bukan `Orchestrator[ n]`; `data.personality` kartu menimpa yang global (dipotong ke 800 karakter).
- **Talk first.** Planner boleh membalas tanpa step dan bertanya balik saat ada yang belum jelas. Step yang dikembalikannya langsung jalan, tanpa Go ahead (keputusan pemilik, 2026-11-04).
- **Yang ditahan.** Hub menahan rencana bila salah satu ini kena: `policy` menemukan `risky` (§3.7), atau planner mengisi `danger` (penilaiannya sendiri untuk bahaya yang tidak terbaca dari pola, mis. "Clean up the staging environment"). Balasan tampil, semua step jadi konfirmasi `proposal` dengan `plan`, alasan di `prompt`, dan TTL **30 menit**. Pola berjalan di hub, jadi tetap menahan walau model tidak menandainya.
- **Membicarakan rencana.** Selama proposal terbuka, kata setuju (`ya, gas, lanjut, oke, setuju, go ahead…`) = confirm tanpa giliran planner; kata tolak (`jangan dulu, batal, not now…`) = cancel, dengan catatan "Nothing was sent to the team."; pesan lain masuk planner bersama `pendingPlan`, dan rencana barunya menggantikan yang lama (rencana baru yang aman langsung jalan dan menutup yang lama).
- **Yang tetap langsung:** order yang menyebut targetnya (`@Arkan …`, `tell Arkan …`, `@everyone …`) dan perintah parser (`pause everyone`, `status`, …). Kecuali order berisi pola berisiko: itu menunggu satu jawaban ya (§3.7). `@Aruna …` (nama lead itu), `@orchestrator`, `@lead` diarahkan ke lead, kecuali ada console bernama itu.
- **Satu pertanyaan per bahaya.** Kartu menyebut order mana dan bagian teks mana yang kena pola, jadi satu kali setuju cukup. Step yang punya konfirmasi sendiri (`kill`, `restart`, `node.delete`, `workspace.close`, `loop.create`, `spawn` > 6) tetap memakai kartunya sendiri, bukan `proposal`. Planner diminta tidak menyalin critical notes ke order (agent sudah menerimanya saat start), karena salinannya bisa kena pola `\.env\b`.
- **Balasan lead ditulis Markdown** (Claude memakainya); klien merender tanpa HTML mentah, gambar jadi placeholder, tautan relatif tidak dibuka. Copy mengambil teks aslinya.
- `talkFirst: false` = rencana langsung jalan seperti sebelumnya; planner hanya dipanggil untuk yang tidak dimengerti parser dengan batas 600 karakter.

**Membalas pesan.** `replyTo` pada §3.1: hub mencari pesan itu di 200 pesan terakhir thread (tidak ketemu = diabaikan). Pesan owner disimpan dengan kata-katanya sendiri plus `replyTo { id, handle?, excerpt }`; yang diteruskan ke agent atau planner mendapat tambahan `[This answers what @Arkan said: "…"]`. Baris rencana di thread tidak menampilkan tambahan itu. Klien: balasan ke pesan agent dikirim sebagai `@Arkan teks`, jadi langsung ke agent itu; "Ask lead" tanpa awalan, jadi ke lead.

### 3.10 Papan tugas lead

Wire `orchestrator → kanban` atau `→ checklist` (seret knob, **Connect consoles…**, atau **New task board** di menu kartu; arah sebaliknya dibalik oleh klien dan planner) membuat lead menjaga papan itu. Isi papan tidak dikirim ke siapa pun karena wire ini, dan semuanya dikerjakan hub tanpa giliran agent atau run planner.

**Mengikuti order.** Order (`source: "order"`) ke console yang diarahkan kartu tersebut, dari thread mana pun, menjadi satu kartu per console: judul baris pertama tanpa `@handle`, sisanya catatan, `assignee` handle-nya. Forward dan handoff tidak dicatat. Hub menulis `orderId`, `sessionId`, `state`, dan bila gagal `error`, lewat op `patch` biasa.

| Target order | `state` | Kolom |
|---|---|---|
| `queued` | `queued` | To do |
| `delivered`, `working` | `working` | In progress |
| `needsYou` | `needsYou` | In progress |
| `finished` | `finished` | Review bila ada, selain itu Done dengan `done: true` |
| `failed`, `timeout`, `cancelled`, `skipped` | `failed` | tetap di tempatnya, `error` berisi alasannya |

Kolom dicari dari namanya (Inggris dan Indonesia), lalu dari posisinya: pertama, sesudahnya, terakhir. Kartu hanya dipindah saat `state` berubah, jadi yang dipindah user tetap di sana. Di checklist, `finished` mencentang item.

**Membagikan sendiri.** `autoRun: true` di data tile (switch di kartu orchestrator atau menu tile, default mati) membuat hub memeriksa tiap 5 detik: kartu di To do yang belum pernah jalan, atau yang dikembalikan user ke To do setelah selesai atau gagal, dikirim ke console tim yang bebas (`ready`, tanpa order aktif), satu per console. Isinya judul plus catatan, `source: "task"`, dan pesan "Sent to" muncul di thread kartu. `assignee` membatasi ke console itu. Kartu di Backlog tidak ikut. Tidak ada konfirmasi karena pemilik yang menyalakannya, dan tiap kartu memakai satu giliran agent.

**Planner.** Input-nya memuat `tasks`: setiap kanban dan checklist di board lingkupnya dengan `linked`, `autoRun`, dan kartunya (≤ 80). Ia punya `task.add`, `task.update`, `task.delete`, dan `send` dengan `tile` plus `card` untuk membagikan kartu yang sudah ada. Membagikan kerja tetap lewat proposal lead (§3.9); mengubah papan tidak.

### 3.11 Fallback tanpa planner (D9)

- Order dengan `@` atau addressee di awal → kata-kata user apa adanya. Beberapa console disebut → **satu** agen bebas (state `ready`, tanpa delivery/order aktif) yang menerima; thread mencatat "Sent to @Ash; 2 left free".
- Teks yang sama ke semua hanya untuk permintaan eksplisit (`same prompt`, `everyone the same`, `independent reviews`) atau direktif pendek (≤ 40 karakter, satu baris).
- Tanpa addressee: tepat satu agen di lingkup → ke dia; selain itu tidak dikirim, catatan "Name an agent with @ or enable the planner".
- Permintaan yang butuh menyusun instruksi ("write a brief for…") tidak dikirim tanpa planner.

## 4. Thread dan order (D3, D15)

```jsonc
// ThreadMessage
{ "id": "tm_…", "threadId": "p_…", "at": "…",
  "role": "user"|"hub"|"agent",
  "kind": "order"|"plan"|"report"|"reply"|"note"|"confirm"|"error",
  "text": "…", "source"?: "typed"|"voice"|"loop"|"api",
  "orderId"?: "o_…", "sessionId"?: "s_…", "handle"?: "Ash",
  "plan"?: [{ "handle": "Ash", "text": "…" }],                       // plan note: apa yang dikirim ke siapa
  "report"?: { "state": "finished"|"failed"|"timeout"|"skipped", "durationMs": 81234, "excerpt": "≤ 400 karakter", "chars": 5120 },
  "confirmationId"?: "cf_…",
  "replyTo"?: { "id": "tm_…", "handle"?: "Ash", "excerpt": "…" } }   // pesan yang dijawab (§3.9)
// Order
{ "id": "o_…", "threadId": "p_…", "commandId": "cmd_…", "text": "…", "createdAt": "…",
  "targets": [{ "sessionId", "handle", "deliveryId",
    "state": "queued"|"delivered"|"working"|"needsYou"|"finished"|"failed"|"timeout"|"cancelled"|"skipped",
    "startedAt", "finishedAt", "error"? }] }
```

- Target `finished` = transisi `working → ready` setelah delivery `confirmed` (atau `sent` lalu `working` teramati). `failed` = delivery gagal, `agent-exit`, atau `error`. `timeout` = `settings.commands.reportTimeoutMin` (30) tanpa selesai → catatan "No reply from @Ash after 30 min".
- Saat `finished`, hub membaca balasan (API-agents §8) dan menambah pesan `report`; pesan menyimpan teks ≤ 8000 karakter. Balasan utuh (≤ 200 KB) disimpan untuk pesan itu sendiri di `threads/<tid>.outputs/<id>.json`, jadi "See output" selalu membuka balasan bubble tersebut, walau agen sudah lanjut ke task lain, hub restart, atau console dihapus. Balasan ikut hilang saat pesannya keluar dari thread atau thread dikosongkan. Report dari sebelum fitur ini menampilkan teks pesannya dengan `partial:true` bila lebih pendek dari `chars`.
- Balasan yang belum masuk saat giliran selesai tidak diganti balasan giliran sebelumnya: report terbit tanpa teks, lalu diisi (`Threads.Update`, id sama) begitu balasannya ada, dalam 60 dtk.
- `ask`: `status` = jumlah per state + daftar needsYou; `who finished`/`what happened` = target order terakhir; `summary` = ringkasan laporan lewat runner (≤ 30 dtk) bila planner tersedia, selain itu gabungan excerpt.
- `cancel that` = `POST /api/orders/{id}/cancel`: delivery yang belum diketik dibatalkan; teks yang sudah ditempel tetap di input agen.
- **Setelah hub restart** semua target yang belum final jadi `failed`, "interrupted by hub restart". Target yang teksnya belum diketik (`queued`) mendapat `undelivered: true`, dan catatan thread membawa `resend: true` (UI: tombol **Send again**). Hub tidak pernah mengirim ulang sendiri. `resend` mengirim ke target itu saja, dalam order yang sama, jadi kartu tugas dan forward tetap mengikutinya. Console yang belum bisa menerima (masih resume) tetap ditawarkan; console yang sudah hilang tidak. Setelah `kill -9`, status bisa tertinggal dari yang terakhir disimpan, jadi orang yang memutuskan.

| Method | Path | Response |
|---|---|---|
| GET | `/api/threads/{tid}/messages?before=&limit=50` | `{"items":[ThreadMessage…],"hasMore":bool,"confirmations":[Confirmation…],"planning":"cmd_…"\|""}` (`limit` ≤ 200). `confirmations` dan `planning` untuk klien yang tidak tersambung saat event-nya terbit (reload, hub restart) |
| DELETE | `/api/threads/{tid}` | `{"ok":true}` (kosongkan; order aktif tetap dilacak) |
| GET | `/api/threads/{tid}/messages/{id}/output` | — | `{"messageId","threadId","projectId"?,"orderId"?,"sessionId"?,"handle"?,"source"?,"text","at","chars","truncated","partial"?}`; `404 no_output` bila pesan sudah tidak ada atau bukan report selesai berisi balasan |
| POST | `/api/threads/{tid}/messages/{id}/output/save` | `{"dir"?: "outputs"}` | `Stat`, menulis balasan itu ke `<project>/<dir>/<YYYY-MM-DD-HHMM>-<handle>.md` |
| GET | `/api/threads/{tid}/orders?active=1` | `{"items":[Order…]}` |
| GET | `/api/orders/{id}` | `Order` |
| POST | `/api/orders/{id}/cancel` | `Order` |
| POST | `/api/orders/{id}/resend` | `Order`, kirim ulang setelah hub restart. `404 order_not_found`, `409 nothing_to_resend` |

Thread project = `p_…`, thread kartu = id node orchestrator `n_…`. Riwayat input command bar (↑/↓, 50) disimpan per klien.

## 5. Grup dan team composer (D14, C2)

`POST /api/workspaces/{id}/spawn`:

```jsonc
{ "slots": [{ "agent"?: "claude", "machineId"?: "m_…", "profileId"?, "yolo"? }],   // 1–12; atau "count" + "agent"
  "group"?: { "name"?: "Backend", "role"?: "backend", "color"?: "teal" } | { "id": "n_grp" },
  "orchestratorId"?: "n_orch", "at"?: { "x": 0, "y": 0 } }
```

→ `201 {"sessions":[Session…],"groupNodeId":"n_…"}` dalam **satu** siaran `boardOps` (terminal, grup, wire). Agen per slot: `slot.agent` → `settings.agents.roleAgents[role]` → default machine. Model dan effort per slot: `slot.model`, `slot.effort` → `settings.agents.roleModels[role]` bila agent-nya agent role itu → `settings.agents.models[agent]` (API-agents.md §25). Kata `shell`/`terminal` di order → `shell`. Nama grup default `Group N`. Error: `400 too_many` (> 12 per panggilan, > 64 sesi per workspace), `404 group_not_found`, `409 name_conflict`.

Grup dibuat, diganti nama, dibubarkan lewat board ops (atau step parser yang menghasilkan ops yang sama di hub). Pause/resume/restart/kill grup = step dengan semua anggota sebagai target.

**Input tertaut:** `data.syncInput: true` pada node grup (diubah lewat board op `patch`, default mati). Hub lalu menyalin input keyboard satu anggota (WS terminal `{"t":"i"}` dan `POST /api/sessions/{id}/input`) ke anggota lain yang berjalan dan tidak di-pause, jadi berlaku dari klien mana pun tanpa perubahan protokol.
- Yang disalin hanya ketikan. Balasan otomatis emulator terminal (fokus, mouse, cursor position, device attributes, jawaban OSC/DCS) tidak disalin, karena hanya berarti bagi layar asalnya (`session.TypedInput`). Konsekuensi: Ctrl/Shift+F3 tidak ikut tersalin karena bentuknya sama dengan cursor position report.
- Tiap anggota punya antrean sendiri yang terurut (≤ 1 MiB, lebih dari itu dibuang + log), sehingga console yang berhenti membaca tidak menahan console yang sedang diketik.
- Order dari command bar tidak lewat jalur ini; delivery engine mengetik ke tiap console sendiri.
- `resize` tidak disalin.

## 6. Kartu orchestrator (D10)

- Mengarahkan = gabungan terminal yang di-wire + anggota grup yang di-wire.
- Perintah dengan `cardId`: target dibatasi; mention console di luar → pesan "@Bo is not wired to this card"; `undo`/`redo`/`workspace.*` ditolak ("Use the main command bar"); push-to-talk selalu ke bar utama.
- Kartu kosong: "No agents wired" + Spawn agent (`spawn` dengan `orchestratorId`).
- Aksi baris: Focus, Pause/Resume/Restart, Kill (konfirmasi), Disconnect (`delEdge`).
- Judul otomatis untuk kartu dari perintah: `Orchestrator`, `Orchestrator 2`, …
- Nama kartu = nama lead-nya: judul bawaan memakai `settings.lead.name`, judul lain jadi nama lead kartu itu (§3.9). "Lead profile…" di menu kartu mengubah judul dan `data.personality`.

## 7. Mission Map (D16)

`GET /api/workspaces/{id}/mission` (`?scope=project` = semua workspace project):

```jsonc
{ "workspaceId": "w_…",
  "directors": [{ "id": "bar"|"n_orch", "kind": "bar"|"orchestrator", "title": "Command bar", "lastOrder"?: { "text", "at" }, "targets": ["n_grp","n_term"] }],
  "groups": [{ "nodeId", "name", "role", "color", "members": ["s_…"], "lastTask"?: "…" }],
  "consoles": [{ "sessionId", "nodeId", "handle", "agent", "role"?, "groupNodeId"?, "agentState", "activity", "stateSince",
                 "paused": false, "subagents": 0, "currentOrder"?: { "orderId", "text" }, "directedBy": ["bar","n_orch"] }],
  "counts": { "working": 2, "needsYou": 1, "idle": 3, "done": 1 } }
```

Filter (klien): All · Working · Needs you (+ error) · Idle (`ready`, `shell`, paused) · Done (`exited`). Console/grup yang tidak diarahkan kartu → `directedBy:["bar"]` (garis putus "command bar only"). `subagents` = jumlah `PreToolUse` tool `Task` tanpa `PostToolUse` pasangannya (hooks saja). Double-click → fokus node di board.

## 8. Pause / resume (D17)

| Method | Path | Response / error |
|---|---|---|
| POST | `/api/sessions/{id}/pause` | `Session` (`paused:true`, `pausedAt`); idempoten. `409 not_agent` bila agen tidak di foreground; `503 machine_offline` |
| POST | `/api/sessions/{id}/resume` | `Session`; juga mengirim SIGCONT bila proses foreground berstatus `T` walau hub tidak mem-pause (program yang menghentikan dirinya sendiri) |
| POST | `/api/workspaces/{id}/pause-all`, `/resume-all` | `{"sessions":[id…]}` (agen saja) |

| Machine | Mekanisme |
|---|---|
| Lokal | `TIOCGPGRP` master PTY → `kill(-pgid, SIGSTOP\|SIGCONT)`; tolak bila pgid ≤ 1 atau sama dengan pgid hub |
| SSH (direct & tmux) | exec read-only + sinyal: `p=<wrapperPid>; t=$(ps -o tpgid= -p "$p"\|tr -d ' '); a=$(ps -o sid= -p "$p"\|tr -d ' '); b=$(ps -o sid= -p "$t"\|tr -d ' '); [ "$t" -gt 1 ] && [ "$a" = "$b" ] && kill -STOP -- "-$t"` |

- `wrapperPid` dari marker `agent-start` (API-agents §3.2). Belum diketahui (sesi lama, re-attach tmux tanpa marker) → `tmux display -p -t nb-<id> '#{pane_pid}'`; direct tanpa pid → `409 pid_unknown`.
- Setelah SIGCONT: resize kecil (cols−1, 100 ms, cols) supaya TUI menggambar ulang.
- Selama `paused`: `agentState` dibekukan (UI pill PAUSED), delivery menunggu, loop melewati sesi itu. `paused` disimpan di `sessions.json`; sesi lokal yang mati karena hub restart → `paused:false`.

## 9. Handoff (D18)

`POST /api/sessions/{id}/handoff` `{ "agent": "codex", "machineId"?, "profileId"?, "workspaceId"?, "note"?, "threadId"? }` → `202 Handoff`. `409 handoff_running` (satu per sesi sumber), `400 not_agent`.

```jsonc
{ "id": "ho_…", "sourceSessionId": "s_…", "targetSessionId": null, "agent": "codex",
  "state": "summarizing",    // collecting → summarizing → spawning → waitingReady → delivering → done | failed | manual
  "jobId": "jb_…", "brief"?: "…", "error"?: "", "createdAt": "…" }
```

1. **collecting:** transcript sumber (giliran terakhir sampai 60 000 karakter) atau, tanpa transcript, 400 baris layar terakhir.
2. **summarizing:** job runner `handoff-summary` (≤ 4 menit) di machine project bila punya Claude/Codex yang login, selain itu di This PC (konten dikirim via stdin). Brief: tujuan, yang sudah selesai, kondisi sekarang, file yang disentuh, langkah berikut, pertanyaan terbuka (≤ 1500 kata). Gagal → brief = 12 000 karakter terakhir apa adanya + catatan.
3. **spawning:** console baru agen tujuan (agen katalog/custom mana pun ber-`kind:"agent"`; boleh machine lain — ekstensi NextBoard), di samping sumber, satu grup dengan sumber.
4. **waitingReady:** ≤ 120 dtk; bila `needsYou` (mis. trust folder) tunggu user ≤ 120 dtk lagi; habis → `manual`, brief ditampilkan di thread dengan Copy.
5. **delivering:** delivery `source:"handoff"`: brief + "Continue this task from exactly this state. Do not redo finished work."

Sumber tetap berjalan. Tanpa konfirmasi.

**Saat hub restart.** Handoff tersimpan di `handoffs.json` dan dilanjutkan, dengan satu catatan di thread. `collecting` dan `summarizing` mengulang ringkasan setelah console sumber kembali (paling lama ditunggu 30 detik), karena job runner mati bersama hub. `spawning` dan `switching` dicoba lagi: yang sudah berganti agent tidak diganti ulang, dan di antara mulai dan tercatatnya spawn bisa muncul satu console lebih, yang terlihat dan tidak mengetik apa pun. `waitingReady` menunggu lagi dari awal. `delivering` **tidak** diketik ulang, karena delivery hanya di memori sehingga tidak ada yang tahu ringkasannya sudah terketik atau belum: state menjadi `manual` dan ringkasan masuk thread (sama dengan order, keputusan #34).

### 9.1 Ganti agent di tile yang sama

`POST /api/sessions/{id}/handoff` dengan `"inPlace": true` memakai console yang sama, bukan console baru: tile, handle, dan folder tetap. Field tambahan: `"noBrief"?: true` melewati ringkasan, `"fresh"?: true` memulai percakapan baru walau agent itu pernah jalan di sini. Respons `Handoff` membawa `inPlace` dan `returning`. `machineId` dan `workspaceId` diabaikan. `400` bila agent tidak terpasang di machine itu, sebelum agent lama dihentikan.

State: `collecting → summarizing → switching → waitingReady → delivering → done | failed | manual`.

1. Agent yang sedang bekerja dijeda (SIGSTOP) selama ringkasan ditulis, supaya isinya tidak basi, lalu dibangunkan tepat sebelum dihentikan. Bila ringkasan atau restart gagal, ia dilanjutkan.
2. Agent yang punya transcript tetapi belum punya percakapan tidak diringkas: tidak ada yang disampaikan dan tidak ada run yang terpakai.
3. Restart memakai `launchContext` biasa, jadi memory project, critical notes, dan MCP server ikut otomatis.
4. `waitingReady` mengabaikan state sampai `startedAt` proses baru lebih baru dari saat switch: state `ready` milik proses lama tidak boleh dipakai untuk mengetik ringkasan. Bila agent baru jatuh ke shell lebih dari 8 detik, state menjadi `manual` dan ringkasan masuk thread.

**Kembali ke agent sebelumnya.** Saat berganti, `pastRuns` di session (`[{agent, agentSessionId?, profileId?, at}]`, terbaru dulu, maksimal 8) mencatat percakapan yang ditinggalkan, hanya bila percakapannya benar-benar ada. Berganti lagi ke agent itu melanjutkan percakapan yang sama (`--resume`) dengan login yang sama, dan ringkasan yang dikirim menjelaskan apa yang terjadi selama ia pergi. Agent yang hanya bisa `continue` tidak dicatat sebagai bisa dilanjutkan persis.

## 10. Forward (D19)

- **Edge `forward`** (persisten): `trigger:"turn"` → setiap giliran sumber selesai; `"order"` → hanya giliran yang dimulai oleh order. Bila sumber grup → menunggu semua anggota yang menerima order yang sama.
- **Satu kali dari perintah** (`when X finish, pass it to Y`): rule `Forward` menempel pada order aktif sumber; bila sumber belum punya order, menunggu ≤ 4 j; setelah sumber bekerja, ≤ 30 menit dari order lalu mengirim yang sudah ada.
- Digest (teks ke target):

  ```
  Results forwarded by NextBoard from @Ash (Claude Code) and @Bo (Codex):

  ## @Ash
  <balasan>

  ## @Bo
  <balasan>

  Use these results for your next step.
  ```

- `mode:"split"` → planner membaca hasil dan memberi tiap target bagiannya sendiri: satu order per target, `forwardDepth` tetap dihitung, dan target tanpa pekerjaan tidak menerima apa pun. Tiap pembagian memakai satu giliran planner di plan pemilik, dan kegagalannya ikut hitungan cooldown §3.5.
  - Jawaban planner tidak dipercaya: hanya target forward itu yang menerima teks, dan nama lain dicatat di thread lalu dibuang.
  - Bagian yang cocok dengan pola berisiko menahan seluruh pembagian sampai pengguna mengonfirmasi (§3.7). Sesudah dikonfirmasi, tiap bagian menjadi order biasa dengan `forwardDepth` 0.
  - Planner tidak tersedia, gagal, atau tidak membagikan apa pun → seluruh digest ke target pertama, sisanya bebas, dan thread menyebut alasannya.
  - Garis yang dihapus selagi planner berpikir tidak mengirim apa pun. Hub restart selagi planner berpikir: hasil itu tidak dikirim, dan thread mencatatnya.
- Batas: ≤ 20 000 karakter per balasan, ≤ 32 000 per digest, batas satu delivery (dipotong dengan penanda); balasan bisa di-forward ≤ 30 menit setelah selesai; `forwardDepth` ≤ 3; tiap edge forward menyala ≤ 1× per 60 dtk.
- Hasil yang selesai di dalam menit itu **ditahan, bukan dibuang**: ≤ 12 hasil per edge, dikirim bersama sebagai satu digest saat menitnya habis, dan bertahan melewati restart hub. Thread mencatat berapa lama menunggu. Edge dicek ulang saat kirim: garis yang sudah dihapus tidak mengirim apa pun.
- Giliran yang dimulai oleh konteks (push memory, isi tile) bukan hasil kerja: tidak diteruskan dan tidak memakai jatah menit edge.

```jsonc
// Forward (satu kali)
{ "id": "fw_…", "projectId": "p_…", "sources": ["s_…"], "targets": ["s_…"], "mode": "digest", "orderId"?: "o_…",
  "state": "waiting"|"collecting"|"sent"|"expired"|"cancelled", "createdAt": "…", "expiresAt": "…" }
```

| Method | Path | Response |
|---|---|---|
| GET | `/api/projects/{id}/forwards` | `{"items":[Forward…]}` |
| DELETE | `/api/forwards/{id}` | `{"ok":true}` |

## 11. Loop (D20)

```jsonc
// LoopDef
{ "id": "lp_…", "projectId": "p_…", "workspaceId": "w_…", "name": "Test guard", "enabled": true,
  "trigger": { "kind": "interval", "everySec": 900 }
           | { "kind": "cron", "expr": "*/15 9-18 * * 1-5" }              // 5 field, zona waktu hub
           | { "kind": "agentIdle" }                                       // target selesai giliran
           | { "kind": "output", "pattern": "FAIL", "regex": false },      // smart-case, ≥ 2 karakter
  "text": "Run the test suite and fix any failure.",
  "targets": { "kind": "handles"|"group"|"orchestrator"|"allAgents", "ids": ["s_…"|"n_…"] },
  "stop": { "maxRuns"?: 20, "maxDurationSec"?: 7200, "until"?: "…",
            "outputPattern"?: "All tests passed",
            "goal"?: { "kind": "agentJudged", "text": "All tests pass and the build is green", "agent": "claude" }
                   | { "kind": "command", "command": "npm test", "timeoutSec": 300 } },
  "createdBy": "user"|"order", "createdAt": "…",
  "state": { "status": "running"|"paused"|"stopped"|"done", "runs": 3, "startedAt", "lastRunAt", "nextRunAt",
             "stoppedReason"?: "maxRuns"|"time"|"goal"|"output"|"user"|"error" } }
// LoopHistoryEntry
{ "at": "…", "kind": "run"|"skip"|"retry"|"goalCheck"|"stop", "note": "…",
  "targets": [{ "sessionId", "result": "delivered"|"busy"|"needsYou"|"paused"|"notAgent" }], "goal"?: { "met": false, "reason": "…" } }
```

- Scheduler di hub, tetap jalan tanpa klien dan untuk workspace di latar. Hub restart → `nextRunAt` dihitung ulang dari sekarang; run yang terlewat tidak diulang.
- Satu run: resolve target (agen saja) → delivery `source:"loop"` dengan `deadlineSec` 60 (bukan mengantre lama). Target `needsYou` dilewati (`needsYou`) + attention `loopHeld`. Run yang tidak mencapai console mana pun → `retry` sekali setelah 60 dtk.
- `agentIdle` dan `output`: ≤ 1× per 120 dtk per console; `output` mengirim begitu console bebas; `agentIdle` membiarkan giliran itu lewat bila console sudah sibuk lagi.
- Goal dicek sebelum tiap run terjadwal dan saat target selesai giliran; tidak dua kali dalam 90 dtk; ≤ 2 cek bersamaan se-hub; hasil tak meyakinkan (CLI belum login, timeout) → run tetap jalan. `agentJudged` = job runner dengan teks goal + balasan terakhir target (≤ 8000 karakter), schema `{"met":bool,"reason":string}`. `command` = exec `$SHELL -lc` di path project (machine project), exit 0 = tercapai; hanya bisa dibuat user secara eksplisit (planner tidak boleh).
- Selesai/berhenti → attention `loopEnded`. Attention API-agents §12 ditambah `kind` `loopHeld` dan `loopEnded`.
- Template bawaan (`GET /api/loops/templates`, nama milik NextBoard): Status ping, Nudge idle agents, Build fixer, Test guard, Checkpoint commit, Review round, Daily summary.

| Method | Path | Request | Response / error |
|---|---|---|---|
| GET | `/api/projects/{id}/loops` | — | `LoopDef[]` |
| POST | `/api/projects/{id}/loops` | `LoopDef` tanpa `id`/`state` | `201 LoopDef` (status `running` bila `enabled`). `400 bad_loop` (interval < 60 dtk atau > 24 j, cron tidak valid, pola < 2 karakter, `maxRuns` di luar 1–500, `maxDurationSec` di luar 900–86400), `429 too_many_loops` (> 50 per hub) |
| PATCH | `/api/loops/{id}` | field yang diubah | `LoopDef` |
| DELETE | `/api/loops/{id}` | — | `{"ok":true}` |
| POST | `/api/loops/{id}/start`, `/stop`, `/run-now` | — | `LoopDef` |
| POST | `/api/loops/stop-all` | — | `{"stopped":N}` |
| GET | `/api/loops/{id}/history?limit=50` | — | `{"items":[LoopHistoryEntry…]}` |

## 12. Memory project (D21)

- **1 project = 1 memory.** Folder hub `memory/<projectId>/`, dengan `MEMORY.md` sebagai main memory: Memory panel, halaman Memory, push, scan, planner, dan semua CLI memakai file yang sama, jadi berganti agent, menutup orchestrator, atau pindah CLI tidak mengubah apa yang diketahui project. File `.md` lain di folder itu adalah catatan agent. `memory/global.md` berlaku untuk semua project. Tidak ada yang ditulis ke machine mana pun.
- **Sub-memory:** tiap main memory (project atau semua project) punya halaman per topik, misalnya design system atau do and don't, di `memory/subs/<projectId|global>/<slug>.md`, dengan `title` dan `description` di front matter. `description` adalah cara agent memilih apa yang dibaca. Maksimal 64 per main memory, 64 KB per halaman. Berkas ada di luar folder auto memory Claude Code, jadi semua CLI mencapainya dengan cara yang sama.
- **Critical notes:** sub-memory `critical` selalu ada di tiap main memory dan tidak bisa dihapus (`409 critical_required`); bila filenya hilang, dibuat lagi. Isinya aturan yang tidak boleh dilanggar agent. Project baru mulai dengan aturan dasar (drop database, reset data lewat test atau seeder, hapus data, riwayat git, production, secret); yang untuk semua project mulai kosong. Maksimal 16 KB.
  - Selalu masuk utuh dan paling depan ke prompt launch tiap CLI, ke `memory_read`, ke push, dan ke `critical` di snapshot planner, tidak terpotong anggaran memory. Baris yang sama di kedua tingkat hanya muncul sekali.
  - Hanya pemilik yang mengubahnya lewat API atau UI. Tool agent menolak (`memory_save` ke `critical`), dan file-nya di luar folder yang boleh ditulis Claude Code tanpa izin.
  - Berbeda dari memory biasa yang disajikan sebagai latar belakang: permintaan langsung pun tidak dihitung sebagai izin. Agent harus berhenti, menyebut aturannya, dan menunggu konfirmasi untuk satu tindakan itu.
  - **Ini instruksi, bukan kunci teknis.** Agent di This PC punya Bash sendiri, jadi kepatuhan bergantung pada model. Diuji sungguhan dengan Haiku pada 2026-11-03: `npm run db:reset` tidak dijalankan dan agent bertanya dulu. Yang ditegakkan oleh CLI atau hub ada di daftar perintah terblokir di bawah.
- **Perintah terblokir:** daftar milik pemilik per project dan untuk semua project, di `memory-blocked.json`, diubah lewat halaman critical notes atau API, dan tidak terjangkau tool agent. Pola adalah awal perintah (`git push --force` juga mencakup yang mengikutinya), `*` berarti apa saja (`*db:reset*` menemukannya di mana pun di perintah). Maksimal 64 pola, 200 karakter. Mulai pertama kali hub jalan, daftar semua project berisi yang disebut critical notes bawaan; pemilik bebas mengubah atau mengosongkannya. Berlaku sejak console berikutnya start.
  - **Claude Code:** `permissions.ask` lewat `--settings` per launch, ditulis `Bash(*pola*)` karena CLI hanya menanggalkan beberapa pembungkus dan tidak menembus `bash -c`. CLI berhenti di dialog izin, status menjadi `needsYou`, dan pemilik menjawab di console. Commit message yang mengutip perintah itu ikut bertanya. Diuji sungguhan dengan Haiku pada 2026-11-05 tanpa skip permissions, termasuk lewat `bash -c`: skrip tidak jalan dan layar menyebut aturannya. Dokumentasi Claude Code menyatakan aturan `ask` tetap berlaku di `bypassPermissions`; itu belum diuji sendiri.
  - **Codex:** `prefix_rule(..., decision = "prompt")` di `rules/nextboard.rules`, hanya di folder config buatan hub, tidak pernah di milik pengguna. Satu folder dipakai semua console, jadi daftarnya gabungan semua project. Codex mencocokkan kata dari awal perintah, jadi pola dengan `*` selain di akhir dilewati. Format diperiksa dengan `codex execpolicy check`; penegakannya belum diuji dengan model sungguhan.
  - **OpenCode:** `permission.bash` lewat `OPENCODE_CONFIG_CONTENT`, juga ditulis `*pola*`. Belum diuji dengan model sungguhan.
  - **Agent di server:** tool `bash` milik hub menolak sendiri tanpa menjalankan apa pun, dengan pesan yang meminta agent berhenti dan bertanya. Bagian perintah majemuk diperiksa satu per satu, termasuk di balik `sudo`, `env`, dan `bash -c`.
  - CLI lain: tidak ada penegakan, hanya critical notes.
  - **Bukan sandbox.** Agent yang punya shell bisa menulis skrip yang melakukan hal sama, atau menyunting file data hub, karena berjalan sebagai user yang sama. Ini menghentikan perintah yang dikenali, bukan niat.
- **Cara tiap CLI memakainya (per launch, tanpa menulis config CLI):**
  - Claude Code: `autoMemoryDirectory` lewat `--settings` menunjuk folder itu, jadi dia membaca dan menulis main memory langsung. Memory lama dari folder config buatan hub (`<config>/projects/<slug>/memory`) digabungkan sekali. Tool hub ditambahkan lewat `--mcp-config` tanpa `--strict-mcp-config`, jadi MCP milik pengguna tetap ada.
  - Codex dan OpenCode: isi memory disuntik ke instruksi sistem saat launch (`developer_instructions`, `instructions`, ≤ 16 000 karakter).
  - Agen lain: lewat push seperti sebelumnya.
  - Planner: `critical` dan `memory` di snapshot (`memory` ≤ 4000 karakter).
- **Mencari per topik (Context7 versi kecil, khusus project ini):** agent tidak membawa semua memory. Di prompt launch hanya ada critical notes, main memory, dan daftar halaman; sisanya dicari saat dibutuhkan lewat tool hub, untuk semua CLI:
  - `memory_search{query, limit?}`: potongan yang cocok dari main memory, sub-memory, catatan agent, dan memory semua project; ≤ 6 potongan, ≤ 4000 karakter. BM25 atas potongan berjudul, di dalam proses, tanpa indeks. Judul dan `description` berbobot lebih; bagian identifier dipecah (`userAuth_token`); kata awalan ikut cocok.
  - `memory_read{name?, topic?}`: tanpa argumen isi memory dan daftar halaman; dengan `name` satu halaman (`global/<slug>` untuk milik semua project); dengan `topic` hanya bagian tentang itu.
  - `memory_save{fact, section?, page?}`: menambah satu fakta ke main memory atau ke sebuah halaman.
  - `memory_search` dan `memory_read` tidak meminta izin (Claude Code lewat `--allowedTools`, OpenCode lewat `permission`, dan `readOnlyHint` di definisi tool); `memory_save` mengikuti aturan izin CLI.
- Memory biasa disajikan sebagai latar belakang, bukan perintah, karena isinya bisa ditulis agen lain dan bisa basi. Agen yang sedang berjalan mendapat isi terbaru saat launch berikutnya, lewat `memory_read`, atau push. `POST …/memory/push` dengan `scopes: ["critical"]` mengirim hanya critical notes ke semua agen project yang sedang berjalan.
- Konvensi `MEMORY.md`: bagian `## Architecture`, `## Structure`, `## Commands`, `## Conventions`, `## Gotchas`, `## Context`; satu fakta = satu bullet. User bebas menyunting. `[[nama]]`, `[[nama|tampilan]]`, dan `[[nama#bagian]]` menautkan dokumen dalam main memory yang sama.
- **Init memory:** job runner baca-saja (purpose `memory-init`, di machine project, cwd = path project, batas 20 menit), effort `quick`/`balanced`/`thorough`. Mengusulkan sekaligus fakta untuk main memory, sub-memory dengan isinya, dan critical rules dari apa yang bisa merusak project ini secara khusus: perintah yang drop atau reset database, test yang menghapus data atau menunjuk database asli, migration yang tidak bisa dibatalkan, skrip deploy. Prompt-nya melarang membuka `.env` dan file rahasia. **Tidak ada yang disimpan sampai pemilik menerima**, dan menyimpan hanya menambah: tulisan yang ada tidak berubah, dan menerapkan dua kali tidak menggandakan.
- Scan: job runner (tool baca-saja, di machine project, cwd = path project), effort `quick`/`balanced`/`thorough` → 6–8 / 8–11 / 11–14 usulan `{kind, text}`. Tidak ada yang disimpan sampai user menerima.
- **Memory tile:** node `memory` adalah tumpukan seperti lego. Memory project sendiri selalu aktif sebagai dasar; blok dari memory semua project, halaman project ini, atau project lain ditambahkan di atasnya, dan bisa diurutkan dengan drag atau Alt+panah: yang lebih atas didahulukan saat anggaran habis. Wire ke console, grup, card orchestrator, atau editor; agen yang sampai adalah console dan grup di ujung wire, ditambah yang di-wire dari orchestrator atau editor itu. Satu console yang terhubung ke beberapa tile mendapat gabungan bloknya. Critical notes tidak bisa jadi blok: milik project sendiri dan semua project selalu ikut, milik project lain mengikat project itu saja.
- **Bagaimana tumpukan sampai ke agen, dari yang termurah:**
  - **Launch:** blok masuk system prompt, tanpa memakai turn, dalam batas 16 000 karakter yang sama dengan memory project. Yang tidak muat atau diset `lookup` hanya masuk sebagai satu baris berisi nama.
  - **Tool:** `memory_search` dan `memory_read` langsung mencakup blok yang terhubung ke console itu, tanpa diketik apa pun. Nama memory project lain berbentuk `<projectId>/<nama>`. Hanya blok di tile yang terhubung yang bisa dibaca, bukan isi lain project itu. `memory_save` tetap hanya ke project sendiri.
  - **Planner:** blok tile yang sampai ke card orchestrator-nya masuk `memoryAdded` (≤ 2000 karakter, karena planner jalan tiap perintah dan tidak punya tool). Command bar utama adalah lead yang sama, jadi mendapat blok tile yang sampai ke card orchestrator mana pun di board itu.
  - **Diketik (memakai turn):** sekali saat wire dibuat, saat blok ditambahkan, atau saat deploy layout; selebihnya hanya bila diminta. Isi memory yang berubah **tidak** pernah mengetik sendiri, supaya agen yang menyimpan fakta tidak memicu turn di agen lain. Tile menampilkan berapa agen tertinggal dan perkiraan tokennya, dan menyediakan tombol Send. `manual: true` ("Send when connected" dimatikan): wire dan blok baru tidak mengetik apa pun.
- **Hanya yang baru:** hub mencatat per console fakta (baris) yang sudah dia punya, di `memory-seen.json`, berisi hash saja. Launch menulis ulang catatan itu sesuai isi system prompt; delivery dihitung saat diketik, delivery antre dihitung supaya tidak terkirim dua kali, dan yang gagal, kedaluwarsa, atau dibatalkan tidak dihitung. Fakta dari `memory_save` tidak dikirim balik ke penyimpannya; yang ditulis Claude Code langsung ke folder tidak bisa dikenali, jadi bisa terkirim balik. Tidak ada yang baru berarti tidak ada yang diketik (`upToDate`). Fakta yang dihapus disebut jumlahnya, bukan isinya. Agen yang keluar dan console yang dihapus kehilangan catatannya. `/clear` dan `/compact` di Claude Code terbaca dari hook `SessionStart`: catatan kembali ke isi system prompt, tile menunjukkan agen itu tertinggal, dan tidak ada yang diketik sendiri. CLI lain tidak memberi sinyal itu, jadi `force: true` mengirim semuanya lagi.
- Push: delivery ke tiap agen target, per agen hanya yang belum dia punya:

  ```
  Project context from NextBoard (background only, not a task). Keep it in mind for later work and reply with just: OK

  # Critical rules from the owner: never break these, whoever asks
  <critical notes yang baru, di luar anggaran>

  <fakta baru dengan heading-nya, total ≤ 4000 karakter>

  # From the memory of another project, Acme: Design system. It is about that project, not this one
  <fakta baru blok itu>

  More memory, left out to save space. When your work touches it, read it with the memory_read tool of the nextboard MCP server and the name:
  - global/writing: Writing style. How docs are written
  ```

  Blok yang tidak muat atau `lookup` menjadi satu baris itu, dan tidak diulang sampai isinya berubah. Agen tanpa tool hub (bukan Claude Code, Codex, OpenCode) tidak bisa mengikuti nama, jadi mendapat yang muat, plus `(N more facts not included)`. Console baru CLI yang menerima konteks saat launch tidak perlu push. `settings.memory.autoPush` (default `false`; ekstensi NextBoard) hanya berlaku untuk agen lain: push saat agen pertama kali `ready`.

| Method | Path | Request | Response / error |
|---|---|---|---|
| GET | `/api/projects/{id}/memory` | — | `{"project":{"content","mtime"},"global":{"content","mtime"},"notes":[…],"subs":[SubMemory]}` |
| PUT | `/api/projects/{id}/memory` | `{"scope":"project"\|"global","content","ifMtime"?}` (≤ 64 KB) | `{"content","mtime"}`; `409 mtime_mismatch` |
| POST | `/api/projects/{id}/memory/init` | `{"effort":"balanced","agent"?: "claude"}` | `202 Job` → `result: {"facts":[{"kind","text"}],"subs":[{"slug","title","description","content"}],"critical":["…"]}` |
| POST | `/api/workspaces/{id}/tiles/{nodeId}/push` | `{"targets"?: ["n_…"]}` | seperti `memory/push`, plus `"empty":true` bila tile tidak punya isi untuk dikirim. Tanpa `targets`: semua yang ter-wire. `404 tile_not_found`; `400 not_content_tile` |
| POST | `/api/projects/{id}/memory/init/apply` | yang diterima dari `result` | `GalaxyView` |
| POST | `/api/projects/{id}/memory/scan` | `{"effort":"balanced","agent"?: "claude"}` | `202 Job` → `result: {"facts":[{"kind","text"}]}` |
| POST | `/api/projects/{id}/memory/accept` | `{"scope","facts":[{"kind","text"}]}` | `{"content","mtime"}` (bullet identik tidak digandakan) |
| POST | `/api/projects/{id}/memory/refresh` | `{"fact":"…"}` | `202 Job` → `{"verdict":"valid"\|"outdated"\|"unknown","suggestion"?}` |
| POST | `/api/projects/{id}/memory/push` | `{"memoryNodeId"?, "workspaceId"?, "targets"?: ["s_…"], "blocks"?: [{"projectId"?,"name","lookup"?}], "scopes"?: ["project","global","critical"], "force"?: false}` | `{"delivered":2,"busy":1,"skipped":0,"notAgent":1,"upToDate":1,"chars":840,"deliveryIds":[…]}`. `memoryNodeId`: tumpukan tile itu, ke yang terhubung saja (tile tanpa wire tidak mengirim ke siapa pun). `blocks`: memory ini, `[]` berarti hanya critical notes. `scopes` sama dengan `blocks` berisi main memory. Tanpa `targets` dan tile: semua agen workspace, atau semua agen project bila tanpa `workspaceId`. `404 tile_not_found` |
| POST | `/api/projects/{id}/memory/plan` | sama dengan `push` | `{"docs":[{"projectId"?,"name","title","owner","chars","locked"?,"lookup"?,"missing"?}],"targets":[{"sessionId","handle","agent","workspaceId","state","canSend","chars","lookup":[…]}],"budget":4000}`. Apa yang akan dilakukan `push`, tanpa mengetik apa pun; `chars: 0` berarti agen itu sudah punya semuanya |
| GET | `/api/memory/blocked?projectId=` | — | `{"commands":["…"],"inherited"?:["…"]}`. Tanpa `projectId`: daftar semua project |
| PUT | `/api/memory/blocked?projectId=` | `{"commands":["…"]}` | `{"commands":["…"]}`, sudah dibersihkan. `400 bad_blocked` |
| GET | `/api/memory/galaxy` | — | `GalaxyView`: `{"global":Owner,"projects":[Owner]}`, `Owner = {"projectId"?,"name","docs":[{"name","kind":"main"\|"sub"\|"note","title","description"?,"critical"?,"chars","facts","mtime"?,"sections":[{"title","chars"}],"links":["…"]}]}`. Tanpa isi. Project terlama dulu, supaya tempat dan warnanya tetap |
| GET | `/api/memory/search?q=&projectId=&limit=` | — | `{"hits":[{"projectId"?,"project"?,"doc","title","section"?,"text","critical"?,"global"?,"score"}]}`; tanpa `projectId` mencari di semua project; `limit` ≤ 20 |
| GET | `/api/memory/docs/{name}?projectId=` | — | `{"name","kind","title","description"?,"critical"?,"content","mtime"}`. `name`: `main`, slug sub-memory, atau `note:<file>`. Tanpa `projectId`: memory semua project. `404 memory_not_found` |
| PUT | `/api/memory/docs/{name}?projectId=` | `{"title"?,"description"?,"content","ifMtime"?}` | seperti GET; membuat bila belum ada. `400 bad_memory_name`; `409 mtime_mismatch`; `409 too_many_memories`; `413 memory_too_big` |
| DELETE | `/api/memory/docs/{name}?projectId=` | — | `{"ok":true}`; `409 critical_required` |

## 13. Template layout (B8)

```jsonc
// Layout — koordinat relatif terhadap titik deploy
{ "id": "builtin:duo" | "ly_…", "name": "Duo", "builtin": true, "createdAt"?: "…",
  "lanes":  [{ "key": "a", "agent"?: "claude", "role"?: "reviewers", "group"?: "g1", "x": 0, "y": 0, "w": 720, "h": 440 }],
  "groups": [{ "key": "g1", "name": "Builders", "role": "general", "color": "sky" }],
  "nodes":  [{ "key": "o1", "kind": "orchestrator"|"kanban"|"note"|"text"|"checklist"|"memory"|"web"|"editor", "x", "y", "w", "h", "data": {…} }],
  "wires":  [{ "from": "o1", "to": "g1", "kind": "wire"|"forward" }] }
```

Built-in (nama NextBoard): **Duo** (grup 2), **Trio** (grup 3), **Quartet** (grup 4), **Two quartets** (2 grup × 4), **Build & review** (2 builder + 1 reviewer + orchestrator + kanban), **Front & back** (grup frontend 2 + grup backend 2 + orchestrator), **Review trio** (3 reviewer), **Triage** (2 tester + 1 reviewer + kanban "Triage"), **Preview lab** (3 agen + web tile `localhost:5173`), **Docs crew** (2 docs + checklist), **Solo** (1 agen + checklist), **Sextet** (grup 6), **Three trios** (3 grup × 3 + orchestrator), **Lead & crew** (grup 4 + orchestrator + kanban), **Full stack** (frontend 2, backend 2, tester 2, reviewer 2 + orchestrator + kanban), **Code & pair** (card editor + grup 2), **Web builder** (frontend + backend + card editor + web tile), **Bug hunt** (2 tester + 2 fixer + kanban "Bugs" + orchestrator), **Security audit** (2 security + 1 reviewer + checklist audit terisi), **Design studio** (2 design + web tile + catatan), **Release crew** (2 builder + tester + docs + kanban "Release" + orchestrator), **Research desk** (2 agen + kartu memory + catatan).

| Method | Path | Request | Response / error |
|---|---|---|---|
| GET | `/api/layouts` | — | `Layout[]` (built-in dulu) |
| POST | `/api/layouts` | `{"name"?, "workspaceId", "nodeIds"?}` | `201 Layout` (nama default `Set N`) |
| PATCH | `/api/layouts/{id}` | `{"name"}` | `Layout` |
| DELETE | `/api/layouts/{id}` | — | `{"ok":true}`; `403 layout_builtin` |
| POST | `/api/workspaces/{id}/deploy` | `{"layoutId", "at"?: {x,y}, "fill"?: {"agent"}, "laneOverrides"?: {"a": {"agent"?, "machineId"?, "profileId"?, "yolo"?}}}` | `201 {"sessions":[Session…],"nodeIds":[…]}`; `404 layout_not_found`, `400 too_many` |

- Deploy sepenuhnya di hub (Android bisa deploy), satu siaran `boardOps`. Agen lane: override → `fill` → `lane.agent` → role agent → default machine (machine = override atau machine project). Agen tak dikenal → default; custom yang sudah dihapus → `shell`. Console mendapat handle baru.
- Simpan layout **menyimpan**: kind, geometri, judul/teks/warna, agen lane, grup, orchestrator, kartu editor (tanpa file yang terbuka), tumpukan memory tile, wire/forward, judul kolom kanban, URL web tile localhost. **Membuang**: gambar, ink, kartu file, isi kartu kanban/checklist/kalender, URL eksternal. Deploy ke board yang sudah punya editor tidak menambah editor kedua: wire-nya memakai yang ada.
- Home screen "arm layout": klien membuka workspace lalu memanggil `deploy`.

## 14. Event baru di `/api/events`

```jsonc
{ "type": "delivery", "delivery": Delivery }
{ "type": "thread", "threadId": "p_…", "message": ThreadMessage }      // baru atau diperbarui (id sama)
{ "type": "threadCleared", "threadId": "p_…" }
{ "type": "order", "order": Order }
{ "type": "confirmation", "confirmation": Confirmation }
{ "type": "command", "commandId": "cmd_…", "clientId": "c_…", "state": "planning"|"done"|"failed", "uiActions"?: [UiAction…] }
{ "type": "handoff", "handoff": Handoff }
{ "type": "forward", "forward": Forward }
{ "type": "loop", "loop": LoopDef }      { "type": "loopRemoved", "id": "lp_…" }
{ "type": "loopRun", "loopId": "lp_…", "entry": LoopHistoryEntry }
{ "type": "memory", "projectId": "p_…", "scope": "project", "mtime": "…" }
{ "type": "layouts" }
```

`session` membawa `paused`, `pausedAt`, `queue`. Topik untuk `?topics=` = nilai `type` di atas.

## 15. Persistensi

| File | Isi |
|---|---|
| `threads/<tid>.jsonl` | append-only; dipadatkan saat lewat batas (project 500 pesan, kartu 200) |
| `orders.json` | order aktif + yang selesai < 24 j (maks 200) |
| `forwards.json` | forward satu kali yang belum selesai |
| `loops.json` | `LoopDef[]` termasuk `state` |
| `loops/<id>.jsonl` | riwayat (200 entri terakhir) |
| `memory/<projectId>/MEMORY.md`, `memory/global.md` | memory |
| `memory-blocked.json` | perintah terblokir per project dan untuk semua project (§12) |
| `memory-seen.json` | per console, hash fakta memory yang sudah dimiliki; tanpa isi (§12) |
| `handoffs.json` | handoff beserta ringkasannya, yang selesai dibuang setelah 24 jam (§9) |
| `layouts.json` | layout simpanan (built-in di dalam binary) |
| `settings.json` | key baru `delivery`, `commands` (`planner`, `confirmTtlSec`, `reportTimeoutMin`, `riskyPatterns`), `memory` (`autoPush`) |
| `sessions.json` | + `paused` |
| `confirmations.json` | baru: konfirmasi yang masih tersimpan beserta konteks perintahnya |

Delivery hanya di memori (hilang saat hub restart; thread mencatat "interrupted by hub restart"). Teks order tetap ada di `orders.json`, jadi order yang belum diketik bisa dikirim ulang (§4). Konfirmasi selamat dari restart: tombol dan jawaban lewat teks tetap jalan, TTL-nya tidak mulai dari awal, dan keputusan yang sudah diambil tidak bisa dijalankan dua kali. Giliran planner yang terputus restart tidak diulang dan tidak menjalankan apa pun; thread mencatat "The hub restarted before this was answered. Send it again."

## 16. Batas dan timeout

| Hal | Nilai |
|---|---|
| Teks command | ≤ 4000 karakter; planner hanya ≤ 600 |
| Delivery | ≤ 32 000 karakter; ≤ 20 antre per sesi; konfirmasi 8 dtk |
| Planner | 2 menit; 1 giliran per thread; cooldown 60 dtk setelah 2 gagal |
| Konfirmasi | TTL 60 dtk (30–300) |
| Spawn | ≤ 12 per perintah/panggilan; ≤ 64 sesi per workspace |
| Order report timeout | 30 menit |
| Handoff | ringkas 4 menit, ready 120 + 120 dtk |
| Forward | 20 000 / 32 000 karakter; tunggu order 4 j; 30 menit setelah order |
| Isi tile | 20 000 karakter; 50 masalah halaman terbaru |
| Loop | interval 60 dtk–24 j; maxRuns 1–500; durasi 15 menit–24 j; 50 loop per hub; goal 90 dtk / 2 bersamaan |
| Memory push | 4000 karakter |
| Mention autocomplete | 30 item |

## 17. Keamanan

- Teks orkestrasi masuk ke terminal **hanya** lewat delivery engine dengan guard agen; tidak pernah ke shell, tidak pernah ke prompt izin. Endpoint lama `POST /api/sessions/{id}/input` tetap untuk input manual user.
- Planner hanya di This PC, memakai plan user; snapshot tanpa isi file/transcript. Output planner dianggap tidak tepercaya: nama diterjemahkan hub, step di luar subset dibuang, kebijakan konfirmasi berlaku.
- Goal `command` menjalankan perintah shell pilihan user; tidak bisa dibuat planner, tampil penuh di konfirmasi loop.
- Pause memakai PID dari marker bertoken dan memvalidasi session id proses; tidak pernah mengirim sinyal ke PID di luar sesi terminal itu.
- Tidak ada tulisan ke machine remote dari fase ini (memory, thread, loop disimpan di hub), kecuali efek order yang dikerjakan agen sendiri.
- Forward meneruskan teks antar agen (risiko prompt injection berantai): digest selalu diberi label sumber, `forwardDepth` dibatasi, edge forward tanpa siklus.

## 18. UX per klien (yang wajib ada)

- **Web/desktop:** command bar di bawah board (Ctrl/⌘K fokus, Esc kembali ke board, ↑/↓ riwayat 50, menu `@` dengan ↑/↓, Tab/Enter, Esc); chip New console, Spawn 4, Group, Layouts, Pause all, Resume all, Memory, dan saat thread terbuka: Loops, Mission Map, Kill all (dua klik + modal). Panel thread: plan note, report dengan "See output", kartu konfirmasi dengan tombol + hitung mundur, "Still thinking…". Badge antrean delivery di header console ("2 queued", klik = daftar + Cancel). Bingkai grup dengan nama/role/warna, composer 4/6 slot (≤ 12, klik siklus agen, klik kanan katalog, isi semua). Kartu orchestrator (input, thread, mode Connect, aksi baris). Mission Map (`X`, tidak bisa di-rebind) dengan filter dan pencarian. Panel loop (`L`): template, estimasi prompt/jam × console, riwayat, badge jumlah loop berjalan. Panel memory (`M`): edit, scan, terima usulan, kartu memory + hasil push. Library layout + inspector lane (agen, machine, profil) + Deploy.
- **Android:** layar project utama = command bar + thread (laporan, konfirmasi bisa disetujui), "Mission control" (needs-you dulu, filter), pause/resume per console, daftar loop (start/stop/run now), deploy layout dari layar project, notifikasi `loopHeld`/`loopEnded`.

## 19. Skenario tes wajib

1. Delivery tidak pernah mengetik ke shell: agen `/exit` saat delivery antre → `failed agent_exited`; console `kind:"shell"` → `409 not_agent`.
2. Delivery menunggu `needsYou` dan mengirim setelah izin dijawab; tidak ada byte yang ditulis selama menunggu (uji dengan PTY palsu).
3. Bracketed paste: teks multi-baris dibungkus `ESC[200~…ESC[201~` bila mode 2004 aktif; tanpa mode → satu baris; ESC di teks dibuang.
4. Guard manusia mengetik: input klien dalam 2 dtk menunda delivery.
5. Parser: setiap baris tabel §3.2 punya tes; `@everyone` = tanpa mention; mention tanpa teks → balasan nudge.
6. Fuzzy: `@Ahs` → `didYouMean` Ash; dua kandidat → `sendToAll`; konfirmasi dari klien kedua setelah klien pertama → `409 already_decided`.
7. Konfirmasi: `kill` selalu pending; ketik `yes` → jalan; teks lain → batal + diproses; kedaluwarsa 60 dtk.
8. Planner: Claude tidak ada → `available:false`, parser fallback dengan catatan; JSON tidak valid → fallback; dua gagal → cooldown; snapshot tidak berisi isi file.
9. Order tracker: report `finished` dengan excerpt dari transcript; timeout 30 menit; `cancel that` membatalkan delivery yang belum diketik saja.
10. Grup: anggota ganda `400`; nama sama dengan handle `409`; hapus terminal → keluar dari `members`.
11. Batch spawn 4 dengan grup + orchestrator → satu `boardOps` berisi semua node/edge.
12. Pause lokal: SIGSTOP ke pgrp foreground; resume → SIGCONT + nudge resize; delivery menunggu saat paused. Remote: skrip menolak bila sid berbeda.
13. Handoff: runner gagal → brief mentah; target `needsYou` lama → `manual`.
14. Forward: digest dua sumber, pemotongan 20 000/32 000, edge siklus ditolak, `forwardDepth` > 3 tidak dikirim.
15. Loop: berjalan tanpa klien tersambung; interval < 60 dtk ditolak; goal `agentJudged` tidak dua kali dalam 90 dtk; target needsYou → `skip` + `loopHeld`; hub restart tidak mengulang run terlewat.
16. Memory: scan tidak menyimpan apa pun sebelum `accept`; push memotong di 4000 karakter dengan catatan.
17. Deploy layout dari Android (bearer) membuat semua sesi + grup + wire; override machine per lane dihormati.

## 20. Open issues / untuk diverifikasi

Status per butir setelah implementasi dan verifikasi ada di §21; butir 1 dan 2 terjawab, butir 5 sebagian.

1. **Runner di Fase 6:** planner, handoff, goal, dan memory scan bergantung pada runner (API-studio §2) yang tercatat di Fase 7 roadmap; urutan kerja perlu disesuaikan.
2. **Flag planner:** `claude -p --json-schema`, `--tools ""`, `--no-session-persistence` — keberadaan dan perilaku di 2.1.x; fallback = instruksi blok JSON di prompt + validasi hub. Latensi (5–15 dtk) dan pemakaian plan user per perintah.
3. **Agen bebas tanpa hooks:** `ready` dari layar/aktivitas bisa salah; pembagian kerja bisa mengirim ke agen yang sebenarnya sibuk (delivery tetap aman karena menunggu).
4. **`whileWorking:"type"`**: hanya aman untuk CLI yang mengantrekan input saat bekerja; perlu daftar per CLI.
5. **SIGSTOP pada TUI Node/Rust**: permintaan API yang sedang berjalan bisa timeout; redraw setelah SIGCONT; `ps -o tpgid=`/`sid=` di BusyBox mungkin tidak ada → `409 pid_unknown`.
6. **Bingkai grup** dihitung klien; dua klien memindahkan anggota bersamaan bisa membuat bingkai sedikit tidak akurat sampai patch berikutnya (last-writer-wins).
7. **Pencocokan fonetik:** Double Metaphone hanya cocok untuk nama berbahasa Inggris, sedangkan ejaan Indonesia punya pelipatan sendiri (`indonesianKey`). Nama handle bawaan tetap berbeda menurut pelipatan itu (`TestDefaultHandlesFoldApart`).
8. **Ringkasan handoff di This PC** untuk sumber remote memindahkan isi transcript remote ke This PC (masih milik user); butuh persetujuan pemilik bila dianggap sensitif.
9. **Cron** memakai zona waktu hub; klien di zona lain melihat `nextRunAt` UTC.
10. **Laporan untuk agen tanpa transcript** diambil dari layar dan bisa berisi sisa UI.
11. **Kehilangan state saat hub restart**: delivery/handoff yang sedang berjalan hilang (konfirmasi sudah dipersist, §15). Diputuskan (keputusan #34): antrean tidak dilanjutkan otomatis, order yang belum diketik ditawarkan untuk dikirim ulang (§4). Handoff tetap hilang.

## 21. Hasil verifikasi (This PC, 2026-10-28)

Claude Code **2.1.312** (`--model haiku` untuk console, model default untuk planner), Codex **0.165.0** (tidak dijalankan interaktif di fase ini). Uji nyata di `internal/server/realcli_orch_test.go` `TestRealOrchestration` (gerbang `NEXTBOARD_REAL_CLI_TEST=1 NEXTBOARD_REAL_ORCH=1`, tidak pernah jalan di `go test` biasa; `newTestHub` juga memaksa `commands.planner:"off"` supaya tes lain tidak pernah menyentuh plan pemilik). **Total 2 prompt kecil** (1 run planner, 1 order ke console nyata). `DISABLE_AUTOUPDATER=1` di env hub (diwarisi console dan runner), tanpa yolo/skip-permissions, console memakai `--permission-mode manual` di folder scratch yang sudah di-trust sejak Fase 5. Checksum `~/.claude/settings.json` dan `~/.codex/config.toml` sama sebelum/sesudah (dicek tes dan manual).

| # | Butir | Hasil | Bukti |
|---|---|---|---|
| 1 | Runner di Fase 6 | Dipakai: `internal/jobs` `SubmitRunner` (purpose `planner`, `handoff-summary`, `loop-goal`, `memory-scan`, `memory-init`, `memory-refresh`, `thread-summary`); `RunnerRequest.Transform` (baru) membentuk hasil per purpose (mis. memory scan → `{"facts":[…]}`) | `internal/orch` |
| 2 | Planner `claude -p --output-format json --json-schema '<PlanSchema>' --tools "" --max-turns 1 --permission-mode dontAsk --no-session-persistence` | **Terkonfirmasi.** `--json-schema` bersama `--tools ""` bekerja: hasil `result` berisi JSON dan objek `structured_output` terisi (sesuai schema: `reply` + `steps[{op:"send",targets:["Ash"],text:…}]`). Latensi 4,4 dtk (job), "Still thinking…" muncul lalu diganti "Planned." Nama `Ash` diterjemahkan hub ke sesi | log `TestRealOrchestration`: `PLANNER state=done elapsedMs=4396`, `structured_output present=true` |
| 3 | Delivery ke console Claude interaktif nyata | **Terkonfirmasi.** Order dari planner → delivery `queued` → diketik (bracketed paste, Claude mengaktifkan DECSET 2004) → `sent` 0,36 dtk → `confirmed` 0,46 dtk (hook `UserPromptSubmit`); order `finished` 2,1 dtk; report thread = "OK" dari transcript | log: `DELIVERIES final … state:confirmed`, `THREAD agent/report: "OK"`; fixture `claude-2.1.312-ready-after-order.txt` |
| 4 | Dialog trust pada console sempit (60 kolom) | Di 60 kolom pertanyaan terlipat ("…or one / you trust?") sehingga pola kalimat penuh Fase 5 tidak lagi cocok. Dengan pola pendek baru: `needsYou` (`stateSource:"screen"`, `activity:"Trust folder?"`); delivery `1` ditahan (`waitReason:"needsYou"`), layar tidak berubah; Esc (input manusia) membatalkan → agen keluar rc 0 → shell, **tanpa** entri trust baru di `~/.claude.json` | fixture `claude-2.1.312-needsYou-trust-60cols.txt` (layar & dialogs) |
| 5 | Pause/resume | Lokal: `TIOCGPGRP` + `kill(-pgid, SIGSTOP/SIGCONT)` pada PTY nyata (`/proc/<pid>/stat` = `T`, output berhenti, lanjut setelah CONT). Remote (sshd OpenSSH sekali pakai, loopback): skrip §8 bekerja **setelah** `kill -STOP -- "-$t"` diganti `kill -s STOP -- "-$t"` (dash menolak bentuk pertama: "Illegal number"); pid tanpa terminal / sesi lain ditolak | `TestLocalSignalForeground`, `TestSSHSignalForeground` (gerbang `NEXTBOARD_SSHD_TEST`), `TestRemoteSignalScriptRefusesForeignSession`, `TestPauseResumeLocal` |
| 6 | Update check Codex | Codex 0.165.0 punya key config `check_for_update_on_startup` (ditemukan di binary), jadi `-c check_for_update_on_startup=false` per-launch tersedia. Fase ini **tidak** menjalankan Codex interaktif; hub belum menambah flag itu ke launch Codex (keputusan pemilik) — perlindungan utamanya delivery guard di bawah | `strings` binary |

**Keamanan delivery (aturan tambahan pemilik):** teks hanya diketik bila layar **dikenali positif** sebagai prompt input normal CLI (`agent.InputPrompt`): Claude = baris kursor `❯` di kolom 0 di antara dua garis `─`; Codex = `›` di kolom 0 bukan opsi bernomor; agen tanpa aturan = karakter prompt di baris kursor. Penanda dialog kuat (opsi bernomor terpilih `› 1.`, "Enter to confirm/continue", "Esc to cancel/skip", "Update now", "Skip until next version", `[y/n]`, "press any key", panah) di baris bawah/sekitar kursor selalu menolak; kata-kata dialog yang juga muncul di prosa ("Do you want…") hanya dicek tepat di sekitar kursor agen tanpa kotak input, supaya balasan agen di atas kotak Claude tidak memblokir. Layar harus diam ≥ 300 ms sebelum mengetik; tepat sebelum Return layar dicek ulang (`agent.InputPromptPasted`, menerima input yang sudah terlipat di dalam kotak) — dialog yang muncul di antara paste dan Return → `failed submit_aborted`, Return tidak dikirim. 22 fixture dialog di `internal/agent/testdata/dialogs/` (dump nyata Claude 2.1.312/Codex 0.163.0 + varian sintetis: dialog update Codex dengan kursor di "Update now", tanpa header, hanya opsi; login/theme/API key/model picker Claude; update Claude; trust/login/approval Codex; `[Y/n]`; prompt shell; layar kosong) diuji ditolak di `TestInputPromptRefusesDialogs` dan lewat engine di `TestDeliveryRefusesDialogs` (tidak ada byte tertulis, akhir `failed prompt_unrecognized`), plus uji hub `TestDeliveryNeverTypesIntoUpdateDialog` (agen palsu menampilkan dialog update setelah `SessionStart`; tidak ada input yang sampai).

**Perbaikan Fase 5 yang ikut dikerjakan (temuan e2e web):**

- **needsYou basi dengan hooks aktif:** setelah hook mengakhiri `needsYou` (mis. `PostToolUse` setelah izin dijawab) sementara dialog yang sudah dijawab masih tergambar, layar tidak lagi menaikkan `needsYou` kedua (dan tidak ada attention kedua) sampai layar pernah bersih dari dialog (`TestHookedPermissionStaleScreen`, `TestStalePermissionSingleAttention`). Ini mempersempit T15h sesuai API-agents §6.2 butir 3.
- **Dialog trust Claude:** pola pendek tahan-lipat (`Yes, I trust this folder`, `Quick safety check`, `project you created or one you trust`, `No, exit`), label activity `Trust folder?`, dan ditandai *startup dialog* sehingga juga menaikkan `needsYou` dari `starting`/`ready` walau `SessionStart` sudah datang (T15h hanya dari `working` untuk dialog lain). Aturan Codex: `^\s*Update available` (banner kotak di layar ready tidak lagi dianggap needsYou), `Update now`, `Sign in with ChatGPT`; label `Update dialog`/`Login dialog`. `ScreenMatch.Startup`, `Pattern.Label/Startup` baru.
- `POST /api/sessions/{id}/restart` sudah menerima `agentSessionId` (route → `session.RestartRequest`, API-agents §3.3, `agents_api_test.go`).
- Data race `os.File.Fd()` vs `Close` pada PTY lokal (resize/TIOCGPGRP saat proses selesai) diperbaiki dengan kunci di `localProc`.

**Penyimpangan / tambahan atas kontrak (implementasi):**

- **Delivery:** `waitReason` tambahan `"dialog"`; setting baru `delivery.promptTimeoutSec` (120, 10–3600) → `failed prompt_unrecognized`. Batas per alasan memberi `expired` dengan `code` `starting_timeout`/`needs_you_timeout`/`working_timeout`/`reconnect_timeout`/`human_typing` (humanTyping ≤ 10 dtk lalu menyerah); deadline lewat → `expired deadline`; deadline default 1 jam (`deadlineSec` 5–86400). `submit_aborted` bila kondisi berubah setelah paste (teks tetap di input). `manualAgent` selalu `not_agent` (belum ada mekanisme izin). Konfirmasi juga dianggap terjadi bila giliran baru dimulai setelah Return (`lastTurn.startedAt`), karena giliran pendek bisa selesai sebelum langkah engine berikut. `Delivery.projectId` (filter `GET /api/deliveries`). Delivery berikutnya menunggu sampai yang aktif `confirmed` atau jendela 8 dtk lewat. Cek proses sebelum mengetik = marker `agent-start` + foreground pgrp wrapper (lokal TIOCGPGRP, SSH `ps`); remote tanpa `wrapperPid` → hanya marker. `source` dari API dibatasi `api|reply|skill|dictation`. `Session.queue` dan `Session.subagents` (Mission Map) ditambah ke Info.
- **Board:** error validasi membawa `code` dan status (`name_conflict` 409, `member_conflict`, `bad_edge`, `forward_cycle`, `bad_group`, `bad_orchestrator`, `bad_memory`, `too_many` 400); validasi hanya untuk node/edge yang disentuh batch (board lama dengan edge `wire` generik tidak ikut ditolak). Siklus forward memperlakukan grup + anggotanya sebagai satu simpul.
- **Command:** `restart <who>` dari command = restart dengan `resume:true` (percakapan dipertahankan). `delete <group>` = `kill` anggota + `group.dissolve` (konfirmasi destructive). Fuzzy `didYouMean`/`sendToAll` hanya untuk handle. Teks yang memerlukan penyusunan instruksi (`write a brief…`, `split the work…`) tidak dikirim tanpa planner. Loop dari order: `maxRuns` 20, target `allAgents` (bar) atau `orchestrator` (kartu). `ask summary` memakai runner `thread-summary` bila planner tersedia. Error 404 `project_not_found`/`workspace_not_found`, 400 `bad_card`, 400 `bad_thread`.
- **PlanSchema:** target berupa handle/nama grup; field `url` (web), `everySec` (loop), `group`, `source`, `sources`. Step planner `loop.create` tidak pernah membawa goal `command`. Planner dianggap tidak login selama 1 menit setelah `cli_not_logged_in` (`plannerLoginWait`).
- **Forward:** satu kali dengan sumber = target → `400 forward_cycle`; `forwardDepth` dilacak per giliran console (dari delivery yang memulai giliran; giliran manual = 0) dan direset setelah giliran selesai; `split` tanpa planner = seluruh digest ke target pertama. Forward dari edge dicatat sebagai order `source:"forward"` di thread project.
- **Memory:** `push` menghitung `busy` = agen sedang bekerja (delivery diantre), `skipped` = paused/needsYou/exited; tanpa `targets`/node → semua console workspace. `memory.autoPush` (ekstensi) aktif saat console pertama kali `ready`.
- **Layout:** kartu kalender disimpan sebagai note berjudul (isi kalender dibuang); nama grup saat deploy diberi sufiks bila bentrok; `fill` = `{"agent"}`.
- **Handoff:** machine ringkasan = machine project bila `claude`/`codex` terdeteksi di probe (bukan cek login), selain itu This PC; console baru ditaruh di kanan sumber dan masuk grup sumber (bingkai dihitung ulang).
- **Pause remote:** `kill -s SIG -- "-$t"` (lihat #5); `ps` tanpa `tpgid`/`sid` (BusyBox) → `409 pid_unknown`; validasi gagal → `409 signal_refused`.

**Efek samping di This PC:** tidak ada perubahan config. Run console interaktif menambah 2 baris (`Reply with the single word OK…`, `/exit`) ke `~/.claude/history.jsonl` milik Claude Code (riwayat input normal; tidak disunting karena proses Claude lain menulis file itu bersamaan); transcript sesi uji dihapus lagi; planner memakai `--no-session-persistence`. Dialog trust dibatalkan sehingga tidak ada entri trust baru.

**Belum dikerjakan / tetap terbuka:** daftar CLI aman untuk `whileWorking:"type"` (#4); Codex interaktif tidak diuji ulang (flag update check di atas belum dipasang otomatis); BusyBox `ps` (#5) belum diuji nyata.

