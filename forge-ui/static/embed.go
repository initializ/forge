package static

import "embed"

// FS exposes the agent dashboard's bundled static assets (index.html,
// app.js, style.css, and the Monaco editor under monaco/) as an
// embed.FS so they ship inside the forge binary.
//
// Files are listed explicitly here rather than embedding the whole
// directory so that this file itself (embed.go) is NOT served as an
// asset. If a new top-level static file is added, it needs to be
// added to this directive.
//
// Note: there is no build step — app.js and style.css are hand-edited
// in place, NOT generated from sources elsewhere. The directory used
// to be called "dist/" but that naming was misleading (review #8); it
// signaled "build artifact" when in fact the directory is the source.
//
//go:embed app.js style.css index.html monaco
var FS embed.FS
