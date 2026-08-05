package markdown

import (
	"regexp"
	"strings"
)

// ctxzipMarkerRe matches an internal context-compression pointer
// ("<<ctxzip:HASH note>>") that ctxzip leaves in place of offloaded content.
// Mirrors github.com/initializ/ctxzip/ccr's marker format. Kept in this shared
// package so every channel plugin strips markers identically (the format is
// also pinned as CompressionMarkerPrefix in forge-core/runtime, which cannot
// import this package for import-cycle reasons).
var ctxzipMarkerRe = regexp.MustCompile(`<<ctxzip:[0-9a-f]{12,64}(?:[ ,][^>]*)?>>`)

// StripCompressionMarkers removes any ctxzip markers that leaked into
// channel-bound text or file content. These are internal artifacts of context
// compression; a user must never see them. The model is expected to expand
// offloaded content it needs before answering, so a marker reaching a channel
// is a leak — drop it rather than surface a dangling pointer.
func StripCompressionMarkers(s string) string {
	if !strings.Contains(s, "<<ctxzip:") {
		return s
	}
	return strings.TrimSpace(ctxzipMarkerRe.ReplaceAllString(s, ""))
}
