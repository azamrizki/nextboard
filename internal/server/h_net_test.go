package server

import (
	"bytes"
	"compress/flate"
	"encoding/json"
	"net/http"
	"testing"
)

// A page times its round trip to the hub over /api/events: a ping gets its pong, with its id.
func TestEventsAnswerAPingWithItsID(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	ws, _ := h.events()
	for _, id := range []int64{7, 8} {
		if err := ws.WriteJSON(map[string]any{"type": "ping", "id": id}); err != nil {
			t.Fatal(err)
		}
		ev := waitEvent(t, ws, func(m map[string]any) bool { return m["type"] == "pong" })
		if ev["id"] != float64(id) {
			t.Errorf("pong %v, want id %d", ev, id)
		}
	}
	// anything else a page sends is still ignored, and the socket stays open
	if err := ws.WriteJSON(map[string]any{"type": "hello"}); err != nil {
		t.Fatal(err)
	}
	_ = ws.WriteJSON(map[string]any{"type": "ping", "id": 9})
	if ev := waitEvent(t, ws, func(m map[string]any) bool { return m["type"] == "pong" }); ev["id"] != float64(9) {
		t.Errorf("after another frame: %v", ev)
	}
}

// The speed test's download is as long as asked, not compressible and not cached.
func TestNetDownloadSendsWhatWasAsked(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	st, body, hdr := h.doRaw("GET", "/api/net/download?bytes=200000", "", nil)
	if st != 200 || len(body) != 200000 {
		t.Fatalf("status %d, %d bytes", st, len(body))
	}
	if hdr.Get("Cache-Control") != "no-store" || hdr.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("headers %v", hdr)
	}
	var packed bytes.Buffer
	fw, _ := flate.NewWriter(&packed, flate.BestCompression)
	_, _ = fw.Write(body)
	_ = fw.Close()
	if packed.Len() < len(body)*99/100 {
		t.Errorf("compresses to %d of %d bytes: a proxy could shrink it", packed.Len(), len(body))
	}
	if st, def, _ := h.doRaw("GET", "/api/net/download", "", nil); st != 200 || len(def) != netDownDefault {
		t.Errorf("default: status %d, %d bytes", st, len(def))
	}
	for _, q := range []string{"0", "-1", "x", "67108865"} {
		if st, _, _ := h.doRaw("GET", "/api/net/download?bytes="+q, "", nil); st != http.StatusBadRequest {
			t.Errorf("bytes=%s: %d", q, st)
		}
	}
}

func TestNetUploadCountsAndRefusesTooMuch(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	st, raw, _ := h.doRaw("POST", "/api/net/upload", "application/octet-stream", make([]byte, 1<<20))
	var got struct {
		Bytes int64 `json:"bytes"`
		MS    int64 `json:"ms"`
	}
	if st != 200 || json.Unmarshal(raw, &got) != nil || got.Bytes != 1<<20 || got.MS < 0 {
		t.Fatalf("status %d: %s", st, raw)
	}
	if st, _, _ := h.doRaw("POST", "/api/net/upload", "application/octet-stream", make([]byte, netUpMax+1)); st != http.StatusRequestEntityTooLarge {
		t.Errorf("over the limit: %d", st)
	}
}

func TestNetSaysWhetherAProxyIsInBetween(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	if st, m := h.do("GET", "/api/net", nil, nil); st != 200 || m["proxied"] != false {
		t.Errorf("direct: %d %v", st, m)
	}
	if st, m := h.do("GET", "/api/net", nil, map[string]string{"X-Forwarded-For": "203.0.113.9"}); st != 200 || m["proxied"] != true {
		t.Errorf("through a proxy: %d %v", st, m)
	}
}
