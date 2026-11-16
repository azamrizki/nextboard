# NextBoard API — Phase 6: orchestration

Extends [API.md](API.md), [API-board.md](API-board.md) and [API-agents.md](API-agents.md) (handle, `agentState`, `foreground`, transcript, settings). Features: [ROADMAP.md](ROADMAP.md) Phase 6. Owner decisions #5 and #6 are binding.

**Cross-phase dependencies:** the planner, handoff summaries, the "agent judged" goal and the memory scan use the **hidden runner** defined in [API-studio.md](API-studio.md) §2. The runner's core (a `claude -p` job on This PC + the Job API) must be built at the start of Phase 6. Git steps in the command bar use the Phase 7 git service; until then the hub replies "Git actions are not available yet".

## 0. Design summary

- One "brain" in the hub. Web, desktop and Android send the same text to `POST /api/projects/{id}/commands`; the result is identical on every client.
- Pipeline: **local parser** → (optional) Claude Code **planner** on This PC → **steps** → **confirmation policy** → **execution** → **thread**.
- Text reaches an agent **only** through the **delivery engine** (§2): bracketed paste + Enter, only when the agent is in the foreground, queued while `working`/`needsYou`, never into a shell.
- Groups, orchestrator cards and memory cards are board nodes the **hub understands**; wires and forwards are edges.
- Loops run in the hub 24/7 with no client.

## 1. Board: new kinds and edges

| kind | `data` | Hub validation |
|---|---|---|
| `group` | `{ "name": "Backend", "role": "general"\|"frontend"\|"backend"\|"testers"\|"reviewers"\|"design"\|"docs"\|"security", "color": "amber"\|"sage"\|"sky"\|"violet"\|"rose"\|"teal", "members": ["n_…"], "syncInput"?: true }` | name 1–24 characters, unique per workspace (case-insensitive) and not equal to any handle in the hub (`409 name_conflict`); `members` = `terminal` nodes on the same board, max 12; a terminal is in ≤ 1 group (`400 member_conflict`) |
| `orchestrator` | `{ "title": "Orchestrator", "personality"?: "…" }` | title ≤ 40; the card's thread = node id; a title other than `Orchestrator[ n]` = the name of that card's lead (§3.9) |
| `memory` | `{ "blocks"?: [{ "projectId"?, "name", "lookup"? }], "manual"?: false, "lastPush"?: { "at", "delivered", "busy", "skipped", "upToDate" } }` | max. 32 blocks; `400 bad_memory` for a block with no name or one named `critical`. An old tile with `scope: "global"` is read as one memory block of all projects (§12) |

| Edge kind | source → target | `data` |
|---|---|---|
| `wire` | `orchestrator`, `memory`, `editor`, or a content tile → `terminal` or `group`; `editor` and `memory` also → `orchestrator`; `memory` also → `editor`; `orchestrator` also → `kanban` or `checklist` (§3.10), and → `editor`: the card directs the agents opened inside that editor card ([API-studio.md](API-studio.md) §7.8), also those opened later. `editor → orchestrator` is the editor's Ask, a different edge; both may exist between the same two cards | `{}`; unique per pair and direction; other combinations → `400 bad_edge` |
| `forward` | `terminal`/`group` → `terminal`/`group` | `{ "mode": "digest"\|"split", "trigger": "turn"\|"order" }`; forward edges may not form a cycle (`400 forward_cycle`) |
| `preview` | `terminal` → `web` | `{}`; a marker line, made by the hub when a URL is detected, or drawn by hand |
| `log` | `web` → `weblog` | `{}`; the log card follows this page |

**Content tiles** (`note`, `checklist`, `kanban`, `calendar`, `file`, `image`, `link`, `document`, `branches`, `web`, `weblog`): their content is sent to the agent at the end of the wire **once, when the wire is made**, and again only on request (the "Send to wired consoles" menu item, the "Send errors" button on the log card, or `POST /api/workspaces/{id}/tiles/{nodeId}/push`). A tile never uses an agent's turn by itself. Its text is marked as reference, not a task; page content is marked as data, not instructions. ≤ 20 000 characters.

- `file`: the card's path and its copy. `image`: the image file's path (the agent is on This PC, so hub assets can be read too). `document`: the version the card shows, or the latest finished version. `branches`: the project's branch list.
- `weblog` and `web`: ≤ 50 most recent problems from the page log (errors, `console.error`, failed requests), without URL query strings. A new wire gets all of them; a repeat request gets only the ones new since the last send. `web` also sends its address.
- `app`, `shape`, `line`, `ink` and `text` have no content that means anything as text, so they cannot be a wire source.

**Consoles without an agent.** Clients and the planner refuse a wire or forward to a console without an agent (plain shell, tool), and a forward from one, with a clear reason: that console has no turns and NextBoard only types to agents. A group is refused only when none of its members is an agent. This rule uses the console's current state, so `board/ops` still checks only the edge's shape (undo, snapshots and layout deploys must not fail because a console has changed). A line whose end has no agent shows dim and dashed with a caption, and turns normal again as soon as an agent runs there.

- Terminal deleted → the hub removes its id from the `members` of every group (a hub op, `clientId:""`). When a delete from the web client removes all of a group's members, the group is deleted too even if its frame was not selected (a marquee does not select frames). A group that still has members shrinks to fit the rest. Undo starts the console again, then restores the group, edges and other nodes with new ids; a console that fails to start is skipped. A group left empty for another reason (console deleted through the API or by the planner) stays.
- **Group frame:** a group node's x/y/w/h = the members' bounding box + padding 24 + header 32. A client that moves members sends a geometry patch for the group in the same ops request; dragging the group header moves all members (one request). Dragging a terminal into/out of the frame = a `members` patch. The hub recomputes the frame only when it adds members itself (spawn).
- Default color per role: general sky, frontend violet, backend teal, testers amber, reviewers rose, design violet, docs sage, security rose.
- The eraser never deletes a `terminal` or `orchestrator` (a client rule); `del` through the menu is still allowed with client confirmation.

## 2. Delivery engine (P6, D6)

```jsonc
// Delivery
{ "id": "dl_…", "sessionId": "s_…", "handle": "Ash",
  "text": "Run the tests and fix failures", "submit": true,
  "source": "order",            // "order"|"loop"|"memory"|"handoff"|"forward"|"reply"|"skill"|"dictation"|"api"
  "guard": "agent",             // "agent" (default) | "dictation"
  "orderId": "o_…", "forwardDepth": 0,
  "state": "waiting",           // see the state machine
  "waitReason": "working",      // "starting"|"needsYou"|"working"|"paused"|"reconnecting"|"humanTyping"|""
  "createdAt": "…", "deadline": "…", "sentAt": null, "confirmedAt": null,
  "error": "", "code": "" }
```

State machine:

```
queued ──► waiting(reason) ──► typing ──► sent ──► confirmed
   │            │                 │         └────► (stays sent if not confirmed within 8 s)
   └────────────┴─────────────────┴──► failed | expired | cancelled
```

| Session condition when checked | Action |
|---|---|
| `agentKind:"shell"`, `foreground:"shell"` (the agent has exited), `manualAgent` without permission | `failed` `not_agent` — **never** types into a shell |
| `exited`, `error`, session deleted | `failed` `session_gone` |
| `starting` | wait ≤ `startingTimeoutSec` (60) |
| `needsYou` | wait ≤ `needsYouTimeoutSec` (900); never types into a permission prompt |
| `working` | `whileWorking:"queue"` (default) wait ≤ `workingTimeoutSec` (1800); `"type"` → send now (CLIs that support an input queue) |
| `paused` | wait until resume or the deadline |
| `reconnecting` | wait ≤ `reconnectTimeoutSec` (300) |
| `ready` | type |

Typing procedure (one active delivery per session, FIFO):

1. Re-check right before typing: `foreground` with a process check (API-agents §6.5), `agentState`, `promptOnScreen` empty, no human input to this session within `humanTypingGuardSec` (2 s; wait ≤ 10 s), not `paused`.
2. Sanitize: drop C0 except `\t` and `\n`, drop ESC and DEL, `\r\n` → `\n`.
3. Bracketed paste mode on in the VT model (DECSET 2004) → write `ESC[200~` + text + `ESC[201~`. Off → `\n` is replaced by a space (one line).
4. `submit:true` → wait `submitDelayMs` (200; 100–1000), then write `\r`.
5. Confirmation: `agentState` becomes `working` within 8 s (`UserPromptSubmit` hook, T14, or the screen) → `confirmed`; otherwise it stays `sent` (UI: "Sent, not confirmed").
6. The agent exits while queued → `failed` `agent_exited`. Deadline passed → `expired` + a thread note when there is an `orderId`.

`guard:"dictation"`: `submit:false`; refused with `409 target_busy` when the agent is not in the foreground, `starting`, or `needsYou` (the client then puts the text into the command bar).

| Method | Path | Request | Response / error |
|---|---|---|---|
| POST | `/api/sessions/{id}/deliveries` | `{text, submit?: true, source?: "api", guard?, deadlineSec?}` | `202 Delivery`. `409 not_agent` (immediately when it is clearly a shell), `409 target_busy` (dictation), `413 text_too_long` (> 32 000 characters), `429 queue_full` (> 20 queued) |
| GET | `/api/sessions/{id}/deliveries` | — | `{"items":[Delivery…]}` (queued + the last 20 finished) |
| GET | `/api/deliveries?projectId=&active=1` | — | `{"items":[Delivery…]}` |
| DELETE | `/api/deliveries/{id}` | — | `{"ok":true}` → `cancelled`; `409 already_sent` when already `typing`/`sent` |

New settings (`settings.json`, PATCH as in API-agents §11):

```jsonc
"delivery": { "whileWorking": "queue", "startingTimeoutSec": 60, "needsYouTimeoutSec": 900, "workingTimeoutSec": 1800,
              "reconnectTimeoutSec": 300, "submitDelayMs": 200, "humanTypingGuardSec": 2 }
```

Session Info gets `queue` (the number of active deliveries).

## 3. Command pipeline (P7, D1–D9)

### 3.1 Endpoint

`POST /api/projects/{id}/commands`

```jsonc
{ "text": "@Ash run the tests", "source": "typed",      // "typed" | "voice"
  "workspaceId": "w_…", "cardId": null,                 // cardId = orchestrator node → card scope
  "clientId": "c_…", "selection": ["n_…"], "planner": "auto",    // "auto" | "off"
  "replyTo": "tm_…",                                    // optional: the thread message being answered (§3.9)
  "place": { "part": "Web" } }                          // optional: where consoles the command starts go when it names no place (§3.10)
```

Response `202`:

```jsonc
{ "commandId": "cmd_…", "threadId": "p_…",             // "p_…" project thread, "n_…" card thread
  "handledBy": "parser",                                // "parser" | "planner" | "pending" (planner running; result via event)
  "orderId": "o_…", "uiActions": [UiAction…],           // uiActions only for the sending client
  "confirmation": null, "messages": [ThreadMessage…] }
```

Error: `400 empty_text`, `413 text_too_long` (> 4000 characters), `404 workspace_not_found`, `429 thread_busy` (already 3 commands queued behind one planner turn), `400 bad_placement` (`place` names the root and a part). A `place` part the project does not have is left out, with a note in the thread.

```jsonc
// UiAction — run by the sending client; also broadcast in the "command" event with clientId
{ "action": "fit"|"zoomIn"|"zoomOut"|"zoomReset"|"focus"|"openPanel"|"closePanel"|"tool"|"undo"|"redo"|"goWorkspace"|"openFile",
  // "focus" with "workspaceId": the node is on another board; the client opens that board first, then focuses it
  "target"?: "n_…", "panel"?: "loops"|"memory"|"mission"|"settings"|"git"|"usage"|"documents"|"code"|"markdown"|"skills",
  "tool"?: "select"|"pen"|"eraser", "workspaceId"?: "w_…",
  // "openFile": an absolute path on the machine of "part" (the part's id; absent = no part holds it), opened by that
  // part's editor card, else by the Root card (§3.6)
  "path"?: "/srv/acme-api/main.go", "line"?: 12, "part"?: "pt_…" }
```

### 3.2 Local parser (D4)

Normalization: trim, single spaces, case-insensitive matching (the original text is used for the order's content), the number words `one`…`twelve` and `satu`…`sebelas`, `a`/`an`/`sebuah` = 1. `<who>` is resolved by §3.3. English and Indonesian (second table below).

| Area | Pattern (example) | Step | Confirmation |
|---|---|---|---|
| Direct | `@Ash <text>` | `send` verbatim (mention dropped) | — |
| All | `@everyone/@all/@everybody <text>` | treated as if there were no mention | — |
| Spawn | `spawn\|spin up\|start\|launch [<n>] [<agent>] [<role>] [in\|on\|at <part> \| at the root \| in all parts]`, `new console`, `open a shell\|terminal` | `spawn` (≤ 12), placed §3.10 | > 6 → `bulkSpawn` |
| Group | `make\|create a group of <n> [<agent>] [<role>] [in <part> \| at the root] [called <name>]`, `group <who…>`, `group these` (selection) | `group.create` | — |
| Add | `add <n> [<agent>\|<role>] to <group>` | `group.add` | — |
| Ungroup | `ungroup\|dissolve <group>` | `group.dissolve` | — |
| Pause | `pause [<who>]`, `wait`, `hold on` / `resume\|continue\|keep going [<who>]` | `pause` / `resume` | — |
| Restart | `restart\|reset <who>` | `restart` | always |
| Kill | `kill\|close\|stop <who>`, `delete <group>`, `kill all` | `kill` | always |
| Tell | `tell <who> to <text>`, `ask <who> to <text>`, `<handle>, <text>` | planner (rewrite); without a planner → `send` the `<text>` part verbatim | — |
| Same | `give everyone the same prompt: <text>`, `tell everyone: <text>` | `send` to all agents in scope | — |
| Handoff | `hand off\|pass [this task] to <agent> [from <handle>]` | `handoff` | — |
| Forward | `when <who> finish(es), pass\|send\|forward (it\|everything) to <who>` | `forward` | — |
| Cancel | `cancel that`, `never mind`, `scratch that` | `cancel` (the last order in the thread) | — |
| Ask | `status`, `how's it going`, `who finished`, `what happened`, `who needs me`, `give me a summary`, `help`, `what can you do` | `ask` | — |
| Camera | `show me everything`, `fit`, `zoom in\|out`, `reset zoom`, `go to <handle>`, `focus on <group>` | `ui` | — |
| Panel | `open loops\|memory\|mission map\|settings\|git\|usage\|documents\|code\|markdown\|skills`, `close that` | `ui` | — |
| Node | `add a note [that says <text>]`, `add a checklist\|kanban\|calendar\|uptime monitor`, `add an orchestrator [for <group>]`, `open localhost <port>`, `preview <port>` | `node.add` | — |
| Wire | `connect <who> to [the] orchestrator [<title>]` | `wire` | — |
| Draw | `pen`, `I want to draw`, `eraser` | `ui` tool | — |
| Undo | `undo`, `redo` (main bar only) | `ui` | — |
| Workspace | `go to\|switch to workspace <n\|name>`, `new workspace [called <name>]`, `rename this workspace to <name>`, `close workspace <n>` | `workspace.*` | close with running sessions: always |
| Loop | `every <n> minute(s)\|hour(s) <text>` (time at the start of the sentence, 1 min–24 h) | `loop.create` | always |
| Git | `commit and push`, `commit [with the message] <msg>`, `push`, `pull` | `git` | — (asked for by the user) |
| Memory | `push memory [to <who>]`, `scan memory` | `memory.push` / `memory.scan` | — |
| Layout | `deploy <layout name>` | `layout.deploy` | — |
| Small talk | `thanks`, `ok`, `nice`, `cool` | a short hub reply, not sent to agents | — |

Text that matches nothing and has no addressee → the planner when available; without a planner → §3.11.

**Indonesian** (`internal/orch/parse_id.go`). Each frame is turned into the English sentence above and then parsed with the same rules, so its steps and confirmations are exactly the same; names and text for agents stay as typed. `<who>` also knows `dan`, `semua`/`semuanya`/`semua agen` (= all), the prefixes `tim`/`grup`/`kelompok`; the roles `penguji`, `pengulas`, `desainer`, `dokumentasi`, `keamanan`, `umum`.

| Area | Pattern (example) |
|---|---|
| Spawn | `buat\|buatkan\|jalankan\|nyalakan\|tambah(kan) [<n>] [<agent>] [<role>] [di\|pada\|ke <part> \| di root \| di semua part\|bagian]` (a role alone is not enough: there must be a number, a CLI, or the word `agen`/`konsol`; the words from `di` on do not count for that), `terminal baru`, `buka shell`, `konsol baru` |
| Group / ungroup | `buat grup\|tim\|kelompok <n> [<agent>] [<role>] [bernama <name>]`, `tambah <n> [<agent>\|<role>] ke [grup\|tim] <group>`, `kelompokkan\|gabungkan <who…>`, `kelompokkan ini`, `bubarkan\|pisahkan <group>` |
| Pause | `jeda\|tahan\|hentikan sementara [<who>]`, `tunggu dulu`, `tahan dulu`, `stop dulu` / `lanjutkan\|teruskan <who>` |
| Restart / kill | `mulai ulang\|muat ulang <who>` / `matikan\|hentikan\|tutup <who>`, `matikan semua` |
| Tell / same | `suruh\|minta\|perintahkan <who> untuk\|agar\|supaya <text>`, `suruh <handle> <text>` / `kasih semua prompt yang sama: <text>`, `bilang ke semua: <text>` |
| Handoff / forward | `serahkan\|oper [tugas ini] ke <agent> [dari <handle>]` / `kalau\|setelah <who> selesai, teruskan\|kirim [hasilnya] ke <who>` |
| Cancel / ask | `batal`, `gak jadi` / `status`, `gimana progresnya`, `siapa yang selesai`, `siapa yang butuh aku`, `ringkasan`, `bantuan` |
| Camera / panel | `perbesar`, `perkecil`, `tampilkan semua`, `fokus ke <who>` / `buka memori\|pengaturan\|git\|kode\|skills\|loop\|peta misi…`, `tutup panel` |
| Draw / undo / layout | `saya mau gambar`, `penghapus` / `urungkan` / `terapkan layout <name>` |
| Node / wire / workspace | `tambah catatan [berisi <text>]`, `tambah checklist\|kanban\|kalender\|uptime`, `tambah orkestrator [untuk <group>]`, `buka localhost <port>` / `hubungkan <who> ke orkestrator [<title>]` / `pindah ke workspace <n>`, `buat workspace baru [bernama <name>]`, `ganti nama workspace jadi <name>`, `tutup workspace <n>` |
| Loop / git / memory | `setiap\|tiap <n> menit\|jam <text>` / `commit dan push`, `commit dengan pesan <msg>` / `kirim memori [ke <who>]`, `pindai memori` |
| Small talk | `makasih`, `terima kasih` (reply "Sama-sama."), `oke`, `sip`, `mantap` |

Deliberately not mapped: `lanjut`, `tunggu`, `lihat`, `hapus` on their own, and `jalankan`/`buat` + something that is not a console (`jalankan tes`, `jalankan lagi`, `jalankan backend`, `buat dokumentasi`, `buat fitur login`); those words are said to the lead as often as they are commands, so the planner reads them. Tested in `TestParserIndonesian` and `TestIndonesianCommands`.

**Unknown names** (both languages). When not one name in `<who>` belongs to an existing console or group, and none is similar, the sentence may not be about consoles ("stop the dev server", "gabungkan branch ini ke main"): the planner reads it. Without a planner the hub answers with what it does not know. One known name among unknown ones is still taken to be about consoles, and the hub names the ones it does not know. Tested in `TestNamesNobodyHasGoToThePlanner`.

### 3.3 @mention and name resolution (D2)

- **Scope:** the main command bar reaches **all of the project's boards (workspaces)**; the request's `workspaceId` only sets the board that is open (where `spawn`, `node.add` and loops land, and the group that wins when two have the same name). An orchestrator card still covers only its own board. "All"/`@everyone`, `pause`, `kill all`, `status`, `push memory` apply to all of the project's boards; other projects are reached only through an explicit handle. The kill confirmation prompt names the number of boards when there is more than one.
- A group stays on one board: members from different boards are refused (`consoles on different boards cannot share a group`), a card's wires go only to consoles on its board, and an `allAgents` loop covers only the agents on the board where the loop was made.
- Resolution order: handle (hub-wide; in card scope only the wired ones) → group name (the open board first, then the other boards in tab order) → role (`@reviewers` = the members of every group with that role) → agent (`@codex` = every console of that agent in scope).
- Exact match after normalization (lowercase, drop non-alphanumerics) → direct target. Not exact → candidates with a Damerau-Levenshtein distance ≤ 1 (names ≤ 4 letters) or ≤ 2 (longer), or the same Double Metaphone primary key, or the same Indonesian spelling (Nopal/Naufal, Raihan/Reyhan, Djihan/Jihan) → confirmation `didYouMean` (one candidate) or `sendToAll` (several).
- A mention without text → the hub replies "What should @Ash do?" with no order.
- One mention + text → verbatim. Several targets or a group/role → planner (splits the work); without a planner → §3.11.

| Method | Path | Response |
|---|---|---|
| GET | `/api/projects/{id}/mentions?workspaceId=&cardId=&q=` | `[{ "kind": "everyone"\|"console"\|"group"\|"role"\|"agent", "id", "label", "handle"?, "agentState"?, "board"? }]` (`board` = tab name when it is not the open board) (≤ 30; order: everyone, console, group) |

### 3.4 Step

```jsonc
{ "op": "send", "targets": ["s_…"], "text": "…", "mode": "verbatim"|"rewritten" }
{ "op": "spawn", "count": 3, "agent"?: "codex", "role"?: "reviewers", "groupId"?: "n_…", "newGroup"?: { "name", "role" }, "machineId"?,
  "part"?: "Web", "root"?: true }                                     // placement, §3.10; both = invalid step
{ "op": "group.create", "members": ["s_…"], "name"?, "role"? }      { "op": "group.add", "groupId", "count", "agent"?, "role"?, "part"?, "root"? }
{ "op": "group.dissolve", "groupId" }
{ "op": "pause"|"resume"|"restart"|"kill", "targets": ["s_…"] }
{ "op": "handoff", "source": "s_…", "agent": "codex", "machineId"?, "part"?, "root"? }   // absent: where the source works (§9)
{ "op": "forward", "sources": ["s_…"], "targets": ["s_…"], "mode": "digest"|"split" }
{ "op": "cancel", "orderId"? }      { "op": "ask", "query": "status"|"finished"|"needsYou"|"summary"|"help" }
{ "op": "ui", "uiAction": UiAction }
{ "op": "node.add", "kind": "note"|"checklist"|"kanban"|"calendar"|"uptime"|"orchestrator"|"web"|"editor"|…, "data"?: {…}, "wireTo"?: ["n_…"],
  "part"?, "root"? }                  // editor: the card of that placement (§3.6); web, file: that part's machine
{ "op": "wire", "orchestratorId": "n_…", "targets": ["n_…"] }
{ "op": "workspace.go"|"workspace.create"|"workspace.rename"|"workspace.close", "workspaceId"?, "name"? }
{ "op": "loop.create", "loop": LoopDef }      { "op": "git", "action": "commitPush"|"commit"|"push"|"pull", "message"? }
{ "op": "memory.push", "targets"?: ["s_…"] }  { "op": "memory.scan" }      { "op": "layout.deploy", "layoutId" }
{ "op": "reply", "text": "…" }                // an answer to the user, not sent to agents
```

### 3.5 Planner (D5, decision #6)

- Only on **This PC**, through the runner (API-studio §2): `claude -p --output-format json --json-schema '<PlanSchema>'`, with no tools (`--tools ""` when supported, otherwise `--disallowedTools` for every tool + `--max-turns 1`), cwd `<data>/runner/planner`, timeout 45 s, never yolo, using Claude's default profile on This PC.
- Available when `claude` is detected on This PC and the last login check succeeded; `settings.commands.planner: "auto"|"off"`.
- **Input** (JSON on stdin, without project file contents or transcripts): the order text (> 600 characters → the planner is skipped, unless Talk first is on: then up to the 4000 limit), the last 20 thread messages (≤ 600 per message, ≤ 6000 characters in total), `pendingPlan` when there is an open plan (§3.9), a snapshot: consoles (`handle`, agent, role, group, `board`, `agentState`, `activity` ≤ 80, running order ≤ 120), groups, orchestrators + wires, `boards` (every board in scope: name, `current`, ≤ 30 non-console tiles per board with `id`, `kind`, position, size, and content as described below), custom agent names, project/workspace names, loop names, the selection, the card scope, `memory` (project memory + notes + global, ≤ 4000 characters; background, not commands).
- **Output** (`PlanSchema`): `{ "reply": string ≤ 2000, "danger"?: string ≤ 300, "steps": PlannerStep[] ≤ 40 }`. `PlannerStep` = a subset of the ops `send, spawn, group.create, group.add, pause, resume, restart, kill, handoff, forward, loop.create, node.add, git, ask, memory.push, reply`; targets are written as handles/group names, and the hub translates them to ids (unknown name → step dropped + a note). `spawn`, `group.create`, `node.add` may carry `workspace` (the name of the target board). The answer to the user is in the top-level `reply`; the hub drops a `reply` step that repeats the same text, so the answer shows once.
- One planner turn per thread; a "Still thinking…" message after 3 s (Talk first: the client shows "`name` is thinking", without this message) (the same thread message is updated). Failure (timeout, invalid JSON, not logged in) → parser fallback + the note "Planner unavailable: <reason>"; two failures in a row → 60 s cooldown. A login failure (including `OAuth session expired`) gives `reason: "not_logged_in"` for 1 minute, because Claude's refresh token can recover by itself.

| Method | Path | Response |
|---|---|---|
| GET | `/api/planner` | `{ "available": true, "reason"?: "claude_missing"\|"not_logged_in"\|"disabled"\|"cooldown", "lastLatencyMs": 6200 }` |


### 3.6 Board control by the planner

The planner's input holds every tile: `id`, `kind`, position, size, and content (`data`, or `contentCut` when it exceeds 700 characters; `points`, `strokes`, `snapshot`, `markup` are not sent), plus each board's `edges`. In a project of several parts an editor card also says what it shows: `"part": "<name>"`, or `"root": true`. At most 40 steps per plan. A tile can be named by id, `@handle`, group name, title, or by kind when there is only one (`editor`); a name that matches more than one tile gets its step dropped. Several editor cards: `editor`/`code` is the Root card when there is one; `editor admin`, `admin editor`, `code editor at the root` name a card by its placement; a copy of a card means the first card of its placement.

| Planner op | Field | Behavior |
|---|---|---|
| `node.add` | `kind` (`note text checklist kanban calendar uptime orchestrator web shape branches editor memory file`), `x y w h`, `title text color`, `data`, `path`, `part`, `root` | an `editor` is placed like a console: `part` (or `data.part`, a name or id) gives that part's card, neither or `root` the Root card (every part). A board keeps one card per placement: asking again focuses it. The new card stores the part's id in `data.part` (none at the root). A `file` card's `path` resolves like `editor.open` and takes its part's machine; a `web` tile with `part` previews on that part's machine |
| `node.update` | `tile`, `x y w h`, `title text color`, `data` | `data` is merged per key, lists are replaced whole; `sessionId previewId assetId appId points strokes` cannot be changed; a console's content cannot be edited |
| `node.delete` | `tile`/`tiles` | always through a `destructive` confirmation; consoles need `kill` |
| `connect`, `disconnect` | `source`, `targets` | card (orchestrator, memory, editor) or content tile → console/group = wire, editor and memory also to an orchestrator, memory also to an editor, an orchestrator also to an editor (it directs the agents opened in it; one with none yet gets a note); console ↔ console = forward; console → web = preview; web → log card = log. Content tiles and memory tiles send their content once when connected. A console without an agent is refused with a note in the thread. Reversed order ("Bea to the editor") is flipped automatically; between two cards the order said is kept: *connect the orchestrator to the editor* is `orchestrator → editor`, *connect the editor to the orchestrator* is `editor → orchestrator`. A disconnect removes the link in the direction said, else the other one |
| `ui` | `action` (`fit zoomIn zoomOut zoomReset focus openPanel closePanel undo redo`), `tile`, `panel` | run by the client that sent the command |
| `task.add`, `task.update`, `task.delete` | `tile`, `card` (id or title), `title`, `text`, `column`, `assignee`, `priority`, `due`, `done` | cards in a kanban or checklist, §3.10; `send` with `tile` and `card` shares an existing card |
| `editor.open` | `path`, `line`, `part` | becomes the uiAction `openFile` with the absolute `path` and the `part` (id) it is in: that part's editor card opens it, else the Root card. The path: with `part`, relative to that part's folder; in a project of several parts, `<part>/…` (`admin/src/x.ts`, also `./admin/…`); an absolute path belongs to the one part whose folder holds it (`/admin/x` names part admin when no folder holds it); otherwise a relative path is in the first part (compat) and an absolute one in no part. An unknown `part` drops the step |
| `console.rename`, `group.dissolve`, `workspace.create`, `workspace.rename`, `workspace.go` | | as named |

`workspace.close` deliberately stays outside the planner's authority. Planner changes do not go into the client's undo; they are reverted through board snapshots. Other clients receive the result like ordinary board ops.
### 3.7 Confirmation (D8)

```jsonc
// Confirmation
{ "id": "cf_…", "threadId": "p_…", "commandId": "cmd_…",
  "kind": "destructive",         // "destructive"|"didYouMean"|"sendToAll"|"risky"|"loop"|"bulkSpawn"|"proposal"
  "plan"?: [{ "handle": "Ash", "text": "…" }],   // proposal: the plan in words
  "prompt": "Kill @Ash and @Bea? Their processes stop; the tiles stay on the board.",
  "options": [{ "id": "confirm", "label": "Kill 2 consoles" }, { "id": "cancel", "label": "Cancel" }],
  "candidates"?: [{ "sessionId", "handle" }],      // didYouMean / sendToAll
  "steps": [Step…], "expiresAt": "…", "state": "pending",   // "pending"|"confirmed"|"cancelled"|"expired"
  "decidedBy"?: "admin" | "d_…" }
```

| Step | Confirmation |
|---|---|
| `kill`, `restart` (console, group, `kill all`) | always `destructive` |
| `workspace.close` with running sessions | always |
| `loop.create` from an order | always `loop` (shows the schedule + an estimate of prompts/hour × targets) |
| `spawn` of > 6 consoles at once | `bulkSpawn` |
| Planner steps nobody asked for: `git`, `node.add web` to a URL, a `send` containing a risky pattern | `risky` (Talk first: shown as a `proposal`, §3.9) |
| A `send` the user typed (`@Arkan …`, `tell Arkan …`, `@everyone …`) containing a risky pattern | `risky`, as a plain question ("Your order for @Arkan says \"delete\"… Send it?"), not a lead proposal (owner decision, 2026-11-08) |
| `git` typed by the user, `pause`/`resume`, `handoff`, `forward`, `send` without a risky pattern, `memory.push` | no |

Risky patterns (RE2, case-insensitive; `settings.commands.riskyPatterns` adds more): `\brm\s+(-\S+\s+)*\S+`, `\bsudo\b`, `curl[^|\n]*\|\s*(ba)?sh`, `\.env\b`, `--dangerously`, `--yolo`, `\b(delete|deletes|deleting|hapus\w*|menghapus\w*|dihapus\w*|penghapusan)\b`, `\bdrop\s+(table|database|schema|collection)\b`, `\btruncate\b`, `\b(migrate:(fresh|reset)|db:(drop|reset|wipe))\b`, `\bgit\s+push\b[^\n]*\s(-f|--force)`, `\bgit\s+reset\s+--hard\b`, `\bgit\s+clean\s+-`, `\bgit\s+branch\s+-d\b`, `\bmkfs\b`, `\bdd\s+if=`. These patterns deliberately lean toward asking, because the lead's other orders run right away: even the sentence "jangan hapus file lain" gets asked about.

- `POST /api/confirmations/{id}` `{"decision":"confirm"|"cancel","choice"?: "s_…"}` → `200 Confirmation`; `409 already_decided`, `410 expired`. The first decision wins (any client, including Android).
- TTL `settings.commands.confirmTtlSec` (default 60, 30–300). While a confirmation is pending in that thread, the text `yes|confirm|do it|go ahead` = confirm, `no|cancel` = cancel; any other text cancels the pending one and is then processed as a new command.

### 3.8 Execution

Steps run in order; a failed step is recorded and the next independent steps still run. A `send` step makes an **order** (§4). `ui` steps are returned as `uiActions`. Planner results that arrive later are sent through the `command` event (`state:"done"`, `uiActions`, `clientId`); other clients ignore `uiActions`.

### 3.9 Lead agent

The orchestrator is the **lead agent**: the owner's assistant and the leader of the other agents, with a name and a personality. It is still the same planner (§3.5); what changes is how it talks and when work is released.

```jsonc
// settings.lead (PATCH /api/settings merges per field)
{ "name": "Aruna",         // 1–24 characters, one line; default "Aruna" since 2026-11-08 (previously "Nara"; a saved "Nara" moves to the new name)
  "personality": "",       // ≤ 800 characters; written by the owner, the hub's rules after it still apply
  "talkFirst": true,
  "runner": "",            // what the lead thinks with (2026-11-11): "" = like hidden runs (Claude Code, through runner.provider if set),
                           // "claude" | "codex" = that CLI on its login, "ap_…" = an API provider on the CLI that speaks its API
  "model": "", "effort": "",  // model and effort of lead runs on that runner; "" = the runner's default
  "providers": { "claude": "ap_…" } }  // consoles the lead spawns, per agent id; absent = that agent's login
```

- **Lead runner** (Settings › Lead agent › What it runs on). Applies to planner turns, `mode:"split"` forwards and thread summaries. An Anthropic-compatible provider → Claude Code, OpenAI with the Responses API → Codex, Chat Completions only → not possible (the runner has no OpenCode). The lead's CLI must be installed on This PC: without it `GET /api/planner` gives `reason: "claude_missing" | "codex_missing"`, and `runner` names its label ("Claude Code", "Codex", "API · <name>"). A failed login is waited out for one minute only for the runner that failed; picking another runner is tried right away. A missing provider: the reason is written in the thread and the parser answers. Deleting a provider clears the `lead.runner` and `lead.providers` that point to it.
- **Strict schema for Codex.** Codex 0.165 sends `--output-schema` to the API with `"strict": true` (measured against a fake API on loopback), and strict mode refuses optional properties, open objects, `maxLength`/`minLength`. The runner rewrites the schema for Codex (`jobs.StrictSchema`: every property `required`, optional ones become nullable, free objects such as `data` become a JSON string) and then turns the answer back into its original shape (`jobs.FromStrict`: optional nulls dropped, JSON strings decoded). Applies to every Codex run that uses a schema.
- **Lead model.** `lead.model`/`lead.effort` go with every lead run (`--model`/`--effort` for Claude, `-m`/`model_reasoning_effort` for Codex). On a provider, a model in the provider's list is used as is; the aliases `haiku`/`sonnet`/`opus` are mapped to the provider's models; other names (belonging to the login) are replaced by the provider's default model. Changing the runner in the UI clears both.
- **Testing with a real model** (`TestLeadEval` in `internal/server/lead_eval_test.go`, only with `NEXTBOARD_LEAD_EVAL=1` and `NB_EVAL_*`): a real test hub, a real provider, a temporary HOME, fake agent consoles, so only the planner is real. Eight cases: `critical` (an order that breaks critical notes), `danger` (a danger caught by neither the patterns nor the notes: a mass email to 40,000 customers), `split-work`, `task-add`, `task-move`, `spawn-api`, `question`, `split-result`. Result on 2026-11-11 with an OpenAI-compatible router (model `gpt-6.1-sol`), lead on Claude Code and on Codex: 16 of 16. Observed: both danger cases sent nothing; the model refused or asked back while citing the rule, without filling `danger`. So `danger` itself has still never been seen filled by a real model. The Codex lead refused to spawn Fake Claude on an OpenAI provider and offered choices that fit.
- **Spawns by the lead on an API.** The planner's input holds `providers: [{id, name, agents}]` (without key or address). A `spawn`/`group.add` step may carry `account`: a provider name or id, or `login`. Without `account`, `lead.providers[agent]` applies; the same goes for typed commands such as `spawn 1 claude`. Spawns from the panel, layouts and handoff are not affected. An unknown provider stops the step with a note; a provider that does not match the agent is refused by session creation (`provider_mismatch`). A provider console uses the provider's model unless the slot picks a model.

- **Identity.** The planner prompt opens with the name and personality. A card has its own lead when its title is not `Orchestrator[ n]`; the card's `data.personality` overrides the global one (cut to 800 characters).
- **Talk first.** The planner may reply without steps and ask back when something is unclear. The steps it returns run right away, without a Go ahead (owner decision, 2026-11-04).
- **What is held.** The hub holds a plan when one of these hits: `policy` finds `risky` (§3.7), or the planner fills `danger` (its own judgment for a danger that cannot be read from the patterns, e.g. "Clean up the staging environment"). The reply shows, every step becomes a `proposal` confirmation with `plan`, the reason in `prompt`, and a TTL of **30 minutes**. The patterns run in the hub, so they still hold a plan even when the model does not flag it.
- **Talking a plan over.** While a proposal is open, words of agreement (`ya, gas, lanjut, oke, setuju, go ahead…`) = confirm without a planner turn; words of refusal (`jangan dulu, batal, not now…`) = cancel, with the note "Nothing was sent to the team."; any other message goes to the planner with `pendingPlan`, and its new plan replaces the old one (a safe new plan runs right away and closes the old one).
- **What still goes straight through:** orders that name their target (`@Arkan …`, `tell Arkan …`, `@everyone …`) and parser commands (`pause everyone`, `status`, …). Except an order containing a risky pattern: that waits for one yes (§3.7). `@Aruna …` (that lead's name), `@orchestrator`, `@lead` go to the lead, unless there is a console with that name.
- **One question per danger.** The card names which order and which part of the text hit a pattern, so agreeing once is enough. Steps that have their own confirmation (`kill`, `restart`, `node.delete`, `workspace.close`, `loop.create`, `spawn` > 6) still use their own card, not `proposal`. The planner is asked not to copy critical notes into orders (agents already got them at start), because the copy can hit the `\.env\b` pattern.
- **Lead replies are written in Markdown** (Claude uses it); clients render it without raw HTML, images become placeholders, relative links are not opened. Copy takes the original text.
- `talkFirst: false` = plans run right away as before; the planner is only called for what the parser does not understand, with the 600-character limit.

**Replying to a message.** `replyTo` in §3.1: the hub looks for that message in the thread's last 200 messages (not found = ignored). The owner's message is stored in its own words plus `replyTo { id, handle?, excerpt }`; what is passed to the agent or planner gets the addition `[This answers what @Arkan said: "…"]`. Plan lines in the thread do not show that addition. Clients: a reply to an agent's message is sent as `@Arkan text`, so it goes straight to that agent; "Ask lead" has no prefix, so it goes to the lead. The Reply field in a report's "See output" window also sends an order like this, so the next result comes back to the thread. Risky patterns (§3.7) are checked only on the owner's words, without the quote: the quote is the agent's own words, and a "Next I can delete …" in it used to hold every reply.

### 3.10 Lead task boards

A wire `orchestrator → kanban` or `→ checklist` (drag the knob, **Connect consoles…**, or **New task board** in the card menu; clients and the planner flip the reverse direction) makes the lead keep that board. The board's content is not sent to anyone because of this wire, and the hub does all of it without an agent turn or a planner run.

**Following orders.** An order (`source: "order"`) to a console that card directs, from any thread, becomes one card per console: the title is the first line without the `@handle`, the rest is notes, `assignee` is its handle. Forwards and handoffs are not recorded. The hub writes `orderId`, `sessionId`, `state`, and on failure `error`, through an ordinary `patch` op.

| Order target | `state` | Column |
|---|---|---|
| `queued` | `queued` | To do |
| `delivered`, `working` | `working` | In progress |
| `needsYou` | `needsYou` | In progress |
| `finished` | `finished` | Review if there is one, otherwise Done with `done: true` |
| `failed`, `timeout`, `cancelled`, `skipped` | `failed` | stays where it is, `error` holds the reason |

Columns are found by their name (English and Indonesian), then by their position: first, the one after it, last. A card is only moved when `state` changes, so one the user moved stays there. In a checklist, `finished` checks the item.

**Handing out by itself.** `autoRun: true` in the tile's data (a switch on the orchestrator card or in the tile menu, off by default) makes the hub check every 5 seconds: cards in To do that have never run, or that the user moved back to To do after they finished or failed, are sent to free team consoles (`ready`, no active order), one per console. The content is the title plus notes, `source: "task"`, and a "Sent to" message shows in the card's thread. `assignee` limits it to that console. Cards in Backlog are not included. There is no confirmation because the owner turned it on, and each card uses one agent turn.

**Planner.** Its input holds `tasks`: every kanban and checklist on the boards in its scope, with `linked`, `autoRun`, and their cards (≤ 80). It has `task.add`, `task.update`, `task.delete`, and `send` with `tile` plus `card` to share an existing card. Handing out work still goes through a lead proposal (§3.9); changing a board does not.

### 3.11 Fallback without a planner (D9)

- An order with `@` or an addressee at the start → the user's words as they are. Several consoles named → **one** free agent (state `ready`, no active delivery/order) receives it; the thread records "Sent to @Ash; 2 left free".
- The same text to everyone only for an explicit request (`same prompt`, `everyone the same`, `independent reviews`) or a short directive (≤ 40 characters, one line).
- No addressee: exactly one agent in scope → to that agent; otherwise not sent, with the note "Name an agent with @ or enable the planner".
- Requests that need instructions to be written ("write a brief for…") are not sent without a planner.

## 4. Threads and orders (D3, D15)

```jsonc
// ThreadMessage
{ "id": "tm_…", "threadId": "p_…", "at": "…",
  "role": "user"|"hub"|"agent",
  "kind": "order"|"plan"|"report"|"reply"|"note"|"confirm"|"error",
  "text": "…", "source"?: "typed"|"voice"|"loop"|"api",
  "orderId"?: "o_…", "sessionId"?: "s_…", "handle"?: "Ash",
  "plan"?: [{ "handle": "Ash", "text": "…" }],                       // plan note: what was sent to whom
  "report"?: { "state": "finished"|"failed"|"timeout"|"skipped", "durationMs": 81234, "excerpt": "≤ 400 characters", "chars": 5120 },
  "confirmationId"?: "cf_…",
  "replyTo"?: { "id": "tm_…", "handle"?: "Ash", "excerpt": "…" } }   // the message being answered (§3.9)
// Order
{ "id": "o_…", "threadId": "p_…", "commandId": "cmd_…", "text": "…", "createdAt": "…",
  "targets": [{ "sessionId", "handle", "deliveryId",
    "state": "queued"|"delivered"|"working"|"needsYou"|"finished"|"failed"|"timeout"|"cancelled"|"skipped",
    "startedAt", "finishedAt", "error"? }] }
```

- Target `finished` = a `working → ready` transition after the delivery is `confirmed` (or `sent` and then `working` observed). `failed` = the delivery failed, `agent-exit`, or `error`. `timeout` = `settings.commands.reportTimeoutMin` (30) without finishing → the note "No reply from @Ash after 30 min".
- On `finished`, the hub reads the reply (API-agents §8) and adds a `report` message; the message keeps ≤ 8000 characters of text. The full reply (≤ 200 KB) is stored for that message itself in `threads/<tid>.outputs/<id>.json`, so "See output" always opens that bubble's reply, even when the agent has moved on to another task, the hub restarted, or the console was deleted. The reply goes away when its message leaves the thread or the thread is cleared. Reports from before this feature show their message text with `partial:true` when it is shorter than `chars`.
- A reply that has not arrived when the turn ends is not replaced by the previous turn's reply: the report is published without text, then filled in (`Threads.Update`, same id) as soon as the reply is there, within 60 s.
- `ask`: `status` = count per state + the needsYou list; `who finished`/`what happened` = the targets of the last order; `summary` = a summary of the reports through the runner (≤ 30 s) when the planner is available, otherwise the excerpts combined.
- `cancel that` = `POST /api/orders/{id}/cancel`: deliveries not typed yet are cancelled; text already pasted stays in the agent's input.
- **After a hub restart** every target that is not final becomes `failed`, "interrupted by hub restart". Targets whose text was not typed yet (`queued`) get `undelivered: true`, and the thread note carries `resend: true` (UI: a **Send again** button). The hub never resends by itself. `resend` sends to those targets only, in the same order, so task cards and forwards keep following it. A console that cannot receive yet (still resuming) is still offered; a console that is gone is not. After `kill -9`, the status can lag behind what was last saved, so a person decides.

| Method | Path | Response |
|---|---|---|
| GET | `/api/threads/{tid}/messages?before=&limit=50` | `{"items":[ThreadMessage…],"hasMore":bool,"confirmations":[Confirmation…],"planning":"cmd_…"\|""}` (`limit` ≤ 200). `confirmations` and `planning` are for clients that were not connected when their event was published (reload, hub restart) |
| DELETE | `/api/threads/{tid}` | `{"ok":true}` (clears it; active orders are still tracked) |
| GET | `/api/threads/{tid}/messages/{id}/output` | — | `{"messageId","threadId","projectId"?,"orderId"?,"sessionId"?,"handle"?,"source"?,"text","at","chars","truncated","partial"?}`; `404 no_output` when the message no longer exists or is not a finished report with a reply |
| POST | `/api/threads/{tid}/messages/{id}/output/save` | `{"dir"?: "outputs"}` | `Stat`, writes that reply to `<project>/<dir>/<YYYY-MM-DD-HHMM>-<handle>.md` |
| GET | `/api/threads/{tid}/orders?active=1` | `{"items":[Order…]}` |
| GET | `/api/orders/{id}` | `Order` |
| POST | `/api/orders/{id}/cancel` | `Order` |
| POST | `/api/orders/{id}/resend` | `Order`, resend after a hub restart. `404 order_not_found`, `409 nothing_to_resend` |

Project thread = `p_…`, card thread = the orchestrator node's id `n_…`. Command bar input history (↑/↓, 50) is kept per client.

### 3.10 Lead and project parts (ROADMAP #49)

A console works at the **root** of the project (it reaches every part; API-board.md §1) or in **one part**. The lead itself is always at the root.

- The planner's input for a project with more than one part holds `parts` (`name`, `machine`: "This PC" or "a server", `path`, `readOnly`), and each console names its `part`, or says `"root": true`.
- The step schema has `part` and `root`. `spawn`/`group.add` with `part` opens a console on that part's machine and folder (name or id, case-insensitive); with `root: true` at the root (This PC, the hub's root folder). Both → the step is dropped ("root or part, not both"). A part that does not exist → nothing is opened, the note "the project has no such part" (API: `404 part_not_found`).
- **A step or typed order that names no placement** goes to the command's `place` (§3.1: the part of the tile the owner has in focus, else the last one used), else to the **root**. `group.add` and a spawn into an existing group first take the group's placement, when its members agree. A single-part project has no root of its own: the root is its part, so nothing changes there. HTTP clients that send no placement keep the compat rule of §5.
- Prompt rules: work about one part is sent to a console in that part; work across parts goes to a root console, or part by part (the first part reports, then its result is forwarded or sent to the next part); a spawn without `part` or `root` starts where the owner is working, else at the root; read-only parts are only for questions and investigation.
- Typed commands: `spawn 2 claude in Backend` (also `on`, `at`, `di`, `pada`, `ke`, and the word `part`/`bagian` after it) when Backend is the name of a project part; `spawn 2 claude at the root`, `in all parts`, `buat 2 claude di root`, `di semua part`, `di semua bagian` place them at the root. A part actually named like one of those words (`root`) wins. `make a group of 3 codex in Backend called Ops` places the group too.
- **Tasks.** `task.add`/`task.update` store `part` (its name) and `partId` on the card, so a renamed part keeps its cards. Automatic handing out gives a card with a part to a free console in that part, else to a free console at the root; a card whose part is gone goes to nobody.
- `editor.open` and editor cards follow the part too (§3.6). A `web` tile from a typed `open localhost <port>` previews on the machine of the command's `place` part.
- `POST /api/workspaces/{id}/spawn` accepts `part` and `root` in the request and in each slot (§5). Sessions the orchestrator makes go through the same placement as `POST /api/sessions` (API-board.md §1), so the hub checks the part's machine and makes the root folder.
- Mission Map shows each console's part (or Root) and an "All parts" filter; kanban cards show a part chip.

## 5. Groups and the team composer (D14, C2)

`POST /api/workspaces/{id}/spawn`:

```jsonc
{ "slots": [{ "agent"?: "claude", "machineId"?: "m_…", "profileId"?, "yolo"?,
              "part"?: "Web", "root"?: true }],                   // 1–12; or "count" + "agent"
  "part"?: "Web", "root"?: true,                                  // for every slot without its own
  "group"?: { "name"?: "Backend", "role"?: "backend", "color"?: "teal" } | { "id": "n_grp" },
  "orchestratorId"?: "n_orch", "at"?: { "x": 0, "y": 0 } }
```

**Placement per slot** (v0.9.0-beta.2): the slot's `part`/`root` → the request's → for `group.id`, the placement its members share (none when they differ) → for the lead and typed orders, the command's `place`, else the root (§3.10) → the compat rule: the slot's `machineId`, else the project's first part's machine, in the folder of the first part on that machine (a machine with no part: its home folder). A slot with its own `machineId` or `cwd` and no placement keeps them and skips the group and command defaults. `part` is a name or id, any case; a single-part project's root is its part. Errors: `400 bad_placement` (root and a part), `404 part_not_found`, and from the hub's placement `400 part_machine`, `500 root_unavailable`. Nothing starts when one slot is refused.

→ `201 {"sessions":[Session…],"groupNodeId":"n_…"}` in **one** `boardOps` broadcast (terminals, group, wires). Agent per slot: `slot.agent` → `settings.agents.roleAgents[role]` → the machine's default. Model and effort per slot: `slot.model`, `slot.effort` → `settings.agents.roleModels[role]` when its agent is that role's agent → `settings.agents.models[agent]` (API-agents.md §25). The word `shell`/`terminal` in an order → `shell`. Default group name `Group N`. Error: `400 too_many` (> 12 per call, > 64 sessions per workspace), `404 group_not_found`, `409 name_conflict`.

Groups are created, renamed and dissolved through board ops (or parser steps that produce the same ops in the hub). Pause/resume/restart/kill of a group = a step with all members as targets.

**Linked input:** `data.syncInput: true` on the group node (changed through a `patch` board op, off by default). The hub then copies one member's keyboard input (terminal WS `{"t":"i"}` and `POST /api/sessions/{id}/input`) to the other members that are running and not paused, so it works from any client with no protocol change.
- Only typing is copied. The terminal emulator's automatic replies (focus, mouse, cursor position, device attributes, OSC/DCS answers) are not copied, because they only mean something to the screen they came from (`session.TypedInput`). Consequence: Ctrl/Shift+F3 is not copied, because it has the same form as a cursor position report.
- Each member has its own ordered queue (≤ 1 MiB; anything beyond that is dropped + logged), so a console that stops reading does not hold up the console being typed into.
- Orders from the command bar do not take this path; the delivery engine types into each console separately.
- `resize` is not copied.

## 6. Orchestrator card (D10)

- Directs = the wired terminals + the members of wired groups.
- Commands with `cardId`: targets are limited; mentioning a console outside it → the message "@Bo is not wired to this card"; `undo`/`redo`/`workspace.*` are refused ("Use the main command bar"); push-to-talk always goes to the main bar.
- Empty card: "No consoles wired" + Spawn agent (`spawn` with `orchestratorId`).
- Row actions: Focus, Pause/Resume/Restart, Kill (confirmation), Disconnect (`delEdge`).
- Automatic titles for cards made from commands: `Orchestrator`, `Orchestrator 2`, …
- Card name = its lead's name: the default title uses `settings.lead.name`, any other title becomes the name of that card's lead (§3.9). "Lead profile…" in the card menu changes the title and `data.personality`.

## 7. Mission Map (D16)

`GET /api/workspaces/{id}/mission` (`?scope=project` = all of the project's workspaces):

```jsonc
{ "workspaceId": "w_…",
  "directors": [{ "id": "bar"|"n_orch", "kind": "bar"|"orchestrator", "title": "Command bar", "lastOrder"?: { "text", "at" }, "targets": ["n_grp","n_term"] }],
  "groups": [{ "nodeId", "name", "role", "color", "members": ["s_…"], "lastTask"?: "…" }],
  "consoles": [{ "sessionId", "nodeId", "handle", "agent", "role"?, "groupNodeId"?, "agentState", "activity", "stateSince",
                 "paused": false, "subagents": 0, "currentOrder"?: { "orderId", "text" }, "directedBy": ["bar","n_orch"] }],
  "counts": { "working": 2, "needsYou": 1, "idle": 3, "done": 1, "asleep": 2 } }
```

Filters (client): All · Working · Needs you (+ error) · Idle (`ready`, `shell`, paused) · Done (`exited`). Consoles/groups no card directs → `directedBy:["bar"]` (a dashed "command bar only" line). `subagents` = the number of `PreToolUse` events for the `Task` tool without a matching `PostToolUse` (hooks only). Double-click → focuses the node on the board.

## 8. Pause / resume (D17)

| Method | Path | Response / error |
|---|---|---|
| POST | `/api/sessions/{id}/pause` | `Session` (`paused:true`, `pausedAt`); idempotent. `409 not_agent` when the agent is not in the foreground; `503 machine_offline` |
| POST | `/api/sessions/{id}/resume` | `Session`; also sends SIGCONT when the foreground process has status `T` even if the hub did not pause it (a program that stopped itself) |
| POST | `/api/workspaces/{id}/pause-all`, `/resume-all` | `{"sessions":[id…]}` (agents only) |

| Machine | Mechanism |
|---|---|
| Local | `TIOCGPGRP` on the PTY master → `kill(-pgid, SIGSTOP\|SIGCONT)`; refused when pgid ≤ 1 or equal to the hub's pgid |
| SSH (direct & tmux) | read-only exec + signal: `p=<wrapperPid>; t=$(ps -o tpgid= -p "$p"\|tr -d ' '); a=$(ps -o sid= -p "$p"\|tr -d ' '); b=$(ps -o sid= -p "$t"\|tr -d ' '); [ "$t" -gt 1 ] && [ "$a" = "$b" ] && kill -STOP -- "-$t"` |

- `wrapperPid` comes from the `agent-start` marker (API-agents §3.2). Not known yet (old sessions, tmux re-attach without a marker) → `tmux display -p -t nb-<id> '#{pane_pid}'`; direct without a pid → `409 pid_unknown`.
- After SIGCONT: a small resize (cols−1, 100 ms, cols) so the TUI redraws.
- While `paused`: `agentState` is frozen (UI pill PAUSED), deliveries wait, loops skip that session. `paused` is stored in `sessions.json`; a local session that died because of a hub restart → `paused:false`.

## 9. Handoff (D18)

`POST /api/sessions/{id}/handoff` `{ "agent": "codex", "part"?, "root"?, "machineId"?, "profileId"?, "workspaceId"?, "note"?, "threadId"? }` → `202 Handoff`. `409 handoff_running` (one per source session), `400 not_agent`, `400 bad_placement`, `404 part_not_found`.

**Where the next console works:** `part`/`root` when given; otherwise, unless `machineId` is given, **where the source works**: at the root for a root console, in its part (by id, so a second part on the first part's machine stays itself), and for a console from before parts that works on a server, that machine and folder. `inPlace` ignores all three.

```jsonc
{ "id": "ho_…", "sourceSessionId": "s_…", "targetSessionId": null, "agent": "codex",
  "state": "summarizing",    // collecting → summarizing → spawning → waitingReady → delivering → done | failed | manual
  "jobId": "jb_…", "brief"?: "…", "error"?: "", "createdAt": "…" }
```

1. **collecting:** the source's transcript (the last turns, up to 60 000 characters) or, without a transcript, the last 400 screen lines.
2. **summarizing:** a `handoff-summary` runner job (≤ 4 minutes) where the source works (its part's machine and folder; This PC for a root console; the first part for a console in no part) when that machine has a logged-in Claude/Codex, otherwise on This PC (the content is sent via stdin). Brief: the goal, what is done, the current state, files touched, next steps, open questions (≤ 1500 words). Failure → brief = the last 12 000 characters as they are + a note.
3. **spawning:** a new console of the target agent (any catalog/custom agent with `kind:"agent"`; another machine is allowed — a NextBoard extension), next to the source, in one group with the source.
4. **waitingReady:** ≤ 120 s; if `needsYou` (e.g. trusting the folder), wait for the user ≤ 120 s more; out of time → `manual`, the brief is shown in the thread with Copy.
5. **delivering:** a `source:"handoff"` delivery: brief + "Continue this task from exactly this state. Do not redo finished work."

The source keeps running. No confirmation.

**On a hub restart.** Handoffs are stored in `handoffs.json` and resumed, with one note in the thread. `collecting` and `summarizing` redo the summary once the source console is back (waited for at most 30 seconds), because the runner job died with the hub. `spawning` and `switching` are tried again: one that has already switched agents is not switched again, and between the start of a spawn and its being recorded one extra console can appear, which is visible and types nothing. `waitingReady` waits again from the start. `delivering` is **not** typed again, because deliveries live only in memory, so nobody knows whether the summary was typed or not: the state becomes `manual` and the summary goes into the thread (same as orders, decision #34).

### 9.1 Switching agents in the same tile

`POST /api/sessions/{id}/handoff` with `"inPlace": true` uses the same console, not a new one: the tile, handle and folder stay. Extra fields: `"noBrief"?: true` skips the summary, `"fresh"?: true` starts a new conversation even if that agent has run here before. The `Handoff` response carries `inPlace` and `returning`. `machineId` and `workspaceId` are ignored. `400` when the agent is not installed on that machine, before the old agent is stopped.

State: `collecting → summarizing → switching → waitingReady → delivering → done | failed | manual`.

1. An agent that is working is paused (SIGSTOP) while the summary is written, so its content does not go stale, then woken right before it is stopped. If the summary or the restart fails, it is resumed.
2. An agent that has a transcript but no conversation yet is not summarized: there is nothing to pass on and no run is used.
3. The restart uses the ordinary `launchContext`, so project memory, critical notes and MCP servers come along automatically.
4. `waitingReady` ignores state until the new process's `startedAt` is later than the moment of the switch: a `ready` state that belongs to the old process must not be used to type the summary. If the new agent falls to a shell for more than 8 seconds, the state becomes `manual` and the summary goes into the thread.

**Back to the previous agent.** On a switch, `pastRuns` in the session (`[{agent, agentSessionId?, profileId?, at}]`, newest first, at most 8) records the conversation left behind, only when that conversation really exists. Switching back to that agent resumes the same conversation (`--resume`) with the same login, and the summary sent explains what happened while it was away. An agent that can only `continue` is not recorded as exactly resumable.

## 10. Forward (D19)

- **`forward` edge** (persistent): `trigger:"turn"` → every time a source's turn ends; `"order"` → only turns started by an order. When the source is a group → waits for all members that received the same order.
- **Once, from a command** (`when X finish, pass it to Y`): a `Forward` rule attaches to the source's active order; if the source has no order yet, it waits ≤ 4 h; once the source is working, ≤ 30 minutes from the order, then it sends whatever is there.
- Digest (text to the target):

  ```
  Results forwarded by NextBoard from @Ash (Claude Code) and @Bo (Codex):

  ## @Ash
  <reply>

  ## @Bo
  <reply>

  Use these results for your next step.
  ```

- `mode:"split"` → the planner reads the results and gives each target its own part: one order per target, `forwardDepth` is still counted, and a target with no work receives nothing. Each split uses one planner turn on the owner's plan, and its failures count toward the §3.5 cooldown.
  - The planner's answer is not trusted: only that forward's targets receive text, and other names are recorded in the thread and dropped.
  - A part that matches a risky pattern holds the whole split until the user confirms (§3.7). Once confirmed, each part becomes an ordinary order with `forwardDepth` 0.
  - Planner unavailable, failed, or handed out nothing → the whole digest goes to the first target, the rest stay free, and the thread names the reason.
  - A line deleted while the planner is thinking sends nothing. A hub restart while the planner is thinking: that result is not sent, and the thread records it.
- Limits: ≤ 20 000 characters per reply, ≤ 32 000 per digest, the limit of one delivery (cut with a marker); a reply can be forwarded ≤ 30 minutes after it finished; `forwardDepth` ≤ 3; each forward edge fires ≤ 1× per 60 s.
- Results that finish within that minute are **held, not dropped**: ≤ 12 results per edge, sent together as one digest when the minute is over, and they survive a hub restart. The thread records how long they waited. The edge is checked again on send: a line that has been deleted sends nothing.
- Turns started by context (memory push, tile content) are not work results: they are not forwarded and do not use up the edge's minute.

```jsonc
// Forward (one-time)
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
           | { "kind": "cron", "expr": "*/15 9-18 * * 1-5" }              // 5 fields, hub time zone
           | { "kind": "agentIdle" }                                       // a target finished its turn
           | { "kind": "output", "pattern": "FAIL", "regex": false },      // smart-case, ≥ 2 characters
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

- The scheduler is in the hub; it keeps running without clients and for workspaces in the background. Hub restart → `nextRunAt` is recomputed from now; missed runs are not repeated.
- One run: resolve targets (agents only) → a delivery `source:"loop"` with `deadlineSec` 60 (no long queueing). A `needsYou` target is skipped (`needsYou`) + attention `loopHeld`. A run that reached no console → `retry` once after 60 s.
- `agentIdle` and `output`: ≤ 1× per 120 s per console; `output` sends as soon as the console is free; `agentIdle` lets that turn pass when the console is already busy again.
- The goal is checked before each scheduled run and when a target finishes a turn; not twice within 90 s; ≤ 2 concurrent checks per hub; an inconclusive result (CLI not logged in, timeout) → the run still goes ahead. `agentJudged` = a runner job with the goal text + the target's last reply (≤ 8000 characters), schema `{"met":bool,"reason":string}`. `command` = exec `$SHELL -lc` in the project path (project machine), exit 0 = met; only the user can make it, explicitly (the planner may not).
- Finished/stopped → attention `loopEnded`. The attention of API-agents §12 gains the `kind`s `loopHeld` and `loopEnded`.
- Built-in templates (`GET /api/loops/templates`, names owned by NextBoard): Status ping, Nudge idle agents, Build fixer, Test guard, Checkpoint commit, Review round, Daily summary.

| Method | Path | Request | Response / error |
|---|---|---|---|
| GET | `/api/projects/{id}/loops` | — | `LoopDef[]` |
| POST | `/api/projects/{id}/loops` | `LoopDef` without `id`/`state` | `201 LoopDef` (status `running` when `enabled`). `400 bad_loop` (interval < 60 s or > 24 h, invalid cron, pattern < 2 characters, `maxRuns` outside 1–500, `maxDurationSec` outside 900–86400), `429 too_many_loops` (> 50 per hub) |
| PATCH | `/api/loops/{id}` | changed fields | `LoopDef` |
| DELETE | `/api/loops/{id}` | — | `{"ok":true}` |
| POST | `/api/loops/{id}/start`, `/stop`, `/run-now` | — | `LoopDef` |
| POST | `/api/loops/stop-all` | — | `{"stopped":N}` |
| GET | `/api/loops/{id}/history?limit=50` | — | `{"items":[LoopHistoryEntry…]}` |

## 12. Project memory (D21)

- **1 project = 1 memory.** Hub folder `memory/<projectId>/`, with `MEMORY.md` as the main memory: the Memory panel, the Memory page, push, scan, the planner and every CLI use the same file, so switching agents, closing the orchestrator or moving to another CLI does not change what the project knows. Other `.md` files in that folder are agent notes. `memory/global.md` applies to all projects. Nothing is written to any machine.
- **Sub-memories:** each main memory (a project's or the all-projects one) has pages per topic, for example design system or do and don't, in `memory/subs/<projectId|global>/<slug>.md`, with `title` and `description` in the front matter. `description` is how an agent chooses what to read. At most 64 per main memory, 64 KB per page. The files are outside Claude Code's auto memory folder, so every CLI reaches them the same way.
- **Critical notes:** the `critical` sub-memory always exists in each main memory and cannot be deleted (`409 critical_required`); if its file goes missing, it is made again. Its content is the rules agents must not break. A new project starts with basic rules (drop database, reset data through tests or seeders, delete data, git history, production, secrets); the one for all projects starts empty. At most 16 KB.
  - Always goes in whole and first into every CLI's launch prompt, into `memory_read`, into push, and into `critical` in the planner snapshot, not cut by the memory budget. A line that is the same at both levels shows only once.
  - Only the owner changes them, through the API or the UI. Agent tools refuse (`memory_save` to `critical`), and the file is outside the folders Claude Code may write to without permission.
  - Unlike ordinary memory, which is presented as background: even a direct request does not count as permission. The agent must stop, name the rule, and wait for confirmation of that one action.
  - **These are instructions, not a technical lock.** Agents on This PC have their own Bash, so compliance depends on the model. Tested for real with Haiku on 2026-11-03: `npm run db:reset` was not run and the agent asked first. What the CLI or the hub enforces is in the blocked commands list below.
- **Blocked commands:** an owner's list per project and for all projects, in `memory-blocked.json`, changed through the critical notes page or the API, and out of reach of agent tools. A pattern is the start of a command (`git push --force` also covers what follows it), `*` means anything (`*db:reset*` finds it anywhere in the command). At most 64 patterns, 200 characters. From the first time the hub runs, the all-projects list holds the ones the built-in critical notes name; the owner is free to change or empty it. Applies from the next console start.
  - **Claude Code:** `permissions.ask` through `--settings` per launch, written `Bash(*pattern*)` because the CLI strips only a few wrappers and does not see through `bash -c`. The CLI stops at a permission dialog, the status becomes `needsYou`, and the owner answers in the console. A commit message that quotes the command asks too. Tested for real with Haiku on 2026-11-05 without skip permissions, including through `bash -c`: the script did not run and the screen named the rule. Claude Code's documentation says `ask` rules still apply in `bypassPermissions`; that has not been tested here.
  - **Codex:** `prefix_rule(..., decision = "prompt")` in `rules/nextboard.rules`, only in the config folder the hub makes, never in the user's own. One folder is used by every console, so its list is the union of all projects. Codex matches words from the start of the command, so patterns with a `*` anywhere but the end are skipped. The format is checked with `codex execpolicy check`; enforcement has not been tested with a real model.
  - **OpenCode:** `permission.bash` through `OPENCODE_CONFIG_CONTENT`, also written `*pattern*`. Not tested with a real model.
  - **Agents on a server:** the hub's `bash` tool refuses by itself without running anything, with a message asking the agent to stop and ask. The parts of a compound command are checked one by one, including behind `sudo`, `env` and `bash -c`.
  - Other CLIs: no enforcement, only critical notes.
  - **Not a sandbox.** An agent with a shell can write a script that does the same thing, or edit the hub's data files, because it runs as the same user. This stops commands it recognizes, not intent.
- **How each CLI uses it (per launch, without writing CLI config):**
  - Claude Code: `autoMemoryDirectory` through `--settings` points at that folder, so it reads and writes the main memory directly. Old memory from the hub-made config folder (`<config>/projects/<slug>/memory`) is merged once. Hub tools are added through `--mcp-config` without `--strict-mcp-config`, so the user's own MCP servers stay.
  - Codex and OpenCode: the memory content is injected into the system instructions at launch (`developer_instructions`, `instructions`, ≤ 16 000 characters).
  - Other agents: through push as before.
  - Planner: `critical` and `memory` in the snapshot (`memory` ≤ 4000 characters).
- **Topic lookup (a small Context7, for this project only):** agents do not carry all of the memory. The launch prompt has only the critical notes, the main memory and the list of pages; the rest is looked up when needed through hub tools, for every CLI:
  - `memory_search{query, limit?}`: matching chunks from the main memory, sub-memories, agent notes and the all-projects memory; ≤ 6 chunks, ≤ 4000 characters. BM25 over titled chunks, in process, without an index. Titles and `description` weigh more; identifier parts are split (`userAuth_token`); prefixes match too.
  - `memory_read{name?, topic?}`: without arguments, the memory content and the list of pages; with `name`, one page (`global/<slug>` for an all-projects one); with `topic`, only the part about it.
  - `memory_save{fact, section?, page?}`: adds one fact to the main memory or to a page.
  - `memory_search` and `memory_read` ask no permission (Claude Code through `--allowedTools`, OpenCode through `permission`, and `readOnlyHint` in the tool definition); `memory_save` follows the CLI's permission rules.
- Ordinary memory is presented as background, not commands, because other agents can write it and it can go stale. A running agent gets the latest content at its next launch, through `memory_read`, or by push. `POST …/memory/push` with `scopes: ["critical"]` sends only the critical notes to every running agent of the project.
- `MEMORY.md` conventions: the sections `## Architecture`, `## Structure`, `## Commands`, `## Conventions`, `## Gotchas`, `## Context`; one fact = one bullet. Users are free to edit it. `[[name]]`, `[[name|display]]` and `[[name#section]]` link documents within the same main memory.
- **Init memory:** a read-only runner job (purpose `memory-init`, on the project's machine, cwd = project path, 20-minute limit), effort `quick`/`balanced`/`thorough`. It proposes, all at once, facts for the main memory, sub-memories with their content, and critical rules from what could damage this project in particular: commands that drop or reset a database, tests that delete data or point at the real database, migrations that cannot be rolled back, deploy scripts. Its prompt forbids opening `.env` and secret files. **Nothing is saved until the owner accepts**, and saving only adds: existing text does not change, and applying twice does not duplicate anything.
- Scan: a runner job (read-only tools, on the project's machine, cwd = project path), effort `quick`/`balanced`/`thorough` → 6–8 / 8–11 / 11–14 proposals `{kind, text}`. Nothing is saved until the user accepts.
- **Memory tile:** a `memory` node is a stack, like lego. The project's own memory is always on as the base; blocks from the all-projects memory, pages of this project, or other projects are added on top, and can be reordered by drag or Alt+arrow: higher ones come first when the budget runs out. Wire it to a console, group, orchestrator card or editor; the agents it reaches are the consoles and groups at the end of the wire, plus those wired from that orchestrator or editor, and the agents inside an editor card that orchestrator is wired to. A console connected to several tiles gets their blocks combined. Critical notes cannot be a block: those of the project itself and of all projects always come along, and those of another project bind only that project.
- **How the stack reaches agents, cheapest first:**
  - **Launch:** blocks go into the system prompt, without using a turn, within the same 16 000-character limit as project memory. Those that do not fit or are set to `lookup` go in only as one line with the name.
  - **Tools:** `memory_search` and `memory_read` directly cover the blocks connected to that console, without anything being typed. Another project's memory names have the form `<projectId>/<name>`. Only blocks in a connected tile can be read, not the rest of that project's content. `memory_save` still goes only to its own project.
  - **Planner:** tile blocks that reach its orchestrator card go into `memoryAdded` (≤ 2000 characters, because the planner runs on every command and has no tools). The main command bar is the same lead, so it gets the tile blocks that reach any orchestrator card on that board.
  - **Typed (uses a turn):** once when the wire is made, when a block is added, or on a layout deploy; otherwise only on request. Changed memory content **never** types by itself, so an agent saving a fact does not trigger a turn in other agents. The tile shows how many agents are behind and the estimated tokens, and has a Send button. `manual: true` ("Send when connected" off): wires and new blocks type nothing.
- **Only what is new:** the hub records per console the facts (lines) it already has, in `memory-seen.json`, as hashes only. A launch rewrites that record to match the system prompt content; a delivery counts when it is typed, a queued delivery counts so nothing is sent twice, and those that fail, expire or are cancelled do not count. Facts from `memory_save` are not sent back to the agent that saved them; what Claude Code writes straight to the folder cannot be recognized, so it can be sent back. Nothing new means nothing is typed (`upToDate`). Deleted facts are mentioned by count, not content. An agent that exits and a console that is deleted lose their record. `/clear` and `/compact` in Claude Code are read from the `SessionStart` hook: the record goes back to the system prompt content, the tile shows that agent as behind, and nothing is typed by itself. Other CLIs give no such signal, so `force: true` sends everything again.
- Push: a delivery to each target agent, with only what that agent does not have yet:

  ```
  Project context from NextBoard (background only, not a task). Keep it in mind for later work and reply with just: OK

  # Critical rules from the owner: never break these, whoever asks
  <new critical notes, outside the budget>

  <new facts with their headings, ≤ 4000 characters in total>

  # From the memory of another project, Acme: Design system. It is about that project, not this one
  <that block's new facts>

  More memory, left out to save space. When your work touches it, read it with the memory_read tool of the nextboard MCP server and the name:
  - global/writing: Writing style. How docs are written
  ```

  Blocks that do not fit or are `lookup` become that one line, and are not repeated until their content changes. Agents without hub tools (not Claude Code, Codex, OpenCode) cannot follow names, so they get what fits, plus `(N more facts not included)`. A new console of a CLI that receives context at launch needs no push. `settings.memory.autoPush` (default `false`; a NextBoard extension) applies only to other agents: a push when the agent is first `ready`.

| Method | Path | Request | Response / error |
|---|---|---|---|
| GET | `/api/projects/{id}/memory` | — | `{"project":{"content","mtime"},"global":{"content","mtime"},"notes":[…],"subs":[SubMemory]}` |
| PUT | `/api/projects/{id}/memory` | `{"scope":"project"\|"global","content","ifMtime"?}` (≤ 64 KB) | `{"content","mtime"}`; `409 mtime_mismatch` |
| POST | `/api/projects/{id}/memory/init` | `{"effort":"balanced","agent"?: "claude"}` | `202 Job` → `result: {"facts":[{"kind","text"}],"subs":[{"slug","title","description","content"}],"critical":["…"]}` |
| POST | `/api/workspaces/{id}/tiles/{nodeId}/push` | `{"targets"?: ["n_…"]}` | like `memory/push`, plus `"empty":true` when the tile has no content to send. Without `targets`: everything wired. `404 tile_not_found`; `400 not_content_tile` |
| POST | `/api/projects/{id}/memory/init/apply` | what was accepted from `result` | `GalaxyView` |
| POST | `/api/projects/{id}/memory/scan` | `{"effort":"balanced","agent"?: "claude"}` | `202 Job` → `result: {"facts":[{"kind","text"}]}` |
| POST | `/api/projects/{id}/memory/accept` | `{"scope","facts":[{"kind","text"}]}` | `{"content","mtime"}` (identical bullets are not duplicated) |
| POST | `/api/projects/{id}/memory/refresh` | `{"fact":"…"}` | `202 Job` → `{"verdict":"valid"\|"outdated"\|"unknown","suggestion"?}` |
| POST | `/api/projects/{id}/memory/push` | `{"memoryNodeId"?, "workspaceId"?, "targets"?: ["s_…"], "blocks"?: [{"projectId"?,"name","lookup"?}], "scopes"?: ["project","global","critical"], "force"?: false}` | `{"delivered":2,"busy":1,"skipped":0,"notAgent":1,"upToDate":1,"chars":840,"deliveryIds":[…]}`. `memoryNodeId`: that tile's stack, to the connected ones only (a tile without wires sends to nobody). `blocks`: these memories, `[]` means only critical notes. `scopes` is the same as `blocks` holding main memories. Without `targets` and a tile: every agent of the workspace, or every agent of the project when there is no `workspaceId`. `404 tile_not_found` |
| POST | `/api/projects/{id}/memory/plan` | same as `push` | `{"docs":[{"projectId"?,"name","title","owner","chars","locked"?,"lookup"?,"missing"?}],"targets":[{"sessionId","handle","agent","workspaceId","state","canSend","chars","lookup":[…]}],"budget":4000}`. What `push` would do, without typing anything; `chars: 0` means that agent already has all of it |
| GET | `/api/memory/blocked?projectId=` | — | `{"commands":["…"],"inherited"?:["…"]}`. Without `projectId`: the all-projects list |
| PUT | `/api/memory/blocked?projectId=` | `{"commands":["…"]}` | `{"commands":["…"]}`, cleaned up. `400 bad_blocked` |
| GET | `/api/memory/galaxy` | — | `GalaxyView`: `{"global":Owner,"projects":[Owner]}`, `Owner = {"projectId"?,"name","docs":[{"name","kind":"main"\|"sub"\|"note","title","description"?,"critical"?,"chars","facts","mtime"?,"sections":[{"title","chars"}],"links":["…"]}]}`. Without content. Oldest project first, so places and colors stay the same |
| GET | `/api/memory/search?q=&projectId=&limit=` | — | `{"hits":[{"projectId"?,"project"?,"doc","title","section"?,"text","critical"?,"global"?,"score"}]}`; without `projectId` it searches all projects; `limit` ≤ 20 |
| GET | `/api/memory/docs/{name}?projectId=` | — | `{"name","kind","title","description"?,"critical"?,"content","mtime"}`. `name`: `main`, a sub-memory slug, or `note:<file>`. Without `projectId`: the all-projects memory. `404 memory_not_found` |
| PUT | `/api/memory/docs/{name}?projectId=` | `{"title"?,"description"?,"content","ifMtime"?}` | like GET; creates it when it does not exist yet. `400 bad_memory_name`; `409 mtime_mismatch`; `409 too_many_memories`; `413 memory_too_big` |
| DELETE | `/api/memory/docs/{name}?projectId=` | — | `{"ok":true}`; `409 critical_required` |

## 13. Layout templates (B8)

```jsonc
// Layout — coordinates relative to the deploy point
{ "id": "builtin:duo" | "ly_…", "name": "Duo", "builtin": true, "createdAt"?: "…",
  "lanes":  [{ "key": "a", "agent"?: "claude", "role"?: "reviewers", "group"?: "g1", "x": 0, "y": 0, "w": 720, "h": 440,
              "part"?: "Web", "root"?: true }],              // where it worked when saved (a part by its name)
  "groups": [{ "key": "g1", "name": "Builders", "role": "general", "color": "sky" }],
  "nodes":  [{ "key": "o1", "kind": "orchestrator"|"kanban"|"note"|"text"|"checklist"|"memory"|"web"|"editor", "x", "y", "w", "h", "data": {…} }],
  "wires":  [{ "from": "o1", "to": "g1", "kind": "wire"|"forward" }] }
```

Built-in (NextBoard names): **Duo** (group of 2), **Trio** (group of 3), **Quartet** (group of 4), **Two quartets** (2 groups × 4), **Build & review** (2 builders + 1 reviewer + orchestrator + kanban), **Front & back** (frontend group of 2 + backend group of 2 + orchestrator), **Review trio** (3 reviewers), **Triage** (2 testers + 1 reviewer + kanban "Triage"), **Preview lab** (3 agents + web tile `localhost:5173`), **Docs crew** (2 docs + checklist), **Solo** (1 agent + checklist), **Sextet** (group of 6), **Three trios** (3 groups × 3 + orchestrator), **Lead & crew** (group of 4 + orchestrator + kanban), **Full stack** (frontend 2, backend 2, tester 2, reviewer 2 + orchestrator + kanban), **Code & pair** (editor card + group of 2), **Web builder** (frontend + backend + editor card + web tile), **Bug hunt** (2 testers + 2 fixers + kanban "Bugs" + orchestrator), **Security audit** (2 security + 1 reviewer + a filled-in audit checklist), **Design studio** (2 design + web tile + note), **Release crew** (2 builders + tester + docs + kanban "Release" + orchestrator), **Research desk** (2 agents + memory card + note). **Two duos** (2 groups × 2), **Octet** (group of 8), **Writer & checker** (1 + 1 reviewer), **Relay** (builder, tester, reviewer with forwards between groups, so each turn uses a turn of the next group), **Editor & agent**, **Code & crew** (editor + group of 4 + orchestrator), **Laravel crew** (backend 2, Blade 1, tests 1 + editor + kanban), **API crew**, **App team**, **UI lab**, **Refactor squad**, **Migration desk**, **Incident room**, **Review board**, **Planning desk**, **Content studio**, **Onboarding**, **Quality gate**, **Performance lab**, **Hackathon**, **Dependency update**, **Data crew**. Each built-in has a one-line `description` that is searched too. 44 in total.

| Method | Path | Request | Response / error |
|---|---|---|---|
| GET | `/api/layouts` | — | `Layout[]` (built-ins first) |
| POST | `/api/layouts` | `{"name"?, "workspaceId", "nodeIds"?}` | `201 Layout` (default name `Set N`) |
| PATCH | `/api/layouts/{id}` | `{"name"}` | `Layout` |
| DELETE | `/api/layouts/{id}` | — | `{"ok":true}`; `403 layout_builtin` |
| POST | `/api/layouts/{id}/sized` | `{"groupSizes": {"g1": 3}}` | `Layout` as it would land with those sizes, without making anything. `404 layout_not_found`, `400 bad_layout`, `400 too_many` |
| POST | `/api/workspaces/{id}/deploy` | `{"layoutId", "at"?: {x,y}, "fill"?: {"agent"}, "groupSizes"?: {"g1": 3}, "laneOverrides"?: {"a": {"agent"?, "machineId"?, "profileId"?, "yolo"?, "part"?, "root"?}}, "places"?: {"g1": {"part": "Web"}, "a2": {"root": true}, "e1": {"part": "Api"}}}` | `201 {"sessions":[Session…],"nodeIds":[…]}`; `404 layout_not_found`, `400 too_many`, `400 bad_placement`, `404 part_not_found`, `409 place_unmatched` |

- A deploy happens entirely in the hub (Android can deploy), in one `boardOps` broadcast. Lane agent: override → `fill` → `lane.agent` → role agent → machine default (machine = the lane's placement, else the override's or the project's machine). Unknown agent → default; a custom one that has been deleted → `shell`. Consoles get new handles.
- **Placement per lane** (v0.9.0-beta.2): `laneOverrides[lane].part`/`root` → `places[lane key]` → `places[group key]` → what the layout saved (`lane.part` matched by **part name**, any case, or `lane.root`) → for the lead and typed orders, the command's place, else the root → the compat rule of §5 (the override's `machineId`, else the project's first part's machine, in the folder of the first part on that machine; so a lane on a server lands in that server's part, not in `~`). An override with a `machineId` and no placement skips what the layout saved. A single-part project ignores what the layout saved.
- **`409 place_unmatched`** `{"error","code":"place_unmatched","unmatched":[{"key":"g1","part":"Web"},{"key":"e1","part":"Api"}]}`: the layout saved part names this project lacks, and `places` does not cover them. A group whose lanes all name the same missing part is listed by its key, other lanes and editor cards by theirs. The client asks the owner to match them and deploys again with `places`. Nothing starts before that.
- **Editor cards** are placed too: `places[node key]`, else the saved `data.part` (a part name; missing = `place_unmatched`), else the root. The board's editor card of that placement (root, or that part) moves into the layout's place and takes its wires; without one, a new card is made with the part's **id** in `data.part`. Several editor cards can be on one board, one per placement.
- **The number of consoles per group** is picked before the deploy, in the Layouts panel or through `groupSizes`. The hub computes the positions, for the preview and the deploy, so what you see is what lands. The first lanes keep their keys and choices, new lanes copy their group's last lane (`a3`, `a4`), and `laneOverrides` applies to new lanes too. Group shape follows the built-in teams: one row up to 3, 4 becomes 2×2, 5–6 become 3 columns, more than that 4 columns; stacked groups stay stacked. Whatever is to the right of and below a group shifts by the change. Space freed by a group that shrinks is only closed if that makes nothing overlap; otherwise it is left empty. Cards the owner deliberately stacked in a saved layout stay that way.
- **A group can be 0** when the layout has more than one group: the group and its wires are not made, other cards stay. A layout always keeps one console (`400 bad_layout`). Limit 12 per group and 12 per deploy (`400 too_many`). Lanes without a group in a saved layout cannot have their count changed. The template does not change: sizes apply only to that deploy. The command bar and the planner deploy the default sizes.
- Saving a layout **keeps**: kind, geometry, title/text/color, lane agents, groups, orchestrators, editor cards (without open files), memory tile stacks, wires/forwards, kanban column titles, localhost web tile URLs, and in a project of several parts where each console works (`lane.part` = the part's **name**, `lane.root`), each editor card's part (`data.part` = its name), and the part whose dev server a web tile shows (`data.part`, from its `partId`, else the one part on its machine; a deploy gives the tile that part's `machineId` and `partId`, and leaves an unknown name to the client's default). A single-part project saves no placement, so its layouts fit any project. **Drops**: images, ink, file cards, the contents of kanban/checklist/calendar cards, external URLs. A deploy to a board that already has an editor card of the same placement does not add a second one: its wires use the existing one.
- Home screen "arm layout": the client opens the workspace, then calls `deploy`.

## 14. New events on `/api/events`

```jsonc
{ "type": "delivery", "delivery": Delivery }
{ "type": "thread", "threadId": "p_…", "message": ThreadMessage }      // new or updated (same id)
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

`session` carries `paused`, `pausedAt`, `queue`. Topics for `?topics=` = the `type` values above.

## 15. Persistence

| File | Content |
|---|---|
| `threads/<tid>.jsonl` | append-only; compacted when it passes the limit (project 500 messages, card 200) |
| `orders.json` | active orders + those finished < 24 h ago (max 200) |
| `forwards.json` | one-time forwards that are not finished |
| `loops.json` | `LoopDef[]` including `state` |
| `loops/<id>.jsonl` | history (last 200 entries) |
| `memory/<projectId>/MEMORY.md`, `memory/global.md` | memory |
| `memory-blocked.json` | blocked commands per project and for all projects (§12) |
| `memory-seen.json` | per console, hashes of the memory facts it already has; no content (§12) |
| `handoffs.json` | handoffs with their summaries; finished ones are dropped after 24 hours (§9) |
| `layouts.json` | saved layouts (built-ins are in the binary) |
| `settings.json` | new keys `delivery`, `commands` (`planner`, `confirmTtlSec`, `reportTimeoutMin`, `riskyPatterns`), `memory` (`autoPush`) |
| `sessions.json` | + `paused` |
| `confirmations.json` | new: confirmations still stored, with their command context |

Deliveries live only in memory (lost on a hub restart; the thread records "interrupted by hub restart"). The order text stays in `orders.json`, so orders not typed yet can be resent (§4). Confirmations survive a restart: buttons and text answers still work, their TTL does not start over, and a decision already taken cannot be run twice. A planner turn cut off by a restart is not repeated and runs nothing; the thread records "The hub restarted before this was answered. Send it again."

## 16. Limits and timeouts

| Item | Value |
|---|---|
| Command text | ≤ 4000 characters; planner only ≤ 600 |
| Delivery | ≤ 32 000 characters; ≤ 20 queued per session; confirmation 8 s |
| Planner | 2 minutes; 1 turn per thread; 60 s cooldown after 2 failures |
| Confirmation | TTL 60 s (30–300) |
| Spawn | ≤ 12 per command/call; ≤ 64 sessions per workspace |
| Order report timeout | 30 minutes |
| Handoff | summary 4 minutes, ready 120 + 120 s |
| Forward | 20 000 / 32 000 characters; wait for an order 4 h; 30 minutes after the order |
| Tile content | 20 000 characters; 50 most recent page problems |
| Loop | interval 60 s–24 h; maxRuns 1–500; duration 15 minutes–24 h; 50 loops per hub; goal 90 s / 2 concurrent |
| Memory push | 4000 characters |
| Mention autocomplete | 30 items |

## 17. Security

- Orchestration text reaches a terminal **only** through the delivery engine with the agent guard; never into a shell, never into a permission prompt. The old endpoint `POST /api/sessions/{id}/input` stays for the user's manual input.
- The planner runs only on This PC, on the user's plan; the snapshot has no file/transcript content. Planner output is treated as untrusted: the hub translates names, steps outside the subset are dropped, the confirmation policy applies.
- The `command` goal runs a shell command of the user's choice; the planner cannot make it, and it is shown in full in the loop confirmation.
- Pause uses the PID from a tokened marker and validates the process's session id; it never sends a signal to a PID outside that terminal session.
- This phase writes nothing to remote machines (memory, threads and loops are stored in the hub), except the effects of orders the agents carry out themselves.
- Forward passes text between agents (risk of chained prompt injection): digests are always labeled with their source, `forwardDepth` is limited, forward edges have no cycles.

## 18. UX per client (what must be there)

- **Web/desktop:** the command bar below the board (Ctrl/⌘K focuses, Esc returns to the board, ↑/↓ history of 50, the `@` menu with ↑/↓, Tab/Enter, Esc); the chips New console, Spawn 4, Group, Layouts, Pause all, Resume all, Memory, and while the thread is open: Loops, Mission Map, Kill all (two clicks + modal). Thread panel: plan notes, reports with "See output", confirmation cards with buttons + countdown, "Still thinking…". A delivery queue badge in the console header ("2 queued", click = list + Cancel). Group frames with name/role/color, a 4/6-slot composer (≤ 12, click cycles agents, right-click for the catalog, fill all). Orchestrator card (input, thread, Connect mode, row actions). Mission Map (`X`, cannot be rebound) with filters and search. Loop panel (`L`): templates, estimated prompts/hour × consoles, history, a badge with the number of running loops. Memory panel (`M`): edit, scan, accept proposals, memory card + push results. Layout library + lane inspector (agent, machine, profile) + Deploy.
- **Android:** the main project screen = command bar + thread (reports; confirmations can be approved), "Mission control" (needs-you first, filters), pause/resume per console, the loop list (start/stop/run now), layout deploy from the project screen, `loopHeld`/`loopEnded` notifications.

## 19. Required test scenarios

1. A delivery never types into a shell: the agent `/exit`s while a delivery is queued → `failed agent_exited`; a `kind:"shell"` console → `409 not_agent`.
2. A delivery waits on `needsYou` and sends after the permission is answered; no byte is written while waiting (tested with a fake PTY).
3. Bracketed paste: multi-line text is wrapped in `ESC[200~…ESC[201~` when mode 2004 is on; without the mode → one line; ESC in the text is dropped.
4. Human typing guard: client input within 2 s delays a delivery.
5. Parser: every row of the §3.2 table has a test; `@everyone` = no mention; a mention without text → a nudge reply.
6. Fuzzy: `@Ahs` → `didYouMean` Ash; two candidates → `sendToAll`; a confirmation from a second client after the first → `409 already_decided`.
7. Confirmation: `kill` is always pending; typing `yes` → runs; other text → cancelled + processed; expires after 60 s.
8. Planner: Claude missing → `available:false`, parser fallback with a note; invalid JSON → fallback; two failures → cooldown; the snapshot holds no file content.
9. Order tracker: a `finished` report with an excerpt from the transcript; 30-minute timeout; `cancel that` cancels only the deliveries not typed yet.
10. Groups: duplicate member `400`; a name equal to a handle `409`; deleting a terminal → it leaves `members`.
11. A batch spawn of 4 with a group + orchestrator → one `boardOps` with all nodes/edges.
12. Local pause: SIGSTOP to the foreground pgrp; resume → SIGCONT + a resize nudge; deliveries wait while paused. Remote: the script refuses when the sid differs.
13. Handoff: the runner fails → raw brief; a target in `needsYou` for too long → `manual`.
14. Forward: a digest of two sources, cutting at 20 000/32 000, cyclic edges refused, `forwardDepth` > 3 not sent.
15. Loop: runs with no client connected; interval < 60 s refused; an `agentJudged` goal not twice within 90 s; a needsYou target → `skip` + `loopHeld`; a hub restart does not repeat missed runs.
16. Memory: scan saves nothing before `accept`; push cuts at 4000 characters with a note.
17. A layout deploy from Android (bearer) makes all sessions + groups + wires; per-lane machine overrides are honored.

## 20. Open issues / to verify

The status of each item after implementation and verification is in §21; items 1 and 2 are answered, item 5 in part.

1. **Runner in Phase 6:** the planner, handoff, goal and memory scan depend on the runner (API-studio §2), which the roadmap lists under Phase 7; the work order needs adjusting.
2. **Planner flags:** `claude -p --json-schema`, `--tools ""`, `--no-session-persistence` — whether they exist and how they behave in 2.1.x; fallback = instructions for a JSON block in the prompt + hub validation. Latency (5–15 s) and use of the user's plan per command.
3. **Free agents without hooks:** `ready` from the screen/activity can be wrong; splitting work can send to an agent that is actually busy (the delivery is still safe because it waits).
4. **`whileWorking:"type"`**: only safe for CLIs that queue input while working; needs a per-CLI list.
5. **SIGSTOP on Node/Rust TUIs**: API requests in flight can time out; redraw after SIGCONT; `ps -o tpgid=`/`sid=` may be missing on BusyBox → `409 pid_unknown`.
6. **Group frames** are computed by clients; two clients moving members at the same time can leave a frame slightly inaccurate until the next patch (last-writer-wins).
7. **Phonetic matching:** Double Metaphone only suits English names, while Indonesian spelling has its own folding (`indonesianKey`). The default handle names stay distinct under that folding (`TestDefaultHandlesFoldApart`).
8. **Handoff summaries on This PC** for a remote source move remote transcript content to This PC (still the user's); this needs the owner's approval if it is considered sensitive.
9. **Cron** uses the hub's time zone; clients in another zone see `nextRunAt` in UTC.
10. **Reports for agents without a transcript** are taken from the screen and can contain leftover UI.
11. **State lost on a hub restart**: running deliveries/handoffs are lost (confirmations are already persisted, §15). Decided (decision #34): the queue is not resumed automatically, and orders not typed yet are offered for resending (§4). Handoffs are still lost.

## 21. Verification results (This PC, 2026-10-28)

Claude Code **2.1.312** (`--model haiku` for consoles, the default model for the planner), Codex **0.165.0** (not run interactively in this phase). Real tests in `internal/server/realcli_orch_test.go` `TestRealOrchestration` (gated by `NEXTBOARD_REAL_CLI_TEST=1 NEXTBOARD_REAL_ORCH=1`, never runs in a plain `go test`; `newTestHub` also forces `commands.planner:"off"` so other tests never touch the owner's plan). **2 small prompts in total** (1 planner run, 1 order to a real console). `DISABLE_AUTOUPDATER=1` in the hub's env (inherited by consoles and the runner), without yolo/skip-permissions; the console uses `--permission-mode manual` in a scratch folder trusted since Phase 5. The checksums of `~/.claude/settings.json` and `~/.codex/config.toml` were the same before/after (checked by the test and by hand).

| # | Item | Result | Evidence |
|---|---|---|---|
| 1 | Runner in Phase 6 | Used: `internal/jobs` `SubmitRunner` (purposes `planner`, `handoff-summary`, `loop-goal`, `memory-scan`, `memory-init`, `memory-refresh`, `thread-summary`); `RunnerRequest.Transform` (new) shapes the result per purpose (e.g. memory scan → `{"facts":[…]}`) | `internal/orch` |
| 2 | Planner `claude -p --output-format json --json-schema '<PlanSchema>' --tools "" --max-turns 1 --permission-mode dontAsk --no-session-persistence` | **Confirmed.** `--json-schema` together with `--tools ""` works: the `result` holds JSON and the `structured_output` object is filled (matching the schema: `reply` + `steps[{op:"send",targets:["Ash"],text:…}]`). Latency 4.4 s (job); "Still thinking…" appears and is then replaced by "Planned." The hub translates the name `Ash` to the session | `TestRealOrchestration` log: `PLANNER state=done elapsedMs=4396`, `structured_output present=true` |
| 3 | Delivery to a real interactive Claude console | **Confirmed.** An order from the planner → delivery `queued` → typed (bracketed paste, Claude turns on DECSET 2004) → `sent` 0.36 s → `confirmed` 0.46 s (`UserPromptSubmit` hook); order `finished` 2.1 s; thread report = "OK" from the transcript | log: `DELIVERIES final … state:confirmed`, `THREAD agent/report: "OK"`; fixture `claude-2.1.312-ready-after-order.txt` |
| 4 | Trust dialog on a narrow console (60 columns) | At 60 columns the question wraps ("…or one / you trust?"), so the full-sentence pattern from Phase 5 no longer matches. With the new short patterns: `needsYou` (`stateSource:"screen"`, `activity:"Trust folder?"`); delivery `1` held (`waitReason:"needsYou"`), the screen did not change; Esc (human input) cancels → the agent exits rc 0 → shell, **without** a new trust entry in `~/.claude.json` | fixture `claude-2.1.312-needsYou-trust-60cols.txt` (screens & dialogs) |
| 5 | Pause/resume | Local: `TIOCGPGRP` + `kill(-pgid, SIGSTOP/SIGCONT)` on a real PTY (`/proc/<pid>/stat` = `T`, output stops, continues after CONT). Remote (throwaway OpenSSH sshd, loopback): the §8 script works **after** `kill -STOP -- "-$t"` was replaced with `kill -s STOP -- "-$t"` (dash rejects the first form: "Illegal number"); a pid without a terminal / from another session is refused | `TestLocalSignalForeground`, `TestSSHSignalForeground` (gated by `NEXTBOARD_SSHD_TEST`), `TestRemoteSignalScriptRefusesForeignSession`, `TestPauseResumeLocal` |
| 6 | Codex update check | Codex 0.165.0 has the config key `check_for_update_on_startup` (found in the binary), so a per-launch `-c check_for_update_on_startup=false` is available. This phase does **not** run Codex interactively; the hub has not added that flag to Codex launches yet (owner decision) — the main protection is the delivery guard below | `strings` on the binary |

**Delivery safety (an extra rule from the owner):** text is typed only when the screen is **positively recognized** as the CLI's normal input prompt (`agent.InputPrompt`): Claude = a cursor line `❯` in column 0 between two `─` lines; Codex = `›` in column 0 that is not a numbered option; agents without a rule = a prompt character on the cursor line. Strong dialog markers (a selected numbered option `› 1.`, "Enter to confirm/continue", "Esc to cancel/skip", "Update now", "Skip until next version", `[y/n]`, "press any key", arrows) on the lines below/around the cursor always refuse; dialog words that also show up in prose ("Do you want…") are only checked right around the cursor of agents without an input box, so an agent's reply above Claude's box does not block. The screen must be still for ≥ 300 ms before typing; right before Return the screen is checked again (`agent.InputPromptPasted`, which accepts input already wrapped inside the box) — a dialog that appears between the paste and Return → `failed submit_aborted`, and Return is not sent. 22 dialog fixtures in `internal/agent/testdata/dialogs/` (real dumps of Claude 2.1.312/Codex 0.163.0 + synthetic variants: a Codex update dialog with the cursor on "Update now", without a header, options only; Claude login/theme/API key/model picker; Claude update; Codex trust/login/approval; `[Y/n]`; a shell prompt; an empty screen) are tested as refused in `TestInputPromptRefusesDialogs` and through the engine in `TestDeliveryRefusesDialogs` (no byte written, ending in `failed prompt_unrecognized`), plus the hub test `TestDeliveryNeverTypesIntoUpdateDialog` (a fake agent shows an update dialog after `SessionStart`; no input arrives).

**Phase 5 fixes done along the way (web e2e findings):**

- **Stale needsYou with hooks on:** after a hook ends `needsYou` (e.g. `PostToolUse` after a permission is answered) while the answered dialog is still drawn, the screen no longer raises a second `needsYou` (and there is no second attention) until the screen has once been clear of the dialog (`TestHookedPermissionStaleScreen`, `TestStalePermissionSingleAttention`). This narrows T15h per API-agents §6.2 item 3.
- **Claude trust dialog:** short wrap-proof patterns (`Yes, I trust this folder`, `Quick safety check`, `project you created or one you trust`, `No, exit`), the activity label `Trust folder?`, and marked as a *startup dialog*, so it also raises `needsYou` from `starting`/`ready` even when `SessionStart` has already come (T15h only from `working` for other dialogs). Codex rules: `^\s*Update available` (the boxed banner on the ready screen is no longer taken as needsYou), `Update now`, `Sign in with ChatGPT`; labels `Update dialog`/`Login dialog`. `ScreenMatch.Startup`, `Pattern.Label/Startup` are new.
- `POST /api/sessions/{id}/restart` now accepts `agentSessionId` (route → `session.RestartRequest`, API-agents §3.3, `agents_api_test.go`).
- The data race between `os.File.Fd()` and `Close` on the local PTY (resize/TIOCGPGRP while the process ends) is fixed with a lock in `localProc`.

**Deviations from / additions to the contract (implementation):**

- **Delivery:** an extra `waitReason` `"dialog"`; a new setting `delivery.promptTimeoutSec` (120, 10–3600) → `failed prompt_unrecognized`. The per-reason limits give `expired` with `code` `starting_timeout`/`needs_you_timeout`/`working_timeout`/`reconnect_timeout`/`human_typing` (humanTyping ≤ 10 s, then it gives up); deadline passed → `expired deadline`; default deadline 1 hour (`deadlineSec` 5–86400). `submit_aborted` when conditions change after the paste (the text stays in the input). `manualAgent` is always `not_agent` (there is no permission mechanism yet). Confirmation is also taken to have happened when a new turn starts after Return (`lastTurn.startedAt`), because a short turn can finish before the engine's next step. `Delivery.projectId` (the `GET /api/deliveries` filter). The next delivery waits until the active one is `confirmed` or the 8 s window has passed. The process check before typing = the `agent-start` marker + the wrapper's foreground pgrp (local TIOCGPGRP, SSH `ps`); remote without `wrapperPid` → the marker only. `source` from the API is limited to `api|reply|skill|dictation`. `Session.queue` and `Session.subagents` (Mission Map) are added to Info.
- **Sleeping agents (ROADMAP #50, API-agents §28):** a delivery to an `asleep` console wakes it and waits with `waitReason: "starting"` (also while its process does not exist yet); the lead, `agentIdle` loops and handoff treat `asleep` the same as `ready`. An unfinished order and a handoff from/to a console keep it awake. Mission Map: `counts.asleep` (also counted in `idle`).
- **Board:** validation errors carry a `code` and status (`name_conflict` 409, `member_conflict`, `bad_edge`, `forward_cycle`, `bad_group`, `bad_orchestrator`, `bad_memory`, `too_many` 400); validation only covers the nodes/edges the batch touches (old boards with generic `wire` edges are not refused). Forward cycles treat a group + its members as one vertex.
- **Command:** `restart <who>` from a command = a restart with `resume:true` (the conversation is kept). `delete <group>` = `kill` the members + `group.dissolve` (destructive confirmation). Fuzzy `didYouMean`/`sendToAll` only for handles. Text that needs instructions to be written (`write a brief…`, `split the work…`) is not sent without a planner. Loops from an order: `maxRuns` 20, target `allAgents` (bar) or `orchestrator` (card). `ask summary` uses the `thread-summary` runner when the planner is available. Errors 404 `project_not_found`/`workspace_not_found`, 400 `bad_card`, 400 `bad_thread`.
- **PlanSchema:** targets as handles/group names; the fields `url` (web), `everySec` (loop), `group`, `source`, `sources`. A planner `loop.create` step never carries a `command` goal. The planner is taken to be logged out for 1 minute after `cli_not_logged_in` (`plannerLoginWait`).
- **Forward:** one-time with source = target → `400 forward_cycle`; `forwardDepth` is tracked per console turn (from the delivery that started the turn; a manual turn = 0) and reset after the turn ends; `split` without a planner = the whole digest to the first target. Forwards from edges are recorded as `source:"forward"` orders in the project thread.
- **Memory:** `push` counts `busy` = agent working (delivery queued), `skipped` = paused/needsYou/exited; without `targets`/node → every console of the workspace. `memory.autoPush` (extension) applies when a console is first `ready`.
- **Layout:** calendar cards are saved as a titled note (the calendar content is dropped); group names get a suffix on deploy when they clash; `fill` = `{"agent"}`.
- **Handoff:** the summary machine = the project machine when `claude`/`codex` is detected in the probe (not a login check), otherwise This PC; the new console is placed to the right of the source and joins the source's group (frame recomputed).
- **Remote pause:** `kill -s SIG -- "-$t"` (see #5); `ps` without `tpgid`/`sid` (BusyBox) → `409 pid_unknown`; failed validation → `409 signal_refused`.

**Side effects on This PC:** no config changes. The interactive console run added 2 lines (`Reply with the single word OK…`, `/exit`) to Claude Code's `~/.claude/history.jsonl` (normal input history; not edited, because other Claude processes write that file at the same time); the test session's transcript was deleted again; the planner uses `--no-session-persistence`. The trust dialog was cancelled, so there is no new trust entry.

**Not done yet / still open:** the list of CLIs that are safe for `whileWorking:"type"` (#4); interactive Codex not retested (the update check flag above is not set automatically yet); BusyBox `ps` (#5) not tested for real.

