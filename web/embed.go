// Package web holds the Lagrange web app. The React sources live here; `npm run build`
// writes the app to dist/, which is embedded in the lagrange binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the built app, or nil if this binary was built without it.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil
	}
	return sub
}
