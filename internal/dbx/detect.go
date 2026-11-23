package dbx

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
)

// Folder is a part of a project, where Detect looks.
type Folder struct {
	Part      string
	MachineID string
	Host      machine.Host
	Path      string
	// Local: the part is on This PC, so its databases are reached straight
	Local bool
}

// Candidate is a connection found in a project's files. Its password stays on the hub: Ref
// lets Add read it again from the file.
type Candidate struct {
	Source      string     `json:"source"`
	Part        string     `json:"part,omitempty"`
	Connection  Connection `json:"connection"`
	HasPassword bool       `json:"hasPassword"`
	Ref         string     `json:"ref"`
}

type detectRef struct {
	MachineID string `json:"m"`
	File      string `json:"f"`
	Kind      string `json:"k"` // "db", "redis", "database_url", "redis_url"
}

const envMax = 256 << 10

// parseEnv reads KEY=VALUE lines of a .env file.
func parseEnv(s string) map[string]string {
	out := map[string]string{}
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "export "))
		if l == "" || l[0] == '#' {
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if len(v) > 0 && (v[0] == '"' || v[0] == '\'') {
			// a quoted value ends at its closing quote; a comment may follow
			if j := strings.IndexByte(v[1:], v[0]); j >= 0 {
				v = v[1 : j+1]
			}
		} else if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		out[k] = v
	}
	return out
}

func envName(e map[string]string) Env {
	switch strings.ToLower(e["APP_ENV"] + e["NODE_ENV"] + e["RAILS_ENV"]) {
	case "production", "prod":
		return EnvProduction
	case "staging", "stage":
		return EnvStaging
	case "development", "dev":
		return EnvDevelopment
	}
	return EnvLocal
}

// fromURL reads mysql://, postgres:// or redis:// URLs; the password comes back apart.
func fromURL(raw string) (Connection, string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return Connection{}, "", false
	}
	c := Connection{Host: u.Hostname(), User: u.User.Username()}
	pw, _ := u.User.Password()
	c.Port, _ = strconv.Atoi(u.Port())
	switch strings.ToLower(u.Scheme) {
	case "mysql", "mysql2":
		c.Driver = MySQL
	case "mariadb":
		c.Driver = MariaDB
	case "postgres", "postgresql", "pgsql":
		c.Driver = Postgres
	case "redis", "rediss":
		c.Driver = Redis
		if u.Scheme == "rediss" {
			c.TLS = TLSRequired
		}
		if db, err := strconv.Atoi(strings.Trim(u.Path, "/")); err == nil {
			c.RedisDB = db
		}
		return c, pw, true
	default:
		return Connection{}, "", false
	}
	c.Database = strings.Trim(u.Path, "/")
	if sm := u.Query().Get("sslmode"); sm == "require" || sm == "verify-full" || sm == "verify-ca" {
		c.TLS = TLSRequired
	}
	return c, pw, true
}

// fromEnv reads the connections a .env names; the password of each is given by key.
func fromEnv(e map[string]string) []struct {
	c    Connection
	pw   string
	kind string
} {
	type item = struct {
		c    Connection
		pw   string
		kind string
	}
	var out []item
	if d := strings.ToLower(e["DB_CONNECTION"]); d != "" && e["DB_DATABASE"] != "" {
		c := Connection{Host: e["DB_HOST"], Database: e["DB_DATABASE"], User: e["DB_USERNAME"]}
		c.Port, _ = strconv.Atoi(e["DB_PORT"])
		switch d {
		case "mysql":
			c.Driver = MySQL
		case "mariadb":
			c.Driver = MariaDB
		case "pgsql", "postgres", "postgresql":
			c.Driver = Postgres
		}
		if c.Driver != "" {
			out = append(out, item{c, e["DB_PASSWORD"], "db"})
		}
	}
	if raw := e["DATABASE_URL"]; raw != "" {
		if c, pw, ok := fromURL(raw); ok {
			out = append(out, item{c, pw, "database_url"})
		}
	}
	if raw := e["REDIS_URL"]; raw != "" {
		if c, pw, ok := fromURL(raw); ok {
			out = append(out, item{c, pw, "redis_url"})
		}
	} else if h := e["REDIS_HOST"]; h != "" && !strings.EqualFold(e["REDIS_CLIENT"], "none") {
		c := Connection{Driver: Redis, Host: h, User: e["REDIS_USERNAME"]}
		c.Port, _ = strconv.Atoi(e["REDIS_PORT"])
		c.RedisDB, _ = strconv.Atoi(e["REDIS_DB"])
		pw := e["REDIS_PASSWORD"]
		if strings.EqualFold(pw, "null") {
			pw = ""
		}
		out = append(out, item{c, pw, "redis"})
	}
	for i := range out {
		if out[i].c.Host == "" || strings.EqualFold(out[i].c.Host, "localhost") {
			out[i].c.Host = "127.0.0.1"
		}
		if strings.EqualFold(out[i].c.User, "null") {
			out[i].c.User = ""
		}
	}
	return out
}

func envFiles(ctx context.Context, f Folder) []string {
	files := []string{path.Join(f.Path, ".env")}
	if l, err := f.Host.ListDir(ctx, f.Path); err == nil {
		n := 0
		for _, e := range l.Entries {
			if !e.Dir || strings.HasPrefix(e.Name, ".") || e.Name == "node_modules" || e.Name == "vendor" {
				continue
			}
			files = append(files, path.Join(f.Path, e.Name, ".env"))
			if n++; n >= 24 {
				break
			}
		}
	}
	return files
}

// Detect looks for connections in the .env files of a project's parts (their root and the
// folders right below).
func Detect(ctx context.Context, folders []Folder) []Candidate {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var out []Candidate
	seen := map[string]bool{}
	for _, f := range folders {
		for _, file := range envFiles(ctx, f) {
			fc, err := f.Host.ReadFile(ctx, file, envMax)
			if err != nil || fc.Binary {
				continue
			}
			env := parseEnv(fc.Content)
			for _, it := range fromEnv(env) {
				c := it.c
				c.Env = envName(env)
				c.Name = orDefault(c.Database, strings.ToLower(string(c.Driver)))
				if f.Local {
					c.Reach = Reach{Kind: ReachDirect}
				} else {
					c.Reach = Reach{Kind: ReachMachine, MachineID: f.MachineID}
				}
				key := f.MachineID + "|" + string(c.Driver) + "|" + c.Host + "|" + strconv.Itoa(c.Port) + "|" + c.Database + "|" + c.User
				if seen[key] {
					continue
				}
				seen[key] = true
				if err := normalize(&c); err != nil {
					continue
				}
				c.SavePassword = true
				ref, _ := json.Marshal(detectRef{MachineID: f.MachineID, File: file, Kind: it.kind})
				rel := strings.TrimPrefix(strings.TrimPrefix(file, f.Path), "/")
				if f.Part != "" {
					rel = f.Part + "/" + rel
				}
				out = append(out, Candidate{Source: rel, Part: f.Part, Connection: c, HasPassword: it.pw != "", Ref: base64.RawURLEncoding.EncodeToString(ref)})
			}
		}
	}
	return out
}

// DetectedPassword reads again the password a candidate's file holds.
func DetectedPassword(ctx context.Context, host func(id string) (machine.Host, bool), ref string) (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(ref)
	if err != nil {
		return "", invalid("detect ref")
	}
	var r detectRef
	if err := json.Unmarshal(b, &r); err != nil || path.Base(r.File) != ".env" {
		return "", invalid("detect ref")
	}
	h, ok := host(r.MachineID)
	if !ok {
		return "", invalid("the machine of that file is not there")
	}
	fc, err := h.ReadFile(ctx, r.File, envMax)
	if err != nil {
		return "", err
	}
	env := parseEnv(fc.Content)
	for _, it := range fromEnv(env) {
		if it.kind == r.Kind {
			return it.pw, nil
		}
	}
	return "", invalid("the file no longer names that connection")
}
