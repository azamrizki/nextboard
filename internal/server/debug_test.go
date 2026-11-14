package server

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
)

// The profiles are served only on a loopback address, only to a loopback Host, and to no web page.
func TestDebugListen(t *testing.T) {
	h := newTestHub(t)
	if _, err := h.srv.serveDebug("0.0.0.0:0"); err == nil {
		t.Fatal("a profile listener on every address")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	stop, err := h.srv.serveDebug(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	get := func(path string, hdr map[string]string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("GET", "http://"+addr+path, nil)
		for k, v := range hdr {
			if k == "Host" {
				req.Host = v
			} else {
				req.Header.Set(k, v)
			}
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := get("/debug/hub", nil)
	var hub map[string]any
	_ = json.NewDecoder(res.Body).Decode(&hub)
	res.Body.Close()
	if res.StatusCode != 200 || hub["goroutines"] == nil || hub["consoles"] == nil {
		t.Fatalf("hub numbers: %d %v", res.StatusCode, hub)
	}
	for _, c := range []struct {
		path string
		hdr  map[string]string
		want int
	}{
		{"/debug/pprof/heap", nil, 200},
		{"/debug/pprof/cmdline", nil, 404},
		{"/debug/hub", map[string]string{"Host": "evil.example:" + strings.Split(addr, ":")[1]}, 403},
		{"/debug/hub", map[string]string{"Origin": "http://evil.example"}, 403},
		{"/debug/hub", map[string]string{"Host": fmt.Sprintf("localhost:%s", strings.Split(addr, ":")[1])}, 200},
	} {
		res := get(c.path, c.hdr)
		res.Body.Close()
		if res.StatusCode != c.want {
			t.Errorf("%s %v: %d, want %d", c.path, c.hdr, res.StatusCode, c.want)
		}
	}
}
