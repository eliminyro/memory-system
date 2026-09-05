package models

import (
	"strings"
	"testing"
)

// FuzzParsePath drives ParsePath with arbitrary paths, asserting it never panics
// and that category and slug are always single segments (no "/"). A deep path
// yields a multi-segment subcategory, validated downstream. Run: go test -fuzz=FuzzParsePath.
func FuzzParsePath(f *testing.F) {
	for _, s := range []string{
		"", "/", "///", "foo", "a/b", "learnings/go/gorm",
		"a/b/c/d", "learnings//gorm", "  ", "a/ /c",
		"a/b/c/d/e/f", strings.Repeat("a/", 5000), strings.Repeat("/", 1000),
		"..\\..\\etc", "a/../b", "\x00/\x00",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, path string) {
		cat, sub, slug := ParsePath(path)
		// No panic is primary. Category and slug are always single segments (first
		// and last), so neither contains "/". The subcategory MAY be multi-segment;
		// a malformed one is caught later by ValidateDocumentPath, not here.
		if strings.Contains(cat, "/") {
			t.Fatalf("ParsePath(%q) produced a category with a separator: %q", path, cat)
		}
		if strings.Contains(slug, "/") {
			t.Fatalf("ParsePath(%q) produced a slug with a separator: cat=%q sub=%v slug=%q", path, cat, sub, slug)
		}
	})
}
