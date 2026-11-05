package marker

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const tok = "tok_abc"

func feedAll(s *Stripper, chunks [][]byte, now time.Time) ([]byte, []Marker) {
	var out []byte
	var ms []Marker
	for _, c := range chunks {
		o, m := s.Feed(c, now)
		out = append(out, o...)
		ms = append(ms, m...)
	}
	return out, ms
}

func TestStripSplitAtEveryOffset(t *testing.T) {
	rc := 3
	mk := Encode(TypeAgentExit, Payload{K: tok, Rc: &rc})
	st := append(Encode(TypeHook, Payload{K: tok, Ev: "Stop"}), []byte{}...)
	st = append(st[:len(st)-1], 0x1b, '\\') // ST terminator variant
	stream := []byte("before \x1b[31mred\x1b[0m ")
	stream = append(stream, mk...)
	stream = append(stream, []byte(" mid \x1b]0;title\x07 ")...)
	stream = append(stream, st...)
	stream = append(stream, []byte(" after\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\")...)
	want := "before \x1b[31mred\x1b[0m  mid \x1b]0;title\x07  after\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\"
	now := time.Now()
	for cut := 0; cut <= len(stream); cut++ {
		for cut2 := cut; cut2 <= len(stream); cut2 += 7 {
			s := &Stripper{Token: tok}
			out, ms := feedAll(s, [][]byte{stream[:cut], stream[cut:cut2], stream[cut2:]}, now)
			if string(out) != want {
				t.Fatalf("cut %d/%d: out %q", cut, cut2, out)
			}
			if len(ms) != 2 || ms[0].Type != TypeAgentExit || *ms[0].Payload.Rc != 3 || ms[1].Payload.Ev != "Stop" {
				t.Fatalf("cut %d/%d: markers %+v", cut, cut2, ms)
			}
			if s.Pending() {
				t.Fatalf("cut %d: still holding", cut)
			}
		}
	}
	// Byte by byte.
	s := &Stripper{Token: tok}
	var chunks [][]byte
	for i := range stream {
		chunks = append(chunks, stream[i:i+1])
	}
	if out, ms := feedAll(s, chunks, now); string(out) != want || len(ms) != 2 {
		t.Fatalf("bytewise: %q %d", out, len(ms))
	}
}

func TestForgedTokenStrippedAndIgnored(t *testing.T) {
	s := &Stripper{Token: tok}
	in := append([]byte("x"), Encode(TypeHook, Payload{K: "wrong", Ev: "Stop"})...)
	in = append(in, Encode(TypeHook, Payload{Ev: "Stop"})...)      // no token
	in = append(in, []byte("\x1b]777;nb;hook;!!notbase64\x07")...) // bad format
	in = append(in, 'y')
	out, ms := s.Feed(in, time.Now())
	if string(out) != "xy" || len(ms) != 0 || s.Dropped != 3 {
		t.Fatalf("out %q ms %v dropped %d", out, ms, s.Dropped)
	}
	// Empty session token never matches.
	s2 := &Stripper{}
	if _, ms := s2.Feed(Encode(TypeHook, Payload{K: ""}), time.Now()); len(ms) != 0 {
		t.Fatal("empty token accepted")
	}
}

func TestUnterminatedReleased(t *testing.T) {
	now := time.Now()
	// No terminator within 16 KiB: released as is.
	s := &Stripper{Token: tok}
	big := append([]byte("\x1b]777;nb;hook;"), bytes.Repeat([]byte("A"), MaxLen+10)...)
	out, ms := s.Feed(big, now)
	if !bytes.Equal(out, big) || len(ms) != 0 || s.Pending() {
		t.Fatalf("16 KiB: released %d of %d, pending %v", len(out), len(big), s.Pending())
	}
	// A partial prefix waits, then is released after 2 s.
	s = &Stripper{Token: tok}
	out, _ = s.Feed([]byte("ab\x1b]77"), now)
	if string(out) != "ab" || !s.Pending() {
		t.Fatalf("partial: %q", out)
	}
	if f := s.Flush(now.Add(time.Second)); f != nil {
		t.Fatal("released too early")
	}
	if f := s.Flush(now.Add(HoldMax)); string(f) != "\x1b]77" || s.Pending() {
		t.Fatalf("flush %q", f)
	}
	// An unterminated body is also released after 2 s.
	s = &Stripper{Token: tok}
	s.Feed([]byte("\x1b]777;nb;hook;abc"), now)
	if f := s.Flush(now.Add(3 * time.Second)); string(f) != "\x1b]777;nb;hook;abc" {
		t.Fatalf("body flush %q", f)
	}
}

func TestOtherEscapesPassUntouched(t *testing.T) {
	s := &Stripper{Token: tok}
	in := []byte("\x1b]777;notify;Title;Body\x07\x1b]7770;x\x07\x1b]77;nbx\x07\x1b[?2004h\x1bPtmux;\x1b\x1b]0;t\x07\x1b\\")
	if out, ms := s.Feed(in, time.Now()); !bytes.Equal(out, in) || len(ms) != 0 {
		t.Fatalf("out %q", out)
	}
	// ESC inside a marker aborts it; the new sequence is kept.
	s = &Stripper{Token: tok}
	out, ms := s.Feed([]byte("\x1b]777;nb;hook;abc\x1b[1mB"), time.Now())
	if string(out) != "\x1b[1mB" || len(ms) != 0 {
		t.Fatalf("abort %q", out)
	}
}

func TestRateLimit(t *testing.T) {
	s := &Stripper{Token: tok}
	now := time.Now()
	var in []byte
	for i := 0; i < RateMax+20; i++ {
		in = append(in, Encode(TypeHook, Payload{K: tok, Ev: "PreToolUse"})...)
	}
	out, ms := s.Feed(in, now)
	if len(out) != 0 || len(ms) != RateMax || s.Dropped != 20 {
		t.Fatalf("out %d ms %d dropped %d", len(out), len(ms), s.Dropped)
	}
	if _, ms := s.Feed(Encode(TypeHook, Payload{K: tok}), now.Add(1100*time.Millisecond)); len(ms) != 1 {
		t.Fatal("window did not reset")
	}
}

func TestParseAlphabets(t *testing.T) {
	js, _ := json.Marshal(Payload{K: tok, Ev: "Notification", Raw: base64.StdEncoding.EncodeToString([]byte(`{"a":1}`))})
	for _, enc := range []string{
		base64.RawURLEncoding.EncodeToString(js), base64.URLEncoding.EncodeToString(js),
		base64.StdEncoding.EncodeToString(js), base64.RawStdEncoding.EncodeToString(js),
	} {
		m, err := Parse([]byte("hook;" + enc))
		if err != nil || m.Payload.Ev != "Notification" || string(m.RawBytes()) != `{"a":1}` {
			t.Fatalf("%s: %+v %v", enc, m, err)
		}
	}
	if _, err := Parse([]byte("nosemicolon")); err == nil {
		t.Fatal("accepted body without type")
	}
}

func TestScanString(t *testing.T) {
	raw := []byte(`{"session_id":"0b5e-1","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"echo \"hi\"\nls","description":"x"},"message":"Claude needs your permission to use Bash","cut":"abc`)
	cases := map[string]string{
		"session_id": "0b5e-1", "tool_name": "Bash", "command": "echo \"hi\"\nls",
		"message": "Claude needs your permission to use Bash", "cut": "abc", "missing": "",
	}
	for k, want := range cases {
		if got := ScanString(raw, k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	// Key text appearing as a value is skipped.
	if got := ScanString([]byte(`{"a":"type","type":"agent-turn-complete"}`), "type"); got != "agent-turn-complete" {
		t.Fatalf("got %q", got)
	}
	if got := ScanString([]byte(`{"m":"café ✓"}`), "m"); !strings.HasPrefix(got, "café") {
		t.Fatalf("unicode %q", got)
	}
}
