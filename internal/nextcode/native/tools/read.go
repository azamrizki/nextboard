package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	readLimit   = 2000      // lines one Read shows when the call gives no limit
	maxReadSize = 100 << 10 // about the most of a file one Read shows, in bytes of output
	sniffSize   = 8 << 10   // what is looked at to tell a binary file or an image
)

const readDescription = `Reads a file from the local file system and shows it numbered like cat -n: each line's number, a tab, then the line.

- file_path is an absolute path (a relative one is taken from the working folder).
- By default it reads up to 2000 lines from the start. For a long file, read the part you need with offset (the 1-based line to start at) and limit (how many lines).
- Lines longer than 2000 characters are cut, and one call shows at most about 100 KB. A note at the end says how many lines remain and the offset to read on from.
- It reads files only: for a folder use Glob, or Bash ls. Binary files and images are not shown, only their size.
- Read a file before you Edit it. When you need several files, read them in parallel calls.`

const readSchema = `{
  "type": "object",
  "properties": {
    "file_path": {"type": "string", "description": "The absolute path of the file to read"},
    "offset": {"type": "integer", "minimum": 1, "description": "The line to start reading at (1-based). Only for a file too long to read at once"},
    "limit": {"type": "integer", "minimum": 1, "description": "How many lines to read (2000 by default). Only for a file too long to read at once"}
  },
  "required": ["file_path"],
  "additionalProperties": false
}`

func readTool() Tool { return newTool("Read", KindRead, readDescription, readSchema, runRead) }

type readInput struct {
	FilePath string  `json:"file_path"`
	Offset   *number `json:"offset"`
	Limit    *number `json:"limit"`
}

func runRead(ctx context.Context, env *Env, input json.RawMessage) Result {
	var in readInput
	if err := decode("Read", input, &in); err != nil {
		return fail("%v", err)
	}
	path, err := pathArg(env, "Read", "file_path: the file to read", in.FilePath)
	if err != nil {
		return fail("%v", err)
	}
	from, limit := 1, readLimit
	if in.Offset != nil && *in.Offset > 1 {
		from = int(*in.Offset)
	}
	if in.Limit != nil && *in.Limit > 0 {
		limit = int(*in.Limit)
	}
	st, err := os.Stat(path)
	if err != nil {
		return fail("%s", fileError(env, path, err))
	}
	if st.IsDir() {
		return fail("%s is a directory; use Glob, or Bash ls", path)
	}
	if irregular(st.Mode()) {
		return fail("%s is not a regular file (a device, a pipe or a socket)", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return fail("%s", fileError(env, path, err))
	}
	defer f.Close()
	head := make([]byte, sniffSize)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return fail("Could not read %s: %v", path, err)
	}
	head = head[:n]
	if kind := imageKind(path, head); kind != "" {
		return ok(fmt.Sprintf("image file (%s, %d bytes): images are not supported yet", kind, st.Size()))
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return ok(fmt.Sprintf("binary file, %d bytes, not shown", st.Size()))
	}
	if n == 0 {
		return ok("(the file is empty)")
	}
	// the head read so far, then the rest of the file
	out, err := readLines(ctx, io.MultiReader(bytes.NewReader(head), f), from, limit)
	if ctx.Err() != nil {
		return stopped()
	}
	if err != nil {
		return fail("Could not read %s: %v", path, err)
	}
	return ok(out)
}

// readLines shows the lines of r from line `from` (1-based), at most limit of them and about
// maxReadSize of output, numbered like cat -n. The lines after them are only counted, for the
// note that says where to read on.
func readLines(ctx context.Context, r io.Reader, from, limit int) (string, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	var b strings.Builder
	no, shown, more := 0, 0, 0
	for {
		if no%1024 == 0 && ctx.Err() != nil {
			return "", ctx.Err()
		}
		keep := lineKeep
		if no+1 < from {
			keep = 0 // a line before the offset is only skipped
		}
		line, ok, err := nextLine(br, keep)
		if err != nil {
			return "", err
		}
		if !ok {
			break
		}
		no++
		if no < from {
			continue
		}
		if shown == limit || b.Len() >= maxReadSize {
			more = 1 + countLines(ctx, br)
			break
		}
		numbered(&b, no, string(line))
		shown++
	}
	if shown == 0 {
		return fmt.Sprintf("(the file has %s; offset %d is past its end)", plural(no, "line", "lines"), from), nil
	}
	out := strings.TrimSuffix(b.String(), "\n")
	if more > 0 {
		out += fmt.Sprintf("\n… %s; read on with offset %d", plural(more, "more line", "more lines"), from+shown)
	}
	return out, nil
}

// imageKind is the kind of an image file, by its name or its first bytes; "" for another file.
func imageKind(path string, head []byte) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "png"
	case ".jpg", ".jpeg":
		return "jpeg"
	case ".gif":
		return "gif"
	case ".webp":
		return "webp"
	}
	switch {
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}):
		return "jpeg"
	case bytes.HasPrefix(head, []byte("GIF87a")), bytes.HasPrefix(head, []byte("GIF89a")):
		return "gif"
	case len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return "webp"
	}
	return ""
}
