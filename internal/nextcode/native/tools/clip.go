package tools

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	maxLineChars = 2000 // a longer line is cut, in Read, Edit and Grep
	// lineKeep is the most of one line that is kept while reading it: enough bytes for
	// maxLineChars characters and one more, so that a cut line is always seen as cut.
	lineKeep = (maxLineChars + 1) * utf8.UTFMax
)

// cutLine cuts a line to max characters, marking the cut with "…".
func cutLine(s string, max int) string {
	if len(s) <= max {
		return s
	}
	n := 0
	for i := range s {
		if n == max {
			return s[:i] + "…"
		}
		n++
	}
	return s
}

// cutText cuts a text to max characters. cut is how many characters were left out.
func cutText(s string, max int) (out string, cut int) {
	if len(s) <= max {
		return s, 0
	}
	n := 0
	for i := range s {
		if n == max {
			return s[:i], utf8.RuneCountInString(s[i:])
		}
		n++
	}
	return s, 0
}

// numbered writes a line as cat -n does: its number right-aligned, a tab, then the line.
func numbered(b *strings.Builder, n int, line string) {
	fmt.Fprintf(b, "%6d\t%s\n", n, cutLine(line, maxLineChars))
}

// lineCount is the number of lines of a text; a last line without its newline counts too.
func lineCount(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// nextLine reads one line without its end (\n or \r\n), keeping at most keep bytes of it: a
// line of a minified file can be megabytes long, and only its start is ever shown. ok is
// false at the end of the input.
func nextLine(br *bufio.Reader, keep int) (line []byte, ok bool, err error) {
	var buf []byte
	read := false
	for {
		chunk, err := br.ReadSlice('\n')
		if len(chunk) > 0 {
			read = true
			if room := keep - len(buf); room > 0 {
				buf = append(buf, chunk[:min(room, len(chunk))]...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil && err != io.EOF {
			return nil, false, err
		}
		if !read {
			return nil, false, nil
		}
		buf = bytes.TrimSuffix(buf, []byte("\n"))
		buf = bytes.TrimSuffix(buf, []byte("\r"))
		return buf, true, nil
	}
}

// countLines counts the lines left in r; a last line without its newline counts too.
func countLines(ctx context.Context, r io.Reader) int {
	buf := make([]byte, 64<<10)
	n, last := 0, byte('\n')
	for ctx.Err() == nil {
		k, err := r.Read(buf)
		if k > 0 {
			n += bytes.Count(buf[:k], []byte("\n"))
			last = buf[k-1]
		}
		if err != nil {
			break
		}
	}
	if last != '\n' {
		n++
	}
	return n
}

// clip keeps the start and the end of an output that may be very long, and counts its
// characters. Characters are UTF-8 sequences, counted by their first byte, so a cut never
// splits one and the count is right however the output is chunked.
type clip struct {
	mu           sync.Mutex
	headN, tailN int // characters kept at each end
	head         []byte
	headChars    int
	headDone     bool
	tail         []byte // the last bytes after head: at least tailN characters' worth
	total        int    // characters in all
}

func (c *clip) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	for _, b := range p {
		if b&0xC0 != 0x80 {
			c.total++
		}
	}
	if !c.headDone {
		i := 0
		for ; i < len(p); i++ {
			if p[i]&0xC0 != 0x80 {
				if c.headChars == c.headN {
					c.headDone = true
					break
				}
				c.headChars++
			}
		}
		c.head = append(c.head, p[:i]...)
		p = p[i:]
	}
	c.tail = append(c.tail, p...)
	if keep := c.tailN * utf8.UTFMax; len(c.tail) > 2*keep {
		c.tail = append(c.tail[:0], c.tail[len(c.tail)-keep:]...)
	}
	return n, nil
}

// String is the output, with its middle replaced by a note when it was too long.
func (c *clip) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	rest := c.total - c.headChars
	if rest <= c.tailN {
		return strings.ToValidUTF8(string(c.head)+string(c.tail), "�")
	}
	// the last tailN characters of tail
	i, n := len(c.tail), 0
	for i > 0 && n < c.tailN {
		i--
		if c.tail[i]&0xC0 != 0x80 {
			n++
		}
	}
	note := fmt.Sprintf("\n… %d characters cut …\n", rest-c.tailN)
	return strings.ToValidUTF8(string(c.head)+note+string(c.tail[i:]), "�")
}
