package dbx

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"
	"unicode/utf8"
)

// CellMax is how much of one cell a result carries; the rest is cut ({"$cut": …, "len": n}).
const CellMax = 64 << 10

// ResultColumn names a column of a result.
type ResultColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Result is what one statement gave.
type Result struct {
	SQL       string         `json:"sql"`
	Kind      Kind           `json:"kind"`
	Columns   []ResultColumn `json:"columns,omitempty"`
	Rows      [][]any        `json:"rows,omitempty"`
	Truncated bool           `json:"truncated,omitempty"`
	Affected  int64          `json:"affected"`
	MS        int64          `json:"ms"`
	Error     string         `json:"error,omitempty"`
	// Pending: a dangerous statement ran in a transaction that waits for Commit or Roll back.
	Pending bool   `json:"pending,omitempty"`
	Notice  string `json:"notice,omitempty"`
}

// readRows reads at most max rows into cells a page can show.
func readRows(rows *sql.Rows, max int) ([]ResultColumn, [][]any, bool, error) {
	types, err := rows.ColumnTypes()
	if err != nil {
		return nil, nil, false, err
	}
	cols := make([]ResultColumn, len(types))
	for i, t := range types {
		cols[i] = ResultColumn{Name: t.Name(), Type: strings.ToLower(t.DatabaseTypeName())}
	}
	var out [][]any
	truncated := false
	for rows.Next() {
		if len(out) >= max {
			truncated = true
			break
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return cols, out, truncated, err
		}
		row := make([]any, len(cols))
		for i, v := range vals {
			row[i] = cell(v, cols[i].Type)
		}
		out = append(out, row)
	}
	return cols, out, truncated, rows.Err()
}

// maxSafe is the largest integer a JavaScript number holds exactly.
const maxSafe = 1<<53 - 1

// cell turns a scanned value into JSON a grid shows as the database has it.
func cell(v any, typ string) any {
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		if utf8.Valid(x) {
			return text(string(x))
		}
		h := x
		if len(h) > 256 {
			h = h[:256]
		}
		return map[string]any{"$bin": hex.EncodeToString(h), "len": len(x)}
	case string:
		return text(x)
	case int64:
		if x > maxSafe || x < -maxSafe {
			return fmt.Sprint(x)
		}
		return x
	case int32, int16, int8, int, uint8, uint16, uint32:
		return x
	case uint64:
		if x > maxSafe {
			return fmt.Sprint(x)
		}
		return x
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return fmt.Sprint(x)
		}
		return x
	case float32:
		return float64(x)
	case bool:
		return x
	case time.Time:
		return text(formatTime(x, typ))
	case *big.Int:
		return x.String()
	case fmt.Stringer:
		return text(x.String())
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return text(fmt.Sprint(x))
		}
		return text(string(b))
	}
}

func text(s string) any {
	if len(s) <= CellMax {
		return s
	}
	cut := s[:CellMax]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return map[string]any{"$cut": cut, "len": len(s)}
}

func formatTime(t time.Time, typ string) string {
	switch typ {
	case "date":
		return t.Format("2006-01-02")
	case "time", "timetz":
		return t.Format("15:04:05.999999")
	case "timestamptz":
		return t.Format("2006-01-02 15:04:05.999999Z07:00")
	}
	return t.Format("2006-01-02 15:04:05.999999")
}
