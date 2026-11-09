package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"

	"github.com/azamrizki/nextboard/internal/auth"
)

// Passkeys of other sites (docs/API-vault.md). The extension in a browser tile asks here, the owner
// answers in the vault in their own browser, and the key signs there: it never reaches the hub or
// the extension. A request lives in memory for as long as the page waits, and holds no secret: a
// challenge, a site and an account name, all of which the page already has.

const (
	passkeyWait       = 120 * time.Second
	passkeyMaxBody    = 256 << 10
	passkeyMaxWaiting = 3
	// passkeySeen: how long since its last word the extension counts as there.
	passkeySeen = 24 * time.Hour
)

type passkeyRequest struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Origin    string          `json:"origin"`
	RPID      string          `json:"rpId"`
	Options   json.RawMessage `json:"options"`
	CreatedAt time.Time       `json:"createdAt"`

	answer chan passkeyAnswer
}

// passkeyAnswer is what the page gets: one of the three.
type passkeyAnswer struct {
	Result   json.RawMessage `json:"result,omitempty"`
	Fallback bool            `json:"fallback,omitempty"`
	Error    *passkeyError   `json:"error,omitempty"`
}

type passkeyError struct {
	Name    string `json:"name"`
	Message string `json:"message"`
}

type passkeyState struct {
	Enabled bool   `json:"enabled"`
	Token   string `json:"token"`
}

type passkeyRelay struct {
	file string

	mu       sync.Mutex
	state    passkeyState
	requests map[string]*passkeyRequest
	lastSeen time.Time
}

func newPasskeyRelay(file string) *passkeyRelay {
	r := &passkeyRelay{file: file, requests: map[string]*passkeyRequest{}}
	if b, err := os.ReadFile(file); err == nil {
		_ = json.Unmarshal(b, &r.state)
	}
	return r
}

func (r *passkeyRelay) saveLocked() error {
	b, err := json.Marshal(r.state)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.file), 0o700); err != nil {
		return err
	}
	return os.WriteFile(r.file, b, 0o600)
}

// passkeysAtStart: the hook listener's port changes with every start, and a browser may be running.
func (s *Server) passkeysAtStart() {
	s.passkeys.mu.Lock()
	st := s.passkeys.state
	s.passkeys.mu.Unlock()
	if st.Enabled && s.gui != nil {
		_, _ = s.gui.SetPasskeyExtension(true, s.passkeyHub(), st.Token)
	}
}

func (s *Server) passkeyHub() string { return strings.TrimSuffix(s.hookURL, "/nb/hook") }

// checkPasskeyAddress says whether a page there may hold a passkey for that rp id. This is the
// rule browsers enforce: the page must be secure, and the id must be its own host or a domain above
// it, but never a public suffix like co.id, or every site below it would share keys.
func checkPasskeyAddress(origin, rpID string) (string, bool) {
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" || u.Path != "" || u.RawQuery != "" {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	loopback := host == "localhost" || host == "127.0.0.1"
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return "", false
	}
	if rpID == "" {
		return host, true
	}
	rpID = strings.ToLower(rpID)
	if rpID == host {
		if net.ParseIP(host) != nil && !loopback {
			return "", false
		}
		return rpID, true
	}
	if !strings.HasSuffix(host, "."+rpID) || net.ParseIP(host) != nil {
		return "", false
	}
	if _, err := publicsuffix.EffectiveTLDPlusOne(rpID); err != nil {
		return "", false
	}
	return rpID, true
}

func (s *Server) passkeyTokenOK(r *http.Request) bool {
	s.passkeys.mu.Lock()
	st := s.passkeys.state
	s.passkeys.mu.Unlock()
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && st.Enabled && st.Token != "" && subtle.ConstantTimeCompare([]byte(got), []byte(st.Token)) == 1
}

// handlePasskeyHello is the extension saying it runs. On the loopback listener, with its token.
func (s *Server) handlePasskeyHello(w http.ResponseWriter, r *http.Request) {
	if !s.passkeyTokenOK(r) {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	s.passkeys.mu.Lock()
	s.passkeys.lastSeen = time.Now()
	s.passkeys.mu.Unlock()
	s.events.Publish(map[string]any{"type": "passkeyRequest"})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handlePasskeyAsk holds the page's request until the owner answers, or nobody does.
func (s *Server) handlePasskeyAsk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.passkeyTokenOK(r) {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		Kind    string          `json:"kind"`
		Origin  string          `json:"origin"`
		Options json.RawMessage `json:"options"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, passkeyMaxBody)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	// The listener's read limit would otherwise end the wait: past it, the server reads the
	// silence as the client gone, and cancels this request while the owner is still deciding.
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
	fallback := func() { writeJSON(w, http.StatusOK, passkeyAnswer{Fallback: true}) }
	var o struct {
		RP struct {
			ID string `json:"id"`
		} `json:"rp"`
		RPID   string `json:"rpId"`
		Params []struct {
			Alg int `json:"alg"`
		} `json:"pubKeyCredParams"`
	}
	if (req.Kind != "create" && req.Kind != "get") || json.Unmarshal(req.Options, &o) != nil {
		fallback()
		return
	}
	want := o.RPID
	if req.Kind == "create" {
		want = o.RP.ID
		// ES256 is all the vault signs with; a site that takes nothing else asks the browser
		es256 := false
		for _, p := range o.Params {
			es256 = es256 || p.Alg == -7
		}
		if !es256 {
			fallback()
			return
		}
	}
	rpID, ok := checkPasskeyAddress(req.Origin, want)
	if !ok {
		// The browser refuses it too, with the error the page expects.
		fallback()
		return
	}
	s.passkeys.mu.Lock()
	crowded := len(s.passkeys.requests) >= passkeyMaxWaiting
	for _, q := range s.passkeys.requests {
		crowded = crowded || q.Origin == req.Origin
	}
	s.passkeys.mu.Unlock()
	if crowded {
		// A page cannot bury the owner in dialogs: what it asks while waiting goes to the browser.
		fallback()
		return
	}
	pr := &passkeyRequest{ID: auth.RandomID("pk_"), Kind: req.Kind, Origin: req.Origin, RPID: rpID, Options: req.Options, CreatedAt: time.Now().UTC(), answer: make(chan passkeyAnswer, 1)}
	s.passkeys.mu.Lock()
	s.passkeys.requests[pr.ID] = pr
	s.passkeys.lastSeen = time.Now()
	s.passkeys.mu.Unlock()
	s.events.Publish(map[string]any{"type": "passkeyRequest", "id": pr.ID})
	defer func() {
		s.passkeys.mu.Lock()
		delete(s.passkeys.requests, pr.ID)
		s.passkeys.mu.Unlock()
		s.events.Publish(map[string]any{"type": "passkeyRequest", "id": pr.ID})
	}()
	timer := time.NewTimer(s.passkeyWaitFor())
	defer timer.Stop()
	select {
	case a := <-pr.answer:
		writeJSON(w, http.StatusOK, a)
	case <-timer.C:
		// Nobody is at the vault: the browser's own authenticator may still help.
		fallback()
	case <-r.Context().Done():
	}
}

func (s *Server) passkeyWaitFor() time.Duration {
	if s.passkeyWaitOverride > 0 {
		return s.passkeyWaitOverride
	}
	return passkeyWait
}

func (s *Server) routesPasskeys(authed func(string, handler)) {
	authed("GET /api/vault/passkeys", s.handlePasskeys)
	authed("PUT /api/vault/passkeys", s.handlePasskeysSet)
	authed("POST /api/vault/passkeys/requests/{id}", s.handlePasskeyAnswer)
}

func (s *Server) handlePasskeys(w http.ResponseWriter, r *http.Request, p *principal) {
	s.passkeys.mu.Lock()
	defer s.passkeys.mu.Unlock()
	items := make([]*passkeyRequest, 0, len(s.passkeys.requests))
	for _, q := range s.passkeys.requests {
		items = append(items, q)
	}
	out := map[string]any{
		"enabled":  s.passkeys.state.Enabled,
		"active":   s.passkeys.state.Enabled && !s.passkeys.lastSeen.IsZero() && time.Since(s.passkeys.lastSeen) < passkeySeen,
		"requests": items,
	}
	if s.passkeys.state.Enabled && s.gui != nil {
		out["path"] = s.gui.PasskeyExtensionDir()
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePasskeysSet(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if !decode(w, r, &req) {
		return
	}
	s.passkeys.mu.Lock()
	s.passkeys.state.Enabled = req.Enabled
	if req.Enabled && s.passkeys.state.Token == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			s.passkeys.mu.Unlock()
			writeErr(w, http.StatusInternalServerError, "no randomness")
			return
		}
		s.passkeys.state.Token = hex.EncodeToString(b)
	}
	if !req.Enabled {
		// A new token next time: a copy of the extension left in a browser stops working.
		s.passkeys.state.Token = ""
		s.passkeys.lastSeen = time.Time{}
	}
	err := s.passkeys.saveLocked()
	token := s.passkeys.state.Token
	pending := make([]*passkeyRequest, 0, len(s.passkeys.requests))
	for _, q := range s.passkeys.requests {
		pending = append(pending, q)
	}
	s.passkeys.mu.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !req.Enabled {
		for _, q := range pending {
			select {
			case q.answer <- passkeyAnswer{Fallback: true}:
			default:
			}
		}
	}
	dir := ""
	if s.gui != nil {
		dir, err = s.gui.SetPasskeyExtension(req.Enabled, s.passkeyHub(), token)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	s.events.Publish(map[string]any{"type": "passkeyRequest"})
	writeJSON(w, http.StatusOK, map[string]any{"enabled": req.Enabled, "path": dir})
}

func (s *Server) handlePasskeyAnswer(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Result   json.RawMessage `json:"result"`
		Fallback bool            `json:"fallback"`
		Refuse   bool            `json:"refuse"`
	}
	if !decode(w, r, &req) {
		return
	}
	s.passkeys.mu.Lock()
	q := s.passkeys.requests[r.PathValue("id")]
	s.passkeys.mu.Unlock()
	if q == nil {
		writeErrCode(w, http.StatusNotFound, "request_gone", "the page stopped waiting for this")
		return
	}
	var a passkeyAnswer
	switch {
	case req.Refuse:
		a.Error = &passkeyError{Name: "NotAllowedError", Message: "The request was refused in the vault."}
	case req.Fallback:
		a.Fallback = true
	case len(req.Result) > 0:
		a.Result = req.Result
	default:
		writeErrCode(w, http.StatusBadRequest, "bad_answer", "nothing to answer with")
		return
	}
	select {
	case q.answer <- a:
	default:
		writeErrCode(w, http.StatusConflict, "request_answered", "this request was already answered")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
