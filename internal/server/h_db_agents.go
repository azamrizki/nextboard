package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/dbx"
	"github.com/azamrizki/nextboard/internal/orch"
)

// Agents and the database browser (docs/API-db.md, Agents): the hub's db_* and redis_* tools,
// within each connection's agent access; a write that needs the owner waits for the owner's
// approval in the browser, and DROP, TRUNCATE and changes without WHERE always do, with the
// connection's name typed.

const (
	dbWait       = 45 * time.Second // one tool call, under the CLIs' tool time limits
	dbExpiry     = 10 * time.Minute
	dbKeep       = 15 * time.Minute
	dbWaitingMax = 30
	dbAgentRows  = 200
	dbAgentText  = 24 << 10
)

var dbToolList = []map[string]any{
	{"name": "db_connections", "description": "List the project's database connections you may use: id, name, driver (mysql, mariadb, postgres, redis, valkey), environment, and what you may do (read, ask to write, write). Use the id or the name in the other db_ tools.",
		"annotations": map[string]any{"readOnlyHint": true}, "inputSchema": toolSchema([]string{}, map[string]any{})},
	{"name": "db_schema", "description": "List the tables, views and routines of an SQL connection (MySQL: a database, PostgreSQL: a schema), with row estimates; for Redis, the server's databases and key counts.",
		"annotations": map[string]any{"readOnlyHint": true},
		"inputSchema": toolSchema([]string{"connection"}, map[string]any{"connection": map[string]any{"type": "string"}, "schema": map[string]any{"type": "string", "description": "a database (MySQL) or schema (PostgreSQL); empty for the connection's own"}})},
	{"name": "db_table", "description": "The structure of a table: columns with types, keys, indexes, foreign keys and the tables that point to it.",
		"annotations": map[string]any{"readOnlyHint": true},
		"inputSchema": toolSchema([]string{"connection", "table"}, map[string]any{"connection": map[string]any{"type": "string"}, "table": map[string]any{"type": "string"}, "schema": map[string]any{"type": "string"}})},
	{"name": "db_query", "description": "Run SQL that only reads (SELECT, SHOW, EXPLAIN, WITH … SELECT) in a read-only transaction. Rows are capped; columns the owner masked show as •••. To change data use db_execute.",
		"annotations": map[string]any{"readOnlyHint": true},
		"inputSchema": toolSchema([]string{"connection", "sql"}, map[string]any{"connection": map[string]any{"type": "string"}, "sql": map[string]any{"type": "string"}, "schema": map[string]any{"type": "string"}, "max_rows": map[string]any{"type": "integer", "description": "at most 200"}})},
	{"name": "db_explain", "description": "Show the database's plan of a statement (EXPLAIN), without running it.",
		"annotations": map[string]any{"readOnlyHint": true},
		"inputSchema": toolSchema([]string{"connection", "sql"}, map[string]any{"connection": map[string]any{"type": "string"}, "sql": map[string]any{"type": "string"}, "schema": map[string]any{"type": "string"}})},
	{"name": "db_execute", "description": "Run SQL that changes data or tables. Depending on the connection it runs at once or waits for the owner's approval (say why). DROP, TRUNCATE and DELETE or UPDATE without WHERE always wait for the owner. A read-only connection refuses it.",
		"inputSchema": toolSchema([]string{"connection", "sql", "why"}, map[string]any{"connection": map[string]any{"type": "string"}, "sql": map[string]any{"type": "string"}, "why": map[string]any{"type": "string", "description": "one line the owner reads before approving"}, "schema": map[string]any{"type": "string"}})},
	{"name": "db_result", "description": "The outcome of a db_execute or redis_command that was waiting for the owner's approval.",
		"annotations": map[string]any{"readOnlyHint": true},
		"inputSchema": toolSchema([]string{"request"}, map[string]any{"request": map[string]any{"type": "string"}})},
	{"name": "redis_scan", "description": "List keys of a Redis or Valkey connection matching a pattern (SCAN, never KEYS), with their type and TTL.",
		"annotations": map[string]any{"readOnlyHint": true},
		"inputSchema": toolSchema([]string{"connection"}, map[string]any{"connection": map[string]any{"type": "string"}, "match": map[string]any{"type": "string"}, "type": map[string]any{"type": "string"}, "cursor": map[string]any{"type": "string"}, "db": map[string]any{"type": "integer"}})},
	{"name": "redis_get", "description": "Read one key: its type, TTL, size and value (a page of it for hashes, lists, sets, sorted sets and streams).",
		"annotations": map[string]any{"readOnlyHint": true},
		"inputSchema": toolSchema([]string{"connection", "key"}, map[string]any{"connection": map[string]any{"type": "string"}, "key": map[string]any{"type": "string"}, "db": map[string]any{"type": "integer"}})},
	{"name": "redis_command", "description": "Run one Redis command line (as redis-cli). Reads run at once; writes follow the connection's agent access like db_execute; FLUSHDB, FLUSHALL and the like always wait for the owner; blocking commands and KEYS are refused.",
		"inputSchema": toolSchema([]string{"connection", "command"}, map[string]any{"connection": map[string]any{"type": "string"}, "command": map[string]any{"type": "string"}, "why": map[string]any{"type": "string"}, "db": map[string]any{"type": "integer"}})},
}

// dbAgentsMay says a project has a connection agents may use.
func (s *Server) dbAgentsMay(projectID string) bool {
	for _, c := range s.dbs.Store.List(projectID) {
		if c.Agents.Access != dbx.AgentsNone {
			return true
		}
	}
	return false
}

// dbWiredTo says the console is wired to a database card of the connection on one of the
// project's boards (directly, through a group or an editor card).
func (s *Server) dbWiredTo(projectID, sessionID, connID string) bool {
	pr, ok := s.projects.Get(projectID)
	if !ok || sessionID == "" {
		return false
	}
	all := s.sessions.List()
	for _, ws := range pr.Workspaces {
		b, err := s.boards.Get(ws.ID)
		if err != nil {
			continue
		}
		for _, n := range b.Nodes {
			if n.Kind != "database" {
				continue
			}
			var d struct {
				ConnectionID string `json:"connectionId"`
			}
			if json.Unmarshal(n.Data, &d) != nil || d.ConnectionID != connID {
				continue
			}
			if orch.DirectedSessions(pr, b, all, n.ID)[sessionID] {
				return true
			}
		}
	}
	return false
}

// dbAgentConn finds the connection an agent names (id or name) and checks it may use it.
func (s *Server) dbAgentConn(t remoteTarget, ref string) (dbx.Connection, error) {
	ref = strings.TrimSpace(ref)
	var found *dbx.Connection
	for _, c := range s.dbs.Store.List(t.projectID) {
		if c.ID == ref || strings.EqualFold(c.Name, ref) {
			c := c
			found = &c
			break
		}
	}
	if found == nil {
		return dbx.Connection{}, fmt.Errorf("no connection %q in this project: db_connections lists them", ref)
	}
	c := *found
	if t.dbConn != "" && c.ID != t.dbConn {
		return dbx.Connection{}, fmt.Errorf("this run may use only the connection it was opened on")
	}
	if c.Agents.Access == dbx.AgentsNone {
		return dbx.Connection{}, fmt.Errorf("the owner does not share %s with agents", c.Name)
	}
	if c.Agents.Scope == "wired" && t.dbRun == "" && !s.dbWiredTo(t.projectID, t.sessionID, c.ID) {
		return dbx.Connection{}, fmt.Errorf("%s is shared only with consoles wired to its card on the board", c.Name)
	}
	return c, nil
}

// dbWho names the agent in the activity log.
func (s *Server) dbWho(t remoteTarget) string {
	if t.dbWho != "" {
		return t.dbWho
	}
	if in, ok := s.sessions.Info(t.sessionID); ok {
		if in.Handle != "" {
			return "@" + in.Handle
		}
		return in.Name
	}
	return "agent"
}

func dbAccessName(c dbx.Connection) string {
	switch {
	case c.Security.ReadOnly || c.Agents.Access == dbx.AgentsRead:
		return "read"
	case c.Agents.Access == dbx.AgentsAsk:
		return "read, ask the owner to write"
	}
	return "read and write"
}

// dbTool runs one db_ or redis_ tool for an agent.
func (s *Server) dbTool(ctx context.Context, t remoteTarget, name string, raw json.RawMessage) (string, bool) {
	var a struct {
		Connection string `json:"connection"`
		Schema     string `json:"schema"`
		Table      string `json:"table"`
		SQL        string `json:"sql"`
		Why        string `json:"why"`
		MaxRows    int    `json:"max_rows"`
		Request    string `json:"request"`
		Match      string `json:"match"`
		Type       string `json:"type"`
		Cursor     string `json:"cursor"`
		Key        string `json:"key"`
		Command    string `json:"command"`
		DB         int    `json:"db"`
	}
	if len(raw) > 0 && json.Unmarshal(raw, &a) != nil {
		return "invalid arguments", true
	}
	if name == "db_connections" {
		var b strings.Builder
		for _, c := range s.dbs.Store.List(t.projectID) {
			if c.Agents.Access == dbx.AgentsNone || (t.dbConn != "" && c.ID != t.dbConn) {
				continue
			}
			if c.Agents.Scope == "wired" && t.dbRun == "" && !s.dbWiredTo(t.projectID, t.sessionID, c.ID) {
				continue
			}
			fmt.Fprintf(&b, "%s\t%s\t%s\t%s\tdatabase %s\tyou may: %s\n", c.ID, c.Name, c.Driver, c.Env, orDash(c.Database), dbAccessName(c))
		}
		if b.Len() == 0 {
			return "No connection of this project is shared with you.", false
		}
		return "id\tname\tdriver\tenvironment\tdatabase\taccess\n" + b.String(), false
	}
	if name == "db_result" {
		return s.dbAwait(ctx, a.Request, t.sessionID)
	}
	c, err := s.dbAgentConn(t, a.Connection)
	if err != nil {
		return err.Error(), true
	}
	who := s.dbWho(t)
	switch name {
	case "db_schema":
		if c.Driver.IsKV() {
			e, err := s.dbs.KV(ctx, c.ID)
			if err != nil {
				return dbx.Friendly(err), true
			}
			info, err := e.Info(ctx)
			if err != nil {
				return dbx.Friendly(err), true
			}
			b, _ := json.Marshal(info)
			return string(b), false
		}
		e, err := s.dbs.SQL(ctx, c.ID)
		if err != nil {
			return dbx.Friendly(err), true
		}
		sc, err := e.Schema(ctx, a.Schema)
		if err != nil {
			return dbx.Friendly(err), true
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s · %s · open: %s (others: %s)\n", c.Name, e.Version(), sc.Current, strings.Join(sc.Databases, ", "))
		for _, o := range sc.Tables {
			fmt.Fprintf(&b, "table %s (≈%d rows)\n", o.Name, o.Rows)
		}
		for _, o := range sc.Views {
			fmt.Fprintf(&b, "view %s\n", o.Name)
		}
		for _, o := range sc.Routines {
			fmt.Fprintf(&b, "%s %s\n", o.Type, o.Name)
		}
		return b.String(), false
	case "db_table":
		e, err := s.dbs.SQL(ctx, c.ID)
		if err != nil {
			return dbx.Friendly(err), true
		}
		schema := a.Schema
		if schema == "" {
			if sc, err := e.Schema(ctx, ""); err == nil {
				schema = sc.Current
			}
		}
		ti, err := e.Table(ctx, schema, a.Table)
		if err != nil {
			return dbx.Friendly(err), true
		}
		return dbTableText(ti, c.Agents.Masked), false
	case "db_query", "db_explain":
		sql := a.SQL
		for _, st := range dbx.Split(sql, c.Driver) {
			if k := dbx.Classify(st, c.Driver); k.Kind != dbx.KindRead && k.Kind != dbx.KindSession {
				return strings.ToUpper(k.Verb) + " changes data: " + name + " only reads. Use db_execute.", true
			}
		}
		if name == "db_explain" {
			stmts := dbx.Split(sql, c.Driver)
			if len(stmts) != 1 {
				return "explain one statement", true
			}
			sql = "EXPLAIN " + stmts[0]
		}
		return s.dbAgentRead(ctx, t, c, who, sql, a.Schema, a.MaxRows)
	case "db_execute":
		return s.dbAgentWrite(ctx, t, c, who, a.SQL, a.Why, a.Schema, false, 0)
	case "redis_scan":
		e, err := s.dbs.KV(ctx, c.ID)
		if err != nil {
			return dbx.Friendly(err), true
		}
		var cur uint64
		fmt.Sscan(a.Cursor, &cur)
		page, err := e.Scan(ctx, a.DB, cur, a.Match, a.Type, 500)
		if err != nil {
			return dbx.Friendly(err), true
		}
		var b strings.Builder
		for _, k := range page.Keys {
			fmt.Fprintf(&b, "%s\t%s\tttl %d ms\n", k.Name, k.Type, k.TTL)
		}
		fmt.Fprintf(&b, "(%d keys here, %d in the database; next cursor %d)\n", len(page.Keys), page.Total, page.Cursor)
		s.dbs.Store.AddActivity(c.ID, dbx.Activity{At: time.Now().UTC(), Who: who, SQL: "SCAN " + orDash(a.Match), Rows: int64(len(page.Keys)), What: "read"})
		return b.String(), false
	case "redis_get":
		e, err := s.dbs.KV(ctx, c.ID)
		if err != nil {
			return dbx.Friendly(err), true
		}
		kv, err := e.Key(ctx, a.DB, a.Key, "")
		if err != nil {
			return dbx.Friendly(err), true
		}
		b, _ := json.Marshal(kv)
		s.dbs.Store.AddActivity(c.ID, dbx.Activity{At: time.Now().UTC(), Who: who, SQL: "GET " + a.Key, Rows: 1, What: "read"})
		return dbClip(string(b)), false
	case "redis_command":
		args, err := dbx.SplitArgs(a.Command)
		if err != nil || len(args) == 0 {
			return "a command line, as redis-cli takes it", true
		}
		st, refused := dbx.RedisStmt(args)
		if refused != "" {
			return refused, true
		}
		if st.Kind == dbx.KindRead || st.Kind == dbx.KindSession {
			e, err := s.dbs.KV(ctx, c.ID)
			if err != nil {
				return dbx.Friendly(err), true
			}
			out, err := e.Command(ctx, dbx.CommandOpts{DB: a.DB, Line: a.Command, ReadOnly: true})
			if err != nil {
				return dbx.Friendly(err), true
			}
			s.dbs.Store.AddActivity(c.ID, dbx.Activity{At: time.Now().UTC(), Who: who, SQL: a.Command, What: "read"})
			return dbClip(out), false
		}
		return s.dbAgentWrite(ctx, t, c, who, a.Command, a.Why, "", true, a.DB)
	}
	return "unknown tool " + name, true
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func dbClip(s string) string {
	if len(s) <= dbAgentText {
		return s
	}
	cut := s[:dbAgentText]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "\n… (cut: ask for fewer rows or columns)"
}

func dbTableText(t *dbx.TableInfo, masked []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "table %s.%s (≈%d rows)\ncolumns:\n", t.Schema, t.Name, t.Rows)
	cols := make([]dbx.ResultColumn, len(t.Columns))
	for i, c := range t.Columns {
		cols[i] = dbx.ResultColumn{Name: c.Name}
	}
	mask := dbx.Masked(masked, cols)
	for i, c := range t.Columns {
		null := "NOT NULL"
		if c.Nullable {
			null = "NULL"
		}
		fmt.Fprintf(&b, "  %s %s %s", c.Name, c.Type, null)
		if c.Key != "" {
			b.WriteString(" " + c.Key)
		}
		if c.Extra != "" {
			b.WriteString(" " + c.Extra)
		}
		if mask[i] {
			b.WriteString(" (masked: its values are hidden from agents)")
		}
		if c.Comment != "" {
			b.WriteString(" -- " + c.Comment)
		}
		b.WriteString("\n")
	}
	for _, ix := range t.Indexes {
		fmt.Fprintf(&b, "index %s (%s) unique=%v primary=%v\n", ix.Name, strings.Join(ix.Columns, ", "), ix.Unique, ix.Primary)
	}
	for _, fk := range t.ForeignKeys {
		fmt.Fprintf(&b, "foreign key (%s) → %s.%s(%s)\n", strings.Join(fk.Columns, ", "), fk.RefSchema, fk.RefTable, strings.Join(fk.RefCols, ", "))
	}
	for _, fk := range t.ReferencedBy {
		fmt.Fprintf(&b, "referenced by %s.%s(%s)\n", fk.RefSchema, fk.RefTable, strings.Join(fk.RefCols, ", "))
	}
	return b.String()
}

// dbResultText writes results as tab-separated text, masked.
func dbResultText(res []dbx.Result, masked []string) string {
	var b strings.Builder
	for i, r := range res {
		if i > 0 {
			b.WriteString("\n")
		}
		if r.Error != "" {
			fmt.Fprintf(&b, "error: %s\n", r.Error)
			continue
		}
		if len(r.Columns) == 0 {
			fmt.Fprintf(&b, "%s: %d row(s) changed, %d ms\n", r.Kind, r.Affected, r.MS)
			if r.Notice != "" {
				b.WriteString(r.Notice + "\n")
			}
			continue
		}
		mask := dbx.Masked(masked, r.Columns)
		dbx.MaskRows(mask, r.Rows)
		names := make([]string, len(r.Columns))
		for j, c := range r.Columns {
			names[j] = c.Name
		}
		b.WriteString(strings.Join(names, "\t") + "\n")
		for _, row := range r.Rows {
			cells := make([]string, len(row))
			for j, v := range row {
				cells[j] = dbCellText(v)
			}
			b.WriteString(strings.Join(cells, "\t") + "\n")
		}
		more := ""
		if r.Truncated {
			more = ", more not shown"
		}
		fmt.Fprintf(&b, "(%d row(s)%s, %d ms)\n", len(r.Rows), more, r.MS)
	}
	return dbClip(b.String())
}

func dbCellText(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case string:
		return strings.NewReplacer("\t", " ", "\n", "\\n").Replace(x)
	case map[string]any:
		if s, ok := x["$cut"].(string); ok {
			return dbCellText(s) + "…"
		}
		if _, ok := x["$bin"]; ok {
			return fmt.Sprintf("<binary %v bytes>", x["len"])
		}
	}
	return fmt.Sprint(v)
}

func (s *Server) dbAgentRead(ctx context.Context, t remoteTarget, c dbx.Connection, who, sql, schema string, maxRows int) (string, bool) {
	e, err := s.dbs.SQL(ctx, c.ID)
	if err != nil {
		return dbx.Friendly(err), true
	}
	if maxRows <= 0 || maxRows > dbAgentRows {
		maxRows = dbAgentRows
	}
	start := time.Now()
	res, err := e.Run(ctx, dbx.RunOpts{SQL: sql, MaxRows: maxRows, ReadOnly: true, Who: who, Schema: schema})
	if err != nil {
		s.dbs.Store.AddActivity(c.ID, dbx.Activity{At: time.Now().UTC(), Who: who, SQL: sql, What: "error", Error: err.Error()})
		return dbx.Friendly(err), true
	}
	var n int64
	errText := ""
	for _, r := range res {
		n += int64(len(r.Rows))
		if r.Error != "" {
			errText = r.Error
		}
	}
	what := "read"
	if errText != "" {
		what = "error"
	}
	s.dbs.Store.AddActivity(c.ID, dbx.Activity{At: time.Now().UTC(), Who: who, SQL: sql, Rows: n, What: what, Error: errText})
	text := dbResultText(res, c.Agents.Masked)
	s.dbLiveCall(t, c, sql, res, time.Since(start), errText)
	return text, errText != ""
}

// dbLiveCall shows a SmartDB run's query, with a few of its rows, as it happens.
func (s *Server) dbLiveCall(t remoteTarget, c dbx.Connection, sql string, res []dbx.Result, took time.Duration, errText string) {
	if t.dbRun == "" {
		return
	}
	ev := map[string]any{"type": "dbAgentCall", "run": t.dbRun, "connectionId": c.ID, "sql": sql, "ms": took.Milliseconds(), "error": errText}
	if len(res) > 0 {
		r := res[len(res)-1]
		mask := dbx.Masked(c.Agents.Masked, r.Columns)
		rows := r.Rows
		if len(rows) > 30 {
			rows = rows[:30]
		}
		dbx.MaskRows(mask, rows)
		ev["columns"], ev["rows"], ev["total"], ev["affected"] = r.Columns, rows, len(r.Rows), r.Affected
	}
	s.events.Publish(ev)
}

// ---- writes and the owner's approval ------------------------------------------------------------

type dbRequest struct {
	ID           string    `json:"id"`
	State        string    `json:"state"` // waiting, running, done, denied, expired, failed
	ConnectionID string    `json:"connectionId"`
	ConnName     string    `json:"connection"`
	Driver       string    `json:"driver"`
	Env          string    `json:"env"`
	ProjectID    string    `json:"projectId"`
	WorkspaceID  string    `json:"workspaceId,omitempty"`
	SessionID    string    `json:"sessionId,omitempty"`
	Who          string    `json:"who"`
	SQL          string    `json:"sql"`
	Why          string    `json:"why"`
	Danger       string    `json:"danger,omitempty"`
	Rows         int64     `json:"rows"` // -1: not known
	Redis        bool      `json:"redis,omitempty"`
	Schema       string    `json:"schema,omitempty"`
	DB           int       `json:"db,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Result       string    `json:"result,omitempty"`
	IsError      bool      `json:"isError,omitempty"`
	run          string
	done         chan struct{}
}

type dbRequests struct {
	mu sync.Mutex
	m  map[string]*dbRequest
	// grants: a console's writes to a connection the owner approved for the session
	grants map[string]bool
}

func newDBRequests() *dbRequests {
	return &dbRequests{m: map[string]*dbRequest{}, grants: map[string]bool{}}
}

func (s *Server) dbPublishRequest(r *dbRequest) {
	s.events.Publish(map[string]any{"type": "dbRequest", "id": r.ID, "state": r.State, "connectionId": r.ConnectionID})
}

// dbAgentWrite runs a write of an agent now, or makes a request the owner approves.
func (s *Server) dbAgentWrite(ctx context.Context, t remoteTarget, c dbx.Connection, who, text, why, schema string, redis bool, db int) (string, bool) {
	if c.Security.ReadOnly {
		s.dbs.Store.AddActivity(c.ID, dbx.Activity{At: time.Now().UTC(), Who: who, SQL: text, What: "refused", Note: "read-only connection"})
		return c.Name + " is read-only: the owner has to turn read-only off for it to change", true
	}
	if c.Agents.Access == dbx.AgentsRead {
		s.dbs.Store.AddActivity(c.ID, dbx.Activity{At: time.Now().UTC(), Who: who, SQL: text, What: "refused", Note: "agents may only read"})
		return "agents may only read " + c.Name, true
	}
	var danger string
	var rows int64 = -1
	if redis {
		args, _ := dbx.SplitArgs(text)
		st, _ := dbx.RedisStmt(args)
		danger = st.Danger
	} else {
		stmts := dbx.Split(text, c.Driver)
		if len(stmts) == 0 {
			return "no SQL", true
		}
		for _, st := range stmts {
			if k := dbx.Classify(st, c.Driver); k.Danger != "" {
				danger = k.Danger
				if e, err := s.dbs.SQL(ctx, c.ID); err == nil {
					rows = e.Estimate(ctx, k)
				}
				break
			}
		}
	}
	s.dbReqs.mu.Lock()
	granted := s.dbReqs.grants[t.sessionID+"|"+c.ID]
	s.dbReqs.mu.Unlock()
	needOwner := danger != "" || (c.Agents.Access == dbx.AgentsAsk && !(granted && t.sessionID != ""))
	if !needOwner {
		out, isErr := s.dbExecute(ctx, c, who, text, schema, redis, db, false, t)
		return out, isErr
	}
	if strings.TrimSpace(why) == "" {
		return "say why in one line (the why argument): the owner reads it before approving", true
	}
	waiting := 0
	s.dbReqs.mu.Lock()
	for _, r := range s.dbReqs.m {
		if r.State == "waiting" {
			waiting++
		}
	}
	s.dbReqs.mu.Unlock()
	if waiting >= dbWaitingMax {
		return "too many requests wait for the owner already", true
	}
	now := time.Now().UTC()
	r := &dbRequest{ID: auth.RandomID("dbr_"), State: "waiting", ConnectionID: c.ID, ConnName: c.Name, Driver: string(c.Driver), Env: string(c.Env),
		ProjectID: c.ProjectID, WorkspaceID: t.workspaceID, SessionID: t.sessionID, Who: who, SQL: text, Why: why, Danger: danger, Rows: rows,
		Redis: redis, Schema: schema, DB: db, CreatedAt: now, ExpiresAt: now.Add(dbExpiry), run: t.dbRun, done: make(chan struct{})}
	s.dbReqs.mu.Lock()
	s.dbReqs.m[r.ID] = r
	s.dbReqs.mu.Unlock()
	time.AfterFunc(dbExpiry, func() { s.dbEnd(r.ID, "waiting", "expired", "Nobody approved it in time: it did not run.", true) })
	s.dbPublishRequest(r)
	if s.settings.View().Notifications.NeedsYou {
		body := text
		if utf8.RuneCountInString(body) > 200 {
			body = string([]rune(body)[:200]) + "…"
		}
		a := s.attn.add(Attention{Kind: "database", SessionID: t.sessionID, Handle: strings.TrimPrefix(who, "@"), ProjectID: c.ProjectID, WorkspaceID: t.workspaceID,
			Title: who + " asks to write to " + c.Name, Body: body})
		s.events.Publish(map[string]any{"type": "attention", "attention": a})
	}
	return s.dbAwait(ctx, r.ID, t.sessionID)
}

// dbExecute runs an approved (or allowed) write and records it.
func (s *Server) dbExecute(ctx context.Context, c dbx.Connection, who, text, schema string, redis bool, db int, approved bool, t remoteTarget) (string, bool) {
	what := "ran"
	if approved {
		what = "approved"
	}
	start := time.Now()
	if redis {
		e, err := s.dbs.KV(ctx, c.ID)
		if err != nil {
			return dbx.Friendly(err), true
		}
		out, err := e.Command(ctx, dbx.CommandOpts{DB: db, Line: text, NoConfirm: true, ConfirmName: c.Name})
		if err != nil {
			s.dbs.Store.AddActivity(c.ID, dbx.Activity{At: time.Now().UTC(), Who: who, SQL: text, What: "error", Error: err.Error()})
			return dbx.Friendly(err), true
		}
		s.dbs.Store.AddActivity(c.ID, dbx.Activity{At: time.Now().UTC(), Who: who, SQL: text, What: what})
		return dbClip(out), false
	}
	e, err := s.dbs.SQL(ctx, c.ID)
	if err != nil {
		return dbx.Friendly(err), true
	}
	sid := "agent_" + auth.RandomID("")
	res, err := e.Run(ctx, dbx.RunOpts{Session: sid, SQL: text, NoConfirm: true, ConfirmName: c.Name, MaxRows: dbAgentRows, Who: who, Schema: schema})
	if err != nil {
		s.dbs.Store.AddActivity(c.ID, dbx.Activity{At: time.Now().UTC(), Who: who, SQL: text, What: "error", Error: err.Error()})
		return dbx.Friendly(err), true
	}
	var n int64
	errText := ""
	for _, r := range res {
		n += r.Affected
		if r.Error != "" {
			errText = r.Error
		}
		if r.Pending {
			// the owner approved it with its rows counted: keep it
			_ = e.Finish(sid, true)
		}
	}
	e.Release(sid)
	if errText != "" {
		what = "error"
	}
	s.dbs.Store.AddActivity(c.ID, dbx.Activity{At: time.Now().UTC(), Who: who, SQL: text, Rows: n, What: what, Error: errText})
	s.dbLiveCall(t, c, text, res, time.Since(start), errText)
	return dbResultText(res, c.Agents.Masked), errText != ""
}

// dbAwait waits a while for a request's outcome; still waiting, it tells the agent to ask again.
func (s *Server) dbAwait(ctx context.Context, id, sessionID string) (string, bool) {
	s.dbReqs.mu.Lock()
	r, ok := s.dbReqs.m[id]
	s.dbReqs.mu.Unlock()
	if !ok || (sessionID != "" && r.SessionID != "" && r.SessionID != sessionID) {
		return "no such request", true
	}
	timer := time.NewTimer(dbWait)
	defer timer.Stop()
	select {
	case <-r.done:
	case <-timer.C:
	case <-ctx.Done():
	}
	s.dbReqs.mu.Lock()
	defer s.dbReqs.mu.Unlock()
	switch r.State {
	case "waiting":
		return fmt.Sprintf("Waiting for the owner to approve it in NextBoard (request %s). Call db_result with this request to get the outcome; do something else meanwhile if you can.", r.ID), false
	case "running":
		return fmt.Sprintf("Approved, running (request %s): call db_result again.", r.ID), false
	}
	return r.Result, r.IsError
}

// dbEnd puts a request in its last state, once.
func (s *Server) dbEnd(id, from, state, result string, isError bool) bool {
	s.dbReqs.mu.Lock()
	r, ok := s.dbReqs.m[id]
	if !ok || r.State != from {
		s.dbReqs.mu.Unlock()
		return false
	}
	r.State, r.Result, r.IsError = state, result, isError
	close(r.done)
	s.dbReqs.mu.Unlock()
	time.AfterFunc(dbKeep, func() {
		s.dbReqs.mu.Lock()
		delete(s.dbReqs.m, id)
		s.dbReqs.mu.Unlock()
	})
	if state == "expired" || state == "denied" {
		s.dbs.Store.AddActivity(r.ConnectionID, dbx.Activity{At: time.Now().UTC(), Who: r.Who, SQL: r.SQL, What: state, Note: result})
	}
	s.dbPublishRequest(r)
	return true
}

func (s *Server) handleDBRequests(w http.ResponseWriter, r *http.Request, _ *principal) {
	s.dbReqs.mu.Lock()
	list := make([]dbRequest, 0, len(s.dbReqs.m))
	for _, x := range s.dbReqs.m {
		list = append(list, *x)
	}
	s.dbReqs.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *Server) handleDBRequestApprove(w http.ResponseWriter, r *http.Request, _ *principal) {
	var b struct {
		Session     bool   `json:"session"`
		ConfirmName string `json:"confirmName"`
	}
	if !decode(w, r, &b) {
		return
	}
	id := r.PathValue("id")
	s.dbReqs.mu.Lock()
	req, ok := s.dbReqs.m[id]
	var state string
	if ok {
		state = req.State
	}
	s.dbReqs.mu.Unlock()
	if !ok {
		writeErrCode(w, http.StatusNotFound, "request_not_found", "no such request")
		return
	}
	if state != "waiting" {
		writeErrCode(w, http.StatusConflict, "request_decided", "already "+state)
		return
	}
	c, ok := s.dbs.Store.Get(req.ConnectionID)
	if !ok {
		s.dbEnd(id, "waiting", "failed", "the connection is gone", true)
		writeErrCode(w, http.StatusNotFound, "not_found", "the connection is gone")
		return
	}
	if req.Danger != "" && b.ConfirmName != c.Name {
		writeErrCode(w, http.StatusConflict, "confirm_name", "type the connection's name to approve "+req.Danger)
		return
	}
	s.dbReqs.mu.Lock()
	if req.State != "waiting" {
		s.dbReqs.mu.Unlock()
		writeErrCode(w, http.StatusConflict, "request_decided", "already "+req.State)
		return
	}
	req.State = "running"
	if b.Session && req.Danger == "" && req.SessionID != "" {
		s.dbReqs.grants[req.SessionID+"|"+req.ConnectionID] = true
	}
	s.dbReqs.mu.Unlock()
	s.dbPublishRequest(req)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(c.Limits.TimeoutSec+30)*time.Second)
		defer cancel()
		out, isErr := s.dbExecute(ctx, c, req.Who, req.SQL, req.Schema, req.Redis, req.DB, true, remoteTarget{dbRun: req.run})
		state := "done"
		if isErr {
			state = "failed"
		}
		s.dbEnd(id, "running", state, out, isErr)
	}()
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handleDBRequestDeny(w http.ResponseWriter, r *http.Request, _ *principal) {
	var b struct {
		Note string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	msg := "The owner said no: it did not run."
	if n := strings.TrimSpace(b.Note); n != "" {
		msg += " Their note: " + n
	}
	if !s.dbEnd(r.PathValue("id"), "waiting", "denied", msg, true) {
		writeErrCode(w, http.StatusConflict, "request_decided", "not waiting")
		return
	}
	writeJSON(w, http.StatusOK, okBody)
}
