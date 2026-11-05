package orch

import (
	"encoding/json"
	"testing"
)

type fakeOpener struct {
	machine string
	port    int
}

func (f *fakeOpener) OpenPreview(machineID string, port int) (string, error) {
	f.machine, f.port = machineID, port
	return "pv_test000001", nil
}

// "open localhost 5173" adds a web tile that already carries its preview.
func TestOpenLocalhostCreatesPreview(t *testing.T) {
	h, s := newFakeHub(t)
	_, wid := h.project("app")
	op := &fakeOpener{}
	s.Previews = op
	cmd(t, s, wid, "open localhost 5173")
	b, err := h.Board(wid)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	for _, n := range b.Nodes {
		if n.Kind == "web" {
			if err := json.Unmarshal(n.Data, &data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if data == nil {
		t.Fatal("no web tile")
	}
	if data["previewId"] != "pv_test000001" || data["port"] != float64(5173) || data["machineId"] != "local" ||
		data["url"] != "http://localhost:5173/" {
		t.Fatalf("web tile data %v", data)
	}
	if op.machine != "local" || op.port != 5173 {
		t.Fatalf("opener got %s:%d", op.machine, op.port)
	}
}
