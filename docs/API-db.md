# Database browser

A database client of the hub's own, in the spirit of DBeaver and TablePlus: a project's MySQL, MariaDB, PostgreSQL, Redis and Valkey connections, their tables and keys, an SQL editor, and SmartDB (the owner's agent CLI reading the database for them). The hub connects; a browser never reaches a database. Package `internal/dbx`, handlers `internal/server/h_db.go`, `h_db_agents.go` (agent tools and approvals), `h_db_ai.go` (SmartDB); client `web/src/db/`, the Code view's Databases tab (`studio/code/tools/DbTool.tsx`) and the board's Database card (`board/tiles/DatabaseTile.tsx`).

## Model

Connections belong to a **project**. Every Database card of the project and its Code view's Databases tab show the same connections, saved queries and history. A card may keep to one connection (`data.connectionId`), which is also what "only consoles wired to its card" means for agents (below). Deleting a card does not delete a connection; deleting a project deletes its connections, saved queries, history and activity log.

Files under the data folder (0600, written atomically):

| File | Holds |
|---|---|
| `dbs/connections.json` | The connections, with their secrets sealed by the hub's key (the vault box, AES-GCM). Nothing in a reply ever carries a secret: `hasPassword` and, per hop, `hasSecret` / `hasKey` say one is kept |
| `dbs/queries.json` | Saved queries of every project |
| `dbs/<id>/history.jsonl` | The last 500 statements run on a connection (the owner's, agents', SmartDB's) |
| `dbs/<id>/activity.jsonl` | What agents did on it: read, asked, approved, denied, ran, failed |

### Connection

```json
{
  "id": "db_8df04a815c8cb29f", "projectId": "p_…", "name": "shop", "description": "", "group": "",
  "driver": "postgres", "env": "staging",
  "host": "10.0.0.5", "port": 5432, "database": "shop", "user": "app", "showAll": false,
  "hasPassword": true, "savePassword": true,
  "tls": "preferred", "props": { "search_path": "app,public" }, "redisDb": 0,
  "reach": { "kind": "ssh", "hops": [ { "host": "bastion.example", "port": 22, "user": "deploy", "auth": "key", "keyPath": "~/.ssh/id_ed25519", "hostKey": "SHA256:…", "hasSecret": false, "hasKey": false } ],
             "configHost": "", "machineId": "", "remoteHost": "", "remotePort": 0, "localPort": 0, "openLocal": false, "keepAliveSec": 30, "timeoutSec": 15, "reconnect": true },
  "security": { "readOnly": false, "confirmWrites": true, "confirmEdits": true, "autoCommit": false },
  "agents": { "access": "ask", "scope": "project", "masked": ["*password*", "*token*", "*secret*", "*api_key*"], "askBar": true, "editorAi": true },
  "limits": { "timeoutSec": 60, "pageSize": 100 }
}
```

- **`driver`**: `mysql`, `mariadb`, `postgres`, `redis`, `valkey`. MySQL covers TiDB and PlanetScale; PostgreSQL covers Supabase, Neon and CockroachDB.
- **`env`** (the connection type): `local`, `development`, `staging`, `production`. Its colour marks every tab and card. A new connection takes the type's **Security** defaults, which the owner can change for that connection:

  | Type | Read-only | Confirm SQL that writes | Confirm grid edits |
  |---|---|---|---|
  | Local, Development | no | no | yes |
  | Staging | no | yes | yes |
  | Production | **yes** | yes | yes |

- **`reach`**: how the hub gets there. `direct` dials `host:port` itself. `machine` goes over the SSH connection NextBoard already keeps to one of the project's machines (`machineId`, `direct-tcpip`): `host:port` are then as that machine sees them. `ssh` is a tunnel of its own (below).
- **`savePassword: false`**: the password is asked when connecting (`409 password_needed`) and kept in the hub's memory until it restarts.
- **`agents`**: what agents of the project may do (Agents, below); `askBar` and `editorAi` turn SmartDB's Ask bar and the SQL editor's SmartDB menu off.
- **`limits`**: a statement stops after `timeoutSec`; a table page has `pageSize` rows.

### SSH tunnels

A tunnel is a chain of hops, the last one the SSH server that reaches the database (`remoteHost`/`remotePort`, else `host`/`port` of the connection), the ones before it jump servers. Each hop authenticates by `password`, `key` (a file on This PC, `keyPath`, or one uploaded and sealed, `hasKey`; an optional passphrase) or `agent` (This PC's SSH agent). `configHost` instead follows a `Host` of This PC's `~/.ssh/config`: its HostName, User, Port, IdentityFile and ProxyJump chain.

Host keys are checked against This PC's `~/.ssh/known_hosts`, or the hop's pinned `hostKey` fingerprint. An unknown key fails the test with `hostKey {hop, host, fingerprint, keyType, changed: false}`; the owner checks it and trusts it (`POST /api/dbs/{id}/trust`, or the wizard pins it before saving). A changed key (`changed: true`) is refused until the owner trusts the new one. Keep-alives go every `keepAliveSec`; an idle connection closes after 10 minutes, 30 when it keeps a local port open.

**`openLocal`**: the tunnel also listens on `127.0.0.1:localPort` of This PC while it is up, so that tools and agents there (`php artisan`, `prisma`, a desktop client) reach the database. Off by default.

## Endpoints

All need a signed-in owner; bodies are JSON (at most 1 MB).

| Method and path | What |
|---|---|
| `GET /api/projects/{id}/dbs` | `{connections, status}`: `status[id] = {connected, version, since, tunnel, localPort, passwordNeeded, error}` |
| `POST /api/projects/{id}/dbs` | `{connection, secrets, detectRef?}` → `201` the connection |
| `GET /api/projects/{id}/dbs/detect` | `{candidates: [{source, part, connection, hasPassword, ref}]}`: connections found in the `.env` files of the project's folders (Laravel `DB_*`, `DATABASE_URL`, `REDIS_URL`, `REDIS_*`), on This PC or the machines of its parts. A candidate's password is never sent: `detectRef` names it for create and test |
| `POST /api/dbs/test` | `{connection, secrets, id?, detectRef?}` → `{ok, version, ms, hops: [{host, ms, error}], tables, keys, error, hostKey?}`. With `id`, secrets not given are the kept ones |
| `GET /api/dbs/{id}` | `{connection, status}` |
| `PUT /api/dbs/{id}` | `{connection, secrets}`: replaces it; a secret left out stays as kept, `""` removes it. The live connection closes |
| `DELETE /api/dbs/{id}` | Forgets it, its secrets, history and activity. The database is not touched |
| `POST /api/dbs/{id}/trust` | `{hop, fingerprint}`: pins a hop's host key |
| `POST /api/dbs/{id}/password` | `{password}`: for a connection that does not save it |
| `POST /api/dbs/{id}/disconnect` | Closes it (and its tunnel) |
| `GET /api/dbs/{id}/schema?current=` | SQL: `{schema: {databases, current, tables, views, routines}}` (objects `{name, type, rows, bytes}`; `rows` −1 when the server has no estimate). Redis: `{kv: {version, db, dbs: {"0": 812}, memory, clients, mode, slowLog}}` |
| `GET /api/dbs/{id}/table?schema=&name=` | `{schema, name, columns, indexes, foreignKeys, referencedBy, primaryKey, ddl, rows}` (cached 20 s) |
| `POST /api/dbs/{id}/rows` | `{schema, table, filters, where, sort, offset, limit}` → `{columns, rows, total, ms, sql}`. Filters `{column, op, value?, values?}` with `op` `= != < <= > >= like` `not like` `contains starts ends` `in` `not in` `null` `not null` `between`; `where` is raw SQL without `;`. `total` −1 when not counted in time |
| `POST /api/dbs/{id}/run` | `{session, sql, schema?, confirmed?, confirmName?, maxRows?}` → `{results: [{sql, kind, columns, rows, truncated, affected, ms, error, pending, notice}]}`. One result per statement |
| `POST /api/dbs/{id}/sessions/{sid}/finish` | `{commit}`: commits or rolls back the session's open transaction |
| `POST /api/dbs/{id}/sessions/{sid}/cancel` | `{stopped}`: stops what the session runs (`KILL QUERY` on MySQL, the driver's cancel request on PostgreSQL) |
| `POST /api/dbs/{id}/apply` | Grid edits: `{schema, table, changes: [{op: update|insert|delete, key, set}], preview?, confirmed?}` → `{sql, affected}`. One transaction; an UPDATE or DELETE that would touch other than exactly one row rolls all of it back |
| `POST /api/dbs/{id}/export` | `{rows: <rows request>} | {sql, schema}`, `format` `csv`, `json` or `sql` (INSERTs), `table?` → the file, streamed from a read-only transaction, at most 1,000,000 rows |
| `GET /api/dbs/{id}/history?limit=` | `{items: [{at, sql, kind, rows, ms, error, who}]}` |
| `GET /api/dbs/{id}/activity?limit=` | `{items: [{at, who, sql, rows, what, note, error}]}` |
| `GET /api/dbs/{id}/keys?db=&cursor=&match=&type=&count=` | Redis: `{keys: [{name, type, ttl}], cursor, total}` by SCAN (never KEYS); `ttl` in ms, −1 none |
| `GET /api/dbs/{id}/key?db=&name=&page=` | `{name, type, ttl, size, encoding, length, value | items | entries, next}`: a page of a hash, list, set, sorted set or stream |
| `POST /api/dbs/{id}/key` | `{db, key, op, field, value, score, index, ttl, to, confirmed}` with `op` `set hset hdel lset rpush lrem sadd srem zadd zrem xadd xdel rename expire persist del` → `{result}` |
| `POST /api/dbs/{id}/command` | `{db, line, confirmed?, confirmName?}` → `{output}`, as redis-cli prints it. Blocking commands (`BLPOP`, `SUBSCRIBE`, `MONITOR`…) and `KEYS` are refused |
| `GET/POST /api/projects/{id}/db-queries`, `DELETE …/db-queries/{qid}` | Saved queries `{id, projectId, connectionId, name, sql, updatedAt}` |
| `GET /api/db-requests` | Agents' requests to write (Agents) |
| `POST /api/db-requests/{id}/approve` | `{session?, confirmName?}` |
| `POST /api/db-requests/{id}/deny` | `{note?}` |
| `POST /api/dbs/{id}/ai/filters` | SmartDB's Ask bar: `{schema, table, text}` → `{filters, sort, where, note}` |
| `POST /api/dbs/{id}/ai/sql` | `{mode: write|explain|fix|optimize, sql, prompt, error, schema}` → `{sql, text}` |
| `POST /api/dbs/{id}/ai/chat` | `{message, history, table?, schema?}` → `202 {run, jobId}`; the answer comes as events |

A **session** is one editor tab: its own server connection, so `BEGIN`, `SET`, `USE` and temporary tables hold across runs (at most 32 per connection; idle ones close). `run` on a session with a statement waiting (`pending`) answers `409 pending` until it is committed or rolled back.

## Safety

Every statement is classified before it runs (`dbx.Classify`): `read`, `write`, `ddl`, `tx` or `session`, and whether it is **dangerous**: `DROP`, `TRUNCATE`, `DELETE` or `UPDATE` without `WHERE`, `ALTER … DROP`, and on Redis `FLUSHDB`, `FLUSHALL`, `SWAPDB`, `SCRIPT FLUSH` and a `DEL` of many keys.

| Case | Answer |
|---|---|
| A read-only connection, anything but a read | `403 read_only`. Reads also run in a read-only transaction |
| `confirmWrites`, writes not yet confirmed | `409 confirm` with `statements`; again with `confirmed: true` |
| Dangerous, always, whatever the connection allows | `409 confirm_name` with `statements` and `rows` (an estimate, −1 unknown); again with `confirmName` = the connection's name. It then runs in a transaction that waits (`pending: true`, a `notice` with the rows touched) for `finish`. MySQL commits DDL at once, and says so |
| A connection that does not save its password | `409 password_needed` |
| A driver's error | `422` with the server's message and what to do about it |

Values a browser cannot show as they are come as objects: integers beyond 2^53 as strings, binary as `{"$bin": "<hex of the first 256 bytes>", "len"}`, text over 64 KB as `{"$cut": "<the first 64 KB>", "len"}`.

## Agents

Agents of the project get these MCP tools (in Claude's allowed tools): `db_connections`, `db_schema`, `db_table`, `db_query`, `db_explain`, `db_execute`, `db_result`, `redis_scan`, `redis_get`, `redis_command`. Per connection, `agents.access`:

| Access | Agents may |
|---|---|
| `none` | Not see it |
| `read` | Schema, `SELECT`s, Redis reads |
| `ask` (default) | Read; a write waits up to 45 s for the owner's approval, then `db_result` tells the outcome |
| `write` | Read and write; dangerous statements still wait for the owner, with the name typed |

Never more than the connection allows: a read-only connection lets agents read only. `agents.scope` `wired` limits it to consoles wired to a Database card kept to this connection. Columns matching `agents.masked` (`table.column` or a column, `*` wildcards) show as `•••` to agents and are never sent to their model. Every call lands in the activity log.

A write waiting raises an attention of kind `database` ("<who> asks to write to <connection>") and the app's request pill; the dialog shows the SQL, the agent's reason and the rows it touches. **Allow for this session** lets the agent's next writes to that connection run without asking until its session ends; a dangerous one still asks.

## SmartDB

Runs the owner's own agent CLI on This PC (Claude Code), with their login. The Ask bar turns words into filters (`ai/filters`), the SQL editor's menu writes, explains, fixes and optimises SQL (`ai/sql`), and the assistant (`ai/chat`) answers questions by running read-only queries itself through the same database tools, as `SmartDB`. The schema, the SQL and, for the assistant, what its queries return go to its model; masked columns do not.

## Events

On `/api/events`: `db` (`{connection, status}` created or changed), `dbRemoved` (`{id, projectId}`), `dbStatus` (`{id, status}`), `dbSchema` (`{id}`: a DDL ran, the schema changed), `dbQueries` (`{projectId}`), `dbRequest` (`{id, state, connectionId}`: a request made or ended; `GET /api/db-requests` has it whole), `dbAgentCall` (`{connectionId, run, sql, ms, columns, rows, total, affected, error}`: a SmartDB query as it runs), `dbChat` (`{connectionId, run, text, error}`: SmartDB's answer).

## Tests

`go test ./internal/dbx` (statements, filters, grid edits, masking, `.env` detection, Redis on miniredis, SSH tunnels on in-process servers). `make test-db` runs the real-server tests against throwaway MySQL 8.4 and PostgreSQL 18 containers it starts and removes (`scripts/db-test.sh`, Docker). `web/e2e/db-browser.spec.ts` runs the browser against throwaway PostgreSQL and Redis containers of its own, and is skipped without Docker.
