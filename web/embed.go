// Package web embeds the browser UI.
package web

import (
	"embed"
	"io/fs"
	"mime"
)

// The manifest makes the UI installable as an app; browsers only accept it
// with its own media type, which Go doesn't know by default.
func init() {
	err := mime.AddExtensionType(".webmanifest", "application/manifest+json")
	if err != nil {
		panic(err)
	}
}

//go:embed static
var files embed.FS

// FS is the UI rooted at static/.
func FS() fs.FS {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		panic(err)
	}
	return sub
}
