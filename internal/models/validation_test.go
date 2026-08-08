package models

import "testing"

func strptr(s string) *string { return &s }

func TestValidateDocumentPath(t *testing.T) {
	longCat := "" // 51 chars > MaxCategoryLen(50)
	for i := 0; i < 51; i++ {
		longCat += "a"
	}
	okLongSlug := "" // 100 chars == MaxSlugLen, allowed
	for i := 0; i < 100; i++ {
		okLongSlug += "a"
	}

	cases := []struct {
		name        string
		category    string
		slug        string
		subcategory *string
		wantErr     bool
	}{
		{"valid no subcategory", "learnings", "gorm", nil, false},
		{"valid with subcategory", "learnings", "gorm", strptr("go"), false},
		{"valid dots dashes underscores", "a.b-c_d", "x.y-z_1", strptr("s.u-b_1"), false},
		{"slug at max length", "learnings", okLongSlug, nil, false},
		{"category over 50", longCat, "gorm", nil, true},
		{"category with space", "a b", "gorm", nil, true},
		{"category with slash", "a/b", "gorm", nil, true},
		{"slug with space", "learnings", "has spaces", nil, true},
		{"slug leading dot", "learnings", ".hidden", nil, true},
		{"empty category", "", "gorm", nil, true},
		{"empty slug", "learnings", "", nil, true},
		{"subcategory with space", "learnings", "gorm", strptr("has spaces"), true},
		{"empty non-nil subcategory", "learnings", "gorm", strptr(""), true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateDocumentPath(c.category, c.slug, c.subcategory)
			if c.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("expected nil, got %v", err)
			}
		})
	}
}
