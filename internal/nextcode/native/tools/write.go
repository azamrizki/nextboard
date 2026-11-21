package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const writeDescription = `Writes a file, replacing it if it exists, and creates its missing parent folders.

- file_path is an absolute path (a relative one is taken from the working folder); content is the whole new text of the file.
- To change part of an existing file, prefer Edit: it sends only the change. Use Write for a new file or a complete rewrite, and Read an existing file first so that nothing in it is lost.
- An existing file keeps its permissions; a new one gets 0644.`

const writeSchema = `{
  "type": "object",
  "properties": {
    "file_path": {"type": "string", "description": "The absolute path of the file to write"},
    "content": {"type": "string", "description": "The whole content of the file"}
  },
  "required": ["file_path", "content"],
  "additionalProperties": false
}`

func writeTool() Tool { return newTool("Write", KindEdit, writeDescription, writeSchema, runWrite) }

type writeInput struct {
	FilePath string  `json:"file_path"`
	Content  *string `json:"content"`
}

func runWrite(ctx context.Context, env *Env, input json.RawMessage) Result {
	var in writeInput
	if err := decode("Write", input, &in); err != nil {
		return fail("%v", err)
	}
	path, err := pathArg(env, "Write", "file_path: the file to write", in.FilePath)
	if err != nil {
		return fail("%v", err)
	}
	if in.Content == nil {
		return fail(`Write needs content: the whole text of the file ("" for an empty file)`)
	}
	mode, fresh := os.FileMode(0o644), true
	st, err := os.Stat(path)
	switch {
	case err == nil && st.IsDir():
		return fail("%s is a directory", path)
	case err == nil && irregular(st.Mode()):
		return fail("%s is not a regular file (a device, a pipe or a socket)", path)
	case err == nil:
		mode, fresh = st.Mode().Perm(), false
	case !errors.Is(err, os.ErrNotExist):
		return fail("%s", fileError(env, path, err))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fail("Could not create the folder of %s: %v", path, err)
	}
	if ctx.Err() != nil {
		return stopped()
	}
	if err := writeFile(path, []byte(*in.Content), mode, fresh); err != nil {
		return fail("Could not write %s: %v", path, err)
	}
	verb := "Updated"
	if fresh {
		verb = "Created"
	}
	return ok(fmt.Sprintf("%s %s (%s)", verb, path, plural(lineCount(*in.Content), "line", "lines")))
}

// writeFile writes data over a file in place, so that an existing file keeps its inode, owner,
// links and mode; a fresh one gets mode exactly, whatever the umask.
func writeFile(path string, data []byte, mode os.FileMode, fresh bool) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil && fresh {
		err = f.Chmod(mode)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
