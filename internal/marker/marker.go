// Package marker implements the terminal event channel of agent consoles
// (docs/API-agents.md §4): the wrapper and the CLI hooks print
//
//	ESC ] 777 ; nb ; <type> ; <base64url(JSON)> BEL   (or ESC \ as terminator)
//
// to /dev/tty. The hub strips every such OSC from the output stream before
// scrollback, fanout and the VT model, and turns the valid ones into events.
package marker

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits (docs/API-agents.md §4.2, §15).
const (
	MaxLen  = 16 << 10        // a marker longer than this is released as plain output
	HoldMax = 2 * time.Second // an unterminated prefix is released after this
	RateMax = 50              // markers per second per session; the rest are dropped
	RawMax  = 3072            // bytes of hook stdin carried in "raw"
)

// Types sent by the wrapper and the hooks.
const (
	TypeAgentStart = "agent-start"
	TypeAgentExit  = "agent-exit"
	TypeHook       = "hook"
	TypeNotify     = "notify"
)

var prefix = []byte("\x1b]777;nb;")

// Payload is the JSON carried by a marker. Fields depend on the type.
type Payload struct {
	K    string `json:"k"` // hookToken of the session
	Pid  int    `json:"pid,omitempty"`
	Mode string `json:"mode,omitempty"`
	Sid  string `json:"sid,omitempty"`
	Rc   *int   `json:"rc,omitempty"`
	Ev   string `json:"ev,omitempty"`
	Raw  string `json:"raw,omitempty"` // std base64 of ≤ RawMax bytes (may cut JSON)
}

// Marker is one decoded event.
type Marker struct {
	Type    string
	Payload Payload
}

// RawBytes decodes Payload.Raw (std base64, padding optional).
func (m Marker) RawBytes() []byte {
	b, _ := decodeB64(m.Payload.Raw)
	return b
}

var (
	errFormat = errors.New("invalid marker")
	errToken  = errors.New("wrong marker token")
)

// decodeB64 accepts base64url or standard base64, with or without padding.
func decodeB64(s string) ([]byte, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "=")
	s = strings.NewReplacer("-", "+", "_", "/", "\n", "", "\r", "").Replace(s)
	return base64.RawStdEncoding.DecodeString(s)
}

// Parse decodes "<type>;<payload>" (the body of a marker or of a POST
// /nb/hook request).
func Parse(body []byte) (Marker, error) {
	typ, enc, ok := bytes.Cut(body, []byte{';'})
	if !ok || len(typ) == 0 || len(typ) > 32 {
		return Marker{}, errFormat
	}
	js, err := decodeB64(string(enc))
	if err != nil {
		return Marker{}, errFormat
	}
	var p Payload
	if err := json.Unmarshal(js, &p); err != nil {
		return Marker{}, errFormat
	}
	return Marker{Type: string(typ), Payload: p}, nil
}

// TokenOK compares a marker token with the session's hookToken in
// constant time. An empty session token never matches.
func TokenOK(got, want string) bool {
	return want != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// Encode builds a marker the way the wrapper does (tests, fake hooks).
func Encode(typ string, p Payload) []byte {
	js, _ := json.Marshal(p)
	return []byte("\x1b]777;nb;" + typ + ";" + base64.RawURLEncoding.EncodeToString(js) + "\a")
}

// Stripper removes markers from a terminal byte stream. It is not safe for
// concurrent use; a session feeds it from its single reader goroutine and
// calls Flush from its ticker under the same lock.
type Stripper struct {
	// Token is the session hookToken; markers carrying another token are
	// dropped (still stripped).
	Token string

	hold  []byte // partial prefix, or the whole marker so far
	body  bool   // prefix complete, collecting until the terminator
	esc   bool   // body ended with ESC (maybe the start of ST)
	since time.Time

	winStart time.Time
	winCount int

	// Dropped counts stripped markers that were not delivered (bad token,
	// bad format, rate limit), for debug logs.
	Dropped int
}

// Pending reports whether bytes are held back waiting for more input.
func (s *Stripper) Pending() bool { return len(s.hold) > 0 }

func (s *Stripper) reset() {
	s.hold, s.body, s.esc = s.hold[:0], false, false
}

// release hands held bytes back to the stream unchanged (not a marker).
func (s *Stripper) release(out []byte) []byte {
	out = append(out, s.hold...)
	s.reset()
	return out
}

// Flush releases a held prefix that waited longer than HoldMax.
func (s *Stripper) Flush(now time.Time) []byte {
	if len(s.hold) == 0 || now.Sub(s.since) < HoldMax {
		return nil
	}
	return s.release(nil)
}

// Feed processes one chunk and returns the bytes to pass on plus the
// markers that were complete, well-formed and carried the right token.
func (s *Stripper) Feed(p []byte, now time.Time) ([]byte, []Marker) {
	out := make([]byte, 0, len(p))
	var ms []Marker
	i := 0
	for i < len(p) {
		switch {
		case s.body:
			c := p[i]
			if s.esc {
				s.esc = false
				if c == '\\' {
					i++
					s.hold = s.hold[:len(s.hold)-1] // the ESC of ST
					ms = s.finish(ms, now)
					continue
				}
				// ESC that is not ST aborts the OSC: drop it, reprocess ESC.
				s.Dropped++
				s.reset()
				s.hold = append(s.hold, 0x1b)
				s.since = now
				continue
			}
			j := indexTerm(p[i:])
			if j < 0 {
				s.hold = append(s.hold, p[i:]...)
				i = len(p)
			} else {
				s.hold = append(s.hold, p[i:i+j]...)
				i += j
				switch p[i] {
				case 0x07:
					i++
					ms = s.finish(ms, now)
					continue
				case 0x1b:
					s.hold = append(s.hold, 0x1b)
					s.esc = true
					i++
				default: // CAN / SUB cancel the sequence
					i++
					s.Dropped++
					s.reset()
					continue
				}
			}
			if len(s.hold) > MaxLen {
				out = s.release(out)
			}
		case len(s.hold) > 0:
			if p[i] == prefix[len(s.hold)] {
				s.hold = append(s.hold, p[i])
				i++
				if len(s.hold) == len(prefix) {
					s.body = true
				}
				continue
			}
			out = s.release(out) // not ours; p[i] is examined again
		default:
			j := bytes.IndexByte(p[i:], 0x1b)
			if j < 0 {
				out = append(out, p[i:]...)
				i = len(p)
				continue
			}
			out = append(out, p[i:i+j]...)
			i += j + 1
			s.hold = append(s.hold[:0], 0x1b)
			s.since = now
		}
	}
	return out, ms
}

// indexTerm finds the first BEL, ESC, CAN or SUB.
func indexTerm(b []byte) int {
	for i, c := range b {
		if c == 0x07 || c == 0x1b || c == 0x18 || c == 0x1a {
			return i
		}
	}
	return -1
}

// finish decodes the held marker and resets the state.
func (s *Stripper) finish(ms []Marker, now time.Time) []Marker {
	body := append([]byte(nil), s.hold[len(prefix):]...)
	s.reset()
	if now.Sub(s.winStart) >= time.Second {
		s.winStart, s.winCount = now, 0
	}
	s.winCount++
	if s.winCount > RateMax {
		s.Dropped++
		return ms
	}
	m, err := Parse(body)
	if err != nil || !TokenOK(m.Payload.K, s.Token) {
		s.Dropped++
		return ms
	}
	return append(ms, m)
}

// ScanString finds the first `"key": "value"` in possibly truncated JSON
// and returns the decoded value (a cut-off string returns what is there).
// Hook payloads are cut at RawMax bytes, so a JSON decoder cannot be used.
func ScanString(data []byte, key string) string {
	needle := []byte(`"` + key + `"`)
	for off := 0; ; {
		i := bytes.Index(data[off:], needle)
		if i < 0 {
			return ""
		}
		j := off + i + len(needle)
		off = j
		for j < len(data) && isSpace(data[j]) {
			j++
		}
		if j >= len(data) || data[j] != ':' {
			continue // the key text appeared as a value
		}
		j++
		for j < len(data) && isSpace(data[j]) {
			j++
		}
		if j >= len(data) || data[j] != '"' {
			return ""
		}
		return unquotePartial(data[j+1:])
	}
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// unquotePartial decodes a JSON string body up to its closing quote or the
// end of data.
func unquotePartial(b []byte) string {
	var sb strings.Builder
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case c == '"':
			return sb.String()
		case c == '\\' && i+1 < len(b):
			i++
			switch b[i] {
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			case 'r':
				sb.WriteByte('\r')
			case 'b', 'f':
			case 'u':
				if i+4 < len(b) {
					var r rune
					for _, h := range b[i+1 : i+5] {
						r <<= 4
						switch {
						case h >= '0' && h <= '9':
							r |= rune(h - '0')
						case h >= 'a' && h <= 'f':
							r |= rune(h-'a') + 10
						case h >= 'A' && h <= 'F':
							r |= rune(h-'A') + 10
						}
					}
					if utf8.ValidRune(r) {
						sb.WriteRune(r)
					}
					i += 4
				} else {
					return sb.String()
				}
			default:
				sb.WriteByte(b[i])
			}
		case c == '\\':
			return sb.String()
		default:
			sb.WriteByte(c)
		}
	}
	return sb.String()
}
