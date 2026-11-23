package dbx

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// DialFunc opens a TCP connection to addr the way the connection reaches its database.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// ConfirmError: the statements need the owner's yes before they run (Code "confirm"), or the
// connection's name typed (Code "confirm_name": DROP, TRUNCATE, changes without WHERE).
type ConfirmError struct {
	Code  string `json:"code"`
	Stmts []Stmt `json:"statements"`
	// Rows: how many rows each dangerous statement would touch (-1: not known), by index in Stmts
	Rows []int64 `json:"rows,omitempty"`
}

func (e *ConfirmError) Error() string {
	if e.Code == "confirm_name" {
		return "type the connection's name to run " + e.Stmts[0].Verb
	}
	return "confirm the statements that change data"
}

// ReadOnlyError: the connection (or the agent's access) is read-only.
type ReadOnlyError struct{ Stmt Stmt }

func (e *ReadOnlyError) Error() string {
	return "this connection is read-only: " + strings.ToUpper(e.Stmt.Verb) + " changes data. Turn read-only off in the connection's Security to run it"
}

// ErrPending: a statement waits for Commit or Roll back in this session.
var ErrPending = errors.New("a statement waits for Commit or Roll back first")

// RunOpts is one Run of a script.
type RunOpts struct {
	Session     string `json:"session"`
	SQL         string `json:"sql"`
	Confirmed   bool   `json:"confirmed"`
	ConfirmName string `json:"confirmName"`
	MaxRows     int    `json:"maxRows"`
	Schema      string `json:"schema"`
	// ReadOnly makes this run read-only whatever the connection allows (an agent's read access).
	ReadOnly bool `json:"-"`
	// Who ran it, for the history: "" the owner.
	Who string `json:"-"`
	// NoConfirm skips the "confirm writes" step (an agent's write the owner approved, or read and
	// write access); the typed name of dangerous statements is never skipped this way.
	NoConfirm bool `json:"-"`
}

type sqlSession struct {
	mu      sync.Mutex
	conn    *sql.Conn
	tx      *sql.Tx
	pending bool
	thread  int64
	schema  string
	used    time.Time

	cmu    sync.Mutex
	cancel context.CancelFunc
}

type sqlEngine struct {
	c   Connection
	db  *sql.DB
	dl  dialect
	ver string

	mu       sync.Mutex
	sessions map[string]*sqlSession
	tables   map[string]tableCache
	store    *Store
}

type tableCache struct {
	t  *TableInfo
	at time.Time
}

const maxSessions = 32

func mysqlTLS(mode string) string {
	switch mode {
	case TLSPreferred:
		return "preferred"
	case TLSRequired:
		return "skip-verify"
	case TLSVerifyCA:
		return "true"
	}
	return ""
}

func pgSSLMode(mode string) string {
	switch mode {
	case TLSPreferred:
		return "prefer"
	case TLSRequired:
		return "require"
	case TLSVerifyCA:
		return "verify-ca"
	}
	return "disable"
}

// openSQL opens a pool to the database through dial. Nothing is sent until ping.
func openSQL(c Connection, password string, dial DialFunc, timeout time.Duration) (*sql.DB, error) {
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	switch c.Driver {
	case MySQL, MariaDB:
		name := "nb-" + c.ID
		if c.ID == "" {
			name = "nb-test-" + newID("")
		}
		mysql.RegisterDialContext(name, func(ctx context.Context, a string) (net.Conn, error) { return dial(ctx, "tcp", a) })
		cfg := mysql.NewConfig()
		cfg.User, cfg.Passwd, cfg.Net, cfg.Addr, cfg.DBName = c.User, password, name, addr, c.Database
		cfg.Timeout = timeout
		cfg.AllowNativePasswords = true
		cfg.Loc = time.UTC
		cfg.TLSConfig = mysqlTLS(c.TLS)
		cfg.AllowCleartextPasswords = c.TLS == TLSRequired || c.TLS == TLSVerifyCA
		if cfg.TLSConfig == "true" {
			cfg.TLS = &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12}
			cfg.TLSConfig = ""
		}
		cfg.Params = map[string]string{}
		for k, v := range c.Props {
			switch strings.ToLower(k) {
			case "charset", "collation", "time_zone", "sql_mode", "wait_timeout", "max_execution_time", "net_read_timeout", "net_write_timeout", "character_set_results":
				if k == "collation" {
					cfg.Collation = v
				} else {
					cfg.Params[k] = v
				}
			}
		}
		conn, err := mysql.NewConnector(cfg)
		if err != nil {
			return nil, err
		}
		return sql.OpenDB(conn), nil
	case Postgres:
		u := url.URL{Scheme: "postgres", User: url.UserPassword(c.User, password), Host: addr, Path: "/" + orDefault(c.Database, "postgres")}
		q := url.Values{}
		q.Set("sslmode", pgSSLMode(c.TLS))
		q.Set("connect_timeout", strconv.Itoa(int(timeout.Seconds())))
		for k, v := range c.Props {
			switch strings.ToLower(k) {
			case "search_path", "timezone", "statement_timeout", "client_encoding", "lock_timeout", "idle_in_transaction_session_timeout":
				q.Set(k, v)
			}
		}
		u.RawQuery = q.Encode()
		pc, err := pgx.ParseConfig(u.String())
		if err != nil {
			return nil, err
		}
		pc.DialFunc = func(ctx context.Context, network, a string) (net.Conn, error) { return dial(ctx, "tcp", a) }
		// the name is resolved where the database is (the far end of a tunnel), not here
		pc.LookupFunc = func(ctx context.Context, host string) ([]string, error) { return []string{host}, nil }
		pc.RuntimeParams["application_name"] = "NextBoard"
		if pc.TLSConfig != nil {
			pc.TLSConfig.ServerName = c.Host
		}
		return stdlib.OpenDB(*pc), nil
	}
	return nil, fmt.Errorf("%w: %s is not an SQL driver", ErrInvalid, c.Driver)
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func newSQLEngine(ctx context.Context, c Connection, password string, dial DialFunc, st *Store) (*sqlEngine, error) {
	timeout := time.Duration(c.Reach.TimeoutSec) * time.Second
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	db, err := openSQL(c, password, dial, timeout)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(maxSessions + 4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)
	e := &sqlEngine{c: c, db: db, dl: dialectFor(c.Driver), sessions: map[string]*sqlSession{}, tables: map[string]tableCache{}, store: st}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := db.PingContext(pctx); err != nil {
		db.Close()
		return nil, err
	}
	if v, err := e.dl.version(pctx, db); err == nil {
		e.ver = v
	}
	return e, nil
}

func (e *sqlEngine) close() {
	e.mu.Lock()
	for id, s := range e.sessions {
		s.closeLocked()
		delete(e.sessions, id)
	}
	e.mu.Unlock()
	e.db.Close()
}

func (s *sqlSession) closeLocked() {
	if s.tx != nil {
		_ = s.tx.Rollback()
		s.tx = nil
	}
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
}

// reap closes the sessions of editor tabs unused for idle.
func (e *sqlEngine) reap(idle time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, s := range e.sessions {
		if s.mu.TryLock() {
			if time.Since(s.used) > idle {
				s.closeLocked()
				delete(e.sessions, id)
			}
			s.mu.Unlock()
		}
	}
}

func (e *sqlEngine) session(id string) (*sqlSession, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if s, ok := e.sessions[id]; ok {
		return s, true
	}
	if len(e.sessions) >= maxSessions {
		// drop the least recently used idle session
		var oldID string
		var old *sqlSession
		for k, s := range e.sessions {
			if !s.pending && s.tx == nil && (old == nil || s.used.Before(old.used)) {
				oldID, old = k, s
			}
		}
		if old != nil && old.mu.TryLock() {
			old.closeLocked()
			old.mu.Unlock()
			delete(e.sessions, oldID)
		}
	}
	s := &sqlSession{used: time.Now()}
	e.sessions[id] = s
	return s, false
}

// ensure opens the session's own connection and points it at schema.
func (e *sqlEngine) ensure(ctx context.Context, s *sqlSession, schema string) error {
	if s.conn == nil {
		c, err := e.db.Conn(ctx)
		if err != nil {
			return err
		}
		s.conn = c
		s.thread = e.dl.threadID(ctx, c)
		s.schema = ""
	}
	if schema != "" && schema != s.schema && s.tx == nil {
		if _, err := s.conn.ExecContext(ctx, e.dl.useSQL(schema)); err != nil {
			return err
		}
		s.schema = schema
	}
	return nil
}

// kill stops what runs on a session's connection on the server (MySQL keeps running a query
// whose client went away).
func (e *sqlEngine) kill(s *sqlSession) {
	if q := e.dl.killSQL(s.thread); q != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = e.db.ExecContext(ctx, q)
	}
}

// Cancel stops the statement running in a session.
func (e *sqlEngine) Cancel(sessionID string) bool {
	e.mu.Lock()
	s := e.sessions[sessionID]
	e.mu.Unlock()
	if s == nil {
		return false
	}
	s.cmu.Lock()
	cancel := s.cancel
	s.cmu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	e.kill(s)
	return true
}

// Finish commits or rolls back what a session holds: a manual transaction or a pending
// dangerous statement.
func (e *sqlEngine) Finish(sessionID string, commit bool) error {
	e.mu.Lock()
	s := e.sessions[sessionID]
	e.mu.Unlock()
	if s == nil {
		return errors.New("no such session")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tx == nil {
		return errors.New("nothing to commit or roll back")
	}
	var err error
	if commit {
		err = s.tx.Commit()
	} else {
		err = s.tx.Rollback()
	}
	s.tx, s.pending = nil, false
	return err
}

// estimate counts the rows a dangerous statement would touch, quickly or not at all.
func (e *sqlEngine) estimate(ctx context.Context, st Stmt) int64 {
	if st.Table == "" {
		return -1
	}
	parts := strings.Split(st.Table, ".")
	for i, p := range parts {
		parts[i] = e.dl.quote(strings.Trim(p, "`\""))
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var n int64
	if err := e.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+strings.Join(parts, ".")).Scan(&n); err != nil {
		return -1
	}
	return n
}

// check applies the connection's safety to a script before anything runs.
func (e *sqlEngine) check(ctx context.Context, stmts []Stmt, o RunOpts) error {
	ro := e.c.Security.ReadOnly || o.ReadOnly
	var writes, danger []Stmt
	for _, st := range stmts {
		if ro && st.Writes() {
			return &ReadOnlyError{Stmt: st}
		}
		if st.Writes() {
			writes = append(writes, st)
		}
		if st.Danger != "" {
			danger = append(danger, st)
		}
	}
	if len(danger) > 0 && o.ConfirmName != e.c.Name {
		ce := &ConfirmError{Code: "confirm_name", Stmts: danger}
		for _, st := range danger {
			ce.Rows = append(ce.Rows, e.estimate(ctx, st))
		}
		return ce
	}
	if len(writes) > 0 && len(danger) == 0 && e.c.Security.ConfirmWrites && !o.Confirmed && !o.NoConfirm {
		return &ConfirmError{Code: "confirm", Stmts: writes}
	}
	return nil
}

// Run runs a script, statement by statement, in the session (an editor tab) or alone.
func (e *sqlEngine) Run(ctx context.Context, o RunOpts) ([]Result, error) {
	scripts := Split(o.SQL, e.c.Driver)
	if len(scripts) == 0 {
		return nil, invalid("nothing to run")
	}
	if len(o.SQL) > maxSQLLength {
		return nil, invalid("at most 256 KB of SQL")
	}
	stmts := make([]Stmt, len(scripts))
	for i, s := range scripts {
		stmts[i] = Classify(s, e.c.Driver)
	}
	if err := e.check(ctx, stmts, o); err != nil {
		return nil, err
	}
	if o.MaxRows <= 0 || o.MaxRows > 10000 {
		o.MaxRows = 1000
	}
	sid := o.Session
	temp := sid == ""
	if temp {
		sid = newID("tmp_")
	}
	s, _ := e.session(sid)
	s.mu.Lock()
	defer func() {
		s.used = time.Now()
		if temp && s.tx == nil {
			s.closeLocked()
			e.mu.Lock()
			delete(e.sessions, sid)
			e.mu.Unlock()
		}
		s.mu.Unlock()
	}()
	if s.pending {
		return nil, ErrPending
	}
	if err := e.ensure(ctx, s, o.Schema); err != nil {
		return nil, err
	}
	ro := e.c.Security.ReadOnly || o.ReadOnly
	var out []Result
	for i, st := range stmts {
		r := e.runOne(ctx, s, st, o.MaxRows, ro)
		if e.store != nil {
			e.store.AddHistory(e.c.ID, HistoryItem{At: time.Now().UTC(), SQL: st.SQL, Kind: st.Kind, Rows: max(r.Affected, int64(len(r.Rows))), MS: r.MS, Error: r.Error, Who: o.Who})
		}
		out = append(out, r)
		if r.Error != "" || r.Pending {
			if r.Pending && i < len(stmts)-1 {
				out[len(out)-1].Notice = fmt.Sprintf("%d more statement(s) wait until this one is committed or rolled back", len(stmts)-1-i)
			}
			break
		}
	}
	return out, nil
}

func (e *sqlEngine) runOne(ctx context.Context, s *sqlSession, st Stmt, maxRows int, ro bool) Result {
	r := Result{SQL: st.SQL, Kind: st.Kind}
	start := time.Now()
	defer func() { r.MS = time.Since(start).Milliseconds() }()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(e.c.Limits.TimeoutSec)*time.Second)
	s.cmu.Lock()
	s.cancel = cancel
	s.cmu.Unlock()
	defer func() {
		s.cmu.Lock()
		s.cancel = nil
		s.cmu.Unlock()
		cancel()
	}()
	fail := func(err error) Result {
		if ctx.Err() != nil {
			e.kill(s)
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				err = fmt.Errorf("stopped after %d s (the connection's limit)", e.c.Limits.TimeoutSec)
			} else {
				err = errors.New("stopped")
			}
		}
		r.Error = err.Error()
		return r
	}
	if st.Kind == KindTx {
		var err error
		switch st.Verb {
		case "BEGIN", "START":
			if s.tx == nil {
				// a transaction lives as long as the session, not the statement that opened it
				s.tx, err = s.conn.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: ro})
				r.Notice = "transaction open: Commit or Roll back when done"
			}
		case "COMMIT", "END":
			if s.tx != nil {
				err = s.tx.Commit()
				s.tx, s.pending = nil, false
			}
		case "ROLLBACK", "ABORT":
			if s.tx != nil {
				err = s.tx.Rollback()
				s.tx, s.pending = nil, false
			}
		default: // SAVEPOINT, RELEASE
			if s.tx == nil {
				err = errors.New(st.Verb + " needs an open transaction")
			} else {
				_, err = s.tx.ExecContext(ctx, st.SQL)
			}
		}
		if err != nil {
			return fail(err)
		}
		return r
	}
	type q interface {
		QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
		ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	}
	var target q = s.conn
	var own *sql.Tx
	switch {
	case s.tx != nil:
		target = s.tx
	case ro || st.Kind == KindRead && e.c.Security.ReadOnly:
		// a read-only transaction: the server refuses a write the classifier missed
		t, err := s.conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return fail(err)
		}
		own, target = t, t
		defer func() { _ = own.Rollback() }()
	case st.Danger != "":
		// it runs in a transaction that waits for the owner's Commit or Roll back
		t, err := s.conn.BeginTx(context.Background(), nil)
		if err != nil {
			return fail(err)
		}
		s.tx, target = t, t
		s.pending, r.Pending = true, true
	}
	returns := st.Kind == KindRead || st.Kind == KindSession && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(st.SQL)), "SHOW") ||
		st.Verb == "CALL" || st.Verb == "EXECUTE" || strings.Contains(strings.ToUpper(st.SQL), "RETURNING")
	if returns {
		rows, err := target.QueryContext(ctx, st.SQL)
		if err != nil {
			return fail(err)
		}
		defer rows.Close()
		cols, data, trunc, err := readRows(rows, maxRows)
		if err != nil {
			return fail(err)
		}
		r.Columns, r.Rows, r.Truncated = cols, data, trunc
		if st.Kind != KindRead {
			r.Affected = int64(len(data))
		}
		if st.Kind == KindSession && st.Verb == "USE" {
			s.schema = ""
		}
		return r
	}
	res, err := target.ExecContext(ctx, st.SQL)
	if err != nil {
		if r.Pending {
			_ = s.tx.Rollback()
			s.tx, s.pending, r.Pending = nil, false, false
		}
		return fail(err)
	}
	if n, err := res.RowsAffected(); err == nil {
		r.Affected = n
	}
	if st.Kind == KindSession && st.Verb == "USE" {
		s.schema = strings.Trim(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(st.SQL)[3:], " ")), "`\";")
	}
	if r.Pending {
		r.Notice = fmt.Sprintf("%d row(s) changed inside a transaction: Commit to keep them, Roll back to undo", r.Affected)
		if e.c.Driver != Postgres && st.Kind == KindDDL {
			r.Notice = "MySQL commits DROP, TRUNCATE and ALTER at once: this cannot be rolled back"
			_ = s.tx.Commit()
			s.tx, s.pending, r.Pending = nil, false, false
		}
	}
	return r
}

// ---- schema and tables -------------------------------------------------------------------------

func (e *sqlEngine) Schema(ctx context.Context, current string) (*Schema, error) {
	return e.dl.schema(ctx, e.db, e.c, current)
}

func (e *sqlEngine) Table(ctx context.Context, schema, name string) (*TableInfo, error) {
	key := schema + "\x00" + name
	e.mu.Lock()
	tc, ok := e.tables[key]
	e.mu.Unlock()
	if ok && time.Since(tc.at) < 20*time.Second {
		return tc.t, nil
	}
	t, err := e.dl.table(ctx, e.db, schema, name)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.tables[key] = tableCache{t: t, at: time.Now()}
	e.mu.Unlock()
	return t, nil
}

// Forget drops the cached structure of tables (after a statement changed the schema).
func (e *sqlEngine) Forget() {
	e.mu.Lock()
	e.tables = map[string]tableCache{}
	e.mu.Unlock()
}

// Filter is one condition of a table view.
type Filter struct {
	Column string   `json:"column"`
	Op     string   `json:"op"`
	Value  string   `json:"value,omitempty"`
	Values []string `json:"values,omitempty"`
}

// Sort orders a table view.
type Sort struct {
	Column string `json:"column"`
	Desc   bool   `json:"desc,omitempty"`
}

// RowsRequest asks for a page of a table.
type RowsRequest struct {
	Schema  string   `json:"schema"`
	Table   string   `json:"table"`
	Filters []Filter `json:"filters,omitempty"`
	Where   string   `json:"where,omitempty"`
	Sort    []Sort   `json:"sort,omitempty"`
	Offset  int      `json:"offset,omitempty"`
	Limit   int      `json:"limit,omitempty"`
}

// RowsPage is a page of a table.
type RowsPage struct {
	Columns []ResultColumn `json:"columns"`
	Rows    [][]any        `json:"rows"`
	Total   int64          `json:"total"` // -1: not counted in time
	MS      int64          `json:"ms"`
	SQL     string         `json:"sql"`
}

// buildWhere writes the WHERE of a table view with parameters; columns must exist.
func buildWhere(dl dialect, t *TableInfo, fs []Filter, raw string) (string, []any, error) {
	known := map[string]bool{}
	for _, c := range t.Columns {
		known[c.Name] = true
	}
	var parts []string
	var args []any
	p := func(v any) string {
		args = append(args, v)
		return dl.param(len(args))
	}
	for _, f := range fs {
		if !known[f.Column] {
			return "", nil, invalid("no column %q in %s", f.Column, t.Name)
		}
		col := dl.quote(f.Column)
		switch strings.ToLower(f.Op) {
		case "=", "!=", "<", "<=", ">", ">=":
			op := f.Op
			if op == "!=" {
				op = "<>"
			}
			parts = append(parts, col+" "+op+" "+p(f.Value))
		case "like":
			parts = append(parts, col+" LIKE "+p(f.Value))
		case "not like":
			parts = append(parts, col+" NOT LIKE "+p(f.Value))
		case "contains":
			parts = append(parts, castText(dl, col)+" LIKE "+p("%"+likeEscape(f.Value)+"%"))
		case "starts":
			parts = append(parts, castText(dl, col)+" LIKE "+p(likeEscape(f.Value)+"%"))
		case "ends":
			parts = append(parts, castText(dl, col)+" LIKE "+p("%"+likeEscape(f.Value)))
		case "in", "not in":
			if len(f.Values) == 0 {
				return "", nil, invalid("%s %s: no values", f.Column, f.Op)
			}
			var ph []string
			for _, v := range f.Values {
				ph = append(ph, p(v))
			}
			not := ""
			if strings.ToLower(f.Op) == "not in" {
				not = "NOT "
			}
			parts = append(parts, col+" "+not+"IN ("+strings.Join(ph, ", ")+")")
		case "null":
			parts = append(parts, col+" IS NULL")
		case "not null":
			parts = append(parts, col+" IS NOT NULL")
		case "between":
			if len(f.Values) != 2 {
				return "", nil, invalid("%s between: two values", f.Column)
			}
			parts = append(parts, col+" BETWEEN "+p(f.Values[0])+" AND "+p(f.Values[1]))
		default:
			return "", nil, invalid("filter operator %q", f.Op)
		}
	}
	if raw = strings.TrimSpace(raw); raw != "" {
		if strings.Contains(raw, ";") {
			return "", nil, invalid("WHERE: one condition, no ;")
		}
		parts = append(parts, "("+raw+")")
	}
	if len(parts) == 0 {
		return "", nil, nil
	}
	return " WHERE " + strings.Join(parts, " AND "), args, nil
}

func castText(dl dialect, col string) string {
	if _, ok := dl.(pgDialect); ok {
		return col + "::text"
	}
	return col
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// Rows gives a page of a table, filtered and sorted, in a read-only transaction.
func (e *sqlEngine) Rows(ctx context.Context, rq RowsRequest) (*RowsPage, error) {
	t, err := e.Table(ctx, rq.Schema, rq.Table)
	if err != nil {
		return nil, err
	}
	where, args, err := buildWhere(e.dl, t, rq.Filters, rq.Where)
	if err != nil {
		return nil, err
	}
	order := ""
	var by []string
	for _, s := range rq.Sort {
		ok := false
		for _, c := range t.Columns {
			ok = ok || c.Name == s.Column
		}
		if !ok {
			return nil, invalid("no column %q to sort by", s.Column)
		}
		d := ""
		if s.Desc {
			d = " DESC"
		}
		by = append(by, e.dl.quote(s.Column)+d)
	}
	if len(by) > 0 {
		order = " ORDER BY " + strings.Join(by, ", ")
	}
	if rq.Limit <= 0 || rq.Limit > 5000 {
		rq.Limit = e.c.Limits.PageSize
	}
	if rq.Offset < 0 {
		rq.Offset = 0
	}
	from := qualified(e.dl, rq.Schema, rq.Table)
	q := "SELECT * FROM " + from + where + order + e.dl.limit(rq.Limit, rq.Offset)
	ctx, cancel := context.WithTimeout(ctx, time.Duration(e.c.Limits.TimeoutSec)*time.Second)
	defer cancel()
	start := time.Now()
	tx, err := e.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	cols, data, _, err := readRows(rows, rq.Limit)
	rows.Close()
	if err != nil {
		return nil, err
	}
	page := &RowsPage{Columns: cols, Rows: data, Total: -1, MS: time.Since(start).Milliseconds(), SQL: q}
	if page.Rows == nil {
		page.Rows = [][]any{}
	}
	cctx, ccancel := context.WithTimeout(ctx, 2*time.Second)
	defer ccancel()
	var n int64
	if err := tx.QueryRowContext(cctx, "SELECT COUNT(*) FROM "+from+where, args...).Scan(&n); err == nil {
		page.Total = n
	}
	return page, nil
}

// ---- edits from the grid -------------------------------------------------------------------------

// Change is one edit of a row: update (Key → Set), insert (Set) or delete (Key).
type Change struct {
	Op  string         `json:"op"`
	Key map[string]any `json:"key,omitempty"`
	Set map[string]any `json:"set,omitempty"`
}

// ApplyRequest is a set of edits of one table, run in one transaction.
type ApplyRequest struct {
	Schema    string   `json:"schema"`
	Table     string   `json:"table"`
	Changes   []Change `json:"changes"`
	Preview   bool     `json:"preview"`
	Confirmed bool     `json:"confirmed"`
}

// ApplyResult says what the edits did, or would do.
type ApplyResult struct {
	SQL      []string `json:"sql"`
	Affected []int64  `json:"affected,omitempty"`
}

type built struct {
	sql  string
	args []any
	show string
}

func (e *sqlEngine) buildChanges(t *TableInfo, schema string, changes []Change) ([]built, error) {
	known := map[string]Column{}
	for _, c := range t.Columns {
		known[c.Name] = c
	}
	key := t.PrimaryKey
	if len(key) == 0 {
		for _, ix := range t.Indexes {
			if ix.Unique {
				key = ix.Columns
				break
			}
		}
	}
	from := qualified(e.dl, schema, t.Name)
	var out []built
	for i, ch := range changes {
		var b built
		args := func(v any) string {
			b.args = append(b.args, v)
			return e.dl.param(len(b.args))
		}
		cols := func(m map[string]any) ([]string, error) {
			var names []string
			for k := range m {
				if _, ok := known[k]; !ok {
					return nil, invalid("change %d: no column %q", i, k)
				}
				names = append(names, k)
			}
			sort.Strings(names)
			return names, nil
		}
		where := func() (string, string, error) {
			if len(key) == 0 {
				return "", "", invalid("%s has no primary or unique key: change its rows with SQL", t.Name)
			}
			var p, shown []string
			for _, k := range key {
				v, ok := ch.Key[k]
				if !ok {
					return "", "", invalid("change %d: the key needs %q", i, k)
				}
				if v == nil {
					p = append(p, e.dl.quote(k)+" IS NULL")
					shown = append(shown, e.dl.quote(k)+" IS NULL")
					continue
				}
				p = append(p, e.dl.quote(k)+" = "+args(v))
				shown = append(shown, e.dl.quote(k)+" = "+literal(e.c.Driver, v))
			}
			return " WHERE " + strings.Join(p, " AND "), " WHERE " + strings.Join(shown, " AND "), nil
		}
		switch ch.Op {
		case "update":
			names, err := cols(ch.Set)
			if err != nil {
				return nil, err
			}
			if len(names) == 0 {
				continue
			}
			var set, shown []string
			for _, n := range names {
				set = append(set, e.dl.quote(n)+" = "+args(ch.Set[n]))
				shown = append(shown, e.dl.quote(n)+" = "+literal(e.c.Driver, ch.Set[n]))
			}
			w, ws, err := where()
			if err != nil {
				return nil, err
			}
			b.sql = "UPDATE " + from + " SET " + strings.Join(set, ", ") + w
			b.show = "UPDATE " + from + " SET " + strings.Join(shown, ", ") + ws
		case "delete":
			w, ws, err := where()
			if err != nil {
				return nil, err
			}
			b.sql, b.show = "DELETE FROM "+from+w, "DELETE FROM "+from+ws
		case "insert":
			names, err := cols(ch.Set)
			if err != nil {
				return nil, err
			}
			var q, ph, shown []string
			for _, n := range names {
				q = append(q, e.dl.quote(n))
				ph = append(ph, args(ch.Set[n]))
				shown = append(shown, literal(e.c.Driver, ch.Set[n]))
			}
			if len(names) == 0 {
				b.sql = "INSERT INTO " + from + " DEFAULT VALUES"
				if e.c.Driver != Postgres {
					b.sql = "INSERT INTO " + from + " () VALUES ()"
				}
				b.show = b.sql
			} else {
				b.sql = "INSERT INTO " + from + " (" + strings.Join(q, ", ") + ") VALUES (" + strings.Join(ph, ", ") + ")"
				b.show = "INSERT INTO " + from + " (" + strings.Join(q, ", ") + ") VALUES (" + strings.Join(shown, ", ") + ")"
			}
		default:
			return nil, invalid("change %d: op update, insert or delete", i)
		}
		out = append(out, b)
	}
	return out, nil
}

// literal writes a value as SQL, for showing the statements (never to run them).
func literal(d Driver, v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case bool:
		if x {
			return "TRUE"
		}
		return "FALSE"
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case string:
		s := strings.ReplaceAll(x, "'", "''")
		if d != Postgres {
			s = strings.ReplaceAll(s, `\`, `\\`)
		}
		return "'" + s + "'"
	}
	return fmt.Sprint(v)
}

// Apply runs grid edits in one transaction; an UPDATE or DELETE that would touch other than one
// row rolls all of it back.
func (e *sqlEngine) Apply(ctx context.Context, rq ApplyRequest) (*ApplyResult, error) {
	if e.c.Security.ReadOnly && !rq.Preview {
		return nil, &ReadOnlyError{Stmt: Stmt{Verb: "UPDATE", Kind: KindWrite}}
	}
	t, err := e.Table(ctx, rq.Schema, rq.Table)
	if err != nil {
		return nil, err
	}
	bs, err := e.buildChanges(t, rq.Schema, rq.Changes)
	if err != nil {
		return nil, err
	}
	out := &ApplyResult{}
	for _, b := range bs {
		out.SQL = append(out.SQL, b.show)
	}
	if rq.Preview || len(bs) == 0 {
		return out, nil
	}
	if e.c.Security.ConfirmEdits && !rq.Confirmed {
		return nil, &ConfirmError{Code: "confirm", Stmts: []Stmt{{SQL: strings.Join(out.SQL, ";\n"), Kind: KindWrite, Verb: "UPDATE"}}}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(e.c.Limits.TimeoutSec)*time.Second)
	defer cancel()
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	for i, b := range bs {
		res, err := tx.ExecContext(ctx, b.sql, b.args...)
		if err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("%s: %w", out.SQL[i], err)
		}
		n, _ := res.RowsAffected()
		if !strings.HasPrefix(b.sql, "INSERT") && n != 1 {
			_ = tx.Rollback()
			return nil, fmt.Errorf("%s would change %d rows, not 1: nothing was changed", out.SQL[i], n)
		}
		out.Affected = append(out.Affected, n)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if e.store != nil {
		e.store.AddHistory(e.c.ID, HistoryItem{At: time.Now().UTC(), SQL: strings.Join(out.SQL, ";\n"), Kind: KindWrite, Rows: int64(len(bs))})
	}
	return out, nil
}

// Version is the server's name and version ("MySQL 8.0.36").
func (e *sqlEngine) Version() string { return e.ver }

// Estimate counts the rows a dangerous statement would touch (-1: not known in time).
func (e *sqlEngine) Estimate(ctx context.Context, st Stmt) int64 { return e.estimate(ctx, st) }

// Release closes a session that holds nothing (an agent's one-off run).
func (e *sqlEngine) Release(sessionID string) {
	e.mu.Lock()
	s := e.sessions[sessionID]
	e.mu.Unlock()
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tx != nil {
		return
	}
	s.closeLocked()
	e.mu.Lock()
	delete(e.sessions, sessionID)
	e.mu.Unlock()
}
