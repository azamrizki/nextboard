package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/orch"
)

// Lead chats over REST (docs/API-orchestration.md §4.1): list, new, per-chat messages, resume
// with its divider, rename, delete and clear, and their events.
func TestLeadChatsAPI(t *testing.T) {
	h := newTestHub(t)
	pid, wid, _ := orchSetup(t, h)
	ws, _ := h.events()
	base := "/api/threads/" + pid
	command := func(text string) {
		t.Helper()
		if st, r := h.do("POST", "/api/projects/"+pid+"/commands", map[string]any{"text": text, "workspaceId": wid}, nil); st != 202 {
			t.Fatalf("command %q: %d %v", text, st, r)
		}
	}
	chats := func() (string, []map[string]any) {
		t.Helper()
		st, r := h.do("GET", base+"/chats", nil, nil)
		if st != 200 {
			t.Fatalf("chats %d %v", st, r)
		}
		var items []map[string]any
		for _, x := range r["items"].([]any) {
			items = append(items, x.(map[string]any))
		}
		return r["active"].(string), items
	}
	messages := func(query string) (string, []map[string]any) {
		t.Helper()
		st, r := h.do("GET", base+"/messages"+query, nil, nil)
		if st != 200 {
			t.Fatalf("messages%s %d %v", query, st, r)
		}
		var items []map[string]any
		for _, x := range r["items"].([]any) {
			items = append(items, x.(map[string]any))
		}
		return r["chatId"].(string), items
	}

	command("help") // the parser answers
	first, items := chats()
	if !orch.ValidChatID(first) || len(items) != 1 || items[0]["id"] != first || items[0]["title"] != "help" || items[0]["titled"] != true || items[0]["messages"] != float64(2) {
		t.Fatalf("first chat %q %v", first, items)
	}
	if chat, msgs := messages(""); chat != first || len(msgs) != 2 || msgs[0]["chatId"] != first || msgs[1]["chatId"] != first {
		t.Fatalf("messages %q %v", chat, msgs)
	}

	// a new chat: 201, active, empty; the old one is kept
	st, nc := h.do("POST", base+"/chats", map[string]any{}, nil)
	second, _ := nc["id"].(string)
	if st != http.StatusCreated || second == first || nc["title"] != "New chat" || nc["titled"] != nil || nc["threadId"] != pid {
		t.Fatalf("new chat %d %v", st, nc)
	}
	waitEvent(t, ws, func(ev map[string]any) bool {
		return ev["type"] == "leadChats" && ev["threadId"] == pid && ev["active"] == second
	})
	if chat, msgs := messages(""); chat != second || len(msgs) != 0 {
		t.Fatalf("the new chat %q %v", chat, msgs)
	}
	if chat, msgs := messages("?chat=" + first); chat != first || len(msgs) != 2 {
		t.Fatalf("?chat= %q %v", chat, msgs)
	}
	if st, r := h.do("GET", base+"/messages?chat=lc_nothere01", nil, nil); st != 404 || r["code"] != "chat_not_found" {
		t.Fatalf("missing chat %d %v", st, r)
	}
	command("what can you do")
	if _, msgs := messages(""); len(msgs) != 2 || msgs[0]["text"] != "what can you do" || msgs[0]["chatId"] != second {
		t.Fatalf("a command goes to the active chat: %v", msgs)
	}
	if active, items := chats(); active != second || len(items) != 2 || items[0]["id"] != second || items[0]["title"] != "what can you do" {
		t.Fatalf("list, latest first %q %v", active, items)
	}

	// rename
	if st, r := h.do("PATCH", base+"/chats/"+first, map[string]any{"title": "Getting started"}, nil); st != 200 || r["title"] != "Getting started" || r["titled"] != true {
		t.Fatalf("rename %d %v", st, r)
	}
	if st, r := h.do("PATCH", base+"/chats/"+first, map[string]any{"title": "  "}, nil); st != 400 || r["code"] != "bad_title" {
		t.Fatalf("empty title %d %v", st, r)
	}
	if st, r := h.do("PATCH", base+"/chats/not-a-chat", map[string]any{"title": "x"}, nil); st != 404 || r["code"] != "chat_not_found" {
		t.Fatalf("bad chat id %d %v", st, r)
	}

	// resume the first chat: active again, with a divider
	if st, r := h.do("POST", base+"/chats/"+first+"/activate", map[string]any{}, nil); st != 200 || r["id"] != first || r["messages"] != float64(3) {
		t.Fatalf("activate %d %v", st, r)
	}
	waitEvent(t, ws, func(ev map[string]any) bool {
		return ev["type"] == "leadChats" && ev["threadId"] == pid && ev["active"] == first
	})
	_, msgs := messages("")
	if div := msgs[len(msgs)-1]; div["source"] != "resume" || div["kind"] != "note" || div["role"] != "hub" || !strings.Contains(div["text"].(string), "has all 2 messages in full") {
		t.Fatalf("divider %v", div)
	}
	if st, r := h.do("POST", base+"/chats/lc_nothere01/activate", map[string]any{}, nil); st != 404 || r["code"] != "chat_not_found" {
		t.Fatalf("activate a missing chat %d %v", st, r)
	}

	// delete: not the active one; the other goes with its messages
	if st, r := h.do("DELETE", base+"/chats/"+first, nil, nil); st != 409 || r["code"] != "chat_active" {
		t.Fatalf("delete the active chat %d %v", st, r)
	}
	if st, r := h.do("DELETE", base+"/chats/"+second, nil, nil); st != 200 || r["ok"] != true {
		t.Fatalf("delete %d %v", st, r)
	}
	if _, items := chats(); len(items) != 1 || items[0]["id"] != first {
		t.Fatalf("after delete %v", items)
	}
	if st, _ := h.do("GET", base+"/messages?chat="+second, nil, nil); st != 404 {
		t.Fatalf("the deleted chat's messages: %d", st)
	}

	// clear empties only the active chat
	if st, _ := h.do("POST", base+"/chats", map[string]any{}, nil); st != 201 {
		t.Fatalf("new chat %d", st)
	}
	command("help")
	third, _ := chats()
	if st, r := h.do("DELETE", base, nil, nil); st != 200 {
		t.Fatalf("clear %d %v", st, r)
	}
	waitEvent(t, ws, func(ev map[string]any) bool {
		return ev["type"] == "threadCleared" && ev["threadId"] == pid && ev["chatId"] == third
	})
	if chat, msgs := messages(""); chat != third || len(msgs) != 0 {
		t.Fatalf("cleared chat %q %v", chat, msgs)
	}
	if _, msgs := messages("?chat=" + first); len(msgs) != 3 {
		t.Fatalf("the other chat after a clear: %v", msgs)
	}
	if active, items := chats(); active != third || len(items) != 2 {
		t.Fatalf("chats after a clear %q %v", active, items)
	}

	if st, r := h.do("GET", "/api/threads/x_bad/chats", nil, nil); st != 400 || r["code"] != "bad_thread" {
		t.Fatalf("bad thread %d %v", st, r)
	}
}

// The errors the web tells apart.
func TestLeadChatErrCodes(t *testing.T) {
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{
		{orch.ErrChatNotFound, 404, "chat_not_found"},
		{orch.ErrLeadBusy, 409, "lead_busy"},
		{orch.ErrResumeBusy, 409, "lead_busy"},
		{orch.ErrChatAnswering, 409, "lead_busy"},
		{orch.ErrChatActive, 409, "chat_active"},
		{orch.ErrChatMission, 409, "chat_mission"},
		{orch.ErrBadChatTitle, 400, "bad_title"},
		{errors.New("disk full"), 500, "internal"},
	} {
		rec := httptest.NewRecorder()
		leadChatErr(rec, c.err)
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), `"code":"`+c.code+`"`) {
			t.Fatalf("%v → %d %s", c.err, rec.Code, rec.Body.String())
		}
	}
}
