package dbx

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/secret"
	"github.com/azamrizki/nextboard/internal/store"
)

const (
	dirName      = "dbs"
	connsFile    = "dbs/connections.json"
	queriesFile  = "dbs/queries.json"
	maxConns     = 200
	maxQueries   = 1000
	maxHistory   = 500
	maxActivity  = 2000
	maxSQLLength = 256 << 10
)

// stored is a connection with its sealed secrets (base64 of secret.Box blobs).
type stored struct {
	Connection
	Password string `json:"password,omitempty"`
	// per hop of Reach.Hops: a password or a key's passphrase, and an uploaded private key
	HopSecrets []string `json:"hopSecrets,omitempty"`
	HopKeys    []string `json:"hopKeys,omitempty"`
}

// Secrets are what a form sends besides the connection. A nil field keeps what is stored; ""
// removes it.
type Secrets struct {
	Password   *string   `json:"password,omitempty"`
	HopSecrets []*string `json:"hopSecrets,omitempty"`
	HopKeys    []*string `json:"hopKeys,omitempty"`
}

// opened are a connection's secrets, opened for the moment of connecting.
type opened struct {
	Password   string
	HopSecrets []string
	HopKeys    []string
}

// Query is a saved query of a project.
type Query struct {
	ID           string    `json:"id"`
	ProjectID    string    `json:"projectId"`
	ConnectionID string    `json:"connectionId,omitempty"`
	Name         string    `json:"name"`
	SQL          string    `json:"sql"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// HistoryItem is one statement run on a connection (its text, never its rows).
type HistoryItem struct {
	At    time.Time `json:"at"`
	SQL   string    `json:"sql"`
	Kind  Kind      `json:"kind"`
	Rows  int64     `json:"rows"`
	MS    int64     `json:"ms"`
	Error string    `json:"error,omitempty"`
	// Who: "" for the owner, else the agent's handle or "SmartDB"
	Who string `json:"who,omitempty"`
}

// Activity is what an agent did or asked on a connection.
type Activity struct {
	At    time.Time `json:"at"`
	Who   string    `json:"who"`
	SQL   string    `json:"sql"`
	Rows  int64     `json:"rows"`
	What  string    `json:"what"` // read | approved | refused | denied | ran | error
	Note  string    `json:"note,omitempty"`
	Error string    `json:"error,omitempty"`
}

// Store keeps the connections of every project and their saved queries.
type Store struct {
	mu      sync.Mutex
	st      *store.Store
	box     *secret.Box
	list    []stored
	queries []Query
	logMu   sync.Mutex
}

// Open loads the store.
func Open(st *store.Store, box *secret.Box) (*Store, error) {
	if err := os.MkdirAll(st.Path(dirName), 0o700); err != nil {
		return nil, err
	}
	s := &Store{st: st, box: box}
	if _, err := st.Load(connsFile, &s.list); err != nil {
		return nil, err
	}
	if _, err := st.Load(queriesFile, &s.queries); err != nil {
		return nil, err
	}
	return s, nil
}

func newID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

func (s *Store) saveLocked() error { return s.st.Save(connsFile, s.list) }

func (s *Store) find(id string) int {
	return slices.IndexFunc(s.list, func(c stored) bool { return c.ID == id })
}

func public(c stored) Connection {
	out := c.Connection
	out.HasPassword = c.Password != ""
	out.Reach.Hops = slices.Clone(c.Reach.Hops)
	for i := range out.Reach.Hops {
		out.Reach.Hops[i].HasSecret = i < len(c.HopSecrets) && c.HopSecrets[i] != ""
		out.Reach.Hops[i].HasKey = i < len(c.HopKeys) && c.HopKeys[i] != ""
	}
	out.Agents.Masked = slices.Clone(c.Agents.Masked)
	if c.Props != nil {
		out.Props = make(map[string]string, len(c.Props))
		for k, v := range c.Props {
			out.Props[k] = v
		}
	}
	return out
}

// List gives a project's connections ("" for every project), by group and name.
func (s *Store) List(projectID string) []Connection {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Connection
	for _, c := range s.list {
		if projectID == "" || c.ProjectID == projectID {
			out = append(out, public(c))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// Get gives one connection.
func (s *Store) Get(id string) (Connection, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.find(id)
	if i < 0 {
		return Connection{}, false
	}
	return public(s.list[i]), true
}

func (s *Store) seal(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	b, err := s.box.Seal([]byte(v))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func (s *Store) unseal(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return "", err
	}
	p, err := s.box.Open(b)
	if err != nil {
		return "", err
	}
	return string(p), nil
}

// applySecrets seals what sec says into c; hops are matched by index.
func (s *Store) applySecrets(c *stored, sec Secrets) error {
	if sec.Password != nil {
		v, err := s.seal(*sec.Password)
		if err != nil {
			return err
		}
		c.Password = v
	}
	n := len(c.Reach.Hops)
	resize := func(l []string) []string {
		for len(l) < n {
			l = append(l, "")
		}
		return l[:n]
	}
	c.HopSecrets, c.HopKeys = resize(c.HopSecrets), resize(c.HopKeys)
	for i := 0; i < n; i++ {
		if i < len(sec.HopSecrets) && sec.HopSecrets[i] != nil {
			v, err := s.seal(*sec.HopSecrets[i])
			if err != nil {
				return err
			}
			c.HopSecrets[i] = v
		}
		if i < len(sec.HopKeys) && sec.HopKeys[i] != nil {
			if k := *sec.HopKeys[i]; k != "" && !strings.Contains(k, "PRIVATE KEY") {
				return invalid("reach.hops[%d]: the uploaded key is not a private key", i)
			}
			v, err := s.seal(*sec.HopKeys[i])
			if err != nil {
				return err
			}
			c.HopKeys[i] = v
		}
	}
	if !c.SavePassword && c.Password != "" && sec.Password == nil {
		c.Password = ""
	}
	return nil
}

// Add makes a connection of a project.
func (s *Store) Add(c Connection, sec Secrets) (Connection, error) {
	if err := normalize(&c); err != nil {
		return Connection{}, err
	}
	// a request that says nothing of safety gets its type's: Production starts read-only
	if c.Security == (Security{}) {
		c.Security = defaultSecurity(c.Env)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.list) >= maxConns {
		return Connection{}, invalid("at most %d connections", maxConns)
	}
	now := time.Now().UTC()
	c.ID, c.CreatedAt, c.UpdatedAt = newID("db_"), now, now
	st := stored{Connection: c}
	if err := s.applySecrets(&st, sec); err != nil {
		return Connection{}, err
	}
	if !c.SavePassword {
		st.Password = ""
	}
	s.list = append(s.list, st)
	if err := s.saveLocked(); err != nil {
		s.list = s.list[:len(s.list)-1]
		return Connection{}, err
	}
	return public(st), nil
}

// Replace writes a connection's settings (all of them, as the edit dialog sends them); hops
// keep their secrets by index unless sec changes them.
func (s *Store) Replace(id string, c Connection, sec Secrets) (Connection, error) {
	if err := normalize(&c); err != nil {
		return Connection{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.find(id)
	if i < 0 {
		return Connection{}, ErrNotFound
	}
	old := s.list[i]
	c.ID, c.ProjectID, c.CreatedAt, c.UpdatedAt = old.ID, old.ProjectID, old.CreatedAt, time.Now().UTC()
	st := stored{Connection: c, Password: old.Password, HopSecrets: old.HopSecrets, HopKeys: old.HopKeys}
	if err := s.applySecrets(&st, sec); err != nil {
		return Connection{}, err
	}
	if !c.SavePassword && sec.Password == nil {
		st.Password = ""
	}
	s.list[i] = st
	if err := s.saveLocked(); err != nil {
		s.list[i] = old
		return Connection{}, err
	}
	return public(st), nil
}

// TrustHostKey pins the host key of a hop.
func (s *Store) TrustHostKey(id string, hop int, fingerprint string) (Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.find(id)
	if i < 0 {
		return Connection{}, ErrNotFound
	}
	if hop < 0 || hop >= len(s.list[i].Reach.Hops) {
		return Connection{}, invalid("no hop %d", hop)
	}
	s.list[i].Reach.Hops[hop].HostKey = fingerprint
	s.list[i].UpdatedAt = time.Now().UTC()
	if err := s.saveLocked(); err != nil {
		return Connection{}, err
	}
	return public(s.list[i]), nil
}

// Delete removes a connection, its history and its activity.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.find(id)
	if i < 0 {
		return ErrNotFound
	}
	old := slices.Clone(s.list)
	s.list = slices.Delete(s.list, i, i+1)
	if err := s.saveLocked(); err != nil {
		s.list = old
		return err
	}
	_ = os.RemoveAll(s.st.Path(dirName, id))
	return nil
}

// DeleteProject removes the connections and saved queries of a project that is gone.
func (s *Store) DeleteProject(projectID string) []string {
	s.mu.Lock()
	var gone []string
	keep := s.list[:0:0]
	for _, c := range s.list {
		if c.ProjectID == projectID {
			gone = append(gone, c.ID)
			continue
		}
		keep = append(keep, c)
	}
	s.list = keep
	_ = s.saveLocked()
	s.queries = slices.DeleteFunc(s.queries, func(q Query) bool { return q.ProjectID == projectID })
	_ = s.st.Save(queriesFile, s.queries)
	s.mu.Unlock()
	for _, id := range gone {
		_ = os.RemoveAll(s.st.Path(dirName, id))
	}
	return gone
}

// secrets opens what is sealed for a connection.
func (s *Store) secrets(id string) (opened, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.find(id)
	if i < 0 {
		return opened{}, ErrNotFound
	}
	c := s.list[i]
	var o opened
	var err error
	if o.Password, err = s.unseal(c.Password); err != nil {
		return opened{}, err
	}
	for k := range c.Reach.Hops {
		var sec, key string
		if k < len(c.HopSecrets) {
			if sec, err = s.unseal(c.HopSecrets[k]); err != nil {
				return opened{}, err
			}
		}
		if k < len(c.HopKeys) {
			if key, err = s.unseal(c.HopKeys[k]); err != nil {
				return opened{}, err
			}
		}
		o.HopSecrets = append(o.HopSecrets, sec)
		o.HopKeys = append(o.HopKeys, key)
	}
	return o, nil
}

// ---- saved queries ---------------------------------------------------------------------------

// Queries gives a project's saved queries.
func (s *Store) Queries(projectID string) []Query {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Query
	for _, q := range s.queries {
		if q.ProjectID == projectID {
			out = append(out, q)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// SaveQuery adds a query (no ID) or replaces one.
func (s *Store) SaveQuery(q Query) (Query, error) {
	q.Name = strings.TrimSpace(q.Name)
	if q.Name == "" || len(q.Name) > 120 {
		return Query{}, invalid("name: 1 to 120 characters")
	}
	if len(q.SQL) > maxSQLLength {
		return Query{}, invalid("sql: at most 256 KB")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	q.UpdatedAt = time.Now().UTC()
	if q.ID == "" {
		if len(s.queries) >= maxQueries {
			return Query{}, invalid("at most %d saved queries", maxQueries)
		}
		q.ID = newID("q_")
		s.queries = append(s.queries, q)
	} else {
		i := slices.IndexFunc(s.queries, func(x Query) bool { return x.ID == q.ID })
		if i < 0 || s.queries[i].ProjectID != q.ProjectID {
			return Query{}, ErrNotFound
		}
		s.queries[i] = q
	}
	return q, s.st.Save(queriesFile, s.queries)
}

// DeleteQuery removes a saved query of a project.
func (s *Store) DeleteQuery(projectID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.queries, func(x Query) bool { return x.ID == id && x.ProjectID == projectID })
	if i < 0 {
		return ErrNotFound
	}
	s.queries = slices.Delete(s.queries, i, i+1)
	return s.st.Save(queriesFile, s.queries)
}

// ---- history and activity (append-only, trimmed) ---------------------------------------------

func (s *Store) appendLine(id, name string, v any, max int) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	dir := s.st.Path(dirName, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	path := filepath.Join(dir, name)
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
	st, _ := f.Stat()
	f.Close()
	// keep the newest max lines: trim when the file is about twice as long as it needs to be
	if st != nil && st.Size() > int64(max)*600 {
		lines := readLines(path)
		if len(lines) > max {
			lines = lines[len(lines)-max:]
			_ = store.WriteFileAtomic(path, []byte(strings.Join(lines, "\n")+"\n"))
		}
	}
}

func readLines(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 2<<20)
	for sc.Scan() {
		if l := sc.Text(); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func readLast[T any](path string, limit int) []T {
	lines := readLines(path)
	if limit > 0 && len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	out := make([]T, 0, len(lines))
	for i := len(lines) - 1; i >= 0; i-- { // newest first
		var v T
		if json.Unmarshal([]byte(lines[i]), &v) == nil {
			out = append(out, v)
		}
	}
	return out
}

// AddHistory records a statement of a connection.
func (s *Store) AddHistory(id string, h HistoryItem) {
	if len(h.SQL) > 16<<10 {
		h.SQL = h.SQL[:16<<10] + "…"
	}
	s.appendLine(id, "history.jsonl", h, maxHistory)
}

// History gives a connection's statements, newest first.
func (s *Store) History(id string, limit int) []HistoryItem {
	return readLast[HistoryItem](s.st.Path(dirName, id, "history.jsonl"), limit)
}

// AddActivity records what an agent did on a connection.
func (s *Store) AddActivity(id string, a Activity) {
	if len(a.SQL) > 8<<10 {
		a.SQL = a.SQL[:8<<10] + "…"
	}
	s.appendLine(id, "activity.jsonl", a, maxActivity)
}

// ActivityLog gives what agents did on a connection, newest first.
func (s *Store) ActivityLog(id string, limit int) []Activity {
	return readLast[Activity](s.st.Path(dirName, id, "activity.jsonl"), limit)
}

// errNoPassword: the connection does not keep its password and none was given.
var errNoPassword = errors.New("password_needed")
