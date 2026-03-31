package main

import "embed"

// staticFiles holds the embedded WebClient directory baked in at build time.
// build.bat copies ../WebClient into static/ before running go build.
//
//go:embed static
var staticFiles embed.FS
