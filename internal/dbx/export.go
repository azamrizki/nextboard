package dbx

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// ExportRows is the most a download carries.
const ExportRows = 1_000_000

// ExportRequest is a download of a table view or of a query's rows.
type ExportRequest struct {
	Rows   *RowsRequest `json:"rows,omitempty"`
	SQL    string       `json:"sql,omitempty"`
	Schema string       `json:"schema,omitempty"`
	Format string       `json:"format"` // csv, json, sql
	// Table names the INSERTs of an SQL export (the table of a table view by default)
	Table string `json:"table,omitempty"`
}

// Export writes rows as CSV, JSON or INSERT statements, in a read-only transaction.
func (e *sqlEngine) Export(ctx context.Context, rq ExportRequest, w io.Writer) error {
	var q string
	var args []any
	table := rq.Table
	switch {
	case rq.Rows != nil:
		t, err := e.Table(ctx, rq.Rows.Schema, rq.Rows.Table)
		if err != nil {
			return err
		}
		where, a, err := buildWhere(e.dl, t, rq.Rows.Filters, rq.Rows.Where)
		if err != nil {
			return err
		}
		q, args = "SELECT * FROM "+qualified(e.dl, rq.Rows.Schema, rq.Rows.Table)+where, a
		var by []string
		for _, s := range rq.Rows.Sort {
			d := ""
			if s.Desc {
				d = " DESC"
			}
			by = append(by, e.dl.quote(s.Column)+d)
		}
		if len(by) > 0 {
			q += " ORDER BY " + strings.Join(by, ", ")
		}
		if table == "" {
			table = rq.Rows.Table
		}
	case rq.SQL != "":
		stmts := Split(rq.SQL, e.c.Driver)
		if len(stmts) != 1 {
			return invalid("export one statement")
		}
		if st := Classify(stmts[0], e.c.Driver); st.Kind != KindRead {
			return invalid("only a statement that reads can be exported")
		}
		q = stmts[0]
		if table == "" {
			table = "result"
		}
	default:
		return invalid("rows or sql")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	conn, err := e.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if rq.Schema != "" {
		if _, err := conn.ExecContext(ctx, e.dl.useSQL(rq.Schema)); err != nil {
			return err
		}
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	types, err := rows.ColumnTypes()
	if err != nil {
		return err
	}
	names := make([]string, len(types))
	for i, t := range types {
		names[i] = t.Name()
	}
	var cw *csv.Writer
	var quoted []string
	switch rq.Format {
	case "csv":
		cw = csv.NewWriter(w)
		_ = cw.Write(names)
	case "json":
		if _, err := io.WriteString(w, "[\n"); err != nil {
			return err
		}
	case "sql":
		for _, n := range names {
			quoted = append(quoted, e.dl.quote(n))
		}
	default:
		return invalid("format: csv, json or sql")
	}
	n := 0
	for rows.Next() {
		if n >= ExportRows {
			break
		}
		vals := make([]any, len(names))
		ptrs := make([]any, len(names))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		switch rq.Format {
		case "csv":
			rec := make([]string, len(vals))
			for i, v := range vals {
				rec[i] = plain(v, strings.ToLower(types[i].DatabaseTypeName()))
			}
			if err := cw.Write(rec); err != nil {
				return err
			}
		case "json":
			obj := make(map[string]any, len(vals))
			for i, v := range vals {
				if v == nil {
					obj[names[i]] = nil
				} else {
					obj[names[i]] = plain(v, strings.ToLower(types[i].DatabaseTypeName()))
				}
			}
			b, _ := json.Marshal(obj)
			sep := ",\n"
			if n == 0 {
				sep = ""
			}
			if _, err := io.WriteString(w, sep+string(b)); err != nil {
				return err
			}
		case "sql":
			lits := make([]string, len(vals))
			for i, v := range vals {
				if v == nil {
					lits[i] = "NULL"
				} else {
					lits[i] = literal(e.c.Driver, plain(v, strings.ToLower(types[i].DatabaseTypeName())))
				}
			}
			if _, err := fmt.Fprintf(w, "INSERT INTO %s (%s) VALUES (%s);\n", e.dl.quote(table), strings.Join(quoted, ", "), strings.Join(lits, ", ")); err != nil {
				return err
			}
		}
		n++
	}
	if cw != nil {
		cw.Flush()
		return cw.Error()
	}
	if rq.Format == "json" {
		_, err = io.WriteString(w, "\n]\n")
		return err
	}
	return rows.Err()
}

// plain is a value as text, whole (no cutting), for a download.
func plain(v any, typ string) string {
	switch x := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(x)
	case string:
		return x
	case time.Time:
		return formatTime(x, typ)
	}
	return fmt.Sprint(v)
}
