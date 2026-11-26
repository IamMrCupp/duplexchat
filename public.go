package main

import (
	"embed"
	"io/fs"
)

// The pages, carried over from multichat at 29a9d2c and built into the binary.
//
//go:embed public
var publicFiles embed.FS

// pages returns public/ with the directory prefix stripped, so the server
// asks for "panel.html", not "public/panel.html".
func pages() fs.FS {
	sub, err := fs.Sub(publicFiles, "public")
	if err != nil {
		panic(err) // only if the embed directive and this name disagree
	}
	return sub
}
