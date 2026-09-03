package models

import "testing"

// TestParsePath pins the category/subcategory/slug contract, including the M1
// fix: 4+-segment paths are unmappable and return empty ("", nil, "") so the
// caller skips them instead of storing a slash-bearing slug.
func TestParsePath(t *testing.T) {
	strptr := func(s string) *string { return &s }

	cases := []struct {
		name    string
		path    string
		wantCat string
		wantSub *string
		wantSlg string
	}{
		{"four-plus segments unmappable -> empty", "learnings/go/frameworks/gorm", "", nil, ""},
		{"single segment -> misc category", "foo", "misc", nil, "foo"},
		{"two segments -> category/slug", "a/b", "a", nil, "b"},
		{"three segments -> category/subcategory/slug", "a/b/c", "a", strptr("b"), "c"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cat, sub, slug := ParsePath(c.path)
			if cat != c.wantCat {
				t.Errorf("category = %q, want %q", cat, c.wantCat)
			}
			if slug != c.wantSlg {
				t.Errorf("slug = %q, want %q", slug, c.wantSlg)
			}
			switch {
			case c.wantSub == nil && sub != nil:
				t.Errorf("subcategory = %q, want nil", *sub)
			case c.wantSub != nil && sub == nil:
				t.Errorf("subcategory = nil, want %q", *c.wantSub)
			case c.wantSub != nil && sub != nil && *sub != *c.wantSub:
				t.Errorf("subcategory = %q, want %q", *sub, *c.wantSub)
			}
		})
	}
}
