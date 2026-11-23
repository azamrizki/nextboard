package dbx

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Schema is what the side panel lists of a connection.
type Schema struct {
	// Databases (MySQL) or schemas (PostgreSQL) the tree shows; Current is the one open by default.
	Databases []string `json:"databases"`
	Current   string   `json:"current"`
	Tables    []Object `json:"tables"`
	Views     []Object `json:"views"`
	Routines  []Object `json:"routines"`
}

// Object is a table, a view or a routine.
type Object struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Rows  int64  `json:"rows"` // an estimate, -1 when unknown
	Bytes int64  `json:"bytes,omitempty"`
}

// Column of a table.
type Column struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Nullable bool    `json:"nullable"`
	Default  *string `json:"default,omitempty"`
	Key      string  `json:"key,omitempty"` // PRI, UNI, MUL
	Extra    string  `json:"extra,omitempty"`
	Comment  string  `json:"comment,omitempty"`
	// Enum values, for an enum column
	Values []string `json:"values,omitempty"`
}

// Index of a table.
type Index struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique"`
	Primary bool     `json:"primary"`
	Kind    string   `json:"kind,omitempty"`
}

// ForeignKey links columns of a table to another's.
type ForeignKey struct {
	Name      string   `json:"name"`
	Columns   []string `json:"columns"`
	RefSchema string   `json:"refSchema"`
	RefTable  string   `json:"refTable"`
	RefCols   []string `json:"refColumns"`
}

// TableInfo is the structure of a table.
type TableInfo struct {
	Schema       string       `json:"schema"`
	Name         string       `json:"name"`
	Columns      []Column     `json:"columns"`
	Indexes      []Index      `json:"indexes"`
	ForeignKeys  []ForeignKey `json:"foreignKeys"`
	ReferencedBy []ForeignKey `json:"referencedBy"`
	PrimaryKey   []string     `json:"primaryKey"`
	DDL          string       `json:"ddl"`
	Rows         int64        `json:"rows"`
}

// dialect is what differs between SQL servers.
type dialect interface {
	quote(ident string) string
	param(n int) string
	version(ctx context.Context, q querier) (string, error)
	schema(ctx context.Context, q querier, c Connection, current string) (*Schema, error)
	table(ctx context.Context, q querier, schema, name string) (*TableInfo, error)
	// threadID and kill stop a running statement on the server (MySQL); nil-safe no-ops elsewhere
	threadID(ctx context.Context, conn *sql.Conn) int64
	killSQL(id int64) string
	useSQL(schema string) string
	limit(n, offset int) string
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func dialectFor(d Driver) dialect {
	if d == Postgres {
		return pgDialect{}
	}
	return myDialect{}
}

// qualified quotes schema.table ("" schema: the table alone).
func qualified(dl dialect, schema, table string) string {
	if schema == "" {
		return dl.quote(table)
	}
	return dl.quote(schema) + "." + dl.quote(table)
}

// ---- MySQL and MariaDB -----------------------------------------------------------------------

type myDialect struct{}

func (myDialect) quote(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }
func (myDialect) param(int) string      { return "?" }
func (myDialect) limit(n, off int) string {
	return fmt.Sprintf(" LIMIT %d OFFSET %d", n, off)
}
func (d myDialect) useSQL(s string) string { return "USE " + d.quote(s) }

func (myDialect) version(ctx context.Context, q querier) (string, error) {
	var v string
	err := q.QueryRowContext(ctx, "SELECT VERSION()").Scan(&v)
	if strings.Contains(strings.ToLower(v), "mariadb") {
		return "MariaDB " + strings.SplitN(v, "-", 2)[0], err
	}
	return "MySQL " + strings.SplitN(v, "-", 2)[0], err
}

func (myDialect) schema(ctx context.Context, q querier, c Connection, current string) (*Schema, error) {
	out := &Schema{}
	if c.ShowAll || c.Database == "" {
		rows, err := q.QueryContext(ctx, "SHOW DATABASES")
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil {
				out.Databases = append(out.Databases, n)
			}
		}
		rows.Close()
	} else {
		out.Databases = []string{c.Database}
	}
	if current == "" {
		current = c.Database
	}
	if current == "" && len(out.Databases) > 0 {
		current = firstUser(out.Databases, "information_schema", "mysql", "performance_schema", "sys")
	}
	out.Current = current
	if current == "" {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, "SELECT TABLE_NAME, TABLE_TYPE, COALESCE(TABLE_ROWS, -1), COALESCE(DATA_LENGTH, 0) + COALESCE(INDEX_LENGTH, 0) FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME", current)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var o Object
		if rows.Scan(&o.Name, &o.Type, &o.Rows, &o.Bytes) != nil {
			continue
		}
		if strings.Contains(o.Type, "VIEW") {
			o.Type, o.Rows = "view", -1
			out.Views = append(out.Views, o)
		} else {
			o.Type = "table"
			out.Tables = append(out.Tables, o)
		}
	}
	rows.Close()
	rows, err = q.QueryContext(ctx, "SELECT ROUTINE_NAME, ROUTINE_TYPE FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = ? ORDER BY ROUTINE_NAME", current)
	if err == nil {
		for rows.Next() {
			var o Object
			if rows.Scan(&o.Name, &o.Type) == nil {
				o.Type, o.Rows = strings.ToLower(o.Type), -1
				out.Routines = append(out.Routines, o)
			}
		}
		rows.Close()
	}
	return out, nil
}

func (d myDialect) table(ctx context.Context, q querier, schema, name string) (*TableInfo, error) {
	t := &TableInfo{Schema: schema, Name: name, Rows: -1}
	rows, err := q.QueryContext(ctx, "SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT, COLUMN_KEY, EXTRA, COLUMN_COMMENT FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION", schema, name)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c Column
		var null string
		var def sql.NullString
		if rows.Scan(&c.Name, &c.Type, &null, &def, &c.Key, &c.Extra, &c.Comment) != nil {
			continue
		}
		c.Nullable = null == "YES"
		if def.Valid {
			c.Default = &def.String
		}
		if strings.HasPrefix(strings.ToLower(c.Type), "enum(") || strings.HasPrefix(strings.ToLower(c.Type), "set(") {
			c.Values = enumValues(c.Type)
		}
		t.Columns = append(t.Columns, c)
	}
	rows.Close()
	if len(t.Columns) == 0 {
		return nil, fmt.Errorf("%w: no table %s.%s", ErrNotFound, schema, name)
	}
	rows, err = q.QueryContext(ctx, "SELECT INDEX_NAME, NON_UNIQUE, COLUMN_NAME, INDEX_TYPE FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY INDEX_NAME, SEQ_IN_INDEX", schema, name)
	if err == nil {
		byName := map[string]*Index{}
		var order []string
		for rows.Next() {
			var n, col, kind string
			var non int
			if rows.Scan(&n, &non, &col, &kind) != nil {
				continue
			}
			ix := byName[n]
			if ix == nil {
				ix = &Index{Name: n, Unique: non == 0, Primary: n == "PRIMARY", Kind: kind}
				byName[n] = ix
				order = append(order, n)
			}
			ix.Columns = append(ix.Columns, col)
		}
		rows.Close()
		for _, n := range order {
			t.Indexes = append(t.Indexes, *byName[n])
			if n == "PRIMARY" {
				t.PrimaryKey = byName[n].Columns
			}
		}
	}
	t.ForeignKeys = myFKs(ctx, q, "k.TABLE_SCHEMA = ? AND k.TABLE_NAME = ?", schema, name, false)
	t.ReferencedBy = myFKs(ctx, q, "k.REFERENCED_TABLE_SCHEMA = ? AND k.REFERENCED_TABLE_NAME = ?", schema, name, true)
	var tn, ddl string
	if q.QueryRowContext(ctx, "SHOW CREATE TABLE "+qualified(d, schema, name)).Scan(&tn, &ddl) == nil {
		t.DDL = ddl
	}
	_ = q.QueryRowContext(ctx, "SELECT COALESCE(TABLE_ROWS, -1) FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?", schema, name).Scan(&t.Rows)
	return t, nil
}

func myFKs(ctx context.Context, q querier, where, schema, name string, incoming bool) []ForeignKey {
	rows, err := q.QueryContext(ctx, "SELECT k.CONSTRAINT_NAME, k.TABLE_SCHEMA, k.TABLE_NAME, k.COLUMN_NAME, k.REFERENCED_TABLE_SCHEMA, k.REFERENCED_TABLE_NAME, k.REFERENCED_COLUMN_NAME FROM information_schema.KEY_COLUMN_USAGE k WHERE k.REFERENCED_TABLE_NAME IS NOT NULL AND "+where+" ORDER BY k.CONSTRAINT_NAME, k.ORDINAL_POSITION", schema, name)
	if err != nil {
		return nil
	}
	defer rows.Close()
	return collectFKs(rows, incoming)
}

func collectFKs(rows *sql.Rows, incoming bool) []ForeignKey {
	var out []ForeignKey
	idx := map[string]int{}
	for rows.Next() {
		var n, ts, tn, col, rs, rt, rc string
		if rows.Scan(&n, &ts, &tn, &col, &rs, &rt, &rc) != nil {
			continue
		}
		key := n + "|" + ts + "|" + tn
		i, ok := idx[key]
		if !ok {
			fk := ForeignKey{Name: n, RefSchema: rs, RefTable: rt}
			if incoming { // the table that points here
				fk.RefSchema, fk.RefTable = ts, tn
			}
			out = append(out, fk)
			i = len(out) - 1
			idx[key] = i
		}
		if incoming {
			out[i].Columns = append(out[i].Columns, rc)
			out[i].RefCols = append(out[i].RefCols, col)
		} else {
			out[i].Columns = append(out[i].Columns, col)
			out[i].RefCols = append(out[i].RefCols, rc)
		}
	}
	return out
}

func (myDialect) threadID(ctx context.Context, conn *sql.Conn) int64 {
	var id int64
	_ = conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id)
	return id
}

func (myDialect) killSQL(id int64) string {
	if id <= 0 {
		return ""
	}
	return fmt.Sprintf("KILL QUERY %d", id)
}

// enumValues reads enum('a','b') or set('a','b').
func enumValues(t string) []string {
	open, end := strings.IndexByte(t, '('), strings.LastIndexByte(t, ')')
	if open < 0 || end <= open {
		return nil
	}
	var out []string
	s := t[open+1 : end]
	for i := 0; i < len(s); i++ {
		if s[i] != '\'' {
			continue
		}
		var b strings.Builder
		j := i + 1
		for j < len(s) {
			if s[j] == '\'' && j+1 < len(s) && s[j+1] == '\'' {
				b.WriteByte('\'')
				j += 2
				continue
			}
			if s[j] == '\'' {
				break
			}
			b.WriteByte(s[j])
			j++
		}
		out = append(out, b.String())
		i = j
	}
	return out
}

func firstUser(list []string, skip ...string) string {
	for _, n := range list {
		system := false
		for _, s := range skip {
			if strings.EqualFold(n, s) {
				system = true
			}
		}
		if !system {
			return n
		}
	}
	return ""
}

// ---- PostgreSQL ------------------------------------------------------------------------------

type pgDialect struct{}

func (pgDialect) quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func (pgDialect) param(n int) string    { return fmt.Sprintf("$%d", n) }
func (pgDialect) limit(n, off int) string {
	return fmt.Sprintf(" LIMIT %d OFFSET %d", n, off)
}
func (d pgDialect) useSQL(s string) string { return "SET search_path TO " + d.quote(s) }

func (pgDialect) version(ctx context.Context, q querier) (string, error) {
	var v string
	err := q.QueryRowContext(ctx, "SHOW server_version").Scan(&v)
	return "PostgreSQL " + strings.Fields(v + " ")[0], err
}

func (pgDialect) schema(ctx context.Context, q querier, c Connection, current string) (*Schema, error) {
	out := &Schema{}
	rows, err := q.QueryContext(ctx, "SELECT nspname FROM pg_namespace WHERE nspname NOT LIKE 'pg\\_%' AND nspname <> 'information_schema' ORDER BY nspname")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			out.Databases = append(out.Databases, n)
		}
	}
	rows.Close()
	if current == "" {
		current = "public"
		if len(out.Databases) > 0 && !contains(out.Databases, "public") {
			current = out.Databases[0]
		}
	}
	out.Current = current
	rows, err = q.QueryContext(ctx, `SELECT c.relname, c.relkind::text, CASE WHEN c.reltuples < 0 THEN -1 ELSE c.reltuples::bigint END,
		CASE WHEN c.relkind IN ('r','m','p') THEN pg_total_relation_size(c.oid) ELSE 0 END
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relkind IN ('r','v','m','p','f') ORDER BY c.relname`, current)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var o Object
		var kind string
		if rows.Scan(&o.Name, &kind, &o.Rows, &o.Bytes) != nil {
			continue
		}
		switch kind {
		case "v", "m":
			o.Type = map[string]string{"v": "view", "m": "materialized view"}[kind]
			out.Views = append(out.Views, o)
		default:
			o.Type = "table"
			out.Tables = append(out.Tables, o)
		}
	}
	rows.Close()
	rows, err = q.QueryContext(ctx, "SELECT routine_name, lower(routine_type) FROM information_schema.routines WHERE routine_schema = $1 ORDER BY routine_name", current)
	if err == nil {
		for rows.Next() {
			var o Object
			if rows.Scan(&o.Name, &o.Type) == nil {
				o.Rows = -1
				out.Routines = append(out.Routines, o)
			}
		}
		rows.Close()
	}
	return out, nil
}

func (d pgDialect) table(ctx context.Context, q querier, schema, name string) (*TableInfo, error) {
	t := &TableInfo{Schema: schema, Name: name, Rows: -1}
	reg := qualified(d, schema, name)
	rows, err := q.QueryContext(ctx, `SELECT a.attname, format_type(a.atttypid, a.atttypmod), NOT a.attnotnull,
		pg_get_expr(ad.adbin, ad.adrelid), COALESCE(col_description(a.attrelid, a.attnum), ''),
		COALESCE((SELECT string_agg(e.enumlabel, E'\x1f' ORDER BY e.enumsortorder) FROM pg_enum e WHERE e.enumtypid = a.atttypid), ''),
		CASE WHEN a.attidentity <> '' THEN 'identity' WHEN pg_get_expr(ad.adbin, ad.adrelid) LIKE 'nextval(%' THEN 'auto_increment' ELSE '' END
		FROM pg_attribute a LEFT JOIN pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
		WHERE a.attrelid = $1::regclass AND a.attnum > 0 AND NOT a.attisdropped ORDER BY a.attnum`, reg)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c Column
		var def sql.NullString
		var enum string
		if rows.Scan(&c.Name, &c.Type, &c.Nullable, &def, &c.Comment, &enum, &c.Extra) != nil {
			continue
		}
		if def.Valid {
			c.Default = &def.String
		}
		if enum != "" {
			c.Values = strings.Split(enum, "\x1f")
		}
		t.Columns = append(t.Columns, c)
	}
	rows.Close()
	if len(t.Columns) == 0 {
		return nil, fmt.Errorf("%w: no table %s.%s", ErrNotFound, schema, name)
	}
	rows, err = q.QueryContext(ctx, `SELECT ic.relname, ix.indisunique, ix.indisprimary, COALESCE(a.attname, '('||pg_get_indexdef(ix.indexrelid, k.ord::int, true)||')'), am.amname
		FROM pg_index ix JOIN pg_class ic ON ic.oid = ix.indexrelid JOIN pg_am am ON am.oid = ic.relam
		JOIN LATERAL unnest(ix.indkey) WITH ORDINALITY AS k(attnum, ord) ON true
		LEFT JOIN pg_attribute a ON a.attrelid = ix.indrelid AND a.attnum = k.attnum AND k.attnum > 0
		WHERE ix.indrelid = $1::regclass ORDER BY ic.relname, k.ord`, reg)
	if err == nil {
		byName := map[string]*Index{}
		var order []string
		for rows.Next() {
			var n, col, kind string
			var uniq, prim bool
			if rows.Scan(&n, &uniq, &prim, &col, &kind) != nil {
				continue
			}
			ix := byName[n]
			if ix == nil {
				ix = &Index{Name: n, Unique: uniq, Primary: prim, Kind: kind}
				byName[n] = ix
				order = append(order, n)
			}
			ix.Columns = append(ix.Columns, col)
		}
		rows.Close()
		for _, n := range order {
			ix := *byName[n]
			t.Indexes = append(t.Indexes, ix)
			if ix.Primary {
				t.PrimaryKey = ix.Columns
			}
		}
	}
	keyOf := map[string]string{}
	for _, ix := range t.Indexes {
		for i, c := range ix.Columns {
			switch {
			case ix.Primary:
				keyOf[c] = "PRI"
			case ix.Unique && len(ix.Columns) == 1 && keyOf[c] == "":
				keyOf[c] = "UNI"
			case i == 0 && keyOf[c] == "":
				keyOf[c] = "MUL"
			}
		}
	}
	for i := range t.Columns {
		t.Columns[i].Key = keyOf[t.Columns[i].Name]
	}
	const fk = `SELECT con.conname, sn.nspname, sc.relname, a.attname, fn.nspname, fc.relname, fa.attname
		FROM pg_constraint con
		JOIN LATERAL unnest(con.conkey, con.confkey) WITH ORDINALITY AS u(k, fk, ord) ON true
		JOIN pg_class sc ON sc.oid = con.conrelid JOIN pg_namespace sn ON sn.oid = sc.relnamespace
		JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = u.k
		JOIN pg_class fc ON fc.oid = con.confrelid JOIN pg_namespace fn ON fn.oid = fc.relnamespace
		JOIN pg_attribute fa ON fa.attrelid = con.confrelid AND fa.attnum = u.fk
		WHERE con.contype = 'f' AND `
	if rows, err := q.QueryContext(ctx, fk+"con.conrelid = $1::regclass ORDER BY con.conname, u.ord", reg); err == nil {
		t.ForeignKeys = collectFKs(rows, false)
		rows.Close()
	}
	if rows, err := q.QueryContext(ctx, fk+"con.confrelid = $1::regclass ORDER BY con.conname, u.ord", reg); err == nil {
		t.ReferencedBy = collectFKs(rows, true)
		rows.Close()
	}
	t.DDL = pgDDL(ctx, q, t, reg)
	_ = q.QueryRowContext(ctx, "SELECT CASE WHEN reltuples < 0 THEN -1 ELSE reltuples::bigint END FROM pg_class WHERE oid = $1::regclass", reg).Scan(&t.Rows)
	return t, nil
}

// pgDDL writes a CREATE TABLE of what the catalog says, with its constraints and indexes.
func pgDDL(ctx context.Context, q querier, t *TableInfo, reg string) string {
	var b strings.Builder
	d := pgDialect{}
	fmt.Fprintf(&b, "CREATE TABLE %s (\n", qualified(d, t.Schema, t.Name))
	for i, c := range t.Columns {
		fmt.Fprintf(&b, "  %s %s", d.quote(c.Name), c.Type)
		if !c.Nullable {
			b.WriteString(" NOT NULL")
		}
		if c.Default != nil {
			b.WriteString(" DEFAULT " + *c.Default)
		}
		if i < len(t.Columns)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	if rows, err := q.QueryContext(ctx, "SELECT conname, pg_get_constraintdef(oid, true) FROM pg_constraint WHERE conrelid = $1::regclass ORDER BY contype, conname", reg); err == nil {
		for rows.Next() {
			var n, def string
			if rows.Scan(&n, &def) == nil {
				fmt.Fprintf(&b, "  , CONSTRAINT %s %s\n", d.quote(n), def)
			}
		}
		rows.Close()
	}
	b.WriteString(");\n")
	if rows, err := q.QueryContext(ctx, "SELECT pg_get_indexdef(indexrelid) FROM pg_index WHERE indrelid = $1::regclass AND NOT indisprimary", reg); err == nil {
		for rows.Next() {
			var def string
			if rows.Scan(&def) == nil {
				b.WriteString(def + ";\n")
			}
		}
		rows.Close()
	}
	return b.String()
}

func (pgDialect) threadID(context.Context, *sql.Conn) int64 { return 0 }
func (pgDialect) killSQL(int64) string                      { return "" }

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
