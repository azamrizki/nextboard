# NextBoard API — Fase 4: project, workspace, board v2, file & aset

Tambahan atas [API.md](API.md); konvensi (auth, CSRF `X-NextBoard: 1`, error `{"error"}`, camelCase, RFC 3339) tetap sama. Fitur & UX: [ROADMAP.md](ROADMAP.md) Fase 4.

Endpoint lama `GET/PUT /api/board` **dihapus** (diganti board per workspace). Event lama `{"type":"board"}` juga dihapus.

## 1. Project & workspace

```jsonc
// Project = folder (biasanya repo git) di sebuah machine
{
  "id": "p_ab12cd34ef", "name": "nextboard",          // name: tampilan, maks 24 karakter
  "machineId": "local", "path": "/home/alice/apps/nextboard", // path absolut di machine itu
  "createdAt": "…", "lastOpenedAt": "…",
  "workspaces": [Workspace…],                          // urut sesuai tab
  "sessionCount": 3, "runningCount": 2,
  "groupId": "g_…", "tags": ["backend"],               // keduanya tidak ada bila kosong; tanpa group = All projects
  "parts": [ProjectPart…]                              // folder yang dicakup project; yang pertama = machineId + path
}
// ProjectPart = satu folder project di satu machine, dengan nama perannya (ROADMAP #49)
{ "id": "main", "name": "Main", "machineId": "local", "path": "/home/alice/apps/shop-api", "readOnly": true }
// id part pertama "main" untuk project lama dan baru, part tambahan "pt_…"; readOnly tidak ada bila false
// Workspace = satu tab board di dalam project
{ "id": "w_9x8y7z6w5v", "projectId": "p_…", "name": "Main", "createdAt": "…", "sessionCount": 2 }
// GitInfo (read-only, untuk home screen)
{ "isRepo": true, "branch": "main", "ahead": 0, "behind": 2, "changed": 5, "error": "" }
```

| Method | Path | Request | Response |
|---|---|---|---|
| GET | `/api/projects` | — | `Project[]` (urut `lastOpenedAt` desc) |
| POST | `/api/projects` | `{"machineId","path","name"?}` | `201 Project` dengan satu workspace "Main". Path yang sama di machine yang sama, juga bila folder itu part project lain → `200` project yang sudah ada. `400` path bukan folder. |
| PATCH | `/api/projects/{id}` | `{"name"?, "opened"?: true, "groupId"?, "tags"?, "sandbox"?, "partAccess"?}` (`partAccess`: `""` atau `"full"`, selain itu `400 bad_part_access`) | `Project` (`opened` memperbarui `lastOpenedAt`; `groupId: ""` mengeluarkan dari group; satu simpan, ditolak utuh: `404 group_not_found`, `400 bad_tag`) |
| DELETE | `/api/projects/{id}` | — | `{"ok":true}`; semua sesinya dihentikan & dihapus, board diarsipkan, aplikasi desktop yang hanya tampil di board itu ditutup. File di disk tidak disentuh. |
| POST | `/api/projects/{id}/parts` | `{"name","machineId","path"}` | `201 Project` dengan part baru di akhir. Folder dicek seperti `POST /api/projects`. `409 part_overlap` (folder sama, di dalam, atau berisi part lain di machine yang sama), `409 name_conflict`, `409 path_in_project` dengan `projectId` (folder itu sudah project lain atau part-nya), `409 too_many_parts` (maks 8), `400` nama atau folder |
| PATCH | `/api/projects/{id}/parts/{part}` | `{"name"?, "readOnly"?, "index"?}` | `Project`. `index` memindah part; part di posisi 0 menjadi `machineId`/`path` project. `404 part_not_found`, `409 name_conflict` |
| DELETE | `/api/projects/{id}/parts/{part}` | — | `Project`. Folder tidak disentuh. `409 main_part` untuk part pertama, `409 part_busy` dengan `sessions` selama masih ada terminal di part itu |
| POST | `/api/projects/{id}/parts/{part}/split` | — | `201 {"project": Project, "from": Project}`: part itu menjadi project sendiri dengan satu board kosong "Main", group dan tag sama, review dan Local History ikut. `409 main_part`, `409 part_busy` (`sessions`) |
| GET | `/api/projects/{id}/merge?from=<project>` | — | Pratinjau: `{"from","parts","workspaces","sessions","running","monitors","loops","memoryChars","subMemories","criticalChars","criticalFits","publicPage","problem"?,"code"?}`. Merge yang tidak bisa tetap `200` dengan `problem` dan `code` (`too_many_parts`, `part_overlap`, `merge_self`) |
| POST | `/api/projects/{id}/merge` | `{"from","critical"?: "append"\|"drop"}` | `Project`. Lihat "Merge" di bawah. `404 project_not_found`, `400 merge_self`, `409 too_many_parts`, `409 part_overlap`, `409 critical_too_big` |
| GET | `/api/projects/{id}/git` | — | `GitInfo` (git read-only via exec: `rev-parse`, `status --porcelain -b`; timeout 5 dtk; bukan repo → `isRepo:false`) |
| GET | `/api/projects/{id}/close-all` | — | Yang akan berakhir bila semua board ditutup: `{"boards","tiles","sessions","running","apps","loops"}` (`tiles` tanpa terminal; `apps` hanya aplikasi yang tidak tampil di board lain; `loops` yang sedang berjalan). |
| POST | `/api/projects/{id}/close-all` | — | `{"workspace": Workspace, "closed","archived","sessions","apps","loops"}`. Semua workspace diganti satu workspace baru kosong "Main" (id baru): loop project dihentikan (definisinya tetap ada), sesi dihentikan & dihapus, aplikasi desktop yang hanya tampil di board itu ditutup, board diarsipkan (board tanpa tile selain terminal tidak masuk arsip). File di disk tidak disentuh. Di luar kewenangan planner, seperti `workspace.close`. |

**Group dan tag project.** Group adalah tree nama di atas project, tag adalah kata pada project. Keduanya hanya menyusun daftar: id, folder, board, dan terminal project tidak ikut berubah.

```jsonc
// ProjectGroup (project-groups.json); Project mendapat "groupId"? dan "tags"?
{ "id": "g_…", "name": "Globex", "parentId": "g_…", "createdAt": "…" }   // tanpa parentId = tingkat atas
```

| Method | Path | Request | Response |
|---|---|---|---|
| GET | `/api/project-groups` | — | `ProjectGroup[]` (urut nama) |
| POST | `/api/project-groups` | `{"name","parentId"?}` | `201 ProjectGroup`. `404 group_not_found`, `409 name_conflict`, `400 group_too_deep` |
| PATCH | `/api/project-groups/{id}` | `{"name"?, "parentId"?}` | `ProjectGroup`; `parentId` memindah group beserta isinya, `""` ke tingkat atas. `400 group_cycle` bila ke dalam dirinya sendiri, `400 group_too_deep`, `409 name_conflict` |
| DELETE | `/api/project-groups/{id}` | — | `{"ok":true}`. Hanya group-nya yang hilang: sub-group dan project di dalamnya naik satu tingkat, event `project` terbit untuk tiap project yang pindah |

- **Batas:** nama group ≤ 24 karakter, unik per tingkat tanpa membedakan huruf besar. Kedalaman 5 tingkat; pemindahan menghitung isi yang ikut pindah. Tag ≤ 24 karakter tanpa koma, ≤ 12 per project, ulangan dibuang (ejaan pertama yang dipakai).
- **Hapus group boleh menghasilkan dua nama sama** pada satu tingkat, karena semua berjalan lewat id dan menolak berarti group itu tidak bisa dihapus.
- **Berkas rusak:** saat start, group yang kehilangan induk atau membentuk lingkaran pindah ke tingkat atas, dan project dengan group yang tidak ada menjadi tanpa group.
- **Urutan di menu project:** baris "Sort groups" dan "Sort projects" di menu masing-masing memilih `Name` (A–Z, angka urut sebagai angka) atau `Recent`. Group `Recent` diurutkan menurut project yang terakhir dibuka di dalamnya atau di sub-group-nya; group kosong ditaruh paling bawah. Default: group A–Z, project yang terakhir dibuka di atas. Pilihan disimpan per browser (`nb.projects.menu.groups`, `nb.projects.menu.projects`), seperti group yang dilipat. Home tetap memakai kontrol Sort-nya sendiri.
- **Migrasi prefix:** hub yang belum punya `project-groups.json` memindah prefix nama menjadi group sekali saat start: `Acme - Portal` menjadi `Portal` di group `Acme`. Hanya pemisah ` - ` yang dihitung, jadi `demo-backoffice` tetap satu nama, dan `A - B - C` menjadi `B - C` di group `A`. Hub baru langsung menulis file kosong, jadi nama yang dibuat kemudian tidak pernah dipecah.
| POST | `/api/projects/{id}/workspaces` | `{"name"?}` | `201 Workspace` (default "Workspace N") |
| PATCH | `/api/workspaces/{id}` | `{"name"?, "index"?}` | `Workspace` (`index` = posisi tab baru) |
| DELETE | `/api/workspaces/{id}` | — | `{"ok":true}`; sesi di workspace dihentikan & dihapus, board diarsipkan, aplikasi desktop yang hanya tampil di board itu ditutup. Workspace terakhir sebuah project tidak bisa dihapus (`409`). |

**Part project (ROADMAP #49).** Satu project bisa mencakup beberapa folder, di satu machine atau lebih: backend di satu server, frontend di server lain. Memory, lead, board, papan tugas, uptime, dan credentials tetap satu per project. Yang per part: terminal dan console (`partId`, folder bawaan per machine), file, git, dan Code workbench. `projects.json` lama mendapat part `main` saat dibuka, jadi klien yang belum tahu part tetap membaca `machineId`/`path`.

- **Project ref.** Route `/api/projects/{id}/…` milik workbench (file, search, LSP, DAP, Laravel, Markdown, eslint, git, tasks, review) menerima `{id}` berbentuk `<project>~<part>` atau `?part=<part>`. Project itu lalu dilihat lewat part tersebut: machine dan folder-nya. Ref tanpa part = part pertama. `404 part_not_found` untuk part yang tidak ada. `projectId` di `fs/write` dan `fs/trash` boleh berupa ref, dan tanpa ref folder dicari dari part di machine itu yang memuat file-nya.
- **Review dan Local History per part.** Tiap part punya review sendiri, dari foldernya (shadow repo di This PC, salinan untuk part di server). Kuncinya `<project>~<part>`, sama dengan project ref; part pertama tetap memakai id project. Perubahan agent masuk ke review part tempat ia mengubahnya, termasuk lewat tool `part`, dan event `review` membawa kunci itu sebagai `projectId`. Menghapus part atau project melupakan review-nya.
- **Git per part.** State git disimpan per part. Part selain yang pertama menyiarkan `{"type":"gitPart","projectId","part","repo","status"}`, bukan `git`/`gitRepo`, supaya klien lama tidak menganggapnya status part pertama.
- **Workbench.** Card editor menampilkan satu part, dipilih di judulnya dan diingat per browser (`nb.wb.part.<project>`). Tiap part punya tab dan setting sendiri. File yang dibuka di folder part lain (oleh lead atau card file) memindah card ke part itu.
- **Merge.** Project `from` menjadi part project ini: tiap folder-nya menjadi part (bernama seperti project itu, atau `<project> · <part>` bila ia punya beberapa, dengan angka bila nama sudah dipakai), workspace-nya menjadi tab di sini (namanya diberi awalan bila bentrok, id tetap), terminal dan console pindah tanpa restart ke part barunya, monitor uptime pindah dengan riwayatnya tapi turun dari halaman publik (halaman publik `from` mati), loop ikut, memory dan catatan agent-nya menjadi sub-memory bernama dari nama project (`shop-web`), sub-memory-nya menjadi `<nama>-<slug>`, review dan Local History tiap folder pindah ke kunci part barunya, tag digabung, dan thread project mendapat satu catatan. Critical notes `from` hanya ditambahkan dengan `"critical": "append"`, di bawah judul `## From <nama>`; tanpa itu tidak dibawa. Credentials yang dibatasi ke `from` tidak berubah, karena batasnya ada di blob terenkripsi: owner membukanya di Credentials. Id `from` menjadi `aliases` project ini, sehingga `/p/<from>/w/<workspace>` membuka board yang sama di sini. Thread project `from` tetap di disk. Folder di disk tidak disentuh.
- **Perintah terblokir per part.** `PATCH …/parts/{part}` `{"blocked": ["git push", …]}` (maks 50, masing-masing ≤ 200 karakter, satu baris; `400 bad_blocked`). Di part itu berlaku di atas daftar hub dan project: saat console di part itu diluncurkan, dan saat `bash` hub dijalankan di part itu, juga dari agent di part lain.
- **Agent.** Lihat API-agents §21.1: part sendiri penuh, part lain lewat tool hub, `partAccess` project (`""` hanya baca, `"full"`), part read-only hanya baca untuk semua.

**Migrasi saat start:** kalau `projects.json` belum ada, hub membuat project "Home" (`local`, home dir) dengan workspace "Main", memindahkan semua sesi lama ke sana, dan mengonversi `board.json` lama (posisi per sesi) menjadi node `terminal`. `board.json` lama diganti nama `board.json.migrated`.

**Sesi:** `Session` mendapat field `projectId`, `workspaceId`, dan `partId`. `POST /api/sessions` menerima:

```jsonc
{ "workspaceId": "w_…", "partId"?: "pt_…", "machineId"?: "…", "preset": "claude", "name"?: "…", "cwd"?: "…", "cols"?: 120, "rows"?: 32,
  "node"?: { "x": 0, "y": 0, "w": 720, "h": 440 } }
```

- Dengan `partId`: `machineId` default = machine part itu (machine lain → `400 part_machine`), `cwd` default = folder part. `404 part_not_found`.
- Tanpa `partId`: `machineId` default = machine project; `cwd` kosong = folder part pertama di machine itu (di machine tanpa part: home); `cwd` di dalam folder part mencatat `partId` part itu.
- Agent di part read-only bekerja seperti di server: CLI di This PC dengan tool lokal mati, dan hanya `read_file`, `grep`, `glob` hub yang bekerja di part itu (`remoteMachineId` = machine part, juga `local`). Begitu juga saat shell di part itu diganti menjadi agent, atau agent di part yang baru dibuat read-only di-restart. Agent yang CLI-nya tidak bisa bekerja lewat tool hub → `422 part_read_only`. Shell biasa tetap boleh.
- Tanpa `workspaceId` (klien lama/Android): pakai workspace pertama project yang terakhir dibuka; kalau tidak ada project sama sekali, buat "Home".
- Hub menambahkan node `terminal` ke board workspace itu (posisi `node` atau otomatis di kanan node terakhir) dalam satu operasi, lalu menyiarkan `boardOps`.
- `DELETE /api/sessions/{id}` juga menghapus node terminalnya (siaran `boardOps`).

## 2. Board v2

```jsonc
// Board
{
  "workspaceId": "w_…", "schemaVersion": 2, "rev": 42,
  "nodes": [Node…],   // urutan array = urutan z (terakhir = paling atas); web menaikkan kartu yang terakhir disentuh hanya di tampilannya (`zIndex`), tanpa op
  "edges": [Edge…]
}
// Node
{
  "id": "n_k3j2h1q9z8",            // dibuat klien (prefix n_ + 10 char) atau hub untuk terminal
  "kind": "terminal",               // lihat tabel kind
  "x": 0, "y": 0, "w": 720, "h": 440,  // koordinat board (px), w/h > 0
  "rotation": 0,                    // derajat, opsional
  "locked": false,                  // opsional
  "data": { … }                     // isi per kind; hub hanya memvalidasi untuk kind "terminal"
}
// Edge (penghubung antar-node; arrow bebas adalah node kind "line")
{ "id": "e_…", "source": "n_…", "target": "n_…", "kind": "wire", "data": {} }
```

Viewport **tidak** disimpan di board (per klien, di localStorage).

### Kind dan `data`

| kind | `data` |
|---|---|
| `terminal` | `{ "sessionId": "s_…" }` — hub yang membuat/menghapus; klien tidak boleh `put` node terminal untuk sesi yang tidak ada (`400`) |
| `note` | `{ "text": "…", "color": "yellow"\|"pink"\|"blue"\|"green"\|"violet"\|"orange" }` |
| `text` | `{ "text": "…", "font": "sans"\|"serif"\|"mono"\|"hand", "size": "s"\|"m"\|"l"\|"xl", "align": "left"\|"center"\|"right", "color": "#hex" }` |
| `checklist` | `{ "title": "…", "autoRun"?: true, "items": [{ "id", "text", "done": bool, …field tugas }] }` |
| `kanban` | `{ "title": "…", "autoRun"?: true, "columns": [{ "id", "title", "color"?: "amber"\|"sage"\|"sky"\|"violet"\|"rose"\|"teal", "cards": [{ "id", "title", "notes"?, "due"?: "YYYY-MM-DD", "priority"?: "low"\|"med"\|"high", "done"?: bool, …field tugas }] }] }` (default 4 kolom: Backlog, To do, In progress, Done). Field tugas ditulis hub, lihat API-orchestration.md §3.10: `assignee`, `sessionId`, `orderId`, `state`, `error` |
| `calendar` | `{ "title": "…", "month": "YYYY-MM", "events": [{ "id", "title", "date": "YYYY-MM-DD", "time"?: "HH:MM", "notes"?, "color"? }] }` |
| `uptime` | `{ "title": "…" }`; monitornya milik project, lihat [API-uptime.md](API-uptime.md) |
| `credentials` | `{ "title": "…" }`; isinya di vault, tidak pernah di board, lihat [API-vault.md](API-vault.md) |
| `shape` | `{ "shape": "rect"\|"ellipse"\|"diamond", "stroke": "#hex", "strokeWidth": 1\|2\|4, "strokeStyle": "solid"\|"dashed"\|"dotted", "roughness": 0\|1\|2, "fill": "#hex"\|"transparent", "fillStyle": "hachure"\|"cross-hatch"\|"solid", "edges": "sharp"\|"round", "opacity": 10..100, "text"?: "…", "font"?, "size"?, "align"?, "seed": int }` |
| `line` | `{ "points": [[x,y],[x,y],[x,y]], "arrowStart": bool, "arrowEnd": bool, "stroke", "strokeWidth", "strokeStyle", "roughness", "opacity", "seed" }` — titik relatif ke (x,y) node; titik tengah = kontrol lengkung. Opsional untuk panah gaya wiring ([API-studio.md](API-studio.md) §6): `"route": [[x,y]…]` (≥ 2 titik, bagian lurus horizontal/vertikal, menggantikan lengkung; `points` tetap garis lurus antar ujungnya untuk klien yang belum mengenalnya) dan `"dots": [[x,y]…]` (titik percabangan) |
| `ink` | `{ "points": [[x,y,pressure]…], "color": "#hex", "width": 1..16 }` — relatif ke (x,y) node |
| `image` | `{ "src": { "asset": "a_…" } \| { "machineId", "path" }, "alt"?: "…", "naturalW", "naturalH" }` |
| `file` | `{ "machineId", "path", "view": "code"\|"markdown"\|"file"\|"folder", "snapshot"?: { "content", "size", "mtime", "truncated" } }` — `code` = snapshot (tidak ikut berubah); `markdown` = live (klien polling `fs/stat`); `folder` = daftar ≤ 40 file |
| `app` | `{ "appId", "name", "sessionId" }` — aplikasi desktop This PC, lihat [API-gui.md](API-gui.md) |

Klien mengabaikan kind yang tidak dikenalnya (render placeholder "Unsupported tile"), jadi kind baru bisa ditambah di fase berikutnya tanpa memecah klien lama.

### Endpoint

| Method | Path | Request | Response |
|---|---|---|---|
| GET | `/api/workspaces/{id}/board` | — | `Board` |
| POST | `/api/workspaces/{id}/board/ops` | `{"clientId":"c_…","baseRev":42,"ops":[Op…]}` (maks 2 MB, ≤ 500 op) | `{"rev":43}` |
| GET | `/api/workspaces/{id}/snapshots` | — | `[{ "id": "2026102814", "at": "…", "nodeCount": 12 }]` (terbaru dulu) |
| POST | `/api/workspaces/{id}/snapshots/{sid}/restore` | — | `Board` (versi sekarang disimpan dulu sebagai snapshot; node terminal yang sesinya sudah tidak ada dibuang) |
| GET | `/api/archive` | — | `[{ "id", "projectName", "workspaceName", "archivedAt", "nodeCount", "reason": "closed"\|"cleared"\|"unreadable" }]` |
| POST | `/api/archive/{id}/restore` | `{"projectId"}` | `201 Workspace` (board dipulihkan tanpa node terminal) |

```jsonc
// Op — diterapkan berurutan secara atomik; last-writer-wins per node
{ "op": "put",     "node": Node }                       // sisip atau ganti seluruh node; tetap di posisi z lama bila sudah ada
{ "op": "patch",   "id": "n_…", "set": { "x": 10, "y": 20, "w"?, "h"?, "rotation"?, "locked"?, "data"?: {…} } } // field top-level diganti; "data" di-merge dangkal (key per key)
{ "op": "del",     "id": "n_…" }                        // node terminal tidak bisa dihapus lewat op (pakai DELETE session) → op diabaikan
{ "op": "z",       "id": "n_…", "to": "front"|"back" }
{ "op": "putEdge", "edge": Edge }
{ "op": "delEdge", "id": "e_…" }
```

- `baseRev` hanya informasi (tidak ditolak bila basi); konvergensi lewat siaran op.
- Validasi: id unik `^[a-z]_[a-z0-9]{6,24}$`, angka finite, `w,h` 1…20000, |x|,|y| ≤ 20000, `data` objek JSON ≤ 256 KB per node, board total ≤ 8 MB, edge harus menunjuk node yang ada (edge yatim dihapus otomatis saat node dihapus).
- Penyimpanan: debounce 2 dtk setelah perubahan terakhir, maksimal tertunda 7 dtk; `boards/<wid>.json` + `boards/<wid>.json.prev`; snapshot pertama di tiap jam yang ada perubahan ke `snapshots/<wid>/<YYYYMMDDHH>.json` (simpan 24 terbaru); arsip ke `archive/<id>.json` (tidak pernah dipangkas).
- **Pemulihan (teruji dengan `kill -9` dan file rusak):** berhenti normal menyimpan semua perubahan. Hub yang mati mendadak kehilangan paling lama beberapa detik terakhir (2 dtk, sampai 7 dtk bila terus diedit). File board ditulis atomik; kalau tetap rusak, hub memakai `.prev` (kehilangan satu penyimpanan terakhir) dan mengarsipkan file rusak dengan alasan `unreadable`. Kalau `.prev` juga rusak, board kosong dan dipulihkan dari snapshot (paling lama satu jam ke belakang). Restore selalu menyimpan versi saat ini lebih dulu, dan salinan itu tidak pernah menimpa salinan lain, jadi restore bisa dibatalkan. Console hidup lagi menurut `resumeOnRestart`; Android dan desktop memakai jalur API yang sama.

### Auto arrange

Tombol *Auto arrange* di rail kanan, tombol pertama ring klik kanan, dan chip dengan nama sama di atas command bar menata ulang board di klien (`web/src/board/arrange.ts`), lewat `patch` biasa, jadi sinkron ke klien lain dan satu langkah undo.

- Ukuran kartu tidak berubah, dan hasilnya tidak tumpang tindih. Menjalankan lagi pada board yang sama tidak mengubah apa pun.
- Kartu yang terhubung edge berdiri berdampingan dalam kolom dari sumber ke target, dengan ruang untuk wire; satu level dengan lebih dari tiga kartu boleh melebar jadi beberapa kolom. Sisanya mengambil tempat kosong tertinggi lalu paling kiri, yang besar lebih dulu, jadi kartu sejenis membentuk baris dan ruang di samping kartu tinggi terisi. Lebar total mengikuti bentuk ruang kosong di layar, lalu tampilan di-fit.
- Komponen wire yang berisi kartu orchestrator ditata sebagai bintang: lead di tengah, yang terhubung dengannya mengelilinginya, dengan satu group sebagai satu blok. Jumlahnya menentukan bentuk: 1 di kanan; 2 di kiri dan kanan; 3 ditambah bawah; 4 atas, bawah, kiri, kanan; 5 sampai 8 mengisi cincin 3×3; 9 ke atas memakai cincin lebih lebar yang bentuknya mengikuti layar. Kanban dan checklist yang terhubung berada di baris sendiri di bawah bintang, berpusat pada lead. Bila satu komponen punya beberapa kartu orchestrator, yang koneksinya terbanyak jadi pusat.
- Grup bergerak utuh bersama anggotanya. Anggota baru disusun ulang bila saling menimpa; susunan yang rapi dibiarkan.
- Kartu terkunci (atau grup dengan anggota terkunci) tidak bergerak, dan yang lain menghindarinya.
- `ink`, `line`, `shape`, `text`: yang pusatnya berada di atas kartu ikut kartu itu, `shape` yang melingkupi kartu tetap melingkupinya, dan coretan lepas yang saling menyentuh tetap utuh dan ditaruh terakhir di ruang yang tersisa.

## 3. Event baru di `/api/events`

```jsonc
{ "type": "snapshot", …, "projects": [Project…] }       // field tambahan pada snapshot
{ "type": "project", "project": Project }               // dibuat / berubah (nama, workspace, jumlah sesi)
{ "type": "projectRemoved", "id": "p_…" }
{ "type": "projectGroups", "groups": [ProjectGroup…] }  // seluruh tree, setiap kali berubah; juga field `projectGroups` pada snapshot
{ "type": "boardOps", "workspaceId": "w_…", "rev": 43, "clientId": "c_…", "ops": [Op…] }
// clientId kosong = op dari hub (sesi dibuat/dihapus, restore)
```

Klien mengabaikan `boardOps` dengan `clientId` miliknya sendiri. Bila `rev` melompat (ada siaran terlewat, mis. setelah reconnect), klien memuat ulang board.

## 4. File machine (P10)

Semua path absolut; `~` dan `~/…` diperluas ke home machine. Lokal memakai filesystem langsung; SSH memakai **SFTP subsystem** di koneksi yang sudah ada (bawaan OpenSSH, tidak memasang apa pun), dengan fallback exec read-only (`stat`, `head -c`) bila SFTP dimatikan.

| Method | Path | Request | Response |
|---|---|---|---|
| GET | `/api/machines/{id}/fs/stat?path=` | — | `{ "path", "name", "dir": bool, "size", "mtime", "exists": bool }` (`exists:false` bila tidak ada, bukan 404) |
| GET | `/api/machines/{id}/fs/read?path=&max=` | `max` default 800000 | `{ "path", "size", "mtime", "binary": bool, "truncated": bool, "content": "…" }` (UTF-8; `binary:true` → content kosong) |
| GET | `/api/machines/{id}/fs/raw?path=` | — | byte mentah ≤ 20 MB, `Content-Type` dari ekstensi (gambar/pdf/teks), `Content-Security-Policy: sandbox`, `nosniff` |
| PUT | `/api/machines/{id}/fs/write` | `{ "path", "content", "encoding": "utf8"\|"base64", "ifMtime"?: "…", "unique"?: bool }` (maks 20 MB) | `Stat`; `409` bila `ifMtime` tidak cocok; `unique:true` → kalau nama sudah ada, hub memakai `name-2.ext`, `name-3.ext`, … dan mengembalikan path akhirnya |
| GET | `/api/machines/{id}/ls?path=` | (sudah ada) | ditambah `size` dan `mtime` per entri |

Tulis hanya terjadi atas aksi eksplisit user (simpan, salin file ke project) — sesuai aturan agentless kategori (a).

## 5. Aset (blob gambar di hub)

| Method | Path | Request | Response |
|---|---|---|---|
| POST | `/api/assets` | body mentah, `Content-Type: image/png\|image/jpeg\|image/gif\|image/webp` (maks 20 MB) | `201 { "id": "a_…", "url": "/api/assets/a_…", "type", "size", "w", "h" }` |
| GET | `/api/assets/{id}` | — | byte, `Cache-Control: private, max-age=31536000, immutable`, `nosniff`, `Content-Security-Policy: sandbox` |

SVG ditolak (risiko skrip). Disimpan di `<data>/assets/` (0600).

## 6. Catatan implementasi hub

Perilaku yang tidak dirinci di atas (hub fase 4):

- **patch:** `set` hanya menerima `x`, `y`, `w`, `h`, `rotation`, `locked`, `data` (field lain → `400`). Di `data`, nilai `null` **menghapus** key-nya.
- **Op ke node/edge yang sudah tidak ada** (`patch`, `del`, `z`, `delEdge`) dilewati tanpa error. Batch yang semuanya terlewati → `{"rev": <rev sekarang>}` tanpa siaran dan tanpa menaikkan `rev`.
- **Siaran `boardOps`** berisi op yang sudah dinormalisasi hub (node/edge lengkap dengan default, mis. `data: {}`, edge `kind: "wire"`). `del` node diikuti `delEdge` untuk edge yang ikut terhapus.
- **Node terminal:** `put` harus untuk sesi di workspace yang sama, satu node per sesi; node terminal tidak bisa diganti kind-nya dan `data.sessionId` tidak bisa diubah (`400`). `del` dari klien diabaikan.
- **Restore snapshot** disiarkan sebagai `delEdge` + `del` untuk semua isi lama, lalu `put` + `putEdge` untuk isi baru (urutan z ikut). Node terminal sesi yang masih hidup tapi tidak ada di snapshot tetap dipertahankan.
- **Snapshot per jam** berisi board *sebelum* penyimpanan pertama di jam itu (jadi selalu bisa kembali ke kondisi awal jam). Salinan otomatis sebelum restore memakai id `YYYYMMDDHHMMSS` (14 digit); id 10 digit = snapshot per jam. Keduanya ikut batas 24 terbaru. Jam dalam UTC.
- **Arsip:** id `r_…`; entri tetap ada setelah di-restore. Board tanpa node selain terminal tidak diarsipkan saat ditutup. `cleared` = satu batch op yang menghapus semua node non-terminal dari board yang punya ≥ 3 node.
- Board dengan `schemaVersion` > 2 (dari hub yang lebih baru) bisa dibaca, tapi op ditolak `409`.
- **File API — kode error:** `400` path relatif / path folder, `403` akses ditolak (termasuk folder data hub di This PC, mis. `secret.key`, untuk read/raw/write), `404` tidak ada (juga folder induk saat `fs/write`), `409` `ifMtime` tidak cocok, `413` > 20 MB, `501` machine SSH tanpa SFTP (tulis tidak didukung), `502` machine belum tersambung, `503 ssh_sessions_full` (lihat di bawah). `ifMtime` dibandingkan dengan presisi yang dikirim klien (detik/milidetik/nano). `size` folder = 0.
- **fs/raw:** ekstensi gambar (termasuk `.svg`, tetap dengan `sandbox`) dan `.pdf` memakai tipe aslinya; isi yang tampak teks → `text/plain; charset=utf-8` (HTML tidak pernah disajikan sebagai HTML); selain itu `application/octet-stream` + `Content-Disposition: attachment`.
- **ls:** `size`/`mtime` tidak ada (size 0, tanpa `mtime`) di fallback exec SSH tanpa SFTP.
- **Aset:** `Content-Type` request harus salah satu dari 4 tipe; tipe yang disimpan/dikembalikan adalah hasil sniff isi file.
- **POST /api/sessions:** `cwd` default = path project hanya bila machine sesi = machine project (selain itu home machine). Nama project/workspace > 24 karakter → `400`; nama default dipotong ke 24.
- Klien SFTP per koneksi ditutup setelah 2 menit tidak dipakai (channel-nya ikut batas `MaxSessions` sshd) dan dibuka lagi saat perlu.
- **Batas `MaxSessions`:** sshd menerima sejumlah channel per koneksi (OpenSSH: 10) dan menolak berikutnya dengan `connect failed ("open failed")`. Code studio yang baru dibuka mengirim banyak perintah sekaligus, jadi channel yang ditolak diminta lagi (backoff 20–250 ms) sampai ada yang bebas, paling lama 15 dtk atau sampai request berakhir. Itu berlaku untuk perintah, console, pipe, dan SFTP (`openSession`, `internal/machine/exec.go`). Bila tetap penuh: file API dan git menjawab `503 ssh_sessions_full`. Koneksinya sendiri tidak dianggap putus.
