// Package web provides embedded static files and templates
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:templates
var templatesFS embed.FS

//go:embed all:static
var staticFS embed.FS

// GetTemplatesFS returns the embedded templates filesystem
func GetTemplatesFS() (fs.FS, error) {
	return fs.Sub(templatesFS, "templates")
}

// GetStaticFS returns the embedded static assets filesystem.
func GetStaticFS() (fs.FS, error) {
	return fs.Sub(staticFS, "static")
}
