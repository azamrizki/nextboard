// Package mcpreg keeps the MCP servers of NextBoard. They are set once here and
// reach every agent at launch, whichever CLI it runs in, so switching agents
// keeps the same tools (docs/API-agents.md §23). Nothing is written to any CLI's
// own config. Header and environment values are sealed at rest, leave the hub
// only through the environment of the process that needs them, and are never
// sent back to a client.
package mcpreg

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/secret"
	"github.com/azamrizki/nextboard/internal/store"
)

const (
	TransportHTTP  = "http"
	TransportStdio = "stdio"

	maxServers = 32
	maxVars    = 16
	maxArgs    = 32
	maxValue   = 8 << 10
	storeName  = "mcp-servers.json"
)

var (
	idRe     = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	headerRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	envRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

	ErrNotFound = errors.New("MCP server not found")
	ErrExists   = errors.New("the MCP server name is already used")
	ErrTooMany  = errors.New("there are already too many MCP servers (at most 32)")
)

// BadError is an invalid server: the message says what to fix.
type BadError struct{ Msg string }

func (e *BadError) Error() string { return e.Msg }

func bad(format string, a ...any) error { return &BadError{Msg: fmt.Sprintf(format, a...)} }

// Var is a header or an environment variable. A client sends Value, an empty one
// keeps what is stored; it only ever reads Set.
type Var struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
	Set   bool   `json:"set"`
}

// Server is one MCP server.
type Server struct {
	ID          string   `json:"id"`
	Description string   `json:"description,omitempty"`
	Transport   string   `json:"transport"`
	URL         string   `json:"url,omitempty"`
	Headers     []Var    `json:"headers"`
	Command     string   `json:"command,omitempty"`
	Args        []string `json:"args"`
	Env         []Var    `json:"env"`
	// Projects limits it to these projects; empty is every project.
	Projects []string `json:"projects"`
	// OnServers offers it to consoles that work on a server. The CLI runs on
	// This PC, so a command-based server would act on This PC: off unless asked.
	OnServers bool      `json:"onServers"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Store is the registry.
type Store struct {
	mu   sync.Mutex
	st   *store.Store
	box  *secret.Box
	list []Server // Value holds the sealed value
}

// Open loads the registry.
func Open(st *store.Store, box *secret.Box) (*Store, error) {
	s := &Store{st: st, box: box}
	if _, err := st.Load(storeName, &s.list); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) saveLocked() error { return s.st.Save(storeName, s.list) }

// masked is a copy safe to give to a client.
func masked(sv Server) Server {
	mask := func(vars []Var) []Var {
		out := make([]Var, len(vars))
		for i, v := range vars {
			out[i] = Var{Name: v.Name, Set: v.Value != ""}
		}
		return out
	}
	sv.Headers, sv.Env = mask(sv.Headers), mask(sv.Env)
	sv.Args = append([]string{}, sv.Args...)
	sv.Projects = append([]string{}, sv.Projects...)
	return sv
}

// List gives every server, without its values.
func (s *Store) List() []Server {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Server, len(s.list))
	for i, sv := range s.list {
		out[i] = masked(sv)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *Store) findLocked(id string) int {
	for i := range s.list {
		if s.list[i].ID == id {
			return i
		}
	}
	return -1
}

func (s *Store) seal(value string) (string, error) {
	b, err := s.box.Seal([]byte(value))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func (s *Store) open(sealed string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", err
	}
	v, err := s.box.Open(b)
	if err != nil {
		return "", err
	}
	return string(v), nil
}

// clean checks a server and seals its new values; old is what it replaces.
func (s *Store) clean(in Server, old *Server) (Server, error) {
	in.Description = strings.Join(strings.Fields(in.Description), " ")
	if r := []rune(in.Description); len(r) > 200 {
		in.Description = string(r[:200])
	}
	if !idRe.MatchString(in.ID) {
		return Server{}, bad("the name must start with a lowercase letter, then lowercase letters, digits, - or _, at most 32")
	}
	if in.ID == "nextboard" {
		return Server{}, bad(`the name "nextboard" is used by the hub itself`)
	}
	sealVars := func(vars []Var, nameRe *regexp.Regexp, what string, prev []Var) ([]Var, error) {
		if len(vars) > maxVars {
			return nil, bad("too many %ss (at most %d)", what, maxVars)
		}
		out := make([]Var, 0, len(vars))
		seen := map[string]bool{}
		for _, v := range vars {
			v.Name = strings.TrimSpace(v.Name)
			if !nameRe.MatchString(v.Name) {
				return nil, bad("invalid %s name %q", what, v.Name)
			}
			if seen[strings.ToLower(v.Name)] {
				return nil, bad("%s %q is written twice", what, v.Name)
			}
			seen[strings.ToLower(v.Name)] = true
			if len(v.Value) > maxValue {
				return nil, bad("the value of %s %q is too long", what, v.Name)
			}
			sealed := ""
			if v.Value != "" {
				var err error
				if sealed, err = s.seal(v.Value); err != nil {
					return nil, err
				}
			} else {
				for _, p := range prev {
					if p.Name == v.Name {
						sealed = p.Value
					}
				}
				if sealed == "" {
					return nil, bad("%s %q has no value yet", what, v.Name)
				}
			}
			out = append(out, Var{Name: v.Name, Value: sealed})
		}
		return out, nil
	}
	var prevHeaders, prevEnv []Var
	if old != nil {
		prevHeaders, prevEnv = old.Headers, old.Env
	}
	var err error
	switch in.Transport {
	case TransportHTTP:
		u, perr := url.Parse(strings.TrimSpace(in.URL))
		if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return Server{}, bad("the URL must be http:// or https://")
		}
		if u.User != nil {
			return Server{}, bad("do not put a password in the URL; use a header")
		}
		in.URL = u.String()
		in.Command, in.Args, in.Env = "", []string{}, []Var{}
		if in.Headers, err = sealVars(in.Headers, headerRe, "header", prevHeaders); err != nil {
			return Server{}, err
		}
	case TransportStdio:
		in.Command = strings.TrimSpace(in.Command)
		if in.Command == "" {
			return Server{}, bad("empty command")
		}
		if len(in.Args) > maxArgs {
			return Server{}, bad("too many arguments (at most %d)", maxArgs)
		}
		if in.Args == nil {
			in.Args = []string{}
		}
		in.URL, in.Headers = "", []Var{}
		if in.Env, err = sealVars(in.Env, envRe, "variable", prevEnv); err != nil {
			return Server{}, err
		}
	default:
		return Server{}, bad(`transport must be "http" or "stdio"`)
	}
	if in.Projects == nil {
		in.Projects = []string{}
	}
	return in, nil
}

// Create adds a server.
func (s *Store) Create(in Server) (Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.findLocked(in.ID) >= 0 {
		return Server{}, ErrExists
	}
	if len(s.list) >= maxServers {
		return Server{}, ErrTooMany
	}
	sv, err := s.clean(in, nil)
	if err != nil {
		return Server{}, err
	}
	sv.CreatedAt = time.Now().UTC()
	sv.UpdatedAt = sv.CreatedAt
	s.list = append(s.list, sv)
	if err := s.saveLocked(); err != nil {
		return Server{}, err
	}
	return masked(sv), nil
}

// Update replaces a server. A variable sent without a value keeps its value, one
// left out is removed.
func (s *Store) Update(id string, in Server) (Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.findLocked(id)
	if i < 0 {
		return Server{}, ErrNotFound
	}
	in.ID = id
	sv, err := s.clean(in, &s.list[i])
	if err != nil {
		return Server{}, err
	}
	sv.CreatedAt = s.list[i].CreatedAt
	sv.UpdatedAt = time.Now().UTC()
	s.list[i] = sv
	if err := s.saveLocked(); err != nil {
		return Server{}, err
	}
	return masked(sv), nil
}

// Delete removes a server.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.findLocked(id)
	if i < 0 {
		return ErrNotFound
	}
	s.list = append(s.list[:i], s.list[i+1:]...)
	return s.saveLocked()
}

// opened is a server with its values in the clear, for the hub's own use.
func (s *Store) opened(id string) (Server, error) {
	s.mu.Lock()
	i := s.findLocked(id)
	if i < 0 {
		s.mu.Unlock()
		return Server{}, ErrNotFound
	}
	sv := s.list[i]
	s.mu.Unlock()
	var err error
	if sv.Headers, err = s.openVars(sv.Headers); err != nil {
		return Server{}, err
	}
	if sv.Env, err = s.openVars(sv.Env); err != nil {
		return Server{}, err
	}
	return sv, nil
}

func (s *Store) openVars(vars []Var) ([]Var, error) {
	out := make([]Var, len(vars))
	for i, v := range vars {
		plain, err := s.open(v.Value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", v.Name, err)
		}
		out[i] = Var{Name: v.Name, Value: plain}
	}
	return out, nil
}

func (sv Server) reaches(projectID string, remote bool) bool {
	if !sv.Enabled || (remote && !sv.OnServers) {
		return false
	}
	if len(sv.Projects) == 0 {
		return true
	}
	for _, p := range sv.Projects {
		if p == projectID {
			return true
		}
	}
	return false
}

// ForLaunch gives what one launch gets: the servers, naming the process
// environment variables that hold their values, and those variables. A value that
// cannot be opened leaves its server out rather than fail the console.
func (s *Store) ForLaunch(projectID string, remote bool) ([]agent.MCPServer, []string) {
	var servers []agent.MCPServer
	var env []string
	for _, m := range s.List() {
		if !m.reaches(projectID, remote) {
			continue
		}
		sv, err := s.opened(m.ID)
		if err != nil {
			continue
		}
		out := agent.MCPServer{Name: sv.ID, URL: sv.URL, Command: sv.Command, Args: sv.Args}
		n := len(servers)
		bind := func(vars []Var) []agent.MCPVar {
			list := make([]agent.MCPVar, len(vars))
			for i, v := range vars {
				name := fmt.Sprintf("NB_MCP_%d_%s_%d", n, strings.NewReplacer("-", "_").Replace(strings.ToUpper(sv.ID)), len(env))
				list[i] = agent.MCPVar{Name: v.Name, Var: name}
				env = append(env, name+"="+v.Value)
			}
			return list
		}
		out.Headers, out.Env = bind(sv.Headers), bind(sv.Env)
		servers = append(servers, out)
	}
	return servers, env
}
