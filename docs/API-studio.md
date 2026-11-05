# NextBoard API — Fase 7: runner, Git, preview web, studio, usage, suara

Tambahan atas [API.md](API.md), [API-board.md](API-board.md), [API-agents.md](API-agents.md) dan [API-orchestration.md](API-orchestration.md). Fitur: [ROADMAP.md](ROADMAP.md) Fase 7. Keputusan pemilik #2 dan #3 mengikat. Error memakai `{"error","code"}` seperti API-agents.

## 0. Ringkasan desain

- **Exec machine** internal (tanpa endpoint exec bebas) dengan timeout dan watchdog idle; dipakai git, runner, usage, goal loop.
- **Job** = model umum untuk pekerjaan panjang (runner, push/pull, publish, usage) dengan progres, batal, dan event.
- **Runner tersembunyi** memakai mode headless CLI (`claude -p`, `codex exec`) di machine project; tidak pernah yolo. **Inti runner (§1–§2) dibutuhkan sejak awal Fase 6.**
- **Preview proxy**: listener HTTP terpisah per preview (port sendiri; HTTPS diberikan nginx), auth token → cookie, header pemblokir frame dibuang, skrip bridge disuntikkan.
- **Studio** (Documents, Diagrams, Code, Markdown, Skills) dibangun di atas file API Fase 4 + runner.
- **Usage**: sesi interaktif tersembunyi di satu folder tetap per user machine `/tmp/nb-usage-<uid>` (dibuat baru dengan `mkdir`, dihapus setelah dipakai — keputusan lead, menggantikan `mktemp`), layar dibaca model VT (best-effort); token chart dari transcript.
- **Suara**: sepenuhnya di klien.

## 1. Exec machine dan Job

### 1.1 Exec (internal)

`Host.Exec(ctx, ExecSpec{Cwd, Argv | Script, Env, Stdin, Timeout, IdleTimeout, MaxOutput}) → ExecResult{ExitCode, Stdout, Stderr, TimedOut, IdleKilled}`.

- Lokal: `os/exec` dengan PATH login (seperti `GitStatus`), stdin `/dev/null` bila tidak ada `Stdin`. SSH: channel non-PTY, `loginWrap(script)`; argumen selalu lewat `machine.Quote`.
- Watchdog idle: tidak ada byte di stdout/stderr selama `IdleTimeout` → hentikan (lokal: SIGTERM ke pgrp, SIGKILL 3 dtk kemudian; SSH: `signal TERM` lalu tutup channel).
- Output dibatasi `MaxOutput` (default 4 MiB, sisanya dibuang dan ditandai).
- **Tidak ada** `POST /api/machines/{id}/exec` publik; setiap pemakai (git, runner, usage, loop goal `command`) membangun perintah dari template tetap.

### 1.2 Job

```jsonc
{ "id": "jb_…", "kind": "runner"|"git"|"publish"|"usage",
  "purpose": "document",   // runner: "document"|"diagram"|"memory-scan"|"memory-init"|"memory-refresh"|"loop-goal"|"planner"|"handoff-summary"|"commit-subject"|"thread-summary"|"code-*" (§7.4)
  "projectId": "p_…", "machineId": "local", "agent": "claude",
  "state": "queued"|"running"|"done"|"failed"|"cancelled",
  "progress": { "phase": "launching"|"thinking"|"writing"|"finishing"|"pushing"|"…", "text": "Writing…", "pct": null },
  "createdAt": "…", "startedAt": "…", "finishedAt": null, "elapsedMs": 41200,
  "error": "", "code": "",   // mis. "cli_missing", "cli_not_logged_in", "timeout", "idle_timeout", "diverged"
  "result": null }           // bentuk per purpose
```

| Method | Path | Response |
|---|---|---|
| GET | `/api/jobs?projectId=&active=1&limit=50` | `{"items":[Job…]}` |
| GET | `/api/jobs/{id}` | `Job` (dengan `result`) |
| POST | `/api/jobs/{id}/cancel` | `Job` (`cancelled`; proses dihentikan, hasil parsial disimpan bila purpose mendukung) |

Event `job` (dibatasi 2×/dtk per job) dan `jobOutput` `{jobId, seq, text}` untuk streaming teks. Antrean: ≤ 2 job runner per machine, ≤ 4 per hub, ≤ 1 job git mutasi per project.

## 2. Runner tersembunyi (E1)

Keputusan: built-in memakai **mode headless** (lebih andal daripada membaca TUI); agen katalog/custom tidak didukung untuk runner (`400 runner_unsupported`). Ini menyimpang dari Symvibe yang sengaja tidak memakai print mode.

| Agen | Perintah (prompt via stdin) |
|---|---|
| Claude | `claude -p --output-format stream-json --verbose\|json --permission-mode dontAsk --no-session-persistence --tools <Read,Glob,Grep \| ""> [--allowedTools Read,Glob,Grep] [--max-turns 1] [--json-schema '<schema>'] [--model M]` |
| Codex | `codex exec --json --sandbox read-only --skip-git-repo-check --ephemeral -C <cwd> [-m M] [--output-schema <file>] -` (file schema di `mktemp -d /tmp/nb-run.XXXXXX`, nama divalidasi `/tmp/nb-run.??????`, dihapus lewat trap EXIT) |

**Terverifikasi (This PC, 2026-10-28, Claude Code 2.1.312 / Codex 0.163.0, 1 run kecil per CLI):** `--permission-mode` di 2.1.312 punya pilihan `acceptEdits|auto|bypassPermissions|manual|dontAsk|plan` (`default` masih diterima parser) dan settings user bisa `defaultMode:"auto"`, jadi runner memakai **`dontAsk` + `--tools`** (tool lain tidak tersedia sama sekali) **+ `--allowedTools`** untuk purpose baca-saja, alih-alih `--permission-mode default`. `--max-turns` diterima (tersembunyi di `--help`). Run `commit-subject` Claude (`--model haiku`, `--tools "" --max-turns 1`): event `system/hook_started`, `system/hook_response`, `system/init`, `rate_limit_event`, `system/commands_changed`, `assistant`, `result/success` (`result`, `is_error`, `session_id`), exit 0, 3 dtk. Hook SessionStart **plugin milik user tetap jalan di `-p`** (konteks plugin bisa ikut masuk; hanya `--bare` yang mematikannya dan itu dilarang). Codex `exec --json`: `thread.started{thread_id}`, `turn.started`, `item.completed{item:{type:"agent_message",text}}`, `turn.completed{usage}`, exit 0; `thread_id` → `result.agentSessionId`. Codex tidak punya flag pembatas tool: purpose tanpa tool di Codex hanya dibatasi sandbox `read-only`. Belum diuji nyata: `--json-schema` bersama `--tools ""`, dan `codex exec --output-schema`.

- Purpose `planner` dan purpose lain tanpa streaming boleh memakai `--output-format json` (satu objek hasil) alih-alih `stream-json`.
- **Tidak pernah**: `--dangerously-*`, `yoloArgs`, `--settings` hooks NextBoard, `--bare` (diassert di `TestRunnerArgvNeverUnsafe`, juga `bypassPermissions`, `acceptEdits`, `danger-full-access`, `--approve-for-me`).
- Implementasi Fase 5: `internal/jobs` (Job + antrean + runner) dan `machine.Exec`; endpoint `GET /api/jobs`, `GET /api/jobs/{id}`, `POST /api/jobs/{id}/cancel` (`404 job_not_found`), event `job` (perubahan state langsung, progres ≤ 2×/dtk) dan `jobOutput`. Belum ada endpoint pembuat job (dibuat oleh purpose Fase 6/7 lewat `SubmitRunner`). Job `queued`/`running` saat hub mati/start → `failed` dengan `code:"hub_restart"`. Batas waktu purpose: planner 45 dtk, handoff-summary 4 mnt, thread-summary 30 dtk, lainnya 10 mnt / idle 200 dtk.
- Kebijakan tool per purpose: `document`, `diagram`, `memory-scan`, `memory-init`, `memory-refresh` → baca-saja (`Read,Glob,Grep`), tool tulis/eksekusi ditolak; `planner`, `loop-goal`, `handoff-summary`, `commit-subject`, `thread-summary` → tanpa tool (`--max-turns 1`).
- Machine: machine project (cwd = path project) untuk purpose berbasis repo; This PC untuk `planner` (cwd `<data>/runner/planner`). Handoff: lihat API-orchestration §9.
- Profil: `profileId` request → default profil `settings.agents.profiles` → login default.
- Progres dari stream: `system init` → `launching`; event `assistant` dengan tool/thinking → `thinking`; teks pertama → `writing` (+ `jobOutput`); `result` → `finishing`. Teks akhir = field `result` (Claude) / `agent_message` terakhir (Codex).
- Kegagalan dikenali: perintah tidak ada → `cli_missing`; pesan login/API key → `cli_not_logged_in`; JSON schema tidak lolos → satu kali ulang otomatis lalu `bad_output`.
- Batas default: 10 menit total, 200 dtk idle (purpose bisa menimpa, lihat §5).

## 3. Git (B20–B22, B21)

### 3.1 Model

```jsonc
// GitStatus
{ "isRepo": true, "branch": "main", "detached": false, "upstream": "origin/main", "hasRemote": true,
  "remoteUrl": "git@github.com:me/app.git", "webUrl": "https://github.com/me/app",
  "ahead": 1, "behind": 0, "fetchedAt": "…",                 // mtime .git/FETCH_HEAD; panel tidak pernah fetch sendiri
  "merging": false, "conflicts": 0,
  "files": [{ "path": "src/a.ts", "oldPath"?: "src/b.ts", "index": "M", "worktree": " ",
              "status": "modified"|"added"|"deleted"|"renamed"|"untracked"|"conflict"|"typechange",
              "added": 12, "deleted": 3, "binary": false }],
  "truncated": false,                                        // > 400 file
  "largeFiles": [{ "path": "data/dump.bin", "sizeBytes": 160000000 }] }   // > 100 MB, tidak ter-track
// Commit
{ "hash": "…", "short": "abc1234", "subject": "…", "author": "…", "at": "…", "pushed": true }
// Branch
{ "name": "main", "current": true, "upstream": "origin/main", "ahead": 0, "behind": 0, "lastCommitAt": "…" }
```

### 3.2 Endpoint (semua di `/api/projects/{id}/git`)

| Method | Path | Request | Response / error |
|---|---|---|---|
| GET | `/status` | — | `GitStatus` (`status --porcelain=v2 -b -z --untracked-files=all` + `diff --numstat -z HEAD`, `GIT_OPTIONAL_LOCKS=0`) |
| GET | `/diff?path=&staged=0` | — | `{"path","diff":"…","truncated"}` (≤ 512 KB; untracked = isi file sebagai tambahan) |
| GET | `/log?limit=100` | ≤ 100 | `{"items":[Commit…]}` (`pushed` dari `rev-list @{u}..HEAD`) |
| GET | `/log?path=&line=&limit=` | ≤ 100 | Riwayat satu file (`--follow`) atau, dengan `line`, satu baris (`-L`). File yang belum dikenal git → `items` kosong |
| GET | `/log?graph=1&all=1&skip=&limit=` | ≤ 100 per halaman | Graph riwayat: `Commit` plus `parents` dan `refs` (`[{name,kind}]`, kind `head`/`branch`/`remote`/`tag`), anak selalu sebelum induk (`--topo-order`). `all=1` mengambil semua branch, remote, dan tag; tanpanya hanya yang terjangkau HEAD. Stash tidak ikut. `pushed:false` = belum ada di remote mana pun |
| GET | `/commits/{ref}` | — | `{hash,short,parents,refs,subject,body,author,email,at,committer,committerEmail,committedAt,files:[{path,oldPath?,status,add,del,binary?}],truncated}`. Merge dibandingkan dengan induk pertama, commit pertama berisi semua file sebagai `added`, ≤ 2000 file. `400 bad_ref`, `404 commit_not_found` |
| GET | `/index?path=` | — | `{"path","exists","binary","text"}`: isi path di index |
| POST | `/stage` | `{"paths":[…]}` atau `{"paths":[satu],"text"}` | `GitStatus`. Dengan `text`, index menerima persis teks itu, cara hub menyimpan sebagian file; mode file lama dipertahankan |
| POST | `/unstage` | `{"paths":[…]}` | `GitStatus`; work tree tidak berubah |
| GET | `/stash` | — | `{"items":[{"ref":"stash@{0}","hash","message","at"}]}` |
| POST | `/stash` | `{"message"?,"untracked"?}` | `GitStatus` |
| POST | `/stash/{apply\|pop\|drop}` | `{"ref"}` | `GitStatus`; konflik bukan kegagalan, status yang menunjukkannya. `400 bad_ref` |
| POST | `/commit-op` | `{"op":"checkout"\|"cherry-pick"\|"revert"\|"merge"\|"reset","ref","mode"?}` | `GitStatus`; `mode` reset `soft`/`mixed`/`hard`. Konflik bukan kegagalan. Git yang menolak: `409 git_refused`. Sebelum `reset --hard` hub mengambil snapshot Local History (§7.11) |
| POST | `/tags` | `{"name","ref","message"?}` | `GitStatus`; dengan `message` tag-nya annotated. `400 bad_ref`, `409 exists` |
| POST | `/operation/{abort\|continue}` | — | `GitStatus`; `409 no_operation`, `409 git_refused` |
| POST | `/blame` | `{"path","text"?}` (≤ 8 MB, `text` = isi editor yang belum disimpan) | `{"commits":[{hash,author,email,at,summary}],"lines":[indeks commit per baris],"me"}`; baris belum di-commit punya hash nol; file baru → kosong |
| POST | `/commit` | `{"message","paths":[…]}`, `{"message","all":true}`, atau `{"message","staged":true}` (hanya isi index), `"allowLarge"?: false` | `{"commit":Commit,"status":GitStatus}`. `400 empty_message`, `409 conflicts`, `409 large_files` (+`paths`), `422 git_identity` (user.email/name belum diset), `409 git_busy` |
| POST | `/push` | `{"setUpstream"?: true}` | `202 Job` (`push --progress [-u origin <branch>]`) |
| POST | `/pull` | — | `202 Job` (`pull --ff-only --progress`); gagal divergen → job `code:"diverged"` (UI: "Branches diverged — merge in a console") |
| POST | `/fetch` | — | `202 Job` (hanya atas klik user) |
| POST | `/discard` | `{"paths":[…]}` | `{"trashedTo":".git/nextboard-trash/20261028-141500","status"}`. Tracked: salin versi worktree ke trash lalu `restore --source=HEAD --staged --worktree --`; untracked: pindahkan ke trash |
| GET | `/trash` | — | `{"items":[{"id":"20261028-141500","at","paths":[…]}]}` |
| POST | `/trash/restore` | `{"id","paths"?}` | `{"restored":[…]}`; `409 exists` (tidak menimpa) |
| POST | `/init` | `{"branch"?: "main"}` | `GitStatus` (`init -b main`; git lama: `init` + `symbolic-ref HEAD refs/heads/main`) |
| POST | `/untrack-large` | `{"paths":[…]}` | `GitStatus` (`rm --cached --` + tambah baris ke `.gitignore`) |
| GET | `/branches` | — | `{"current","items":[Branch…]}` (≤ 200) |
| POST | `/branches` | `{"name","from"?,"switch"?: true}` | `GitStatus`; `400 bad_ref` (`check-ref-format --branch`), `409 exists` |
| POST | `/switch` | `{"name"}` | `GitStatus`; tanpa konfirmasi; git menolak bila menimpa pekerjaan → `409 git_refused` + pesan git |
| DELETE | `/branches/{name}` | — | `{"ok":true}` (`branch -d`); `409 not_merged` |
| GET | `/suggest?ai=0` | — | `{"subject","source":"heuristic"}`; `ai=1` (`&staged=1`) → `202 Job` `commit-subject` |

- Commit: `add -A -- <paths>` lalu `commit -m <msg> -- <paths>`; hook commit milik user tetap berjalan (tanpa `--no-verify`). Pada detached HEAD tombol berbunyi "Commit". Push tanpa upstream = "Publish branch".
- **Env git:** `GIT_TERMINAL_PROMPT=0`, `GIT_SSH_COMMAND="ssh -o BatchMode=yes"` (kecuali user sudah mengatur `core.sshCommand`/`GIT_SSH_COMMAND`), `SSH_ASKPASS_REQUIRE=never`, `GCM_INTERACTIVE=never`, `GIT_EDITOR=true`, `GIT_PAGER=cat`, `LC_ALL=C`, stdin `/dev/null`.
- **Timeout:** baca 10 dtk; commit 120 dtk; push/pull/fetch/publish 15 menit total dan **5 menit tanpa output** (watchdog §1.1; `--progress` memastikan ada output).
- Saran subjek heuristik (≤ 72 karakter): satu file → `Add|Update|Remove <nama>`; satu folder → `Update <folder>: N files`; hanya `.md` → `Docs: …`; hanya tes → `Tests: …`; selain itu `Update N files in <folder teratas>`.
- Semua path divalidasi relatif terhadap root repo dan diberikan setelah `--`.

### 3.3 GitHub lewat `gh` (B21)

| Method | Path | Request | Response |
|---|---|---|---|
| GET | `/api/projects/{id}/github` | — | `{"ghInstalled","authenticated","user","remoteUrl","repoUrl","installHint"}` (`gh auth status --hostname github.com`; hint per OS dari probe: apt / dnf / brew / tautan cli.github.com) |
| POST | `/api/projects/{id}/github/login` | — | `201 Session` — console biasa di machine project yang menjalankan `gh auth login`; hub memeriksa `gh auth status` tiap 5 dtk selama sesi hidup lalu menyiarkan `github` |
| POST | `/api/projects/{id}/github/publish` | `{"name","visibility":"private"\|"public","commitAll":true,"message"?}` | `202 Job` `publish`: commit semua (bila diminta) → `gh repo create <name> --<visibility> --source . --remote origin --push` |

Token tetap di keyring `gh` di machine itu; hub tidak membacanya.

**Pull request** lewat `gh` yang sama, tampil di view Source Control dan tab sendiri:

| Method | Path | Request | Response / error |
|---|---|---|---|
| GET | `/git/pulls` | — | `{"items":[…]}` (yang terbuka, ≤ 50) |
| POST | `/git/pulls` | `{"title","body","base"?,"draft"?}` | Push `origin HEAD`, lalu `201 {"url"}` |
| GET | `/git/pulls/{n}` | — | Detail, cek, file, komentar, review |
| GET | `/git/pulls/{n}/diff` | — | `{"diff"}` (≤ 4 MB) |
| POST | `/git/pulls/{n}/checkout` | — | `GitStatus` |
| POST | `/git/pulls/{n}/review` | `{"event":"approve"\|"comment"\|"request-changes","body"}` | `{"ok":true}` |
| POST | `/git/pulls/{n}/merge` | `{"method":"merge"\|"squash"\|"rebase","deleteBranch"}` | `GitStatus` |

Error: `409 gh_missing`, `409 gh_auth`, `409 no_github_remote`, `502 gh_failed`. Bagian ini tidak tampil kalau `gh` tidak ada atau remote-nya bukan GitHub. Merge meminta konfirmasi.

### 3.4 Command bar dan kartu branch

Step `git` dari API-orchestration §3.4: `commitPush` = commit file yang dicentang terakhir di panel (atau `all` bila tidak ada state panel) lalu push; `commit` dengan pesan; `push`; `pull`. Diketik user → langsung, tanpa konfirmasi; dari planner → konfirmasi `risky`.

Node baru `branches` `{ "projectId": "p_…" }`: branch sekarang, ahead/behind, jumlah file berubah, ≤ 200 branch lokal; klik = switch, "New branch", hapus = `branch -d`, "Panel" membuka panel Git.

### 3.5 Implementasi hub (Fase 7 bagian A) — deviasi dan verifikasi

Kode: `internal/gitsvc` (layanan), `internal/server/h_git.go` (endpoint, adapter command bar), `internal/orch/git.go` (`Service.Git`). Job push/pull/fetch/publish memakai `jobs.SubmitFunc` (kind `git`/`publish`, purpose `push`|`pull`|`fetch`|`publish`), memegang kunci repository selama job berjalan (`409 git_busy` untuk mutasi lain di repository itu).

- **Shell:** semua skrip git adalah POSIX sh dan selalu dijalankan lewat `exec /bin/sh -c …` (di SSH, `Exec` memakai login shell user; zsh membaca `"$b:r"` sebagai modifier history — ditemukan oleh uji sshd). PATH login tetap terwarisi.
- **Env tambahan** di atas §3.2: `unset GIT_ASKPASS SSH_ASKPASS` (askpass = prompt; mis. askpass VS Code), `GIT_LITERAL_PATHSPECS=1` (glob/magic `:(…)` di path selalu literal), `GIT_SEQUENCE_EDITOR=true`, `GIT_MERGE_AUTOEDIT=no`, `GH_PROMPT_DISABLED=1`, `GH_NO_UPDATE_NOTIFIER=1`, `NO_COLOR=1`. `GIT_SSH_COMMAND` BatchMode hanya bila user tidak mengatur `GIT_SSH_COMMAND`/`GIT_SSH`/`core.sshCommand`.
- **Config repo:** skrip baca (`prelude(…, readOnly)`, juga status di home screen) tidak menjalankan apa pun dari config repo: `core.fsmonitor=false`, `log.showSignature=false`, dan filter driver dari scope `local`/`worktree` dimatikan lewat `GIT_CONFIG_*` (butuh git 2.31; git lebih lama hanya mendapat dua yang pertama). Filter dari config global (git-lfs) tetap jalan; file yang hanya ditangani filter milik repo bisa terbaca berubah. Aksi tulis (stage, commit, pull) tetap menjalankan hook dan filter.
- **Root repo:** semua operasi berjalan di `git rev-parse --show-toplevel` (project boleh subfolder repo; folder induk berisi banyak repo: §3.6); semua path relatif ke root repo. Path ditolak (`400 bad_path`) bila absolut, keluar repo (`..`), mengandung kontrol/newline, atau menyentuh `.git`.
- **GitStatus** tambahan: `root`, `hasCommits`, `remoteName`, `changed` (jumlah total, `files` dibatasi 400 dan diurut per path), `largeFiles[].tracked`. Remote = remote upstream, lalu `origin`, lalu remote pertama; `remoteUrl` tanpa kredensial (`https://user:token@` dibuang); `webUrl` dari URL https/ssh/scp-like (path lokal → kosong). Jumlah baris file untracked hanya untuk 50 file pertama ≤ 1 MiB (`git diff --no-index --numstat`). `largeFiles` = path yang berubah (untracked atau ditambah/diubah) > 100 MiB (104 857 600 B), dari `wc -c` (fstat), tanpa membaca isi.
- **Diff:** `staged=1` → `diff --cached <HEAD|empty tree>`; selain itu terhadap HEAD (repo tanpa commit: empty tree); `--no-ext-diff --no-textconv`; field tambahan `binary`.
- **Commit:** path harus ada di daftar perubahan saat ini (path lain diabaikan; tidak ada yang tersisa → `409 nothing_to_commit`); entri rename membawa `oldPath`. Saat merge tanpa konflik, path dipilih di-stage lalu merge diselesaikan dengan seluruh index (git menolak commit parsial saat merge). Identitas dicek sebelum commit (`user.email`/`user.name` atau `GIT_AUTHOR_*`/`EMAIL`) plus pesan git sendiri → `422 git_identity`. Kode tambahan: `400 no_paths`, `400 bad_message`.
- **Discard:** path yang ada di HEAD disalin (`cp -pP`) ke trash lalu `git restore --source=HEAD --staged --worktree` (git ≥ 2.23); path yang tidak ada di HEAD (untracked, file baru, target rename) **dipindah** ke trash (+ `rm --cached` bila ter-stage) — tidak ada `rm` file user. Folder trash = `$(git rev-parse --absolute-git-dir)/nextboard-trash/<YYYYMMDD-HHMMSS>[-N]` (`mkdir` tanpa `-p`, jadi dua discard dalam detik yang sama tidak bercampur); respons selalu `.git/nextboard-trash/<id>` + `trashed`. `409 nothing_to_discard`. `GET /trash`: ≤ 50 item terbaru, ≤ 400 path per item. **Restore:** menolak (`409 exists` + `paths`) bila ada file di tempat tujuan, **kecuali** file ter-track yang identik dengan HEAD (itulah sisa discard; tidak ada pekerjaan yang hilang); folder item yang sudah kosong dihapus dengan `rmdir`.
- **Push:** tanpa upstream (atau `setUpstream`) → `push --progress -u <branch.<b>.remote|origin|remote pertama> refs/heads/<b>:refs/heads/<b>`. Kode job: `detached`, `no_remote`, `no_upstream`, `diverged`, `rejected`, `auth_failed`, `large_files`, `network`, `git_refused`, `timeout`, `idle_timeout`, `git_failed`. Pull: `pull --no-rebase --ff-only --progress` (`pull.rebase` user tidak berlaku). Progres job dari baris `--progress` (`Writing objects 45%`); `result` = `{"action","output"}` (≤ 20 baris terakhir).
- **Branch:** `400 bad_ref` juga untuk `from` yang tidak ada; `switch` memakai `--no-guess` (tidak membuat branch dari remote diam-diam), branch tidak ada → `404 not_found`; hapus branch yang sedang aktif → `409 git_refused`. `DELETE /branches/{name...}` menerima `/` (mentah atau `%2F`).
- **Pesan commit:** tombol sparkle di panel Git dan Source Control menulis pesan [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/).
  - `ai=1` (default klien, `git.commitWithRunner`) → job runner `commit-subject` (agen `runner.defaultAgent`, path dari `agents.paths`; tanpa tool, batas 2 menit) → `result` `{"message","subject","source":"runner"}`. Hub yang membaca perubahan dan menyerahkannya, karena isi diff datang dari repo dan tidak boleh bisa memakai tool: branch, 10 subjek commit terakhir, daftar file, lalu diff semua file (total ≤ 48 KB, dibagi adil: diff kecil utuh, sisanya untuk yang besar; ≤ 80 file). `staged=1` membaca index. Isi file rahasia (`.env*` selain `.example`, kunci, `credentials*`), biner, dan file hasil generate (lockfile, `dist/`, `*.min.js`) tidak dikirim, hanya namanya.
  - Jawaban terstruktur `{type,scope,description,body,breaking}`, dan hub yang merakit pesannya, jadi bentuknya selalu sah: `type(scope)!: description` ≤ 72 karakter, body setelah satu baris kosong dan dibungkus 72 kolom, `BREAKING CHANGE:` sebagai footer. Type di luar daftar jadi `chore`, scope tidak sah dibuang, baris `Co-Authored-By` dibuang.
  - `ai=0`, atau CLI gagal → `{"subject","source":"heuristic"}`: subjek instan dari nama file, bentuknya sama, tetapi hanya menyatakan yang terbukti dari path: `docs`, `test`, `ci`, `build`, selain itu `chore`; `feat` dan `fix` butuh membaca perubahannya. Scope = folder bersama terdalam yang bermakna (bukan `src`, `internal`, …), lalu folder level atas yang dipakai lebih dari separuh file. `?path=` (boleh berulang) membatasi ke file tercentang.
- **Seleksi panel (tambahan):** `PUT /api/projects/{id}/git/selection {"paths":[…]}` → `{"paths"}` (di memori). Command bar `commit`/`commit and push` memakai seleksi ini (difilter ke perubahan yang masih ada), selain itu `all`; pesan kosong → subjek heuristik.
- **Command bar:** `orch.Service.Git` (`GitActions`); diketik → langsung; dari planner → konfirmasi `risky` (logika Fase 6). Balasan langsung mis. `Committed abc1234 “Fix login” (1 file). Pushing…`; hasil job menyusul sebagai pesan thread `Push done.` / `Pull done.` / `Push failed: …` / `Branches diverged — merge in a console.` (kind `error`).
- **GitHub:** hint instal sesuai package manager yang ada (`brew`, `apt`, `dnf`, `pacman`, selain itu `https://cli.github.com`). Publish menolak bila sudah ada remote apa pun (`409 remote_exists`), detached (`409 detached`), tanpa commit dan `commitAll:false` (`409 no_commits`), file > 100 MB saat `commitAll` (`409 large_files`); nama `[owner/]repo` (`400 bad_name`). Job: `commit -A` (pesan default heuristik / `Initial commit`) → `gh repo create <name> --private|--public --source . --remote origin --push`; `result` `{"repoUrl","committed","output"}`, kode `gh_missing`/`gh_not_logged_in`. Login: console shell "GitHub login" di workspace `workspaceId` (body opsional) atau workspace pertama project; hub mengetik `gh auth login` setelah prompt muncul, lalu `gh auth status` tiap 5 dtk (≤ 30 menit) selama sesi `running`; event `github` saat berubah dan saat selesai.
- **Event `git`** dikirim saat ringkasan berubah (juga dari `GET /status`) dan selalu setelah mutasi.

**Verifikasi (This PC, git 2.53, gh 2.46 tidak dipanggil — `gh` palsu di tes):** `go test -race ./internal/gitsvc` (repo sementara, config global terisolasi `GIT_CONFIG_GLOBAL`): status rename/untracked/`-dash`/staged/deleted/file 101 MiB sparse, subfolder project, truncate 400, konflik merge → `409 conflicts`; init/`exists`/`bad_ref`; commit path terpilih saja, path literal `-x [1]*.txt`, `nothing_to_commit`, `large_files` + untrack-large (`.gitignore` tanpa duplikat), identitas kosong → `git_identity`; `git_busy`; push tanpa upstream (`-u`), ahead/pushed, fetch/behind/`fetchedAt`, pull ff, divergen → `diverged` tanpa mengubah HEAD, push ditolak → `rejected`; remote HTTP yang meminta password → `auth_failed` seketika; discard ke trash (salinan + restore), detik sama → `-2`, restore tidak menimpa (`409 exists`); branch create/switch/refused/delete/not_merged; publish lewat `gh` palsu; watcher login. `NEXTBOARD_SSHD_TEST=1 go test ./internal/machine -run TestGitOverSSH` (sshd sementara, login shell zsh): status/commit/push/pull/discard/branch/log lewat SSH; env git di sisi remote direkam lewat remote `ext::` lokal-repo (`GIT_TERMINAL_PROMPT=0`, `GIT_SSH_COMMAND=ssh -o BatchMode=yes`, `SSH_ASKPASS_REQUIRE=never`, `GCM_INTERACTIVE=never`, tanpa `GIT_ASKPASS`); tidak ada file baru/berubah di luar repo + remote. Server: `TestGitAPI`, `TestGitCommandBar` (seleksi panel, `commit and push` → `Push done.` di thread), `internal/orch TestGitSteps` (tanpa layanan → balasan lama; diketik tanpa konfirmasi; planner → `risky`).

### 3.6 Banyak repository dalam satu project

Folder project boleh berupa folder induk yang berisi beberapa repository, dengan atau tanpa repository di folder itu sendiri. Project dengan satu repository tidak berubah.

**Scan:** `GET /git/repos` → `{"items":[RepoInfo…],"truncated":false}`.

```jsonc
// RepoInfo
{ "path": "group/app-b",   // relatif ke folder project; "" = repository project itu sendiri (boleh di atas foldernya)
  "name": "app-b", "root": "/abs/group/app-b",
  "branch": "main", "ahead": 0, "behind": 0, "changed": 3,    // mode status sama dengan panel Git
  "error"?: "…" }                                             // ringkasan gagal dibaca; repository tetap terdaftar
```

- Read-only, tidak fetch, tidak mengirim event, dan tidak mengikuti symlink. Memakai `find` dan `xargs -0` di machine, jadi jalan juga di SSH tanpa agen. Tidak pernah dipanggil berkala: klien scan saat project dibuka, saat panel dibuka, dan saat Refresh.
- **Batas:** 4 level di bawah folder project, ≤ 50 repository, 10 dtk untuk scan, ≤ 4 baca status sekaligus, 20 dtk untuk semua ringkasan. `truncated:true` = kena batas jumlah atau waktu; hasil yang sudah ketemu tetap dikembalikan.
- **Dilewati:** `node_modules`, `bower_components`, `vendor`, `target`, `dist`, `build`, `.cache`, `.gradle`, `.venv`, `venv`, `__pycache__`, `.next`, `.terraform`.
- `.git` hanya dihitung bila folder itu benar-benar root repository (`rev-parse --show-toplevel`), jadi folder `.git` kosong tidak ikut. Submodule dan linked worktree ikut terdaftar.

**Memilih repository:** `?repo=<path>` berlaku di semua endpoint `/git/*` dan `/github`; tanpa parameter = repository project itu sendiri, seperti sebelumnya. Di setiap operasi, skrip di machine memastikan `<folder project>/<path>` masih root repository dan dicapai tanpa symlink; bila tidak, hasilnya `isRepo:false` atau `409 not_repo`, tidak pernah jatuh ke repository luar. `400 bad_path` bila absolut, keluar project (`..`), atau menyentuh `.git`. Pengecekan ini sengaja dilakukan per operasi, bukan dari cache hasil scan, karena cache bisa basi.

**State per repository:** kunci `git_busy`, seleksi panel, dan ringkasan terakhir. Push di satu repository tidak memblokir repository lain.

**Event:** repository di bawah folder project memakai `{"type":"gitRepo","projectId","repo","status"}` agar klien yang hanya mengenal repository project tidak salah membaca ringkasannya. Repository project itu sendiri tetap `{"type":"git"}`.

**Repository bersarang di dalam repository lain:**
- `GitStatus.nestedRepos` (`["libs/inner"]`) mendaftarnya; `files` dan `changed` tidak lagi memuat `libs/inner/`.
- Commit `all` (panel, command bar, publish) tidak men-stage-nya: `git add -A` akan mencatat gitlink `160000` yang isinya tidak ikut ter-clone, dan gagal untuk repository tanpa commit.
- `POST /init` di folder yang berisi repository → `409 nested_repos` (+`paths`): repository baru akan membungkus semuanya.

**Command bar:** `repo <nama> <perintah git>`, mis. `repo app-a commit and push`, `repo group/app-b push`, `repo "my app" commit with the message Fix login`, `repo . pull` (repository project itu sendiri). `<nama>` = path atau nama folder yang unik, huruf besar/kecil diabaikan. Step `git` planner punya field `repo`.
- Tanpa nama: 0 repository → pesan lama; 1 repository → otomatis repository itu; ≥ 2 → ditolak dengan daftar nama dan tidak ada yang dijalankan. Tidak pernah commit ke semua repository.
- Pesan thread untuk repository di bawah folder project diawali path-nya: `app-a: Push done.`

**Web:** pilihan repository disimpan per project di browser dan dipakai bersama oleh pill, panel Git, kartu Branches, dan Source Control di Code studio. Picker tersembunyi bila repository kurang dari dua. Dekorasi Explorer dan gutter editor mengikuti repository terpilih saja.

**Folder induk yang bukan repository:**
- Go to file membaca tiap repository di bawahnya (≤ 5 level, ≤ 50 repository) lewat git-nya sendiri, sehingga `.gitignore`-nya berlaku. Sisanya lewat `find` dengan daftar prune, `source` tetap `walk`.
- Search dengan ripgrep juga mengikuti `.gitignore` tiap repository. Tanpa ripgrep, hanya daftar prune yang berlaku.
- Folder yang tidak bisa dibaca hanya menghilangkan folder itu, bukan seluruh daftar atau pencarian.
- Baris project di home screen menjumlahkan repository di bawah folder itu.

**Belum:** Explorer tidak meredupkan file yang di-ignore di repository di bawah folder induk. Android dan desktop belum punya picker; tanpa `?repo=` perilakunya tetap.

**Verifikasi (This PC, 2026-11-03):** `go test -race ./internal/gitsvc` (`TestRepos*`, `TestSubRepo*`, `TestNestedInOuterRepo`, `TestInitRefusedAboveRepos`, `TestPick`), `internal/server TestGitMultiRepo`, `internal/orch TestGitSteps`. `NEXTBOARD_SSHD_TEST=1 go test ./internal/machine -run TestGitReposOverSSH` (sshd sementara, login shell zsh, nama folder dengan spasi). Chrome headless lewat Playwright terhadap hub dan Vite sementara: folder induk, repository + nested, satu repository, folder kosong (panel, pill, kartu Branches, Source Control, command bar).

## 4. Preview web (B17–B19)

### 4.1 Probe port

`GET /api/machines/{id}/ports?probe=3000,3001,4200,5173,8000,8080` → `[{"port":5173,"open":true,"http":true}]`. ≤ 20 port, paralel; koneksi TCP ke `127.0.0.1` (lalu `[::1]`) di machine itu (lokal: dial; SSH: channel `direct-tcpip`), timeout 800 ms; `http` = respons HTTP ke `GET /` dalam 1,5 dtk.

### 4.2 Preview

```jsonc
{ "id": "pv_…", "machineId": "local", "targetPort": 5173, "targetScheme": "http", "path": "/",
  "listenPort": 12321, "targetHost"?: "portal.example.net", "sessionId"?: "s_…", "auto": false,
  "state": "waiting"|"live"|"error", "error": "", "createdAt": "…", "lastUsedAt": "…" }
```

| Method | Path | Request | Response / error |
|---|---|---|---|
| GET | `/api/previews` | — | `Preview[]` |
| POST | `/api/previews` | `{"machineId","port","scheme"?: "http","path"?: "/","sessionId"?}` atau `{"url"}` untuk situs di tailnet (§4.9) | `201 {"preview","launchPath":"/__nb/launch?t=…&to=%2F"}`; (machine, port) atau situs yang sama → `200` preview lama + token baru. `400 bad_port` (1–65535; di This PC port hub dan rentang preview ditolak), `503 no_preview_port`. Respons bisa masih `state:"waiting"` sementara pengecekan port berjalan di latar; event `preview` dengan `state:"live"` bisa tiba **sebelum** respons — klien memakai versi event yang lebih baru. |
| POST | `/api/previews/{id}/launch` | `{"path"?}` | `{"launchPath","expiresAt"}` (token baru, sekali pakai, 60 dtk) |
| DELETE | `/api/previews/{id}` | — | `{"ok":true}` (listener ditutup) |
| GET | `/api/previews/{id}/logs?since=` | — | `{"items":[LogEntry…],"seq"}` |

Klien membangun URL dari origin yang sedang ia pakai: `${location.protocol}//${location.hostname}:${listenPort}${launchPath}`, dan target `postMessage` bridge dengan cara yang sama. Jangan hard-code `https://`: di app desktop SPA berjalan di `http://127.0.0.1` di balik proxy lokal yang menulis ulang `listenPort` ke listener loopback lokal (lihat README nextboard-desktop). Iframe preview diberi atribut `sandbox` tanpa `allow-top-navigation`.

Format yang diparse app desktop dan harus dijaga stabil: `var HUB = <array JSON>;` di `bridge.js`; entri `<skema>://<host>:<port>` di `frame-src` CSP SPA; objek preview ber-`id` `pv_…` dengan `listenPort`; `launchPath` berbentuk `/__nb/launch?t=…`.

### 4.3 Listener dan auth

- Satu listener HTTP per preview di `settings.previews.portRange` (default 12320–12339), alamat bind sama dengan listener utama; nginx memberi HTTPS di nomor port yang sama. Ditutup bila 30 menit tanpa request dan tidak ada node `web` yang merujuknya.
- `/__nb/launch?t=` memvalidasi token (sekali pakai, terikat preview) → cookie `nb_pv` (`HttpOnly; SameSite=Lax; Path=/`, `Secure` bila lewat HTTPS, acak per preview, 12 j, diperpanjang) → `302` ke `to`. Request lain tanpa cookie valid → `401` halaman "Open this preview from NextBoard". Iframe dari origin hub dianggap same-site (host sama, port beda) sehingga cookie Lax terkirim; situs lain yang membingkai preview tidak membawa cookie.
- Origin preview tidak pernah melayani `/api`. Path `/__nb/*` dicadangkan: `launch`, `bridge.js`, `log`.
- CSP UI hub mendapat `frame-src` = `<skema request>://<hostname request>:<tiap port rentang>` (dihitung per request; skema dari `X-Forwarded-Proto` peer loopback).

### 4.4 Aturan proxy

- Upstream: `127.0.0.1:<port>` lalu `[::1]:<port>` di machine target (lokal dial; SSH `direct-tcpip`), atau situs di tailnet (§4.9). Target lain tidak pernah di-proxy. Upstream `https` boleh self-signed di loopback.
- Request: `Host` dan `Origin` ditulis ulang ke `localhost:<port>` (dev server memeriksa host); cookie `nb_pv` dibuang; `Accept-Encoding` dibatasi `gzip, identity`.
- Respons: buang `X-Frame-Options`, `Strict-Transport-Security`, direktif `frame-ancestors` (CSP dan Report-Only); bila CSP punya `script-src`/`default-src` tanpa `'self'`, tambahkan `'self'` agar bridge bisa jalan; `Location` ke `http(s)://(localhost|127.0.0.1|0.0.0.0|[::1]):<port>` ditulis ulang ke origin preview; atribut `Domain=` pada `Set-Cookie` dibuang.
- `text/html` (status 200): gzip dibuka, `<script src="/__nb/bridge.js"></script>` disisipkan setelah tag `<head…>` dalam 64 KiB pertama (tidak ketemu → di awal dokumen), `Content-Length` dihapus.
- WebSocket (HMR) di-proxy transparan. Dial ditolak → `502` halaman "Waiting for the dev server on :5173…"; tile mencoba ulang 10× tiap 1 dtk lalu menampilkan Retry / Open in new tab.
- Batas: body request ≤ 100 MB; dial 5 dtk; header respons 60 dtk; streaming tanpa batas.

### 4.5 Protokol bridge (postMessage)

Semua pesan `{ "nb": "bridge", "v": 1, "type": …, … }`. Induk mengirim `hello` saat iframe dimuat; bridge mencatat `event.origin` bila ada di daftar origin hub (dari `Hub.urls` + hostname preview dengan port utama) dan hanya membalas ke origin itu.

| Arah | type | Isi |
|---|---|---|
| induk → bridge | `hello`, `back`, `forward`, `reload {hard}`, `navigate {url}`, `fill {id,username,password,code}` | `navigate` hanya path same-origin. `fill` hanya atas pilihan orangnya ([API-vault.md](API-vault.md)) |
| bridge → induk | `filled {id,username,password,code}` | Tiga boolean: field apa yang terisi. Tidak pernah nilainya |
| bridge → induk | `ready {url,title}`, `nav {url,title}` (pushState/replaceState/popstate/hashchange) | |
| bridge → induk | `console {level,args[],at}`, `error {message,source,line,col,stack}`, `net {method,url,status,ms}` | args di-stringify ≤ 2 KB |
| bridge → induk | `open {url}` | `window.open`/`target=_blank`: same-origin → tab baru di tile (≤ 12), eksternal → tab browser klien |

Bridge juga mengirim batch log ke `POST /__nb/log` (≤ 64 KB, ≤ 10/dtk) → ring 500 entri per preview di hub → event `previewLog` dan `/logs`, sehingga **kartu log** (pengganti inspector WebKit) bekerja di semua klien.

### 4.6 Preview otomatis (B18)

Hub memindai output tiap sesi (setelah strip marker dan ANSI) dengan `https?://(localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1\]):(\d{2,5})(/[^\s"'<>]*)?`, sekali per (sesi, port) per run agen. Port dicek di machine sesi (3× dalam 3 dtk); terbuka → preview `auto:true`, hub menyisipkan node `web` di kanan terminal + edge `preview` lewat `boardOps`, event `previewSuggested`. Dimatikan dengan `settings.previews.auto:false`.

### 4.7 Node dan edge

| kind | `data` |
|---|---|
| `web` | `{ "url": "http://localhost:5173/", "previewId"?: "pv_…", "machineId"?: "local", "port"?: 5173, "tailnet"?: true, "realBrowser"?: true, "device": "fit"\|"desktop-1920"\|"desktop-2560"\|"desktop-3840"\|"phone-375"\|"phone-393"\|"phone-430"\|"phone-412"\|"phone-360" (id lain dibaca sebagai `fit`), "zoom": 100, "auto": false, "tabs"?: [{ "id", "url", "title" }], "activeTab"?: "…" }` |
| `weblog` | `{ "previewId": "pv_…", "filter": "all"\|"errors"\|"network" }` |
| `link` | `{ "url": "https://…", "title"?: "…" }` — situs eksternal, dibuka di tab browser baru |
| `branches` | §3.4 |
| `document` | `{ "documentId": "doc_…", "versionId"?: "v_…" }` |

Edge baru: `preview` (terminal → web, warna cyan), `log` (web → weblog). Ukuran device: iframe berukuran tetap + `transform: scale()`; zoom 50–250 % langkah 10.

### 4.8 Implementasi hub (Fase 7 bagian A) — deviasi dan verifikasi

Kode: `internal/preview` (manager, proxy, bridge, log, deteksi, probe port), `internal/machine/dial.go` (`DialLoopback`, `ProbePort`), `internal/server/h_preview.go` + `gitpreview.go`.

- **Cookie per listener: `nb_pv_<listenPort>`** (bukan `nb_pv`; lewat HTTP polos `nb_pv_http_<listenPort>`, karena browser menolak halaman `http://` menimpa cookie `Secure` bernama sama): cookie dibatasi host, bukan port, jadi satu nama untuk semua preview akan saling menimpa antar tile. Nilainya acak per peluncuran (bukan per preview), 12 j geser, dikirim ulang paling sering tiap jam. Cookie NextBoard (`nb_session`, `nb_session_http`, `nb_pv*`) **dibuang dari request ke upstream**, dan `Set-Cookie` upstream dengan nama itu dibuang (mencegah cookie tossing ke hub).
- **Header tambahan yang dibuang:** `Clear-Site-Data` (bisa menghapus cookie hub di host yang sama), `Alt-Svc`, `X-Forwarded-For`, `Forwarded`. `Referer` dari origin preview ditulis ulang ke `http(s)://localhost:<port>`, selain itu dihapus. `connect-src` juga mendapat `'self'` (beacon log); `'none'` dibuang bila `'self'` ditambahkan. Respons preview mendapat CSP tambahan `frame-ancestors 'self' <origin hub>` (hanya UI hub yang boleh membingkai; tab baru/WebView tidak terpengaruh).
- **Auth:** semua path kecuali `/__nb/launch` butuh cookie, termasuk `/__nb/bridge.js` dan `/__nb/log`. `to` pada launch harus path same-origin (`//host`, `/\` ditolak → `/`). Token 32 byte acak, sekali pakai, 60 dtk; respons launch `no-store`. Halaman 401/502 tanpa skrip (CSP `default-src 'none'`).
- **Proxy:** `httputil.ReverseProxy` (WebSocket/HMR transparan, `FlushInterval -1` untuk SSE), satu `http.Transport` per preview yang selalu men-dial `127.0.0.1` lalu `[::1]` port target di machine itu (alamat dari request tidak pernah dipakai). Injeksi: HTML 200 (juga `application/xhtml+xml`), gzip dibuka, encoding lain (br/zstd) dibiarkan tanpa injeksi. Dial ditolak → `502` "Waiting for the dev server on :N…" + state `waiting`; SSH tanpa forwarding (`administratively prohibited`) → `machine.ErrForwardingDisabled`, state `error` + pesan. State `live` saat probe awal/respons berhasil; event `preview` saat dibuat dan saat state berubah.
- **Listener:** HTTP polos, bind = host dari `listen` hub (`0.0.0.0` → semua). Port pertama yang bebas di `previews.portRange`; port yang dipakai proses lain dilewati. ≤ 20 listener. Reaper (tiap menit, mulai saat preview pertama dibuat) menutup preview yang 30 menit tidak dipakai dan tidak dirujuk node `web` mana pun (`data.previewId` dipindai di semua board). `DELETE` menutup listener seketika.
- **Port target yang ditolak di This PC:** port hub, port listener hook loopback, dan seluruh rentang preview (`400 bad_port`). Error lain: `400 bad_scheme`, `400 bad_path`, `404 preview_not_found`, `404 machine_not_found`, `503 no_preview_port`.
- **`/__nb/log`:** body `{"entries":[…]}` atau array, ≤ 64 KB, ≤ 200 entri, ≤ 10 request/dtk (`429`), `204`. Entri divalidasi (`console|error|net`, arg ≤ 2 KB, ≤ 20 arg) dan diberi `seq`/`at` hub. `GET /logs?since=` → `{"items","seq"}`; event `previewLog` digabung ≤ 4×/dtk.
- **Probe port:** hasil diurut per port; `400 bad_port` / `too_many`; machine terputus → `503 machine_offline`. `http` = baris status `HTTP/1.x` untuk `GET /` dalam 1,5 dtk.
- **Preview otomatis:** detektor (`preview.Detector`) dipasang berantai pada `sessions.OnOutput`; ANSI dibuang (`charmbracelet/x/ansi`), URL yang terpotong di batas chunk digabung dengan sisa chunk sebelumnya, tanda baca di akhir path dibuang, host `0.0.0.0`/`[::1]` → `localhost`. "Run" = `startedAt` sesi. Port dicek 3× dalam 3 dtk; bila tertutup, URL yang dicetak lagi nanti boleh dicoba lagi. Node `web` 960×640 di kanan node terminal (+60 px) dengan `data {url, previewId, machineId, port, device:"fit", zoom:100, auto:true}` + edge `preview`; tidak ditambah bila workspace sudah punya node `web` untuk preview atau (machine, port) itu. Event `previewSuggested` mendapat field tambahan `machineId`, `port`, `workspaceId`, `nodeId`.
- **CSP UI hub:** `frame-src 'self' <skema request>://<hostname request>:<tiap port rentang>`.
- **Pengaturan (§12):** `previews.auto`, `previews.portRange` (`[awal, akhir]`, 1024–65535, ≤ 200 port, dibaca saat hub start), `git.commitWithRunner` (default `true`, hanya dibaca klien; `git.suggestWithRunner` yang lama diabaikan) — `internal/settings/previews_git.go`.
- **Command bar** `open localhost <port>`: hub membuka (atau memakai ulang) preview di machine project dan menyisipkan node `web` dengan `data {url, previewId, machineId, port, device:"fit", zoom:100, auto:false}`. Klien tetap boleh membuat preview sendiri untuk node `web` lama tanpa `previewId`.

**Verifikasi:** `go test -race ./internal/preview` (server HTTP lokal sebagai dev server, listener di rentang port acak, bukan 12320–12339): tanpa cookie `401`, token kedua kali `401`, launch → cookie `HttpOnly; Secure; SameSite=Lax`, bridge disisipkan setelah `<HEAD lang=en>` / di awal / setelah gzip dibuka, XFO/HSTS/Clear-Site-Data/`frame-ancestors` dibuang, CSP `'self'` ditambah, `Domain=` dibuang, `nb_session` tidak sampai upstream maupun di-set upstream, Host/Origin/Referer ditulis ulang, `Location` loopback → origin preview, upload 3 MB, WebSocket echo lewat proxy (tanpa cookie `401`), dev server mati → `502` + `waiting`, port hub/rentang ditolak, open redirect `to` ditolak, beacon log + event + rate limit, delete menutup listener, reaper idle vs node yang merujuk, probe port (tertutup / TCP saja / HTTP), detektor (ANSI, chunk terpotong, dobel, port tertutup, `auto:false`). `NEXTBOARD_SSHD_TEST=1 go test ./internal/machine -run TestPreviewOverSSH`: probe dan proxy lewat `direct-tcpip`, `AllowTcpForwarding no` → `ErrForwardingDisabled`. Server: `TestPreviewAPI` (CSP `frame-src`, probe, buat/ulang/launch/log/hapus, proxy lewat listener nyata), `TestAutoPreview` (console shell mencetak URL berwarna → satu preview + node `web` + edge `preview`, cetak ulang tidak dobel, port tertutup tidak membuat tile). Bridge: `node --check` + simulasi DOM palsu di Node 24 (manual, di luar `go test`): pesan sebelum `hello` diantre, `hello` dari origin asing diabaikan, `ready`/`console`/`nav` (pushState)/`error`/`net`/`open` hanya ke origin hub yang menyapa, `back` dari origin lain diabaikan, `navigate` lintas origin ditolak, beacon `/__nb/log` terkirim dan tidak dilaporkan sebagai `net`. Belum diuji di browser nyata (iframe + sertifikat self-signed, open issue #2).

### 4.9 Situs luar lewat proxy

Alamat yang resolve ke IP Tailscale atau IP publik tampil di web tile lewat proxy yang sama, tanpa browser sungguhan, jadi tidak ada Chrome yang memakan RAM di This PC. Situs yang tidak bisa jalan lewat proxy, dan alamat di This PC atau jaringan lokalnya, ke browser sungguhan (`docs/API-gui.md` §8). Data `tailnet:true` di node berarti "lewat proxy", termasuk untuk situs publik.

**Alur.** Tile mengirim alamat yang diketik ke `POST /api/previews {"url"}`. Hub me-resolve host itu dari This PC, apa pun machine project-nya.

| Hasil | Arti | Tile |
|---|---|---|
| `201`/`200` | Situs menjawab | Node dapat `tailnet:true` dan `previewId`, address bar menampilkan alamat aslinya |
| `400 needs_browser` | Situs publik yang akan rusak lewat proxy (lihat "Uji awal") | Browser sungguhan, dengan pesan singkat |
| `400 private_address` | Hanya mengarah ke This PC, jaringan lokal, metadata cloud, atau alamat khusus lain | Browser sungguhan |
| `400 no_such_host` | DNS gagal | Browser sungguhan |
| `400 bad_url`, `bad_path`, `bad_port` | Bukan http(s), memuat user:password, path tidak valid, atau port hub sendiri di IP This PC | Browser sungguhan |
| `502 site_unreachable` | Tidak ada yang menjawab dalam 5 dtk | Pesan di tile; browser tidak dimulai |

**Uji awal (hanya situs publik).** Sebelum listener dibuka, hub meminta halamannya sekali, dengan User-Agent browser yang meminta, mengikuti redirect (maks 6). Redirect ke `https` atau ke nama yang sama dengan/tanpa `www` mengubah tujuan preview, karena kalau tidak frame akan keluar dari proxy; respons `preview` memuat tujuan akhirnya. Redirect ke host lain, `403`/`429`/`503`, header `Cf-Mitigated`, dan sertifikat yang tidak valid menjadi `needs_browser`: login di host lain tidak bisa kembali lewat proxy, dan situs yang menolak hub hanya akan menampilkan penolakannya.

Toolbar dan menu tile punya **Open in a real browser** (`realBrowser:true`) untuk situs yang tidak cocok lewat proxy. Pilihan itu diingat per host di browser yang memilih (`localStorage`), jadi alamat berikutnya di host itu langsung ke sana; menu **Try the light view** membatalkannya. Node lama dan node dari planner berpindah ke jalur ini saat **Open here** ditekan.

**Yang dianggap publik.** Unicast global, kecuali rentang privat, `0.0.0.0/8`, `100.64.0.0/10`, `192.0.0.0/24`, rentang dokumentasi dan benchmark, `240.0.0.0/4`, NAT64 `64:ff9b::/96`, dan 6to4 `2002::/16` (dua terakhir bisa membungkus IPv4 privat).

**Yang dianggap tailnet.** `100.64.0.0/10` dan `fd7a:115c:a1e0::/48`, kecuali alamat layanan Tailscale (`100.100.100.100`, `fd7a:115c:a1e0::53`) dan `100.115.92.0/23`. Rentang ini dicek dari alamatnya saja, jadi alamat CGNAT lain di dalamnya (misalnya antarmuka Cloudflare WARP) ikut lolos, tetapi tetap hanya alamat yang bisa dijangkau This PC.

**Keamanan.**

- Tiap koneksi me-resolve nama lagi dan hanya men-dial IP Tailscale atau publik, termasuk tiap hop uji awal, jadi nama atau redirect yang mengarah ke loopback, jaringan lokal, atau metadata cloud tidak pernah sampai. Nama dengan alamat campuran hanya di-dial ke alamat yang diizinkan.
- Sertifikat diverifikasi terhadap nama situs, juga untuk IP publik polos. Hanya IP Tailscale polos yang tidak diverifikasi: tidak ada nama untuk dicek, dan WireGuard sudah mengautentikasi node-nya.
- **Antar-tile:** browser menganggap semua port di satu host sebagai satu site, jadi halaman di satu tile bisa memanggil tile lain dengan cookie-nya. Permintaan dengan `Origin` dari port lain di host yang sama ditolak `403` oleh situs, dan oleh dev server bila asalnya port situs. Hub sendiri sudah menolak: tanpa CORS, mutasi perlu `X-NextBoard`, WebSocket mengecek origin.
- **Port yang berganti pemilik:** `localStorage` dan kawan-kawan terikat origin, yaitu port. Bridge menghapus `localStorage`, `sessionStorage`, Cache, dan IndexedDB sebelum halaman jalan bila port itu terakhir dipakai situs lain (penanda: cookie `nb_pv_owner_<port>`). Service worker ditolak (`404`) karena akan hidup lebih lama dari situsnya. Situs mendapat port dari hash namanya, jadi biasanya kembali ke port yang sama setelah hub restart dan datanya tetap ada.
- Auth listener sama dengan §4.3. Situs tidak menjadi terbuka untuk siapa pun yang bisa menjangkau hub.

**Yang ditulis ulang.**

- Lewat HTTP polos, direktif CSP `upgrade-insecure-requests` dibuang, karena akan mengubah semua aset ke HTTPS yang tidak ada di origin preview.
- URL situs sendiri (`http(s)://`, `ws(s)://`, `//`, dan bentuk JSON `https:\/\/`) di semua header dan body teks (HTML, CSS, JS, JSON, XML) menjadi origin preview, termasuk yang di-gzip. `text/event-stream` tidak disentuh agar event tidak tertahan.
- Host lain, URL yang di-encode, dan nama host polos dibiarkan.
- `Host`, `Origin`, dan `Referer` ke upstream memakai alamat asli situs.

**Cookie.** Cookie dibatasi host, bukan port, jadi tanpa pemisahan sesi satu situs ikut terkirim ke situs lain di tile lain. Tiap situs punya prefix sendiri `nb_c_<hash host:port>_` di browser. Hub hanya meneruskan cookie berprefix itu, dengan nama aslinya; bridge melakukan hal yang sama pada `document.cookie`. Dev server localhost tidak pernah menerima cookie berprefix ini. Lewat HTTP polos `Secure` dibuang dan `SameSite=None` menjadi `Lax`, sebab browser menolak cookie itu kalau tidak.

**Batasan.**

- Pemisahan cookie mencegah cookie salah kirim, bukan situs yang berniat jahat. Semua preview berbagi hostname dengan hub, jadi JS sebuah situs yang sengaja melewati `document.cookie` bisa membaca cookie non-`HttpOnly` situs lain dan menimpanya. Cookie `HttpOnly`, termasuk sesi hub, tidak terbaca dan tidak tertimpa, dan API hub tetap menolak origin preview. Buka lewat jalur ini hanya situs di tailnet yang dipercaya.
- Port lain di host yang sama adalah server lain dan tidak di-proxy: browser yang membuka NextBoard memuatnya langsung. Aset Vite dev (`portal…:5177`) jadi hanya tampil kalau perangkat itu ada di tailnet dan server itu mengizinkan origin preview lewat CORS. Build produksi tidak terkena.
- Link ke host lain keluar dari proxy dan bisa ditolak frame-nya.
- `document.cookie` hanya dibungkus di halaman HTML berstatus 200 (tempat bridge disisipkan); `cookieStore` tidak dibungkus.
- Asset dengan `integrity` yang isinya menyebut nama situs akan gagal hash-nya.
- Data di `localStorage` terikat port listener. Kalau hash dua situs bertabrakan, situs mendapat port lain dan datanya hilang; cookie tidak, karena prefix-nya dari nama situs.
- Situs yang memanggil host lain lewat JS (API di subdomain lain) kena CORS, karena origin-nya sekarang origin preview. Hub lewat HTTP polos juga bukan secure context: `crypto.subtle`, clipboard, dan passkey tidak ada. Untuk semua ini: **Open in a real browser**.

**Verifikasi (This PC, 2026-11-03):** `go test -race ./internal/preview -run 'Site|IsTailnet'`, `internal/server TestPreviewOfSiteAPI`. Browser sungguhan di hub tes: login dengan CSRF, redirect, dan dua situs dengan nama cookie sama di IP Tailscale This PC (server palsu); halaman login `portal.devboxdev.my.id` asli (Laravel, Inertia, Vite dev) terbuka lengkap, tanpa pernah di-submit. Belum dicoba: login sungguhan ke situs lewat jalur ini, HTTPS di depan hub lewat browser, dan situs di node tailnet lain.

## 5. Documents (E2)

```jsonc
{ "id": "doc_…", "projectId": "p_…", "type": "architecture", "title": "Architecture", "ask"?: "…",
  "versions": [{ "id": "v_…", "at": "…", "agent": "claude", "machineId": "local", "effort": "balanced",
                 "state": "complete"|"incomplete", "words": 980, "durationMs": 312000, "jobId": "jb_…" }] }
```

- Tipe: `overview`, `onboarding`, `architecture`, `readme`, `api`, `dataModel`, `security`, `testing`, `releaseNotes`, `custom` (dengan `ask`). Ditulis dalam bahasa Inggris, Markdown, diawali `# <judul>`.
- Effort: `quick` 400–700 kata / 6 menit, `balanced` 700–1100 / 12 menit, `thorough` 900–1500 / 25 menit; idle 200 dtk. Tool baca-saja.
- Streaming lewat `jobOutput`; Stop → versi `incomplete` dengan teks yang sudah datang (Regenerate, Export draft).

| Method | Path | Request | Response |
|---|---|---|---|
| GET | `/api/projects/{id}/documents` | — | `Document[]` |
| POST | `/api/projects/{id}/documents` | `{"type","ask"?,"effort","agent"?,"profileId"?,"documentId"?}` | `202 {"document","versionId","job"}` |
| GET | `/api/documents/{docId}` | — | `Document` |
| GET | `/api/documents/{docId}/versions/{vid}` | — | `{"version","markdown"}` |
| DELETE | `/api/documents/{docId}`, `/api/documents/{docId}/versions/{vid}` | — | `{"ok":true}` (permanen) |
| GET | `/api/documents/stats?machineId=&agent=` | — | `{"quick":{"medianMs":…,"runs":5},"balanced":…,"thorough":…}` (5 run selesai terakhir) |

**PDF (di klien):** rute `/print/documents/<docId>/<vid>?paper=a4|letter` dengan stylesheet cetak: selalu tema terang, halaman sampul (judul, agen, project, tanggal), daftar isi bila ≥ 3 heading, header berjalan, nomor halaman via `@page` margin box, `h1` mulai halaman baru, gambar jadi placeholder berlabel. Web/desktop: `window.print()`; Android: `PrintManager` + WebView. Juga Export Markdown, Copy Markdown, Add to board (node `document`). Renderer Markdown tanpa HTML mentah.

### 5.1 Implementasi hub (Fase 7 bagian B) — deviasi dan verifikasi

`internal/studio` (`documents.go`), endpoint di `internal/server/h_studio.go`. Job = runner purpose `document` (`claude -p --output-format stream-json --verbose … --tools Read,Glob,Grep --allowedTools Read,Glob,Grep`, Codex `exec --sandbox read-only`), batas waktu per effort, idle 200 dtk, prompt bahasa Inggris dengan rentang kata dan `# <judul>`.

- **Versi `running`** (tambahan pada enum `state`): versi dicatat sebelum job mulai (dengan `jobId`) lalu diselesaikan `OnFinish`: `complete` dengan teks, `incomplete` dengan teks parsial (Stop/timeout/gagal), dihapus bila tidak ada teks sama sekali. Saat hub start versi `running` → `incomplete` (bila ada file) atau dihapus. `words` tidak menghitung penanda Markdown (`#`, `-`).
- `documentId` (Regenerate) memakai tipe dan `ask` dokumen itu. Agen: request → `settings.runner.defaultAgent` → Codex bila Claude tidak ada di machine; selain claude/codex → `400 runner_unsupported`. Profil: request → profil default `settings.agents.profiles` → login default; profil yang tidak cocok → `400 profile_mismatch`. Error lain: `400 bad_type|bad_effort|bad_ask`, `404 document_not_found|version_not_found`.
- Event tambahan `documentRemoved {projectId, documentId}` saat `DELETE /api/documents/{docId}`.
- **Terverifikasi nyata** (This PC, Claude Code 2.1.312, `--model haiku`, 1 run, repo scratch 2 file): `done` 24,7 dtk, 342 kata (di bawah 400 — rentang kata hanya arahan), fase `launching → thinking → writing → finishing → done`. `stream-json` mengirim teks per pesan asisten, jadi run ini menghasilkan **satu** chunk `jobOutput`; streaming per token butuh `--include-partial-messages` (tidak dipakai/diverifikasi). Claude membuat folder kosong `~/.claude/projects/<slug repo>/memory/` walau `--no-session-persistence` (perilaku CLI; folder uji dihapus lagi). Checksum `~/.claude/settings.json` sama.

## 6. Diagrams (E3)

```jsonc
// nb.diagram/v1 — output runner, divalidasi hub
{ "schema": "nb.diagram/v1", "title": "Request lifecycle", "direction": "TB",     // "TB" | "LR"
  "nodes":  [{ "id": "api", "label": "API server", "note"?: "Go net/http", "shape": "box"|"round"|"ellipse"|"diamond", "group"?: "backend" }],
  "edges":  [{ "from": "web", "to": "api", "label"?: "HTTPS", "style"?: "solid"|"dashed" }],
  "groups": [{ "id": "backend", "label": "Backend" }] }
```

- Batas: node ≤ 40, edge ≤ 80, grup ≤ 8, `label` ≤ 48, `note` ≤ 80, id `^[a-z0-9_-]{1,32}$`, edge harus menunjuk node yang ada. Edge keluar dari `diamond` tanpa label → peringatan.
- Subjek: `architecture`, `userFlow`, `dataFlow`, `components`, `requestLifecycle`, `stateFlow`, `custom`. Effort: 6–10 / 8–14 / 10–16 komponen. Gaya `ink` (default, garis warna teks, isi transparan) atau `color` (warna per grup).

| Method | Path | Request | Response |
|---|---|---|---|
| POST | `/api/projects/{id}/diagrams` | `{"subject","ask"?,"effort","style","agent"?}` | `202 {"diagramId","job"}` → result `nb.diagram/v1` |
| GET | `/api/projects/{id}/diagrams` | — | `[{ "id","title","createdAt","agent" }]` |
| GET | `/api/diagrams/{id}` | — | `{"id","diagram","style","createdAt"}` |

**Mendarat di board (klien web):** layout berlapis sendiri (sama dengan hub, tanpa pustaka) → satu request ops: node `shape` (`box`→rect sharp, `round`→rect round, `ellipse`, `diamond`) dengan `text`; node `line` `arrowEnd:true` (`dashed` → `strokeStyle:"dashed"`) + node `text` di titik tengah untuk label; zona grup = `shape` rect `strokeStyle:"dashed"` di belakang bila ≥ 2 grup; judul `text` di atas. Setiap node mendapat `data.diagramId` + `data.diagramKey`. Hasil berupa shape lepas (tanpa editor diagram). Ekspor PNG (2×) dan Excalidraw JSON dibuat dari JSON as-generated. Starter tanpa AI (Flow, Steps, Decision, Mind map) = template `nb.diagram/v1` di klien.

**Hasil yang terbaca.** Web (`web/src/studio/diagrams/layout.ts`) dan hub (`internal/studio/layout.go`) memakai aturan dan angka yang sama:

- **Bingkai mengikuti teks.** Ukuran dihitung dari teks yang sudah di-wrap, memakai lebar huruf Caveat terukur dan ukuran nyatanya (`.font-hand`: "m" 25 px, baris 31,25 px). Teks di `diamond` dan `ellipse` berada di persegi yang muat di dalam garisnya (`.shape-label.in-diamond`, `.in-ellipse`).
- **Panah menghindar.** Panah yang bisa lurus digambar dulu beserta labelnya. Sisanya melengkung sesedikit mungkin dan menjaga jarak 12 px dari tile lain dan dari label itu. Panah berhenti di garis tepi bentuk sebenarnya; dua panah di antara tile yang sama berjajar.
- **Label di samping panah,** di tempat kosong terdekat, tidak di atasnya. Node berisi `backed: true`: latar sewarna papan, jadi panah yang terpaksa lewat (bisa terjadi di diagram sangat padat, karena satu kurva tidak selalu lolos) ada di belakang bingkai dan tidak mencoret teks.
- **Export** PNG dan Excalidraw memakai layout dan wrap yang sama.

**Gaya panah.** Dua pilihan di panel Diagrams, terlepas dari `Shapes` (Ink/Colorful), berlaku untuk apa pun yang mendarat atau diekspor berikutnya (hasil run, starter, Earlier diagrams), dan diingat per browser (`nb.diagram.arrows`, `nb.diagram.arrowColor`). Tidak mengubah JSON diagram, jadi diagram lama bisa didaratkan ulang dengan gaya lain tanpa run baru. Ekspor dari menu klik kanan di board mengikuti cara diagram itu didaratkan, bukan pilihan panel.

- **Arrows:** `curved` (default, seperti di atas) atau `wiring`, seperti diagram wiring: `web/src/studio/diagrams/wires.ts` dan `internal/studio/wires.go`, aturan dan angka sama.
  - Grid 16 px, satu pencarian per panah dengan biaya untuk belokan dan persilangan. Panah pendek dulu, lalu semuanya sekali lagi dengan mengetahui yang lain. Jarak 16 px dari tile yang bukan miliknya.
  - Panah dari satu sumber dan gaya garis yang sama berbagi jalan keluar dan berpisah di titik (`dots`); persilangan tanpa titik berarti tidak tersambung. Panah dari sumber lain tidak berbagi jalan, kecuali benar-benar tidak ada ruang (jarang, hanya di diagram sangat padat). Tiap panah punya titik masuk sendiri di tile tujuan.
  - Layout menyesuaikan: gap antar layer dan antar node memberi ruang untuk jalur, tiap layer bergeser ke arah yang disambungnya supaya panah lebih lurus, dan tengah tile jatuh di garis grid. Karena itu posisi tile berbeda dari `curved`.
  - Label di samping panah, di bagian yang tidak dibagi bila ada, lalu panah dirutekan ulang menjauhi label dan label ditempatkan sekali lagi.
  - Garis `roughness: 0` (bersih), bingkai tetap gaya tangan.
- **Arrow color:** `mono` (satu tinta, mengikuti tema) atau `color`: satu warna per sumber agar panah bisa diikuti, label ikut warna panahnya. Bila `Shapes` = Colorful dan sumbernya punya grup, panah memakai warna grup itu. Palet `DIAGRAM_WIRE_COLORS` berulang setelah 6 sumber. Warnanya token tema, terbaca di gelap dan terang.
- **Di board:** node `line` membawa `route` + `dots` ([API-board.md](API-board.md)). Knob: dua ujung, plus satu per bagian di antara dua sudut untuk menggeser bagian itu. Panah tetap lepas: memindahkan tile tidak membawa panahnya, sama seperti `curved`. Klien yang belum mengenal `route` (Android, desktop) menggambar garis lurus lewat `points`.
- **API:** `POST /api/diagrams/{id}/land` menerima `"arrows"` dan `"arrowColor"`; nilai lain diabaikan. Tanpa `arrowColor` panah mengikuti warna bentuk, seperti sebelum ada pilihan ini.

### 6.1 Implementasi hub (Fase 7 bagian B) — deviasi dan verifikasi

`internal/studio` (`diagrams.go`, `layout.go`). Runner purpose `diagram` dengan `--json-schema`/`--output-schema` = skema ketat (semua field wajib; teks opsional berupa `""`, cocok untuk structured output Codex). Output divalidasi hub (`ValidateDiagram`): batas §6 ketat; normalisasi longgar alias shape (`pill`/`rounded` → `round`, `circle` → `ellipse`, `rect` → `box`, `decision` → `diamond`), arah `TD`/`DOWN` → `TB`, `dotted` → `dashed`, field kosong dibuang.

- **Ulang sekali:** `internal/jobs/runner.go` kini menjalankan `Transform` di dalam loop percobaan, jadi output yang JSON-nya valid tetapi melanggar bentuk/batas (mis. 41 node) dianggap `bad_output` → satu kali ulang → `failed bad_output` (juga berlaku untuk `memory-scan`/`memory-refresh`, sesuai §2).
- Record `diagrams/<projectId>/<id>.json`: `{id, projectId, subject, ask, effort, style, agent, title, createdAt, state: running|done|failed|cancelled, jobId, error, warnings, diagram}`; `GET /api/diagrams/{id}` mengembalikan record ini; daftar menambah `subject`, `state`. Hub restart → `running` jadi `failed`. Error: `400 bad_subject|bad_effort|bad_style|bad_ask`, `404 diagram_not_found`.
- **Ekstensi:** `POST /api/diagrams/{id}/land {"workspaceId","x"?,"y"?,"style"?}` → `{"rev","nodeIds"}`: pendaratan di hub (untuk Android/command bar/tes) dengan layout berlapis sendiri (longest-path + barycenter, tanpa elkjs), satu batch ops. `diagramKey`: `node:<id>`, `edge:<from>><to>:<i>`, `edgeLabel:<i>`, `group:<id>`, `groupLabel:<id>`, `title`. Zona hanya bila ≥ 2 grup dan tidak bertabrakan (antar zona atau dengan node grup lain). Ink = `#e6eaf2` (palet web), `color` = warna palet per grup. Tanpa `x/y` → di kanan isi board. `409 diagram_not_ready`, `404 workspace_not_found`.
- Tidak ada run nyata diagram (anggaran 4 run dipakai untuk usage dan dokumen); skema + validasi diuji dengan CLI palsu.

## 7. Code studio (E4)

Perluasan file API Fase 4:

| Method | Path | Request | Response / error |
|---|---|---|---|
| GET | `/api/machines/{id}/fs/tree?path=&hidden=0` | satu level (lazy) | `{"path","entries":[{name,dir,size,mtime,hidden,binary}],"pruned":[…],"truncated"}` (≤ 5000 entri) |
| POST | `/api/machines/{id}/fs/mkdir` | `{"path"}` | `Stat`; `409 exists` |
| POST | `/api/machines/{id}/fs/rename` | `{"from","to"}` | `Stat`; `409 exists` (tidak menimpa) |
| POST | `/api/machines/{id}/fs/copy` | `{"from","to","unique"?}` | `Stat`; `409 exists` tanpa `unique`, dengan `unique` memakai `nama-2.ext`; `400` folder ke dalam dirinya sendiri. Lihat §7.9 |
| POST | `/api/machines/{id}/fs/trash` | `{"path","projectId"}` | `{"trashedTo"}`: repo git → `<project>/.git/nextboard-trash/<ts>/<rel>`, bukan git → `<project>/.nextboard-trash/<ts>/<rel>`; di luar project → `403 outside_project` |
| POST | `/api/machines/{id}/fs/stat-batch` | `{"paths":[…]}` (≤ 200) | `Stat[]` |

- Prune default: `.git node_modules build dist .next .nuxt .svelte-kit venv .venv target vendor coverage DerivedData Pods __pycache__ .cache .turbo .gradle .idea`; dotfile disembunyikan kecuali `.gitignore .env.example .editorconfig .github .claude`. `hidden=1` menampilkan semuanya.
- Simpan memakai `fs/write` dengan `ifMtime`; konflik → banner "Changed on disk: Reload · Keep mine". BOM dan CRLF dipertahankan apa adanya; file non-UTF-8 (`binary:true`) read-only. Path nyata (realpath; SFTP `RealPath`) di luar project → simpan butuh `allowOutside:true` (UI bertanya sekali per sesi studio). SSH tanpa SFTP → tulis `501 no_sftp`.
- Editor web: **Monaco** (`monaco-editor`, editor VS Code, di-bundle lokal dan dimuat hanya saat workbench dibuka), lihat §7.2. CodeMirror 6 tetap dipakai Markdown studio dan Skills. File ≤ 1 MB bisa diedit, 1–10 MB read-only, > 10 MB tidak dibuka; draft di IndexedDB per (machine, path) dan dipulihkan.
- Ke board: Insert = node `file` view `code` (snapshot, termasuk edit yang belum disimpan); Add folder = view `folder`.

### 7.1 Implementasi hub (Fase 7 bagian B) — deviasi dan verifikasi

`internal/studio/tree.go`, `watch.go`; operasi file baru di `internal/machine/studio_fs.go` sebagai fungsi paket (`machine.Rename/Remove/RealPath/Lstat`, interface `Host` tidak berubah; SSH lewat SFTP, tanpa fallback exec → `501 no_sftp`).

- `fs/tree`: `binary` dari ekstensi (gambar, arsip, objek, media, font…), `pruned` = nama yang disembunyikan (folder prune + dotfile), `.nextboard-trash` ikut di-prune. Data hub This PC → `403`.
- `fs/rename` tidak pernah menimpa: lokal file = `link`+`unlink` (fallback `rename` bila hard link gagal), folder = cek `lstat` lalu `rename`; SFTP = `SSH_FXP_RENAME` + cek. `fs/trash`: `rel` di `.git` atau di trash ditolak, folder project sendiri ditolak, `409 exists`.
- `fs/write` (Fase 4) ditambah `projectId` + `allowOutside` (realpath di luar project → `403 outside_project`) dan `confirm` (§9.2 untuk folder config Claude di machine SSH: `~/.claude` dan semua profil Claude machine itu; juga `fs/mkdir`, `fs/rename`). Body error file API kini membawa `code` `mtime_mismatch` / `no_sftp`.
- **Ekstensi:** `GET /api/machines/{id}/fs/find?root=&q=&limit=50` (cari nama file, semua kata harus ada di path relatif, aturan prune, ≤ 20 000 entri / 10 dtk → `truncated`); **watch polling**: `POST /api/machines/{id}/fs/watch {"paths"(≤ 200),"ttlSec"(≤ 300, default 30)}` → `{"watchId","stats","intervalMs":3000}`, `POST /api/fs/watch/{wid}/renew`, `DELETE /api/fs/watch/{wid}`; hub mem-poll stat tiap 3 dtk dan mengirim `fsChanged` bila size/mtime/ada berubah (≤ 64 watch).
- Tes: lokal (`internal/studio`), SFTP (`internal/machine/studio_ssh_test.go`, `NEXTBOARD_SSHD_TEST=1`), API (`studio_api_test.go`: `ifMtime` basi → `409`, symlink keluar project → `403` lalu `allowOutside`).

### 7.2 Code workbench

Studio Code adalah workbench selebar jendela di atas board: activity bar, Explorer, Search, Source Control, Agents, Settings, grup editor (maksimal 3), panel bawah, status bar, Go to file, command palette. Referensi perilaku: halaman Files DevWorkspace Pro.

| Method | Path | Request | Response / error |
|---|---|---|---|
| GET | `/api/projects/{id}/files` | | `{"items":[rel…],"truncated","source":"git"\|"walk"}` (≤ 200 000). Repo git: `ls-files -co --exclude-standard` tanpa file yang sudah terhapus. Bukan repo: `find` dengan aturan prune §7. |
| POST | `/api/projects/{id}/files/search` | `{"query","matchCase","wholeWord","regex","include","exclude","limit"}` | `{"files":[{"rel","matches":[{"line","col","len","text","textCol"}]}],"count","truncated","engine"}`; `400 bad_pattern` |
| POST | `/api/projects/{id}/files/replace` | field search + `{"replacement","paths":[rel…],"confirm"}` (≤ 2000 file) | `{"files":[{"rel","count","error"}],"count"}`; `403 outside_project` untuk path yang keluar project |
| POST | `/api/projects/{id}/files/ignored` | `{"paths":[rel…]}` (≤ 5000) | `{"items":[rel…]}` dari `git check-ignore` |
| POST | `/api/projects/{id}/files/on-save` | `{"path":rel}` | `{"runs":[{"pattern","command","exitCode","timedOut","output"}]}` |
| GET | `/api/projects/{id}/git/show?path=&ref=HEAD` | `path` relatif ke repo | `{"path","ref","exists","binary","truncated","content"}`; file yang tidak ada di commit = `exists:false`; `400 bad_ref` |
| POST | `/api/sessions` | `+ "panel": true` | sesi tanpa node board, untuk terminal panel bawah; tidak bisa dijangkau order orkestrasi dan tidak masuk live chip |

- **Search:** berjalan di machine project lewat `rg`, lalu `git grep` di work tree, lalu `grep`. Kolom dihitung hub dalam unit UTF-16 supaya sama untuk semua engine. Biner dan file ignored dilewati; batas 1 MB per file hanya berlaku dengan `rg`. Maksimal 5000 hasil, 30 dtk. Regex mengikuti sintaks engine; lokasi kolom memakai RE2.
- **Replace:** hub membaca, mengganti, dan menulis dengan `ifMtime`, byte lain tidak berubah (CRLF dan BOM aman). File di balik link keluar project gagal per file. UI melewati file yang punya edit belum tersimpan.
- **Keamanan:** git dipanggil dengan `core.fsmonitor=false` dan `core.hooksPath=/dev/null`, sehingga membuka project tidak menjalankan kode dari config repo. Panel Git dan home screen: §3.
- **Settings:** `editor.onSave` = `[{"pattern","command"}]` (≤ 50). Pola tanpa `/` mencocokkan nama file, dengan `/` dari folder project; `{file}` = path penuh (di-quote). Dijalankan berurutan di folder project, berhenti di kegagalan pertama, 60 dtk per perintah. Klien Android belum memakainya; field ini aditif.
- **Aset:** `assets/*` teks dikirim gzip (dikompres sekali saat diminta pertama), karena worker Monaco berukuran megabyte. Biner hub bertambah sekitar 14 MB.

**Riwayat git** (`CommitGraph.tsx`, `commitGraph.ts`, `CommitView.tsx`). Bagian History di view Source Control menggambar commit di atas lane berwarna, dengan label branch dan tag. Branch lokal dan remote bernama sama jadi satu label. Titik berongga berarti belum ada di remote, cincin menandai yang sedang di-checkout. Tombol *All branches* / *Current branch* memilih cakupan, *Show more* memuat 100 berikutnya sampai 1000. Klik commit membuka satu tab preview yang berisi pesan lengkap, penulis dan committer, hash, induk, jumlah perubahan, daftar file, dan diff file terpilih. Klik berikutnya memakai tab yang sama, klik ganda menyematkannya. Tab commit tidak dipulihkan saat board dibuka lagi. Teks commit ditampilkan sebagai teks biasa.

**Git lanjutan** (`ScmPanel.tsx`, `StashSection.tsx`, `mergeConflicts.ts`). `Status.operation` berisi `merge`, `cherry-pick`, atau `revert` selama operasi itu berjalan; panel menampilkan banner dengan Abort dan Continue. Begitu index berisi sesuatu, panel memisahkan **Staged** dan **Changes**, dan tombol commit hanya mengambil yang staged. Tab diff punya Stage, Revert, dan Unstage per perubahan. Revert masuk ke editor, jadi Ctrl+Z membatalkannya. Konflik diwarnai di editor dengan Accept Current, Incoming, dan Both di atas tiap konflik, lalu Mark as resolved dan Continue. Klik kanan commit di graph berisi checkout, branch, tag, merge, cherry-pick, revert, dan reset; reset hard meminta konfirmasi. Stash menampilkan isinya seperti tab commit.

### 7.3 Language server

| Method | Path | Response / error |
|---|---|---|
| GET | `/api/projects/{id}/lsp` | `{"items":[{"id","name","languages","available","command","install","extra"?,"plugin"?}]}` |
| GET (WebSocket) | `/api/projects/{id}/lsp/{server}/ws` | `404 lsp_not_found` sebelum upgrade; satu pesan JSON-RPC per text frame; close `1011` berisi akhir stderr saat server mati |

Baris katalog dengan `extra: true` (Tailwind CSS) bekerja berdampingan dengan server utama bahasanya dan hanya hidup selama extension-nya terpasang (§7.6). Completion dan hover digabung dari semua server; diagnostics punya pemilik marker per server. Fitur lain tetap dari server utama.

Server Vue versi 3 ke atas tidak membaca `<script>` sendiri. Baris katalog `vue` melaporkan `plugin`, folder paketnya yang berisi `@vue/typescript-plugin`; kosong untuk versi 2, yang masih memakai `hybridMode: false` dan membaca script sendiri. Selama ada `plugin`, membuka file Vue juga menghidupkan `typescript-language-server` dengan plugin itu dan membuka file yang sama di sana; server yang telanjur jalan tanpa plugin dimulai ulang karena plugin hanya dibaca saat start. Notifikasi `tsserver/request` dari server Vue diteruskan klien lewat `workspace/executeCommand` `typescript.tsserverRequest`, dan balasannya kembali sebagai `tsserver/response`, bernilai `null` bila server TypeScript belum siap supaya permintaannya tidak menggantung. Untuk file Vue, signature help, definition, references, highlight, dan rename memakai jawaban pertama yang tidak kosong; code action digabung dari kedua server.

`typescript-language-server` membutuhkan `lib/tsserver.js`, yang tidak ada di TypeScript 7, sehingga perintah install katalog memakai `typescript@6`. Hub menganggapnya `available` hanya bila file itu ada di project atau di sebelah server; kalau tidak, editor tetap memakai layanan bawaan Monaco per file. Project yang `node_modules`-nya berisi TypeScript 7 tetap dilayani TypeScript 6 global. Monaco membaca setelan layanan bawaannya sekali saja, jadi server yang baru mati saat berjalan hanya bisa diganti layanan bawaan dengan memuat ulang halaman.

`gopls` memuat setiap package lewat `go`. Tanpa `go` ia tetap hidup tetapi hanya melaporkan `go command required` untuk setiap file yang dibuka, jadi hub menganggapnya `available: false`, dan `install` baris itu menjadi `sudo apt install golang-go`. Pencarian `go` memakai PATH yang sama dengan server: PATH shell login ditambah folder di bawah. Go yang hanya didaftarkan di `~/.bashrc` pada folder lain tidak terlihat; pindahkan baris PATH-nya ke `~/.profile`.

Hub menjalankan server milik machine project di folder project (lokal atau SSH, kanal non-PTY) dan menjembatani stdio-nya dengan framing `Content-Length`. Umur server sama dengan koneksinya. Tidak ada yang diinstal otomatis; tombol Install di Settings hanya mengetik perintahnya di terminal. `node_modules/.bin` milik project sengaja tidak masuk PATH, tetapi server itu sendiri bisa menjalankan alat project (mis. `go list`, build script Rust). `$HOME/go/bin`, `.cargo/bin`, `.local/bin`, `.npm-global/bin`, lalu folder tarball Go (`/usr/local/go/bin`, `$HOME/.local/go/bin`, `$HOME/.go/bin`) ditambahkan di belakang PATH, juga untuk debug adapter dan ESLint. CSS, HTML, JSON memakai layanan bawaan Monaco; TS/JS jatuh ke layanan bawaan per file bila `typescript-language-server` tidak ada.

Klien (`web/src/studio/code/lsp.ts`) ditulis sendiri karena `monaco-lsp-client` mengunci `rootUri: null` dan mengirim semua model. Fitur: completion, hover, signature help, definition, declaration, type definition, implementation, references, highlight, document dan workspace symbol, rename, code action, formatting, inlay hint, diagnostics, progress.

### 7.4 Edit dengan AI

`POST /api/projects/{id}/code/edit` `{"path","language","instruction","before","selection","after","agent","profileId"}` (≤ 256 KB kode) → `202 Job` purpose `code-edit`, read-only, 3 menit. Hasil `structured.replacement` diperlihatkan sebagai diff; UI baru menerapkannya setelah Accept, dan satu Undo mengembalikannya. Ghost text: `POST /api/projects/{id}/code/complete` `{"path","language","before","after"}` → `202 Job` purpose `code-complete` tanpa tool, 30 dtk, model `haiku` untuk Claude; hasil ada di `result.text`. Mati secara default, bisa diaktifkan hanya untuk Alt+\\ atau saat jeda mengetik, karena tiap saran menjalankan CLI (beberapa detik, memakai kuota). Mengetik lagi membatalkan job-nya.

Tanya kode tanpa console: `POST /api/projects/{id}/code/ask` `{"question","path","language","selection","startLine","endLine"}` → `202 Job` purpose `code-ask`, read-only, streaming `jobOutput`, 5 menit.

### 7.5 Debugger

| Method | Path | Response / error |
|---|---|---|
| GET | `/api/projects/{id}/dap` | `{"items":[{"id","name","types","languages","available","command","install"}]}` |
| GET (WebSocket) | `/api/projects/{id}/dap/{adapter}/ws` | `404 adapter_not_found`, `502 adapter_failed`; satu pesan DAP per text frame |

Katalog: `go` (Delve, `dlv dap --listen`, lewat TCP loopback atau `direct-tcpip`), `python` (debugpy), `node` (adapter bawaan hub, `internal/nodedap`, hanya di This PC), `native` (lldb-dap, lldb-vscode, `gdb --interpreter=dap`), `php` (php-debug-adapter). Hanya memakai adapter yang sudah ada di machine; adapter hidup selama koneksi, dan baru jalan saat pengguna menekan F5. Konfigurasi dibaca dari `.vscode/launch.json` (JSONC, format VS Code) ditambah entri otomatis untuk Go dan Python; variabel `${workspaceFolder}`, `${file}`, `${relativeFile}`, `${fileDirname}`, `${fileBasename}`, `${fileBasenameNoExtension}`. Reverse request `runInTerminal` dan `startDebugging` ditolak, jadi js-debug (Node) belum didukung.

**Node.js** tidak butuh `js-debug`: `nextboard dap-node` menjalankan `node --inspect-brk=127.0.0.1:0` dan menerjemahkan DAP ke inspector Node. Mendukung breakpoint, kondisi, logpoint, step, call stack, variabel, evaluate (hover tanpa efek samping), dan exception. `.ts` jalan selama Node-nya bisa menjalankannya sendiri (Node 24). Belum ada: source map, proses anak, dan attach ke proses yang sudah berjalan. Selama sesi, inspector terbuka di port loopback acak, jadi di host bersama pengguna lain di mesin itu bisa menjangkaunya.

### 7.6 Extension

| Method | Path | Response / error |
|---|---|---|
| GET | `/api/extensions` | `{"items":[Extension]}` |
| GET | `/api/extensions/search?q=` | `{"items":[…],"hidden","unchecked"}` dari Open VSX: hanya yang bisa dipasang di sini, tiap item dengan `hasCode`, `native`, `gives` |
| POST | `/api/extensions` | `{"namespace","name"}` → `201 Extension` (`native` bila editor mengerjakan fungsinya sendiri); `422 extension_code_only`, `400 bad_extension`, `404 extension_not_found`, `502 extension_failed` |
| DELETE | `/api/extensions/{id}` | `{"ok"}` |
| GET | `/api/extensions/{id}/file?path=` | isi file data, `text/plain`, CSP `sandbox` |
| POST | `/api/projects/{id}/eslint` | `{"path","text","fix"?}` (≤ 8 MB) → `{"available","error"?,"messages"?,"output"?}`; `400 bad_path` |

Monaco tidak punya extension host, jadi **kode extension tidak pernah jalan dan tidak disimpan**. Yang dibongkar hanya file data (`.json`, `.jsonc`, `.code-snippets`, `.tmLanguage`, `.tmTheme`, `.plist`, `.tmSnippet`, `.svg`) ke `<data>/extensions/<namespace>.<name>/`, paket ≤ 100 MB, isi ≤ 64 MB. Yang dipakai: tema warna, tema ikon file (SVG lewat `<img>`, jadi skripnya tidak jalan; ikon berbasis font atau PNG belum), snippet, grammar TextMate (`vscode-textmate` + Oniguruma WASM), definisi bahasa. Paket yang hanya berisi kode ditolak, kecuali yang fungsinya dikerjakan editor sendiri selama extension-nya terpasang (tabel `extNative` di `internal/studio/extensions.go`):

| Extension | `native` | Yang dikerjakan editor |
|---|---|---|
| Vim (`vscodevim.vim`) | `vim` | `monaco-vim` di tiap editor, baris status di bawah editor, `:w :q :wq :x`. Shortcut workbench (Ctrl+P, Ctrl+Shift+P, Ctrl+S) tetap menang di normal mode. Pengaturan `vim.*` dan plugin Vim tidak ada |
| Prettier (`esbenp.prettier-vscode`) | `prettier` | Prettier asli di browser (`prettier/standalone`) sebagai satu-satunya formatter bahasa yang dikenalnya (selector `exclusive`). Membaca `.prettierrc` (JSON, YAML, TOML datar), `package.json#prettier`, `overrides`, `.editorconfig`, `.prettierignore`. Config JavaScript dan plugin pihak ketiga adalah kode project, jadi tidak dijalankan |
| ESLint (`dbaeumer.vscode-eslint`) | `eslint` | Hub menjalankan `node_modules/.bin/eslint` terdekat dari file, atau `eslint` milik machine, dengan isi editor lewat stdin. Marker, quick fix, nonaktifkan rule per baris, fix all. Config ESLint adalah kode, jadi **baru jalan setelah pengguna mengizinkan per project** (status bar atau palette, disimpan di browser) |
| GitLens (`eamodio.gitlens`) | `gitlens` | Blame baris aktif dan hover commit (teks commit di-escape, hanya satu command yang dipercaya), CodeLens penulis di atas file, blame seluruh file, riwayat file dan baris, diff per commit. Commit graph, Launchpad, worktree, dan fitur Pro tidak ada |
| Error Lens (`usernamehw.errorlens`) | `errorlens` | Pesan masalah terparah di akhir baris dan baris diwarnai, dari semua marker |
| Python, Go, rust-analyzer, Tailwind CSS, Vue, Even Better TOML | `lsp:<id>` | IntelliSense dari language server katalog §7.3 di machine project; grammar dan snippet dari paketnya. Panel Extensions menunjukkan status server dan tombol Install yang mengetik perintahnya di terminal. Vue memakai `@vue/language-server@2` dengan `hybridMode: false`. Fitur yang butuh kode extension (run test dari CodeLens rust-analyzer, pemilih venv Python, perintah `Go:`) tidak ada |

Material Icon Theme tidak perlu baris sendiri: tema ikon adalah data, jadi tema ikon SVG lain ikut bekerja.

Hasil pencarian Open VSX tidak menyebut isi paket, jadi hub meminta 50 hit, membaca `package.json` tiap hit (di sebelah `.vsix`, 8 sekaligus, batas total 25 dtk, hasilnya diingat per versi selama hub hidup), dan menampilkan paling banyak 24. Yang tampil hanya yang fungsi utamanya benar-benar jalan: ada di `extNative`, paket tanpa kode yang menyatakan tema, tema ikon, snippet, atau grammar ber-`language` dengan file data, atau paket berkategori `Themes` yang kodenya hanya tambahan. Paket bahasa atau alat yang fungsi utamanya ada di kode tetap disembunyikan walau grammar-nya bisa dipasang (`POST /api/extensions` masih menerimanya), dan begitu juga paket > 100 MB (`HEAD`, tanpa mengunduh). `hidden` menghitung yang disembunyikan, `unchecked` yang manifest-nya gagal dibaca; bila semua gagal, responsnya `502 extension_failed`. Paket yang manifest-nya menjanjikan file yang ternyata tidak ada baru diketahui saat dipasang (`422 extension_code_only`). Grammar tanpa `language` (injection) tidak dihitung, juga saat pasang, karena Monaco tidak bisa memakainya. Tema ikon berbasis font atau PNG lolos dari saringan dan menolak dipakai dengan pesan saat dipilih.

### 7.7 Card di board

Workbench adalah node `editor` (`board/tiles/EditorTile.tsx`), bukan overlay: satu card per board, tab dan edit belum tersimpan milik project (localStorage dan IndexedDB), bukan milik card. Tombol Fill membesarkan card selebar tampilan pada zoom 100 % dan mengembalikannya. Wire `editor → terminal | group | orchestrator` menentukan tujuan pertanyaan dari panel Ask; `POST /board/ops` memvalidasinya. Klien lain yang belum mengenal kind ini menampilkan tile unsupported.

### 7.8 Agent di dalam editor

Console agent dan orchestrator bisa dibuka di dalam card editor, tanpa tile di board. Kode: `web/src/studio/code/AgentPanes.tsx`, `Workspace.placePane` dan kawan-kawannya.

- **Tampilan orchestrator:** satu kolom selebar paling banyak 800 px di tengah pane, jadi tetap terbaca di tab lebar dan di side bar. Header menyebut berapa console yang mendengar thread ini; pilihan thread hanya muncul bila board punya kartu orchestrator. Thread kosong menawarkan tiga saran perintah, yang mengikuti file terbuka bila ada; klik hanya mengisi kotak perintah, tidak mengirim. Console yang berjalan tampil sebagai chip di atas kotak perintah; klik menyisipkan `@handle`.

- **Tempat:** side bar kanan (tombol di title bar, `Ctrl+Alt+B`, lebarnya bisa diubah) atau tab editor (ikon agent di tab strip, ikut split). Satu pane hanya di satu tempat: tombol di side bar memindahkannya ke tab, menu tab memindahkannya kembali. Palette: *Agents: Open … in the Side Bar / in a New Tab*. Tata letaknya tersimpan per browser seperti tab file.
- **Console:** `POST /api/sessions` dengan `panel: true` dan agent dari katalog machine project. Sesi biasa tanpa node board: status, needs-you, chip live, dan koneksi IDE Claude (`docs/API-agents.md` §24) tetap berlaku. "Show" pada needs-you dan `?focus=<id>` membuka card lalu memunculkan pane-nya (`agents/jump.ts`). Agent editor yang tidak punya pane di browser ini, misalnya dibuka dari browser lain, ditawarkan sebagai *Show @nama*.
- **Menutup:** tombol tutup console meminta konfirmasi lalu menghapus sesinya, percakapannya tetap tersimpan oleh CLI. Menyembunyikan side bar tidak menghentikan apa pun. Close Others dan Close All melewati pane.
- **Orchestrator:** thread project atau thread kartu orchestrator di board, dengan input `@mention` dan konfirmasi yang sama seperti command bar. File atau teks yang diblok ikut sebagai konteks order, dan chip-nya bisa dibuang sebelum dikirim. Pilihan thread diingat per board di browser ini, jadi tetap sama setelah keluar dari board atau reload; kalau kartunya sudah dihapus, kembali ke thread project.
- **Order ke agent editor:** scope orchestrator kini mencakup sesi `panel` yang berupa agent. Terminal panel bawah tetap shell milik pengguna dan tidak menerima order. Kartu orchestrator di board hanya mengarahkan console yang diwire, jadi tidak menjangkau agent editor.
- **Alt+K:** console yang tertaut IDE menerima `@file#Lx-y`; agent lain di editor (misalnya Codex) menerima `path:x-y` sebagai teks di prompt-nya, tanpa dikirim.
- **Board dihapus:** semua sesi `panel` di board itu ikut berhenti (`Manager.AllPlacements`). Sebelumnya terminal panel tertinggal berjalan.

**Verifikasi (This PC, 2026-11-03):** `e2e-panes` di hub tes terisolasi dengan CLI Claude 2.1.317 asli lawan server model palsu, dan Codex asli sampai layar login karena tanpa kredensial. Belum diuji: project di server lewat SSH, Claude yang login dengan model sungguhan, dan Codex yang benar-benar menjawab.

### 7.9 Paste file dan gambar

Berlaku untuk console (tile dan panel terminal workbench), input order (kartu orchestrator, command bar, panel agent), dan editor. Teks biasa tetap jalan seperti sebelumnya.

| Method | Path | Body | Respons |
|---|---|---|---|
| POST | `/api/sessions/{id}/attach?name=` | byte mentah (maks 20 MB) | `Stat` di machine tempat console berjalan |
| POST | `/api/projects/{id}/attach?name=` | byte mentah (maks 20 MB) | `Stat` di This PC, tempat agent berjalan |
| GET | `/api/projects/{id}/attach/{name}` | — | byte file itu, untuk thread. `Content-Security-Policy: sandbox`; hanya nama yang bisa diberikan hub, di folder project itu, tanpa mengikuti symlink keluar. File API biasa tetap tertutup untuk folder data hub. |

`400` file kosong, `404` session/project tidak ada, `413` lebih dari 20 MB.

- **Lokasi dipilih hub**, klien hanya mengirim nama: tidak ada path yang bisa diarahkan ke file hub atau ke project, dan project tetap bersih dari file sementara. Nama dibersihkan (bagian terakhir saja, spasi dan tanda baca jadi `-`, tanpa awalan `.`/`-`, ≤ 80 karakter), jadi tidak perlu dikutip di CLI mana pun. Nama yang sudah ada tidak ditimpa (`nama-2.ext`).
- **This PC:** `<data>/attachments/<sessionId>/` atau `<data>/attachments/project-<projectId>/`, folder `0700`. Disimpan 7 hari (percakapan yang di-resume bisa masih menunjuknya); disapu saat hub start dan tiap paste, folder console ikut hilang saat console dihapus.
- **Server (keputusan #2):** `/tmp/nb-paste-<uid>-<sessionId>/`, satu path tetap per console seperti folder usage: dibuat `mkdir -m 700` hanya sebagai folder asli milik user, symlink atau folder orang lain ditolak. Hilang bersama console; folder milik console yang dihapus saat server tidak terjangkau disapu saat server tersambung lagi. Tidak perlu catatan apa pun untuk hub yang crash, karena daftar console hidup sudah cukup.
- **Yang diterima order** (klien): chip di atas input (DESIGN §3.13). Hub tidak punya field lampiran: saat kirim, path ditambahkan di akhir teks order dipisah spasi, karena path itulah yang dibaca agent. Thread mengenali path ini di teks (`…/attachments/project-<id>/<nama>`, satu kata utuh) dan menampilkannya sebagai lampiran; *Copy* tetap menyalin teks aslinya. Antrean delivery dan plan menampilkan teks apa adanya.
- **Yang diterima console** (klien): path dipaste lewat `term.paste` (bracketed paste bila program memintanya) lalu spasi, satu per satu dengan jeda 250 ms, karena CLI membaca gambar dulu sebelum menampilkannya. Gemini memakai `@path`; yang lain path, dikutip bila perlu.
- **Ctrl+V di console** (bukan macOS) kini jadi paste browser, karena hanya event paste yang menunjukkan isi clipboard: ada file → seperti di atas; teks di agent CLI → teks (CLI sendiri hanya melihat clipboard This PC, jadi selama ini gagal: "no image over SSH"); teks di shell, atau clipboard kosong → tetap `^V`, karena vim dan readline memakainya. Paste teks di shell tetap Ctrl+Shift+V.
- **Claude Code** di This PC dijalankan dengan `--add-dir <data>/attachments`, supaya file yang dipaste (dan path dalam order) bisa dibaca tanpa dialog izin. CLI lain tidak diberi flag ini; bila CLI-nya bertanya, izinnya lewat console seperti biasa.
- **Input order:** path masuk ke teks di posisi caret saat upload selesai, jadi bisa dibaca dan diedit; Enter dan Send menunggu upload selesai. Agent menerima path itu dalam order dan membacanya sendiri.
- **Clipboard mana yang file:** teks plus gambar dari spreadsheet dianggap teks; file plus namanya dari file manager dianggap file; folder ditolak dengan pesan.

**Explorer (Code workbench):** Ctrl+C, Ctrl+X, Ctrl+V dan menu konteks untuk file dan folder, seperti VS Code. Yang dicopy disimpan per tab browser, jadi bisa pindah antar project di machine yang sama (antar-machine ditolak dengan pesan); path-nya juga masuk clipboard, jadi Ctrl+V di console atau order menghasilkan path. Paste masuk ke folder terpilih atau folder dari file terpilih; folder yang dipaste ke dirinya sendiri mendarat di sebelahnya; cut ke dirinya sendiri ditolak; Esc membatalkan cut. Ctrl+V juga menerima file dari komputer (`fs/write` base64, `unique`). Paste file saat mengetik di editor menaruhnya di sebelah file itu dan menyisipkan namanya; di Markdown jadi `![nama](nama)` atau `[nama](nama)`. Klik sekali pada file (preview) tidak lagi memindahkan fokus ke editor, supaya Ctrl+C setelah klik berarti file-nya, seperti VS Code; double click dan Enter tetap pindah.

`fs/copy` memakai `cp -a` milik machine itu sendiri (lokal dan SSH), jadi tidak ada byte yang lewat hub, link, mode dan waktu ikut. Tidak pernah menimpa; `cp` yang gagal tidak meninggalkan salinan setengah jadi; batas waktu 5 menit.

**Verifikasi (2026-11-03):** Chrome sungguhan dengan clipboard sungguhan. Claude Code 2.1.317 dan Codex 0.165.0 asli menampilkan `[Image #n]` untuk gambar dan path untuk file lain; Claude Code menerima `--add-dir` dan benar-benar membaca file di folder itu. Paste dan `fs/copy` lewat sshd sungguhan. **Belum diverifikasi:** `@path` Gemini (CLI tidak terpasang), copy file dari file manager sungguhan (diuji dengan event paste berisi file; browser di Linux bisa saja hanya memberi daftar URI), macOS.

### 7.10 Review perubahan agent

Panel **Review** menampilkan file yang diubah tiap agent dan belum ditinjau. Mekanismenya repo bayangan per work tree di `<data>/review/<projectId>/`: repo asli tidak disentuh, folder tanpa git ikut, dan edit yang tidak dilaporkan hook (misalnya `sed` dari tool shell) tetap terlihat. Hanya project di This PC; selain itu `supported:false`.

- **Snapshot:** saat agent mulai dan selesai bekerja, tiap 3 dtk selama bekerja, dan setelah tiap simpan dari editor. Mengikuti `.gitignore` work tree, melewati `node_modules` dan sejenisnya, file di atas 10 MB, dan maksimal 20000 path per snapshot.
- **Atribusi:** hook `Edit`, `Write`, `MultiEdit`, dan `NotebookEdit` menentukan pemiliknya. Tanpa hook, perubahan diberikan ke agent yang bekerja tanpa hook (Codex), lalu ke yang punya hook. Lebih dari satu kandidat ditandai `unsure`. Yang disimpan pemilik dari editor bukan milik agent, dan perubahan saat tidak ada agent bekerja juga bukan.
- **Dua agent:** tiap agent dibandingkan dengan isi yang ia temukan. Satu file milik dua agent memberi `also` dan satu event `reviewOverlap`.

| Method | Path | Request | Response / error |
|---|---|---|---|
| GET | `/api/projects/{id}/review` | — | `{"supported","agents":[{"sessionId","name","kind","working","files":[{"path","add","del","status","binary"?,"unsure"?,"also"?,"since"}]}]}` |
| GET | `/api/projects/{id}/review/file?session=&path=` | — | `{"path","base","baseExists","current","currentExists","binary","tooBig"}`; `404 no_change` |
| POST | `/api/projects/{id}/review/accept` | `{"session","path"?,"base"?}` | Tanpa `path` semua file agent itu. Dengan `base`, hanya sebagian yang diterima dan review berlanjut dari teks itu |
| POST | `/api/projects/{id}/review/reject` | `{"session","path"?,"text"?}` | File kembali ke isi yang ditemukan agent; isi sekarang masuk Local History dulu. Dengan `text`, hanya sebagian yang dibatalkan |

Event `review` memberi tahu klien untuk memuat ulang. Pending bertahan saat hub restart. Batasan: file besar dan folder yang di-exclude tidak ditinjau; perubahan Bash oleh dua agent sekaligus hanya bisa ditebak (`unsure`); project di server SSH belum didukung.

**Folder home tidak ikut:** project yang menunjuk folder home atau induknya (termasuk project bawaan "Home") tidak punya review dan Local History (`supported:false`). Kalau ikut, snapshot menyalin `~/.ssh`, semua login, dan project lain ke repo bayangan.

### 7.11 Local History

Riwayat tiap file dari repo bayangan yang sama, siapa pun yang menulisnya: tab **Local History** dari menu tab, Explorer, editor, atau palette. Snapshot terjadi seperti §7.10, jadi perubahan di luar editor tercatat pada snapshot berikutnya, atau saat riwayat dibuka. Disimpan 30 hari atau 2000 snapshot per work tree, dipangkas sekali sehari; base review yang masih pending tidak ikut terhapus.

| Method | Path | Response / error |
|---|---|---|
| GET | `/api/projects/{id}/history?path=&limit=` | `{"items":[{"commit","at","who","exists"}]}`, terbaru dulu; `400 bad_path` |
| GET | `/api/projects/{id}/history/version?path=&commit=` | `{"content","binary"}`; `404 no_version` |

**Restore** memasukkan teks ke editor kalau file sedang terbuka, sebagai satu langkah yang bisa dibatalkan Ctrl+Z dan belum tersimpan ke disk; kalau tidak terbuka, teks langsung ditulis ke file.

### 7.12 Tasks dan tes

Tab **Tasks** di panel bawah berisi yang bisa dijalankan menurut file project: skrip `package.json` (npm, pnpm, yarn, bun menurut lockfile), target `Makefile`, resep `justfile`, `go.mod`, `Cargo.toml`, pytest, dan skrip `composer.json`, di folder itu dan satu tingkat di bawahnya. Output tampil langsung. Lokasi error dan tes yang gagal dibaca dari output: klik membuka barisnya, dan tugas yang gagal bisa dikirim ke agent. Palette: `Tasks: Run Task…`, `Tasks: Run Tests`.

| Method | Path | Response / error |
|---|---|---|
| GET | `/api/projects/{id}/tasks` | `{"items":[{"id","group","name","kind","command","cwd"}]}` |
| POST | `/api/projects/{id}/tasks/run` | `{"id"}` → `202 Job` (`kind:"task"`, output lewat `jobOutput`, berhenti lewat `POST /api/jobs/{id}/cancel`, maksimal 30 menit). `404 task_not_found` |

Yang berjalan hanya task yang disebut file project saat itu; hub tidak menerima perintah dari klien.

### 7.13 Ports dan bookmark

Tab **Ports** menampilkan port TCP yang terbuka di machine project, diperbarui tiap 5 dtk: `GET /api/machines/{id}/listening` → `{"items":[{"port","address","process","pid"}]}` (`ss`, atau `lsof` di luar Linux). Port milik pengguna lain disembunyikan kecuali dicentang. Tiap port bisa dibuka sebagai web tile.

**Bookmark** tersimpan per project di browser dan mengikuti barisnya saat file diedit: `Ctrl+Alt+K` memasang atau melepas, `Ctrl+Alt+L` dan `Ctrl+Alt+J` berpindah, `Bookmarks: List` menampilkan semuanya.

**`@file` di Ask:** `@` mengusulkan file project. Untuk Quick answer, isi file ikut terkirim (maksimal 5 file, 20 KB per file) tanpa tampil di percakapan; agent yang punya folder project membaca sendiri dari path-nya.

**Commit graph satu tab:** tombol *Open graph* atau palette `Git: Open Commit Graph`, dengan kolom pesan, author, hash, tanggal, dan filter yang meredupkan baris lain tanpa memutus lane.

## 8. Markdown studio dan link graph (E5)

`GET /api/projects/{id}/markdown?refresh=0` →

```jsonc
{ "files": [{ "path": "/home/alice/app/docs/x.md", "rel": "docs/x.md", "size": 1200, "mtime": "…", "title": "X" }],
  "links": [{ "from": "docs/x.md", "to": "docs/y.md", "kind": "wiki"|"md", "resolved": true }],
  "agentMemory": { "dir": "/home/alice/.claude/projects/-home-alice-app/memory", "files": [ … ] },
  "truncated": false }
```

- File `.md .markdown .mdx` (prune §7 + folder tersembunyi), ≤ 2000. Link diekstrak hub: `[[nama|alias#heading]]` (cocok basename, case-insensitive) dan `[teks](rel.md)`; file ≤ 256 KB, anggaran 20 MB / 20 dtk; cache per mtime.
- Agent memory Claude per project ditampilkan pertama. Menulis ke sana di machine SSH mengikuti protokol konfirmasi §9.2 (berada di `~/.claude`).
- Klien: file terbuka di-poll `fs/stat-batch` tiap 3 dtk (tanpa edit lokal → muat ulang otomatis, dengan edit → tanya); mode Source autosave 1,5 dtk setelah berhenti mengetik (`fs/write` + `ifMtime`); graph `d3-force` (link wiki membentuk graph, node yatim tampil); PDF lewat rute cetak §5 (`/print/markdown?machineId=&path=`, gambar repo via `fs/raw`). Kartu markdown live Fase 4 memakai polling yang sama.

### 8.1 Implementasi hub (Fase 7 bagian B) — deviasi dan verifikasi

`internal/studio/markdown.go`. Respons menambah `scannedAt`; link membawa `heading` bila ada. Judul = `title:` frontmatter → `# heading` pertama → nama file. Link di blok kode/inline code dan skema eksternal (`https:` dll.) diabaikan; `[t](a%20b.md#x?y)` didekode. Wiki: path relatif bila berisi `/`, selain itu basename (duplikat: urutan path pertama menang). Cache per (machine, root) di memori per (size, mtime); anggaran habis → `truncated` dan file tidak di-cache.

- `agentMemory.dir` = `<cfg>/projects/<ClaudeSlug(path project)>/memory` dengan `<cfg>` = profil Claude default machine atau `~/.claude` (layout dicek read-only di This PC: `MEMORY.md` + file fakta; `MEMORY.md` di urutan pertama).
- **Ekstensi:** `GET /api/projects/{id}/markdown/graph` → `{"nodes":[{id,title,in,out,orphan}],"edges":[{from,to,kind,count}],"unresolved","truncated"}` (link wiki dan md yang ter-resolve, dedupe, node yatim ikut); `GET /api/projects/{id}/markdown/backlinks?path=<rel>` → `{"path","backlinks":[{from,title,kind,heading}],"outgoing":[Link…]}` (`404 not_in_vault`). Live: polling klien `fs/stat-batch` atau watch hub §7.1 (perubahan tampil ≤ 3 dtk, diuji).

## 9. Skills manager (E6, keputusan #3)

### 9.1 Lokasi dan model

| scope | Path (`<cfg>` = folder profil atau `~/.claude` di machine itu) | Tulis |
|---|---|---|
| `personal` | `<cfg>/skills/<name>/SKILL.md` (satu level subfolder) | SSH: konfirmasi §9.2 |
| `project` | `<repo>/.claude/skills/<name>/SKILL.md` | biasa (repo) |
| `personalCommand` | `<cfg>/commands/*.md` | SSH: konfirmasi §9.2 |
| `projectCommand` | `<repo>/.claude/commands/*.md` | biasa |
| `plugin` | skill & command dari plugin terpasang di `<cfg>/plugins/` | read-only (`403 read_only`) |

```jsonc
{ "id": "personal:commit-message", "scope": "personal", "kind": "skill"|"command", "name": "commit-message",
  "path": "/home/alice/.claude/skills/commit-message/SKILL.md", "description": "…", "invoke": "/commit-message",
  "argumentHint"?: "[scope]", "modelInvocable": true, "subagent": false, "model"?: "…", "allowedTools"?: ["Bash(git:*)"],
  "tokensEstimate": 820, "siblings": ["reference.md"], "shadowedBy"?: "project:commit-message", "mtime": "…",
  "lint": [{ "rule": "name-format", "severity": "warn", "message": "…" }] }
```

Shadowing: personal mengalahkan project; skill mengalahkan command bernama sama. Lint lokal (tanpa AI): nama bukan `[a-z0-9-]` atau > 64; nama ≠ folder; deskripsi kosong atau terlalu panjang; body kosong atau > 500 baris; key frontmatter tak dikenal; `<`/`>` di nilai frontmatter.

### 9.2 Protokol konfirmasi tulis remote

Untuk machine SSH, setiap tulis/hapus yang menyentuh `<cfg>` wajib membawa `"confirm": {"paths": [...]}` yang **persis sama** dengan himpunan path (file dan folder baru) yang dihitung hub. Tanpa itu → `428 {"error":"confirmation required","code":"confirm_required","machine":"devbox","paths":[…]}`. UI menampilkan nama machine dan daftar path, tombol `Write to devbox` / `Cancel`. Sebelum hapus/timpa, hub menyalin versi lama ke `<data>/backups/skills/<machineId>/<ts>/` (di hub, bukan di remote).

| Method | Path | Request | Response / error |
|---|---|---|---|
| GET | `/api/machines/{id}/skills?projectId=&profileId=` | — | `{"items":[Skill…],"truncated"}` (≤ 400, tanpa body) |
| GET | `/api/machines/{id}/skills/item?path=` | — | `{"skill","content"}` |
| POST | `/api/machines/{id}/skills/lint` | `{"content","path"?}` | `{"lint":[…]}` |
| PUT | `/api/machines/{id}/skills/item` | `{"path","content","ifMtime"?,"confirm"?}` | `Skill`; `428`, `403 read_only`, `409 mtime_mismatch` |
| POST | `/api/machines/{id}/skills` | `{"scope","kind","name","description","argumentHint"?,"body","projectId"?,"confirm"?}` | `201 Skill` |
| POST | `/api/machines/{id}/skills/import` | `{"fromMachineId","fromPath","scope","projectId"?,"confirm"?}` | `201 Skill` (folder ber-`SKILL.md`, ≤ 50 file, ≤ 5 MB) |
| POST | `/api/machines/{id}/skills/duplicate` | `{"path","toScope","projectId"?,"confirm"?}` | `201 Skill` |
| DELETE | `/api/machines/{id}/skills/item` | `{"path","confirm"?}` (body) | `{"ok":true,"backup":"…"}` |
| GET | `/api/skills/starters` | — | starter NextBoard: `/commit-message`, `/pr-summary`, `/release-notes`, `/write-tests`, `/find-bugs`, `/plan-refactor`, `/document-api`, `/daily-update`, `/explain-code`, `/speed-check`, `/audit-deps`, `/update-readme` |
| POST | `/api/sessions/{id}/skill` | `{"name","args"?}` | `202 Delivery` (`/name args` lewat delivery engine; `409 not_agent`) |

### 9.3 Implementasi hub (Fase 7 bagian B) — deviasi dan verifikasi

`internal/skills` (frontmatter, lint, scan, shadowing, protokol konfirmasi, backup, starter), endpoint di `internal/server/h_skills.go`.

- `<cfg>` = `profileId` → profil Claude default machine (`settings.agents.profiles[m].claude`) → `~/.claude` (absolut); respons daftar menambah `cfg`. Plugin dibaca dari `<cfg>/plugins/installed_plugins.json` (`installPath`, Claude Code 2.x): `<install>/skills/*/SKILL.md` dan `<install>/commands/*.md`, id `plugin:<plugin>:<name>`, invoke `/<plugin>:<name>`, read-only.
- Shadowing: skill personal > skill project > command personal > command project. Lint (id aturan): `frontmatter-missing`, `name-missing`, `name-format` (nama frontmatter **dan** folder), `name-folder`, `description-missing`, `description-length` (> 1024, atau + `when_to_use` > 1536), `body-empty`, `body-length` (> 500 baris), `unknown-key`, `angle-brackets`.
- **Himpunan konfirmasi** (path absolut, dibandingkan sebagai himpunan): buat = folder baru + file; edit = `[file]`; hapus = folder skill + subfolder + semua file (folder symlink: hanya link-nya, target tidak disentuh); import/duplicate = folder baru + file. Hub tidak pernah membuat `<cfg>` sendiri (keputusan #4), boleh membuat `<cfg>/skills|commands`; `.claude` di repo boleh dibuat. Body `428` persis kontrak (`machine` = nama machine).
- Backup ke `<data>/backups/skills/<machineId>/<YYYYMMDD-HHMMSS.mmm>/<path relatif terhadap cfg atau repo/.claude>` sebelum **setiap** timpa/hapus (juga This PC); hapus = hapus (bukan trash) setelah backup. Batas: isi ≤ 512 KB, hapus ≤ 200 file, import 50 file / 5 MB.
- Kode error: `403 read_only`, `400 not_skill|bad_name|bad_scope|no_skill_file`, `409 exists|mtime_mismatch`, `413 too_large`, `501 no_sftp`, `428 confirm_required`. Starter menambah `title` dan `content` (SKILL.md siap tulis). `POST /api/sessions/{id}/skill`: nama `[A-Za-z0-9_.-]` (+ `:plugin`), args satu baris ≤ 2000, delivery `source:"skill"`.
- Tes: lokal (`internal/skills`), SSH dengan sshd sekali pakai (`internal/machine/studio_ssh_test.go`: tanpa `confirm` → `428` + daftar path, beda satu path → `428`, backup ada di hub sebelum timpa/hapus, plugin read-only, tulis project tanpa konfirmasi, `<cfg>` yang tidak ada tidak dibuat). Semua `<cfg>` uji = folder temp; `~/.claude/skills` tidak disentuh.

## 10. Usage (F2–F4, keputusan #2)

### 10.1 Usage pill

```jsonc
{ "machineId": "local", "agent": "claude", "profileId": "", "checkedAt": "…", "trigger": "background"|"user",
  "state": "ok"|"needsInteractive"|"notLoggedIn"|"unsupported"|"error",
  "windows": [{ "id": "session", "label": "Current session", "usedPct": 42, "resetsAt": "…"|null, "resetsText": "Resets 6pm" }],
  "facts": [{ "label": "Total tokens", "value": "12.3M" }],
  "raw": ["…baris layar…"], "parsed": true, "error": "" }
```

Prosedur pemeriksaan (job `usage`):

1. Exec di machine: `d=/tmp/nb-usage-$(id -u)` — **satu path tetap per user machine** (keputusan lead, menggantikan `mktemp`, supaya Claude Code hanya mencatat satu entri trust folder). `$d` symlink atau bukan folder milik user → gagal (tidak dipakai, tidak dihapus); sisa folder milik user (hub crash) dihapus dulu; lalu `mkdir -m 700 -- "$d"` (gagal bila ada lagi di antaranya); dicatat di `usage/pending-cleanup.json` sebelum CLI jalan. Karena path tetap, hanya satu pemeriksaan per machine sekaligus.
2. Sesi PTY tersembunyi (tidak di `sessions.json`, tidak di board), 120×40, cwd `$d`, env profil, **tanpa** hooks/yolo; model VT mode `respond` (membalas DSR/DA).
3. Tunggu prompt siap ≤ 30 dtk. Prompt trust-folder untuk `$d` dijawab "trust" (folder kosong milik hub); prompt update dilewati; prompt login → `notLoggedIn`; prompt lain → `needsInteractive`.
4. Ketik perintah usage + Enter: Claude `/usage`, Codex `/status`, Gemini `/stats`; agen lain `unsupported`. Tunggu layar stabil 1,5 dtk (≤ 15 dtk).
5. Snapshot layar → parser per CLI (regex persen `(\d{1,3})%\s*used`, teks reset; `resetsAt` best-effort, `null` bila format tidak dikenal). Gagal parse → `parsed:false` + `raw`.
6. Keluar (Claude: Esc lalu `/exit`; Codex: `/quit`; kill setelah 3 dtk), exec `rm -rf -- "$d"` setelah memvalidasi `^/tmp/nb-usage-[0-9]+$` (di Go) dan lagi di shell (`case`, `[ -d "$d" ] && [ -O "$d" ]`, bukan symlink), hapus dari `pending-cleanup.json`. Juga saat job dibatalkan atau gagal. Saat hub start, sisa di file itu dibersihkan per machine begitu tersambung (hanya path yang tercatat dan lolos validasi). Tidak ada pemindaian `/tmp` lain.

| Method | Path | Request | Response |
|---|---|---|---|
| GET | `/api/usage?machineId=&agent=&profileId=` | — | `{"items":[UsageReport…]}` (cache) |
| POST | `/api/usage/check` | `{"machineId","agent","profileId"?,"maxAgeSec"?: 180,"trigger"?: "background"}` | cache cukup baru → `200 UsageReport`; selain itu `202 Job`. ≤ 1 per (machine, agen, profil), ≤ 2 per hub; machine terputus → `503 machine_offline` |

Klien meminta `maxAgeSec:180` saat membuka board, `30` saat popover dibuka, `0` untuk "Check again". Warna bar: amber ≥ 75 %, merah ≥ 90 %. `settings.usage.background` (default **false**) mengizinkan pemeriksaan latar; `settings.usage.agents` mematikan pemeriksaan per CLI untuk semua trigger dan semua perangkat (default `{"codex": false}`; key yang tidak ada = aktif). Ini satu-satunya sakelar per CLI: klien web mengubahnya di Settings › Usage dan lewat tombol "Turn on and check" di popover, dan tidak menyimpan pilihan CLI per browser. Alasan default: tiap pemeriksaan Claude menambah 2 baris ke `history.jsonl` login yang dipakai, pemeriksaan Codex membuat Codex menulis blok `[tui]` ke `config.toml` login yang dipakai bila belum ada. Klien wajib mengirim `trigger:"user"` untuk klik pengguna; tanpa `trigger` hub menganggapnya `background`.

### 10.2 Chime reset (F3)

`settings.usage.chime {enabled:true, play:"after100"|"always", sound:"bell"|"chime"|"pop"|"soft"}` (suara bundel NextBoard). Hub memasang timer di `resetsAt + 30 dtk` tiap window (dari `usage/reports.json`, dihitung ulang saat start); syarat `play` terpenuhi → event `usageReset {machineId, agent, profileId, window, at}` → web memutar suara (tab terbuka), Android notifikasi "Claude usage reset", desktop notifikasi native.

### 10.3 Grafik token (F4)

`GET /api/usage/tokens?days=30&machineId=&agent=` →

```jsonc
{ "from": "2026-09-29", "to": "2026-10-28",
  "series": [{ "date": "2026-10-28", "agent": "claude", "input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0 }],
  "byAgent": [{ "agent": "claude", "days30": { "total": 0, "output": 0 }, "allTime": { "total": 0 },
                "sessions": 442, "favoriteModel": "claude-opus-5-5", "longestSessionSec": 4063924 }],
  "busiestDay": { "date": "…", "total": 0 }, "top": "claude", "scannedAt": "…", "partial": false }
```

Indeks inkremental per machine `usage/tokens/<machineId>.json` (`files: {path: {size, offset, mtime}}`, agregat per hari per agen): Claude `<cfg>/projects/*/*.jsonl`, Codex `<home>/sessions/**/rollout-*.jsonl`, untuk login default dan semua profil machine itu; hanya byte baru sejak `offset` yang dibaca (`fs/read?offset=`); dedupe Claude per `message.id` (ingat 200 id terakhir per file). Anggaran 30 dtk per request (`partial:true` bila habis); refresh latar tiap 30 menit untuk machine tersambung. Tanggal menurut zona waktu hub. UI menyatakan ini bukan tagihan.

Statistik per CLI ikut tersimpan di indeks dan tetap ada walau transcript-nya sudah dihapus CLI: `sessions` = transcript dengan minimal satu giliran; `favoriteModel` = model dengan token terbanyak (Claude `message.model`, Codex `turn_context`); `longestSessionSec` = giliran pertama sampai terakhir dalam satu transcript, jadi sesi yang dilanjutkan berhari-hari terhitung panjang. Indeks lama diisi lewat pembacaan kedua sekali saja (`statOffset`), tanpa menghitung token dua kali. Scan pertama di This PC: 2,2 GB transcript, 7,4 dtk. Popover usage meminta `days=366&agent=…` untuk heatmap.

### 10.4 Implementasi hub dan verifikasi (Fase 7 bagian B)

`internal/usage` (folder tetap, driver PTY tersembunyi, parser, chime, indeks token), job lewat `jobs.SubmitFunc` (baru, `internal/jobs/func.go`: job fungsi Go di luar antrean runner, kind `usage`, purpose `usage`, `result` = `UsageReport`).

- Antrean: ≤ 1 pemeriksaan per **machine** (path tetap) dan ≤ 2 per hub; request untuk (machine, agen, profil) yang sedang jalan mendapat job yang sama. `profileId:""` = login default, yaitu profil default machine itu bila ada (klien tidak menampilkan chip kedua untuk profil tersebut), profil lain harus cocok machine+agen (`400 profile_mismatch`). Agen tanpa pemeriksaan → `200` report `unsupported` tanpa job. `trigger:"background"` saat `settings.usage.background:false` → `409 background_disabled`. Agen yang dimatikan di `settings.usage.agents` → `409 usage_disabled` (trigger apa pun). Layar tak dikenal → `state:"error"`, `parsed:false`, `raw`. Login → `notLoggedIn`; dialog lain/prompt tidak siap 30 dtk → `needsInteractive` (tanpa menjawab apa pun). CLI keluar sebelum prompt siap → `state:"error"`, `error` memuat akhir layarnya (misalnya Codex tanpa jaringan: `account/read failed…`).
- Driver: tombol diketik per karakter (jeda 25 ms, bukan paste); trust Claude dijawab dengan ↓ lalu Enter **hanya** setelah kursor terlihat di "Yes, I trust this folder" (default dialog = "No, exit"); dialog update hanya di-Esc (tidak pernah Enter); env Claude `DISABLE_AUTOUPDATER=1`; Codex `-c check_for_update_on_startup=false -c projects."<d>".trust_level="trusted"`. Selama Codex mencetak `Limits: refresh requested; run /status again` atau `Limits: data not available yet` tanpa bar, `/status` diketik ulang setelah 2 dtk, paling banyak 4 kali; bila tetap kosong → `state:"error"`, `error` "belum menerima data limit…".
- Parser: persen `used` **atau** `left` (`used = 100 − left`, format Codex 0.165), label dari baris di atas bar (Claude) atau sebelum `:` (Codex), teks reset di baris bar/2 baris berikutnya; `resetsAt` dari `6pm (Asia/Jakarta)`, `Nov 1, 8pm (…)`, `18:00`, `12:52 AM on 29 Oct`, `in 2h 5m` (zona dalam kurung dipakai, selain itu zona hub); daftar "What's contributing…" diabaikan. Codex `Limits: not available for this account` (login API key) → `state:"unsupported"` dengan facts, bukan error. Facts: Codex `Model`, `Account`, dll., termasuk `Warning: limits may be stale…`; Claude `/usage` 2.1.312 tidak punya facts. Gemini `/stats`: tidak diverifikasi (tidak terpasang).
- Chime: satu kali per (report, window, `resetsAt`); reset yang terlewat ≤ 10 menit saat start tetap berbunyi, yang lebih lama tidak; event `usageReset` menambah `sound`. Diuji dengan clock injeksi.
- Token: Codex `input` = `input_tokens − cached_input_tokens`, `cacheRead` = cached; file dengan `token_usage_record` memakai record (dedupe `response_id`), selain itu `token_count` (dedupe total berjalan); ≤ 20 000 file terbaru; `machineId` kosong = semua machine tersambung; baris terakhir yang belum lengkap dibaca lagi nanti.
- **Run nyata** (This PC, `NEXTBOARD_REAL_CLI_TEST=1 NEXTBOARD_REAL_USAGE=1`, `internal/usage/realcli_test.go`): **Claude 2.1.312** `/usage` — dialog trust untuk `/tmp/nb-usage-1000` dijawab, layar "Settings · Status · Config · Usage · Stats" dengan `Current session` 16 % (Resets 12am (Asia/Jakarta)), `Current week (all models)` 36 %, `Current week (Fable)` 4 % → 3 window ter-parse, ±8 dtk (fixture `internal/usage/testdata/claude-2.1.312-usage.txt`). **Codex 0.165.0** `/status` — tanpa dialog; `5h limit: [████] 100% left (resets 12:52 AM on 29 Oct)`, `Weekly limit: … (resets 1:53 PM on 4 Nov)` → 2 window + facts (fixture `codex-0.165.0-status.txt`, email disensor). Folder usage selalu terhapus, `pending-cleanup.json` kosong. **Limit yang belum tiba** (This PC, 2026-11-03, Codex 0.165.0 di belakang proxy yang menahan semua lalu lintas 7 dtk mulai 0,7 dtk setelah koneksi pertama): `/status` pertama tanpa bar, driver menunggu lalu mengetik ulang, hasil `ok` dengan 2 window dalam ±13 dtk. Tanpa jaringan sama sekali Codex keluar saat start (`account/read failed during TUI bootstrap`), dan alasan itu masuk `error`. Teks `Limits: not available for this account` dan `Warning: limits may be stale…` diambil dari binary Codex 0.165.0 dan baru teruji dengan TUI palsu, bukan login API key sungguhan.
- **Efek samping CLI di This PC:** Claude menambah satu entri `projects["/tmp/nb-usage-1000"].hasTrustDialogAccepted=true` ke `~/.claude.json` (sesuai tujuan path tetap) dan **2 baris per pemeriksaan** (`/usage`, `/exit`) ke `~/.claude/history.jsonl` (terakumulasi pada pemeriksaan latar → keputusan pemilik); tidak ada transcript atau folder project untuk folder usage; `settings.json` tidak berubah. **Codex 0.165 menulis `[tui] animations = false` + `screen_reader_detection_done = true` ke `~/.codex/config.toml`** pada start interaktif pertama: probe screen-reader Codex (`tui/src/screen_reader.rs`) jalan setiap kali penanda itu tidak ada *di file* (override `-c tui.screen_reader_detection_done=true` tidak dibaca — diverifikasi dengan run kedua), `animations=false` karena bus a11y D-Bus This PC melaporkan `IsEnabled`. Hub tidak bisa mencegahnya tanpa menulis config; file pemilik dipulihkan byte-per-byte setelah kedua run (checksum sama). Codex juga memperbarui state/log sqlite, `models_cache.json`, `skills/.system`, `shell_snapshots` di `~/.codex` (start normal), tanpa rollout/history. **Butuh keputusan pemilik:** terima penanda sekali-tulis Codex (Codex menulisnya juga saat user sendiri pertama kali menjalankan 0.165 secara interaktif) atau matikan pemeriksaan usage Codex.
- Tes tanpa CLI asli: TUI palsu (`internal/usage/testdata/fake-tui.sh`) di PTY lokal nyata dan lewat SSH (sshd sekali pakai): trust, update dilewati tanpa pernah Enter, login, layar tak dikenal, hang (timeout), CLI hilang, batal → folder terhapus, crash → dibersihkan saat machine tersambung, symlink di path tetap ditolak, path tercatat yang tidak valid tidak pernah di-`rm`.

## 11. Suara (D11–D13)

Sepenuhnya di klien; hub tidak menerima audio dan tidak punya endpoint suara. Saat mendengarkan, web juga membuka mikrofon lewat `getUserMedia` untuk mengukur level waveform (kecuali Android); nilainya hanya dipakai di halaman itu dan mikrofon ditutup begitu selesai.

- **Web:** `SpeechRecognition`/`webkitSpeechRecognition`, **opt-in** di Settings › Voice dengan pengungkapan (teks UI): "Speech recognition here is provided by your browser. In Chrome and Edge your audio is sent to Google or Microsoft to be transcribed. NextBoard never stores or sends audio. Turn on voice?". Browser tanpa API → kontrol suara disembunyikan. Pengaturan per klien (localStorage): `enabled`, `lang` (BCP-47, default `en-US`), `holdSpace` (true), `dictationKey` (default tahan `AltRight`), `ttsConfirmations` (true), `ttsVoice`.
- **Push-to-talk:** tahan Spasi saat board fokus (bukan console/field) atau tahan/ketuk tombol mic; hasil sementara tampil di command bar; lepas → `POST /api/projects/{id}/commands` `source:"voice"`; Esc batal; maks 60 dtk (terpotong → tidak dikirim). Nama yang mirip diserahkan ke resolver fuzzy hub (konfirmasi `didYouMean`). "yes/confirm/cancel" menjawab konfirmasi pending.
- **Dikte:** tahan tombol dikte → teks ke field yang fokus, atau ke console fokus lewat delivery `{guard:"dictation", submit:false, source:"dictation"}`; `409` → teks masuk ke command bar. Kata "comma", "period", "question mark", "new line" dipetakan klien (bahasa Inggris). Maks 5 menit.
- **TTS:** `speechSynthesis` hanya untuk kalimat pendek hub (konfirmasi, "Arkan finished", pertanyaan); tidak pernah membacakan balasan agen.
- **Android:** tombol mic di command bar dan input terminal; `SpeechRecognizer.createOnDeviceSpeechRecognizer` (API 31+) bila tersedia, selain itu `EXTRA_PREFER_OFFLINE` dengan pengungkapan bila jatuh ke pengenal online; TTS via `TextToSpeech`.
- **Desktop (Tauri):** pakai Web Speech bila webview mendukung; bila tidak, kontrol disembunyikan.

## 12. Pengaturan dan rebinding shortcut

Semua memakai store Fase 5 (`GET/PATCH /api/settings`, event `settings`). Key baru:

```jsonc
"previews": { "auto": true, "portRange": [12320, 12339] },      // portRange berlaku setelah hub restart
"usage":    { "background": false, "agents": { "codex": false }, "chime": { "enabled": true, "play": "after100", "sound": "bell" } },   // bagian B: tervalidasi (play/sound enum)
"runner":   { "defaultAgent": "claude" },
"git":      { "commitWithRunner": true }
```

UI Settings (web/desktop): General (resume saat restart, notifikasi, chime), Agents (Fase 5), Shortcuts (daftar aksi `settings.shortcuts` + default; klik lalu tekan tombol baru; Esc = batal; Delete = default; `X` tetap; bentrok ditandai sebelum PATCH), Previews, Voice (per klien), Connections (status `gh` per machine + Connect), About. Android: Notifications, Voice, Agents (default per machine, yolo default dengan konfirmasi).

## 13. Event baru di `/api/events`

```jsonc
{ "type": "job", "job": Job }                      { "type": "jobOutput", "jobId": "jb_…", "seq": 12, "text": "…" }
{ "type": "git", "projectId": "p_…", "status": { "branch", "ahead", "behind", "changed" } }
{ "type": "gitRepo", "projectId": "p_…", "repo": "group/app-b", "status": { "branch", "ahead", "behind", "changed" } }   // repository di bawah folder project (§3.6)
{ "type": "github", "projectId": "p_…", "github": { … } }
{ "type": "preview", "preview": Preview }          { "type": "previewRemoved", "id": "pv_…" }
{ "type": "previewSuggested", "sessionId": "s_…", "previewId": "pv_…", "url": "http://localhost:5173/", "machineId": "local", "port": 5173, "workspaceId": "w_…", "nodeId": "n_…" }
{ "type": "previewLog", "previewId": "pv_…", "entries": [LogEntry…] }   // ≤ 4×/dtk
{ "type": "document", "projectId": "p_…", "document": Document }
{ "type": "diagram", "projectId": "p_…", "diagramId": "dg_…", "state": "done" }
{ "type": "usage", "report": UsageReport }         { "type": "usageReset", "machineId", "agent", "profileId", "window", "at", "sound" }
{ "type": "skills", "machineId": "m_…" }           // daftar skill berubah oleh hub
{ "type": "documentRemoved", "projectId", "documentId" }   // ekstensi bagian B
{ "type": "fsChanged", "watchId": "fw_…", "machineId", "changes": [Stat…] }   // ekstensi bagian B (§7.1)
```

## 14. Persistensi

| Lokasi | Isi |
|---|---|
| `jobs.json` | metadata 100 job terakhir (tanpa output besar) |
| `documents/<projectId>/index.json`, `documents/<projectId>/<docId>/<vid>.md` | library dokumen |
| `diagrams/<projectId>/<id>.json` | hasil diagram as-generated |
| `usage/reports.json`, `usage/tokens/<machineId>.json`, `usage/pending-cleanup.json` | usage |
| `backups/skills/<machineId>/<ts>/` | salinan sebelum timpa/hapus skill |
| `runner/planner/` | cwd kosong planner (This PC) |
| `settings.json` | key §12 |

Preview dan listener-nya hanya di memori (node `web` di board yang persisten). **Tulisan di machine target** (hanya atas aksi user): file repo (git, Code/Markdown studio, skill project), `.git/nextboard-trash/` atau `.nextboard-trash/`, `<cfg>/skills|commands` (dengan konfirmasi §9.2), folder sementara `/tmp/nb-usage-<uid>` dan `/tmp/nb-run.*` (selalu dihapus). Efek samping CLI sendiri saat usage/runner: lihat §10.4.

## 15. Batas dan timeout

| Hal | Nilai |
|---|---|
| Exec output | 4 MiB |
| Git | baca 10 dtk; commit 120 dtk; jaringan 15 menit / idle 5 menit; status ≤ 400 file; diff ≤ 512 KB; log ≤ 100; branch ≤ 200 |
| Runner | default 10 menit / idle 200 dtk; dokumen 6/12/25 menit; ≤ 2 per machine, ≤ 4 per hub |
| Preview | ≤ 20 listener; token 60 dtk; cookie 12 j; idle 30 menit; log 500 entri; beacon 64 KB, 10/dtk |
| Diagram | 40 node, 80 edge, 8 grup |
| Code studio | tree 5000 entri/level; buka ≤ 800 KB; highlight ≤ 120 000 karakter; stat-batch 200 |
| Markdown | 2000 file; 256 KB/file; 20 MB / 20 dtk per indeks |
| Skills | 400 item; import 50 file / 5 MB |
| Usage | siap 30 dtk; layar 15 dtk; total 60 dtk |
| Token index | 30 dtk per request |
| Suara | push-to-talk 60 dtk; dikte 5 menit |

## 16. Keamanan

- Tidak ada exec bebas; perintah git/runner/usage dari template, ref divalidasi `check-ref-format`, path setelah `--` dan dicek di dalam repo.
- Discard dan trash tidak pernah menghapus, hanya memindahkan ke trash di dalam project.
- Preview: origin terpisah per port, cookie per origin, token sekali pakai, target hanya loopback machine itu atau IP Tailscale yang dicek ulang tiap koneksi (§4.9; tidak ada SSRF ke jaringan lain), port hub dikecualikan, HSTS upstream dibuang agar tidak menempel di host hub, `/api` tidak tersedia di origin preview.
- Runner: tidak pernah yolo, tool baca-saja atau tanpa tool, output dianggap tidak tepercaya (JSON diagram divalidasi, Markdown dirender tanpa HTML mentah).
- Skill/agent memory remote: protokol 428 + backup di hub (keputusan #3). Profil remote tetap referensi (keputusan #4).
- Folder sementara: hanya di `/tmp` (`/tmp/nb-run.XXXXXX` lewat `mktemp -d`; usage = path tetap `/tmp/nb-usage-<uid>` lewat `mkdir`, symlink/milik orang lain ditolak), path divalidasi sebelum `rm -rf`, dicatat agar pasti dibersihkan (keputusan #2).
- Suara: opt-in dengan pengungkapan; audio tidak pernah ke hub.

## 17. UX per klien (yang wajib ada)

- **Web/desktop:** pill GitHub di kiri bawah (branch + jumlah berubah) → panel Git (`G`): header ahead/behind + waktu fetch terakhir, daftar perubahan dengan centang + All/None, diff per file, saran subjek (ikon), Commit & Push / Commit, Pull (bila behind), Publish to GitHub (bila tanpa remote), Discard (konfirmasi), riwayat 100, peringatan file > 100 MB ("Keep off GitHub"); kartu branch. Web tile: dial localhost (port umum, titik hijau bila terbuka), address bar, back/forward/reload, device size, zoom, tab, "Open in new tab", status menunggu dev server; kartu log; tile otomatis dengan edge cyan. Studio Documents (pilih CLI, effort + median durasi, tipe, streaming, library versi, preview mengalir/berhalaman, PDF A4/Letter), Diagrams (panel generator + starter, kartu "drawing" yang bisa dihentikan), Code (`tree` + CodeMirror, Find, status bar, simpan manual, draft), Markdown (`D`: vault, Reading/Source/Graph, autosave), Skills (`S`: Installed/Starters, filter scope, halaman skill + lint, edit, new, run ke console), usage pill + popover (bar, reset, akun/profil, raw view, grafik token), Settings §12.
- **Android:** status Git + Commit/Push/Pull dari layar project; daftar dokumen (baca, cetak PDF); usage ringkas di header + notifikasi reset; preview di `WebView` native yang memuat URL preview (sertifikat dipin dengan fingerprint hub); mic untuk command bar dan dikte terminal.

## 18. Skenario tes wajib

1. Exec watchdog: proses tanpa output 5 menit dihentikan (`idle_timeout`); output > 4 MiB dipotong.
2. Git: status repo dengan rename/untracked/konflik; commit dengan path terpilih saja; `409 conflicts` saat merge; push tanpa upstream memakai `-u`; pull divergen → `diverged`; discard memindahkan file ke `.git/nextboard-trash` dan restore tidak menimpa; push meminta password → gagal cepat (tanpa hang) dengan pesan jelas.
3. Git di SSH: perintah memakai `BatchMode`; tidak ada file di luar repo yang berubah.
4. Runner: tidak pernah ada `--dangerously-*` di argv (assert di tes); `cli_missing` dan `cli_not_logged_in` dikenali; cancel menghentikan proses dan menyimpan dokumen parsial.
5. Preview: iframe dari origin hub mendapat cookie; request tanpa cookie `401`; token kedua kali `401`; `X-Frame-Options`/`frame-ancestors`/HSTS dibuang; HMR WebSocket Vite berfungsi; target non-loopback mustahil; port hub ditolak.
6. Bridge: navigasi SPA memicu `nav`; `console.error` muncul di kartu log di dua klien; pesan dari origin tak dikenal diabaikan.
7. Preview otomatis: `npm run dev` mencetak URL → satu tile + edge (tidak dobel saat URL dicetak ulang); port tertutup → tidak ada tile.
8. Documents: streaming `jobOutput` berurutan; versi `incomplete` setelah Stop; median durasi dari 5 run.
9. Diagram: JSON di luar batas ditolak lalu diulang sekali; pendaratan menghasilkan shape/line/text dengan `diagramId`.
10. Code studio: simpan dengan `ifMtime` basi → `409` + banner; CRLF/BOM tetap; symlink keluar project butuh `allowOutside`; SSH tanpa SFTP → `501`.
11. Markdown: link wiki alias/heading ter-resolve; perubahan file oleh agen tampil ≤ 3 dtk tanpa edit lokal.
12. Skills: tulis personal di SSH tanpa `confirm` → `428` dengan daftar path; `confirm` beda satu path → `428`; backup ada di hub sebelum hapus; plugin `403`.
13. Usage: folder `/tmp/nb-usage-<uid>` selalu terhapus, juga setelah hub di-kill di tengah pemeriksaan (dibersihkan saat start); layar tak dikenal → `parsed:false` dengan `raw`; chime hanya bila `play` terpenuhi.
14. Token chart: indeks inkremental hanya membaca byte baru; dedupe `message.id`; `partial:true` saat anggaran habis.
15. Suara: tanpa opt-in tidak ada akses mikrofon; rekaman > 60 dtk tidak dikirim; dikte ke console shell → masuk command bar.

## 19. Open issues / untuk diverifikasi

1. **Listener per port** lewat reverse proxy: nginx harus memetakan tiap port preview (lihat `deploy/nginx.conf.example`); `tailscale serve` hanya meneruskan port utama, jadi web tile tidak berfungsi lewat sana.
2. **Sertifikat self-signed di iframe** (bila nginx memakainya): browser tidak menampilkan halaman peringatan di dalam iframe; buka preview sekali di tab baru untuk menerima sertifikatnya.
3. **Penulisan ulang `Host`/`Origin`** ke `localhost:<port>` bisa merusak app yang membangun URL absolut dari Host (OAuth callback); penambahan `'self'` ke CSP upstream untuk bridge; situs dengan CSP berbasis nonce.
4. **`direct-tcpip`** butuh `AllowTcpForwarding` yes/local di remote.
5. **Flag runner:** sebagian terverifikasi (§2): `--permission-mode dontAsk`, `--tools`, `--max-turns 1`, `--no-session-persistence`, `--model` bekerja di print mode; `--json-schema` tanpa tool dan `codex exec --output-schema` belum diuji nyata.
6. **Runner headless vs layar:** menyimpang dari Symvibe; agen katalog tidak didukung untuk studio. Konfirmasi ke pemilik.
7. **Usage best-effort:** format layar `/usage` (Claude), `/status` (Codex), `/stats` (Gemini) berubah antar versi. Akumulasi entri trust **diputuskan** (lead): path tetap `/tmp/nb-usage-<uid>` → satu entri `~/.claude.json` per user (terverifikasi, §10.4). Masih terbuka: `~/.claude/history.jsonl` mendapat 2 baris (`/usage`, `/exit`) per pemeriksaan Claude, dan Codex 0.165 menulis penanda screen-reader ke `config.toml` sekali (§10.4).
8. **Path agent memory Claude** per project dan daftar key frontmatter skill yang valid; layout folder plugin.
9. **PDF di browser:** dukungan margin box `@page` (Chrome ≥ 131), bookmark PDF tidak dijamin; Android `PrintManager`.
10. **Web Speech** di webview Tauri (WebKitGTK/WKWebView) dan `getUserMedia` di origin self-signed.
11. **Git:** prioritas `core.sshCommand` user vs `BatchMode`; credential helper HTTPS di remote; hook commit yang butuh TTY; penulisan ulang riwayat untuk file > 100 MB yang sudah ter-commit belum dicakup.
12. **`gh repo create --source . --push`**: verifikasi flag dan perilaku saat remote `origin` sudah ada.
13. **Pustaka layout diagram:** selesai, tidak dipakai. Tata letak ditulis sendiri (`internal/studio`, `web/src/studio/diagrams/layout.ts`).
14. **Indeks token lewat SFTP** pada folder transcript sangat besar (ribuan file) — perlu uji performa.
