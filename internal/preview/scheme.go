package preview

import (
	"net"
	"net/http"
	"strings"
)

// SchemeOf returns the scheme the client used. The hub and its preview
// listeners serve plain HTTP; TLS ends at a reverse proxy on the same host,
// so X-Forwarded-Proto is honoured only from a loopback peer (anyone else
// could claim HTTPS by setting the header).
func SchemeOf(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return "http"
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return "http"
	}
	proto, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	if strings.EqualFold(strings.TrimSpace(proto), "https") {
		return "https"
	}
	return "http"
}
