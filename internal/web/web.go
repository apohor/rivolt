// Package web embeds the built Vite web app into the Go binary.
//
// Run `make web` (or `npm run build` in web/) to populate web/dist before
// building the Go binary. During development the Vite dev server runs
// separately on :5173 and proxies /api to the Go server.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// Assets returns the embedded web assets rooted at dist/. Returns nil if
// the dist directory wasn't populated at build time.
func Assets() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil
	}
	// Verify dist has content (index.html); otherwise treat as unbuilt.
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil
	}
	return sub
}
