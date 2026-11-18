# NextBoard API — Phase 4: projects, workspaces, board v2, files & assets

An addition to [API.md](API.md); the conventions (auth, CSRF `X-NextBoard: 1`, error `{"error"}`, camelCase, RFC 3339) stay the same. Features & UX: [ROADMAP.md](ROADMAP.md) Phase 4.

The old `GET/PUT /api/board` endpoint is **removed** (replaced by a board per workspace). The old `{"type":"board"}` event is removed too.

## 1. Project & workspace

```jsonc
// Project = a folder (usually a git repo) on a machine
{
  "id": "p_ab12cd34ef", "name": "nextboard",          // name: for display, max 24 characters
  "machineId": "local", "path": "/home/alice/apps/nextboard", // absolute path on that machine
  "createdAt": "…", "lastOpenedAt": "…",
  "workspaces": [Workspace…],                          // in tab order
  "sessionCount": 3, "runningCount": 2,
  "groupId": "g_…", "tags": ["backend"],               // both absent when empty; no group = All projects
  "parts": [ProjectPart…],                             // the folders the project spans, in display order; machineId + path = the first, for old clients
  "archivedAt": "…",                                   // only while archived (see "Archive and delete for good")
  "cover": ProjectImage, "icon": ProjectImage,         // each absent when none
  // ProjectImage = {"assetId","source":"upload"|"ai","at", "title"?: true, "tagline"?, "accent"?: "#RRGGBB"}:
  // title = a cover made as a background, which clients show with the title set over it (Cover and icon)
  "rootDir": "<data>/roots/p_ab12cd34ef"               // two or more parts only: the project's root folder on This PC, where root consoles work
                                                       // (whether or not it exists yet); a path a root console names through it,
                                                       // <rootDir>/<part name>/…, is that part's file (the editor maps it back)
}
// ProjectImage = an image of the asset store (§5); nothing garbage-collects assets
{ "assetId": "a_…", "source": "upload", "at": "…" }   // source: "upload" | "ai"
// ProjectPart = one project folder on one machine, with the name of its role (ROADMAP #49)
{ "id": "pt_…", "name": "shop-api", "machineId": "local", "path": "/home/alice/apps/shop-api", "readOnly": true }
// every part gets a "pt_…" id; "main" is only in data from before v0.9.0-beta.2 and is never rewritten;
// the name is also the part's folder name at the project root (path-safe, see "Project parts"); readOnly absent when false
// Workspace = one board tab inside a project
{ "id": "w_9x8y7z6w5v", "projectId": "p_…", "name": "Main", "createdAt": "…", "sessionCount": 2 }
// GitInfo (read-only, for the home screen)
{ "isRepo": true, "branch": "main", "ahead": 0, "behind": 2, "changed": 5, "error": "" }
```

| Method | Path | Request | Response |
|---|---|---|---|
| GET | `/api/projects` | — | `Project[]` of the active projects (sorted by `lastOpenedAt` desc). `?archived=1`: the archived ones only, sorted by `archivedAt` desc |
| POST | `/api/projects` | `{"machineId","path","name"?,"partName"?,"parts"?: [{"name","machineId","path"}]}` | `201 Project` with one workspace "Main". The same path on the same machine, also when that folder is a part of another project → `200` the existing project. `400` path is not a folder. With `partName` or `parts`, the project is created from its parts at once: the folder itself becomes the first part (named `partName`), then `parts` in order (max 8 in all). Every part gets a `pt_…` id, and a part without a name uses its folder's name, made path-safe, with a number when another part has that name. A plain add (no `partName`, no `parts`) makes one part named after its folder. All folders are checked first; if one is wrong, nothing is created and the error carries `part`, the index of the wrong folder (0 = the folder itself). A name that is not path-safe → `400 bad_part_name`. The errors are the same as for `POST …/parts`. A single part whose folder is already a project → `200` that project, the same as without `partName`; with several parts → `409 path_in_project`. The Add project form always takes the shape of this list of parts, one or many. |
| PATCH | `/api/projects/{id}` | `{"name"?, "opened"?: true, "groupId"?, "tags"?, "sandbox"?, "partAccess"?, "cover"?, "icon"?}` (`partAccess`: `""` (parts on another machine are read-only for agents) or `"full"`, anything else `400 bad_part_access`; `cover`/`icon`: `{"assetId":"a_…"}` of an uploaded image, which then has `source:"upload"`, or `null` to remove it) | `Project` (`opened` updates `lastOpenedAt`; `groupId: ""` takes it out of its group; one save, refused as a whole: `404 group_not_found`, `400 bad_tag`, `400 bad_asset` when the asset does not exist or is not an image) |
| POST | `/api/projects/{id}/archive` | — | `Project` with `archivedAt`. See "Archive and delete for good". Archiving an archived project changes nothing |
| POST | `/api/projects/{id}/restore` | — | `Project` without `archivedAt`. `409 not_archived` when it was not archived |
| DELETE | `/api/projects/{id}` | — | Without a query: **archives**, the same answer as `POST …/archive` (the safe default for older clients). With `?permanent=1`: deletes the project for good, active or archived: `{"ok":true}` and the event `projectRemoved`. Folders on any machine are never touched |
| POST | `/api/projects/bulk` | `{"ids": ["p_…", …], "action": "archive"\|"restore"\|"delete"}` (1–200 ids; repeats are dropped; `delete` = for good) | `{"results": [{"id","ok":true} \| {"id","ok":false,"error":"…","code"?}]}` in the order of `ids`. Each id is handled on its own (`code`: `project_not_found`, `not_archived`). Besides the per-project events, one `{"type":"projects"}` at the end: clients refetch the list. `400 bad_action`, `400 bad_ids` |
| GET | `/api/art/capability` | — | `{"available": bool, "tool": "codex", "reason": "…"}` (kept about a minute). See "Cover and icon" |
| POST | `/api/projects/{id}/art` | `{"kind": "cover"\|"icon"}` | `202 Job` (kind `art`, purpose `projectArt`; progress and the end as for other jobs: `GET /api/jobs/{id}`, `POST /api/jobs/{id}/cancel`, `job` events). `400 bad_kind`, `404 project_not_found`, `409 art_running` (with `jobId`) while one of that kind runs for the project, `412 no_image_tool` (with the reason) when no image tool is there |
| POST | `/api/projects/{id}/parts` | `{"name","machineId","path"}` | `201 Project` with the new part at the end. The folder is checked as in `POST /api/projects`. `409 part_overlap` (the same folder as, inside, or containing another part on the same machine), `409 name_conflict`, `409 path_in_project` with `projectId` (that folder is already another project or one of its parts), `409 too_many_parts` (max 8), `400 bad_part_name` (no name, or not path-safe), `400` folder |
| PATCH | `/api/projects/{id}/parts/{part}` | `{"name"?, "readOnly"?, "index"?}` | `Project`. `index` moves the part: display order only, so no id, review, Local History or git moves with it; the part at position 0 is what old clients see as the project's `machineId`/`path`. `404 part_not_found`, `409 name_conflict`, `400 bad_part_name` |
| DELETE | `/api/projects/{id}/parts/{part}` | — | `Project`. Any part, the first one too, while another is left; when the first goes, `machineId`/`path` follow the new first part. The folder is not touched; the part's review and Local History are forgotten, and uptime monitors labelled with it lose the label. `409 last_part` for a project's one part, `409 part_busy` with `sessions` while that part still has terminals (a root console does not hold a part). `main_part` is gone |
| POST | `/api/projects/{id}/parts/{part}/split` | — | `201 {"project": Project, "from": Project}`: that part, any one of them, becomes a project of its own with one empty board "Main", the same group and tags. It keeps the part as it was, its id and name too, and its review and Local History move from `<project>~<part>` to `<new project>~<part>`; monitors labelled with it lose the label. `409 last_part`, `409 part_busy` (`sessions`) |
| GET | `/api/projects/{id}/merge?from=<project>` | — | Preview: `{"from","parts","workspaces","sessions","running","monitors","loops","memoryChars","subMemories","criticalChars","criticalFits","publicPage","problem"?,"code"?}`. A merge that cannot happen is still `200` with `problem` and `code` (`too_many_parts`, `part_overlap`, `merge_self`) |
| POST | `/api/projects/{id}/merge` | `{"from","critical"?: "append"\|"drop"}` | `Project`. See "Merge" below. `404 project_not_found`, `400 merge_self`, `409 too_many_parts`, `409 part_overlap`, `409 critical_too_big` |
| GET | `/api/projects/{id}/git` | — | `GitInfo` (read-only git via exec: `rev-parse`, `status --porcelain -b`; timeout 5 s; not a repo → `isRepo:false`) |
| GET | `/api/projects/{id}/close-all` | — | What would end if all boards were closed: `{"boards","tiles","sessions","running","apps","loops"}` (`tiles` without terminals; `apps` only apps that do not show on another board; `loops` that are running). |
| POST | `/api/projects/{id}/close-all` | — | `{"workspace": Workspace, "closed","archived","sessions","apps","loops"}`. All workspaces are replaced by one new empty workspace "Main" (new id): the project's loops are stopped (their definitions stay), sessions are stopped & deleted, desktop apps that show only on that board are closed, boards are archived (a board with no tiles other than terminals is not archived). Files on disk are not touched. Outside the planner's authority, like `workspace.close`. |
| POST | `/api/projects/{id}/workspaces` | `{"name"?}` | `201 Workspace` (default "Workspace N") |
| PATCH | `/api/workspaces/{id}` | `{"name"?, "index"?}` | `Workspace` (`index` = the new tab position) |
| DELETE | `/api/workspaces/{id}` | — | `{"ok":true}`; the workspace's sessions are stopped & deleted, the board is archived, desktop apps that show only on that board are closed. A project's last workspace cannot be deleted (`409`). |

**Opening a project (web).** `/p/<id>/w/<workspace>` is a board, `/p/<id>/code` the project's Code view (its editor full screen, bound to the board opened last, DESIGN.md §3.19). Where a Home card, the project menu or a link `/p/<id>` with no view goes is the hub setting `ui.openProjectsIn` (API-agents.md §11): `""` asks, `"board"`, `"code"`. Opening either view sends `PATCH /api/projects/{id}` with `opened:true`.

**Project groups and tags.** A group is a tree of names above projects, a tag is a word on a project. Both only arrange the list: a project's id, folder, boards and terminals do not change with them.

```jsonc
// ProjectGroup (project-groups.json); Project gets "groupId"? and "tags"?
{ "id": "g_…", "name": "Globex", "parentId": "g_…", "createdAt": "…" }   // no parentId = top level
```

| Method | Path | Request | Response |
|---|---|---|---|
| GET | `/api/project-groups` | — | `ProjectGroup[]` (sorted by name) |
| POST | `/api/project-groups` | `{"name","parentId"?}` | `201 ProjectGroup`. `404 group_not_found`, `409 name_conflict`, `400 group_too_deep` |
| PATCH | `/api/project-groups/{id}` | `{"name"?, "parentId"?}` | `ProjectGroup`; `parentId` moves the group with its contents, `""` to the top level. `400 group_cycle` when into itself, `400 group_too_deep`, `409 name_conflict` |
| DELETE | `/api/project-groups/{id}` | — | `{"ok":true}`. Only the group goes: the sub-groups and projects inside it move up one level, a `project` event is published for each project that moves |
| POST | `/api/project-groups/bulk` | `{"ids": ["g_…", …], "action": "delete"}` (1–200 ids) | `{"results": [{"id","ok":true} \| {"id","ok":false,"error":"…","code"?}]}`. Each group goes as with `DELETE` (its contents move up a level); `code` `group_not_found`. Groups have no archive: they are only names. `400 bad_action`, `400 bad_ids` |

- **Limits:** a group name is ≤ 24 characters, unique per level regardless of case. Depth is 5 levels; a move counts the contents that move with it. A tag is ≤ 24 characters without commas, ≤ 12 per project, duplicates are dropped (the first spelling is kept).
- **Deleting a group may leave two equal names** on one level, because everything works by id and refusing would mean that group cannot be deleted.
- **Damaged file:** at start, a group that lost its parent or forms a cycle moves to the top level, and a project whose group does not exist becomes ungrouped.
- **Order in the project menu:** the "Sort groups" and "Sort projects" rows in their menus choose `Name` (A–Z, numbers sorted as numbers) or `Recent`. `Recent` sorts groups by the project opened last inside them or in their sub-groups; empty groups go at the bottom. Default: groups A–Z, the project opened last on top. The choice is saved per browser (`nb.projects.menu.groups`, `nb.projects.menu.projects`), like collapsed groups. Home keeps its own Sort control.
- **Prefix migration:** a hub that has no `project-groups.json` yet turns name prefixes into groups once at start: `Acme - Portal` becomes `Portal` in group `Acme`. Only the ` - ` separator counts, so `demo-backoffice` stays one name, and `A - B - C` becomes `B - C` in group `A`. A new hub writes an empty file right away, so names created later are never split.

**Archive and delete for good.** Deleting a project offers two things. **Archive** keeps everything and can be undone at any time; **delete for good** removes what NextBoard keeps of the project. Neither touches the project's folders or files on any machine.

- **What archiving stops.** Its consoles end the way sleep ends them (API-agents §28): an agent of This PC whose conversation is known ends with its conversation kept and is `asleep` (what was typed at its prompt is kept when it can be followed exactly), whatever it was doing; any other console (a shell, an agent on a server or one started by hand at a shell) is stopped and shows `exited`. Its boards, memory, review and Local History, threads, loops, uptime monitors, documents and pasted files all stay.
- **While archived:** nothing of it starts, relaunches or wakes: not when its terminal is opened or typed into, not for deliveries, not at a hub start (`POST /api/sessions/{id}/wake` → `409 project_archived`, `POST /api/projects/{id}/wake` → `{"woken":0}`). No review snapshots or ticks are taken (what is there stays readable), loops do not fire, uptime monitors are not checked and show `paused` (a push is refused as for a paused monitor; the public status page stays as it was). The project is not in `GET /api/projects` nor in the snapshot of `/api/events`, nor in the lead's and the MCP tools' project lists, and the command bar answers `409 project_archived`. Adding a project for a folder an archived project holds returns that project (`200`, with `archivedAt`): restore it to use it.
- **Restore** brings it back as it was: monitors are checked again at once, loops fire on their schedule from now (runs missed meanwhile are not made up for), agents stay asleep and wake when their board is opened, they are typed into or sent work, as asleep ones always do. Consoles that were stopped stay `exited` until restarted.
- **Delete for good** (`?permanent=1`, bulk `delete`) stops and deletes its consoles and their scrollback, its boards with their snapshots and their copies in the board archive (`GET /api/archive`; also those of boards closed earlier), the desktop apps only its boards show, its lead's thread and the threads of the orchestrator cards on its boards, its orders, handoffs and loops with their history, its memory (MEMORY.md, the agents' notes and sub-memories) and its blocked commands, review and Local History (the shadow repositories and the copies of server parts), its uptime monitors with their history and its public status page, its documents, the files pasted into its orders (`<data>/attachments/project-<id>`), running jobs of it (cancelled), and the project record.
- **Events:** archive and restore publish `{"type":"project","project":…}` (clients drop an archived one from the active list); `projectRemoved` is only for a delete for good.

**Cover and icon.** A project card shows a cover across its top and an icon next to its name. Both are images of the asset store: upload with `POST /api/assets`, then `PATCH /api/projects/{id}` `{"cover": {"assetId"}}` (or `"icon"`); `null` removes one.

- **Create with AI.** `GET /api/art/capability` says whether an image tool is on This PC: the `codex` CLI found by the hub's agent catalog (settings path or the login PATH, not turned off) whose `codex features list` shows `image_generation` on. Otherwise `available:false` with the reason, and the web does not show the option. The tools are a list tried in order; only Codex exists now.
- **The job** (`POST /api/projects/{id}/art`, one image at a time on the hub; phases `queued`, `reading`, `generating`, `saving`) writes a short brief of the project from its own content: its name, the names of its parts, and for **every part** whose machine is connected (read at once through the machine file API, so on a server too; a product is described best by the part its users see, not only the first) what its code is about, by names only: the files in its model folders (`app/Models`, `models`, `src/entities`, Prisma `model`s), the tables in its migration file names (a framework's own tables left out), the first segment of each path in `routes/web.php` and `routes/api.php`, and its page folders (`resources/js/Pages`, `src/pages`, `pages`, `src/routes`, …); the beginning of `README*` (~4 KB for one part, ~4 KB shared among several, at least 1 KB each), the name and description in `package.json` and `composer.json`, the module in `go.mod`, what its `index.html` (`./`, `public/`, `src/`, `web/`, `frontend/`) says in `<title>`, `description`/`og:*` and `theme-color`, the name, description and colours of its web app manifest, and its **brand colours**: the most used colours of its logo or favicon SVG (in the app, `public/` or an image folder; white, black and greys left out), then brand colours its main stylesheet or Tailwind config names (`--color-primary`, `primary: '#…'`), else the most used colours of its built stylesheet (`public/build/assets`, `dist/assets`, …); and the **dark colours** that built stylesheet uses most (backgrounds, at most two). Colours are not read from a folder whose name says it is a back office or plumbing (`admin`, `backend`, `api`, `service`, `dashboard`, `panel`, …): their look is a UI kit's, not the brand's. A part folder that holds apps instead of being one (a web root with a backend and a front end inside) is read one level down, up to 6 apps, front ends first. A framework's starter text (Laravel's skeleton name and description, starter READMEs) is left out, and so are template placeholders (`{{ … }}`, `%VITE_…%`). The project memory (its first ~1 KB) goes in only when the folders said almost nothing, and without its lines about running the product (servers, ports, certificates, deploys). URLs, host names, IP addresses, mail addresses and paths are taken out of the READMEs and the memory before anything is sent. Files whose names say they hold secrets (`.env`, keys, credentials) are never read; control characters are removed and the brief is at most 12 KB. It then runs `codex exec` on This PC in an empty temp folder: `--skip-git-repo-check`, `--sandbox read-only`, the user config not loaded (`--ignore-user-config` where the CLI has it), web search and the browser, computer-use, apps and plugin features off, the default login profile of Codex (its `CODEX_HOME`), at most 3 minutes, with a prompt that asks for exactly one image and marks the brief as data. Both picture **the product as its users know it**, never how it is built: several parts are one product; no servers, data centres, code, dashboards, network lines, and no fantasy or metaphor (floating islands, castles, domes, portals) unless the product is about that; the whole palette built on the brand colours when the brief lists them. The model is told to work out what the product does from the names in its code first, not from the project's name, which may be a working label; the prompt has no example scenes, because a model copies them. A cover is **only a background**, a wide 3:2 (1536×1024) scene its users recognise at once, of what the product offers and who it is for, commercial photography or a high-end 3D render; people, if any, adults, fully clothed and tastefully shown; **no text of any kind** and no logos or brand marks of other companies anywhere (also on clothing, equipment or signs); composed for the card: the subject inside the middle 3:1 band and in the right half, the left 40% calm and darker. The clients set the title over it themselves (below), because a model's lettering changed from one image to the next. Codex's reply holds the image's path and a tagline of two to five words; the tagline is kept only when it is one short line of words (two to eight, at most 48 characters, letters, digits and plain punctuation), and is only ever shown as text. In a folder that holds apps, the ones its users see (`frontend`, `web`, `site`, `app`, `client`, …) are read first and the back office (`admin`, `api`, `service`, …) last, up to 6. An icon is a square 1024×1024 premium app-store icon: one object that stands for what the product offers, joined with what sets it apart (its standing, audience or theme) when that is clear; in the look of a premium mobile game's icon: layered materials (a deep glossy enamel body in the dark tone taken from the dark colours, a polished metal rim, edge and raised symbol in the brand colours, a light ivory or cream accent on repeated details), never one colour family only; seen straight from the front with no tilt (a three-quarter view warped the shapes) yet clearly a 3D model rendered in a studio: its depth comes from the model (layers stepping toward the viewer, a symbol in high relief, deep bevels, physically based materials, specular edge highlights, ambient occlusion, contact shadows); precise as a vector design: symmetrical, round parts exact circles, repeated parts identical and evenly spaced, crisp outlines; one soft key light from the upper left and a faint rim light; always the same composition for consistent results: centred, about 80% of the square wide with equal space around it, on a smooth gradient of the dark tone, nothing else; readable at 32 px, no text.
- **The result.** The image is the file Codex made in its `generated_images` folder during the run: the path in its reply, else the newest new file in the folder of its session, else in the whole folder. Nothing outside that folder is read, whatever the reply says, and Codex's reply is never executed. The file must be a PNG or JPEG of at most 25 MB and 8192 px a side; a cover is stored as a JPEG at most 1600 px wide, an icon as a 256×256 PNG cut from its centre. The project then gets it with `source:"ai"` and a `project` event, and the job's result is `{"kind","assetId","url","type","w","h"}`, for a cover with `"title":true,"tagline","accent"`. Failures end the job with a code: `no_image` (Codex finished without an image), `timeout`, `cli_not_logged_in`, `cli_failed`, `bad_image`, `no_image_tool`, `project_not_found`.
- **The title over a cover.** A cover with `title:true` is shown with a frame over it: a dark gradient from the left fading out toward the right, the project's name without working labels (`PROD - Lumen` → `Lumen`, `shop (staging)` → `shop`; the web's `coverTitle`, the same rule as the hub's `art.DisplayName`), heavy, in capitals, in the app's own font (Geist), and the tagline under it, white. The name is in `accent`, the brightest of the first three brand colours that reads on the dark side, else white. The frame takes the project's current name, so a rename shows at once, and its sizes follow the cover's height, so every card and the dialog's preview look the same. Uploads and covers made before have no `title` and show as they are.
- **Privacy.** It uses the owner's Codex account. The brief (the name, part names, the names in each part's code, its README excerpt, descriptions, web page title and description, and sometimes the memory excerpt, all without URLs, hosts, addresses or paths) is sent to the model.

**Project parts (ROADMAP #49).** One project can span several folders, on one machine or more: the backend on one server, the frontend on another. Memory, the lead, boards, task boards, uptime and credentials stay one per project. Per part: terminals and consoles (`partId`, a default folder per machine), files, git and the code workbench. An old `projects.json` gets a part `main` (named after its folder) when it is opened, so clients that do not know parts yet still read `machineId`/`path`.

- **All parts are equal** (v0.9.0-beta.2). No part is the main one: any part may be renamed, moved or removed, and split off, while one is left (`409 last_part`). The order is only the display order. Nothing is keyed by position: ids, review and Local History, git state and events keep their key when the parts are reordered or the first one goes. `project.machineId`/`path` are the first part's only for clients that know nothing of parts. Old data whose first part has the id `main` works as it is, with nothing special about that id.
- **Part names.** A part's name is also its folder's name at the project root, so it is safe as one path segment: 1–24 characters, no `/`, `\` or control characters, not `.` or `..`, not starting with `.`, not `AGENTS.md` or `CLAUDE.md` (any case), and unique in the project regardless of case. Otherwise `400 bad_part_name` (`409 name_conflict` for a taken name). The default name is the folder's name, made safe (`/` → `Part`).
- **Project ref.** The workbench's `/api/projects/{id}/…` routes (files, search, LSP, DAP, Laravel, Markdown, eslint, git, tasks, review) accept an `{id}` of the form `<project>~<part>` or `?part=<part>`, for every part, the first one included. The project is then seen through that part: its machine and folder. A ref without a part = the first part in display order, only for old clients: the web names the part on every workbench call. `404 part_not_found` for a part that does not exist. `projectId` in `fs/write` and `fs/trash` may be a ref, and without a ref the folder is found from the part on that machine that holds the file.
- **Review and Local History per part.** Each part has its own review, from its folder (a shadow repo on This PC, a copy for a part on a server). Its key is `<project>~<part>` for every part, the first one too, the same as the project ref; nothing is kept under a bare project id. An agent's changes go to the review of the part where it made them, including through the `part` tool, and the `review` and `reviewOverlap` events carry that key as `projectId` (an old client that waits for a bare project id misses the first part's live refreshes). Deleting a part or project forgets its review.
- **Git per part.** Git state is kept per part. Every part broadcasts `{"type":"gitPart","projectId","part","repo","status"}`; the first part in display order also sends the old `{"type":"git"}` (its own repository) or `{"type":"gitRepo"}` (one below its folder), so old clients keep seeing it. After a reorder the old events follow the new first part. The web keeps git state under `<project>~<part>` (`lib/parts.ts` `partRef`).
- **Workbench.** An editor card is placed like a console (v0.9.0-beta.2): its node's `data.part` is a part id, and absent, `""` or a part that no longer exists means the **Root**, one tree with every part as a top-level folder. A board holds at most one card per placement (a second copy shows "already open, Show it"), each with its own tabs, explorer, git, review and debug state; two cards showing the same file share its unsaved edits. A single-part project has one placement, so its card looks as before and is never asked about. Opening the editor from the rail, the ring or the studios menu in a project of several parts asks **Show: Root (all parts) / <part>** (default: the focused tile's placement, else the last used); a file picks the card of its part, else a Root card, else adds a card in its part, without asking. The old per-browser part switch (`nb.wb.part.<project>`) is no longer read. Details: API-studio §7.7.
- **Place in** (web, v0.9.0-beta.2). Every place that starts a console or a group in a project of several parts shows "Place in: Root (all parts) / <part>…": the new console form (the command bar's agent chip, the phone sheet), the Spawn tool's flyout (T), the ring's Spawn agent, Spawn N and the team composer, the layouts panel (per group, per lane and per editor card), and the handoff dialog (default: the source console's placement). The default is the focused tile's placement (focus mode, else the selection: a console's root or part, an editor card's placement, the part holding a file card, a web tile's or branches card's part, a group whose members agree), else the one used last in this browser for the project (`nb.place.<project>`), else the root. A single-part project never shows it. "Other…" in the new console form keeps the old machine and folder choice. The command bar sends the same default as the command's `place`, and its agent chip names it ("Claude Code · Root"). Undo, paste and "Resume in a new console" start the new console where the old one worked: its root or part, and for an agent of a server or read-only part that machine and folder (not the hub's work folder on This PC); a console pasted from another project keeps its machine and folder, not that project's part.
- **Merge.** The `from` project becomes parts of this project: each of its folders becomes a part (named like that project, or `<project> · <part>` when it has several, made path-safe, with a number when the name is taken), its workspaces become tabs here (their names get a prefix on a clash, ids stay), terminals and consoles move to their new part without a restart, uptime monitors move with their history but come off the public page (the public page of `from` goes dead), loops come along, its memory and agent notes become a sub-memory named from the project's name (`shop-web`), its sub-memories become `<name>-<slug>`, the review and Local History of each folder move to the key of its new part, tags are joined, its lead thread is merged into this project's thread by time (within the thread's message limit, oldest dropped first), then that thread gets one note. The critical notes of `from` are added only with `"critical": "append"`, under the heading `## From <name>`; without it they are not brought over. Credentials limited to `from` do not change, because the limit is inside the encrypted blob: the owner opens them in Credentials. The id of `from` goes into this project's `aliases`, so `/p/<from>/w/<workspace>` opens the same board here. Folders on disk are not touched.
- **Blocked commands per part.** `PATCH …/parts/{part}` `{"blocked": ["git push", …]}` (max 50, each ≤ 200 characters, one line; `400 bad_blocked`). In that part they apply on top of the hub and project lists: when a console in that part is launched, and when the hub's `bash` runs in that part, also from an agent in another part.
- **Agents.** See API-agents §21.1: its own part and other parts on the same machine in full, parts on another machine through hub tools according to the project's `partAccess` (`""` read-only, `"full"`), read-only parts read-only for everyone.

**Migration at start:** if `projects.json` does not exist yet, the hub creates a project "Home" (`local`, home dir) with a workspace "Main", moves all old sessions there, and converts the old `board.json` (positions per session) into `terminal` nodes. The old `board.json` is renamed `board.json.migrated`.

**Equal parts migration** (v0.9.0-beta.2, every step idempotent, so a second start changes nothing):
- **Part names.** A part name that is not path-safe is made safe (`/` and `\` → `-`, leading dots dropped, `.`/`..` → `Part`, ` part` added to `AGENTS.md`/`CLAUDE.md`, a number when taken), and the project's lead thread gets one note listing the renames. A one-part project whose part is the old `{main, "Main"}` is named after its folder, once (`projects-migrated.json` records it, so a part the owner names "Main" again stays so). A project in several parts keeps "Main" as a plain, renamable name. Ids never change. `projects.json` is saved only when something changed.
- **Review keys.** Before review starts, `review/<project>` (the first part's snapshots, Local History and server copies) is renamed to `review/<project>~<first part>`. A rename is atomic; a project whose part key already has data keeps both folders, and it is logged.
- **Sessions.** A session in a project with no `partId` and not at the root gets the part that holds its folder (on a server or in a read-only part, the folder it works on there), else the first part, which is what `""` meant before. After this, `partId: ""` without `root` means "in no part's folder", and `part_busy` counts those old sessions too.

**Sessions:** `Session` gets the fields `projectId`, `workspaceId`, `partId` and `root` (only when `true`). `POST /api/sessions` accepts:

```jsonc
{ "workspaceId": "w_…", "partId"?: "pt_…", "root"?: true, "machineId"?: "…", "preset": "claude", "name"?: "…", "cwd"?: "…", "cols"?: 120, "rows"?: 32,
  "node"?: { "x": 0, "y": 0, "w": 720, "h": 440 } }
```

A console is placed at the project root (`root`) or in one part (`partId`), and never moves (v0.9.0-beta.2). The rules, in order:

| Request | Result |
|---|---|
| `root` and `partId` | `400 bad_placement` |
| `root` in a project of one part | that part: the answer has its `partId` and no `root`, so such a project never gets a root folder |
| `root` in a project of two or more parts | `machineId` absent or `"local"` and no `cwd`, else `400 bad_placement`. The console runs on This PC (`machineId:"local"`) in the project's root folder, `cwd` `<data>/roots/<projectId>`, `partId` `""`, `root:true`; it reaches every part (API-agents §21.2). Shells and agents alike, never `remoteMachineId`. A root folder that cannot be made → `500 root_unavailable` |
| `partId` | as below |
| neither (old clients) | as below |

- **Project root.** The root folder is a view of the project on This PC: a link for each part on This PC that agents may change, a note folder for a read-only part and for a part on a server (nothing is copied), and `AGENTS.md`/`CLAUDE.md` listing every part with its machine, folder and what an agent may do there (API-agents §21.2). The hub keeps it current on every launch of a root console and on every change of the project, and removes it when the project is deleted for good. A terminal at the root is a plain shell in that folder: `ls` shows the parts, `cd Web` works in that part's folder.
- `root`, `partId` and `cwd` are fixed for the console's life: restart, sleep, wake and a hub restart keep them, and the hub syncs the root folder before each of those launches. To work somewhere else, open a new console. A merge keeps a root console at the root, in its folder. A root console does not hold a part (`part_busy` does not count it), and it is never read-only itself: its read-only parts are notes and hub read tools.
- With `partId`: `machineId` defaults to that part's machine (another machine → `400 part_machine`), `cwd` defaults to the part's folder. `404 part_not_found`.
- Without `partId`: `machineId` defaults to the project's machine; an empty `cwd` = the folder of the first part on that machine (on a machine without a part: home); a `cwd` inside a part's folder records that part's `partId`.
- An agent in a read-only part works as on a server: the CLI on This PC with its local tools off, and only the hub's `read_file`, `grep`, `glob` work in that part (`remoteMachineId` = the part's machine, `local` too). The same when a shell in that part is switched to an agent, or when an agent in a part that was just made read-only is restarted. An agent whose CLI cannot work through hub tools → `422 part_read_only`. A plain shell is still allowed.
- Without `workspaceId` (old clients/Android): the first workspace of the project opened last is used; if there is no project at all, "Home" is created.
- The hub adds a `terminal` node to that workspace's board (at the `node` position or automatically to the right of the last node) in one operation, then broadcasts `boardOps`.
- `DELETE /api/sessions/{id}` also deletes its terminal node (broadcast `boardOps`).

## 2. Board v2

```jsonc
// Board
{
  "workspaceId": "w_…", "schemaVersion": 2, "rev": 42,
  "nodes": [Node…],   // array order = z order (last = topmost); the web raises the card touched last only in its own view (`zIndex`), without an op
  "edges": [Edge…]
}
// Node
{
  "id": "n_k3j2h1q9z8",            // made by the client (prefix n_ + 10 chars) or by the hub for terminals
  "kind": "terminal",               // see the kind table
  "x": 0, "y": 0, "w": 720, "h": 440,  // board coordinates (px), w/h > 0
  "rotation": 0,                    // degrees, optional
  "locked": false,                  // optional
  "data": { … }                     // content per kind; the hub validates it only for kind "terminal"
}
// Edge (a link between nodes; a free arrow is a node of kind "line")
{ "id": "e_…", "source": "n_…", "target": "n_…", "kind": "wire", "data": {} }
```

The viewport is **not** stored in the board (it is per client, in localStorage).

### Kind and `data`

| kind | `data` |
|---|---|
| `terminal` | `{ "sessionId": "s_…" }` — the hub creates/deletes it; a client may not `put` a terminal node for a session that does not exist (`400`) |
| `note` | `{ "text": "…", "color": "yellow"\|"pink"\|"blue"\|"green"\|"violet"\|"orange" }` |
| `text` | `{ "text": "…", "font": "sans"\|"serif"\|"mono"\|"hand", "size": "s"\|"m"\|"l"\|"xl", "align": "left"\|"center"\|"right", "color": "#hex" }` |
| `checklist` | `{ "title": "…", "autoRun"?: true, "items": [{ "id", "text", "done": bool, …task fields }] }` |
| `kanban` | `{ "title": "…", "autoRun"?: true, "columns": [{ "id", "title", "color"?: "amber"\|"sage"\|"sky"\|"violet"\|"rose"\|"teal", "cards": [{ "id", "title", "notes"?, "due"?: "YYYY-MM-DD", "priority"?: "low"\|"med"\|"high", "done"?: bool, …task fields }] }] }` (default 4 columns: Backlog, To do, In progress, Done). The task fields are written by the hub, see API-orchestration.md §3.10: `assignee`, `sessionId`, `orderId`, `state`, `error` |
| `calendar` | `{ "title": "…", "month": "YYYY-MM", "events": [{ "id", "title", "date": "YYYY-MM-DD", "time"?: "HH:MM", "notes"?, "color"? }] }` |
| `uptime` | `{ "title": "…" }`; its monitors belong to the project, see [API-uptime.md](API-uptime.md) |
| `credentials` | `{ "title": "…" }`; the contents are in the vault, never on the board, see [API-vault.md](API-vault.md) |
| `shape` | `{ "shape": "rect"\|"ellipse"\|"diamond", "stroke": "#hex", "strokeWidth": 1\|2\|4, "strokeStyle": "solid"\|"dashed"\|"dotted", "roughness": 0\|1\|2, "fill": "#hex"\|"transparent", "fillStyle": "hachure"\|"cross-hatch"\|"solid", "edges": "sharp"\|"round", "opacity": 10..100, "text"?: "…", "font"?, "size"?, "align"?, "seed": int }` |
| `line` | `{ "points": [[x,y],[x,y],[x,y]], "arrowStart": bool, "arrowEnd": bool, "stroke", "strokeWidth", "strokeStyle", "roughness", "opacity", "seed" }` — points relative to the node's (x,y); the middle point = the curve's control point. Optional for wiring-style arrows ([API-studio.md](API-studio.md) §6): `"route": [[x,y]…]` (≥ 2 points, straight horizontal/vertical segments, replacing the curve; `points` stays a straight line between its ends for clients that do not know it yet) and `"dots": [[x,y]…]` (junction points) |
| `ink` | `{ "points": [[x,y,pressure]…], "color": "#hex", "width": 1..16 }` — relative to the node's (x,y) |
| `image` | `{ "src": { "asset": "a_…" } \| { "machineId", "path" }, "alt"?: "…", "naturalW", "naturalH" }` |
| `file` | `{ "machineId", "path", "view": "code"\|"markdown"\|"file"\|"folder", "snapshot"?: { "content", "size", "mtime", "truncated" } }` — `code` = snapshot (does not change with the file); `markdown` = live (the client polls `fs/stat`); `folder` = a list of ≤ 40 files |
| `app` | `{ "appId", "name", "sessionId" }` — a desktop app on This PC, see [API-gui.md](API-gui.md) |

Clients ignore kinds they do not know (rendering an "Unsupported tile" placeholder), so new kinds can be added in later phases without breaking old clients.

### Endpoint

| Method | Path | Request | Response |
|---|---|---|---|
| GET | `/api/workspaces/{id}/board` | — | `Board` |
| POST | `/api/workspaces/{id}/board/ops` | `{"clientId":"c_…","baseRev":42,"ops":[Op…]}` (max 2 MB, ≤ 500 ops) | `{"rev":43}` |
| GET | `/api/workspaces/{id}/snapshots` | — | `[{ "id": "2026102814", "at": "…", "nodeCount": 12 }]` (newest first) |
| POST | `/api/workspaces/{id}/snapshots/{sid}/restore` | — | `Board` (the current version is first saved as a snapshot; terminal nodes whose sessions no longer exist are dropped) |
| GET | `/api/archive` | — | `[{ "id", "projectId", "projectName", "workspaceName", "archivedAt", "nodeCount", "reason": "closed"\|"cleared"\|"unreadable" }]`; `projectId` is the board's project, absent in archives from before it was kept. A restore offers that project by id, even renamed, else one of the same name |
| POST | `/api/archive/{id}/restore` | `{"projectId"}` | `201 Workspace` (the board is restored without terminal nodes) |

```jsonc
// Op — applied in order, atomically; last-writer-wins per node
{ "op": "put",     "node": Node }                       // insert or replace the whole node; keeps its old z position if it already exists
{ "op": "patch",   "id": "n_…", "set": { "x": 10, "y": 20, "w"?, "h"?, "rotation"?, "locked"?, "data"?: {…} } } // top-level fields are replaced; "data" is merged shallowly (key by key)
{ "op": "del",     "id": "n_…" }                        // a terminal node cannot be deleted by an op (use DELETE session) → the op is ignored
{ "op": "z",       "id": "n_…", "to": "front"|"back" }
{ "op": "putEdge", "edge": Edge }
{ "op": "delEdge", "id": "e_…" }
```

- `baseRev` is for information only (not refused when stale); convergence comes through the op broadcast.
- Validation: unique ids `^[a-z]_[a-z0-9]{6,24}$`, finite numbers, `w,h` 1…20000, |x|,|y| ≤ 20000, `data` a JSON object ≤ 256 KB per node, total board ≤ 8 MB, an edge must point to existing nodes (orphan edges are deleted automatically when a node is deleted).
- Storage: debounce of 2 s after the last change, delayed at most 7 s; `boards/<wid>.json` + `boards/<wid>.json.prev`; the first snapshot in each hour that has changes goes to `snapshots/<wid>/<YYYYMMDDHH>.json` (the 24 newest are kept); archive to `archive/<id>.json` (never pruned).
- **Recovery (tested with `kill -9` and damaged files):** a normal stop saves all changes. A hub that dies suddenly loses at most the last few seconds (2 s, up to 7 s while edits keep coming). Board files are written atomically; if one is still damaged, the hub uses `.prev` (losing the last save) and archives the damaged file with reason `unreadable`. If `.prev` is damaged too, the board is empty and is restored from a snapshot (at most one hour back). A restore always saves the current version first, and that copy never overwrites another copy, so a restore can be undone. Consoles come back according to `resumeOnRestart`; Android and desktop use the same API path.

### Auto arrange

The *Auto arrange* button in the right rail, the first button of the right-click ring, and the chip of the same name above the command bar rearrange the board on the client (`web/src/board/arrange.ts`), through ordinary `patch` ops, so the result syncs to other clients and is one undo step.

- Card sizes do not change, and the result has no overlaps. Running it again on the same board changes nothing.
- Cards linked by edges stand side by side in columns from source to target, with room for the wires; a level with more than three cards may widen into several columns. The rest take the highest free spot, then the leftmost, larger cards first, so cards of one kind form rows and the space beside tall cards gets filled. The total width follows the shape of the free space on screen, then the view is fitted.
- A wire component that contains an orchestrator card is laid out as a star: the lead in the middle, the cards connected to it around it, with a group as one block. Their number decides the shape: 1 on the right; 2 left and right; 3 adds below; 4 above, below, left, right; 5 to 8 fill a 3×3 ring; 9 and up use a wider ring whose shape follows the screen. Connected kanbans and checklists sit in a row of their own below the star, centered on the lead. When one component has several orchestrator cards, the one with the most connections becomes the center.
- A group moves whole with its members. New members are rearranged when they overlap each other; a tidy arrangement is left alone.
- Locked cards (or groups with a locked member) do not move, and the others avoid them.
- `ink`, `line`, `shape`, `text`: one whose center lies over a card goes with that card, a `shape` that encloses cards keeps enclosing them, and loose strokes that touch each other stay whole and are placed last in the space that is left.

## 3. New events on `/api/events`

```jsonc
{ "type": "snapshot", …, "projects": [Project…] }       // an extra field on the snapshot
{ "type": "project", "project": Project }               // created / changed (name, workspaces, session count, archived, restored)
{ "type": "projectRemoved", "id": "p_…" }               // deleted for good (an archived project gets a `project` event with archivedAt)
{ "type": "projects" }                                  // after a bulk action: refetch the list
{ "type": "projectGroups", "groups": [ProjectGroup…] }  // the whole tree, every time it changes; also the `projectGroups` field on the snapshot
{ "type": "boardOps", "workspaceId": "w_…", "rev": 43, "clientId": "c_…", "ops": [Op…] }
// empty clientId = an op from the hub (session created/deleted, restore)
```

Clients ignore `boardOps` that carry their own `clientId`. When `rev` jumps (a broadcast was missed, e.g. after a reconnect), the client reloads the board.

## 4. Machine files (P10)

All paths are absolute; `~` and `~/…` expand to the machine's home. Local uses the filesystem directly; SSH uses the **SFTP subsystem** on the existing connection (built into OpenSSH, installs nothing), with a read-only exec fallback (`stat`, `head -c`) when SFTP is turned off.

| Method | Path | Request | Response |
|---|---|---|---|
| GET | `/api/machines/{id}/fs/stat?path=` | — | `{ "path", "name", "dir": bool, "size", "mtime", "exists": bool }` (`exists:false` when it does not exist, not 404) |
| GET | `/api/machines/{id}/fs/read?path=&max=` | `max` default 800000 | `{ "path", "size", "mtime", "binary": bool, "truncated": bool, "content": "…" }` (UTF-8; `binary:true` → empty content) |
| GET | `/api/machines/{id}/fs/raw?path=` | — | raw bytes ≤ 20 MB, `Content-Type` from the extension (image/pdf/text), `Content-Security-Policy: sandbox`, `nosniff` |
| PUT | `/api/machines/{id}/fs/write` | `{ "path", "content", "encoding": "utf8"\|"base64", "ifMtime"?: "…", "unique"?: bool }` (max 20 MB) | `Stat`; `409` when `ifMtime` does not match; `unique:true` → if the name already exists, the hub uses `name-2.ext`, `name-3.ext`, … and returns the final path |
| PUT | `/api/machines/{id}/fs/write?path=&unique=&ifMtime=&projectId=&allowOutside=&confirm=` | raw bytes, `Content-Type: application/octet-stream`, `Content-Encoding: gzip` allowed (max 20 MB after unpacking) | the same as the JSON form; `confirm` once per path. For uploads from a computer: no base64, the hub spools the body to a temporary file in its data folder (not in memory) and then writes it, so an interrupted upload does not leave half a file. `400` broken gzip / truncated body, `413` over 20 MB (also a small body that unpacks to a large one), `415` another encoding |
| GET | `/api/machines/{id}/ls?path=` | (already exists) | adds `size` and `mtime` per entry |

Writes happen only on an explicit user action (save, copy a file into the project), in line with the agentless rule of category (a).

## 5. Assets (image blobs on the hub)

| Method | Path | Request | Response |
|---|---|---|---|
| POST | `/api/assets` | raw body, `Content-Type: image/png\|image/jpeg\|image/gif\|image/webp`, `Content-Encoding: gzip` allowed (max 20 MB) | `201 { "id": "a_…", "url": "/api/assets/a_…", "type", "size", "w", "h" }` |
| GET | `/api/assets/{id}` | — | bytes, `Cache-Control: private, max-age=31536000, immutable`, `nosniff`, `Content-Security-Policy: sandbox` |

SVG is refused (script risk). Stored in `<data>/assets/` (0600).

## 6. Hub implementation notes

Behavior not detailed above (Phase 4 hub):

- **patch:** `set` accepts only `x`, `y`, `w`, `h`, `rotation`, `locked`, `data` (other fields → `400`). In `data`, a `null` value **deletes** its key.
- **Ops on a node/edge that no longer exists** (`patch`, `del`, `z`, `delEdge`) are skipped without an error. A batch whose ops are all skipped → `{"rev": <current rev>}` without a broadcast and without raising `rev`.
- **The `boardOps` broadcast** holds the ops as the hub normalized them (full nodes/edges with defaults, e.g. `data: {}`, edge `kind: "wire"`). A node `del` is followed by `delEdge` for the edges deleted with it.
- **Terminal nodes:** a `put` must be for a session in the same workspace, one node per session; a terminal node's kind cannot be changed and `data.sessionId` cannot be changed (`400`). A `del` from a client is ignored.
- **Snapshot restore** is broadcast as `delEdge` + `del` for all the old content, then `put` + `putEdge` for the new content (z order included). Terminal nodes of sessions that are still alive but not in the snapshot are kept.
- **The hourly snapshot** holds the board *before* the first save in that hour (so you can always go back to the state at the start of the hour). The automatic copy before a restore uses the id `YYYYMMDDHHMMSS` (14 digits); a 10-digit id = an hourly snapshot. Both count toward the limit of the 24 newest. Hours are in UTC.
- **Archive:** id `r_…`; an entry stays after it is restored. A board with no nodes other than terminals is not archived when it is closed. `cleared` = one op batch that deletes all non-terminal nodes from a board that has ≥ 3 nodes.
- A board with `schemaVersion` > 2 (from a newer hub) can be read, but its ops are refused with `409`.
- **File API — error codes:** `400` relative path / folder path, `403` access denied (including the hub's data folder on This PC, e.g. `secret.key`, for read/raw/write), `404` does not exist (also the parent folder on `fs/write`), `409` `ifMtime` does not match, `413` > 20 MB, `501` SSH machine without SFTP (writing not supported), `502` machine not connected yet, `503 ssh_sessions_full` (see below). `ifMtime` is compared at the precision the client sent (seconds/milliseconds/nanoseconds). A folder's `size` = 0.
- **fs/raw:** image extensions (including `.svg`, still with `sandbox`) and `.pdf` use their real type; content that looks like text → `text/plain; charset=utf-8` (HTML is never served as HTML); anything else `application/octet-stream` + `Content-Disposition: attachment`.
- **ls:** `size`/`mtime` are missing (size 0, no `mtime`) in the SSH exec fallback without SFTP.
- **Assets:** the request's `Content-Type` must be one of the 4 types; the stored/returned type is the result of sniffing the file's content.
- **POST /api/sessions:** `cwd` defaults to the project path only when the session's machine = the project's machine (otherwise the machine's home). A project/workspace name > 24 characters → `400`; default names are cut to 24.
- The SFTP client per connection is closed after 2 minutes unused (its channel counts toward sshd's `MaxSessions` limit) and opened again when needed.
- **The `MaxSessions` limit:** sshd accepts a number of channels per connection (OpenSSH: 10) and refuses the next with `connect failed ("open failed")`. A code workbench that was just opened sends many commands at once, so a refused channel is asked for again (backoff 20–250 ms) until one is free, for at most 15 s or until the request ends. That applies to commands, consoles, pipes and SFTP (`openSession`, `internal/machine/exec.go`). When it stays full, the file API and git answer `503 ssh_sessions_full`. The connection itself is not considered dropped.
