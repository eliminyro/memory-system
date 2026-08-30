package models

import (
	"strings"
	"testing"
)

// FuzzParsePath drives ParsePath with arbitrary path strings, asserting it never
// panics and never emits a slug containing a path separator (the M1 invariant:
// unmappable/deep paths return empty rather than a slash-bearing slug that the
// write-path validator would later reject). Run: go test -fuzz=FuzzParsePath.
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
		// No panic is the primary property. Invariant: a returned slug must not
		// contain "/" — every accepted shape (1/2/3-segment) yields a leaf slug,
		// and the 4+-segment case returns empty. A slash here would mean a
		// mangled path slipped past the write-path contract (M1 regression).
		if strings.Contains(slug, "/") {
			t.Fatalf("ParsePath(%q) produced a slug with a separator: cat=%q sub=%v slug=%q", path, cat, sub, slug)
		}
		if sub != nil && strings.Contains(*sub, "/") {
			t.Fatalf("ParsePath(%q) produced a subcategory with a separator: %q", path, *sub)
		}
	})
}
