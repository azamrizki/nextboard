package dbx

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/secret"
	"github.com/azamrizki/nextboard/internal/store"
)

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	box, err := secret.Open(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(st, box)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func ptr(s string) *string { return &s }

func TestSecretsAreSealedAndNeverShown(t *testing.T) {
	s, dir := testStore(t)
	c, err := s.Add(Connection{ProjectID: "p_1", Driver: MySQL, Name: "shop", User: "shop", SavePassword: true,
		Reach: Reach{Kind: ReachSSH, Hops: []Hop{{Host: "bastion", User: "deploy", Auth: AuthPassword}, {Host: "db", User: "ubuntu", Auth: AuthKey}}}},
		Secrets{Password: ptr("db-pass-42"), HopSecrets: []*string{ptr("ssh-pass-7"), ptr("phrase")}, HopKeys: []*string{nil, ptr("-----BEGIN OPENSSH PRIVATE KEY-----\nx\n-----END OPENSSH PRIVATE KEY-----")}})
	if err != nil {
		t.Fatal(err)
	}
	if !c.HasPassword || !c.Reach.Hops[0].HasSecret || !c.Reach.Hops[1].HasKey || c.Reach.Hops[0].HasKey {
		t.Fatalf("flags %+v", c)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "dbs", "connections.json"))
	for _, plain := range []string{"db-pass-42", "ssh-pass-7", "phrase", "OPENSSH"} {
		if strings.Contains(string(raw), plain) {
			t.Errorf("%q is in connections.json as it was", plain)
		}
	}
	b, _ := json.Marshal(c)
	if strings.Contains(string(b), "db-pass-42") || strings.Contains(string(b), "password\":\"") {
		t.Errorf("the API shape carries a secret: %s", b)
	}
	o, err := s.secrets(c.ID)
	if err != nil || o.Password != "db-pass-42" || o.HopSecrets[0] != "ssh-pass-7" || !strings.Contains(o.HopKeys[1], "OPENSSH") {
		t.Fatalf("opened %+v %v", o, err)
	}
	// an edit that sends no secrets keeps them; "" removes one
	c.Name = "shop2"
	c2, err := s.Replace(c.ID, c, Secrets{HopSecrets: []*string{ptr("")}})
	if err != nil || !c2.HasPassword || c2.Reach.Hops[0].HasSecret || !c2.Reach.Hops[1].HasSecret {
		t.Fatalf("after replace %+v %v", c2, err)
	}
	// a connection that does not save its password keeps none
	c2.SavePassword = false
	c3, _ := s.Replace(c.ID, c2, Secrets{})
	if c3.HasPassword {
		t.Error("a password kept although Save password is off")
	}
}

func TestProductionStartsReadOnlyAndDefaultsFill(t *testing.T) {
	c := Connection{Driver: Postgres, Env: EnvProduction}
	if err := normalize(&c); err != nil {
		t.Fatal(err)
	}
	if c.Port != 5432 || c.Host != "127.0.0.1" || c.Agents.Access != AgentsAsk || !reflect.DeepEqual(c.Agents.Masked, DefaultMasked) || c.Limits.PageSize != 100 {
		t.Errorf("defaults %+v", c)
	}
	if !defaultSecurity(EnvProduction).ReadOnly || defaultSecurity(EnvLocal).ReadOnly {
		t.Error("only production starts read-only")
	}
	for _, bad := range []Connection{{Driver: "oracle"}, {Driver: MySQL, Port: 70000}, {Driver: MySQL, Reach: Reach{Kind: ReachSSH}}, {Driver: MySQL, Reach: Reach{Kind: ReachMachine}}, {Driver: MySQL, Agents: Agents{Access: "all"}}} {
		if err := normalize(&bad); err == nil {
			t.Errorf("%+v passed", bad)
		}
	}
}

func TestDeleteProjectTakesItsConnectionsAndQueries(t *testing.T) {
	s, _ := testStore(t)
	a, _ := s.Add(Connection{ProjectID: "p_a", Driver: Redis}, Secrets{})
	b, _ := s.Add(Connection{ProjectID: "p_b", Driver: Redis}, Secrets{})
	if _, err := s.SaveQuery(Query{ProjectID: "p_a", Name: "q", SQL: "SELECT 1"}); err != nil {
		t.Fatal(err)
	}
	s.AddHistory(a.ID, HistoryItem{SQL: "SELECT 1"})
	gone := s.DeleteProject("p_a")
	if len(gone) != 1 || gone[0] != a.ID {
		t.Fatalf("gone %v", gone)
	}
	if _, ok := s.Get(b.ID); !ok || len(s.List("p_a")) != 0 || len(s.Queries("p_a")) != 0 || len(s.History(a.ID, 10)) != 0 {
		t.Error("project a still has things, or b lost its own")
	}
}

func TestHistoryIsNewestFirstAndBounded(t *testing.T) {
	s, _ := testStore(t)
	for i := 0; i < maxHistory*3; i++ {
		s.AddHistory("db_x", HistoryItem{SQL: "SELECT " + strings.Repeat("x", 500) + string(rune('a'+i%26))})
	}
	h := s.History("db_x", 0)
	if len(h) > maxHistory*2 || len(h) < maxHistory {
		t.Errorf("history keeps %d", len(h))
	}
	if !strings.HasSuffix(h[0].SQL, string(rune('a'+(maxHistory*3-1)%26))) {
		t.Error("not newest first")
	}
}

func TestFiltersBecomeParameters(t *testing.T) {
	tbl := &TableInfo{Name: "orders", Columns: []Column{{Name: "id"}, {Name: "status"}, {Name: "total"}, {Name: "note"}}}
	where, args, err := buildWhere(myDialect{}, tbl, []Filter{
		{Column: "status", Op: "in", Values: []string{"paid", "pending"}},
		{Column: "total", Op: ">", Value: "500000"},
		{Column: "note", Op: "contains", Value: "50%_off"},
		{Column: "id", Op: "not null"},
	}, "user_id IN (SELECT id FROM users WHERE city = 'Jakarta')")
	if err != nil {
		t.Fatal(err)
	}
	want := " WHERE `status` IN (?, ?) AND `total` > ? AND `note` LIKE ? AND `id` IS NOT NULL AND (user_id IN (SELECT id FROM users WHERE city = 'Jakarta'))"
	if where != want {
		t.Errorf("where\n got %s\nwant %s", where, want)
	}
	if !reflect.DeepEqual(args, []any{"paid", "pending", "500000", `%50\%\_off%`}) {
		t.Errorf("args %q", args)
	}
	pw, _, _ := buildWhere(pgDialect{}, tbl, []Filter{{Column: "status", Op: "=", Value: "x"}, {Column: "note", Op: "starts", Value: "a"}}, "")
	if pw != ` WHERE "status" = $1 AND "note"::text LIKE $2` {
		t.Errorf("pg where %s", pw)
	}
	if _, _, err := buildWhere(myDialect{}, tbl, []Filter{{Column: "nope", Op: "=", Value: "1"}}, ""); err == nil {
		t.Error("an unknown column passed")
	}
	if _, _, err := buildWhere(myDialect{}, tbl, nil, "1=1; DROP TABLE orders"); err == nil {
		t.Error("a second statement in WHERE passed")
	}
}

func TestGridEditsNeedAKeyAndShowTheirSQL(t *testing.T) {
	e := &sqlEngine{c: Connection{Driver: MySQL}, dl: myDialect{}}
	tbl := &TableInfo{Name: "orders", Columns: []Column{{Name: "id"}, {Name: "status"}, {Name: "note"}}, PrimaryKey: []string{"id"}}
	bs, err := e.buildChanges(tbl, "shop", []Change{
		{Op: "update", Key: map[string]any{"id": float64(1040)}, Set: map[string]any{"status": "refunded", "note": "it's"}},
		{Op: "insert", Set: map[string]any{"status": "pending"}},
		{Op: "delete", Key: map[string]any{"id": float64(1038)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	shows := []string{
		"UPDATE `shop`.`orders` SET `note` = 'it''s', `status` = 'refunded' WHERE `id` = 1040",
		"INSERT INTO `shop`.`orders` (`status`) VALUES ('pending')",
		"DELETE FROM `shop`.`orders` WHERE `id` = 1038",
	}
	for i, b := range bs {
		if b.show != shows[i] {
			t.Errorf("show %d\n got %s\nwant %s", i, b.show, shows[i])
		}
		if strings.Contains(b.sql, "refunded") || strings.Contains(b.sql, "1040") {
			t.Errorf("values in the SQL that runs: %s", b.sql)
		}
	}
	tbl.PrimaryKey = nil
	if _, err := e.buildChanges(tbl, "shop", []Change{{Op: "delete", Key: map[string]any{"id": float64(1)}}}); err == nil {
		t.Error("a delete without a key passed")
	}
}

func TestMaskedColumns(t *testing.T) {
	cols := []ResultColumn{{Name: "id"}, {Name: "password_hash"}, {Name: "Email"}, {Name: "api_token"}, {Name: "city"}}
	got := Masked([]string{"*password*", "users.email", "*token*"}, cols)
	if !reflect.DeepEqual(got, []bool{false, true, true, true, false}) {
		t.Errorf("masked %v", got)
	}
	rows := [][]any{{1, "h", "a@b", "t", "Jakarta"}, {2, nil, "c@d", "u", "Bandung"}}
	MaskRows(got, rows)
	if rows[0][1] != "•••" || rows[1][1] != nil || rows[0][2] != "•••" || rows[0][4] != "Jakarta" {
		t.Errorf("rows %v", rows)
	}
}

func TestDetectReadsLaravelEnvAndURLs(t *testing.T) {
	env := parseEnv(`APP_ENV=production
DB_CONNECTION=mysql
DB_HOST=localhost
DB_PORT=3307
DB_DATABASE="shop_db"
DB_USERNAME=shop
DB_PASSWORD='s3cret#1' # a comment
REDIS_HOST=127.0.0.1
REDIS_PASSWORD=null
REDIS_PORT=6380
DATABASE_URL=postgres://app:pw@db.internal:5433/app?sslmode=require
`)
	got := fromEnv(env)
	if len(got) != 3 {
		t.Fatalf("found %d: %+v", len(got), got)
	}
	my := got[0]
	if my.c.Driver != MySQL || my.c.Host != "127.0.0.1" || my.c.Port != 3307 || my.c.Database != "shop_db" || my.pw != "s3cret#1" {
		t.Errorf("mysql %+v %q", my.c, my.pw)
	}
	pg := got[1]
	if pg.c.Driver != Postgres || pg.c.Host != "db.internal" || pg.c.Port != 5433 || pg.c.User != "app" || pg.pw != "pw" || pg.c.TLS != TLSRequired {
		t.Errorf("postgres %+v %q", pg.c, pg.pw)
	}
	rd := got[2]
	if rd.c.Driver != Redis || rd.c.Port != 6380 || rd.pw != "" {
		t.Errorf("redis %+v %q", rd.c, rd.pw)
	}
	if envName(env) != EnvProduction {
		t.Error("APP_ENV=production is not production")
	}
}

func TestAProductionConnectionMadeWithoutSafetyIsReadOnly(t *testing.T) {
	s, _ := testStore(t)
	c, err := s.Add(Connection{ProjectID: "p", Driver: MySQL, Env: EnvProduction}, Secrets{})
	if err != nil || !c.Security.ReadOnly || !c.Security.ConfirmWrites {
		t.Fatalf("%+v %v", c.Security, err)
	}
	// one that says what it wants keeps it
	c, _ = s.Add(Connection{ProjectID: "p", Driver: MySQL, Env: EnvProduction, Security: Security{ConfirmWrites: true}}, Secrets{})
	if c.Security.ReadOnly {
		t.Error("an explicit full access was made read-only")
	}
}

func TestTunnelFormsTheHubReadsAsTheOwnerMeantThem(t *testing.T) {
	// a Host of ~/.ssh/config chosen after the form had made an empty SSH server: the Host is the way
	r := Reach{Kind: ReachSSH, ConfigHost: " db-bastion ", Hops: []Hop{{Host: "", User: ""}}}
	if err := normalizeReach(&r); err != nil {
		t.Fatalf("a config Host with a leftover empty server: %v", err)
	}
	if r.ConfigHost != "db-bastion" || len(r.Hops) != 0 {
		t.Fatalf("reach %+v", r)
	}
	// a server left empty says which, in the form's words
	for _, c := range []struct {
		hops []Hop
		want string
	}{
		{[]Hop{{Host: "db-1", User: ""}}, "SSH tunnel: fill in the host and user name of the SSH server"},
		{[]Hop{{Host: "", User: "deploy"}, {Host: "db-1", User: "deploy"}}, "SSH tunnel: fill in the host and user name of jump server 1"},
		{nil, "SSH tunnel: fill in the SSH server, or choose a Host of ~/.ssh/config"},
	} {
		r := Reach{Kind: ReachSSH, Hops: c.hops}
		err := normalizeReach(&r)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v, want %q", c.hops, err, c.want)
		}
	}
}
