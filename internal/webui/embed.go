// Package webui embeds the built React frontend (web/ → internal/webui/dist). The bundle is fixed
// when the hub is compiled: under `make dev`, the hub's own port shows a web change only after the
// bundle and the binary are built again. Vite's port shows it at once.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the SPA files rooted at dist/.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
