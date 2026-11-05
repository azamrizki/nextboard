package server

import (
	"net/http/httptest"
	"slices"
	"testing"

	"nextboard/internal/config"
)

// In dev mode the accepted Vite origin follows --dev-web-port; outside dev
// mode, and on any other port, cross-origin cookie WebSockets stay refused.
func TestDevOriginFollowsWebPort(t *testing.T) {
	check := func(s *Server, origin string) bool {
		r := httptest.NewRequest("GET", "https://hub.example:12312/api/events", nil)
		r.Header.Set("Origin", origin)
		return s.checkOrigin(r, &principal{Kind: "admin"})
	}
	dev := &Server{dev: true, devWeb: 15173}
	for origin, want := range map[string]bool{
		"http://localhost:15173": true,
		"http://127.0.0.1:15173": true,
		"http://localhost:5173":  false,
		"https://evil.example":   false,
	} {
		if got := check(dev, origin); got != want {
			t.Errorf("dev origin %s = %v, want %v", origin, got, want)
		}
	}
	if check(&Server{devWeb: 15173}, "http://localhost:15173") {
		t.Error("Vite origin accepted outside dev mode")
	}
	dev.listen, dev.cfg = "127.0.0.1:12312", &config.Config{}
	if o := dev.hubOrigins("http", "localhost:12320"); !slices.Contains(o, "http://localhost:15173") || slices.Contains(o, "http://localhost:5173") {
		t.Errorf("hubOrigins %v", o)
	}
}

func TestIsLoopbackListen(t *testing.T) {
	for listen, want := range map[string]bool{
		"127.0.0.1:12312": true,
		"[::1]:12312":     true,
		"localhost:12312": true,
		"0.0.0.0:12312":   false,
		"[::]:12312":      false,
		":12312":          false,
		"192.168.1.5:80":  false,
		"garbage":         false,
	} {
		if got := isLoopbackListen(listen); got != want {
			t.Errorf("isLoopbackListen(%q) = %v, want %v", listen, got, want)
		}
	}
}
