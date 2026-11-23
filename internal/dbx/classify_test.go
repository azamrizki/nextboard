package dbx

import (
	"slices"
	"testing"
)

func TestSplitKeepsSemicolonsInsideStringsAndComments(t *testing.T) {
	cases := []struct {
		d    Driver
		in   string
		want []string
	}{
		{MySQL, "SELECT 1; SELECT 2;", []string{"SELECT 1", "SELECT 2"}},
		{MySQL, "SELECT 'a;b'; SELECT \"c;d\"", []string{"SELECT 'a;b'", "SELECT \"c;d\""}},
		{MySQL, "SELECT 'it''s;'; SELECT 'x\\';y'", []string{"SELECT 'it''s;'", "SELECT 'x\\';y'"}},
		{MySQL, "SELECT `a;b` FROM t -- c;d\n; # e;f\nSELECT 2", []string{"SELECT `a;b` FROM t -- c;d", "# e;f\nSELECT 2"}},
		{MySQL, "/* a; b */ SELECT 1", []string{"/* a; b */ SELECT 1"}},
		{Postgres, "CREATE FUNCTION f() RETURNS int AS $$ BEGIN RETURN 1; END; $$ LANGUAGE plpgsql; SELECT f()",
			[]string{"CREATE FUNCTION f() RETURNS int AS $$ BEGIN RETURN 1; END; $$ LANGUAGE plpgsql", "SELECT f()"}},
		{Postgres, "SELECT $tag$;$tag$; SELECT $1", []string{"SELECT $tag$;$tag$", "SELECT $1"}},
		{MySQL, ";;  ; -- only a comment", nil},
	}
	for _, c := range cases {
		if got := Split(c.in, c.d); !slices.Equal(got, c.want) {
			t.Errorf("Split(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		d      Driver
		sql    string
		kind   Kind
		danger bool
		table  string
	}{
		{MySQL, "SELECT * FROM orders WHERE id = 1", KindRead, false, ""},
		{MySQL, "  -- a note\n select 1", KindRead, false, ""},
		{MySQL, "SHOW TABLES", KindRead, false, ""},
		{MySQL, "EXPLAIN SELECT 1", KindRead, false, ""},
		{Postgres, "EXPLAIN ANALYZE DELETE FROM sessions", KindWrite, true, "sessions"},
		{Postgres, "EXPLAIN (ANALYZE, BUFFERS) UPDATE t SET a = 1 WHERE id = 2", KindWrite, false, "t"},
		{Postgres, "EXPLAIN ANALYZE SELECT 1", KindRead, false, ""},
		{MySQL, "SELECT a INTO @x FROM t", KindRead, false, ""},
		{Postgres, "SELECT * INTO new_table FROM t", KindWrite, false, ""},
		{MySQL, "SELECT * FROM t FOR UPDATE", KindWrite, false, ""},
		{Postgres, "WITH x AS (SELECT 1) SELECT * FROM x", KindRead, false, ""},
		{Postgres, "WITH gone AS (DELETE FROM t WHERE a < 1 RETURNING *) SELECT count(*) FROM gone", KindWrite, false, ""},
		{MySQL, "INSERT INTO t VALUES (1)", KindWrite, false, ""},
		{MySQL, "UPDATE orders SET status = 'paid' WHERE id = 1", KindWrite, false, "orders"},
		{MySQL, "UPDATE orders SET status = 'paid'", KindWrite, true, "orders"},
		// a WHERE inside a subquery does not limit the UPDATE
		{MySQL, "UPDATE orders SET total = (SELECT 1 FROM x WHERE x.id = 2)", KindWrite, true, "orders"},
		{MySQL, "DELETE FROM sessions", KindWrite, true, "sessions"},
		{MySQL, "DELETE FROM `sessions` WHERE last_seen < NOW()", KindWrite, false, "sessions"},
		{MySQL, "delete low_priority from logs", KindWrite, true, "logs"},
		// the word WHERE inside a string is not a WHERE
		{MySQL, "DELETE FROM t -- WHERE id = 1", KindWrite, true, "t"},
		{MySQL, "UPDATE t SET note = 'where'", KindWrite, true, "t"},
		{MySQL, "TRUNCATE TABLE jobs", KindDDL, true, "jobs"},
		{Postgres, "TRUNCATE jobs", KindDDL, true, "jobs"},
		{MySQL, "DROP TABLE IF EXISTS users", KindDDL, true, "users"},
		{MySQL, "DROP DATABASE shop", KindDDL, true, ""},
		{MySQL, "ALTER TABLE t ADD COLUMN c int", KindDDL, false, ""},
		{MySQL, "ALTER TABLE t DROP COLUMN c", KindDDL, true, ""},
		{MySQL, "CREATE INDEX i ON t (a)", KindDDL, false, ""},
		{MySQL, "BEGIN", KindTx, false, ""},
		{MySQL, "START TRANSACTION READ ONLY", KindTx, false, ""},
		{MySQL, "COMMIT", KindTx, false, ""},
		{MySQL, "USE shop", KindSession, false, ""},
		{MySQL, "SET NAMES utf8mb4", KindSession, false, ""},
		{Postgres, "SET search_path TO app", KindSession, false, ""},
		{MySQL, "SET @x = 1", KindSession, false, ""},
		// a SET that could undo read-only is a write
		{MySQL, "SET SESSION transaction_read_only = 0", KindWrite, false, ""},
		{Postgres, "SET default_transaction_read_only = off", KindWrite, false, ""},
		{MySQL, "CALL refresh_totals()", KindWrite, false, ""},
		{Postgres, "COPY t TO STDOUT", KindRead, false, ""},
		{Postgres, "COPY t FROM STDIN", KindWrite, false, ""},
		{Postgres, "DO $$ BEGIN DELETE FROM t; END $$", KindWrite, false, ""},
		{MySQL, "LOAD DATA INFILE 'x' INTO TABLE t", KindWrite, false, ""},
		{MySQL, "KILL 42", KindWrite, false, ""},
	}
	for _, c := range cases {
		got := Classify(c.sql, c.d)
		if got.Kind != c.kind || (got.Danger != "") != c.danger || got.Table != c.table {
			t.Errorf("Classify(%q) = kind %s, danger %q, table %q; want %s, %v, %q", c.sql, got.Kind, got.Danger, got.Table, c.kind, c.danger, c.table)
		}
	}
}

func TestASystemVariableIsNotAUserVariable(t *testing.T) {
	for _, sql := range []string{"SET @@session.transaction_read_only = 0", "SET @@transaction_read_only = 0", "SET SESSION TRANSACTION READ WRITE"} {
		if got := Classify(sql, MySQL); got.Kind != KindWrite {
			t.Errorf("Classify(%q) = %s, want write: it could undo read-only", sql, got.Kind)
		}
	}
}
