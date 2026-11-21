package native

import (
	"bufio"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/nextcode/native/llm"
	"github.com/azamrizki/nextboard/internal/usage"
)

func TestResume(t *testing.T) {
	fake := newFake(callTool("t1", "Read", `{"file_path":"a"}`), say("first answer"))
	cfg := newConfig(t, fake, newKit())
	cfg.sessionID = "sess-r"
	cfg.appendPrompt = "CONTEXT ONE"
	h := startAgent(t, cfg)
	h.initialize()
	h.say("hello")
	h.result()
	h.close()
	before := loadConv(t, cfg, "sess-r")

	// the hub's context changed: the conversation keeps its own prompt
	for _, how := range []string{"resume", "session-id"} {
		t.Run(how, func(t *testing.T) {
			fake2 := newFake(say("second answer"))
			cfg2 := *cfg
			cfg2.client = fake2
			cfg2.appendPrompt = "CONTEXT TWO"
			cfg2.sessionID, cfg2.resume = "", ""
			if how == "resume" {
				cfg2.resume = "sess-r"
			} else {
				cfg2.sessionID = "sess-r"
			}
			h := startAgent(t, &cfg2)
			h.initialize()
			h.say("again")
			res, _ := h.result()
			h.close()
			if res["session_id"] != "sess-r" {
				t.Fatalf("result: %v", res)
			}
			req := fake2.request(t, 0)
			if req.System != before.System || !strings.Contains(req.System, "CONTEXT ONE") || strings.Contains(req.System, "CONTEXT TWO") {
				t.Fatal("the system prompt changed on resume")
			}
			old, _ := json.Marshal(before.Messages)
			sent, _ := json.Marshal(req.Messages[:len(before.Messages)])
			if string(old) != string(sent) {
				t.Fatalf("the old messages changed:\n%s\n%s", old, sent)
			}
			checkHistory(t, loadConv(t, &cfg2, "sess-r").Messages)
		})
	}
}

func TestResumeMissing(t *testing.T) {
	cfg := newConfig(t, newFake(), newKit())
	cfg.sessionID, cfg.resume = "", "new-one"
	h := startAgent(t, cfg)
	h.send(map[string]any{"type": "control_request", "request_id": "nb_1", "request": map[string]any{"subtype": "initialize"}})
	ini, _ := h.until(is("system", "init"))
	if ini["session_id"] != "new-one" || !strings.Contains(h.stderr.String(), "no conversation new-one yet") {
		t.Fatalf("init %v, stderr %q", ini, h.stderr.String())
	}
}

func TestContinue(t *testing.T) {
	cfg := newConfig(t, newFake(), newKit())
	s := store{dir: cfg.stateDir}
	for i, id := range []string{"older", "newest", "old"} {
		c := newConversation(id, "system "+id)
		if err := s.save(c); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(time.Duration(i-10) * time.Hour)
		if id == "newest" {
			at = time.Now()
		}
		if err := os.Chtimes(s.path(id), at, at); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(cfg.stateDir, ".newest.123.tmp"), []byte("x"), 0o600)
	cfg.sessionID, cfg.cont = "", true
	h := startAgent(t, cfg)
	h.initialize()
	h.close()
	if !strings.Contains(h.stdout.String(), `"session_id":"newest"`) {
		t.Fatalf("did not continue the newest: %s", h.stdout.String())
	}

	// nothing to continue: a new conversation
	cfg2 := newConfig(t, newFake(), newKit())
	cfg2.sessionID, cfg2.cont = "", true
	h = startAgent(t, cfg2)
	h.initialize()
	h.close()
	if !strings.Contains(h.stderr.String(), "no conversation to continue") {
		t.Fatalf("stderr %q", h.stderr.String())
	}
}

func TestNewConversationID(t *testing.T) {
	cfg := newConfig(t, newFake(), newKit())
	cfg.sessionID = ""
	h := startAgent(t, cfg)
	h.send(map[string]any{"type": "control_request", "request_id": "nb_1", "request": map[string]any{"subtype": "initialize"}})
	ini, _ := h.until(is("system", "init"))
	if id, _ := ini["session_id"].(string); len(id) != 36 || id[14] != '4' {
		t.Fatalf("session id %q", id)
	}
}

func TestDamagedConversation(t *testing.T) {
	cfg := newConfig(t, newFake(), newKit())
	if err := os.MkdirAll(cfg.stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.stateDir, "sess-1.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr lockedBuf
	if code := run(t.Context(), cfg, strings.NewReader(""), &lockedBuf{}, &stderr); code != 1 || !strings.Contains(stderr.String(), "damaged") {
		t.Fatalf("exit %d, %q", code, stderr.String())
	}
}

func TestSaveAtomic(t *testing.T) {
	s := store{dir: filepath.Join(t.TempDir(), "state")}
	c := newConversation("c1", "sys")
	c.Messages = append(c.Messages, llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{textBlock("hi")}})
	if err := s.save(c); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(s.dir)
	if len(entries) != 1 || entries[0].Name() != "c1.json" {
		t.Fatalf("files: %v", entries)
	}
	b, _ := os.ReadFile(s.path("c1"))
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "created", "system", "messages", "compactions"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("no %q in the file", k)
		}
	}
	if !validID("0b5e1c1e-7c1a-4f6e-9d1a-1a2b3c4d5e6f") || validID("../x") || validID(".hidden") || validID("a/b") || validID("") {
		t.Error("validID")
	}
}

func TestLedger(t *testing.T) {
	fake := newFake(answer(llm.StopToolUse, llm.Usage{Input: 100, Output: 10, CacheRead: 1000, CacheWrite: 50}, toolUse("t1", "Read", `{"file_path":"a"}`)),
		answer(llm.StopEnd, llm.Usage{Input: 5, Output: 20, CacheRead: 1100}, textBlock("done")))
	cfg := newConfig(t, fake, newKit())
	h := startAgent(t, cfg)
	h.initialize()
	h.set("claude-alt")
	h.say("go")
	res, _ := h.result()
	if field(res, "usage", "input_tokens") != 105.0 || field(res, "usage", "cache_read_input_tokens") != 2100.0 || field(res, "usage", "output_tokens") != 30.0 {
		t.Fatalf("turn usage: %v", res["usage"])
	}
	path := filepath.Join(cfg.homeDir, "usage.jsonl")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("ledger: %v %v", info, err)
	}
	f, _ := os.Open(path)
	defer f.Close()
	// the hub's ledgerEntry (internal/server/providers.go)
	type ledgerEntry struct {
		At      time.Time `json:"at"`
		Model   string    `json:"model"`
		Purpose string    `json:"purpose,omitempty"`
		usage.Tokens
	}
	var got []ledgerEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e ledgerEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("%v: %s", err, sc.Bytes())
		}
		var raw map[string]any
		_ = json.Unmarshal(sc.Bytes(), &raw)
		if keys := slices.Sorted(maps.Keys(raw)); !slices.Equal(keys, []string{"at", "cacheRead", "cacheWrite", "input", "model", "output", "purpose"}) {
			t.Fatalf("fields %v", keys)
		}
		got = append(got, e)
	}
	if len(got) != 2 {
		t.Fatalf("%d lines", len(got))
	}
	want := usage.Tokens{Input: 100, Output: 10, CacheRead: 1000, CacheWrite: 50}
	if got[0].Tokens != want || got[0].Model != "claude-alt" || got[0].Purpose != "nextcode" || time.Since(got[0].At) > time.Minute || got[0].At.Location() != time.UTC {
		t.Fatalf("line: %+v", got[0])
	}
	if got[1].Tokens.CacheRead != 1100 {
		t.Fatalf("line: %+v", got[1])
	}
}

func TestNoLedger(t *testing.T) {
	cfg := newConfig(t, newFake(say("x")), newKit())
	cfg.homeDir = ""
	h := startAgent(t, cfg)
	h.initialize()
	h.say("go")
	h.result()
	if _, err := os.Stat(filepath.Join(filepath.Dir(cfg.stateDir), "home")); !os.IsNotExist(err) {
		t.Fatal("a ledger was written without NEXTCODE_HOME")
	}
}

// set switches the model, as the driver does.
func (h *harness) set(model string) {
	h.t.Helper()
	h.control("nb_m", map[string]any{"subtype": "set_model", "model": model})
	h.until(is("control_response"))
}
