// Package providers keeps the API providers (docs/API-agents.md §10.2): an Anthropic- or
// OpenAI-compatible endpoint, its key and its models, which a console of Claude Code, Codex or
// OpenCode can run against instead of a subscription login. The key is sealed with the hub's
// master key, like an SSH credential; it never goes to a client, into the settings document, a
// command line or a log, only into the environment of a process that uses it.
package providers

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/secret"
	"github.com/azamrizki/nextboard/internal/store"
)

const storeName = "providers.json"

// Kinds of endpoint.
const (
	KindAnthropic = "anthropic" // POST /v1/messages
	KindOpenAI    = "openai"    // /chat/completions or /responses
)

// OpenAI API styles. Codex speaks only the Responses API since 0.165.
const (
	APIChat      = "chat"
	APIResponses = "responses"
)

// Anthropic auth headers: Bearer is what Anthropic-compatible services expect from Claude Code
// (ANTHROPIC_AUTH_TOKEN); x-api-key is Anthropic's own (ANTHROPIC_API_KEY).
const (
	AuthBearer = "bearer"
	AuthAPIKey = "x-api-key"
)

const (
	maxProviders = 40
	maxModels    = 200
)

var (
	ErrNotFound = errors.New("provider not found")
	ErrInvalid  = errors.New("invalid provider")
	idRe        = regexp.MustCompile(`^ap_[a-z0-9]{4,24}$`)
	modelRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+\-\[\]]{0,159}$`)
)

// ValidID reports whether id has the shape of a provider id.
func ValidID(id string) bool { return idRe.MatchString(id) }

// Price is what a model costs, in US dollars per million tokens.
type Price struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead,omitempty"`
	CacheWrite float64 `json:"cacheWrite,omitempty"`
}

// Provider is one endpoint, as clients see it: without its key.
type Provider struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	BaseURL   string `json:"baseUrl"`
	API       string `json:"api,omitempty"`  // openai: chat or responses
	Auth      string `json:"auth,omitempty"` // anthropic: bearer or x-api-key
	Model     string `json:"model"`
	FastModel string `json:"fastModel,omitempty"`
	// Models are the ones it is known to have, from a test of the connection or typed.
	Models []string         `json:"models,omitempty"`
	Prices map[string]Price `json:"prices,omitempty"`
	// Budget is US dollars per calendar month; 0 = none.
	Budget    float64   `json:"budget,omitempty"`
	HasKey    bool      `json:"hasKey"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// stored is a provider with its sealed key.
type stored struct {
	Provider
	Key string `json:"key,omitempty"` // base64 of the sealed key
}

// Input is what POST and PATCH take. Key "" keeps the key a provider has.
type Input struct {
	Name      *string           `json:"name"`
	Kind      *string           `json:"kind"`
	BaseURL   *string           `json:"baseUrl"`
	API       *string           `json:"api"`
	Auth      *string           `json:"auth"`
	Model     *string           `json:"model"`
	FastModel *string           `json:"fastModel"`
	Models    *[]string         `json:"models"`
	Prices    *map[string]Price `json:"prices"`
	Budget    *float64          `json:"budget"`
	Key       *string           `json:"apiKey"`
}

// Store is the list of providers.
type Store struct {
	mu   sync.Mutex
	st   *store.Store
	box  *secret.Box
	list []stored
}

// Open loads the providers.
func Open(st *store.Store, box *secret.Box) (*Store, error) {
	s := &Store{st: st, box: box}
	if _, err := st.Load(storeName, &s.list); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) saveLocked() error { return s.st.Save(storeName, s.list) }

func (s *Store) findLocked(id string) int {
	return slices.IndexFunc(s.list, func(p stored) bool { return p.ID == id })
}

func public(p stored) Provider {
	out := p.Provider
	out.HasKey = p.Key != ""
	out.Models = slices.Clone(p.Models)
	if p.Prices != nil {
		out.Prices = make(map[string]Price, len(p.Prices))
		for k, v := range p.Prices {
			out.Prices[k] = v
		}
	}
	return out
}

// List gives every provider, without keys, by name.
func (s *Store) List() []Provider {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Provider, len(s.list))
	for i, p := range s.list {
		out[i] = public(p)
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// Get gives one provider, without its key.
func (s *Store) Get(id string) (Provider, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.findLocked(id)
	if i < 0 {
		return Provider{}, false
	}
	return public(s.list[i]), true
}

// Key opens the key of a provider, for the environment of a process that uses it.
func (s *Store) Key(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.findLocked(id)
	if i < 0 {
		return "", ErrNotFound
	}
	if s.list[i].Key == "" {
		return "", nil
	}
	b, err := base64.StdEncoding.DecodeString(s.list[i].Key)
	if err != nil {
		return "", err
	}
	v, err := s.box.Open(b)
	if err != nil {
		return "", err
	}
	return string(v), nil
}

// Add makes a provider.
func (s *Store) Add(in Input) (Provider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.list) >= maxProviders {
		return Provider{}, fmt.Errorf("%w: at most %d providers", ErrInvalid, maxProviders)
	}
	now := time.Now().UTC()
	p := stored{Provider: Provider{ID: auth.RandomID("ap_"), CreatedAt: now, UpdatedAt: now}}
	if err := s.applyLocked(&p, in, true); err != nil {
		return Provider{}, err
	}
	s.list = append(s.list, p)
	if err := s.saveLocked(); err != nil {
		s.list = s.list[:len(s.list)-1]
		return Provider{}, err
	}
	return public(p), nil
}

// Update changes the fields given.
func (s *Store) Update(id string, in Input) (Provider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.findLocked(id)
	if i < 0 {
		return Provider{}, ErrNotFound
	}
	p := s.list[i]
	if err := s.applyLocked(&p, in, false); err != nil {
		return Provider{}, err
	}
	p.UpdatedAt = time.Now().UTC()
	old := s.list[i]
	s.list[i] = p
	if err := s.saveLocked(); err != nil {
		s.list[i] = old
		return Provider{}, err
	}
	return public(p), nil
}

// NoteModels remembers the models a test of the connection found.
func (s *Store) NoteModels(id string, models []string) (Provider, error) {
	return s.Update(id, Input{Models: &models})
}

// Delete removes a provider and its key.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.findLocked(id)
	if i < 0 {
		return ErrNotFound
	}
	old := slices.Clone(s.list)
	s.list = slices.Delete(s.list, i, i+1)
	if err := s.saveLocked(); err != nil {
		s.list = old
		return err
	}
	return nil
}

func bad(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// applyLocked checks the input and puts it into p; full means every required field must be there.
func (s *Store) applyLocked(p *stored, in Input, full bool) error {
	str := func(v *string, cur string) string {
		if v == nil {
			return cur
		}
		return strings.TrimSpace(*v)
	}
	p.Name = str(in.Name, p.Name)
	p.Kind = str(in.Kind, p.Kind)
	p.BaseURL = strings.TrimRight(str(in.BaseURL, p.BaseURL), "/")
	p.API = str(in.API, p.API)
	p.Auth = str(in.Auth, p.Auth)
	p.Model = str(in.Model, p.Model)
	p.FastModel = str(in.FastModel, p.FastModel)

	if n := len([]rune(p.Name)); n == 0 || n > 40 || strings.IndexFunc(p.Name, unicode.IsControl) >= 0 {
		return bad("name: 1 to 40 characters")
	}
	if slices.ContainsFunc(s.list, func(o stored) bool { return o.ID != p.ID && strings.EqualFold(o.Name, p.Name) }) {
		return bad("there is already a provider called %q", p.Name)
	}
	switch p.Kind {
	case KindAnthropic:
		p.API = ""
		p.Auth = cmpOr(p.Auth, AuthBearer)
		if p.Auth != AuthBearer && p.Auth != AuthAPIKey {
			return bad("auth: bearer or x-api-key")
		}
		// Claude Code adds /v1/messages itself
		p.BaseURL = strings.TrimSuffix(p.BaseURL, "/v1")
	case KindOpenAI:
		p.Auth = ""
		p.API = cmpOr(p.API, APIChat)
		if p.API != APIChat && p.API != APIResponses {
			return bad("api: chat or responses")
		}
	default:
		return bad("kind: anthropic or openai")
	}
	if err := checkBaseURL(p.BaseURL); err != nil {
		return err
	}
	if !modelRe.MatchString(p.Model) {
		return bad("model: the name of a model, such as glm-4.6 or openai/gpt-5")
	}
	if p.FastModel != "" && !modelRe.MatchString(p.FastModel) {
		return bad("fastModel: the name of a model")
	}
	if in.Models != nil {
		var models []string
		for _, m := range *in.Models {
			m = strings.TrimSpace(m)
			if modelRe.MatchString(m) && !slices.Contains(models, m) {
				models = append(models, m)
			}
			if len(models) == maxModels {
				break
			}
		}
		sort.Strings(models)
		p.Models = models
	}
	if in.Prices != nil {
		prices := map[string]Price{}
		for m, pr := range *in.Prices {
			if !modelRe.MatchString(m) {
				return bad("prices: %q is not the name of a model", m)
			}
			for _, v := range []float64{pr.Input, pr.Output, pr.CacheRead, pr.CacheWrite} {
				if v < 0 || v > 10000 || math.IsNaN(v) || math.IsInf(v, 0) {
					return bad("prices of %s: dollars per million tokens, 0 to 10000", m)
				}
			}
			prices[m] = pr
		}
		if len(prices) > maxModels {
			return bad("prices: at most %d models", maxModels)
		}
		p.Prices = prices
	}
	if in.Budget != nil {
		b := *in.Budget
		if b < 0 || b > 1e6 || math.IsNaN(b) || math.IsInf(b, 0) {
			return bad("budget: dollars per month, 0 for none")
		}
		p.Budget = math.Round(b*100) / 100
	}
	if in.Key != nil && *in.Key != "" {
		key := strings.TrimSpace(*in.Key)
		if key == "" || len(key) > 4096 || strings.ContainsAny(key, "\n\r\x00 ") {
			return bad("apiKey: one line, no spaces")
		}
		sealed, err := s.box.Seal([]byte(key))
		if err != nil {
			return err
		}
		p.Key = base64.StdEncoding.EncodeToString(sealed)
	}
	if full && p.Key == "" && !isLocal(p.BaseURL) {
		return bad("apiKey: the key of the account (a server on this machine may do without)")
	}
	return nil
}

// CheckDraft checks what a test of a connection needs: the kind, the address and the model.
func CheckDraft(p Provider) error {
	switch p.Kind {
	case KindAnthropic:
		if p.Auth != "" && p.Auth != AuthBearer && p.Auth != AuthAPIKey {
			return bad("auth: bearer or x-api-key")
		}
	case KindOpenAI:
		if p.API != "" && p.API != APIChat && p.API != APIResponses {
			return bad("api: chat or responses")
		}
	default:
		return bad("kind: anthropic or openai")
	}
	if err := checkBaseURL(p.BaseURL); err != nil {
		return err
	}
	if p.Model != "" && !modelRe.MatchString(p.Model) {
		return bad("model: the name of a model")
	}
	return nil
}

// checkBaseURL takes http or https only, and nothing that could break out of the TOML or JSON it
// goes into for Codex and OpenCode.
func checkBaseURL(raw string) error {
	if raw == "" || len(raw) > 300 || strings.ContainsAny(raw, "\"'\\ \t\n\r\x00`$<>{}|^") {
		return bad("baseUrl: an http or https address")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return bad("baseUrl: an http or https address, without a login, query or #")
	}
	return nil
}

// isLocal is a server on this machine, which often needs no key (Ollama, LM Studio).
func isLocal(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	h := u.Hostname()
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Agents a provider can run, by the CLI they are built on: Claude Code speaks Anthropic's
// Messages API, Codex only OpenAI's Responses API, OpenCode both styles of both.
func Compatible(base string, p Provider) bool {
	switch base {
	case "claude":
		return p.Kind == KindAnthropic
	case "codex":
		return p.Kind == KindOpenAI && p.API == APIResponses
	case "opencode":
		return true
	}
	return false
}

// Cost of tokens at a price, in dollars.
func (pr Price) Cost(input, output, cacheRead, cacheWrite int64) float64 {
	return (float64(input)*pr.Input + float64(output)*pr.Output + float64(cacheRead)*pr.CacheRead + float64(cacheWrite)*pr.CacheWrite) / 1e6
}
