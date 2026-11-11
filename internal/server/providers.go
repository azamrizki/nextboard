package server

// API providers (docs/API-agents.md §10.2): the store, consoles that run on one, the test of a
// connection, and what a provider costs this month. The key leaves the store only into the
// environment of a process that uses it.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/jobs"
	"github.com/azamrizki/nextboard/internal/orch"
	"github.com/azamrizki/nextboard/internal/providers"
	"github.com/azamrizki/nextboard/internal/session"
	"github.com/azamrizki/nextboard/internal/usage"
)

// ---- consoles on a provider (session.ProviderHub)

type providerHub struct{ s *Server }

func (h providerHub) Check(id string, a agent.Agent) error {
	p, ok := h.s.providers.Get(id)
	if !ok {
		return fmt.Errorf("%w: it was removed", session.ErrProviderNotFound)
	}
	if !providers.Compatible(a.CapBase(), p) {
		return fmt.Errorf("%w: %s", session.ErrProviderMismatch, compatHint(a, p))
	}
	if p.Budget > 0 {
		if sp := h.s.providerSpend(p, monthOf(time.Now())); sp.Cost >= p.Budget {
			return fmt.Errorf("%w: %s has spent $%.2f of its $%.2f for %s", session.ErrProviderBudget, p.Name, sp.Cost, p.Budget, sp.Month)
		}
	}
	return nil
}

func compatHint(a agent.Agent, p providers.Provider) string {
	switch a.CapBase() {
	case "claude":
		return a.Name + " needs an Anthropic-compatible provider"
	case "codex":
		return a.Name + " needs an OpenAI provider with the Responses API"
	}
	return a.Name + " cannot use API providers; Claude Code, Codex and OpenCode can"
}

func (h providerHub) Launch(id string, a agent.Agent) (*agent.ProviderLaunch, error) {
	if err := h.Check(id, a); err != nil {
		return nil, err
	}
	p, ok := h.s.providers.Get(id)
	if !ok {
		return nil, session.ErrProviderNotFound
	}
	key, err := h.s.providers.Key(id)
	if err != nil {
		return nil, fmt.Errorf("the key of %s cannot be read: %w", p.Name, err)
	}
	dir, err := h.s.providerConfigDir(p, a.CapBase(), key)
	if err != nil {
		return nil, err
	}
	return &agent.ProviderLaunch{ID: p.ID, Name: p.Name, Kind: p.Kind, BaseURL: p.BaseURL, API: p.API, Auth: p.Auth,
		Model: p.Model, FastModel: p.FastModel, Key: key, ConfigDir: dir}, nil
}

func (h providerHub) ConfigDir(id string, a agent.Agent) string {
	switch base := a.CapBase(); base {
	case "claude", "codex":
		return h.s.st.Path("providers", id, base)
	}
	return ""
}

// providerConfigDir makes the CLI's own folder for a provider: apart from every login, so the
// subscription is neither used nor overwritten, and with Claude Code's first-run questions answered.
func (s *Server) providerConfigDir(p providers.Provider, base, key string) (string, error) {
	if base != "claude" && base != "codex" {
		return "", nil
	}
	dir := s.st.Path("providers", p.ID, base)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if base == "claude" {
		if err := seedClaudeConfig(dir, p, key); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// seedClaudeConfig answers what Claude Code asks on its first start in a new folder: the theme
// and the onboarding, and for an x-api-key provider whether to use the key it found.
func seedClaudeConfig(dir string, p providers.Provider, key string) error {
	path := filepath.Join(dir, ".claude.json")
	doc := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &doc)
	}
	doc["hasCompletedOnboarding"] = true
	doc["lspRecommendationDisabled"] = true
	if _, ok := doc["theme"]; !ok {
		doc["theme"] = "dark"
	}
	if p.Auth == providers.AuthAPIKey && key != "" {
		// Claude Code remembers a key it was told to use by its last 20 characters
		suffix := key[max(0, len(key)-20):]
		approved := []any{}
		if r, ok := doc["customApiKeyResponses"].(map[string]any); ok {
			if a, ok := r["approved"].([]any); ok {
				approved = a
			}
		}
		if !slices.Contains(approved, any(suffix)) {
			approved = append(approved, suffix)
		}
		doc["customApiKeyResponses"] = map[string]any{"approved": approved, "rejected": []any{}}
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ---- what a provider costs

// ProviderSpend is GET /api/providers/{id}/spend.
type ProviderSpend struct {
	Month    string       `json:"month"`
	Cost     float64      `json:"cost"`
	Budget   float64      `json:"budget,omitempty"`
	Tokens   usage.Tokens `json:"tokens"`
	ByModel  []ModelSpend `json:"byModel"`
	Unpriced []string     `json:"unpriced,omitempty"`
}

// ModelSpend is one model's share.
type ModelSpend struct {
	Model string `json:"model"`
	usage.Tokens
	Cost   float64 `json:"cost"`
	Priced bool    `json:"priced"`
}

// spendCache keeps the turns of transcripts that did not change.
type spendCache struct {
	mu    sync.Mutex
	files map[string]cachedTurns
}

type cachedTurns struct {
	size  int64
	mtime time.Time
	turns []usage.Turn
}

func monthOf(t time.Time) string { return t.Local().Format("2006-01") }

// providerTurns are the turns of a provider's consoles (their transcripts in its folders) and
// of the hidden runs made through it (its ledger).
func (s *Server) providerTurns(id string) []usage.Turn {
	var out []usage.Turn
	c := &s.spend
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.files == nil {
		c.files = map[string]cachedTurns{}
	}
	for _, base := range []string{"claude", "codex"} {
		for _, fi := range usage.TranscriptFiles(s.st.Path("providers", id, base), base) {
			path := usage.PathOf(fi)
			if ct, ok := c.files[path]; ok && ct.size == fi.Size() && ct.mtime.Equal(fi.ModTime()) {
				out = append(out, ct.turns...)
				continue
			}
			turns, err := usage.FileTurns(path, base)
			if err != nil {
				continue
			}
			c.files[path] = cachedTurns{size: fi.Size(), mtime: fi.ModTime(), turns: turns}
			out = append(out, turns...)
		}
	}
	return append(out, s.ledgerTurns(id)...)
}

// priceOf finds a model's price; OpenCode names models provider/model.
func priceOf(p providers.Provider, model string) (providers.Price, bool) {
	if pr, ok := p.Prices[model]; ok {
		return pr, true
	}
	if _, rest, ok := strings.Cut(model, "/"); ok {
		if pr, ok := p.Prices[rest]; ok {
			return pr, true
		}
	}
	return providers.Price{}, false
}

// costOf sums turns into a spend.
func costOf(p providers.Provider, month string, turns []usage.Turn) ProviderSpend {
	sp := ProviderSpend{Month: month, Budget: p.Budget, ByModel: []ModelSpend{}}
	by := map[string]*ModelSpend{}
	for _, t := range turns {
		if month != "" && monthOf(t.At) != month {
			continue
		}
		m := by[t.Model]
		if m == nil {
			_, ok := priceOf(p, t.Model)
			m = &ModelSpend{Model: t.Model, Priced: ok}
			by[t.Model] = m
		}
		m.Input += t.Input
		m.Output += t.Output
		m.CacheRead += t.CacheRead
		m.CacheWrite += t.CacheWrite
	}
	for _, m := range by {
		if pr, ok := priceOf(p, m.Model); ok {
			m.Cost = pr.Cost(m.Input, m.Output, m.CacheRead, m.CacheWrite)
		} else if m.Input+m.Output > 0 {
			sp.Unpriced = append(sp.Unpriced, m.Model)
		}
		sp.Cost += m.Cost
		sp.Tokens.Input += m.Input
		sp.Tokens.Output += m.Output
		sp.Tokens.CacheRead += m.CacheRead
		sp.Tokens.CacheWrite += m.CacheWrite
		sp.ByModel = append(sp.ByModel, *m)
	}
	sort.Slice(sp.ByModel, func(i, j int) bool {
		return sp.ByModel[i].Cost > sp.ByModel[j].Cost || (sp.ByModel[i].Cost == sp.ByModel[j].Cost && sp.ByModel[i].Model < sp.ByModel[j].Model)
	})
	sort.Strings(sp.Unpriced)
	return sp
}

func (s *Server) providerSpend(p providers.Provider, month string) ProviderSpend {
	return costOf(p, month, s.providerTurns(p.ID))
}

// ---- the ledger of hidden runs

type ledgerEntry struct {
	At      time.Time `json:"at"`
	Model   string    `json:"model"`
	Purpose string    `json:"purpose,omitempty"`
	usage.Tokens
}

// noteProviderRun adds a hidden run's tokens to the provider's ledger: those runs keep no
// transcript, so this is the only record of what they cost.
func (s *Server) noteProviderRun(id, model, purpose string, t usage.Tokens) {
	if id == "" || t.Input+t.Output+t.CacheRead+t.CacheWrite == 0 {
		return
	}
	b, _ := json.Marshal(ledgerEntry{At: time.Now().UTC(), Model: model, Purpose: purpose, Tokens: t})
	dir := s.st.Path("providers", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	s.spend.mu.Lock()
	defer s.spend.mu.Unlock()
	f, err := os.OpenFile(filepath.Join(dir, "ledger.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

func (s *Server) ledgerTurns(id string) []usage.Turn {
	b, err := os.ReadFile(filepath.Join(s.st.Path("providers", id), "ledger.jsonl"))
	if err != nil {
		return nil
	}
	var out []usage.Turn
	for _, l := range bytes.Split(b, []byte("\n")) {
		var e ledgerEntry
		if json.Unmarshal(l, &e) == nil && !e.At.IsZero() {
			out = append(out, usage.Turn{At: e.At, Model: e.Model, Tokens: e.Tokens})
		}
	}
	return out
}

// ---- API

type providerView struct {
	providers.Provider
	MonthCost float64 `json:"monthCost"`
	InUse     int     `json:"inUse"`
}

func (s *Server) providerViews() []providerView {
	month := monthOf(time.Now())
	use := map[string]int{}
	for _, in := range s.sessions.List() {
		if in.ProviderID != "" && in.Status == session.StatusRunning {
			use[in.ProviderID]++
		}
	}
	out := []providerView{}
	for _, p := range s.providers.List() {
		out = append(out, providerView{Provider: p, MonthCost: s.providerSpend(p, month).Cost, InUse: use[p.ID]})
	}
	return out
}

func (s *Server) publishProvider(id string) {
	for _, v := range s.providerViews() {
		if v.ID == id {
			s.events.Publish(map[string]any{"type": "provider", "provider": v})
			return
		}
	}
}

func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request, p *principal) {
	writeJSON(w, http.StatusOK, map[string]any{"items": s.providerViews()})
}

func providerErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, providers.ErrNotFound):
		writeErrCode(w, http.StatusNotFound, "provider_not_found", err.Error())
	case errors.Is(err, providers.ErrInvalid):
		writeErrCode(w, http.StatusBadRequest, "bad_provider", strings.TrimPrefix(err.Error(), providers.ErrInvalid.Error()+": "))
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) handleProviderCreate(w http.ResponseWriter, r *http.Request, p *principal) {
	var in providers.Input
	if !decode(w, r, &in) {
		return
	}
	pv, err := s.providers.Add(in)
	if err != nil {
		providerErr(w, err)
		return
	}
	s.publishProvider(pv.ID)
	writeJSON(w, http.StatusCreated, pv)
}

func (s *Server) handleProviderUpdate(w http.ResponseWriter, r *http.Request, p *principal) {
	var in providers.Input
	if !decode(w, r, &in) {
		return
	}
	pv, err := s.providers.Update(r.PathValue("id"), in)
	if err != nil {
		providerErr(w, err)
		return
	}
	s.publishProvider(pv.ID)
	writeJSON(w, http.StatusOK, pv)
}

func (s *Server) handleProviderDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	id := r.PathValue("id")
	for _, in := range s.sessions.List() {
		if in.ProviderID == id && in.Status == session.StatusRunning {
			writeErrCode(w, http.StatusConflict, "provider_in_use", "consoles still run on this provider: close or restart them first")
			return
		}
	}
	if err := s.providers.Delete(id); err != nil {
		providerErr(w, err)
		return
	}
	// nothing may keep naming it: the hidden runs, the lead, the lead's consoles (broadcast by the store)
	set := s.settings.Get()
	patch := map[string]any{}
	if set.Runner.Provider == id {
		patch["runner"] = map[string]any{"provider": nil}
	}
	lead := map[string]any{}
	if set.Lead.Runner == id {
		lead["runner"] = nil
	}
	gone := map[string]any{}
	for ag, pid := range set.Lead.Providers {
		if pid == id {
			gone[ag] = nil
		}
	}
	if len(gone) > 0 {
		lead["providers"] = gone
	}
	if len(lead) > 0 {
		patch["lead"] = lead
	}
	if len(patch) > 0 {
		b, _ := json.Marshal(patch)
		_, _ = s.settings.Patch(b)
	}
	if r.URL.Query().Get("purge") == "1" {
		_ = os.RemoveAll(s.st.Path("providers", id))
	}
	s.events.Publish(map[string]any{"type": "providerRemoved", "id": id})
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handleProviderSpend(w http.ResponseWriter, r *http.Request, p *principal) {
	pv, ok := s.providers.Get(r.PathValue("id"))
	if !ok {
		writeErrCode(w, http.StatusNotFound, "provider_not_found", "provider not found")
		return
	}
	month := r.URL.Query().Get("month")
	if month == "" {
		month = monthOf(time.Now())
	} else if _, err := time.Parse("2006-01", month); err != nil {
		writeErr(w, http.StatusBadRequest, "month: YYYY-MM")
		return
	}
	writeJSON(w, http.StatusOK, s.providerSpend(pv, month))
}

// handleSessionCost is what a console on a provider has cost: every turn of its conversation.
func (s *Server) handleSessionCost(w http.ResponseWriter, r *http.Request, p *principal) {
	sess, ok := s.sessionByPath(w, r)
	if !ok {
		return
	}
	sp := sess.Spec()
	pv, ok := s.providers.Get(sp.ProviderID)
	if sp.ProviderID == "" || !ok {
		writeErrCode(w, http.StatusConflict, "not_on_provider", "this console runs on a login, not an API provider")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	out := costOf(pv, "", nil)
	if _, kind, path, err := sess.TranscriptLocation(ctx); err == nil && path != "" {
		if turns, err := usage.FileTurns(path, kind); err == nil {
			out = costOf(pv, "", turns)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- testing a connection

// providerTest is POST /api/providers/test: a provider as typed in the form, or a saved one (id),
// with the key typed or else the saved one.
type providerTest struct {
	ID string `json:"id"`
	providers.Input
}

type testResult struct {
	OK     bool     `json:"ok"`
	Models []string `json:"models,omitempty"`
	// Checked says how: by its list of models, or by a one-token message when it has no list.
	Checked string `json:"checked,omitempty"`
	Status  int    `json:"status,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (s *Server) handleProviderTest(w http.ResponseWriter, r *http.Request, p *principal) {
	var req providerTest
	if !decode(w, r, &req) {
		return
	}
	var base providers.Provider
	key := ""
	if req.ID != "" {
		pv, ok := s.providers.Get(req.ID)
		if !ok {
			writeErrCode(w, http.StatusNotFound, "provider_not_found", "provider not found")
			return
		}
		base = pv
		key, _ = s.providers.Key(req.ID)
	}
	// the draft over the saved one
	pick := func(v *string, cur string) string {
		if v != nil {
			return strings.TrimSpace(*v)
		}
		return cur
	}
	base.Kind, base.BaseURL = pick(req.Kind, base.Kind), strings.TrimRight(pick(req.BaseURL, base.BaseURL), "/")
	base.API, base.Auth, base.Model = pick(req.API, base.API), pick(req.Auth, base.Auth), pick(req.Model, base.Model)
	if req.Key != nil && strings.TrimSpace(*req.Key) != "" {
		key = strings.TrimSpace(*req.Key)
	}
	if err := providers.CheckDraft(base); err != nil {
		providerErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	res := testProvider(ctx, base, key)
	if res.OK && req.ID != "" && len(res.Models) > 0 {
		if _, err := s.providers.NoteModels(req.ID, res.Models); err == nil {
			s.publishProvider(req.ID)
		}
	}
	writeJSON(w, http.StatusOK, res)
}

// testProvider asks for the list of models, and when there is none, sends one message of one
// token. Neither says the key in what it answers.
func testProvider(ctx context.Context, p providers.Provider, key string) testResult {
	client := &http.Client{Timeout: 20 * time.Second}
	headers := func(req *http.Request) {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "NextBoard")
		if p.Kind == providers.KindAnthropic {
			req.Header.Set("anthropic-version", "2023-06-01")
			if p.Auth == providers.AuthAPIKey {
				req.Header.Set("x-api-key", key)
			} else if key != "" {
				req.Header.Set("Authorization", "Bearer "+key)
			}
		} else if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
	do := func(method, url string, body any) (int, []byte, error) {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, rd)
		if err != nil {
			return 0, nil, err
		}
		headers(req)
		resp, err := client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		return resp.StatusCode, b, nil
	}
	clean := func(b []byte) string {
		t := strings.Join(strings.Fields(string(b)), " ")
		if key != "" {
			t = strings.ReplaceAll(t, key, "…")
		}
		if r := []rune(t); len(r) > 240 {
			t = string(r[:240]) + "…"
		}
		return t
	}
	fail := func(status int, b []byte, err error) testResult {
		switch {
		case err != nil:
			msg := err.Error()
			if key != "" {
				msg = strings.ReplaceAll(msg, key, "…")
			}
			return testResult{Error: "could not reach it: " + msg}
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			return testResult{Status: status, Error: "the key was refused: " + clean(b)}
		}
		return testResult{Status: status, Error: fmt.Sprintf("it answered %d: %s", status, clean(b))}
	}

	modelsURL := p.BaseURL + "/models"
	if p.Kind == providers.KindAnthropic {
		modelsURL = p.BaseURL + "/v1/models"
	}
	status, b, err := do(http.MethodGet, modelsURL, nil)
	if err == nil && status == http.StatusOK {
		var list struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			Models []struct {
				Name string `json:"name"`
				ID   string `json:"id"`
			} `json:"models"`
		}
		if json.Unmarshal(b, &list) == nil {
			var models []string
			for _, m := range list.Data {
				models = append(models, m.ID)
			}
			for _, m := range list.Models {
				models = append(models, cmpOrStr(m.ID, m.Name))
			}
			if len(models) > 0 {
				return testResult{OK: true, Models: models, Checked: "models"}
			}
		}
	}
	if err == nil && (status == http.StatusUnauthorized || status == http.StatusForbidden) {
		return fail(status, b, nil)
	}
	// no list: one message of one token
	switch {
	case p.Kind == providers.KindAnthropic:
		status, b, err = do(http.MethodPost, p.BaseURL+"/v1/messages", map[string]any{
			"model": p.Model, "max_tokens": 1, "messages": []any{map[string]string{"role": "user", "content": "ping"}}})
	case p.API == providers.APIResponses:
		status, b, err = do(http.MethodPost, p.BaseURL+"/responses", map[string]any{"model": p.Model, "input": "ping", "max_output_tokens": 16})
	default:
		status, b, err = do(http.MethodPost, p.BaseURL+"/chat/completions", map[string]any{
			"model": p.Model, "max_tokens": 1, "messages": []any{map[string]string{"role": "user", "content": "ping"}}})
	}
	if err != nil || status/100 != 2 {
		return fail(status, b, err)
	}
	return testResult{OK: true, Checked: "message"}
}

func cmpOrStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ---- hidden runs (settings.runner.provider, lead.runner)

// providerRunner is the CLI that runs hidden runs on provider id: Claude Code on an Anthropic API,
// Codex on the Responses API. The runner has no OpenCode, so a Chat Completions provider has none.
func (s *Server) providerRunner(id string) (string, string, error) {
	p, ok := s.providers.Get(id)
	if !ok {
		return "", "", fmt.Errorf("%w: it was removed", session.ErrProviderNotFound)
	}
	for _, base := range []string{"claude", "codex"} {
		if providers.Compatible(base, p) {
			return base, p.Name, nil
		}
	}
	return "", "", fmt.Errorf("%w: hidden runs need an Anthropic-compatible API (Claude Code) or the Responses API (Codex), and %s has neither", session.ErrProviderMismatch, p.Name)
}

// providerInfos lists the providers with the agents that are on and can run on each, for the lead.
func (s *Server) providerInfos() []orch.ProviderInfo {
	off := s.settings.Get().Agents.Disabled
	out := []orch.ProviderInfo{}
	for _, p := range s.providers.List() {
		pi := orch.ProviderInfo{ID: p.ID, Name: p.Name, Agents: []string{}}
		for _, a := range s.agents.All() {
			if a.Kind == agent.KindAgent && !off[a.ID] && providers.Compatible(a.CapBase(), p) {
				pi.Agents = append(pi.Agents, a.ID)
			}
		}
		out = append(out, pi)
	}
	return out
}

// withRunnerProvider runs a hidden job through the API provider of settings.runner.provider when
// that provider can run the job's CLI, and notes what it used in the provider's ledger. A spent
// budget refuses the job: it never moves to the subscription by itself. A provider that cannot
// run that CLI leaves the job on the login.
//
// req.Provider overrides the setting: jobs.ProviderLogin keeps the login, an id uses that provider,
// and then a provider that cannot run the CLI is an error instead of the login.
func (s *Server) withRunnerProvider(req *jobs.RunnerRequest) error {
	id, named := req.Provider, req.Provider != ""
	switch id {
	case jobs.ProviderLogin:
		return nil
	case "":
		id = s.settings.Get().Runner.Provider
	}
	if id == "" {
		return nil
	}
	a, ok := s.agents.Get(req.Agent)
	if !ok {
		if named {
			return fmt.Errorf("%w: %s is not an agent", session.ErrProviderMismatch, req.Agent)
		}
		return nil
	}
	pl, err := providerHub{s}.Launch(id, a)
	switch {
	case errors.Is(err, session.ErrProviderBudget):
		return err
	case err != nil && named:
		return err
	case err != nil:
		return nil
	}
	model := ""
	switch a.CapBase() {
	case "claude":
		// the CLI's own names map to the provider's models; any other name is Anthropic's
		if m := req.Model; m == "haiku" || m == "sonnet" || m == "opus" {
			model = m
		}
	case "codex":
		model = pl.Model
	}
	req.Model = model
	env := []string{}
	if a.ProfileEnv != "" && pl.ConfigDir != "" {
		env = append(env, a.ProfileEnv+"="+pl.ConfigDir)
	}
	req.Env = append(env, agent.ProviderEnv(a, pl, agent.ProviderModel(a, pl, model))...)
	req.Extra = append(req.Extra, agent.ProviderRunnerArgs(a, pl)...)
	purpose, done, fallback := req.Purpose, req.OnFinish, cmpOrStr(model, pl.Model)
	req.OnFinish = func(j jobs.Job) {
		if u := j.Usage; u != nil {
			s.noteProviderRun(id, cmpOrStr(u.Model, fallback), purpose, usage.Tokens{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite})
		}
		if done != nil {
			done(j)
		}
	}
	return nil
}
