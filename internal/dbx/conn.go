// Package dbx is the database browser (docs/API-db.md): connections that belong to a project,
// reached straight, through a machine of the project or through an SSH tunnel; their schema,
// rows, SQL and Redis keys; what agents may do with them. The hub does the connecting, so the
// browser never has to reach a database.
package dbx

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Driver names a database driver.
type Driver string

const (
	MySQL    Driver = "mysql"
	MariaDB  Driver = "mariadb"
	Postgres Driver = "postgres"
	Redis    Driver = "redis"
	Valkey   Driver = "valkey"
)

// Drivers are the ones built in, in the order the wizard shows them.
var Drivers = []Driver{MySQL, MariaDB, Postgres, Redis, Valkey}

// IsSQL says the driver speaks SQL (tables, rows, statements).
func (d Driver) IsSQL() bool { return d == MySQL || d == MariaDB || d == Postgres }

// IsKV says the driver is a key-value store (keys, values, commands).
func (d Driver) IsKV() bool { return d == Redis || d == Valkey }

// DefaultPort is the driver's usual port.
func (d Driver) DefaultPort() int {
	switch d {
	case MySQL, MariaDB:
		return 3306
	case Postgres:
		return 5432
	case Redis, Valkey:
		return 6379
	}
	return 0
}

// Env is a connection type: a colour and the defaults of its safety.
type Env string

const (
	EnvLocal       Env = "local"
	EnvDevelopment Env = "development"
	EnvStaging     Env = "staging"
	EnvProduction  Env = "production"
)

var envs = []Env{EnvLocal, EnvDevelopment, EnvStaging, EnvProduction}

// Reach kinds: how the hub gets to the database.
const (
	ReachDirect  = "direct"  // the hub dials it itself
	ReachMachine = "machine" // over the SSH NextBoard keeps to a machine of the project
	ReachSSH     = "ssh"     // an SSH tunnel of its own: hops, or a Host of ~/.ssh/config
)

// Hop auth methods.
const (
	AuthPassword = "password"
	AuthKey      = "key"
	AuthAgent    = "agent"
)

// Hop is one SSH server on the way. The last one is the SSH host, the ones before it jumps.
type Hop struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	User string `json:"user"`
	Auth string `json:"auth"`
	// KeyPath is a private key on This PC (~ allowed); empty with an uploaded key.
	KeyPath string `json:"keyPath,omitempty"`
	// HasKey: a private key was uploaded and is kept sealed.
	HasKey bool `json:"hasKey,omitempty"`
	// HasSecret: a password or a key's passphrase is kept sealed.
	HasSecret bool `json:"hasSecret,omitempty"`
	// HostKey is the SHA256 fingerprint trusted for this server ("SHA256:…"); empty until trusted
	// or when ~/.ssh/known_hosts knows it.
	HostKey string `json:"hostKey,omitempty"`
}

func (h Hop) addr() string { return fmt.Sprintf("%s:%d", h.Host, h.Port) }

// Reach is how the hub reaches the database.
type Reach struct {
	Kind      string `json:"kind"`
	MachineID string `json:"machineId,omitempty"`
	// ConfigHost is a Host of This PC's ~/.ssh/config, used instead of Hops.
	ConfigHost string `json:"configHost,omitempty"`
	Hops       []Hop  `json:"hops,omitempty"`
	// RemoteHost and RemotePort override the connection's host and port as the SSH host sees them.
	RemoteHost string `json:"remoteHost,omitempty"`
	RemotePort int    `json:"remotePort,omitempty"`
	// OpenLocal opens 127.0.0.1:LocalPort on This PC while the tunnel is up, for agents and tools.
	OpenLocal    bool `json:"openLocal,omitempty"`
	LocalPort    int  `json:"localPort,omitempty"`
	KeepAliveSec int  `json:"keepAliveSec,omitempty"`
	TimeoutSec   int  `json:"timeoutSec,omitempty"`
	Reconnect    bool `json:"reconnect,omitempty"`
}

// Security is what may be done on the connection.
type Security struct {
	ReadOnly      bool `json:"readOnly"`
	ConfirmWrites bool `json:"confirmWrites"`
	ConfirmEdits  bool `json:"confirmEdits"`
	AutoCommit    bool `json:"autoCommit"`
}

// Agent access levels.
const (
	AgentsNone  = "none"
	AgentsRead  = "read"
	AgentsAsk   = "ask"
	AgentsWrite = "write"
)

// Agents is what the project's agents may do with the connection.
type Agents struct {
	Access string `json:"access"`
	// Scope "project": every agent of the project; "wired": the consoles wired to its card.
	Scope  string   `json:"scope"`
	Masked []string `json:"masked"`
	// SmartDB features in the viewer: on unless turned off (absent from an older or shorter
	// connection means on).
	AskBar   *bool `json:"askBar,omitempty"`
	EditorAI *bool `json:"editorAi,omitempty"`
}

// On says whether a SmartDB switch is on: absent is on.
func On(b *bool) bool { return b == nil || *b }

func yes() *bool { t := true; return &t }

// DefaultMasked are the columns agents never see unless the owner says otherwise.
var DefaultMasked = []string{"*password*", "*token*", "*secret*", "*api_key*"}

// Limits bound a query and a page.
type Limits struct {
	TimeoutSec int `json:"timeoutSec"`
	PageSize   int `json:"pageSize"`
}

// TLS modes.
const (
	TLSOff       = "off"
	TLSPreferred = "preferred"
	TLSRequired  = "required"
	TLSVerifyCA  = "verify-ca"
)

// Connection is what the API shows of a connection: never its secrets.
type Connection struct {
	ID          string `json:"id"`
	ProjectID   string `json:"projectId"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Group       string `json:"group,omitempty"`
	Driver      Driver `json:"driver"`
	Env         Env    `json:"env"`

	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database,omitempty"`
	User     string `json:"user,omitempty"`
	// ShowAll lists every database of the server, not only Database.
	ShowAll     bool `json:"showAll,omitempty"`
	ServerTZ    bool `json:"serverTz,omitempty"`
	HasPassword bool `json:"hasPassword"`
	// SavePassword false: the password is asked when connecting and kept in memory only.
	SavePassword bool              `json:"savePassword"`
	TLS          string            `json:"tls"`
	Props        map[string]string `json:"props,omitempty"`
	RedisDB      int               `json:"redisDb,omitempty"`

	Reach    Reach    `json:"reach"`
	Security Security `json:"security"`
	Agents   Agents   `json:"agents"`
	Limits   Limits   `json:"limits"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Errors the API turns into status codes.
var (
	ErrNotFound = errors.New("no such connection")
	ErrInvalid  = errors.New("invalid")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Defaults of a connection type: Production starts read-only and asks before writes.
func defaultSecurity(e Env) Security {
	switch e {
	case EnvProduction:
		return Security{ReadOnly: true, ConfirmWrites: true, ConfirmEdits: true}
	case EnvStaging:
		return Security{ConfirmWrites: true, ConfirmEdits: true}
	}
	return Security{ConfirmWrites: false, ConfirmEdits: true, AutoCommit: true}
}

// normalize fills defaults and checks a connection; err is ErrInvalid with what is wrong.
func normalize(c *Connection) error {
	c.Name = strings.TrimSpace(c.Name)
	c.Host = strings.TrimSpace(c.Host)
	c.Database = strings.TrimSpace(c.Database)
	c.User = strings.TrimSpace(c.User)
	if !slices.Contains(Drivers, c.Driver) {
		return invalid("driver: one of mysql, mariadb, postgres, redis, valkey")
	}
	if c.Name == "" {
		c.Name = c.Database
		if c.Name == "" {
			c.Name = string(c.Driver)
		}
	}
	if len(c.Name) > 80 {
		return invalid("name: at most 80 characters")
	}
	if c.Env == "" {
		c.Env = EnvLocal
	}
	if !slices.Contains(envs, c.Env) {
		return invalid("env: local, development, staging or production")
	}
	if c.Host == "" {
		c.Host = "127.0.0.1"
	}
	if c.Port == 0 {
		c.Port = c.Driver.DefaultPort()
	}
	if c.Port < 1 || c.Port > 65535 {
		return invalid("port: 1 to 65535")
	}
	switch c.TLS {
	case "":
		c.TLS = TLSOff
	case TLSOff, TLSPreferred, TLSRequired, TLSVerifyCA:
	default:
		return invalid("tls: off, preferred, required or verify-ca")
	}
	if c.RedisDB < 0 || c.RedisDB > 255 {
		return invalid("redisDb: 0 to 255")
	}
	if len(c.Props) > 50 {
		return invalid("props: at most 50")
	}
	if err := normalizeReach(&c.Reach); err != nil {
		return err
	}
	switch c.Agents.Access {
	case "":
		c.Agents.Access = AgentsAsk
	case AgentsNone, AgentsRead, AgentsAsk, AgentsWrite:
	default:
		return invalid("agents.access: none, read, ask or write")
	}
	if c.Agents.AskBar == nil {
		c.Agents.AskBar = yes()
	}
	if c.Agents.EditorAI == nil {
		c.Agents.EditorAI = yes()
	}
	switch c.Agents.Scope {
	case "":
		c.Agents.Scope = "project"
	case "project", "wired":
	default:
		return invalid("agents.scope: project or wired")
	}
	if c.Agents.Masked == nil {
		c.Agents.Masked = slices.Clone(DefaultMasked)
	}
	if len(c.Agents.Masked) > 100 {
		return invalid("agents.masked: at most 100")
	}
	if c.Limits.TimeoutSec == 0 {
		c.Limits.TimeoutSec = 60
	}
	if c.Limits.TimeoutSec < 1 || c.Limits.TimeoutSec > 3600 {
		return invalid("limits.timeoutSec: 1 to 3600")
	}
	if c.Limits.PageSize == 0 {
		c.Limits.PageSize = 100
	}
	if c.Limits.PageSize < 10 || c.Limits.PageSize > 5000 {
		return invalid("limits.pageSize: 10 to 5000")
	}
	return nil
}

// hopName is how the form names a hop: the last one is the SSH server, those before it jump servers.
func hopName(i, n int) string {
	if i == n-1 {
		return "the SSH server"
	}
	return fmt.Sprintf("jump server %d", i+1)
}

func normalizeReach(r *Reach) error {
	switch r.Kind {
	case "":
		r.Kind = ReachDirect
	case ReachDirect:
	case ReachMachine:
		if r.MachineID == "" {
			return invalid("reach.machineId: which machine")
		}
	case ReachSSH:
		r.ConfigHost = strings.TrimSpace(r.ConfigHost)
		if r.ConfigHost != "" {
			// the Host brings its own chain (HostName, User, ProxyJump): servers a form kept
			// from before it was chosen are not the way
			r.Hops = nil
		}
		if r.ConfigHost == "" && len(r.Hops) == 0 {
			return invalid("SSH tunnel: fill in the SSH server, or choose a Host of ~/.ssh/config")
		}
		if len(r.Hops) > 8 {
			return invalid("reach.hops: at most 8")
		}
		for i := range r.Hops {
			h := &r.Hops[i]
			h.Host = strings.TrimSpace(h.Host)
			h.User = strings.TrimSpace(h.User)
			if h.Host == "" || h.User == "" {
				return invalid("SSH tunnel: fill in the host and user name of %s", hopName(i, len(r.Hops)))
			}
			if h.Port == 0 {
				h.Port = 22
			}
			if h.Port < 1 || h.Port > 65535 {
				return invalid("SSH tunnel: the port of %s is 1 to 65535", hopName(i, len(r.Hops)))
			}
			switch h.Auth {
			case "":
				h.Auth = AuthKey
			case AuthPassword, AuthKey, AuthAgent:
			default:
				return invalid("reach.hops[%d].auth: password, key or agent", i)
			}
		}
	default:
		return invalid("reach.kind: direct, machine or ssh")
	}
	if r.Kind != ReachSSH {
		r.Hops, r.ConfigHost, r.OpenLocal, r.LocalPort = nil, "", false, 0
	}
	if r.OpenLocal && (r.LocalPort < 1024 || r.LocalPort > 65535) {
		return invalid("reach.localPort: 1024 to 65535")
	}
	if r.KeepAliveSec == 0 {
		r.KeepAliveSec = 30
	}
	if r.TimeoutSec == 0 {
		r.TimeoutSec = 15
	}
	if r.KeepAliveSec < 5 || r.KeepAliveSec > 600 || r.TimeoutSec < 2 || r.TimeoutSec > 120 {
		return invalid("reach: keep-alive 5 to 600 s, timeout 2 to 120 s")
	}
	return nil
}
