package server

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Download in the Code view: fs/raw with download=1 saves any file under its own name; without
// it a text file is still shown, not saved.
func TestFSRawDownload(t *testing.T) {
	h := newTestHub(t)
	h.setup()
	dir := t.TempDir()
	for name, body := range map[string]string{"orders.ts": "export const x = 1;\n", "laporan akhir é.md": "# hi\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw := func(name, q string) (int, []byte, string) {
		st, b, hd := h.doRaw("GET", "/api/machines/local/fs/raw?path="+url.QueryEscape(filepath.Join(dir, name))+q, "", nil)
		return st, b, hd.Get("Content-Disposition")
	}
	if st, b, cd := raw("orders.ts", ""); st != 200 || cd != "" || string(b) != "export const x = 1;\n" {
		t.Fatalf("plain %d %q %q", st, cd, b)
	}
	if st, b, cd := raw("orders.ts", "&download=1"); st != 200 || cd != `attachment; filename=orders.ts` || string(b) != "export const x = 1;\n" {
		t.Fatalf("download %d %q %q", st, cd, b)
	}
	// a name with spaces and letters outside ASCII keeps them (RFC 2231)
	if st, _, cd := raw("laporan akhir é.md", "&download=1"); st != 200 || !strings.HasPrefix(cd, "attachment; filename*=utf-8''laporan%20akhir%20%C3%A9.md") {
		t.Fatalf("utf-8 name %d %q", st, cd)
	}
}
