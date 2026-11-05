package server

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/version"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": s.hubName(), "version": version.Version})
}

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"needsSetup": s.auth.NeedsSetup(), "hubName": s.hubName()})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code     string `json:"code"`
		Password string `json:"password"`
		HubName  string `json:"hubName"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !s.auth.NeedsSetup() {
		writeErr(w, http.StatusConflict, auth.ErrAlreadySetup.Error())
		return
	}
	key := "setup:" + clientIP(r)
	if s.limiter.Blocked(key) {
		writeErr(w, http.StatusTooManyRequests, "terlalu banyak percobaan, coba lagi nanti")
		return
	}
	req.HubName = strings.TrimSpace(req.HubName)
	if req.HubName == "" || len(req.HubName) > 64 {
		writeErr(w, http.StatusBadRequest, "nama hub wajib diisi (maks 64 karakter)")
		return
	}
	tok, err := s.auth.CompleteSetup(req.Code, req.Password)
	switch {
	case errors.Is(err, auth.ErrWeakPassword):
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, auth.ErrBadCode):
		s.limiter.Fail(key)
		writeErr(w, http.StatusForbidden, err.Error())
		return
	case errors.Is(err, auth.ErrAlreadySetup):
		writeErr(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.cfgMu.Lock()
	s.cfg.HubName = req.HubName
	err = s.cfg.Save(s.st)
	s.cfgMu.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.setCookie(w, r, tok)
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	key := "login:" + clientIP(r)
	if s.limiter.Blocked(key) {
		writeErr(w, http.StatusTooManyRequests, "terlalu banyak percobaan gagal, coba lagi dalam 10 menit")
		return
	}
	tok, err := s.auth.Login(req.Password)
	if err != nil {
		if errors.Is(err, auth.ErrNotSetup) {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		s.limiter.Fail(key)
		writeErr(w, http.StatusUnauthorized, "password salah")
		return
	}
	s.setCookie(w, r, tok)
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, p *principal) {
	if p.Cookie != "" {
		s.auth.Logout(p.Cookie)
	}
	s.clearCookie(w, r)
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, p *principal) {
	if p.Kind == "device" {
		d, _ := s.auth.Device(p.DeviceID)
		writeJSON(w, http.StatusOK, map[string]any{"kind": "device", "deviceId": d.ID, "deviceName": d.Name})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"kind": "admin"})
}

func (s *Server) handleChallenge(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceID string `json:"deviceId"`
	}
	if !decode(w, r, &req) {
		return
	}
	nonce, err := s.auth.Challenge(req.DeviceID)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"nonce": nonce})
}

func (s *Server) handleDeviceAuth(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceID string `json:"deviceId"`
		Nonce    string `json:"nonce"`
		Sig      string `json:"sig"`
	}
	if !decode(w, r, &req) {
		return
	}
	key := "device:" + clientIP(r)
	if s.limiter.Blocked(key) {
		writeErr(w, http.StatusTooManyRequests, "terlalu banyak percobaan gagal")
		return
	}
	tok, exp, err := s.auth.DeviceLogin(req.DeviceID, req.Nonce, req.Sig)
	if err != nil {
		s.limiter.Fail(key)
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"accessToken": tok, "expiresIn": int(auth.AccessTokenTTL / time.Second), "expiresAt": exp.UTC(),
	})
}

func (s *Server) handleHub(w http.ResponseWriter, r *http.Request, p *principal) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name": s.hubName(), "version": version.Version,
		"urls": s.urlsFor(r), "listen": s.listen,
	})
}

// PairURI builds nextboard://pair?h=&u=&c= (each URL escaped, then
// comma-joined). The hub has no certificate of its own; apps trust the
// reverse proxy's certificate through the system CA store.
func PairURI(hub string, urls []string, code string) string {
	esc := make([]string, len(urls))
	for i, u := range urls {
		esc[i] = url.QueryEscape(u)
	}
	return "nextboard://pair?h=" + url.QueryEscape(hub) + "&u=" + strings.Join(esc, ",") + "&c=" + url.QueryEscape(code)
}

func (s *Server) handlePairStart(w http.ResponseWriter, r *http.Request, p *principal) {
	code, exp, err := s.auth.NewPairCode()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	urls := s.urlsFor(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"code": code, "expiresAt": exp.UTC(), "uri": PairURI(s.hubName(), urls, code),
		"urls": urls, "hubName": s.hubName(),
	})
}

func (s *Server) handlePairComplete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code       string `json:"code"`
		DeviceName string `json:"deviceName"`
		Platform   string `json:"platform"`
		PublicKey  string `json:"publicKey"`
	}
	if !decode(w, r, &req) {
		return
	}
	key := "pair:" + clientIP(r)
	if s.limiter.Blocked(key) {
		writeErr(w, http.StatusTooManyRequests, "terlalu banyak percobaan gagal")
		return
	}
	d, err := s.auth.CompletePairing(req.Code, req.DeviceName, req.Platform, req.PublicKey)
	switch {
	case errors.Is(err, auth.ErrBadCode):
		s.limiter.Fail(key)
		writeErr(w, http.StatusForbidden, err.Error())
		return
	case errors.Is(err, auth.ErrBadInput):
		writeErr(w, http.StatusBadRequest, "deviceName/platform/publicKey tidak valid")
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deviceId": d.ID, "hubName": s.hubName()})
}

// deviceView is the API shape of a device (no public key).
func (s *Server) deviceView(d auth.Device) map[string]any {
	online := s.conns.online(d.ID)
	return map[string]any{
		"id": d.ID, "name": d.Name, "platform": d.Platform,
		"createdAt": d.CreatedAt.UTC(), "lastSeen": d.LastSeen.UTC(),
		"online": online || time.Since(d.LastSeen) < time.Minute,
	}
}

func (s *Server) deviceList() []map[string]any {
	ds := s.auth.Devices()
	out := make([]map[string]any, 0, len(ds))
	for _, d := range ds {
		out = append(out, s.deviceView(d))
	}
	return out
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request, p *principal) {
	writeJSON(w, http.StatusOK, s.deviceList())
}

func (s *Server) handleDeviceRename(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	d, err := s.auth.RenameDevice(r.PathValue("id"), req.Name)
	switch {
	case errors.Is(err, auth.ErrUnknownDevice):
		writeErr(w, http.StatusNotFound, err.Error())
	case err != nil:
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusOK, s.deviceView(d))
	}
}

func (s *Server) handleDeviceRevoke(w http.ResponseWriter, r *http.Request, p *principal) {
	if err := s.auth.RevokeDevice(r.PathValue("id")); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, okBody)
}
