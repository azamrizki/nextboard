package llm

import (
	"bufio"
	"bytes"
	"io"
)

// sseEvent is one event of a stream of server-sent events: its name ("" = none) and its data,
// the data lines joined by "\n".
type sseEvent struct {
	name string
	data []byte
}

// sseReader reads the events of a stream of server-sent events, as every wire streams its
// answer. Comments, ids and retry fields are left out; an event with no data is skipped.
type sseReader struct{ r *bufio.Reader }

func newSSE(r io.Reader) *sseReader { return &sseReader{r: bufio.NewReaderSize(r, 64<<10)} }

// next is the next event; io.EOF, or the read's error, once there is none. An event the
// stream ended in without its blank line still counts.
func (s *sseReader) next() (sseEvent, error) {
	var ev sseEvent
	var data []byte
	has := false
	for {
		line, err := s.r.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimRight(line, "\r\n")
			switch {
			case len(line) == 0:
				if has {
					ev.data = data
					return ev, nil
				}
				ev = sseEvent{}
			case line[0] == ':': // a comment, as a keep-alive
			default:
				field, value, _ := bytes.Cut(line, []byte(":"))
				value = bytes.TrimPrefix(value, []byte(" "))
				switch string(field) {
				case "event":
					ev.name = string(value)
				case "data":
					if has {
						data = append(data, '\n')
					}
					data = append(data, value...)
					has = true
				}
			}
		}
		if err != nil {
			if has {
				ev.data = data
				return ev, nil
			}
			return sseEvent{}, err
		}
	}
}
