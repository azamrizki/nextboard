# NextBoard API — Phase 7: runner, Git, web preview, studio, usage, voice

Additions to [API.md](API.md), [API-board.md](API-board.md), [API-agents.md](API-agents.md) and [API-orchestration.md](API-orchestration.md). Features: [ROADMAP.md](ROADMAP.md) Phase 7. Owner decisions #2 and #3 are binding. Errors use `{"error","code"}` as in API-agents.

## 0. Design summary

- **Machine exec**, internal (no free-form exec endpoint), with a timeout and an idle watchdog; used by git, the runner, usage and the goal loop.
- **Job** = the general model for long work (runner, push/pull, publish, usage) with progress, cancel and events.
- **Hidden runner** uses the CLI's headless mode (`claude -p`, `codex exec`) on the project's machine; never yolo. **The runner core (§1–§2) is needed from the start of Phase 6.**
- **Preview proxy**: a separate HTTP listener per preview (its own port; HTTPS is provided by nginx), auth token → cookie, frame-blocking headers stripped, bridge script injected.
- **Studios** (Documents, Diagrams, Code, Markdown, Skills) are built on the Phase 4 file API + the runner.
- **Usage**: a hidden interactive session in one fixed folder per machine user, `/tmp/nb-usage-<uid>` (created fresh with `mkdir`, deleted after use — lead decision, replacing `mktemp`), screen read by the VT model (best-effort); token chart from transcripts.
- **Voice**: entirely in the client.

## 1. Machine exec and Job

### 1.1 Exec (internal)

`Host.Exec(ctx, ExecSpec{Cwd, Argv | Script, Env, Stdin, Timeout, IdleTimeout, MaxOutput}) → ExecResult{ExitCode, Stdout, Stderr, TimedOut, IdleKilled}`.

- Local: `os/exec` with the login PATH (like `GitStatus`), stdin `/dev/null` when there is no `Stdin`. SSH: a non-PTY channel, `loginWrap(script)`; arguments always go through `machine.Quote`.
- Idle watchdog: no bytes on stdout/stderr for `IdleTimeout` → stop (local: SIGTERM to the pgrp, SIGKILL 3 s later; SSH: `signal TERM`, then close the channel).
- Output is capped at `MaxOutput` (default 4 MiB; the rest is dropped and marked).
- There is **no** public `POST /api/machines/{id}/exec`; every user (git, runner, usage, goal loop `command`) builds its command from a fixed template.

### 1.2 Job

```jsonc
{ "id": "jb_…", "kind": "runner"|"git"|"publish"|"usage",
  "purpose": "document",   // runner: "document"|"diagram"|"memory-scan"|"memory-init"|"memory-refresh"|"loop-goal"|"planner"|"handoff-summary"|"commit-subject"|"thread-summary"|"code-*" (§7.4)
  "projectId": "p_…", "machineId": "local", "agent": "claude",
  "state": "queued"|"running"|"done"|"failed"|"cancelled",
  "progress": { "phase": "launching"|"thinking"|"writing"|"finishing"|"pushing"|"…", "text": "Writing…", "pct": null },
  "createdAt": "…", "startedAt": "…", "finishedAt": null, "elapsedMs": 41200,
  "error": "", "code": "",   // e.g. "cli_missing", "cli_not_logged_in", "timeout", "idle_timeout", "diverged"
  "result": null }           // shape per purpose
```

| Method | Path | Response |
|---|---|---|
| GET | `/api/jobs?projectId=&active=1&limit=50` | `{"items":[Job…]}` |
| GET | `/api/jobs/{id}` | `Job` (with `result`) |
| POST | `/api/jobs/{id}/cancel` | `Job` (`cancelled`; the process is stopped, a partial result is kept when the purpose supports it) |

Event `job` (limited to 2×/s per job) and `jobOutput` `{jobId, seq, text}` for streaming text. Queue: ≤ 2 runner jobs per machine, ≤ 4 per hub, ≤ 1 mutating git job per project.

## 2. Hidden runner (E1)

Decision: the built-ins use **headless mode** (more reliable than reading the TUI); catalog/custom agents are not supported for the runner (`400 runner_unsupported`). This departs from Symvibe, which deliberately does not use print mode.

| Agent | Command (prompt via stdin) |
|---|---|
| Claude | `claude -p --output-format stream-json --verbose\|json --permission-mode dontAsk --no-session-persistence --tools <Read,Glob,Grep \| ""> [--allowedTools Read,Glob,Grep] [--max-turns 1] [--json-schema '<schema>'] [--model M]` |
| Codex | `codex exec --json --sandbox read-only --skip-git-repo-check --ephemeral -C <cwd> [-m M] [--output-schema <file>] -` (schema file in `mktemp -d /tmp/nb-run.XXXXXX`, name validated as `/tmp/nb-run.??????`, deleted via trap EXIT) |

**Verified (This PC, 2026-10-28, Claude Code 2.1.312 / Codex 0.163.0, 1 small run per CLI):** `--permission-mode` in 2.1.312 has the choices `acceptEdits|auto|bypassPermissions|manual|dontAsk|plan` (`default` is still accepted by the parser) and user settings can have `defaultMode:"auto"`, so the runner uses **`dontAsk` + `--tools`** (other tools are not available at all) **+ `--allowedTools`** for read-only purposes, instead of `--permission-mode default`. `--max-turns` is accepted (hidden from `--help`). A Claude `commit-subject` run (`--model haiku`, `--tools "" --max-turns 1`): events `system/hook_started`, `system/hook_response`, `system/init`, `rate_limit_event`, `system/commands_changed`, `assistant`, `result/success` (`result`, `is_error`, `session_id`), exit 0, 3 s. The user's **own plugin SessionStart hooks still run under `-p`** (plugin context can come along; only `--bare` turns them off, and that is forbidden). Codex `exec --json`: `thread.started{thread_id}`, `turn.started`, `item.completed{item:{type:"agent_message",text}}`, `turn.completed{usage}`, exit 0; `thread_id` → `result.agentSessionId`. Codex has no tool-limiting flag: a tool-less purpose on Codex is limited only by the `read-only` sandbox. Not yet tried for real: `--json-schema` together with `--tools ""`, and `codex exec --output-schema`.

- The `planner` purpose and other purposes without streaming may use `--output-format json` (one result object) instead of `stream-json`.
- **Never**: `--dangerously-*`, `yoloArgs`, NextBoard `--settings` hooks, `--bare` (asserted in `TestRunnerArgvNeverUnsafe`, as are `bypassPermissions`, `acceptEdits`, `danger-full-access`, `--approve-for-me`).
- Phase 5 implementation: `internal/jobs` (Job + queue + runner) and `machine.Exec`; endpoints `GET /api/jobs`, `GET /api/jobs/{id}`, `POST /api/jobs/{id}/cancel` (`404 job_not_found`), events `job` (state changes immediately, progress ≤ 2×/s) and `jobOutput`. There is no job-creating endpoint yet (jobs are created by Phase 6/7 purposes through `SubmitRunner`). A `queued`/`running` job when the hub stops/starts → `failed` with `code:"hub_restart"`. Purpose time limits: planner 45 s, handoff-summary 4 min, thread-summary 30 s, the rest 10 min / idle 200 s.
- Tool policy per purpose: `document`, `diagram`, `memory-scan`, `memory-init`, `memory-refresh` → read-only (`Read,Glob,Grep`), write/exec tools refused; `planner`, `loop-goal`, `handoff-summary`, `commit-subject`, `thread-summary` → no tools (`--max-turns 1`).
- Machine: the project's machine (cwd = project path) for repo-based purposes; This PC for `planner` (cwd `<data>/runner/planner`). Handoff: see API-orchestration §9.
- Profile: request `profileId` → default profile `settings.agents.profiles` → default login.
- Progress from the stream: `system init` → `launching`; an `assistant` event with tool/thinking → `thinking`; first text → `writing` (+ `jobOutput`); `result` → `finishing`. Final text = the `result` field (Claude) / the last `agent_message` (Codex).
- Recognized failures: command not found → `cli_missing`; a login/API key message → `cli_not_logged_in`; JSON schema not passing → one automatic retry, then `bad_output`.
- Default limits: 10 minutes total, 200 s idle (a purpose can override, see §5).

## 3. Git (B20–B22, B21)

### 3.1 Model

```jsonc
// GitStatus
{ "isRepo": true, "branch": "main", "detached": false, "upstream": "origin/main", "hasRemote": true,
  "remoteUrl": "git@github.com:me/app.git", "webUrl": "https://github.com/me/app",
  "ahead": 1, "behind": 0, "fetchedAt": "…",                 // mtime of .git/FETCH_HEAD; the panel never fetches by itself
  "merging": false, "conflicts": 0,
  "files": [{ "path": "src/a.ts", "oldPath"?: "src/b.ts", "index": "M", "worktree": " ",
              "status": "modified"|"added"|"deleted"|"renamed"|"untracked"|"conflict"|"typechange",
              "added": 12, "deleted": 3, "binary": false }],
  "truncated": false,                                        // > 400 files
  "largeFiles": [{ "path": "data/dump.bin", "sizeBytes": 160000000 }] }   // > 100 MB, untracked
// Commit
{ "hash": "…", "short": "abc1234", "subject": "…", "author": "…", "at": "…", "pushed": true }
// Branch
{ "name": "main", "current": true, "upstream": "origin/main", "ahead": 0, "behind": 0, "lastCommitAt": "…" }
```

### 3.2 Endpoints (all under `/api/projects/{id}/git`)

| Method | Path | Request | Response / error |
|---|---|---|---|
| GET | `/status` | — | `GitStatus` (`status --porcelain=v2 -b -z --untracked-files=all` + `diff --numstat -z HEAD`, `GIT_OPTIONAL_LOCKS=0`) |
| GET | `/diff?path=&staged=0` | — | `{"path","diff":"…","truncated"}` (≤ 512 KB; untracked = the file's content as an addition) |
| GET | `/log?limit=100` | ≤ 100 | `{"items":[Commit…]}` (`pushed` from `rev-list @{u}..HEAD`) |
| GET | `/log?path=&line=&limit=` | ≤ 100 | History of one file (`--follow`) or, with `line`, of one line (`-L`). A file git does not know yet → empty `items` |
| GET | `/log?graph=1&all=1&skip=&limit=` | ≤ 100 per page | History graph: `Commit` plus `parents` and `refs` (`[{name,kind}]`, kind `head`/`branch`/`remote`/`tag`), a child always before its parent (`--topo-order`). `all=1` takes every branch, remote and tag; without it only what HEAD reaches. Stashes are not included. `pushed:false` = not on any remote yet |
| GET | `/commits/{ref}` | — | `{hash,short,parents,refs,subject,body,author,email,at,committer,committerEmail,committedAt,files:[{path,oldPath?,status,add,del,binary?}],truncated}`. A merge is compared with its first parent, the first commit has every file as `added`, ≤ 2000 files. `400 bad_ref`, `404 commit_not_found` |
| GET | `/index?path=` | — | `{"path","exists","binary","text"}`: the path's content in the index |
| POST | `/stage` | `{"paths":[…]}` or `{"paths":[one],"text"}` | `GitStatus`. With `text`, the index receives exactly that text, which is how the hub stages part of a file; the old file mode is kept |
| POST | `/unstage` | `{"paths":[…]}` | `GitStatus`; the work tree does not change |
| GET | `/stash` | — | `{"items":[{"ref":"stash@{0}","hash","message","at"}]}` |
| POST | `/stash` | `{"message"?,"untracked"?}` | `GitStatus` |
| POST | `/stash/{apply\|pop\|drop}` | `{"ref"}` | `GitStatus`; a conflict is not a failure, the status shows it. `400 bad_ref` |
| POST | `/commit-op` | `{"op":"checkout"\|"cherry-pick"\|"revert"\|"merge"\|"reset","ref","mode"?}` | `GitStatus`; reset `mode` `soft`/`mixed`/`hard`. A conflict is not a failure. Git refusing: `409 git_refused`. Before `reset --hard` the hub takes a Local History snapshot (§7.11) |
| POST | `/tags` | `{"name","ref","message"?}` | `GitStatus`; with `message` the tag is annotated. `400 bad_ref`, `409 exists` |
| POST | `/operation/{abort\|continue}` | — | `GitStatus`; `409 no_operation`, `409 git_refused` |
| POST | `/blame` | `{"path","text"?}` (≤ 8 MB, `text` = unsaved editor content) | `{"commits":[{hash,author,email,at,summary}],"lines":[commit index per line],"me"}`; uncommitted lines have a zero hash; a new file → empty |
| POST | `/commit` | `{"message","paths":[…]}`, `{"message","all":true}`, or `{"message","staged":true}` (index content only), `"allowLarge"?: false` | `{"commit":Commit,"status":GitStatus}`. `400 empty_message`, `409 conflicts`, `409 large_files` (+`paths`), `422 git_identity` (user.email/name not set), `409 git_busy` |
| POST | `/push` | `{"setUpstream"?: true}` | `202 Job` (`push --progress [-u origin <branch>]`) |
| POST | `/pull` | — | `202 Job` (`pull --ff-only --progress`); a diverged failure → job `code:"diverged"` (UI: "Branches diverged — merge in a console") |
| POST | `/fetch` | — | `202 Job` (only on a user click) |
| POST | `/discard` | `{"paths":[…]}` | `{"trashedTo":".git/nextboard-trash/20261028-141500","status"}`. Tracked: copy the worktree version to the trash, then `restore --source=HEAD --staged --worktree --`; untracked: move to the trash |
| GET | `/trash` | — | `{"items":[{"id":"20261028-141500","at","paths":[…]}]}` |
| POST | `/trash/restore` | `{"id","paths"?}` | `{"restored":[…]}`; `409 exists` (does not overwrite) |
| POST | `/init` | `{"branch"?: "main"}` | `GitStatus` (`init -b main`; old git: `init` + `symbolic-ref HEAD refs/heads/main`) |
| POST | `/untrack-large` | `{"paths":[…]}` | `GitStatus` (`rm --cached --` + add a line to `.gitignore`) |
| GET | `/large-history` | — | `LargeHistory`: paths > 100 MiB in commits that are not on any remote yet (§3.7) |
| POST | `/large-history/remove` | `{"head","paths":[…]}` | `{"backup","head","rewritten","dropped","status"}`. `409 head_moved`, `409 not_large` (+`paths`), `409 detached`, `409 operation_running`, `409 too_many_commits` |
| POST | `/large-history/undo` | `{"backup","head","paths":[…]}` | `GitStatus`. `409 head_moved` when there is already a new commit, `404 no_backup` |
| GET | `/branches` | — | `{"current","items":[Branch…]}` (≤ 200) |
| POST | `/branches` | `{"name","from"?,"switch"?: true}` | `GitStatus`; `400 bad_ref` (`check-ref-format --branch`), `409 exists` |
| POST | `/switch` | `{"name"}` | `GitStatus`; no confirmation; git refuses when it would overwrite work → `409 git_refused` + git's message |
| DELETE | `/branches/{name}` | — | `{"ok":true}` (`branch -d`); `409 not_merged` |
| GET | `/suggest?ai=0` | — | `{"subject","source":"heuristic"}`; `ai=1` (`&staged=1`) → `202 Job` `commit-subject` |

- Commit: `add -A -- <paths>`, then `commit -m <msg> -- <paths>`; the user's own commit hooks still run (no `--no-verify`), without a terminal: stdin is empty and there is no `/dev/tty`, so a hook that asks fails fast and its message reaches the panel. A stuck hook is stopped after 2 minutes without leaving a lock. Processes left behind by a hook do not hold up the commit on This PC; on a server over SSH, the commit waits for those processes to finish or to close their output, for at most 2 minutes. On a detached HEAD the button reads "Commit". Push without an upstream = "Publish branch".
- **Git env:** `GIT_TERMINAL_PROMPT=0`, `GIT_SSH_COMMAND="ssh -o BatchMode=yes"` (unless the user has already set `core.sshCommand`/`GIT_SSH_COMMAND`), `SSH_ASKPASS_REQUIRE=never`, `GCM_INTERACTIVE=never`, `GIT_EDITOR=true`, `GIT_PAGER=cat`, `LC_ALL=C`, stdin `/dev/null`.
- **Timeouts:** reads 10 s; commit 120 s; push/pull/fetch/publish 15 minutes total and **5 minutes without output** (watchdog §1.1; `--progress` makes sure there is output).
- Heuristic subject suggestion (≤ 72 characters): one file → `Add|Update|Remove <name>`; one folder → `Update <folder>: N files`; only `.md` → `Docs: …`; only tests → `Tests: …`; otherwise `Update N files in <top folder>`.
- Every path is validated as relative to the repo root and passed after `--`.

### 3.3 GitHub through `gh` (B21)

| Method | Path | Request | Response |
|---|---|---|---|
| GET | `/api/projects/{id}/github` | — | `{"ghInstalled","authenticated","user","remoteUrl","repoUrl","installHint"}` (`gh auth status --hostname github.com`; hint per OS from the probe: apt / dnf / brew / a cli.github.com link) |
| POST | `/api/projects/{id}/github/login` | — | `201 Session` — an ordinary console on the project's machine that runs `gh auth login`; the hub checks `gh auth status` every 5 s while the session is alive, then broadcasts `github` |
| POST | `/api/projects/{id}/github/publish` | `{"name","visibility":"private"\|"public","commitAll":true,"message"?}` | `202 Job` `publish`: commit everything (when asked) → `gh repo create <name> --<visibility> --source . --remote origin --push` |

The token stays in the `gh` keyring on that machine; the hub does not read it.

**Pull requests** through the same `gh`, shown in the Source Control view and in a tab of their own:

| Method | Path | Request | Response / error |
|---|---|---|---|
| GET | `/git/pulls` | — | `{"items":[…]}` (open ones, ≤ 50) |
| POST | `/git/pulls` | `{"title","body","base"?,"draft"?}` | Push `origin HEAD`, then `201 {"url"}` |
| GET | `/git/pulls/{n}` | — | Details, checks, files, comments, reviews |
| GET | `/git/pulls/{n}/diff` | — | `{"diff"}` (≤ 4 MB) |
| POST | `/git/pulls/{n}/checkout` | — | `GitStatus` |
| POST | `/git/pulls/{n}/review` | `{"event":"approve"\|"comment"\|"request-changes","body"}` | `{"ok":true}` |
| POST | `/git/pulls/{n}/merge` | `{"method":"merge"\|"squash"\|"rebase","deleteBranch"}` | `GitStatus` |

Errors: `409 gh_missing`, `409 gh_auth`, `409 no_github_remote`, `502 gh_failed`. This section is not shown when `gh` is missing or the remote is not GitHub. Merge asks for confirmation.

### 3.4 Command bar and branch card

The `git` step from API-orchestration §3.4: `commitPush` = commit the files last checked in the panel (or `all` when there is no panel state), then push; `commit` with a message; `push`; `pull`. Typed by the user → immediate, no confirmation; from the planner → `risky` confirmation.

New node `branches` `{ "projectId": "p_…" }`: current branch, ahead/behind, number of changed files, ≤ 200 local branches; click = switch, "New branch", delete = `branch -d`, "Panel" opens the Git panel.

### 3.5 Hub implementation (Phase 7 part A) — deviations and verification

Code: `internal/gitsvc` (service), `internal/server/h_git.go` (endpoints, command bar adapter), `internal/orch/git.go` (`Service.Git`). Push/pull/fetch/publish jobs use `jobs.SubmitFunc` (kind `git`/`publish`, purpose `push`|`pull`|`fetch`|`publish`) and hold the repository lock while the job runs (`409 git_busy` for other mutations in that repository).

- **Shell:** every git script is POSIX sh and always runs through `exec /bin/sh -c …` (over SSH, `Exec` uses the user's login shell; zsh reads `"$b:r"` as a history modifier — found by the sshd test). The login PATH is still inherited.
- **Extra env** on top of §3.2: `unset GIT_ASKPASS SSH_ASKPASS` (askpass = a prompt; e.g. VS Code's askpass), `GIT_LITERAL_PATHSPECS=1` (globs/magic `:(…)` in paths are always literal), `GIT_SEQUENCE_EDITOR=true`, `GIT_MERGE_AUTOEDIT=no`, `GH_PROMPT_DISABLED=1`, `GH_NO_UPDATE_NOTIFIER=1`, `NO_COLOR=1`. BatchMode `GIT_SSH_COMMAND` only when the user has not set `GIT_SSH_COMMAND`/`GIT_SSH`/`core.sshCommand`.
- **Repo config:** read scripts (`prelude(…, readOnly)`, also the status on the home screen) run nothing from the repo config: `core.fsmonitor=false`, `log.showSignature=false`, and filter drivers from the `local`/`worktree` scope are turned off through `GIT_CONFIG_*` (needs git 2.31; older git gets only the first two). Filters from the global config (git-lfs) still run; files handled only by a repo's own filter can read as changed. Write actions (stage, commit, pull) still run hooks and filters.
- **Repo root:** every operation runs in `git rev-parse --show-toplevel` (a project may be a subfolder of a repo; a parent folder holding several repos: §3.6); every path is relative to the repo root. A path is refused (`400 bad_path`) when it is absolute, leaves the repo (`..`), contains control characters/newlines, or touches `.git`.
- **GitStatus** additions: `root`, `hasCommits`, `remoteName`, `changed` (total count; `files` is capped at 400 and sorted by path), `largeFiles[].tracked`. Remote = the upstream remote, then `origin`, then the first remote; `remoteUrl` without credentials (`https://user:token@` dropped); `webUrl` from an https/ssh/scp-like URL (local path → empty). Line counts of untracked files only for the first 50 files ≤ 1 MiB (`git diff --no-index --numstat`). `largeFiles` = changed paths (untracked or added/modified) > 100 MiB (104 857 600 B), from `wc -c` (fstat), without reading the content.
- **Diff:** `staged=1` → `diff --cached <HEAD|empty tree>`; otherwise against HEAD (repo without commits: empty tree); `--no-ext-diff --no-textconv`; extra field `binary`.
- **Commit:** paths must be in the current list of changes (other paths are ignored; nothing left → `409 nothing_to_commit`); a rename entry carries `oldPath`. During a merge without conflicts, the chosen paths are staged and the merge is completed with the whole index (git refuses a partial commit during a merge). Identity is checked before the commit (`user.email`/`user.name` or `GIT_AUTHOR_*`/`EMAIL`) plus git's own message → `422 git_identity`. Extra codes: `400 no_paths`, `400 bad_message`.
- **Discard:** paths that exist in HEAD are copied (`cp -pP`) to the trash, then `git restore --source=HEAD --staged --worktree` (git ≥ 2.23); paths not in HEAD (untracked, new files, rename targets) are **moved** to the trash (+ `rm --cached` when staged) — no `rm` of user files. Trash folder = `$(git rev-parse --absolute-git-dir)/nextboard-trash/<YYYYMMDD-HHMMSS>[-N]` (`mkdir` without `-p`, so two discards in the same second do not mix); the response is always `.git/nextboard-trash/<id>` + `trashed`. `409 nothing_to_discard`. `GET /trash`: ≤ 50 most recent items, ≤ 400 paths per item. **Restore:** refuses (`409 exists` + `paths`) when there is a file at the destination, **except** a tracked file identical to HEAD (that is what discard leaves; no work is lost); an item folder that is already empty is removed with `rmdir`.
- **Push:** without an upstream (or with `setUpstream`) → `push --progress -u <branch.<b>.remote|origin|first remote> refs/heads/<b>:refs/heads/<b>`. Job codes: `detached`, `no_remote`, `no_upstream`, `diverged`, `rejected`, `auth_failed`, `large_files`, `network`, `git_refused`, `timeout`, `idle_timeout`, `git_failed`. Pull: `pull --no-rebase --ff-only --progress` (the user's `pull.rebase` does not apply). Job progress from the `--progress` lines (`Writing objects 45%`); `result` = `{"action","output"}` (≤ 20 last lines).
- **Branch:** `400 bad_ref` also for a `from` that does not exist; `switch` uses `--no-guess` (does not quietly create a branch from a remote), branch does not exist → `404 not_found`; deleting the active branch → `409 git_refused`. `DELETE /branches/{name...}` accepts `/` (raw or `%2F`).
- **Commit message:** the sparkle button in the Git panel and Source Control writes a [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/) message.
  - `ai=1` (client default, `git.commitWithRunner`) → runner job `commit-subject` (agent `runner.defaultAgent`, path from `agents.paths`; no tools, 2-minute limit) → `result` `{"message","subject","source":"runner"}`. The hub reads the changes and hands them over, because the diff content comes from the repo and must not be able to use tools: branch, the last 10 commit subjects, the file list, then the diff of every file (total ≤ 48 KB, split fairly: small diffs whole, the rest for the large ones; ≤ 80 files). `staged=1` reads the index. The content of secret files (`.env*` other than `.example`, keys, `credentials*`), binaries and generated files (lockfiles, `dist/`, `*.min.js`) is not sent, only their names.
  - A structured answer `{type,scope,description,body,breaking}`, and the hub assembles the message, so its shape is always valid: `type(scope)!: description` ≤ 72 characters, the body after one blank line and wrapped at 72 columns, `BREAKING CHANGE:` as a footer. A type outside the list becomes `chore`, an invalid scope is dropped, `Co-Authored-By` lines are dropped.
  - `ai=0`, or the CLI fails → `{"subject","source":"heuristic"}`: an instant subject from the file names, the same shape, but it states only what the paths prove: `docs`, `test`, `ci`, `build`, otherwise `chore`; `feat` and `fix` need reading the changes. Scope = the deepest meaningful shared folder (not `src`, `internal`, …), then the top-level folder used by more than half of the files. `?path=` (may repeat) limits to the checked files.
- **Panel selection (addition):** `PUT /api/projects/{id}/git/selection {"paths":[…]}` → `{"paths"}` (in memory). Command bar `commit`/`commit and push` uses this selection (filtered to changes that still exist), otherwise `all`; an empty message → heuristic subject.
- **Command bar:** `orch.Service.Git` (`GitActions`); typed → immediate; from the planner → `risky` confirmation (Phase 6 logic). Immediate reply e.g. `Committed abc1234 “Fix login” (1 file). Pushing…`; the job result follows as a thread message `Push done.` / `Pull done.` / `Push failed: …` / `Branches diverged — merge in a console.` (kind `error`).
- **GitHub:** install hint matching the package manager present (`brew`, `apt`, `dnf`, `pacman`, otherwise `https://cli.github.com`). Publish refuses when there is already any remote (`409 remote_exists`), detached (`409 detached`), no commits and `commitAll:false` (`409 no_commits`), files > 100 MB with `commitAll` (`409 large_files`); name `[owner/]repo` (`400 bad_name`). Job: `commit -A` (default message heuristic / `Initial commit`) → `gh repo create <name> --private|--public --source . --remote origin --push`; `result` `{"repoUrl","committed","output"}`, codes `gh_missing`/`gh_not_logged_in`. Login: a "GitHub login" shell console in workspace `workspaceId` (optional body) or the project's first workspace; the hub types `gh auth login` once the prompt appears, then `gh auth status` every 5 s (≤ 30 minutes) while the session is `running`; event `github` on change and on finish.
- **Event `git`** is sent when the summary changes (also from `GET /status`) and always after a mutation.

**Verification (This PC, git 2.53, gh 2.46 not called — a fake `gh` in the tests):** `go test -race ./internal/gitsvc` (temporary repo, global config isolated with `GIT_CONFIG_GLOBAL`): status of rename/untracked/`-dash`/staged/deleted/a sparse 101 MiB file, project subfolder, truncate at 400, merge conflict → `409 conflicts`; init/`exists`/`bad_ref`; commit of chosen paths only, literal path `-x [1]*.txt`, `nothing_to_commit`, `large_files` + untrack-large (`.gitignore` without duplicates), empty identity → `git_identity`; `git_busy`; push without upstream (`-u`), ahead/pushed, fetch/behind/`fetchedAt`, pull ff, diverged → `diverged` without changing HEAD, push rejected → `rejected`; an HTTP remote asking for a password → `auth_failed` at once; discard to trash (copy + restore), same second → `-2`, restore does not overwrite (`409 exists`); branch create/switch/refused/delete/not_merged; publish through a fake `gh`; login watcher. `NEXTBOARD_SSHD_TEST=1 go test ./internal/machine -run TestGitOverSSH` (temporary sshd, zsh login shell): status/commit/push/pull/discard/branch/log over SSH; the git env on the remote side recorded through a local-repo `ext::` remote (`GIT_TERMINAL_PROMPT=0`, `GIT_SSH_COMMAND=ssh -o BatchMode=yes`, `SSH_ASKPASS_REQUIRE=never`, `GCM_INTERACTIVE=never`, no `GIT_ASKPASS`); no new/changed files outside the repo + remote. Server: `TestGitAPI`, `TestGitCommandBar` (panel selection, `commit and push` → `Push done.` in the thread), `internal/orch TestGitSteps` (no service → old reply; typed without confirmation; planner → `risky`).

### 3.6 Several repositories in one project

A project folder may be a parent folder holding several repositories, with or without a repository in that folder itself. A project with one repository does not change.

**Scan:** `GET /git/repos` → `{"items":[RepoInfo…],"truncated":false}`.

```jsonc
// RepoInfo
{ "path": "group/app-b",   // relative to the project folder; "" = the project's own repository (may be above its folder)
  "name": "app-b", "root": "/abs/group/app-b",
  "branch": "main", "ahead": 0, "behind": 0, "changed": 3,    // same status mode as the Git panel
  "error"?: "…" }                                             // the summary could not be read; the repository stays listed
```

- Read-only, no fetch, no events sent, and symlinks are not followed. Uses `find` and `xargs -0` on the machine, so it also works over SSH without an agent. Never called periodically: the client scans when the project opens, when the panel opens, and on Refresh.
- **Limits:** 4 levels below the project folder, ≤ 50 repositories, 10 s for the scan, ≤ 4 status reads at once, 20 s for all summaries. `truncated:true` = hit the count or time limit; what was already found is still returned.
- **Skipped:** `node_modules`, `bower_components`, `vendor`, `target`, `dist`, `build`, `.cache`, `.gradle`, `.venv`, `venv`, `__pycache__`, `.next`, `.terraform`.
- `.git` only counts when that folder is really a repository root (`rev-parse --show-toplevel`), so an empty `.git` folder is not included. Submodules and linked worktrees are listed too.

**Choosing a repository:** `?repo=<path>` applies to every `/git/*` endpoint and `/github`; without the parameter = the project's own repository, as before. On every operation, a script on the machine makes sure `<project folder>/<path>` is still a repository root and is reached without symlinks; if not, the result is `isRepo:false` or `409 not_repo`, never falling through to an outside repository. `400 bad_path` when absolute, leaving the project (`..`), or touching `.git`. This check is done per operation on purpose, not from the cached scan result, because the cache can be stale.

**State per repository:** the `git_busy` lock, the panel selection and the last summary. A push in one repository does not block other repositories.

**Event:** every part of a project sends `{"type":"gitPart","projectId","part","repo","status"}` for each of its repositories (`repo` `""` = the part folder's own), keyed by the part's id, so the key stays when the parts are reordered (API-board §1 "Project parts"). The first part in display order also sends the old events: repositories below its folder use `{"type":"gitRepo","projectId","repo","status"}` so that a client that only knows the project's repository does not misread their summary, and its own repository `{"type":"git"}`. The web keeps git state per part under `<project>~<part>`, and the old events under the bare project id for views that still ask by it. State per repository (`git_busy`, selection, last summary) is keyed by project, part and repository.

**A repository nested inside another repository:**
- `GitStatus.nestedRepos` (`["libs/inner"]`) lists it; `files` and `changed` no longer include `libs/inner/`.
- Commit `all` (panel, command bar, publish) does not stage it: `git add -A` would record a `160000` gitlink whose content is not cloned along, and it fails for a repository without commits.
- `POST /init` in a folder that holds repositories → `409 nested_repos` (+`paths`): the new repository would wrap them all.

**Command bar:** `repo <name> <git command>`, e.g. `repo app-a commit and push`, `repo group/app-b push`, `repo "my app" commit with the message Fix login`, `repo . pull` (the project's own repository). `<name>` = a path or a unique folder name, case is ignored. The planner's `git` step has a `repo` field.
- Without a name: 0 repositories → the old message; 1 repository → that repository automatically; ≥ 2 → refused with the list of names and nothing runs. Never commits to every repository.
- Thread messages for a repository below the project folder start with its path: `app-a: Push done.`

**Web:** the repository choice is kept per project in the browser and shared by the pill, the Git panel, the Branches card and Source Control in the Code studio. The picker is hidden when there are fewer than two repositories. Explorer decorations and the editor gutter follow the chosen repository only.

**A parent folder that is not a repository:**
- Go to file reads every repository below it (≤ 5 levels, ≤ 50 repositories) through its own git, so its `.gitignore` applies. The rest goes through `find` with the prune list, `source` stays `walk`.
- Search with ripgrep also follows each repository's `.gitignore`. Without ripgrep, only the prune list applies.
- A folder that cannot be read drops only that folder, not the whole list or search.
- The project's row on the home screen adds up the repositories below that folder.

Explorer also dims ignored files in repositories below the parent folder: in a project that is not a work tree, `files/ignored` asks the git of each path's folder (≤ 16 folders per request). **Not yet:** Android and desktop have no picker yet; without `?repo=` the behavior is unchanged.

**Verification (This PC, 2026-11-03):** `go test -race ./internal/gitsvc` (`TestRepos*`, `TestSubRepo*`, `TestNestedInOuterRepo`, `TestInitRefusedAboveRepos`, `TestPick`), `internal/server TestGitMultiRepo`, `internal/orch TestGitSteps`. `NEXTBOARD_SSHD_TEST=1 go test ./internal/machine -run TestGitReposOverSSH` (temporary sshd, zsh login shell, folder names with spaces). Headless Chrome through Playwright against a temporary hub and Vite: parent folder, repository + nested, one repository, empty folder (panel, pill, Branches card, Source Control, command bar).

### 3.7 Large files already committed

`untrack-large` only helps files that are not in a commit yet. Once they are, push still fails because that commit still holds the blob, so the cure is a new commit without that file.

```jsonc
// LargeHistory
{ "head": "…", "detached": false, "unpushed": 3, "tooMany": false,
  "files": [{ "path": "data/dump.bin", "sizeBytes": 160000000, "commits": 2, "inPushed": false }] }
```

- **Only commits not yet on a remote are touched:** `HEAD --not --remotes`, at most 2000 commits. Commits already pushed do not change, so a force push is never needed.
- **Scan:** reads `ls-tree` of each commit, not each blob once, because one blob under two names must disappear under both. `inPushed` means a commit on the remote also has that path at an accepted size, so removing it means the next push deletes that file there.
- **How it rewrites:** plumbing with a temporary index (`read-tree`, `update-index --force-remove`, `write-tree`, `commit-tree`), not `filter-branch`, which refuses a dirty work tree. Author, committer, dates, message and merges stay. Commits whose content is only that file are dropped. GPG signatures are lost because the content changes.
- **Atomic:** the branch moves in the last step with its old value checked (`update-ref <ref> <new> <old>`), so a failure or timeout midway changes nothing.
- **Guards:** `head` must match what the owner saw. The paths must be found above the limit by a fresh scan, so a mistaken call cannot pull an ordinary file out of history. Refused when detached and while a merge, rebase, cherry-pick, revert or bisect is running.
- **What stays:** the files on disk, open edits, and the index content other than those paths. The paths go into `.gitignore`.
- **Backup:** `refs/nextboard/backup/<id>` points to the old history and is not pushed. `.git` does not shrink until that ref is deleted (`git update-ref -d`) and `git gc` runs. Undo reverts it as long as there is no new commit.
- **UI:** a warning appears in the Git panel and Source Control when `ahead` is 1–200, or after a push fails with code `large_files`.

### 3.8 The board's Git panel: every part, history graph

No new endpoint: each part's git is already served under its project ref (`<project>~<part>`, API-board §1 "Project parts"), and its events come as `gitPart`, the first part's too (§3.6).

- **Several parts:** the panel (G) lists every part at once, in the project's order and under its own name. No part is marked as the main one. Each header shows the part's machine and its repository: branch, ahead/behind and the number of changed files. A part with several repositories (§3.6) shows one line per repository; a click picks that repository. A part with none says so. Opening the panel scans every part (`GET …~<part>/git/repos`). Refresh reads every part again.
- **Opening a part:** each part opens and closes on its own, and several can be open at once. Which parts are open is kept per project in the browser (`nb.git.parts.<project>`). An open part has the whole panel for its picked repository: changes and diffs, commit, push, pull, fetch, publish to GitHub through the `gh` of that part's machine, discard and trash, and files over 100 MB. Its ticks go to its own `PUT …/git/selection`. Messages about a job say which part they are about (`Web: Push done.`).
- **One part:** the panel looks as before, with the repository picker of §3.6.
- **Branches, Stash, History, Discarded:** the sections of the editor's Source Control, closed until opened. History is the commit graph of the workbench (the same component, all branches or the current one, the right-click menu for checkout, branch, tag, merge, cherry-pick, revert and reset). A commit picked there, or a stash, shows its files with their line counts (`GET …/git/commits/{ref}`). **Open in editor** shows it in a tab of the editor card, with the diff of each file. When the card shows another part, a file of that commit moves the card to the commit's part.
- **`github` event:** it carries only `projectId`. A part's publish card reads `GET …~<part>/github` again on each one, so a sign-in on another machine shows without a reload.

**Verification (This PC, 2026-11-15):** `web/src/git/parts.test.ts`, and `e2e/git-parts.spec.ts` in light and dark. A project in two parts: both headers with their branch, changes and ahead count before opening either; both parts open at once with their own message box; a commit in the second part lands only there; the history graph with the remote branch; a commit's files; Open in editor. A project in one folder: the panel as before, with stash and graph, and no overflow at phone width. Not tried yet: parts on another machine over SSH, and a GitHub sign-in on that machine.

## 4. Web preview (B17–B19)

### 4.1 Port probe

`GET /api/machines/{id}/ports?probe=3000,3001,4200,5173,8000,8080` → `[{"port":5173,"open":true,"http":true}]`. ≤ 20 ports, in parallel; a TCP connection to `127.0.0.1` (then `[::1]`) on that machine (local: dial; SSH: a `direct-tcpip` channel), timeout 800 ms; `http` = an HTTP response to `GET /` within 1.5 s.

### 4.2 Preview

```jsonc
{ "id": "pv_…", "machineId": "local", "targetPort": 5173, "targetScheme": "http", "path": "/",
  "listenPort": 12321, "targetHost"?: "portal.example.net", "sessionId"?: "s_…", "auto": false,
  "state": "waiting"|"live"|"error", "error": "", "createdAt": "…", "lastUsedAt": "…" }
```

| Method | Path | Request | Response / error |
|---|---|---|---|
| GET | `/api/previews` | — | `Preview[]` |
| POST | `/api/previews` | `{"machineId","port","scheme"?: "http","path"?: "/","sessionId"?}` or `{"url"}` for a site on the tailnet (§4.9) | `201 {"preview","launchPath":"/__nb/launch?t=…&to=%2F"}`; the same (machine, port) or site → `200` the old preview + a new token. `400 bad_port` (1–65535; on This PC the hub's port and the preview range are refused), `503 no_preview_port`. The response may still be `state:"waiting"` while the port check runs in the background; a `preview` event with `state:"live"` can arrive **before** the response — the client uses the newer version from the event. |
| POST | `/api/previews/{id}/launch` | `{"path"?}` | `{"launchPath","expiresAt"}` (new token, single use, 60 s) |
| DELETE | `/api/previews/{id}` | — | `{"ok":true}` (listener closed) |
| GET | `/api/previews/{id}/logs?since=` | — | `{"items":[LogEntry…],"seq"}` |

The client builds the URL from the origin it is using: `${location.protocol}//${location.hostname}:${listenPort}${launchPath}`, and the bridge's `postMessage` target the same way. Do not hard-code `https://`: in the desktop app the SPA runs at `http://127.0.0.1` behind a local proxy that rewrites `listenPort` to a local loopback listener (see the nextboard-desktop README). The preview iframe gets a `sandbox` attribute without `allow-top-navigation`.

Formats the desktop app parses and that must stay stable: `var HUB = <JSON array>;` in `bridge.js`; `<scheme>://<host>:<port>` entries in the SPA's `frame-src` CSP; preview objects with an `id` of `pv_…` and a `listenPort`; a `launchPath` of the form `/__nb/launch?t=…`.

### 4.3 Listener and auth

- One HTTP listener per preview in `settings.previews.portRange` (default 12320–12339), bound to the same address as the main listener; nginx provides HTTPS on the same port number. Closed after 30 minutes without requests when no `web` node refers to it.
- `/__nb/launch?t=` validates the token (single use, bound to the preview) → cookie `nb_pv` (`HttpOnly; SameSite=Lax; Path=/`, `Secure` when over HTTPS, random per preview, 12 h, extended) → `302` to `to`. Other requests without a valid cookie → `401` page "Open this preview from NextBoard". An iframe from the hub's origin counts as same-site (same host, different port), so the Lax cookie is sent; other sites framing the preview do not carry the cookie.
- The preview origin never serves `/api`. The `/__nb/*` paths are reserved: `launch`, `bridge.js`, `log`.
- The hub UI's CSP gets `frame-src` = `<request scheme>://<request hostname>:<each port in the range>` (computed per request; scheme from `X-Forwarded-Proto` of a loopback peer).

### 4.4 Proxy rules

- Upstream: `127.0.0.1:<port>`, then `[::1]:<port>` on the target machine (local dial; SSH `direct-tcpip`), or a site on the tailnet (§4.9). Other targets are never proxied. An `https` upstream may be self-signed on loopback.
- Request: `Host` and `Origin` are rewritten to `localhost:<port>` (dev servers check the host); the `nb_pv` cookie is dropped; `Accept-Encoding` is limited to `gzip, identity`.
- Response: drop `X-Frame-Options`, `Strict-Transport-Security`, `frame-ancestors` directives (CSP and Report-Only); when the CSP has `script-src`/`default-src` without `'self'`, add `'self'` so the bridge can run; a `Location` to `http(s)://(localhost|127.0.0.1|0.0.0.0|[::1]):<port>` is rewritten to the preview origin; the `Domain=` attribute on `Set-Cookie` is dropped.
- `text/html` (status 200): gzip is opened, `<script src="/__nb/bridge.js"></script>` is inserted after the `<head…>` tag within the first 64 KiB (not found → at the start of the document), `Content-Length` is removed.
- WebSocket (HMR) is proxied transparently. Dial refused → `502` page "Waiting for the dev server on :5173…"; the tile retries 10× every 1 s, then shows Retry / Open in new tab.
- Limits: request body ≤ 100 MB; dial 5 s; response headers 60 s; streaming without limit.

### 4.5 Bridge protocol (postMessage)

Every message is `{ "nb": "bridge", "v": 1, "type": …, … }`. The parent sends `hello` when the iframe loads; the bridge records `event.origin` when it is in the list of hub origins (from `Hub.urls` + the preview's hostname with the main port) and replies only to that origin.

| Direction | type | Content |
|---|---|---|
| parent → bridge | `hello`, `back`, `forward`, `reload {hard}`, `navigate {url}`, `fill {id,username,password,code}` | `navigate` only same-origin paths. `fill` only on the person's own choice ([API-vault.md](API-vault.md)) |
| bridge → parent | `filled {id,username,password,code}` | Three booleans: which fields were filled. Never their values |
| bridge → parent | `ready {url,title}`, `nav {url,title}` (pushState/replaceState/popstate/hashchange) | |
| bridge → parent | `console {level,args[],at}`, `error {message,source,line,col,stack}`, `net {method,url,status,ms}` | args stringified ≤ 2 KB |
| bridge → parent | `open {url}` | `window.open`/`target=_blank`: same-origin → a new tab in the tile (≤ 12), external → a tab in the client's browser |

The bridge also sends log batches to `POST /__nb/log` (≤ 64 KB, ≤ 10/s) → a ring of 500 entries per preview on the hub → event `previewLog` and `/logs`, so the **log card** (replacing the WebKit inspector) works in every client.

### 4.6 Automatic preview (B18)

The hub scans each session's output (after stripping markers and ANSI) with `https?://(localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1\]):(\d{2,5})(/[^\s"'<>]*)?`, once per (session, port) per agent run. The port is checked on the session's machine (3× within 3 s); open → preview `auto:true`, the hub inserts a `web` node to the right of the terminal + a `preview` edge through `boardOps`, event `previewSuggested`. Turned off with `settings.previews.auto:false`.

### 4.7 Nodes and edges

| kind | `data` |
|---|---|
| `web` | `{ "url": "http://localhost:5173/", "previewId"?: "pv_…", "machineId"?: "local", "port"?: 5173, "tailnet"?: true, "realBrowser"?: true, "device": "fit"\|"desktop-1920"\|"desktop-2560"\|"desktop-3840"\|"phone-375"\|"phone-393"\|"phone-430"\|"phone-412"\|"phone-360" (other ids read as `fit`), "zoom": 100, "auto": false, "tabs"?: [{ "id", "url", "title" }], "activeTab"?: "…" }` |
| `weblog` | `{ "previewId": "pv_…", "filter": "all"\|"errors"\|"network" }` |
| `link` | `{ "url": "https://…", "title"?: "…" }` — an external site, opened in a new browser tab |
| `branches` | §3.4 |
| `document` | `{ "documentId": "doc_…", "versionId"?: "v_…" }` |

New edges: `preview` (terminal → web, cyan), `log` (web → weblog). Device sizes: a fixed-size iframe + `transform: scale()`; zoom 50–250 % in steps of 10.

### 4.8 Hub implementation (Phase 7 part A) — deviations and verification

Code: `internal/preview` (manager, proxy, bridge, log, detection, port probe), `internal/machine/dial.go` (`DialLoopback`, `ProbePort`), `internal/server/h_preview.go` + `gitpreview.go`.

- **Cookie per listener: `nb_pv_<listenPort>`** (not `nb_pv`; over plain HTTP `nb_pv_http_<listenPort>`, because the browser refuses to let an `http://` page overwrite a `Secure` cookie of the same name): cookies are scoped by host, not port, so one name for every preview would make tiles overwrite each other. The value is random per launch (not per preview), 12 h sliding, resent at most once an hour. NextBoard cookies (`nb_session`, `nb_session_http`, `nb_pv*`) are **dropped from requests to the upstream**, and upstream `Set-Cookie` with those names is dropped (prevents cookie tossing onto the hub).
- **Extra headers dropped:** `Clear-Site-Data` (could delete the hub's cookies on the same host), `Alt-Svc`, `X-Forwarded-For`, `Forwarded`. A `Referer` from the preview origin is rewritten to `http(s)://localhost:<port>`, otherwise removed. `connect-src` also gets `'self'` (the log beacon); `'none'` is dropped when `'self'` is added. Preview responses get an extra CSP `frame-ancestors 'self' <hub origin>` (only the hub UI may frame them; new tabs/WebViews are not affected).
- **Auth:** every path except `/__nb/launch` needs the cookie, including `/__nb/bridge.js` and `/__nb/log`. `to` on launch must be a same-origin path (`//host`, `/\` refused → `/`). Token of 32 random bytes, single use, 60 s; the launch response is `no-store`. The 401/502 pages have no scripts (CSP `default-src 'none'`).
- **Proxy:** `httputil.ReverseProxy` (WebSocket/HMR transparent, `FlushInterval -1` for SSE), one `http.Transport` per preview that always dials `127.0.0.1`, then `[::1]` at the target port on that machine (the address from the request is never used). Injection: HTML 200 (also `application/xhtml+xml`), gzip is opened, other encodings (br/zstd) are left without injection. Dial refused → `502` "Waiting for the dev server on :N…" + state `waiting`; SSH without forwarding (`administratively prohibited`) → `machine.ErrForwardingDisabled`, state `error` + message. State `live` once the initial probe/a response succeeds; event `preview` on creation and on state change.
- **Listener:** plain HTTP, bind = the host from the hub's `listen` (`0.0.0.0` → all). The first free port in `previews.portRange`; ports used by other processes are skipped. ≤ 20 listeners. A reaper (every minute, starting when the first preview is created) closes previews unused for 30 minutes and not referred to by any `web` node (`data.previewId` scanned on every board). `DELETE` closes the listener at once.
- **Target ports refused on This PC:** the hub's port, the loopback hook listener's port, and the whole preview range (`400 bad_port`). Other errors: `400 bad_scheme`, `400 bad_path`, `404 preview_not_found`, `404 machine_not_found`, `503 no_preview_port`.
- **`/__nb/log`:** body `{"entries":[…]}` or an array, ≤ 64 KB, ≤ 200 entries, ≤ 10 requests/s (`429`), `204`. Entries are validated (`console|error|net`, arg ≤ 2 KB, ≤ 20 args) and given a hub `seq`/`at`. `GET /logs?since=` → `{"items","seq"}`; `previewLog` events merged ≤ 4×/s.
- **Port probe:** results sorted by port; `400 bad_port` / `too_many`; machine disconnected → `503 machine_offline`. `http` = an `HTTP/1.x` status line for `GET /` within 1.5 s.
- **Automatic preview:** the detector (`preview.Detector`) is chained onto `sessions.OnOutput`; ANSI is stripped (`charmbracelet/x/ansi`), a URL cut at a chunk boundary is joined with the rest of the previous chunk, punctuation at the end of the path is dropped, host `0.0.0.0`/`[::1]` → `localhost`. "Run" = the session's `startedAt`. The port is checked 3× within 3 s; when closed, a URL printed again later may be tried again. A 960×640 `web` node to the right of the terminal node (+60 px) with `data {url, previewId, machineId, port, device:"fit", zoom:100, auto:true}` + a `preview` edge; not added when the workspace already has a `web` node for that preview or (machine, port). The `previewSuggested` event gets extra fields `machineId`, `port`, `workspaceId`, `nodeId`.
- **Hub UI CSP:** `frame-src 'self' <request scheme>://<request hostname>:<each port in the range>`.
- **Settings (§12):** `previews.auto`, `previews.portRange` (`[start, end]`, 1024–65535, ≤ 200 ports, read when the hub starts), `git.commitWithRunner` (default `true`, read by the client only; the old `git.suggestWithRunner` is ignored) — `internal/settings/previews_git.go`.
- **Command bar** `open localhost <port>`: the hub opens (or reuses) a preview on the project's machine and inserts a `web` node with `data {url, previewId, machineId, port, device:"fit", zoom:100, auto:false}`. The client may still create a preview itself for an old `web` node without `previewId`.

**Verification:** `go test -race ./internal/preview` (a local HTTP server as the dev server, listeners in a random port range, not 12320–12339): no cookie `401`, a token used twice `401`, launch → cookie `HttpOnly; Secure; SameSite=Lax`, bridge inserted after `<HEAD lang=en>` / at the start / after gzip is opened, XFO/HSTS/Clear-Site-Data/`frame-ancestors` dropped, CSP `'self'` added, `Domain=` dropped, `nb_session` neither reaches the upstream nor is set by the upstream, Host/Origin/Referer rewritten, loopback `Location` → preview origin, 3 MB upload, WebSocket echo through the proxy (no cookie `401`), dev server down → `502` + `waiting`, hub port/range refused, open redirect `to` refused, log beacon + event + rate limit, delete closes the listener, idle reaper vs a referring node, port probe (closed / TCP only / HTTP), detector (ANSI, cut chunk, duplicate, closed port, `auto:false`). `NEXTBOARD_SSHD_TEST=1 go test ./internal/machine -run TestPreviewOverSSH`: probe and proxy through `direct-tcpip`, `AllowTcpForwarding no` → `ErrForwardingDisabled`. Server: `TestPreviewAPI` (CSP `frame-src`, probe, create/reuse/launch/log/delete, proxy through a real listener), `TestAutoPreview` (a shell console printing a colored URL → one preview + `web` node + `preview` edge, printing again does not duplicate, a closed port makes no tile). Bridge: `node --check` + a fake DOM simulation in Node 24 (manual, outside `go test`): messages before `hello` are queued, `hello` from a foreign origin is ignored, `ready`/`console`/`nav` (pushState)/`error`/`net`/`open` only to the hub origin that greeted, `back` from another origin ignored, cross-origin `navigate` refused, the `/__nb/log` beacon is sent and not reported as `net`. Not yet tried in a real browser (iframe + self-signed certificate, open issue #2).

### 4.9 Outside sites through the proxy

Addresses that resolve to a Tailscale IP or a public IP show in a web tile through the same proxy, without a real browser, so no Chrome eats RAM on This PC. Sites that cannot work through the proxy, and addresses on This PC or its local network, go to a real browser (`docs/API-gui.md` §8). `tailnet:true` data on the node means "through the proxy", including for public sites.

**Flow.** The tile sends the typed address to `POST /api/previews {"url"}`. The hub resolves that host from This PC, whatever the project's machine is.

| Result | Meaning | Tile |
|---|---|---|
| `201`/`200` | The site answers | The node gets `tailnet:true` and `previewId`, the address bar shows the original address |
| `400 needs_browser` | A public site that would break through the proxy (see "Initial check") | A real browser, with a short message |
| `400 private_address` | Points only to This PC, the local network, cloud metadata or another special address | A real browser |
| `400 no_such_host` | DNS failed | A real browser |
| `400 bad_url`, `bad_path`, `bad_port` | Not http(s), contains user:password, invalid path, or the hub's own port on a This PC IP | A real browser |
| `502 site_unreachable` | Nothing answers within 5 s | A message in the tile; no browser is started |

**Initial check (public sites only).** Before the listener opens, the hub requests the page once, with the User-Agent of the browser that asks, following redirects (max 6). A redirect to `https` or to the same name with/without `www` changes the preview's destination, because otherwise the frame would leave the proxy; the `preview` response carries the final destination. A redirect to another host, a bot check and an invalid certificate become `needs_browser`: a login on another host cannot come back through the proxy, and a bot wall would only show its refusal. Bot check = a `Cf-Mitigated` header, or a `403`/`429`/`503` whose page carries a challenge marker (Cloudflare, Imperva, Sucuri, DataDome, PerimeterX, hCaptcha, reCAPTCHA; first 64 KB). A site's own `403`/`429`/`503`, for example Laravel in `artisan down` during a deploy, still goes through the proxy, and its page tells what is going on: before, such a site moved to a real browser and stayed there. The error message names the reason ("it answers the hub with a bot check (403)"), and the toast in the tile names it too. A tile opened in a real browser because of an earlier choice says so and offers "Try the light view".

The tile's toolbar and menu have **Open in a real browser** (`realBrowser:true`) for sites that do not suit the proxy. That choice is remembered per host in the browser that made it (`localStorage`), so the next address on that host goes straight there; the **Try the light view** menu item undoes it. Old nodes and nodes from the planner move to this path when **Open here** is pressed.

**What counts as public.** Global unicast, except the private ranges, `0.0.0.0/8`, `100.64.0.0/10`, `192.0.0.0/24`, the documentation and benchmark ranges, `240.0.0.0/4`, NAT64 `64:ff9b::/96`, and 6to4 `2002::/16` (the last two can wrap a private IPv4).

**What counts as tailnet.** `100.64.0.0/10` and `fd7a:115c:a1e0::/48`, except the Tailscale service addresses (`100.100.100.100`, `fd7a:115c:a1e0::53`) and `100.115.92.0/23`. These ranges are checked from the address alone, so other CGNAT addresses inside them (for example a Cloudflare WARP interface) also pass, but still only addresses This PC can reach.

**Security.**

- Each connection resolves the name again and dials only Tailscale or public IPs, including each hop of the initial check, so a name or redirect pointing to loopback, the local network or cloud metadata never gets there. A name with mixed addresses is dialed only to the allowed addresses.
- Certificates are verified against the site's name, also for a bare public IP. Only a bare Tailscale IP is not verified: there is no name to check, and WireGuard already authenticates the node.
- **Between tiles:** the browser treats every port on one host as one site, so a page in one tile could call another tile with its cookies. Requests with an `Origin` from another port on the same host are refused with `403` by sites, and by dev servers when the origin is a site's port. The hub itself already refuses: no CORS, mutations need `X-NextBoard`, WebSockets check the origin.
- **Ports that change owner:** `localStorage` and friends are bound to the origin, that is the port. The bridge clears `localStorage`, `sessionStorage`, Cache and IndexedDB before the page runs when the port was last used by another site (marker: cookie `nb_pv_owner_<port>`). Service workers are refused (`404`) because they would outlive the site. A site gets its port from a hash of its name, so it usually comes back to the same port after a hub restart and its data is still there.
- Listener auth is the same as §4.3. A site does not become open to anyone who can reach the hub.

**What is rewritten.**

- Over plain HTTP, the CSP directive `upgrade-insecure-requests` is dropped, because it would turn every asset into HTTPS, which does not exist on the preview origin.
- The site's own URLs (`http(s)://`, `ws(s)://`, `//`, and the JSON form `https:\/\/`) in every header and text body (HTML, CSS, JS, JSON, XML) become the preview origin, including gzipped ones. `text/event-stream` is not touched so that events are not held back.
- Other hosts, encoded URLs and bare host names are left alone.
- `Host`, `Origin` and `Referer` to the upstream use the site's original address.

**Cookies.** Cookies are scoped by host, not port, so without session separation one site's cookies would also be sent to another site in another tile. Each site has its own prefix `nb_c_<hash of host:port>_` in the browser. The hub forwards only cookies with that prefix, under their original names; the bridge does the same for `document.cookie`. A localhost dev server never receives these prefixed cookies. Over plain HTTP `Secure` is dropped and `SameSite=None` becomes `Lax`, since the browser refuses such a cookie otherwise.

**Limits.**

- Cookie separation prevents misdelivered cookies, not a malicious site. Every preview shares a hostname with the hub, so a site's JS that deliberately goes around `document.cookie` can read another site's non-`HttpOnly` cookies and overwrite them. `HttpOnly` cookies, including the hub session, cannot be read or overwritten, and the hub API still refuses preview origins. Open only trusted tailnet sites through this path.
- Other ports on the same host are other servers and are not proxied: the browser that opens NextBoard loads them directly. Vite dev assets (`portal…:5177`) therefore only show when that device is on the tailnet and that server allows the preview origin through CORS. Production builds are not affected.
- Links to other hosts leave the proxy and their frame may be refused.
- `document.cookie` is wrapped only in HTML pages with status 200 (where the bridge is inserted); `cookieStore` is not wrapped.
- Assets with `integrity` whose content names the site will fail their hash.
- Data in `localStorage` is bound to the listener's port. If the hashes of two sites collide, a site gets another port and its data is lost; cookies are not, because their prefix comes from the site's name.
- A site that calls another host from JS (an API on another subdomain) runs into CORS, because its origin is now the preview origin. A hub over plain HTTP is also not a secure context: `crypto.subtle`, the clipboard and passkeys are not there. For all of these: **Open in a real browser**.

**Verification (This PC, 2026-11-03):** `go test -race ./internal/preview -run 'Site|IsTailnet'`, `internal/server TestPreviewOfSiteAPI`. A real browser on a test hub: a login with CSRF, redirects, and two sites with the same cookie name on This PC's Tailscale IP (fake servers); the login page of a real Laravel app (Inertia, Vite dev) opened in full, without ever being submitted. Not tried yet: a real login to a site through this path, HTTPS in front of the hub through a browser, and a site on another tailnet node.

## 5. Documents (E2)

```jsonc
{ "id": "doc_…", "projectId": "p_…", "type": "architecture", "title": "Architecture", "ask"?: "…",
  "versions": [{ "id": "v_…", "at": "…", "agent": "claude", "machineId": "local", "effort": "balanced",
                 "state": "complete"|"incomplete", "words": 980, "durationMs": 312000, "jobId": "jb_…" }] }
```

- Types: `overview`, `onboarding`, `architecture`, `readme`, `api`, `dataModel`, `security`, `testing`, `releaseNotes`, `custom` (with `ask`). Written in English, Markdown, starting with `# <title>`.
- Effort: `quick` 400–700 words / 6 minutes, `balanced` 700–1100 / 12 minutes, `thorough` 900–1500 / 25 minutes; idle 200 s. Read-only tools.
- Streaming through `jobOutput`; Stop → an `incomplete` version with the text that has arrived (Regenerate, Export draft).

| Method | Path | Request | Response |
|---|---|---|---|
| GET | `/api/projects/{id}/documents` | — | `Document[]` |
| POST | `/api/projects/{id}/documents` | `{"type","ask"?,"effort","agent"?,"profileId"?,"documentId"?}` | `202 {"document","versionId","job"}` |
| GET | `/api/documents/{docId}` | — | `Document` |
| GET | `/api/documents/{docId}/versions/{vid}` | — | `{"version","markdown"}` |
| DELETE | `/api/documents/{docId}`, `/api/documents/{docId}/versions/{vid}` | — | `{"ok":true}` (permanent) |
| GET | `/api/documents/stats?machineId=&agent=` | — | `{"quick":{"medianMs":…,"runs":5},"balanced":…,"thorough":…}` (the last 5 finished runs) |

**PDF (in the client):** route `/print/documents/<docId>/<vid>?paper=a4|letter` with a print stylesheet: always the light theme, a cover page (title, agent, project, date), a table of contents when there are ≥ 3 headings, a running header, page numbers via `@page` margin boxes, `h1` starts a new page, images become labeled placeholders. Web/desktop: `window.print()`; Android: `PrintManager` + WebView. Also Export Markdown, Copy Markdown, Add to board (a `document` node). The Markdown renderer has no raw HTML.

### 5.1 Hub implementation (Phase 7 part B) — deviations and verification

`internal/studio` (`documents.go`), endpoints in `internal/server/h_studio.go`. Job = runner purpose `document` (`claude -p --output-format stream-json --verbose … --tools Read,Glob,Grep --allowedTools Read,Glob,Grep`, Codex `exec --sandbox read-only`), time limit per effort, idle 200 s, an English prompt with the word range and `# <title>`.

- **A `running` version** (an addition to the `state` enum): the version is recorded before the job starts (with `jobId`) and completed in `OnFinish`: `complete` with text, `incomplete` with partial text (Stop/timeout/failure), deleted when there is no text at all. When the hub starts, a `running` version → `incomplete` (when there is a file) or deleted. `words` does not count Markdown markers (`#`, `-`).
- `documentId` (Regenerate) uses that document's type and `ask`. Agent: request → `settings.runner.defaultAgent` → Codex when Claude is not on the machine; anything other than claude/codex → `400 runner_unsupported`. Profile: request → default profile `settings.agents.profiles` → default login; a profile that does not match → `400 profile_mismatch`. Other errors: `400 bad_type|bad_effort|bad_ask`, `404 document_not_found|version_not_found`.
- Extra event `documentRemoved {projectId, documentId}` on `DELETE /api/documents/{docId}`.
- **Verified for real** (This PC, Claude Code 2.1.312, `--model haiku`, 1 run, a scratch repo of 2 files): `done` in 24.7 s, 342 words (below 400 — the word range is only guidance), phases `launching → thinking → writing → finishing → done`. `stream-json` sends text per assistant message, so this run produced **one** `jobOutput` chunk; per-token streaming needs `--include-partial-messages` (not used/verified). Claude creates an empty folder `~/.claude/projects/<repo slug>/memory/` even with `--no-session-persistence` (CLI behavior; the test folder was deleted again). The checksum of `~/.claude/settings.json` is unchanged.

## 6. Diagrams (E3)

```jsonc
// nb.diagram/v1 — runner output, validated by the hub
{ "schema": "nb.diagram/v1", "title": "Request lifecycle", "direction": "TB",     // "TB" | "LR"
  "nodes":  [{ "id": "api", "label": "API server", "note"?: "Go net/http", "shape": "box"|"round"|"ellipse"|"diamond", "group"?: "backend" }],
  "edges":  [{ "from": "web", "to": "api", "label"?: "HTTPS", "style"?: "solid"|"dashed" }],
  "groups": [{ "id": "backend", "label": "Backend" }] }
```

- Limits: nodes ≤ 40, edges ≤ 80, groups ≤ 8, `label` ≤ 48, `note` ≤ 80, id `^[a-z0-9_-]{1,32}$`, an edge must point to an existing node. An edge leaving a `diamond` without a label → a warning.
- Subjects: `architecture`, `userFlow`, `dataFlow`, `components`, `requestLifecycle`, `stateFlow`, `custom`. Effort: 6–10 / 8–14 / 10–16 components. Style `ink` (default, lines in the text color, transparent fill) or `color` (a color per group).

| Method | Path | Request | Response |
|---|---|---|---|
| POST | `/api/projects/{id}/diagrams` | `{"subject","ask"?,"effort","style","agent"?}` | `202 {"diagramId","job"}` → result `nb.diagram/v1` |
| GET | `/api/projects/{id}/diagrams` | — | `[{ "id","title","createdAt","agent" }]` |
| GET | `/api/diagrams/{id}` | — | `{"id","diagram","style","createdAt"}` |

**Landing on the board (web client):** its own layered layout (the same as the hub's, no library) → one ops request: `shape` nodes (`box`→sharp rect, `round`→round rect, `ellipse`, `diamond`) with `text`; `line` nodes with `arrowEnd:true` (`dashed` → `strokeStyle:"dashed"`) + a `text` node at the midpoint for the label; group zones = a `shape` rect with `strokeStyle:"dashed"` behind, when there are ≥ 2 groups; a `text` title on top. Every node gets `data.diagramId` + `data.diagramKey`. The result is loose shapes (no diagram editor). PNG (2×) and Excalidraw JSON exports are made from the JSON as generated. Starters without AI (Flow, Steps, Decision, Mind map) = `nb.diagram/v1` templates in the client.

**Readable results.** Web (`web/src/studio/diagrams/layout.ts`) and hub (`internal/studio/layout.go`) use the same rules and numbers:

- **Frames fit the text.** Sizes are computed from the wrapped text, using the measured glyph widths of Caveat and its real size (`.font-hand`: "m" 25 px, line 31.25 px). Text in a `diamond` or `ellipse` sits in a rectangle that fits inside its outline (`.shape-label.in-diamond`, `.in-ellipse`).
- **Arrows avoid things.** Arrows that can be straight are drawn first, with their labels. The rest curve as little as possible and keep 12 px away from other tiles and from those labels. Arrows stop at the real outline of the shape; two arrows between the same tiles run side by side.
- **Labels beside the arrow,** in the nearest empty spot, not on top of it. Nodes carry `backed: true`: a background in the board's color, so an arrow forced to pass (can happen in a very dense diagram, because one curve does not always get through) is behind the frame and does not strike through the text.
- **Export** to PNG and Excalidraw uses the same layout and wrap.

**Arrow style.** Two choices in the Diagrams panel, separate from `Shapes` (Ink/Colorful), apply to whatever lands or is exported next (run results, starters, Earlier diagrams), and are remembered per browser (`nb.diagram.arrows`, `nb.diagram.arrowColor`). They do not change the diagram JSON, so an old diagram can be landed again in another style without a new run. Export from the right-click menu on the board follows how that diagram was landed, not the panel's choice.

- **Arrows:** `curved` (default, as above) or `wiring`, like a wiring diagram: `web/src/studio/diagrams/wires.ts` and `internal/studio/wires.go`, same rules and numbers.
  - A 16 px grid, one search per arrow with a cost for bends and crossings. Short arrows first, then all of them once more knowing the others. 16 px away from tiles that are not their own.
  - Arrows from one source with the same line style share a way out and split at a dot (`dots`); a crossing without a dot means not connected. Arrows from other sources do not share a path, unless there is really no room (rare, only in very dense diagrams). Each arrow has its own entry point at the target tile.
  - The layout adjusts: the gaps between layers and between nodes leave room for routes, each layer shifts toward what it connects to so arrows are straighter, and tile centers fall on grid lines. Because of this, tile positions differ from `curved`.
  - Labels beside the arrow, on an unshared segment when there is one, then the arrows are rerouted away from the labels and the labels are placed once more.
  - Lines `roughness: 0` (clean), frames keep the hand-drawn style.
- **Arrow color:** `mono` (one ink, following the theme) or `color`: one color per source so arrows can be followed, labels take their arrow's color. When `Shapes` = Colorful and the source has a group, the arrow uses that group's color. The `DIAGRAM_WIRE_COLORS` palette repeats after 6 sources. The colors are theme tokens, readable in dark and light.
- **On the board:** `line` nodes carry `route` + `dots` ([API-board.md](API-board.md)). Knobs: the two ends, plus one per segment between two corners to move that segment. Arrows stay loose: moving a tile does not take its arrows along, the same as `curved`. Clients that do not know `route` yet (Android, desktop) draw a straight line through `points`.
- **API:** `POST /api/diagrams/{id}/land` accepts `"arrows"` and `"arrowColor"`; other values are ignored. Without `arrowColor` arrows follow the shape color, as before this choice existed.

### 6.1 Hub implementation (Phase 7 part B) — deviations and verification

`internal/studio` (`diagrams.go`, `layout.go`). Runner purpose `diagram` with `--json-schema`/`--output-schema` = a strict schema (every field required; optional text as `""`, which suits Codex structured output). The output is validated by the hub (`ValidateDiagram`): the §6 limits strictly; loose normalization of shape aliases (`pill`/`rounded` → `round`, `circle` → `ellipse`, `rect` → `box`, `decision` → `diamond`), direction `TD`/`DOWN` → `TB`, `dotted` → `dashed`, empty fields dropped.

- **One retry:** `internal/jobs/runner.go` now runs `Transform` inside the attempt loop, so output whose JSON is valid but breaks the shape/limits (e.g. 41 nodes) counts as `bad_output` → one retry → `failed bad_output` (also applies to `memory-scan`/`memory-refresh`, per §2).
- Record `diagrams/<projectId>/<id>.json`: `{id, projectId, subject, ask, effort, style, agent, title, createdAt, state: running|done|failed|cancelled, jobId, error, warnings, diagram}`; `GET /api/diagrams/{id}` returns this record; the list adds `subject`, `state`. Hub restart → `running` becomes `failed`. Errors: `400 bad_subject|bad_effort|bad_style|bad_ask`, `404 diagram_not_found`.
- **Extension:** `POST /api/diagrams/{id}/land {"workspaceId","x"?,"y"?,"style"?}` → `{"rev","nodeIds"}`: landing in the hub (for Android/command bar/tests) with its own layered layout (longest-path + barycenter, no elkjs), one ops batch. `diagramKey`: `node:<id>`, `edge:<from>><to>:<i>`, `edgeLabel:<i>`, `group:<id>`, `groupLabel:<id>`, `title`. Zones only when there are ≥ 2 groups and they do not collide (with each other or with another group's nodes). Ink = `#e6eaf2` (web palette), `color` = a palette color per group. Without `x/y` → to the right of the board's content. `409 diagram_not_ready`, `404 workspace_not_found`.
- No real diagram run (the budget of 4 runs went to usage and documents); schema + validation tested with a fake CLI.

## 7. Code studio (E4)

Extensions of the Phase 4 file API:

| Method | Path | Request | Response / error |
|---|---|---|---|
| GET | `/api/machines/{id}/fs/tree?path=&hidden=0` | one level (lazy) | `{"path","entries":[{name,dir,size,mtime,hidden,binary}],"pruned":[…],"truncated"}` (≤ 5000 entries) |
| POST | `/api/machines/{id}/fs/mkdir` | `{"path"}` | `Stat`; `409 exists` |
| POST | `/api/machines/{id}/fs/rename` | `{"from","to"}` | `Stat`; `409 exists` (does not overwrite) |
| POST | `/api/machines/{id}/fs/copy` | `{"from","to","unique"?}` | `Stat`; `409 exists` without `unique`, with `unique` uses `name-2.ext`; `400` for a folder into itself. See §7.9 |
| POST | `/api/machines/{id}/fs/trash` | `{"path","projectId"}` | `{"trashedTo"}`: git repo → `<project>/.git/nextboard-trash/<ts>/<rel>`, not git → `<project>/.nextboard-trash/<ts>/<rel>`; outside the project → `403 outside_project` |
| POST | `/api/machines/{id}/fs/stat-batch` | `{"paths":[…]}` (≤ 200) | `Stat[]` |

- Default prune: `.git node_modules build dist .next .nuxt .svelte-kit venv .venv target vendor coverage DerivedData Pods __pycache__ .cache .turbo .gradle .idea`; dotfiles are hidden except `.gitignore .env.example .editorconfig .github .claude`. `hidden=1` shows everything.
- Save uses `fs/write` with `ifMtime`; conflict → banner "Changed on disk: Reload · Keep mine". BOM and CRLF are kept as they are; non-UTF-8 files (`binary:true`) are read-only. A real path (realpath; SFTP `RealPath`) outside the project → saving needs `allowOutside:true` (the UI asks once per studio session). SSH without SFTP → writes `501 no_sftp`.
- Web editor: **Monaco** (`monaco-editor`, the VS Code editor, bundled locally and loaded only when the workbench opens), see §7.2. CodeMirror 6 is still used by the Markdown studio and Skills. Files ≤ 1 MB can be edited, 1–10 MB read-only, > 10 MB are not opened; drafts in IndexedDB per (machine, path), restored.
- To the board: Insert = a `file` node with view `code` (a snapshot, including unsaved edits); Add folder = view `folder`.

### 7.1 Hub implementation (Phase 7 part B) — deviations and verification

`internal/studio/tree.go`, `watch.go`; new file operations in `internal/machine/studio_fs.go` as package functions (`machine.Rename/Remove/RealPath/Lstat`, the `Host` interface does not change; SSH through SFTP, no exec fallback → `501 no_sftp`).

- `fs/tree`: `binary` from the extension (images, archives, objects, media, fonts…), `pruned` = hidden names (prune folders + dotfiles), `.nextboard-trash` is pruned too. The hub's data on This PC → `403`.
- `fs/rename` never overwrites: local file = `link`+`unlink` (fallback `rename` when the hard link fails), folder = an `lstat` check, then `rename`; SFTP = `SSH_FXP_RENAME` + check. `fs/trash`: a `rel` in `.git` or in the trash is refused, the project folder itself is refused, `409 exists`.
- `fs/write` (Phase 4) gains `projectId` + `allowOutside` (realpath outside the project → `403 outside_project`) and `confirm` (§9.2 for the Claude config folder on an SSH machine: `~/.claude` and every Claude profile on that machine; also `fs/mkdir`, `fs/rename`). The file API's error body now carries `code` `mtime_mismatch` / `no_sftp`.
- **Extensions:** `GET /api/machines/{id}/fs/find?root=&q=&limit=50` (file name search, every word must be in the relative path, prune rules, ≤ 20 000 entries / 10 s → `truncated`); **polling watch**: `POST /api/machines/{id}/fs/watch {"paths"(≤ 200),"ttlSec"(≤ 300, default 30)}` → `{"watchId","stats","intervalMs":3000}`, `POST /api/fs/watch/{wid}/renew`, `DELETE /api/fs/watch/{wid}`; the hub polls stat every 3 s and sends `fsChanged` when size/mtime/existence changes (≤ 64 watches).
- Tests: local (`internal/studio`), SFTP (`internal/machine/studio_ssh_test.go`, `NEXTBOARD_SSHD_TEST=1`), API (`studio_api_test.go`: stale `ifMtime` → `409`, a symlink leaving the project → `403`, then `allowOutside`).

### 7.2 Code workbench

The Code studio is a window-wide workbench over the board: activity bar, Explorer, Search, Source Control, Agents, Settings, editor groups (at most 3), bottom panel, status bar, Go to file, command palette. Behavior reference: the Files page of DevWorkspace Pro.

| Method | Path | Request | Response / error |
|---|---|---|---|
| GET | `/api/projects/{id}/files` | | `{"items":[rel…],"truncated","source":"git"\|"walk"}` (≤ 200 000). Git repo: `ls-files -co --exclude-standard` without files already deleted. Not a repo: `find` with the §7 prune rules. |
| POST | `/api/projects/{id}/files/search` | `{"query","matchCase","wholeWord","regex","include","exclude","limit"}` | `{"files":[{"rel","matches":[{"line","col","len","text","textCol"}]}],"count","truncated","engine"}`; `400 bad_pattern` |
| POST | `/api/projects/{id}/files/replace` | search fields + `{"replacement","paths":[rel…],"confirm"}` (≤ 2000 files) | `{"files":[{"rel","count","error"}],"count"}`; `403 outside_project` for paths that leave the project |
| POST | `/api/projects/{id}/files/ignored` | `{"paths":[rel…]}` (≤ 5000) | `{"items":[rel…]}` from `git check-ignore`: once in a project that is a work tree, or per path folder in a project that is not (repositories below a parent folder) |
| POST | `/api/projects/{id}/files/on-save` | `{"path":rel}` | `{"runs":[{"pattern","command","exitCode","timedOut","output"}]}` |
| GET | `/api/projects/{id}/git/show?path=&ref=HEAD` | `path` relative to the repo | `{"path","ref","exists","binary","truncated","content"}`; a file not in the commit = `exists:false`; `400 bad_ref` |
| POST | `/api/sessions` | `+ "panel": true` | a session without a board node, for the bottom panel's terminal; cannot be reached by orchestration orders and does not go into the live chips |

- **Search:** runs on the project's machine through `rg`, then `git grep` in a work tree, then `grep`. Columns are computed by the hub in UTF-16 units so they are the same for every engine. Binaries and ignored files are skipped; the 1 MB per file limit only applies with `rg`. At most 5000 results, 30 s. The regex follows the engine's syntax; column positions use RE2.
- **Replace:** the hub reads, replaces and writes with `ifMtime`, other bytes do not change (CRLF and BOM are safe). Files behind a link leaving the project fail per file. The UI skips files that have unsaved edits.
- **Security:** git is called with `core.fsmonitor=false` and `core.hooksPath=/dev/null`, so opening a project runs no code from the repo config. Git panel and home screen: §3.
- **Settings:** `editor.onSave` = `[{"pattern","command"}]` (≤ 50). A pattern without `/` matches the file name, with `/` from the project folder; `{file}` = the full path (quoted). Run in order in the project folder, stopping at the first failure, 60 s per command. The Android client does not use it yet; this field is additive.
- **Assets:** text `assets/*` are sent gzipped (compressed once when first requested), because Monaco's workers are megabytes in size. The hub binary grows by about 14 MB.

**Git history** (`CommitGraph.tsx`, `commitGraph.ts`, `CommitView.tsx`). The History section in the Source Control view draws commits on colored lanes, with branch and tag labels. A local and a remote branch with the same name become one label. A hollow dot means not on the remote yet, a ring marks the one checked out. The *All branches* / *Current branch* buttons choose the scope, *Show more* loads the next 100, up to 1000. Clicking a commit opens one preview tab holding the full message, author and committer, hash, parents, change counts, file list, and the diff of the chosen file. The next click uses the same tab, a double click pins it. Commit tabs are not restored when the board opens again. Commit text is shown as plain text.

**Advanced git** (`ScmPanel.tsx`, `StashSection.tsx`, `mergeConflicts.ts`). `Status.operation` holds `merge`, `cherry-pick` or `revert` while that operation is running; the panel shows a banner with Abort and Continue. Once the index holds something, the panel separates **Staged** and **Changes**, and the commit button takes only what is staged. Diff tabs have Stage, Revert and Unstage per change. Revert goes into the editor, so Ctrl+Z undoes it. Conflicts are colored in the editor with Accept Current, Incoming and Both above each conflict, then Mark as resolved and Continue. Right-clicking a commit in the graph offers checkout, branch, tag, merge, cherry-pick, revert and reset; reset hard asks for confirmation. A stash shows its content like a commit tab. Pull requests, Stash and History are only mounted after the status says `isRepo:true`. That way, a panel opened before the status arrives (a view saved from the last visit) does not ask for lists from a folder that is not a repository (`409 not_repo`). Tested in `e2e/studio-git.spec.ts`.

### 7.3 Language server

| Method | Path | Response / error |
|---|---|---|
| GET | `/api/projects/{id}/lsp` | `{"items":[{"id","name","languages","available","command","install","extra"?,"plugin"?}]}` |
| GET (WebSocket) | `/api/projects/{id}/lsp/{server}/ws` | `404 lsp_not_found` before the upgrade; one JSON-RPC message per text frame; close `1011` carrying the tail of stderr when the server dies |

Catalog rows with `extra: true` (Tailwind CSS) work alongside the main server of their language and live only while their extension is installed (§7.6). Completion and hover are merged from every server; diagnostics have a marker owner per server. Other features still come from the main server.

The Vue server from version 3 on does not read `<script>` itself. The `vue` catalog row reports `plugin`, its package folder holding `@vue/typescript-plugin`; empty for version 2, which still uses `hybridMode: false` and reads the script itself. As long as there is a `plugin`, opening a Vue file also starts `typescript-language-server` with that plugin and opens the same file there; a server already running without the plugin is restarted, because the plugin is only read at start. `tsserver/request` notifications from the Vue server are forwarded by the client through `workspace/executeCommand` `typescript.tsserverRequest`, and the reply comes back as `tsserver/response`, with the value `null` when the TypeScript server is not ready yet so the request does not hang. For Vue files, signature help, definition, references, highlight and rename use the first non-empty answer; code actions are merged from both servers.

`typescript-language-server` needs `lib/tsserver.js`, which is not in TypeScript 7, so the catalog's install command uses `typescript@6`. The hub counts it as `available` only when that file is in the project or next to the server; otherwise the editor keeps using Monaco's built-in per-file service. A project whose `node_modules` holds TypeScript 7 is still served by the global TypeScript 6. Monaco reads its built-in service settings only once, so a server that dies while running can only be replaced by the built-in service by reloading the page.

`gopls` loads every package through `go`. Without `go` it still lives but only reports `go command required` for every opened file, so the hub counts it as `available: false`, and that row's `install` becomes `sudo apt install golang-go`. The search for `go` uses the same PATH as the server: the login shell's PATH plus the folders below. A Go registered only in `~/.bashrc` in another folder is not seen; move its PATH line to `~/.profile`.

The hub runs the project machine's server in the project folder (local or SSH, a non-PTY channel) and bridges its stdio with `Content-Length` framing. The server lives as long as its connection. Nothing is installed automatically; the Install button in Settings only types the command into a terminal. The project's `node_modules/.bin` is deliberately not on PATH, but the server itself can run project tools (e.g. `go list`, a Rust build script). `$HOME/go/bin`, `.cargo/bin`, `.local/bin`, `.npm-global/bin`, then the Go tarball folders (`/usr/local/go/bin`, `$HOME/.local/go/bin`, `$HOME/.go/bin`) are appended to PATH, also for debug adapters and ESLint. CSS, HTML and JSON use Monaco's built-in services; TS/JS fall back to the built-in per-file service when `typescript-language-server` is missing.

The client (`web/src/studio/code/lsp.ts`) is written in-house because `monaco-lsp-client` locks `rootUri: null` and sends every model. Features: completion, hover, signature help, definition, declaration, type definition, implementation, references, highlight, document and workspace symbols, rename, code actions, formatting, inlay hints, diagnostics, progress.

### 7.4 Edit with AI

`POST /api/projects/{id}/code/edit` `{"path","language","instruction","before","selection","after","agent","profileId"}` (≤ 256 KB of code) → `202 Job` purpose `code-edit`, read-only, 3 minutes. The `structured.replacement` result is shown as a diff; the UI applies it only after Accept, and one Undo reverts it. Ghost text: `POST /api/projects/{id}/code/complete` `{"path","language","before","after"}` → `202 Job` purpose `code-complete` without tools, 30 s, model `haiku` for Claude; the result is in `result.text`. Off by default, can be turned on only for Alt+\\ or on a typing pause, because each suggestion runs the CLI (a few seconds, uses quota). Typing again cancels its job.

Asking about code without a console: `POST /api/projects/{id}/code/ask` `{"question","path","language","selection","startLine","endLine"}` → `202 Job` purpose `code-ask`, read-only, streaming `jobOutput`, 5 minutes.

### 7.5 Debugger

| Method | Path | Response / error |
|---|---|---|
| GET | `/api/projects/{id}/dap` | `{"items":[{"id","name","types","languages","available","command","install"}]}` |
| GET (WebSocket) | `/api/projects/{id}/dap/{adapter}/ws` | `404 adapter_not_found`, `502 adapter_failed`; one DAP message per text frame. `?inspect=<port>` for `node` only: attach to the inspector on that port |
| GET | `/api/projects/{id}/dap/node/processes` | `{"items":[{"pid","command","cwd"}]}`: node processes of this user on the project's machine, ≤ 100, those in the project folder first. `501 no_procfs` |
| POST | `/api/projects/{id}/dap/node/inspect` | `{"pid"}` or `{"port"}` → `{"port","opened"}`. `404 no_process`, `409 not_node`, `409 not_inspector`, `502 inspector_failed`, `502 forwarding_disabled` |

Catalog: `go` (Delve, `dlv dap --listen`, through loopback TCP or `direct-tcpip`), `python` (debugpy), `node` (the hub's built-in adapter, `internal/nodedap`, runs on This PC; on a server attach only), `native` (lldb-dap, lldb-vscode, `gdb --interpreter=dap`), `php` (php-debug-adapter). Only adapters already on the machine are used; an adapter lives as long as the connection, and only runs when the user presses F5. Configurations are read from `.vscode/launch.json` (JSONC, VS Code format) plus automatic entries for Go and Python; variables `${workspaceFolder}`, `${file}`, `${relativeFile}`, `${fileDirname}`, `${fileBasename}`, `${fileBasenameNoExtension}`. The reverse requests `runInTerminal` and `startDebugging` are refused, so js-debug (Node) is not supported yet.

**Node.js** does not need `js-debug`: `nextboard dap-node` runs `node --inspect-brk=127.0.0.1:0` and translates DAP to the Node inspector. Supports breakpoints, conditions, logpoints, stepping, call stack, variables, evaluate (hover without side effects) and exceptions. `.ts` runs as long as its Node can run it by itself (Node 24). **Source maps** (v3, a `.map` file next to it or an inline `data:`, `sourceRoot` respected): breakpoints in a source file (`.ts`, or a bundle's source) are set in the generated script, and the call stack points to the source file. Generated scripts not loaded yet are looked up in the launch.json `outFiles` folders, or in `cwd` when there are none (without `node_modules` and dot folders, ≤ 5000 files, scripts ≤ 20 MB), then breakpoints are set by their URL, so CommonJS modules loaded later are hit too. A mapped script loaded without being found first pauses briefly before running (`beforeScriptWithSourceMapExecution`) so its breakpoints get set. A line with no code of its own (e.g. `interface`) lands on the next line with code, at most 20 lines away. Stepping still runs in the generated code. **Child processes:** each `node` process the program starts (`fork`, `spawn`, `exec` and so on) becomes a thread in the same session, with its own breakpoints, exceptions, stack, variables and evaluate; one thread stopping does not stop the others. How: the session's `NODE_OPTIONS` loads a bootloader (`--require`, in the session's 0700 folder). In the program itself, the bootloader only drops the `--inspect*` flags from `process.execArgv`, because before, a `fork` child inherited `--inspect-brk` and waited for a debugger forever. In other processes, the bootloader opens an inspector on `127.0.0.1` at a random port, writes its address to the session folder, then waits at most 10 seconds for the adapter to attach and its breakpoints to go in. If nothing answers, that process carries on without a debugger. The adapter only attaches to inspectors on loopback. Not yet: attaching to an already running process, and worker threads. During a session, the inspector is open on a random loopback port, so on a shared host other users on that machine can reach it.

**Attach to an already running process** (`"request":"attach"`, `type:"node"`), on This PC as well as on a server. The adapter stays on This PC, so the server needs nothing, not even `node` on its login PATH. With `port`, that inspector is used. Without `port`, the person picks a process from a list, from the automatic entry "Attach to a running Node.js process".

- **Opening the inspector:** when the process does not have one yet, the hub sends `SIGUSR1`. That signal kills a program that does not handle it, so right before it is sent `/proc/<pid>/exe` is checked to be `node` or `nodejs`, and that `readlink` itself only answers for this user's processes. The inspector port is found from the process's sockets (`/proc/<pid>/fd` and `net/tcp`), then each port is asked `GET /json/version`, so an application server in that process may receive that one request. Without `/proc` (macOS), only `port`.
- **Node only:** the `/json/version` answer must be `node.js/…`. Browser debugging ports answer the same request, and are refused.
- **Security:** while the inspector is open, anyone with a login on that machine can run code in that process, and the picker dialog says so. An inspector opened by this session is closed again when the session ends (`process._debugEnd()`), including when the browser goes away. If the hub or the adapter dies suddenly, the inspector stays open until the process stops. On the This PC side there is no TCP port: the adapter reaches the inspector through a unix socket in a 0700 folder (`NEXTBOARD_INSPECT_SOCKET`), forwarded through `DialLoopback`. On a server it needs `AllowTcpForwarding`.
- **Stopping does not kill:** `terminate` and `disconnect` release the process, resume it if it is paused, and the breakpoints go away with the session. A process that stops by itself ends the session.
- **Output:** only `console.*` calls through the inspector, from the moment of attach. What the program writes straight to stdout is not seen.
- **Limits:** source maps are read from This PC's disk, so on a server breakpoints and the stack use the files actually run. Child processes are not included. A program in a container with its own network namespace cannot be reached.

### 7.6 Extensions

| Method | Path | Response / error |
|---|---|---|
| GET | `/api/extensions` | `{"items":[Extension]}` |
| GET | `/api/extensions/search?q=` | `{"items":[…],"hidden","unchecked"}` from Open VSX: only those that can be installed here, each item with `hasCode`, `native`, `gives` |
| POST | `/api/extensions` | `{"namespace","name"}` → `201 Extension` (`native` when the editor does its function itself); `422 extension_code_only`, `400 bad_extension`, `404 extension_not_found`, `502 extension_failed` |
| DELETE | `/api/extensions/{id}` | `{"ok"}` |
| GET | `/api/extensions/{id}/file?path=` | the data file's content, `text/plain`, CSP `sandbox` |
| POST | `/api/projects/{id}/eslint` | `{"path","text","fix"?}` (≤ 8 MB) → `{"available","error"?,"messages"?,"output"?}`; `400 bad_path` |

Monaco has no extension host, so **extension code never runs and is not stored**. Only data files (`.json`, `.jsonc`, `.code-snippets`, `.tmLanguage`, `.tmTheme`, `.plist`, `.tmSnippet`, `.svg`) are unpacked to `<data>/extensions/<namespace>.<name>/`, package ≤ 100 MB, content ≤ 64 MB. What is used: color themes, file icon themes (SVG through `<img>`, so their scripts do not run; font- or PNG-based icons not yet), snippets, TextMate grammars (`vscode-textmate` + Oniguruma WASM), language definitions. Packages that hold only code are refused, except those whose function the editor does itself while the extension is installed (the `extNative` table in `internal/studio/extensions.go`):

| Extension | `native` | What the editor does |
|---|---|---|
| Vim (`vscodevim.vim`) | `vim` | `monaco-vim` in each editor, a status line below the editor, `:w :q :wq :x`. Workbench shortcuts (Ctrl+P, Ctrl+Shift+P, Ctrl+S) still win in normal mode. `vim.*` settings and Vim plugins are not there |
| Prettier (`esbenp.prettier-vscode`) | `prettier` | The real Prettier in the browser (`prettier/standalone`) as the only formatter for the languages it knows (selector `exclusive`). Reads `.prettierrc` (JSON, YAML, flat TOML), `package.json#prettier`, `overrides`, `.editorconfig`, `.prettierignore`. JavaScript configs and third-party plugins are project code, so they are not run |
| ESLint (`dbaeumer.vscode-eslint`) | `eslint` | The hub runs the `node_modules/.bin/eslint` nearest to the file, or the machine's `eslint`, with the editor content through stdin. Markers, quick fixes, disable a rule per line, fix all. An ESLint config is code, so it **only runs after the user allows it per project** (status bar or palette, kept in the browser) |
| GitLens (`eamodio.gitlens`) | `gitlens` | Blame of the active line and a commit hover (commit text escaped, only one trusted command), an author CodeLens at the top of the file, whole-file blame, file and line history, diff per commit. Commit graph, Launchpad, worktrees and Pro features are not there |
| Error Lens (`usernamehw.errorlens`) | `errorlens` | The most severe problem's message at the end of the line and the line colored, from every marker |
| Python, Go, rust-analyzer, Tailwind CSS, Vue, Even Better TOML | `lsp:<id>` | IntelliSense from the §7.3 catalog language server on the project's machine; grammars and snippets from the package. The Extensions panel shows the server's status and an Install button that types its command into a terminal. Vue uses `@vue/language-server@2` with `hybridMode: false`. Features that need extension code (running a test from rust-analyzer's CodeLens, the Python venv picker, `Go:` commands) are not there |

Material Icon Theme needs no row of its own: an icon theme is data, so other SVG icon themes work as well.

Open VSX search results do not say what a package holds, so the hub asks for 50 hits, reads each hit's `package.json` (next to the `.vsix`, 8 at a time, a total limit of 25 s, the result remembered per version while the hub lives), and shows at most 24. Only those whose main function really works are shown: in `extNative`, a code-free package that declares themes, icon themes, snippets or grammars with a `language` and data files, or a package in the `Themes` category whose code is only an extra. A language or tool package whose main function is in code stays hidden even when its grammar could be installed (`POST /api/extensions` still accepts it), and so do packages > 100 MB (`HEAD`, without downloading). `hidden` counts the hidden ones, `unchecked` those whose manifest failed to read; when all of them fail, the response is `502 extension_failed`. A package whose manifest promises files that turn out not to exist is only found out on install (`422 extension_code_only`). Grammars without a `language` (injections) do not count, also on install, because Monaco cannot use them. Font- or PNG-based icon themes pass the filter and refuse to be used, with a message, when chosen.

### 7.7 Card on the board

The workbench is an `editor` node (`board/tiles/EditorTile.tsx`), not an overlay. The Fill button enlarges the card to the width of the view at 100 % zoom and restores it. A wire `editor → terminal | group | orchestrator` sets where questions from the Ask panel go; `POST /board/ops` validates it. Other clients that do not know this kind yet show an unsupported tile.

**Cards by placement** (v0.9.0-beta.2, `board/editorCards.ts`). A card is placed like a console: `data.part` names a part (its id), and absent, `""` or an unknown id is the **Root** card of a project of several parts, one tree with each part as a top-level folder. A single-part project has one placement; its cards look and persist exactly as before. A board holds one card per placement: a second card of the same placement shows "already open, Show it" with a button that frames the first. Tabs, layout, explorer, git selection, review and debug state belong to the project and the placement (`wb.<viewKey>`), not to the card or the board, so the same placement on two boards is one view. Two cards showing one file share one model and its unsaved edits. The card's label and title name the placement: "Code editor · admin", "Code editor · All parts", and `acme-shop · admin` in its title bar; the status bar of a Root card names the part of the active file.

- **Which card opens.** The board's `openCode` (`BoardView.tsx`) decides:
  - A file (a file card's "Open in the editor card", the lead's `editor.open` with `UIAction.part`, the Git panel): the card of the file's part (its `partId`, else the part holding the path on its machine), else a Root card, else a new card placed in that part (a file outside every part: the Root). Nothing is asked.
  - A placement (an agent pane, a commit from the Git panel, the lead's `openPanel code`): that card, a Root card for a pane or a commit (it lists every part), else a new card. The lead's `open code` is the Root card.
  - Neither (the rail's Code editor, the studios menu's Code): in a project of several parts a small dialog asks **Show: Root (all parts) / <part>**, with the focused tile's placement (else the last used) chosen; the card of that placement comes into view, or a new one is added. A single-part project shows its one card, or adds it.
- **`ui.openFile`** carries `nodeId` (the card that takes it), `machineId` and `partId`; the card opens `(machineId, path)`. A request without `nodeId` (an older sender) goes to the board's first editor card.
- **Agent panes** (`agents/jump.ts`): every editor card of the board registers how to show a pane; a jump asks each card, and when none has the pane the board opens the card of the session's placement (else a Root card), which then shows it. A pane is handed only to a card of its placement, or to a Root card.
- **The Git panel's "Open in editor"** (`git/openInEditor.ts`) opens the commit in the view of the card the board picked, with that part's repository.
- **Ports panel → web tile:** the tile shows the page from the machine the ports were listed on (the active part's, or the section's when the panel names one), with that part's `partId`.

### 7.8 Agents inside the editor

Agent consoles and the orchestrator can be opened inside the editor card, without a tile on the board. Code: `web/src/studio/code/AgentPanes.tsx`, `Workspace.placePane` and friends.

- **Orchestrator view:** one column at most 800 px wide in the middle of the pane, so it stays readable in a wide tab and in the side bar. The header says how many consoles listen to this thread; the thread choice only appears when the board has an orchestrator card. An empty thread offers three suggested commands, which follow the open file when there is one; a click only fills the command box, it does not send. Running consoles show as chips above the command box; a click inserts `@handle`.

- **Where:** the right side bar (a button in the title bar, `Ctrl+Alt+B`, its width can be changed) or an editor tab (the agent icon in the tab strip, follows splits). A pane is only in one place: a button in the side bar moves it to a tab, the tab menu moves it back. Palette: *Agents: Open … in the Side Bar / in a New Tab*. The layout is kept per browser, like file tabs.
- **Console:** `POST /api/sessions` with `panel: true` and an agent from the project machine's catalog. An ordinary session without a board node: status, needs-you, live chips and the Claude IDE connection (`docs/API-agents.md` §24) still apply. "Show" on needs-you and `?focus=<id>` open the card, then bring up its pane (`agents/jump.ts`). An editor agent with no pane in this browser, for example one opened from another browser, is offered as *Show @name*.
- **Closing:** a console's close button asks for confirmation, then deletes its session; its conversation stays saved by the CLI. Hiding the side bar stops nothing. Close Others and Close All skip panes.
- **Orchestrator:** the project thread or the thread of an orchestrator card on the board, with the same `@mention` input and confirmations as the command bar. Files or selected text go along as the order's context, and their chips can be removed before sending. The thread choice is remembered per board in this browser, so it stays the same after leaving the board or a reload; if the card has been deleted, it goes back to the project thread.
- **Orders to editor agents:** the orchestrator's scope now covers `panel` sessions that are agents. The bottom panel's terminal stays the user's own shell and receives no orders. An orchestrator card on the board only directs the consoles wired to it, so it does not reach editor agents.
- **Alt+K:** an IDE-linked console receives `@file#Lx-y`; other agents in the editor (for example Codex) receive `path:x-y` as text in their prompt, without it being sent.
- **Board deleted:** every `panel` session on that board stops too (`Manager.AllPlacements`). Before, panel terminals were left running.

**Verification (This PC, 2026-11-03):** `e2e-panes` on an isolated test hub with the real Claude 2.1.317 CLI against a fake model server, and real Codex up to the login screen since there were no credentials. Not tried yet: a project on a server over SSH, Claude logged in with a real model, and Codex really answering.

### 7.9 Pasting files and images

Applies to consoles (tiles and the workbench's terminal panel), order inputs (orchestrator card, command bar, agent panel) and the editor. Plain text still works as before.

| Method | Path | Body | Response |
|---|---|---|---|
| POST | `/api/sessions/{id}/attach?name=` | raw bytes, `Content-Encoding: gzip` allowed (max 20 MB) | `Stat` on the machine where the console runs |
| POST | `/api/projects/{id}/attach?name=` | raw bytes, `Content-Encoding: gzip` allowed (max 20 MB) | `Stat` on This PC, where the agent runs |
| GET | `/api/projects/{id}/attach/{name}` | — | that file's bytes, for the thread. `Content-Security-Policy: sandbox`; only names the hub can hand out, in that project's folder, without following symlinks out. The ordinary file API stays closed for the hub's data folder. |

`400` empty file, `404` session/project does not exist, `413` more than 20 MB.

- **The hub picks the location**, the client only sends a name: there is no path that can be aimed at the hub's files or at the project, and the project stays free of temporary files. The name is cleaned (last part only, spaces and punctuation become `-`, no leading `.`/`-`, ≤ 80 characters), so it never needs quoting in any CLI. An existing name is not overwritten (`name-2.ext`).
- **This PC:** `<data>/attachments/<sessionId>/` or `<data>/attachments/project-<projectId>/`, folder `0700`. Kept 7 days (a resumed conversation may still point to it); swept when the hub starts and on each paste, a console's folder goes away when the console is deleted.
- **Server (decision #2):** `/tmp/nb-paste-<uid>-<sessionId>/`, one fixed path per console like the usage folder: created with `mkdir -m 700` only as a real folder owned by the user, a symlink or someone else's folder is refused. Goes away with the console; folders of consoles deleted while the server was unreachable are swept when the server connects again. No record of any kind is needed for a hub that crashes, because the list of live consoles is enough.
- **What an order receives** (client): chips above the input (DESIGN §3.13). The hub has no attachment field: on send, the paths are appended to the end of the order text separated by spaces, because the path is what the agent reads. The thread recognizes these paths in the text (`…/attachments/project-<id>/<name>`, one whole word) and shows them as attachments; *Copy* still copies the original text. The delivery queue and plans show the text as it is.
- **What a console receives** (client): paths are pasted through `term.paste` (bracketed paste when the program asks for it), then a space, one at a time with a 250 ms pause, because the CLI reads the image before showing it. Gemini uses `@path`; the others the path, quoted when needed.
- **Ctrl+V in a console** (not macOS) is now a browser paste, because only the paste event shows the clipboard's content: a file → as above; text in an agent CLI → text (the CLI itself only sees This PC's clipboard, so until now it failed: "no image over SSH"); text in a shell, or an empty clipboard → still `^V`, because vim and readline use it. Pasting text in a shell is still Ctrl+Shift+V.
- **Claude Code** on This PC runs with `--add-dir <data>/attachments`, so pasted files (and paths in orders) can be read without a permission dialog. Other CLIs do not get this flag; when their CLI asks, the permission goes through the console as usual.
- **Order input:** the path goes into the text at the caret position when the upload finishes, so it can be read and edited; Enter and Send wait for the upload to finish. The agent receives that path in the order and reads it itself.
- **Which clipboard is a file:** text plus an image from a spreadsheet counts as text; a file plus its name from a file manager counts as a file; folders are refused with a message.

**Explorer (Code workbench):** Ctrl+C, Ctrl+X, Ctrl+V and a context menu for files and folders, like VS Code. What is copied is kept per browser tab, so it can move between projects on the same machine (between machines is refused with a message); its path also goes onto the clipboard, so Ctrl+V in a console or an order gives the path. A paste goes into the selected folder or the folder of the selected file; a folder pasted into itself lands next to it; a cut into itself is refused; Esc cancels a cut. Ctrl+V also accepts files from the computer (`fs/write` raw bytes, `unique`; text gzipped by the browser when that makes it clearly smaller, images/video/archives as they are). Pasting a file while typing in the editor puts it next to that file and inserts its name; in Markdown it becomes `![name](name)` or `[name](name)`. A single click on a file (preview) no longer moves focus to the editor, so Ctrl+C after a click means the file, like VS Code; a double click and Enter still move.

`fs/copy` uses that machine's own `cp -a` (local and SSH), so no bytes go through the hub, and links, modes and times come along. Never overwrites; a failed `cp` leaves no half-made copy; time limit 5 minutes.

**Verification (2026-11-03):** a real Chrome with a real clipboard. Real Claude Code 2.1.317 and Codex 0.165.0 show `[Image #n]` for images and the path for other files; Claude Code accepts `--add-dir` and really reads files in that folder. Paste and `fs/copy` through a real sshd. **Not verified yet:** Gemini's `@path` (CLI not installed), copying a file from a real file manager (tested with a paste event holding a file; a browser on Linux might give only a list of URIs), macOS.

### 7.10 Reviewing agent changes

The **Review** panel shows the files each agent changed that have not been reviewed yet. The mechanism is a shadow repo per work tree in `<data>/review/<projectId>/`: the real repo is not touched, folders without git are included, and edits not reported by a hook (for example `sed` from a shell tool) are still seen. Projects on a server are included through a copy on This PC (below).

- **Snapshot:** when an agent starts and finishes working, every 3 s while it works, and after each save from the editor. Follows the work tree's `.gitignore`, skips `node_modules` and the like, files above 10 MB, and at most 20000 paths per snapshot.
- **Code only:** images, audio, video, fonts, archives, programs, office documents, databases and models (by extension, case-insensitive), and other files with a NUL byte in their first 8 KB are not in snapshots, review or Local History. SVG is still included (text). An asset diff says nothing and its history is git's business; assets used to be most of the copy's content. On a server, assets are not copied to the mirror. Shadow repos from before this rule are cleaned once: pending changes to assets are released, assets are taken out of every snapshot (code, times and agent names stay; only commit ids change), then deleted from disk.
- **Attribution:** the `Edit`, `Write`, `MultiEdit` and `NotebookEdit` hooks decide the owner. Without a hook, the change goes to an agent working without hooks (Codex), then to one with hooks. More than one candidate is marked `unsure`. What the owner saves from the editor does not belong to an agent, and neither do changes while no agent is working.
- **Two agents:** each agent is compared with the content it found. One file belonging to two agents gives `also` and one `reviewOverlap` event.

| Method | Path | Request | Response / error |
|---|---|---|---|
| GET | `/api/projects/{id}/review` | — | `{"supported","agents":[{"sessionId","name","kind","working","files":[{"path","add","del","status","binary"?,"unsure"?,"also"?,"since"}]}]}` |
| GET | `/api/projects/{id}/review/file?session=&path=` | — | `{"path","base","baseExists","current","currentExists","binary","tooBig"}`; `404 no_change` |
| POST | `/api/projects/{id}/review/accept` | `{"session","path"?,"base"?}` | Without `path`, every file of that agent. With `base`, only part is accepted and review continues from that text |
| POST | `/api/projects/{id}/review/reject` | `{"session","path"?,"text"?}` | The file goes back to the content the agent found; the current content goes into Local History first. With `text`, only part is undone |

The `review` event tells clients to reload. Pending items survive a hub restart. Limits: large files and excluded folders are not reviewed; Bash changes by two agents at once can only be guessed (`unsure`).

**Projects on a server.** Nothing is left on the server, which may be a production server. The hub keeps a copy in `<data>/review/<projectId>/mirror/`, and the shadow repo works on that copy as in a local folder.

- **Copy:** the file list from `git ls-files -c -o --exclude-standard`, or `find` skipping `vendor`, `node_modules` and the like when the folder is not a repo; only files changed by time, size and mode are fetched, through `tar` per at most 1000 files or 32 MB, with `nice -n 19`. Starts when the Code card opens, because what an agent changes before the first copy is complete has nothing to compare with.
- **When it looks:** when an agent starts and finishes, every 10 s while it works (not 3 s), and after the agent's `write_file`, `edit_file` or `bash` tool. That tool also decides the file's owner, like a hook.
- **All or nothing:** a snapshot only runs after the copy is complete. A truncated list or a server that drops changes nothing, so files do not look deleted. The reason shows as `problem` in `GET /review`.
- **Never brought over:** files whose name marks a credential, even when tracked in git: `.env`, `.env.*` (except `.env.example`, `.sample`, `.dist`, `.template`), `*.pem`, `*.key`, `*.p12`, `*.pfx`, `*.keystore`, `*.jks`, `*.kdbx`, `id_rsa`, `id_ed25519`, `auth.json`, `credentials.json`, `.npmrc`, `.netrc`, `.pgpass`, `wp-config.php`, and the like. Agents on This PC can read the hub's data folder. Changes to these files are not reviewed and have no Local History.
- **The server is assumed to be possibly taken over:** names, types and sizes from the server are checked in the hub. `..`, absolute paths, `.git`, links and files that were not asked for are refused. The copy never holds links, so writes into it cannot get out.
- **Reject:** writes on the server through a temporary file, then `mv`. Stops when a link in a parent folder leads out of the project, and a link at the target is replaced, not followed.
- **Limits:** 20000 files or 1 GB (`review_too_big`). Needs GNU `tar` and `stat` on the server. The server's home folder or its parents are not included, the same as on This PC. Links are not reviewed. The copy of the server's code stays on This PC as long as the project exists.

**Home folder not included:** a project that points to the home folder or its parents (including the built-in "Home" project) has no review and no Local History (`supported:false`; on a server, `problem`). If it were included, snapshots would copy `~/.ssh`, every login and other projects into the shadow repo.

### 7.11 Local History

The history of each file from the same shadow repo, whoever wrote it: the **Local History** tab from the tab menu, Explorer, the editor or the palette. Snapshots happen as in §7.10, so changes outside the editor are recorded at the next snapshot, or when the history is opened. Kept 30 days or 2000 snapshots per work tree, pruned once a day; review bases still pending are not deleted.

**Who:** per file, from the same claims Review uses (§7.10). A version goes to the agent or agents whose hook (or server tool) named that file in that snapshot, and to `You` for a save from the editor or an undo from Review. A file nobody named goes to every agent working at that moment, or `You` when none was. Stored in each snapshot's commit message: the subject names everyone working, and a `Who-By-Path: {"<path in the work tree>":"<names>"}` line in the body lists the files whose writer differs. Snapshots from before that line show their subject, so they still name everyone who was working.

| Method | Path | Response / error |
|---|---|---|
| GET | `/api/projects/{id}/history?path=&limit=` | `{"items":[{"commit","at","who","exists"}]}`, newest first; `400 bad_path` |
| GET | `/api/projects/{id}/history/version?path=&commit=` | `{"content","binary"}`; `404 no_version` |

**Restore** puts the text into the editor when the file is open, as one step that Ctrl+Z can undo and that is not saved to disk yet; when it is not open, the text is written straight to the file.

### 7.12 Tasks and tests

The **Tasks** tab in the bottom panel holds what can be run according to the project's files: `package.json` scripts (npm, pnpm, yarn, bun by lockfile), `Makefile` targets, `justfile` recipes, `go.mod`, `Cargo.toml`, pytest, and `composer.json` scripts, in that folder and one level below it. Output shows live. Error locations and failed tests are read from the output: a click opens the line, and a failed task can be sent to an agent. Palette: `Tasks: Run Task…`, `Tasks: Run Tests`.

| Method | Path | Response / error |
|---|---|---|
| GET | `/api/projects/{id}/tasks` | `{"items":[{"id","group","name","kind","command","cwd"}]}` |
| POST | `/api/projects/{id}/tasks/run` | `{"id"}` → `202 Job` (`kind:"task"`, output through `jobOutput`, stopped through `POST /api/jobs/{id}/cancel`, at most 30 minutes). `404 task_not_found` |

Only tasks named by the project's files at that moment run; the hub accepts no commands from the client.

### 7.13 Ports and bookmarks

The **Ports** tab shows the TCP ports open on the project's machine, refreshed every 5 s: `GET /api/machines/{id}/listening` → `{"items":[{"port","address","process","pid"}]}` (`ss`, or `lsof` outside Linux). Ports of other users are hidden unless checked. Each port can be opened as a web tile.

**Bookmarks** are kept per project in the browser and follow their line as the file is edited: `Ctrl+Alt+K` sets or removes one, `Ctrl+Alt+L` and `Ctrl+Alt+J` move between them, `Bookmarks: List` shows them all.

**`@file` in Ask:** `@` suggests project files. For Quick answer, the file content goes along (at most 5 files, 20 KB per file) without showing in the conversation; an agent that has the project folder reads it itself from its path.

**Commit graph in one tab:** the *Open graph* button or the palette's `Git: Open Commit Graph`, with columns for message, author, hash, date, and a filter that dims other rows without breaking the lanes.

### 7.14 Laravel

A project that has `artisan` and a `composer.json` with `laravel/framework`, in the project folder or one level below it, gets a **Laravel** view in the Code card and help in the editor. Needs no PHP, Composer or any extension on This PC, and works the same on a server.

| Method | Path | Response / error |
|---|---|---|
| GET | `/api/projects/{id}/laravel` | `{"detected","root"?,"version"?,"views":[{name,file}],"components":[{name,file}],"routes":[{name?,method,uri,action?,file,line}],"config":[{key,file,line}],"lang":[{key,file,line}],"env":[…]}` |
| GET | `/api/projects/{id}/laravel/log` | `{"file","entries":[{at,env,level,message,trace?,file?,line?}],"cut"}`, newest first, at most 200 entries from the last 512 KB of the newest log file. `409 not_laravel` |
| POST | `/api/projects/{id}/laravel/routes` | `{"items":[{method,uri,name?,action,middleware?}]}`: `php artisan route:list --json`. `409 not_laravel`, `501 no_php`, `502 artisan_failed` |

- **Nothing runs to read.** The index is read from files: `routes/*.php`, `config/*.php`, `lang/en/*.php`, the file names in `resources/views` and `app/View/Components`. Running the application just because a card opened is not something that may happen on a production server. Reads use `nice -n 19`.
- **`.env` is never read.** Variable names for `env('…')` come from `.env.example`.
- **Editor help**, in `php` and Blade files: completion, Go to Definition and hover for `route()`, `to_route()`, `view()`, `@include`, `@extends`, `config()`, `__()`, `trans()`, `env()` (completion only), and `<x-…>`. The index is read again when a file in those folders is saved.
- **Blade:** `.blade.php` has its own language with highlighting for `{{ }}`, `{!! !!}`, `@directive` and tags. The PHP language server does not attach to this language.
- **Routes:** read from files with a small PHP reader: method chains, `group` with `prefix` and `name`, `resource` and `apiResource` with `only` and `except`, `match`, `view`, `redirect`. Routes built by code or from packages are not read; **Ask artisan** asks the application for the full list, after a confirmation, because that command boots the application. Its result does not replace the completion index.
- **Log:** the newest `storage/logs/*.log` file, so daily logs are included. Each entry points to the first file inside the project that it names, not `vendor/` code.
- **Artisan:** the buttons and the command field type `php artisan …` into a new terminal on the project's machine, without Enter. The hub accepts no free-form artisan commands from the client.
- **Limits:** languages other than `en` are not read. Config values are not evaluated. Components in package namespaces and Livewire are not read. Warnings for names that do not exist are not built yet.

## 8. Markdown studio and link graph (E5)

`GET /api/projects/{id}/markdown?refresh=0` →

```jsonc
{ "files": [{ "path": "/home/alice/app/docs/x.md", "rel": "docs/x.md", "size": 1200, "mtime": "…", "title": "X" }],
  "links": [{ "from": "docs/x.md", "to": "docs/y.md", "kind": "wiki"|"md", "resolved": true }],
  "agentMemory": { "dir": "/home/alice/.claude/projects/-home-alice-app/memory", "files": [ … ] },
  "truncated": false }
```

- Files `.md .markdown .mdx` (prune §7 + hidden folders), ≤ 2000. Links are extracted by the hub: `[[name|alias#heading]]` (matches the basename, case-insensitive) and `[text](rel.md)`; files ≤ 256 KB, a budget of 20 MB / 20 s; cache per mtime.
- Claude's per-project agent memory is shown first. Writing there on an SSH machine follows the §9.2 confirmation protocol (it is in `~/.claude`).
- Client: open files are polled with `fs/stat-batch` every 3 s (no local edits → automatic reload, with edits → ask); Source mode autosaves 1.5 s after typing stops (`fs/write` + `ifMtime`); a `d3-force` graph (wiki links form the graph, orphan nodes are shown); PDF through the §5 print route (`/print/markdown?machineId=&path=`, repo images via `fs/raw`). The Phase 4 live markdown card uses the same polling.
- **One part at a time** (v0.9.0-beta.2). `{id}` is a project ref on all three routes, as on the other project routes (API-board §1 "Project parts"). `<project>~<part>` (or `?part=<id>`) reads that part's folder on that part's machine. Its links, graph and backlinks stay inside that folder, and `agentMemory` is Claude's memory for that folder. A bare project id reads the first part in display order, for old clients. An unknown part returns `404 part_not_found`. A link to a note in another part's folder is unresolved.
- **Web, several parts:** the studio shows **Notes in: <part>** (the part picker without Root). It starts in the part named by where it was opened:
  1. the part of the markdown card it was opened from (the part whose folder holds the file on that machine, else the first part on that machine);
  2. else the focused tile's part;
  3. else the part last placed in;
  4. else a part on This PC;
  5. else the first part shown.

  Picking another part saves the open note and opens that part's first note. Writes and trash send the part ref as `projectId`. A single-part project sends the bare id and shows no picker, as before.

### 8.1 Hub implementation (Phase 7 part B) — deviations and verification

`internal/studio/markdown.go`. The response adds `scannedAt`; links carry `heading` when there is one. Title = frontmatter `title:` → the first `# heading` → the file name. Links in code blocks/inline code and external schemes (`https:` etc.) are ignored; `[t](a%20b.md#x?y)` is decoded. Wiki: a relative path when it contains `/`, otherwise the basename (duplicates: the first in path order wins). Cache per (machine, root) in memory per (size, mtime); budget used up → `truncated` and the file is not cached.

- `agentMemory.dir` = `<cfg>/projects/<ClaudeSlug(part folder)>/memory` (the folder of the part the ref names) with `<cfg>` = that machine's default Claude profile or `~/.claude` (layout checked read-only on This PC: `MEMORY.md` + fact files; `MEMORY.md` first).
- **Extensions:** `GET /api/projects/{id}/markdown/graph` → `{"nodes":[{id,title,in,out,orphan}],"edges":[{from,to,kind,count}],"unresolved","truncated"}` (resolved wiki and md links, deduped, orphan nodes included); `GET /api/projects/{id}/markdown/backlinks?path=<rel>` → `{"path","backlinks":[{from,title,kind,heading}],"outgoing":[Link…]}` (`404 not_in_vault`). Live: client polling with `fs/stat-batch` or the §7.1 hub watch (changes show within ≤ 3 s, tested).

## 9. Skills manager (E6, decision #3)

### 9.1 Locations and model

| scope | Path (`<cfg>` = the profile folder or `~/.claude` on that machine) | Write |
|---|---|---|
| `personal` | `<cfg>/skills/<name>/SKILL.md` (one level of subfolder) | SSH: §9.2 confirmation |
| `project` | `<repo>/.claude/skills/<name>/SKILL.md` | ordinary (repo) |
| `personalCommand` | `<cfg>/commands/*.md` | SSH: §9.2 confirmation |
| `projectCommand` | `<repo>/.claude/commands/*.md` | ordinary |
| `plugin` | skills & commands from plugins installed in `<cfg>/plugins/` | read-only (`403 read_only`) |

```jsonc
{ "id": "personal:commit-message", "scope": "personal", "kind": "skill"|"command", "name": "commit-message",
  "path": "/home/alice/.claude/skills/commit-message/SKILL.md", "description": "…", "invoke": "/commit-message",
  "argumentHint"?: "[scope]", "modelInvocable": true, "subagent": false, "model"?: "…", "allowedTools"?: ["Bash(git:*)"],
  "tokensEstimate": 820, "siblings": ["reference.md"], "shadowedBy"?: "project:commit-message", "mtime": "…",
  "lint": [{ "rule": "name-format", "severity": "warn", "message": "…" }] }
```

Shadowing: personal beats project; a skill beats a command of the same name. Local lint (no AI): a name that is not `[a-z0-9-]` or is > 64; name ≠ folder; an empty or too long description; an empty body or > 500 lines; an unknown frontmatter key; `<`/`>` in a frontmatter value.

### 9.2 Remote write confirmation protocol

For SSH machines, every write/delete that touches `<cfg>` must carry `"confirm": {"paths": [...]}` that is **exactly the same** as the set of paths (files and new folders) the hub computes. Without it → `428 {"error":"confirmation required","code":"confirm_required","machine":"devbox","paths":[…]}`. The UI shows the machine name and the list of paths, with `Write to devbox` / `Cancel` buttons. Before a delete/overwrite, the hub copies the old version to `<data>/backups/skills/<machineId>/<ts>/` (on the hub, not on the remote).

| Method | Path | Request | Response / error |
|---|---|---|---|
| GET | `/api/machines/{id}/skills?projectId=&profileId=` | — | `{"items":[Skill…],"truncated"}` (≤ 400, without body). `projectId` (here and in the bodies below) is a project ref: `<project>~<part>` is that part's folder, which must be on this machine; a bare project id is its first part on this machine; neither → `404 project_not_found` |
| GET | `/api/machines/{id}/skills/item?path=` | — | `{"skill","content"}` |
| POST | `/api/machines/{id}/skills/lint` | `{"content","path"?}` | `{"lint":[…]}` |
| PUT | `/api/machines/{id}/skills/item` | `{"path","content","ifMtime"?,"confirm"?}` | `Skill`; `428`, `403 read_only`, `409 mtime_mismatch` |
| POST | `/api/machines/{id}/skills` | `{"scope","kind","name","description","argumentHint"?,"body","projectId"?,"confirm"?}` | `201 Skill` |
| POST | `/api/machines/{id}/skills/import` | `{"fromMachineId","fromPath","scope","projectId"?,"confirm"?}` | `201 Skill` (a folder with `SKILL.md`, ≤ 50 files, ≤ 5 MB) |
| POST | `/api/machines/{id}/skills/duplicate` | `{"path","toScope","projectId"?,"confirm"?}` | `201 Skill` |
| DELETE | `/api/machines/{id}/skills/item` | `{"path","confirm"?}` (body) | `{"ok":true,"backup":"…"}` |
| GET | `/api/skills/starters` | — | NextBoard starters: `/commit-message`, `/pr-summary`, `/release-notes`, `/write-tests`, `/find-bugs`, `/plan-refactor`, `/document-api`, `/daily-update`, `/explain-code`, `/speed-check`, `/audit-deps`, `/update-readme` |
| POST | `/api/sessions/{id}/skill` | `{"name","args"?}` | `202 Delivery` (`/name args` through the delivery engine; `409 not_agent`) |

### 9.3 Hub implementation (Phase 7 part B) — deviations and verification

`internal/skills` (frontmatter, lint, scan, shadowing, confirmation protocol, backup, starters), endpoints in `internal/server/h_skills.go`.

- `<cfg>` = `profileId` → the machine's default Claude profile (`settings.agents.profiles[m].claude`) → `~/.claude` (absolute); the list response adds `cfg`. Plugins are read from `<cfg>/plugins/installed_plugins.json` (`installPath`, Claude Code 2.x): `<install>/skills/*/SKILL.md` and `<install>/commands/*.md`, id `plugin:<plugin>:<name>`, invoke `/<plugin>:<name>`, read-only.
- Shadowing: personal skill > project skill > personal command > project command. Lint (rule ids): `frontmatter-missing`, `name-missing`, `name-format` (the frontmatter name **and** the folder), `name-folder`, `description-missing`, `description-length` (> 1024, or with `when_to_use` > 1536), `body-empty`, `body-length` (> 500 lines), `unknown-key`, `angle-brackets`.
- **Confirmation set** (absolute paths, compared as a set): create = new folders + files; edit = `[file]`; delete = the skill folder + subfolders + every file (a symlinked folder: only the link, the target is not touched); import/duplicate = new folders + files. The hub never creates `<cfg>` itself (decision #4), it may create `<cfg>/skills|commands`; `.claude` in a repo may be created. The `428` body is exactly the contract (`machine` = the machine's name).
- Backup to `<data>/backups/skills/<machineId>/<YYYYMMDD-HHMMSS.mmm>/<path relative to cfg or repo/.claude>` before **every** overwrite/delete (This PC too); delete = delete (not trash) after the backup. Limits: content ≤ 512 KB, delete ≤ 200 files, import 50 files / 5 MB.
- Error codes: `403 read_only`, `400 not_skill|bad_name|bad_scope|no_skill_file`, `409 exists|mtime_mismatch`, `413 too_large`, `501 no_sftp`, `428 confirm_required`. Starters add `title` and `content` (a SKILL.md ready to write). `POST /api/sessions/{id}/skill`: name `[A-Za-z0-9_.-]` (+ `:plugin`), args one line ≤ 2000, delivery `source:"skill"`.
- Tests: local (`internal/skills`), SSH with a single-use sshd (`internal/machine/studio_ssh_test.go`: without `confirm` → `428` + the path list, one path different → `428`, the backup is on the hub before an overwrite/delete, plugins read-only, project writes without confirmation, a `<cfg>` that does not exist is not created). Every test `<cfg>` = a temp folder; `~/.claude/skills` is not touched.

## 10. Usage (F2–F4, decision #2)

### 10.1 Usage pill

```jsonc
{ "machineId": "local", "agent": "claude", "profileId": "", "checkedAt": "…", "trigger": "background"|"user",
  "state": "ok"|"needsInteractive"|"notLoggedIn"|"unsupported"|"error",
  "windows": [{ "id": "session", "label": "Current session", "usedPct": 42, "resetsAt": "…"|null, "resetsText": "Resets 6pm" }],
  "facts": [{ "label": "Total tokens", "value": "12.3M" }],
  "raw": ["…screen lines…"], "parsed": true, "error": "" }
```

Check procedure (job `usage`):

1. Exec on the machine: `d=/tmp/nb-usage-$(id -u)` — **one fixed path per machine user** (lead decision, replacing `mktemp`, so that Claude Code records only one folder trust entry). `$d` a symlink or not a folder owned by the user → fail (not used, not deleted); a leftover folder owned by the user (hub crash) is deleted first; then `mkdir -m 700 -- "$d"` (fails when one appears again in between); recorded in `usage/pending-cleanup.json` before the CLI runs. Because the path is fixed, only one check per machine at a time.
2. A hidden PTY session (not in `sessions.json`, not on the board), 120×40, cwd `$d`, the profile's env, **without** hooks/yolo; VT model in `respond` mode (answers DSR/DA).
3. Wait for the prompt to be ready ≤ 30 s. The folder trust prompt for `$d` is answered "trust" (an empty folder owned by the hub); an update prompt is skipped; a login prompt → `notLoggedIn`; another prompt → `needsInteractive`.
4. Type the usage command + Enter: Claude `/usage`, Codex `/status`, Gemini `/stats`; other agents `unsupported`. Wait for the screen to be stable for 1.5 s (≤ 15 s).
5. Screen snapshot → parser per CLI (percent regex `(\d{1,3})%\s*used`, reset text; `resetsAt` best-effort, `null` when the format is not known). Parse failure → `parsed:false` + `raw`.
6. Exit (Claude: Esc, then `/exit`; Codex: `/quit`; kill after 3 s), exec `rm -rf -- "$d"` after validating `^/tmp/nb-usage-[0-9]+$` (in Go) and again in the shell (`case`, `[ -d "$d" ] && [ -O "$d" ]`, not a symlink), remove from `pending-cleanup.json`. Also when the job is cancelled or fails. When the hub starts, leftovers in that file are cleaned per machine as soon as it connects (only paths that are recorded and pass validation). No other `/tmp` scan.

| Method | Path | Request | Response |
|---|---|---|---|
| GET | `/api/usage?machineId=&agent=&profileId=` | — | `{"items":[UsageReport…]}` (cache) |
| POST | `/api/usage/check` | `{"machineId","agent","profileId"?,"maxAgeSec"?: 180,"trigger"?: "background"}` | cache fresh enough → `200 UsageReport`; otherwise `202 Job`. ≤ 1 per (machine, agent, profile), ≤ 2 per hub; machine disconnected → `503 machine_offline` |

The client asks for `maxAgeSec:180` when opening a board, `30` when the popover opens, `0` for "Check again". Bar colors: amber ≥ 75 %, red ≥ 90 %. `settings.usage.background` (default **false**) allows background checks; `settings.usage.agents` turns checks off per CLI for every trigger and every device (default `{"codex": false}`; a missing key = on). This is the only per-CLI switch: the web client changes it in Settings › Usage and through the "Turn on and check" button in the popover, and keeps no per-browser CLI choice. Reason for the default: each Claude check adds 2 lines to the `history.jsonl` of the login in use, a Codex check makes Codex write a `[tui]` block into the `config.toml` of the login in use when there is none yet. The client must send `trigger:"user"` for user clicks; without `trigger` the hub treats it as `background`.

### 10.2 Reset chime (F3)

`settings.usage.chime {enabled:true, play:"after100"|"always", sound:"bell"|"chime"|"pop"|"soft"}` (sounds bundled with NextBoard). The hub sets a timer at `resetsAt + 30 s` for each window (from `usage/reports.json`, recomputed at start); the `play` condition met → event `usageReset {machineId, agent, profileId, window, at}` → the web plays a sound (tab open), Android a "Claude usage reset" notification, desktop a native notification.

### 10.3 Token chart (F4)

`GET /api/usage/tokens?days=30&machineId=&agent=` →

```jsonc
{ "from": "2026-09-29", "to": "2026-10-28",
  "series": [{ "date": "2026-10-28", "agent": "claude", "input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0 }],
  "byAgent": [{ "agent": "claude", "days30": { "total": 0, "output": 0 }, "allTime": { "total": 0 },
                "sessions": 442, "favoriteModel": "claude-opus-5-5", "longestSessionSec": 4063924 }],
  "busiestDay": { "date": "…", "total": 0 }, "top": "claude", "scannedAt": "…", "partial": false }
```

An incremental index per machine `usage/tokens/<machineId>.json` (`files: {path: {size, offset, mtime}}`, aggregates per day per agent): Claude `<cfg>/projects/*/*.jsonl`, Codex `<home>/sessions/**/rollout-*.jsonl`, for the default login and every profile on that machine; only new bytes since `offset` are read (`fs/read?offset=`); Claude dedupe per `message.id` (remembers the last 200 ids per file). A budget of 30 s per request (`partial:true` when used up); background refresh every 30 minutes for connected machines. Dates follow the hub's time zone. The UI states that this is not a bill.

Per-CLI statistics are also kept in the index and stay even after the CLI has deleted its transcripts: `sessions` = transcripts with at least one turn; `favoriteModel` = the model with the most tokens (Claude `message.model`, Codex `turn_context`); `longestSessionSec` = the first to the last turn in one transcript, so a session continued over several days counts as long. An old index is filled through a second read, once only (`statOffset`), without counting tokens twice. First scan on This PC: 2.2 GB of transcripts, 7.4 s. The usage popover asks for `days=366&agent=…` for the heatmap.

### 10.4 Hub implementation and verification (Phase 7 part B)

`internal/usage` (fixed folder, hidden PTY driver, parsers, chime, token index), jobs through `jobs.SubmitFunc` (new, `internal/jobs/func.go`: a Go-function job outside the runner queue, kind `usage`, purpose `usage`, `result` = `UsageReport`).

- Queue: ≤ 1 check per **machine** (fixed path) and ≤ 2 per hub; a request for a (machine, agent, profile) already running gets the same job. `profileId:""` = the default login, that is the machine's default profile when there is one (the client shows no second chip for that profile), other profiles must match machine+agent (`400 profile_mismatch`). An agent without a check → `200` report `unsupported` without a job. `trigger:"background"` while `settings.usage.background:false` → `409 background_disabled`. An agent turned off in `settings.usage.agents` → `409 usage_disabled` (any trigger). An unknown screen → `state:"error"`, `parsed:false`, `raw`. Login → `notLoggedIn`; another dialog/prompt not ready in 30 s → `needsInteractive` (without answering anything). The CLI exits before the prompt is ready → `state:"error"`, `error` holds the tail of its screen (for example Codex without a network: `account/read failed…`).
- Driver: keys are typed per character (25 ms pause, not a paste); Claude's trust is answered with ↓ then Enter **only** after the cursor is seen on "Yes, I trust this folder" (the dialog's default = "No, exit"); update dialogs only get Esc (never Enter); Claude env `DISABLE_AUTOUPDATER=1`; Codex `-c check_for_update_on_startup=false -c projects."<d>".trust_level="trusted"`. While Codex prints `Limits: refresh requested; run /status again` or `Limits: data not available yet` without bars, `/status` is typed again after 2 s, at most 4 times; when it stays empty → `state:"error"`, `error` "no limit data from the server yet…".
- Parser: percent `used` **or** `left` (`used = 100 − left`, the Codex 0.165 format), the label from the line above the bar (Claude) or before `:` (Codex), the reset text on the bar line/the next 2 lines; `resetsAt` from `6pm (Asia/Jakarta)`, `Nov 1, 8pm (…)`, `18:00`, `12:52 AM on 29 Oct`, `in 2h 5m` (a zone in parentheses is used, otherwise the hub's zone); the "What's contributing…" list is ignored. Codex `Limits: not available for this account` (API key login) → `state:"unsupported"` with facts, not an error. Facts: Codex `Model`, `Account`, etc., including `Warning: limits may be stale…`; Claude `/usage` 2.1.312 has no facts. Gemini `/stats`: not verified (not installed).
- Chime: once per (report, window, `resetsAt`); a reset missed by ≤ 10 minutes at start still sounds, older ones do not; the `usageReset` event adds `sound`. Tested with an injected clock.
- Tokens: Codex `input` = `input_tokens − cached_input_tokens`, `cacheRead` = cached; files with `token_usage_record` use the record (dedupe `response_id`), otherwise `token_count` (dedupe the running total); ≤ 20 000 newest files; empty `machineId` = every connected machine; an incomplete last line is read again later.
- **Real run** (This PC, `NEXTBOARD_REAL_CLI_TEST=1 NEXTBOARD_REAL_USAGE=1`, `internal/usage/realcli_test.go`): **Claude 2.1.312** `/usage` — the trust dialog for `/tmp/nb-usage-1000` answered, the screen "Settings · Status · Config · Usage · Stats" with `Current session` 16 % (Resets 12am (Asia/Jakarta)), `Current week (all models)` 36 %, `Current week (Fable)` 4 % → 3 windows parsed, ±8 s (fixture `internal/usage/testdata/claude-2.1.312-usage.txt`). **Codex 0.165.0** `/status` — no dialog; `5h limit: [████] 100% left (resets 12:52 AM on 29 Oct)`, `Weekly limit: … (resets 1:53 PM on 4 Nov)` → 2 windows + facts (fixture `codex-0.165.0-status.txt`, email censored). The usage folder was always deleted, `pending-cleanup.json` empty. **Limits not arrived yet** (This PC, 2026-11-03, Codex 0.165.0 behind a proxy that holds all traffic for 7 s starting 0.7 s after the first connection): the first `/status` without bars, the driver waits, then types again, result `ok` with 2 windows in ±13 s. With no network at all Codex exits at start (`account/read failed during TUI bootstrap`), and that reason goes into `error`. The texts `Limits: not available for this account` and `Warning: limits may be stale…` were taken from the Codex 0.165.0 binary and are only tested with a fake TUI, not a real API key login.
- **CLI side effects on This PC:** Claude adds one entry `projects["/tmp/nb-usage-1000"].hasTrustDialogAccepted=true` to `~/.claude.json` (as the fixed path intends) and **2 lines per check** (`/usage`, `/exit`) to `~/.claude/history.jsonl` (accumulating with background checks → owner decision); no transcript or project folder for the usage folder; `settings.json` unchanged. **Codex 0.165 writes `[tui] animations = false` + `screen_reader_detection_done = true` to `~/.codex/config.toml`** on the first interactive start: Codex's screen-reader probe (`tui/src/screen_reader.rs`) runs every time that marker is not *in the file* (the override `-c tui.screen_reader_detection_done=true` is not read — verified with a second run), `animations=false` because This PC's a11y D-Bus bus reports `IsEnabled`. The hub cannot prevent this without writing the config; the owner's file was restored byte for byte after both runs (same checksum). Codex also updates its sqlite state/log, `models_cache.json`, `skills/.system`, `shell_snapshots` in `~/.codex` (a normal start), without rollout/history. **Needs an owner decision:** accept Codex's write-once marker (Codex also writes it when the user first runs 0.165 interactively themselves) or turn off Codex usage checks.
- Tests without the real CLI: a fake TUI (`internal/usage/testdata/fake-tui.sh`) in a real local PTY and over SSH (single-use sshd): trust, update skipped without ever pressing Enter, login, unknown screen, hang (timeout), CLI missing, cancel → folder deleted, crash → cleaned when the machine connects, a symlink at the fixed path refused, an invalid recorded path is never `rm`-ed.

## 11. Voice (D11–D13)

Entirely in the client; the hub receives no audio and has no voice endpoint. While listening, the web also opens the microphone through `getUserMedia` to measure the waveform level (except Android); the value is only used on that page and the microphone is closed as soon as it is done.

- **Web:** `SpeechRecognition`/`webkitSpeechRecognition`, **opt-in** in Settings › Voice with a disclosure (UI text): "Speech recognition here is provided by your browser. In Chrome and Edge your audio is sent to Google or Microsoft to be transcribed. NextBoard never stores or sends audio. Turn on voice?". A browser without the API → voice controls hidden. Settings per client (localStorage): `enabled`, `lang` (BCP-47, default `en-US`), `holdSpace` (true), `dictationKey` (default hold `AltRight`), `ttsConfirmations` (true), `ttsVoice`.
- **Push-to-talk:** hold Space while the board has focus (not a console/field) or hold/tap the mic button; interim results show in the command bar; release → `POST /api/projects/{id}/commands` `source:"voice"`; Esc cancels; max 60 s (cut off → not sent). Similar-sounding names are handed to the hub's fuzzy resolver (`didYouMean` confirmation). "yes/confirm/cancel" answers a pending confirmation.
- **Dictation:** hold the dictation key → text to the focused field, or to the focused console through delivery `{guard:"dictation", submit:false, source:"dictation"}`; `409` → the text goes into the command bar. The words "comma", "period", "question mark", "new line" are mapped by the client (English). Max 5 minutes.
- **TTS:** `speechSynthesis` only for the hub's short sentences (confirmations, "Arkan finished", questions); never reads agent replies aloud.
- **Android:** a mic button in the command bar and the terminal input; `SpeechRecognizer.createOnDeviceSpeechRecognizer` (API 31+) when available, otherwise `EXTRA_PREFER_OFFLINE` with a disclosure when it falls back to an online recognizer; TTS via `TextToSpeech`.
- **Desktop (Tauri):** uses Web Speech when the webview supports it; when not, the controls are hidden.

## 12. Settings and shortcut rebinding

Everything uses the Phase 5 store (`GET/PATCH /api/settings`, event `settings`). New keys:

```jsonc
"previews": { "auto": true, "portRange": [12320, 12339] },      // portRange applies after a hub restart
"usage":    { "background": false, "agents": { "codex": false }, "chime": { "enabled": true, "play": "after100", "sound": "bell" } },   // part B: validated (play/sound enums)
"runner":   { "defaultAgent": "claude" },
"git":      { "commitWithRunner": true }
```

Settings UI (web/desktop): General (resume on restart, notifications, chime), Agents (Phase 5), Shortcuts (list of `settings.shortcuts` actions + defaults; click, then press the new key; Esc = cancel; Delete = default; `X` fixed; conflicts flagged before the PATCH), Previews & studios, Voice (per client), Connections (`gh` status per machine + Connect), About. Android: Notifications, Voice, Agents (default per machine, yolo default with confirmation).

## 13. New events on `/api/events`

```jsonc
{ "type": "job", "job": Job }                      { "type": "jobOutput", "jobId": "jb_…", "seq": 12, "text": "…" }
{ "type": "git", "projectId": "p_…", "status": { "branch", "ahead", "behind", "changed" } }
{ "type": "gitRepo", "projectId": "p_…", "repo": "group/app-b", "status": { "branch", "ahead", "behind", "changed" } }   // a repository below the project folder (§3.6)
{ "type": "github", "projectId": "p_…", "github": { … } }
{ "type": "preview", "preview": Preview }          { "type": "previewRemoved", "id": "pv_…" }
{ "type": "previewSuggested", "sessionId": "s_…", "previewId": "pv_…", "url": "http://localhost:5173/", "machineId": "local", "port": 5173, "workspaceId": "w_…", "nodeId": "n_…" }
{ "type": "previewLog", "previewId": "pv_…", "entries": [LogEntry…] }   // ≤ 4×/s
{ "type": "document", "projectId": "p_…", "document": Document }
{ "type": "diagram", "projectId": "p_…", "diagramId": "dg_…", "state": "done" }
{ "type": "usage", "report": UsageReport }         { "type": "usageReset", "machineId", "agent", "profileId", "window", "at", "sound" }
{ "type": "skills", "machineId": "m_…" }           // skill list changed by the hub
{ "type": "documentRemoved", "projectId", "documentId" }   // part B extension
{ "type": "fsChanged", "watchId": "fw_…", "machineId", "changes": [Stat…] }   // part B extension (§7.1)
```

## 14. Persistence

| Location | Content |
|---|---|
| `jobs.json` | metadata of the last 100 jobs (without large output) |
| `documents/<projectId>/index.json`, `documents/<projectId>/<docId>/<vid>.md` | document library |
| `diagrams/<projectId>/<id>.json` | diagram results as generated |
| `usage/reports.json`, `usage/tokens/<machineId>.json`, `usage/pending-cleanup.json` | usage |
| `backups/skills/<machineId>/<ts>/` | copies before a skill overwrite/delete |
| `runner/planner/` | the planner's empty cwd (This PC) |
| `settings.json` | §12 keys |

Previews and their listeners are in memory only (the `web` nodes on the board are what persists). **Writes on the target machine** (only on user action): repo files (git, Code/Markdown studio, project skills), `.git/nextboard-trash/` or `.nextboard-trash/`, `<cfg>/skills|commands` (with the §9.2 confirmation), the temporary folders `/tmp/nb-usage-<uid>` and `/tmp/nb-run.*` (always deleted). The CLI's own side effects during usage/runner: see §10.4.

## 15. Limits and timeouts

| Item | Value |
|---|---|
| Exec output | 4 MiB |
| Git | reads 10 s; commit 120 s; network 15 minutes / idle 5 minutes; status ≤ 400 files; diff ≤ 512 KB; log ≤ 100; branches ≤ 200 |
| Runner | default 10 minutes / idle 200 s; documents 6/12/25 minutes; ≤ 2 per machine, ≤ 4 per hub |
| Preview | ≤ 20 listeners; token 60 s; cookie 12 h; idle 30 minutes; log 500 entries; beacon 64 KB, 10/s |
| Diagram | 40 nodes, 80 edges, 8 groups |
| Code studio | tree 5000 entries/level; open ≤ 800 KB; highlight ≤ 120 000 characters; stat-batch 200 |
| Markdown | 2000 files; 256 KB/file; 20 MB / 20 s per index |
| Skills | 400 items; import 50 files / 5 MB |
| Usage | ready 30 s; screen 15 s; total 60 s |
| Token index | 30 s per request |
| Voice | push-to-talk 60 s; dictation 5 minutes |

## 16. Security

- No free-form exec; git/runner/usage commands from templates, refs validated with `check-ref-format`, paths after `--` and checked to be inside the repo.
- Discard and trash never delete, they only move to a trash inside the project.
- Preview: a separate origin per port, cookies per origin, single-use tokens, targets only that machine's loopback or a Tailscale IP checked again on each connection (§4.9; no SSRF into other networks), the hub's port excluded, upstream HSTS dropped so it does not stick to the hub's host, `/api` not available on the preview origin.
- Runner: never yolo, read-only tools or no tools, output treated as untrusted (diagram JSON validated, Markdown rendered without raw HTML).
- Remote skills/agent memory: the 428 protocol + backup on the hub (decision #3). Remote profiles stay references (decision #4).
- Temporary folders: only in `/tmp` (`/tmp/nb-run.XXXXXX` through `mktemp -d`; usage = the fixed path `/tmp/nb-usage-<uid>` through `mkdir`, symlinks/someone else's refused), paths validated before `rm -rf`, recorded so they are surely cleaned up (decision #2).
- Voice: opt-in with a disclosure; audio never goes to the hub.

## 17. UX per client (what must be there)

- **Web/desktop:** a GitHub pill at the bottom left (branch + changed count) → Git panel (`G`): an ahead/behind header + time of the last fetch, list of changes with checkboxes + All/None, diff per file, subject suggestion (icon), Commit & Push / Commit, Pull (when behind), Publish to GitHub (when there is no remote), Discard (confirmation), history of 100, a warning for files > 100 MB ("Keep off GitHub"); branch card. Web tile: localhost dial (common ports, a green dot when open), address bar, back/forward/reload, device size, zoom, tabs, "Open in new tab", a waiting-for-the-dev-server status; log card; automatic tiles with a cyan edge. Documents studio (choose the CLI, effort + median duration, type, streaming, version library, flowing/paged preview, PDF A4/Letter), Diagrams (generator panel + starters, a "drawing" card that can be stopped), Code (`tree` + CodeMirror, Find, status bar, manual save, drafts), Markdown (`D`: vault, Reading/Source/Graph, autosave), Skills (`S`: Installed/Starters, scope filter, skill page + lint, edit, new, run to a console), usage pill + popover (bars, reset, account/profile, raw view, token chart), Settings §12.
- **Android:** Git status + Commit/Push/Pull from the project screen; document list (read, print PDF); compact usage in the header + reset notification; previews in a native `WebView` that loads the preview URL (certificate pinned with the hub fingerprint); mic for the command bar and terminal dictation.

## 18. Required test scenarios

1. Exec watchdog: a process without output for 5 minutes is stopped (`idle_timeout`); output > 4 MiB is cut.
2. Git: repo status with rename/untracked/conflict; commit of chosen paths only; `409 conflicts` during a merge; push without upstream uses `-u`; diverged pull → `diverged`; discard moves files to `.git/nextboard-trash` and restore does not overwrite; a push asking for a password → fails fast (no hang) with a clear message.
3. Git over SSH: commands use `BatchMode`; no file outside the repo changes.
4. Runner: never any `--dangerously-*` in argv (asserted in a test); `cli_missing` and `cli_not_logged_in` recognized; cancel stops the process and keeps the partial document.
5. Preview: an iframe from the hub origin gets the cookie; a request without the cookie `401`; a token used twice `401`; `X-Frame-Options`/`frame-ancestors`/HSTS dropped; Vite HMR WebSocket works; a non-loopback target impossible; the hub's port refused.
6. Bridge: SPA navigation triggers `nav`; `console.error` appears in the log card in two clients; messages from an unknown origin are ignored.
7. Automatic preview: `npm run dev` prints a URL → one tile + edge (no duplicate when the URL is printed again); a closed port → no tile.
8. Documents: `jobOutput` streaming in order; an `incomplete` version after Stop; median duration from 5 runs.
9. Diagram: JSON outside the limits is refused, then retried once; landing produces shape/line/text with `diagramId`.
10. Code studio: save with a stale `ifMtime` → `409` + banner; CRLF/BOM kept; a symlink leaving the project needs `allowOutside`; SSH without SFTP → `501`.
11. Markdown: wiki links with alias/heading resolve; a file change by an agent shows within ≤ 3 s without local edits.
12. Skills: a personal write over SSH without `confirm` → `428` with the path list; `confirm` one path different → `428`; the backup is on the hub before a delete; plugin `403`.
13. Usage: the folder `/tmp/nb-usage-<uid>` is always deleted, also after the hub is killed in the middle of a check (cleaned at start); an unknown screen → `parsed:false` with `raw`; chime only when `play` is met.
14. Token chart: the incremental index reads only new bytes; dedupe `message.id`; `partial:true` when the budget is used up.
15. Voice: without opt-in no microphone access; a recording > 60 s is not sent; dictation to a shell console → goes into the command bar.

## 19. Open issues / to verify

1. **A listener per port** behind a reverse proxy: nginx must map every preview port (see `deploy/nginx.conf.example`); `tailscale serve` only forwards the main port, so web tiles do not work through it.
2. **Self-signed certificates in an iframe** (when nginx uses one): the browser does not show its warning page inside an iframe; open the preview once in a new tab to accept its certificate.
3. **Rewriting `Host`/`Origin`** to `localhost:<port>` can break apps that build absolute URLs from Host (OAuth callbacks); adding `'self'` to the upstream CSP for the bridge; sites with a nonce-based CSP.
4. **`direct-tcpip`** needs `AllowTcpForwarding` yes/local on the remote.
5. **Runner flags:** partly verified (§2): `--permission-mode dontAsk`, `--tools`, `--max-turns 1`, `--no-session-persistence`, `--model` work in print mode; `--json-schema` without tools and `codex exec --output-schema` not tried for real yet.
6. **Headless runner vs screen:** departs from Symvibe; catalog agents are not supported for studios. **Approved by the owner 2026-11-08** (ROADMAP #42): in line with the local-oriented direction.
7. **Usage is best-effort:** the screen formats of `/usage` (Claude), `/status` (Codex), `/stats` (Gemini) change between versions. Accumulating trust entries is **decided** (lead): the fixed path `/tmp/nb-usage-<uid>` → one `~/.claude.json` entry per user (verified, §10.4). Still open: `~/.claude/history.jsonl` gets 2 lines (`/usage`, `/exit`) per Claude check, and Codex 0.165 writes the screen-reader marker to `config.toml` once (§10.4).
8. **Claude's per-project agent memory path** and the list of valid skill frontmatter keys; the plugin folder layout.
9. **PDF in the browser:** support for `@page` margin boxes (Chrome ≥ 131), PDF bookmarks not guaranteed; Android `PrintManager`.
10. **Web Speech** in the Tauri webview (WebKitGTK/WKWebView) and `getUserMedia` on a self-signed origin.
11. **Git:** priority of the user's `core.sshCommand` vs `BatchMode`; HTTPS credential helpers on the remote.
12. **`gh repo create --source . --push`**: verify the flags and the behavior when an `origin` remote already exists.
13. **Diagram layout library:** done, not used. The layout is written in-house (`internal/studio`, `web/src/studio/diagrams/layout.ts`).
14. **Token index over SFTP** on very large transcript folders (thousands of files) — needs a performance test.
