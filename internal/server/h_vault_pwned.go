package server

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"time"
)

// pwnedURL is Have I Been Pwned's Pwned Passwords range API; tests point it elsewhere.
var pwnedURL = "https://api.pwnedpasswords.com/range/"

var pwnedPrefix = regexp.MustCompile(`^[0-9A-F]{5}$`)

const pwnedMax = 2 << 20

// handleVaultPwned passes a breach check on (web/src/vault/breach.ts). The browser hashes
// the password and sends only the first 5 hex characters of its SHA-1; the answer is every
// suffix sharing them, padded, compared in the browser. The hub asks so that the page's
// connect-src stays 'self'.
func (s *Server) handleVaultPwned(w http.ResponseWriter, r *http.Request, p *principal) {
	prefix := r.PathValue("prefix")
	if !pwnedPrefix.MatchString(prefix) {
		writeErr(w, http.StatusBadRequest, "prefix must be 5 hex characters in capitals")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pwnedURL+prefix, nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	req.Header.Set("Add-Padding", "true")
	req.Header.Set("User-Agent", "NextBoard")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeErrCode(w, http.StatusBadGateway, "pwned_unreachable", "Have I Been Pwned is out of reach from the hub")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeErrCode(w, http.StatusBadGateway, "pwned_failed", "Have I Been Pwned answered "+resp.Status)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(w, io.LimitReader(resp.Body, pwnedMax))
}
