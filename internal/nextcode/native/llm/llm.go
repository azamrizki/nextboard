package llm

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The clients of the three wires share their HTTP here: the headers of every request, the
// retries of what is worth trying again, and the errors the caller gets. Each wire builds its
// own body and reads its own stream (anthropic.go, chat.go, responses.go) with the reader of
// sse.go.

const (
	userAgent   = "NextBoard-NextCode"
	maxAttempts = 4
)

// The waits of the retries and of the stream. They are variables so that the tests can make
// them short.
var (
	// retryWaits are the waits before the second, third and fourth attempt, each with up to a
	// fifth more of jitter, when the provider did not say how long to wait.
	retryWaits = []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second}
	// maxRetryAfter caps the wait a provider's Retry-After asks for.
	maxRetryAfter = 60 * time.Second
	// headerTimeout is how long a client of its own waits for the headers of an answer.
	headerTimeout = 120 * time.Second
	// idleTimeout ends a stream that sent nothing for this long (0 = never).
	idleTimeout = 5 * time.Minute
)

// NewClient is the client of a provider's wire. hc nil = a client of its own: with no overall
// timeout, since an answer can stream for many minutes, but a limit on the wait for the
// headers. Every stream ends once nothing came for idleTimeout, whichever client it runs on.
func NewClient(cfg ProviderConfig, hc *http.Client) Client {
	if hc == nil {
		hc = ownHTTP()
	}
	c := &conn{cfg: cfg, base: strings.TrimRight(cfg.BaseURL, "/"), hc: hc}
	switch cfg.Wire() {
	case WireAnthropic:
		return &anthropicClient{c: c}
	case WireResponses:
		return &responsesClient{c: c}
	}
	return &chatClient{c: c}
}

func ownHTTP() *http.Client {
	t, ok := http.DefaultTransport.(*http.Transport)
	if ok {
		t = t.Clone()
	} else {
		t = &http.Transport{Proxy: http.ProxyFromEnvironment}
	}
	t.ResponseHeaderTimeout = headerTimeout
	return &http.Client{Transport: t}
}

// conn is what the clients of every wire share: the provider and the HTTP client.
type conn struct {
	cfg  ProviderConfig
	base string // cfg.BaseURL without a trailing "/"
	hc   *http.Client
}

// bearer is the auth of the OpenAI-compatible APIs.
func (c *conn) bearer(h http.Header) {
	if c.cfg.Key != "" {
		h.Set("Authorization", "Bearer "+c.cfg.Key)
	}
}

// ---- attempts and retries

// callError is an attempt that failed: the error the caller gets, whether another attempt may
// help, and how long the provider asked to wait first.
type callError struct {
	api   *APIError
	retry bool
	after time.Duration // the provider's Retry-After, -1 = it said nothing
	why   string        // for EvRetry
}

func failure(api *APIError, retry bool, why string) *callError {
	return &callError{api: api, retry: retry, after: -1, why: why}
}

// attempt is one try of a call: the request and the reading of its stream. emit is how the
// answer streams to the caller.
type attempt func(ctx context.Context, emit func(Event)) (*Response, *callError)

// run makes a call, trying it again while another attempt may help and nothing of the answer
// was shown: once a block started, the caller has shown part of the answer, and the failure is
// its error. A cancelled ctx is ctx.Err(), never an APIError.
func (c *conn) run(ctx context.Context, on func(Event), try attempt) (*Response, error) {
	if on == nil {
		on = func(Event) {}
	}
	for n := 1; ; n++ {
		shown := false
		resp, ce := try(ctx, func(e Event) {
			shown = shown || e.Kind == EvBlockStart || e.Kind == EvDelta
			on(e)
		})
		switch {
		case ce == nil:
			return resp, nil
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case !ce.retry || shown || n >= maxAttempts:
			return nil, ce.api
		}
		on(Event{Kind: EvRetry, Attempt: n + 1, MaxAttempts: maxAttempts, Text: ce.why})
		if err := sleep(ctx, ce.wait(n)); err != nil {
			return nil, err
		}
	}
}

// wait is how long to wait after attempt n failed: what the provider asked, or the backoff.
func (e *callError) wait(n int) time.Duration {
	if e.after >= 0 {
		return e.after
	}
	if len(retryWaits) == 0 {
		return 0
	}
	d := retryWaits[min(n, len(retryWaits))-1]
	if j := int64(d / 5); j > 0 {
		d += time.Duration(rand.Int64N(j))
	}
	return d
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// retryAfter is the wait a Retry-After header asks for, in seconds or as an HTTP date, at most
// maxRetryAfter; -1 when it asks none.
func retryAfter(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return -1
	}
	var d time.Duration
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n < 0 {
			return -1
		}
		d = time.Duration(min(n, 3600)) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		d = max(time.Until(t), 0)
	} else {
		return -1
	}
	return min(d, maxRetryAfter)
}

// retryStatus says whether another attempt may help after this HTTP status.
func retryStatus(s int) bool {
	switch s {
	case 408, 409, 425, 429, 500, 502, 503, 504, 529:
		return true
	}
	return false
}

var statusWords = map[int]string{408: "timeout", 409: "conflict", 425: "too early", 429: "rate limited",
	500: "server error", 502: "bad gateway", 503: "unavailable", 504: "gateway timeout", 529: "overloaded"}

// retryReason is why an attempt is tried again, as EvRetry says it.
func retryReason(status int) string {
	if w, ok := statusWords[status]; ok {
		return strconv.Itoa(status) + " " + w
	}
	return strconv.Itoa(status)
}

// ---- the request

// errStalled is a stream that sent nothing for idleTimeout.
var errStalled = errors.New("stalled")

// open posts one attempt's body to path and returns the stream of the answer, or the error of
// the provider's refusal.
func (c *conn) open(ctx context.Context, path string, body []byte, header func(http.Header)) (io.ReadCloser, *callError) {
	actx, cancel := context.WithCancelCause(ctx)
	req, err := http.NewRequestWithContext(actx, http.MethodPost, c.base+path, bytes.NewReader(body))
	if err != nil {
		cancel(nil)
		return nil, failure(c.apiError(0, "bad provider URL: "+err.Error(), detail{}), false, "")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	header(req.Header)
	resp, err := c.hc.Do(req)
	if err != nil {
		cancel(nil)
		return nil, failure(c.apiError(0, "could not reach it: "+err.Error(), detail{}), true, "could not connect")
	}
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		cancel(nil)
		return nil, c.statusError(resp.StatusCode, resp.Header, b)
	}
	w := &watched{ReadCloser: resp.Body, ctx: actx, cancel: cancel, idle: idleTimeout}
	if w.idle > 0 {
		w.timer = time.AfterFunc(w.idle, func() { cancel(errStalled) })
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		return w, nil
	}
	// a 2xx that is no stream: an error some gateways give in place of one, or a stream a
	// server mislabels, which is read whole here and then streamed
	b, err := io.ReadAll(io.LimitReader(w, 1<<20))
	switch d, ok := parseDetail(b); {
	case ok:
		w.Close()
		return nil, c.streamError(d)
	case err != nil:
		w.Close()
		return nil, c.broken(err)
	case json.Valid(b):
		w.Close()
		return nil, failure(c.apiError(0, "it answered without a stream: "+string(b), detail{}), false, "")
	}
	return struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(b), w), w}, nil
}

// watched is the body of a streaming answer, whose request ends once nothing came for idle: a
// provider that hangs mid-answer must not hang the agent.
type watched struct {
	io.ReadCloser
	ctx    context.Context
	cancel context.CancelCauseFunc
	idle   time.Duration
	timer  *time.Timer
}

func (w *watched) Read(p []byte) (int, error) {
	n, err := w.ReadCloser.Read(p)
	if n > 0 && w.timer != nil {
		w.timer.Reset(w.idle)
	}
	if err != nil && errors.Is(context.Cause(w.ctx), errStalled) {
		err = errStalled
	}
	return n, err
}

func (w *watched) Close() error {
	if w.timer != nil {
		w.timer.Stop()
	}
	err := w.ReadCloser.Close()
	w.cancel(nil)
	return err
}

// broken is a stream that ended before the answer did: the connection dropped, it stalled, or
// the server just stopped. Another attempt may help, if nothing was shown yet.
func (c *conn) broken(err error) *callError {
	msg := "the stream ended before the answer did"
	switch {
	case errors.Is(err, errStalled):
		msg = fmt.Sprintf("the stream stalled: nothing came for %s", idleTimeout)
	case err != nil && !errors.Is(err, io.EOF):
		msg = "the stream broke: " + err.Error()
	}
	return failure(c.apiError(0, msg, detail{}), true, "the stream broke")
}

// ---- errors

// detail is what an error body or a streamed error says.
type detail struct {
	message string
	kind    string // Anthropic's error.type, OpenAI's error.type
	code    string // OpenAI's error.code
	status  int    // a numeric code, as some gateways give it
}

// parseDetail reads an error of either family: {"error":{"type","message"}} of Anthropic,
// {"error":{"message","code","type"}} of OpenAI, {"error":"text"}, or a flat
// {"code","message"} as a Responses stream's error event has it. ok = it is one.
func parseDetail(b []byte) (detail, bool) {
	var top struct {
		Error   json.RawMessage `json:"error"`
		Type    json.RawMessage `json:"type"`
		Code    json.RawMessage `json:"code"`
		Message json.RawMessage `json:"message"`
		Detail  json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(b, &top) != nil {
		return detail{}, false
	}
	if s, ok := jsonString(top.Error); ok {
		return detail{message: s}, s != ""
	}
	if len(top.Error) > 0 && top.Error[0] == '{' {
		var e struct {
			Type    json.RawMessage `json:"type"`
			Code    json.RawMessage `json:"code"`
			Message json.RawMessage `json:"message"`
		}
		if json.Unmarshal(top.Error, &e) == nil {
			d := detailOf(e.Type, e.Code, e.Message)
			return d, d != detail{}
		}
	}
	d := detailOf(top.Type, top.Code, top.Message)
	if d.message == "" {
		d.message, _ = jsonString(top.Detail)
	}
	return d, d.message != ""
}

func detailOf(typ, code, msg json.RawMessage) detail {
	d := detail{}
	d.kind, _ = jsonString(typ)
	d.message, _ = jsonString(msg)
	if s, ok := jsonString(code); ok {
		d.code = s
	} else if n, err := strconv.Atoi(string(code)); err == nil {
		d.status = n
	}
	return d
}

// streamKinds are the kinds of error a provider can send in place of the answer or in its
// stream: the HTTP status each stands for, and whether another attempt may help.
var streamKinds = map[string]struct {
	status int
	retry  bool
}{
	"overloaded_error": {529, true}, "overloaded": {529, true},
	"api_error": {500, true}, "server_error": {500, true},
	"rate_limit_error": {429, true}, "rate_limit_exceeded": {429, true},
	"invalid_request_error": {400, false}, "context_length_exceeded": {400, false},
	"authentication_error": {401, false}, "permission_error": {403, false},
	"not_found_error": {404, false}, "request_too_large": {413, false},
}

// streamError is an error the provider sent with a 2xx: in its stream, or as the whole body.
// A kind it does not know is a 500 not worth retrying.
func (c *conn) streamError(d detail) *callError {
	status, retry := 500, false
	if d.status >= 400 && d.status <= 599 {
		status, retry = d.status, retryStatus(d.status)
	} else if k, ok := streamKinds[d.kind]; ok {
		status, retry = k.status, k.retry
	} else if k, ok := streamKinds[d.code]; ok {
		status, retry = k.status, k.retry
	}
	msg := cmp.Or(d.message, d.kind, d.code, "unknown error")
	return failure(c.apiError(status, msg, d), retry && !noQuota(d), retryReason(status))
}

// statusError is a refusal of the provider: its status, and the message of its body.
func (c *conn) statusError(status int, h http.Header, body []byte) *callError {
	d, _ := parseDetail(body)
	msg := cmp.Or(d.message, string(body), http.StatusText(status))
	ce := failure(c.apiError(status, msg, d), retryStatus(status) && !noQuota(d), retryReason(status))
	ce.after = retryAfter(h)
	return ce
}

// noQuota is OpenAI's 429 of an account without credit: waiting does not help it.
func noQuota(d detail) bool { return d.code == "insufficient_quota" || d.kind == "insufficient_quota" }

// apiError is the error the caller gets: "<status> <message>", the whitespace collapsed, at
// most about 500 characters, and the key never in it.
func (c *conn) apiError(status int, msg string, d detail) *APIError {
	msg = strings.Join(strings.Fields(msg), " ")
	if c.cfg.Key != "" {
		msg = strings.ReplaceAll(msg, c.cfg.Key, "…")
	}
	if r := []rune(msg); len(r) > 500 {
		msg = string(r[:500]) + "…"
	}
	e := &APIError{Status: status, Message: msg, ContextOverflow: status != http.StatusTooManyRequests && overflow(msg, d)}
	if status != 0 {
		e.Message = strings.TrimSpace(strconv.Itoa(status) + " " + msg)
	}
	return e
}

// overflowPhrases are how the providers say the conversation is longer than the model takes:
// Anthropic and the vendors behind its API, then OpenAI and the servers built like it. Both
// lists apply to both families, since a gateway of one often fronts a model of the other.
var overflowPhrases = []string{"prompt is too long", "context window", "too many tokens", "exceeds the maximum",
	"exceed context limit", "maximum context length", "context length", "context_window"}

func overflow(msg string, d detail) bool {
	if d.code == "context_length_exceeded" || d.kind == "context_length_exceeded" {
		return true
	}
	m := strings.ToLower(msg)
	for _, p := range overflowPhrases {
		if strings.Contains(m, p) {
			return true
		}
	}
	return false
}

// ---- JSON

// enc is v as JSON, with <, > and & as they are: code is full of them.
func enc(v any) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte{'\n'}), nil
}

// object is a JSON object written field by field, in order. raw values go in as they are, byte
// for byte: encoding/json would compact a json.RawMessage, and a replayed block must reach the
// provider exactly as the provider gave it.
type object struct {
	buf bytes.Buffer
	err error
}

func (o *object) set(key string, v any) *object {
	b, err := enc(v)
	if err != nil {
		if o.err == nil {
			o.err = fmt.Errorf("llm: %s: %w", key, err)
		}
		return o
	}
	return o.raw(key, b)
}

func (o *object) raw(key string, v []byte) *object {
	if o.buf.Len() == 0 {
		o.buf.WriteByte('{')
	} else {
		o.buf.WriteByte(',')
	}
	k, _ := enc(key) // a string always encodes
	o.buf.Write(k)
	o.buf.WriteByte(':')
	o.buf.Write(v)
	return o
}

func (o *object) bytes() ([]byte, error) {
	if o.err != nil {
		return nil, o.err
	}
	if o.buf.Len() == 0 {
		return []byte("{}"), nil
	}
	return append(o.buf.Bytes(), '}'), nil
}

// array is a JSON array of encoded items.
func array(items [][]byte) []byte {
	return append(append([]byte{'['}, bytes.Join(items, []byte{','})...), ']')
}

// jsonString is raw as a string, when it is a JSON string.
func jsonString(raw json.RawMessage) (string, bool) {
	var s string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// argText is a tool call's arguments as text: a JSON string as it says, or an object some
// servers send in place of one, as its JSON.
func argText(raw json.RawMessage) string {
	if s, ok := jsonString(raw); ok {
		return s
	}
	if t := bytes.TrimSpace(raw); len(t) > 0 && t[0] == '{' {
		return string(t)
	}
	return ""
}

// objectInput is a tool call's JSON input as a JSON object: "" is {}, and text that is not an
// object is kept as {"_invalid_json": text}, so the agent can tell the model what was wrong.
func objectInput(s string) json.RawMessage {
	t := strings.TrimSpace(s)
	if t == "" {
		return json.RawMessage("{}")
	}
	if t[0] == '{' && json.Valid([]byte(t)) {
		var b bytes.Buffer
		if json.Compact(&b, []byte(t)) == nil {
			return b.Bytes()
		}
	}
	b, _ := enc(map[string]string{"_invalid_json": s}) // a string always encodes
	return b
}

// inputOf is a tool_use block's input to send: always a JSON object.
func inputOf(b Block) json.RawMessage { return objectInput(string(b.Input)) }

// schemaOf is a tool's input schema to send: an empty one is an object of no properties.
func schemaOf(t ToolDef) (json.RawMessage, error) {
	if len(bytes.TrimSpace(t.Schema)) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`), nil
	}
	if !json.Valid(t.Schema) {
		return nil, fmt.Errorf("llm: the schema of tool %s is not JSON", t.Name)
	}
	return t.Schema, nil
}

// ---- the conversation

// noAnswer stands in for an assistant message left with no blocks, so that the roles still
// alternate.
const noAnswer = "(no answer)"

// merged is msgs with each run of messages of the same role joined into one: the APIs want
// the roles to alternate. The caller's slices are not changed.
func merged(msgs []Message) []Message {
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		if n := len(out); n > 0 && out[n-1].Role == m.Role {
			out[n-1].Blocks = append(out[n-1].Blocks, m.Blocks...)
			continue
		}
		out = append(out, Message{Role: m.Role, Blocks: append([]Block(nil), m.Blocks...)})
	}
	return out
}

// replayable says whether b goes to wire as its Raw: it was given by that wire, and is JSON.
func replayable(b Block, wire string) bool {
	return len(b.Raw) > 0 && b.Wire == wire && json.Valid(b.Raw)
}

// ---- the answer

// answer is the blocks of an answer as they stream, in the order they started: a block's index
// is its position here, and so in Response.Blocks.
type answer struct {
	emit  func(Event)
	parts []*part
}

// part is one block of the answer: its start, and the text, thinking or tool call JSON so far.
type part struct {
	Block
	buf  strings.Builder
	sig  string          // Anthropic: the thinking's signature
	raw  json.RawMessage // the block as the provider gave it, when it is kept as given
	init json.RawMessage // Anthropic: a tool call's input as its start gave it
}

// start begins a block and tells the caller.
func (a *answer) start(b Block) int {
	a.parts = append(a.parts, &part{Block: b})
	i := len(a.parts) - 1
	a.emit(Event{Kind: EvBlockStart, Index: i, BlockType: b.Type, ToolID: b.ID, ToolName: b.Name})
	return i
}

// add is more of block i.
func (a *answer) add(i int, s string) {
	if s == "" {
		return
	}
	a.parts[i].buf.WriteString(s)
	a.emit(Event{Kind: EvDelta, Index: i, Text: s})
}
