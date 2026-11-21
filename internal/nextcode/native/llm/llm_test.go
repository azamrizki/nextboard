package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	retryWaits = []time.Duration{0, 0, 0}
	os.Exit(m.Run())
}

// ---- a provider that answers from a script

// seen is a request the fake provider got.
type seen struct {
	Path   string
	Header http.Header
	Body   string
}

// fake is a provider that answers its n-th request with its n-th answer, and the last answer
// to any after.
type fake struct {
	srv     *httptest.Server
	mu      sync.Mutex
	got     []seen
	answers []http.HandlerFunc
}

func newFake(t *testing.T, answers ...http.HandlerFunc) *fake {
	t.Helper()
	f := &fake{answers: answers}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.got = append(f.got, seen{Path: r.URL.Path, Header: r.Header.Clone(), Body: string(b)})
		a := f.answers[min(len(f.got), len(f.answers))-1]
		f.mu.Unlock()
		a(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) requests() []seen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]seen(nil), f.got...)
}

func (f *fake) last(t *testing.T) seen {
	t.Helper()
	got := f.requests()
	if len(got) == 0 {
		t.Fatal("no request")
	}
	return got[len(got)-1]
}

// stream answers with server-sent events.
func stream(events ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, e := range events {
			io.WriteString(w, e)
			w.(http.Flusher).Flush()
		}
	}
}

// reply answers with a status and a body.
func reply(status int, body string, header ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i+1 < len(header); i += 2 {
			w.Header().Set(header[i], header[i+1])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}
}

// ev is one named event; data is one unnamed one.
func ev(name, data string) string { return "event: " + name + "\ndata: " + data + "\n\n" }
func data(d string) string        { return "data: " + d + "\n\n" }

// ---- the events

type recorder struct{ events []Event }

func (r *recorder) on(e Event) { r.events = append(r.events, e) }

func (r *recorder) kind(k string) []Event {
	var out []Event
	for _, e := range r.events {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

// checkEvents checks the event contract: one EvBlockStart per block, at its index and in
// index order, before its deltas, and the deltas of each block joining to what it says.
func checkEvents(t *testing.T, events []Event, resp *Response) {
	t.Helper()
	started := map[int]bool{}
	joined := map[int]*strings.Builder{}
	for _, e := range events {
		switch e.Kind {
		case EvBlockStart:
			if started[e.Index] {
				t.Errorf("block %d started twice", e.Index)
			}
			if e.Index != len(started) {
				t.Errorf("block %d started out of order", e.Index)
			}
			started[e.Index] = true
			joined[e.Index] = &strings.Builder{}
			if e.Index < len(resp.Blocks) {
				b := resp.Blocks[e.Index]
				if b.Type != e.BlockType || (b.Type == BlockToolUse && (b.ID != e.ToolID || b.Name != e.ToolName)) {
					t.Errorf("start of block %d = %+v, the block is %+v", e.Index, e, b)
				}
			}
		case EvDelta:
			if !started[e.Index] {
				t.Errorf("delta of block %d before its start", e.Index)
				continue
			}
			joined[e.Index].WriteString(e.Text)
		}
	}
	if len(started) != len(resp.Blocks) {
		t.Errorf("%d blocks started, the answer has %d", len(started), len(resp.Blocks))
	}
	for i, b := range resp.Blocks {
		got := ""
		if j := joined[i]; j != nil {
			got = j.String()
		}
		if b.Type == BlockToolUse {
			if string(objectInput(got)) != string(b.Input) {
				t.Errorf("deltas of block %d join to %q, its input is %s", i, got, b.Input)
			}
		} else if got != b.Text {
			t.Errorf("deltas of block %d join to %q, its text is %q", i, got, b.Text)
		}
	}
}

func apiErr(t *testing.T, err error) *APIError {
	t.Helper()
	var e *APIError
	if !errors.As(err, &e) {
		t.Fatalf("error = %v (%T), want an *APIError", err, err)
	}
	return e
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ---- the shared parts

func TestNewClientPicksTheWire(t *testing.T) {
	for _, tc := range []struct {
		cfg  ProviderConfig
		want string
	}{
		{ProviderConfig{Kind: "anthropic"}, "*llm.anthropicClient"},
		{ProviderConfig{Kind: "openai"}, "*llm.chatClient"},
		{ProviderConfig{Kind: "openai", API: "chat"}, "*llm.chatClient"},
		{ProviderConfig{Kind: "openai", API: "responses"}, "*llm.responsesClient"},
	} {
		if got := fmt.Sprintf("%T", NewClient(tc.cfg, nil)); got != tc.want {
			t.Errorf("%+v: %s, want %s", tc.cfg, got, tc.want)
		}
	}
}

func TestOwnHTTPClientHasNoOverallTimeout(t *testing.T) {
	hc := ownHTTP()
	if hc.Timeout != 0 {
		t.Errorf("timeout = %s", hc.Timeout)
	}
	tr, ok := hc.Transport.(*http.Transport)
	if !ok || tr.ResponseHeaderTimeout != headerTimeout {
		t.Errorf("transport = %#v", hc.Transport)
	}
}

func TestURLs(t *testing.T) {
	ok := map[string]http.HandlerFunc{
		WireAnthropic: stream(antText1...),
		WireChat:      stream(chatText1...),
		WireResponses: stream(respText1...),
	}
	for _, tc := range []struct {
		cfg  ProviderConfig
		base string
		want string
	}{
		{ProviderConfig{Kind: "anthropic"}, "", "/v1/messages"},
		{ProviderConfig{Kind: "anthropic"}, "/", "/v1/messages"},
		{ProviderConfig{Kind: "anthropic"}, "/api/anthropic/", "/api/anthropic/v1/messages"},
		{ProviderConfig{Kind: "openai"}, "/v1", "/v1/chat/completions"},
		{ProviderConfig{Kind: "openai"}, "/v1/", "/v1/chat/completions"},
		{ProviderConfig{Kind: "openai", API: "responses"}, "/v1", "/v1/responses"},
		{ProviderConfig{Kind: "openai", API: "responses"}, "/v1/", "/v1/responses"},
	} {
		f := newFake(t, ok[tc.cfg.Wire()])
		tc.cfg.BaseURL = f.srv.URL + tc.base
		if _, err := NewClient(tc.cfg, nil).Stream(context.Background(), Request{Model: "m"}, nil); err != nil {
			t.Fatalf("%s: %v", tc.cfg.BaseURL, err)
		}
		if got := f.last(t).Path; got != tc.want {
			t.Errorf("%s%s: path %s, want %s", tc.cfg.Kind, tc.base, got, tc.want)
		}
	}
}

func TestRetryAfter(t *testing.T) {
	h := func(v string) http.Header { return http.Header{"Retry-After": {v}} }
	for _, tc := range []struct {
		v    string
		want time.Duration
	}{
		{"", -1},
		{"0", 0},
		{"2", 2 * time.Second},
		{"120", 60 * time.Second},
		{"99999999999999", 60 * time.Second},
		{"-3", -1},
		{"soon", -1},
	} {
		if got := retryAfter(h(tc.v)); got != tc.want {
			t.Errorf("Retry-After %q = %s, want %s", tc.v, got, tc.want)
		}
	}
	date := time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)
	if got := retryAfter(h(date)); got < 25*time.Second || got > 30*time.Second {
		t.Errorf("Retry-After %q = %s", date, got)
	}
	if got := retryAfter(h("Mon, 02 Jan 2006 15:04:05 GMT")); got != 0 {
		t.Errorf("a date gone = %s", got)
	}
}

func TestBackoff(t *testing.T) {
	saved := retryWaits
	defer func() { retryWaits = saved }()
	retryWaits = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	for n, base := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 9: 4 * time.Second} {
		got := failure(&APIError{}, true, "").wait(n)
		if got < base || got > base+base/5 {
			t.Errorf("wait after attempt %d = %s, want %s and a little", n, got, base)
		}
	}
	if got := (&callError{after: 3 * time.Second}).wait(1); got != 3*time.Second {
		t.Errorf("Retry-After wait = %s", got)
	}
}

func TestSleepEndsWithTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := sleep(ctx, time.Minute); !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Errorf("sleep = %v after %s", err, time.Since(start))
	}
}

func TestObjectInput(t *testing.T) {
	for in, want := range map[string]string{
		"":                         `{}`,
		"  ":                       `{}`,
		`{"a": 1, "b": [1, 2]}`:    `{"a":1,"b":[1,2]}`,
		`{"path":"<a&b>"}`:         `{"path":"<a&b>"}`,
		`{"a":`:                    `{"_invalid_json":"{\"a\":"}`,
		`[1,2]`:                    `{"_invalid_json":"[1,2]"}`,
		`"text"`:                   `{"_invalid_json":"\"text\""}`,
		`null`:                     `{"_invalid_json":"null"}`,
		`{"a":1}{"b":2}`:           `{"_invalid_json":"{\"a\":1}{\"b\":2}"}`,
		`{"cmd":"a && b > c.txt"}`: `{"cmd":"a && b > c.txt"}`,
	} {
		if got := string(objectInput(in)); got != want {
			t.Errorf("objectInput(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestParseDetail(t *testing.T) {
	for body, want := range map[string]detail{
		`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`:             {message: "Overloaded", kind: "overloaded_error"},
		`{"error":{"message":"Bad key","type":"invalid_request_error","code":"invalid_api_key"}}`: {message: "Bad key", kind: "invalid_request_error", code: "invalid_api_key"},
		`{"error":{"message":"Upstream down","code":502}}`:                                        {message: "Upstream down", status: 502},
		`{"error":"no such model"}`: {message: "no such model"},
		`{"type":"error","code":"server_error","message":"Something went wrong","sequence_number":3}`: {message: "Something went wrong", kind: "error", code: "server_error"},
		`{"detail":"Not Found"}`: {message: "Not Found"},
	} {
		got, ok := parseDetail([]byte(body))
		if !ok || got != want {
			t.Errorf("parseDetail(%s) = %+v %v, want %+v", body, got, ok, want)
		}
	}
	for _, body := range []string{"", "<html>502</html>", `{"id":"x"}`, `[1]`} {
		if d, ok := parseDetail([]byte(body)); ok {
			t.Errorf("parseDetail(%q) = %+v, want none", body, d)
		}
	}
}

func TestAPIErrorMessage(t *testing.T) {
	c := &conn{cfg: ProviderConfig{Key: "sk-secret-123"}}
	e := c.apiError(401, "  invalid key\n  sk-secret-123  ", detail{})
	if e.Message != "401 invalid key …" || e.Status != 401 || e.ContextOverflow {
		t.Errorf("error = %+v", e)
	}
	long := c.apiError(500, strings.Repeat("x", 2000), detail{})
	if r := []rune(long.Message); len(r) > 510 || !strings.HasSuffix(long.Message, "…") {
		t.Errorf("long message of %d runes", len(r))
	}
	if e := c.apiError(429, "too many tokens per minute", detail{}); e.ContextOverflow {
		t.Error("a rate limit is no context overflow")
	}
	if e := c.apiError(0, "could not reach it: dial tcp", detail{}); e.Message != "could not reach it: dial tcp" {
		t.Errorf("status 0 message = %q", e.Message)
	}
}

func TestSSEReader(t *testing.T) {
	in := ": keep-alive\r\n\r\nevent: one\r\ndata: {\"a\":1}\r\n\r\n" +
		"data: line 1\ndata: line 2\nid: 7\nretry: 100\n\n" +
		"event: empty\n\n" +
		"data:no space\n\n" +
		"event: last\ndata: tail"
	r := newSSE(strings.NewReader(in))
	want := []sseEvent{{"one", []byte(`{"a":1}`)}, {"", []byte("line 1\nline 2")}, {"", []byte("no space")}, {"last", []byte("tail")}}
	for i, w := range want {
		got, err := r.next()
		if err != nil || got.name != w.name || string(got.data) != string(w.data) {
			t.Fatalf("event %d = %q %q %v, want %q %q", i, got.name, got.data, err, w.name, w.data)
		}
	}
	if _, err := r.next(); err != io.EOF {
		t.Errorf("after the last event: %v", err)
	}
}

func TestRetriesRunOutAfterFourAttempts(t *testing.T) {
	f := newFake(t, reply(503, `{"error":{"message":"busy","type":"server_error"}}`))
	rec := &recorder{}
	_, err := NewClient(ProviderConfig{Kind: "openai", BaseURL: f.srv.URL}, nil).Stream(context.Background(), Request{Model: "m"}, rec.on)
	e := apiErr(t, err)
	if e.Status != 503 || e.Message != "503 busy" {
		t.Errorf("error = %+v", e)
	}
	if n := len(f.requests()); n != 4 {
		t.Errorf("%d attempts, want 4", n)
	}
	retries := rec.kind(EvRetry)
	if len(retries) != 3 {
		t.Fatalf("retries = %+v", retries)
	}
	for i, r := range retries {
		if r.Attempt != i+2 || r.MaxAttempts != 4 || r.Text != "503 unavailable" {
			t.Errorf("retry %d = %+v", i, r)
		}
	}
}

func TestNoRetryOfAClientError(t *testing.T) {
	f := newFake(t, reply(400, `{"error":{"message":"bad tools","type":"invalid_request_error"}}`))
	_, err := NewClient(ProviderConfig{Kind: "openai", BaseURL: f.srv.URL}, nil).Stream(context.Background(), Request{Model: "m"}, nil)
	if e := apiErr(t, err); e.Status != 400 || e.ContextOverflow {
		t.Errorf("error = %+v", e)
	}
	if n := len(f.requests()); n != 1 {
		t.Errorf("%d attempts", n)
	}
}

func TestNoRetryWithoutQuota(t *testing.T) {
	f := newFake(t, reply(429, `{"error":{"message":"You exceeded your current quota","type":"insufficient_quota","code":"insufficient_quota"}}`))
	_, err := NewClient(ProviderConfig{Kind: "openai", BaseURL: f.srv.URL}, nil).Stream(context.Background(), Request{Model: "m"}, nil)
	apiErr(t, err)
	if n := len(f.requests()); n != 1 {
		t.Errorf("%d attempts", n)
	}
}

func TestUnreachable(t *testing.T) {
	f := newFake(t, reply(200, "{}"))
	url := f.srv.URL
	f.srv.Close()
	rec := &recorder{}
	_, err := NewClient(ProviderConfig{Kind: "openai", BaseURL: url, Key: "sk-k"}, nil).Stream(context.Background(), Request{Model: "m"}, rec.on)
	e := apiErr(t, err)
	if e.Status != 0 || !strings.HasPrefix(e.Message, "could not reach it: ") {
		t.Errorf("error = %+v", e)
	}
	if n := len(rec.kind(EvRetry)); n != 3 {
		t.Errorf("%d retries", n)
	}
}

func TestA200ErrorBody(t *testing.T) {
	f := newFake(t, reply(200, `{"error":{"message":"model not found","type":"invalid_request_error"}}`))
	_, err := NewClient(ProviderConfig{Kind: "openai", BaseURL: f.srv.URL}, nil).Stream(context.Background(), Request{Model: "m"}, nil)
	if e := apiErr(t, err); e.Status != 400 || e.Message != "400 model not found" {
		t.Errorf("error = %+v", e)
	}
}

func TestAStreamThatStopsIsRetried(t *testing.T) {
	f := newFake(t, stream(data(`{"id":"c","choices":[{"index":0,"delta":{"role":"assistant"}}]}`)), stream(chatText1...))
	rec := &recorder{}
	resp, err := NewClient(ProviderConfig{Kind: "openai", BaseURL: f.srv.URL}, nil).Stream(context.Background(), Request{Model: "m"}, rec.on)
	if err != nil || resp.Blocks[0].Text != "Hello" {
		t.Fatalf("%+v %v", resp, err)
	}
	if r := rec.kind(EvRetry); len(r) != 1 || r[0].Text != "the stream broke" {
		t.Errorf("retries = %+v", r)
	}
}

func TestAStalledStreamEnds(t *testing.T) {
	saved := idleTimeout
	idleTimeout = 50 * time.Millisecond
	defer func() { idleTimeout = saved }()
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, ev("message_start", `{"type":"message_start","message":{"id":"m"}}`))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	start := time.Now()
	_, err := NewClient(ProviderConfig{Kind: "anthropic", BaseURL: f.srv.URL}, nil).Stream(context.Background(), Request{Model: "m"}, nil)
	e := apiErr(t, err)
	if e.Status != 0 || !strings.Contains(e.Message, "stalled") {
		t.Errorf("error = %+v", e)
	}
	if n := len(f.requests()); n != 4 {
		t.Errorf("%d attempts", n)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %s", d)
	}
}

// cancelMidStream streams the start of an answer and then nothing, and the caller stops the
// call at its first delta: Stream must return ctx.Err() at once.
func cancelMidStream(t *testing.T, cfg ProviderConfig, first ...string) {
	t.Helper()
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range first {
			io.WriteString(w, e)
		}
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	})
	cfg.BaseURL = f.srv.URL
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var at time.Time
	_, err := NewClient(cfg, nil).Stream(ctx, Request{Model: "m"}, func(e Event) {
		if e.Kind == EvDelta && at.IsZero() {
			at = time.Now()
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v (%T), want context.Canceled", err, err)
	}
	if at.IsZero() {
		t.Fatal("no delta came")
	}
	if d := time.Since(at); d > 2*time.Second {
		t.Errorf("returned %s after the cancel", d)
	}
}

func TestCancelMidStream(t *testing.T) {
	cancelMidStream(t, ProviderConfig{Kind: "anthropic"}, antText1[:4]...)
	cancelMidStream(t, ProviderConfig{Kind: "openai"}, chatText1[:2]...)
	cancelMidStream(t, ProviderConfig{Kind: "openai", API: "responses"}, respText1[:4]...)
}

func TestCancelledWhileWaitingToRetry(t *testing.T) {
	saved := retryWaits
	retryWaits = []time.Duration{time.Minute}
	defer func() { retryWaits = saved }()
	f := newFake(t, reply(503, "busy"))
	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	_, err := NewClient(ProviderConfig{Kind: "openai", BaseURL: f.srv.URL}, nil).Stream(ctx, Request{Model: "m"}, func(e Event) {
		if e.Kind == EvRetry {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || time.Since(start) > 2*time.Second {
		t.Errorf("error = %v after %s", err, time.Since(start))
	}
}

func TestMerged(t *testing.T) {
	in := []Message{
		{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "a"}}},
		{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "b"}}},
		{Role: RoleAssistant, Blocks: []Block{{Type: BlockText, Text: "c"}}},
	}
	out := merged(in)
	if len(out) != 2 || len(out[0].Blocks) != 2 || len(in[0].Blocks) != 1 {
		t.Errorf("merged = %+v, input now %+v", out, in)
	}
}

func TestAMislabelledStreamIsStillRead(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		for _, e := range chatText1 {
			io.WriteString(w, e)
		}
	})
	resp, err := NewClient(ProviderConfig{Kind: "openai", BaseURL: f.srv.URL}, nil).Stream(context.Background(), Request{Model: "m"}, nil)
	if err != nil || resp.Blocks[0].Text != "Hello" {
		t.Fatalf("%+v %v", resp, err)
	}
}

func TestAnAnswerWithoutAStream(t *testing.T) {
	f := newFake(t, reply(200, `{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"hi"}}]}`))
	_, err := NewClient(ProviderConfig{Kind: "openai", BaseURL: f.srv.URL}, nil).Stream(context.Background(), Request{Model: "m"}, nil)
	if e := apiErr(t, err); e.Status != 0 || !strings.HasPrefix(e.Message, "it answered without a stream: ") || len(f.requests()) != 1 {
		t.Errorf("error %+v after %d requests", e, len(f.requests()))
	}
}

func TestABadSchemaIsAnErrorOfTheCaller(t *testing.T) {
	f := newFake(t, stream(chatText1...))
	tools := []ToolDef{{Name: "Broken", Schema: json.RawMessage(`{"type":`)}}
	for _, cfg := range []ProviderConfig{{Kind: "anthropic"}, {Kind: "openai"}, {Kind: "openai", API: "responses"}} {
		cfg.BaseURL = f.srv.URL
		_, err := NewClient(cfg, nil).Stream(context.Background(), Request{Model: "m", Tools: tools}, nil)
		var e *APIError
		if err == nil || errors.As(err, &e) || !strings.Contains(err.Error(), "Broken") {
			t.Errorf("%s: error %v", cfg.Wire(), err)
		}
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("%d requests", n)
	}
}

func TestAnEmptyUserMessage(t *testing.T) {
	b, err := anthropicBody(Request{Model: "m", Messages: []Message{{Role: RoleUser, Blocks: []Block{{Type: BlockText}}}}}, false)
	if err != nil || !strings.Contains(string(b), `"content":[{"type":"text","text":"(no message)","cache_control":{"type":"ephemeral"}}]`) {
		t.Errorf("%s %v", b, err)
	}
}
