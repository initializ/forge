// Package util provides shared utility functions.
package util

import (
	"regexp"
	"strings"
)

var (
	nonAlphanumHyphen = regexp.MustCompile(`[^a-z0-9-]`)
	multipleHyphens   = regexp.MustCompile(`-{2,}`)
)

// Slugify converts a human-readable name into a URL/ID-safe slug.
// It lowercases, replaces spaces with hyphens, strips non-[a-z0-9-],
// collapses multiple hyphens, and trims leading/trailing hyphens.
func Slugify(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.ReplaceAll(s, " ", "-")
	s = nonAlphanumHyphen.ReplaceAllString(s, "")
	s = multipleHyphens.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}
