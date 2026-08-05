// Package web embeds the static UI assets (templates and static files) so the
// server can serve them without depending on the process's working directory.
// This makes `gitrieve server` work from any cwd and keeps tests independent
// of the on-disk layout.
package web

import (
	"embed"
	"io/fs"
)

// TemplatesFS holds the HTML templates under web/templates. The "templates/"
// prefix is kept because template.ParseFS matches against full paths.
//
//go:embed templates/*
var TemplatesFS embed.FS

// staticFS holds the raw embedded static assets under web/static.
//
//go:embed static/*
var staticFS embed.FS

// StaticFS is the static asset subtree with the "static/" prefix stripped, so
// gin's StaticFS("/static", ...) (which strips the "/static" URL prefix before
// looking up the file) resolves /static/css/main.css to css/main.css.
var StaticFS fs.FS

func init() {
	var err error
	StaticFS, err = fs.Sub(staticFS, "static")
	if err != nil {
		// fs.Sub only errors if the named directory is missing; since
		// staticFS embeds static/* this can never happen at runtime.
		panic(err)
	}
}
