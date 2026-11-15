package art

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/machine"
)

// TestRealCodex makes one cover and one icon with the real Codex CLI of this machine and its own
// login (CODEX_HOME or ~/.codex), through Make, for a made-up project in a temp folder. It uses
// the owner's Codex account and makes two images, so it never runs in the normal suite: set
// NEXTBOARD_REAL_CODEX=1. NEXTBOARD_REAL_CODEX_OUT names a folder for the results.
func TestRealCodex(t *testing.T) {
	if os.Getenv("NEXTBOARD_REAL_CODEX") != "1" {
		t.Skip("set NEXTBOARD_REAL_CODEX=1 to make two images with the real Codex")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	out := os.Getenv("NEXTBOARD_REAL_CODEX_OUT")
	if out == "" {
		out = t.TempDir()
	}
	folder := t.TempDir()
	files := map[string]string{
		"README.md": "# Tidepool\n\nTidepool is a made-up sample app. It tracks the tide times of a small seaside harbour " +
			"and sends the fishing boats a short summary every morning: high and low water, wind and swell.\n\n" +
			"## Parts\n\n- a small Go service that reads tide tables\n- a web page with today's chart\n",
		"package.json": `{"name":"tidepool-web","description":"Tide times and morning summaries for a small harbour","private":true}`,
		"go.mod":       "module example.com/tidepool\n\ngo 1.26\n",
	}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(folder, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	host := machine.NewLocal(machine.Config{})
	c := Codex{Host: host, Command: "codex", Home: home, Detected: host.Has("codex")}
	ctx := context.Background()
	if capab := c.Check(ctx); !capab.Available {
		t.Fatalf("no image tool: %s", capab.Reason)
	}
	src := Source{Name: "Tidepool", Parts: []string{"Service", "Web"}, Host: host, Folder: folder,
		Memory: "Made-up sample project for a test. The harbour master reads the summary at 6 am."}
	for _, kind := range []string{KindCover, KindIcon} {
		start := time.Now()
		im, err := Make(ctx, c, kind, src, func(phase, text string) { t.Logf("%s: %s %s", kind, phase, text) })
		took := time.Since(start).Round(time.Second)
		if err != nil {
			t.Fatalf("%s after %s: %v (%s)", kind, took, err, Code(err))
		}
		ext := ".png"
		if im.Type == "image/jpeg" {
			ext = ".jpg"
		}
		p := filepath.Join(out, "real-"+kind+ext)
		if err := os.WriteFile(p, im.Data, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %dx%d %s, %d bytes, in %s → %s", kind, im.Width, im.Height, im.Type, len(im.Data), took, p)
	}
}
