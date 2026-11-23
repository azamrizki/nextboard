package dbx

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
)

// These run against real servers that scripts/db-test.sh (make test-db) starts as throwaway
// containers and removes again. NEXTBOARD_DB_TEST_MYSQL and NEXTBOARD_DB_TEST_POSTGRES are
// "host:port user password database"; without them the tests skip. Never point them at a
// database that holds data: the tests create and drop their own tables.

func realEngine(t *testing.T, d Driver, sec Security, limits Limits) *sqlEngine {
	t.Helper()
	env := map[Driver]string{MySQL: "NEXTBOARD_DB_TEST_MYSQL", Postgres: "NEXTBOARD_DB_TEST_POSTGRES"}[d]
	v := os.Getenv(env)
	if v == "" {
		t.Skip(env + " not set: make test-db runs these against throwaway containers")
	}
	f := strings.Fields(v)
	if len(f) != 4 {
		t.Fatalf("%s: host:port user password database", env)
	}
	host, port, _ := net.SplitHostPort(f[0])
	p, _ := strconv.Atoi(port)
	c := Connection{ID: "db_test" + string(d), Name: "nbtest", Driver: d, Host: host, Port: p, User: f[1], Database: f[3], Limits: limits}
	if err := normalize(&c); err != nil {
		t.Fatal(err)
	}
	c.Security = sec
	var dl net.Dialer
	e, err := newSQLEngine(context.Background(), c, f[2], dl.DialContext, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.close)
	return e
}

// mustRun runs a script statement by statement, committing what waits for Commit.
func mustRun(t *testing.T, e *sqlEngine, sql string) []Result {
	t.Helper()
	var out []Result
	for _, st := range Split(sql, e.c.Driver) {
		res, err := e.Run(context.Background(), RunOpts{Session: "setup", SQL: st, Confirmed: true, ConfirmName: e.c.Name})
		if err != nil {
			t.Fatalf("%s: %v", st, err)
		}
		for _, r := range res {
			if r.Error != "" {
				t.Fatalf("%s: %s", r.SQL, r.Error)
			}
			if r.Pending {
				if err := e.Finish("setup", true); err != nil {
					t.Fatal(err)
				}
			}
		}
		out = append(out, res...)
	}
	return out
}

func setupShop(t *testing.T, e *sqlEngine) {
	t.Helper()
	if e.c.Driver == Postgres {
		mustRun(t, e, `DROP TABLE IF EXISTS orders; DROP TABLE IF EXISTS users; DROP TYPE IF EXISTS order_status;
CREATE TYPE order_status AS ENUM ('pending','paid','refunded');
CREATE TABLE users (id bigserial PRIMARY KEY, name text NOT NULL, city text);
CREATE TABLE orders (id bigserial PRIMARY KEY, user_id bigint REFERENCES users(id), status order_status NOT NULL DEFAULT 'pending', total numeric(12,2), big bigint, note text);
CREATE INDEX orders_status ON orders (status);`)
	} else {
		mustRun(t, e, "DROP TABLE IF EXISTS orders; DROP TABLE IF EXISTS users;")
		mustRun(t, e, "CREATE TABLE users (id bigint AUTO_INCREMENT PRIMARY KEY, name varchar(80) NOT NULL, city varchar(80))")
		mustRun(t, e, "CREATE TABLE orders (id bigint AUTO_INCREMENT PRIMARY KEY, user_id bigint, status enum('pending','paid','refunded') NOT NULL DEFAULT 'pending', total decimal(12,2), big bigint, note varchar(255), KEY orders_status (status), CONSTRAINT orders_user FOREIGN KEY (user_id) REFERENCES users(id))")
	}
	mustRun(t, e, "INSERT INTO users (name, city) VALUES ('Ani', 'Jakarta'), ('Budi', 'Bandung')")
	mustRun(t, e, "INSERT INTO orders (user_id, status, total, big, note) VALUES (1, 'paid', 249000, 9007199254740993, 'a;b'), (1, 'pending', 89500, 1, NULL), (2, 'paid', 1120000, 2, 'x')")
	e.Forget()
}

func TestRealSQL(t *testing.T) {
	for _, d := range []Driver{MySQL, Postgres} {
		t.Run(string(d), func(t *testing.T) {
			ctx := context.Background()
			e := realEngine(t, d, Security{}, Limits{TimeoutSec: 2, PageSize: 100})
			if !strings.HasPrefix(e.Version(), map[Driver]string{MySQL: "MySQL", Postgres: "PostgreSQL"}[d]) {
				t.Errorf("version %q", e.Version())
			}
			setupShop(t, e)
			sc, err := e.Schema(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			names := map[string]bool{}
			for _, o := range sc.Tables {
				names[o.Name] = true
			}
			if !names["users"] || !names["orders"] {
				t.Fatalf("tables %+v", sc.Tables)
			}
			ti, err := e.Table(ctx, sc.Current, "orders")
			if err != nil {
				t.Fatal(err)
			}
			if len(ti.PrimaryKey) != 1 || ti.PrimaryKey[0] != "id" || len(ti.ForeignKeys) != 1 || ti.ForeignKeys[0].RefTable != "users" || ti.DDL == "" {
				t.Errorf("orders %+v", ti)
			}
			var status Column
			for _, c := range ti.Columns {
				if c.Name == "status" {
					status = c
				}
			}
			if strings.Join(status.Values, ",") != "pending,paid,refunded" {
				t.Errorf("enum values %v", status.Values)
			}
			users, _ := e.Table(ctx, sc.Current, "users")
			if len(users.ReferencedBy) != 1 || users.ReferencedBy[0].RefTable != "orders" {
				t.Errorf("users referenced by %+v", users.ReferencedBy)
			}

			// a filtered, sorted page with its count
			page, err := e.Rows(ctx, RowsRequest{Schema: sc.Current, Table: "orders", Filters: []Filter{{Column: "status", Op: "=", Value: "paid"}}, Sort: []Sort{{Column: "total", Desc: true}}})
			if err != nil {
				t.Fatal(err)
			}
			if page.Total != 2 || len(page.Rows) != 2 {
				t.Fatalf("page %+v", page)
			}
			bigAt := -1
			for i, c := range page.Columns {
				if c.Name == "big" {
					bigAt = i
				}
			}
			if page.Rows[1][bigAt] != "9007199254740993" {
				t.Errorf("a big integer lost digits: %#v", page.Rows[1][bigAt])
			}

			// a statement with a ; in a string, and results per statement
			res := mustRun(t, e, "SELECT note FROM orders WHERE note = 'a;b'; SELECT COUNT(*) FROM users")
			if len(res) != 2 || res[0].Rows[0][0] != "a;b" {
				t.Errorf("results %+v", res)
			}

			// DELETE without WHERE asks for the name, counts its rows, and waits for Commit
			var ce *ConfirmError
			if _, err := e.Run(ctx, RunOpts{Session: "tab1", SQL: "DELETE FROM orders"}); !errors.As(err, &ce) || ce.Code != "confirm_name" || ce.Rows[0] != 3 {
				t.Fatalf("delete all: %v %+v", err, ce)
			}
			res, err = e.Run(ctx, RunOpts{Session: "tab1", SQL: "DELETE FROM orders", ConfirmName: "nbtest"})
			if err != nil || !res[0].Pending || res[0].Affected != 3 {
				t.Fatalf("pending delete: %+v %v", res, err)
			}
			if _, err := e.Run(ctx, RunOpts{Session: "tab1", SQL: "SELECT 1"}); !errors.Is(err, ErrPending) {
				t.Errorf("a second statement while one waits: %v", err)
			}
			if err := e.Finish("tab1", false); err != nil {
				t.Fatal(err)
			}
			if r := mustRun(t, e, "SELECT COUNT(*) FROM orders"); cellNum(r[0].Rows[0][0]) != 3 {
				t.Errorf("rolled back delete still removed rows: %v", r[0].Rows[0][0])
			}

			// a manual transaction
			mustRunIn(t, e, "tab2", "BEGIN; UPDATE orders SET note = 'temp' WHERE id = 2")
			mustRunIn(t, e, "tab2", "ROLLBACK")
			if r := mustRun(t, e, "SELECT note FROM orders WHERE id = 2"); r[0].Rows[0][0] != nil {
				t.Errorf("rolled back update kept: %v", r[0].Rows[0][0])
			}

			// grid edits in one transaction, each touching exactly one row
			if _, err := e.Apply(ctx, ApplyRequest{Schema: sc.Current, Table: "orders", Confirmed: true, Changes: []Change{
				{Op: "update", Key: map[string]any{"id": float64(1)}, Set: map[string]any{"status": "refunded"}},
				{Op: "insert", Set: map[string]any{"user_id": float64(2), "status": "pending", "total": "10"}},
				{Op: "delete", Key: map[string]any{"id": float64(3)}},
			}}); err != nil {
				t.Fatal(err)
			}
			if r := mustRun(t, e, "SELECT COUNT(*) FROM orders WHERE status = 'refunded'"); cellNum(r[0].Rows[0][0]) != 1 {
				t.Error("the edit did not apply")
			}
			if _, err := e.Apply(ctx, ApplyRequest{Schema: sc.Current, Table: "orders", Confirmed: true, Changes: []Change{{Op: "update", Key: map[string]any{"id": float64(999)}, Set: map[string]any{"note": "x"}}}}); err == nil {
				t.Error("an edit of no row passed")
			}

			// a query over the limit stops, on the server too
			sleep := map[Driver]string{MySQL: "SELECT SLEEP(5)", Postgres: "SELECT pg_sleep(5)"}[d]
			if res, _ := e.Run(ctx, RunOpts{SQL: sleep}); len(res) != 1 || !strings.Contains(res[0].Error, "stopped after 2 s") {
				t.Errorf("timeout: %+v", res)
			}

			// export
			var buf bytes.Buffer
			if err := e.Export(ctx, ExportRequest{Rows: &RowsRequest{Schema: sc.Current, Table: "users"}, Format: "csv"}, &buf); err != nil || !strings.Contains(buf.String(), "Ani,Jakarta") {
				t.Errorf("csv %q %v", buf.String(), err)
			}
			buf.Reset()
			if err := e.Export(ctx, ExportRequest{SQL: "SELECT name FROM users ORDER BY id", Format: "sql", Table: "people"}, &buf); err != nil || !strings.Contains(buf.String(), "VALUES ('Ani');") {
				t.Errorf("sql %q %v", buf.String(), err)
			}
			if err := e.Export(ctx, ExportRequest{SQL: "DELETE FROM users", Format: "csv"}, &buf); err == nil {
				t.Error("a DELETE exported")
			}
		})
	}
}

func mustRunIn(t *testing.T, e *sqlEngine, session, sql string) {
	t.Helper()
	res, err := e.Run(context.Background(), RunOpts{Session: session, SQL: sql, Confirmed: true})
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	for _, r := range res {
		if r.Error != "" {
			t.Fatalf("%s: %s", r.SQL, r.Error)
		}
	}
}

func cellNum(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return -1
}

func TestRealReadOnlyAndConfirm(t *testing.T) {
	for _, d := range []Driver{MySQL, Postgres} {
		t.Run(string(d), func(t *testing.T) {
			ctx := context.Background()
			rw := realEngine(t, d, Security{}, Limits{TimeoutSec: 10, PageSize: 100})
			setupShop(t, rw)
			ro := realEngine(t, d, Security{ReadOnly: true}, Limits{TimeoutSec: 10, PageSize: 100})
			var roe *ReadOnlyError
			if _, err := ro.Run(ctx, RunOpts{SQL: "UPDATE orders SET note = 'x' WHERE id = 1"}); !errors.As(err, &roe) {
				t.Errorf("UPDATE on read-only: %v", err)
			}
			// the server refuses too, should the classifier miss a write: a function call here
			if res, err := ro.Run(ctx, RunOpts{SQL: "SELECT 1; SELECT * FROM orders LIMIT 1"}); err != nil || res[1].Error != "" {
				t.Errorf("reads on read-only: %+v %v", res, err)
			}
			if _, err := ro.Apply(ctx, ApplyRequest{Schema: "nbtest", Table: "orders", Changes: []Change{{Op: "delete", Key: map[string]any{"id": float64(1)}}}}); !errors.As(err, &roe) {
				t.Errorf("grid edit on read-only: %v", err)
			}
			cw := realEngine(t, d, Security{ConfirmWrites: true}, Limits{TimeoutSec: 10, PageSize: 100})
			var ce *ConfirmError
			if _, err := cw.Run(ctx, RunOpts{SQL: "UPDATE orders SET note = 'y' WHERE id = 1"}); !errors.As(err, &ce) || ce.Code != "confirm" {
				t.Errorf("write without confirming: %v", err)
			}
			if _, err := cw.Run(ctx, RunOpts{SQL: "UPDATE orders SET note = 'y' WHERE id = 1", Confirmed: true}); err != nil {
				t.Errorf("confirmed write: %v", err)
			}
		})
	}
}
