package server

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/preview"
)

// Session cookie names, one per scheme. Cookies ignore scheme and port, and a
// browser refuses to let an http:// page overwrite a Secure cookie of the same
// name: with one name, login over HTTP silently fails on any host that was
// used over HTTPS before (login answers 200, the cookie is dropped).
const (
	cookieName      = "nb_session"
	cookieNamePlain = "nb_session_http"
)

func sessionCookieName(r *http.Request) string {
	if preview.SchemeOf(r) == "https" {
		return cookieName
	}
	return cookieNamePlain
}

// principal is the authenticated caller.
type principal struct {
	Kind     string // "admin" | "device"
	DeviceID string
	Bearer   bool
	Cookie   string
}

// connKey is what the caller's open sockets are registered under, to end with
// its login.
func (p *principal) connKey() string {
	if p.Cookie != "" {
		return cookieConnPrefix + p.Cookie
	}
	return p.DeviceID
}

type handler func(w http.ResponseWriter, r *http.Request, p *principal)

func (s *Server) routes() {
	m := http.NewServeMux()
	pub := func(pattern string, h http.HandlerFunc) { m.HandleFunc(pattern, h) }
	authed := func(pattern string, h handler) { m.HandleFunc(pattern, s.requireAuth(h, false)) }
	admin := func(pattern string, h handler) { m.HandleFunc(pattern, s.requireAuth(h, true)) }

	pub("GET /api/health", s.handleHealth)
	pub("GET /api/setup/status", s.handleSetupStatus)
	pub("POST /api/setup", s.handleSetup)
	pub("POST /api/auth/login", s.handleLogin)
	authed("POST /api/auth/logout", s.handleLogout)
	authed("GET /api/auth/me", s.handleMe)
	pub("POST /api/auth/challenge", s.handleChallenge)
	pub("POST /api/auth/device", s.handleDeviceAuth)
	authed("GET /api/hub", s.handleHub)

	admin("POST /api/pair/start", s.handlePairStart)
	pub("POST /api/pair/complete", s.handlePairComplete)
	authed("GET /api/devices", s.handleDevices)
	admin("PATCH /api/devices/{id}", s.handleDeviceRename)
	admin("DELETE /api/devices/{id}", s.handleDeviceRevoke)

	authed("GET /api/machines", s.handleMachines)
	admin("GET /api/logs/connections", s.handleConnLog)
	authed("GET /api/machines/sshconfig", s.handleSSHConfig)
	authed("POST /api/machines", s.handleMachineAdd)
	authed("POST /api/machines/{id}/hostkey", s.handleMachineHostKey)
	authed("POST /api/machines/{id}/credentials", s.handleMachineCredentials)
	authed("POST /api/machines/{id}/connect", s.handleMachineConnect)
	authed("POST /api/machines/{id}/probe", s.handleMachineProbe)
	authed("PATCH /api/machines/{id}", s.handleMachineUpdate)
	authed("DELETE /api/machines/{id}", s.handleMachineDelete)
	authed("GET /api/machines/{id}/presets", s.handleMachinePresets)
	authed("GET /api/machines/{id}/ls", s.handleMachineLs)
	authed("GET /api/machines/{id}/fs/stat", s.handleFSStat)
	authed("GET /api/machines/{id}/fs/read", s.handleFSRead)
	authed("GET /api/machines/{id}/fs/raw", s.handleFSRaw)
	authed("PUT /api/machines/{id}/fs/write", s.handleFSWrite)
	authed("GET /api/machines/{id}/agents", s.handleMachineAgents)
	authed("GET /api/machines/{id}/agents/{agent}/models", s.handleAgentModels)
	authed("GET /api/machines/{id}/conversations", s.handleConversations)
	authed("GET /api/machines/{id}/orphans", s.handleMachineOrphans)
	authed("POST /api/machines/{id}/cleanup", s.handleMachineCleanup)

	authed("GET /api/presets", s.handlePresets)
	authed("GET /api/sessions", s.handleSessions)
	authed("POST /api/sessions", s.handleSessionCreate)
	authed("GET /api/sessions/{id}", s.handleSessionGet)
	authed("PATCH /api/sessions/{id}", s.handleSessionRename)
	authed("DELETE /api/sessions/{id}", s.handleSessionDelete)
	authed("POST /api/sessions/{id}/restart", s.handleSessionRestart)
	authed("POST /api/sessions/{id}/kill", s.handleSessionKill)
	authed("POST /api/sessions/{id}/sleep", s.handleSessionSleep)
	authed("POST /api/sessions/{id}/wake", s.handleSessionWake)
	authed("DELETE /api/sessions/{id}/draft", s.handleSessionDraftDismiss)
	authed("POST /api/projects/{id}/wake", s.handleProjectWake)
	authed("POST /api/sessions/{id}/input", s.handleSessionInput)
	authed("POST /api/sessions/{id}/attach", s.handleSessionAttach)
	authed("GET /api/projects/{id}/uptime", s.handleUptimeList)
	authed("POST /api/projects/{id}/uptime", s.handleUptimeCreate)
	authed("PUT /api/projects/{id}/uptime/settings", s.handleUptimeSettings)
	authed("POST /api/projects/{id}/uptime/settings/test", s.handleUptimeTest)
	authed("PUT /api/projects/{id}/uptime/maintenance", s.handleUptimeMaintenance)
	authed("PATCH /api/uptime/{id}", s.handleUptimeUpdate)
	authed("DELETE /api/uptime/{id}", s.handleUptimeDelete)
	authed("POST /api/uptime/{id}/check", s.handleUptimeCheck)
	authed("PUT /api/projects/{id}/uptime/public", s.handleUptimePublic)
	authed("POST /api/projects/{id}/uptime/public/rotate", s.handleUptimePublicRotate)
	authed("GET /api/uptime/{id}/history", s.handleUptimeHistory)
	authed("GET /api/uptime/{id}/events", s.handleUptimeEvents)
	// no login: a cron job on another machine has only the token in the URL
	pub("GET /api/push/{token}", s.handleUptimePush)
	// the vault holds ciphertext only (docs/API-vault.md)
	authed("GET /api/vault", s.handleVaultGet)
	admin("POST /api/vault", s.handleVaultInit)
	admin("PUT /api/vault/key", s.handleVaultKey)
	admin("POST /api/vault/reset", s.handleVaultReset)
	authed("POST /api/vault/batch", s.handleVaultBatch)
	authed("PUT /api/vault/items/{id}", s.handleVaultPut)
	authed("DELETE /api/vault/items/{id}", s.handleVaultDelete)
	authed("POST /api/vault/pins", s.handleVaultPinCreate)
	authed("POST /api/vault/pins/{id}/open", s.handleVaultPinOpen)
	authed("DELETE /api/vault/pins/{id}", s.handleVaultPinDelete)
	authed("GET /api/vault/pwned/{prefix}", s.handleVaultPwned)
	s.routesPasskeys(authed)
	admin("DELETE /api/vault/pins", s.handleVaultPinsClear)
	// agents asking for a credential: names in the catalog, values only from the browser that approves
	authed("GET /api/vault/agents", s.handleVaultAgents)
	authed("PUT /api/vault/agents/{id}", s.handleVaultAgentPut)
	authed("GET /api/vault/requests", s.handleCredentialRequests)
	authed("POST /api/vault/requests/{id}/approve", s.handleCredentialApprove)
	authed("POST /api/vault/requests/{id}/deny", s.handleCredentialDeny)
	authed("GET /api/vault/agent-log", s.handleCredentialLog)
	authed("POST /api/projects/{id}/attach", s.handleProjectAttach)
	authed("GET /api/projects/{id}/attach/{name}", s.handleProjectAttachRaw)
	authed("POST /api/sessions/{id}/agent", s.handleSessionAgent)
	authed("GET /api/sessions/{id}/reply", s.handleSessionReply)
	authed("POST /api/sessions/{id}/reply/save", s.handleSessionReplySave)
	authed("GET /api/sessions/{id}/transcript", s.handleSessionTranscript)
	authed("GET /api/sessions/{id}/tokens", s.handleSessionTokens)
	authed("GET /api/sessions/{id}/cost", s.handleSessionCost)
	authed("GET /api/sessions/{id}/ws", s.handleTerminalWS)
	authed("GET /api/events", s.handleEventsWS)

	authed("GET /api/projects", s.handleProjects)
	authed("POST /api/projects", s.handleProjectCreate)
	authed("PATCH /api/projects/{id}", s.handleProjectUpdate)
	authed("DELETE /api/projects/{id}", s.handleProjectDelete)
	authed("POST /api/projects/{id}/parts", s.handlePartCreate)
	authed("PATCH /api/projects/{id}/parts/{part}", s.handlePartUpdate)
	authed("DELETE /api/projects/{id}/parts/{part}", s.handlePartDelete)
	authed("POST /api/projects/{id}/parts/{part}/split", s.handleSplit)
	authed("GET /api/projects/{id}/merge", s.handleMergePreview)
	authed("POST /api/projects/{id}/merge", s.handleMerge)
	authed("GET /api/project-groups", s.handleProjectGroups)
	authed("POST /api/project-groups", s.handleProjectGroupCreate)
	authed("PATCH /api/project-groups/{id}", s.handleProjectGroupUpdate)
	authed("DELETE /api/project-groups/{id}", s.handleProjectGroupDelete)
	authed("GET /api/projects/{id}/git", s.handleProjectGit)
	authed("GET /api/projects/{id}/close-all", s.handleProjectCloseAllPreview)
	authed("POST /api/projects/{id}/close-all", s.handleProjectCloseAll)
	authed("POST /api/projects/{id}/workspaces", s.handleWorkspaceCreate)
	authed("PATCH /api/workspaces/{id}", s.handleWorkspaceUpdate)
	authed("DELETE /api/workspaces/{id}", s.handleWorkspaceDelete)
	authed("GET /api/workspaces/{id}/board", s.handleBoardGet)
	authed("GET /api/workspaces/{id}/ide/ws", s.handleIDESocket)
	authed("POST /api/workspaces/{id}/board/ops", s.handleBoardOps)
	authed("GET /api/workspaces/{id}/snapshots", s.handleSnapshots)
	authed("POST /api/workspaces/{id}/snapshots/{sid}/restore", s.handleSnapshotRestore)
	authed("GET /api/archive", s.handleArchive)
	authed("POST /api/archive/{id}/restore", s.handleArchiveRestore)

	authed("GET /api/agents", s.handleAgents)
	authed("GET /api/agents/sandbox", s.handleSandboxStatus)
	authed("POST /api/agents", s.handleAgentCreate)
	authed("PATCH /api/agents/{id}", s.handleAgentUpdate)
	authed("DELETE /api/agents/{id}", s.handleAgentDelete)
	authed("GET /api/settings", s.handleSettings)
	authed("PATCH /api/settings", s.handleSettingsPatch)
	authed("GET /api/profiles", s.handleProfiles)
	authed("POST /api/profiles", s.handleProfileCreate)
	authed("PATCH /api/profiles/{id}", s.handleProfileUpdate)
	authed("DELETE /api/profiles/{id}", s.handleProfileDelete)
	authed("POST /api/profiles/{id}/signin", s.handleProfileSignIn)
	authed("GET /api/providers", s.handleProviders)
	authed("POST /api/providers", s.handleProviderCreate)
	authed("POST /api/providers/test", s.handleProviderTest)
	authed("PATCH /api/providers/{id}", s.handleProviderUpdate)
	authed("DELETE /api/providers/{id}", s.handleProviderDelete)
	authed("GET /api/providers/{id}/spend", s.handleProviderSpend)
	authed("GET /api/attention", s.handleAttention)
	authed("GET /api/jobs", s.handleJobs)
	authed("GET /api/jobs/{id}", s.handleJobGet)
	authed("POST /api/jobs/{id}/cancel", s.handleJobCancel)

	s.orchRoutes(authed)
	s.routesMCP(authed, admin)
	s.routesGitPreview(authed)
	s.routesReview(authed)
	s.routesTasks(authed)
	s.routesLaravel(authed)
	s.routesStudio(authed)
	s.routesGUI(authed)

	authed("POST /api/assets", s.handleAssetUpload)
	authed("GET /api/assets/{id}", s.handleAssetGet)

	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "endpoint not found")
	})
	s.mux = m
}

// authenticate resolves the caller from the session cookie, a Bearer
// header. Tokens are never read from the URL: a reverse proxy logs query
// strings.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) *principal {
	if c, err := r.Cookie(sessionCookieName(r)); err == nil && c.Value != "" {
		if ok, renew := s.auth.ValidateCookie(c.Value); ok {
			if renew {
				s.setCookie(w, r, c.Value)
			}
			return &principal{Kind: "admin", Cookie: c.Value}
		}
	}
	tok := ""
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		tok = strings.TrimSpace(h[len("Bearer "):])
	}
	if tok != "" {
		if id, ok := s.auth.ValidateBearer(tok); ok {
			s.auth.TouchDevice(id)
			return &principal{Kind: "device", DeviceID: id, Bearer: true}
		}
	}
	return nil
}

func (s *Server) requireAuth(h handler, adminOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := s.authenticate(w, r)
		if p == nil {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if adminOnly && p.Kind != "admin" {
			writeErr(w, http.StatusForbidden, "admin only")
			return
		}
		h(w, r, p)
	}
}

// setCookie marks the cookie Secure when the client came over HTTPS (behind
// the reverse proxy); on plain HTTP a Secure cookie would be dropped.
func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, tok string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName(r), Value: tok, Path: "/",
		MaxAge: int(auth.CookieTTL / time.Second), Expires: time.Now().Add(auth.CookieTTL),
		HttpOnly: true, Secure: preview.SchemeOf(r) == "https", SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) clearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName(r), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: preview.SchemeOf(r) == "https", SameSite: http.SameSiteStrictMode,
	})
}

// clientIP returns the peer IP; X-Forwarded-For is trusted only from
// loopback (tailscale serve / local reverse proxy).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
	}
	return host
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeErrCode adds a stable snake_case code (docs/API-agents.md: error
// bodies may carry "code"; old clients only read "error").
func writeErrCode(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

var okBody = map[string]bool{"ok": true}

// decode reads a JSON body into v, mapping size errors to 413.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeErrCode(w, http.StatusRequestEntityTooLarge, "body_too_large", "body too large")
			return false
		}
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return false
	}
	return true
}
