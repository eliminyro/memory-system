package models

import (
	"fmt"
	"regexp"
)

// Path-segment length caps, aligned with the documents table column sizes
// (Document: category size:50, subcategory/slug size:100). Keeping the
// validation limits equal to the column widths means an over-long segment is
// rejected as invalid input (400/errorResult) instead of surfacing as a
// Postgres "value too long" error (500) at write time.
const (
	MaxCategoryLen    = 50
	MaxSubcategoryLen = 100
	MaxSlugLen        = 100
)

// validPathSegment matches a single path component: it must start with an
// alphanumeric and thereafter allow only [A-Za-z0-9._-]. Shared by the MCP and
// HTTP write surfaces so both enforce one identical path contract.
var validPathSegment = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// ValidateDocumentPath validates a document's (category, subcategory, slug)
// against the shared length + character contract. subcategory nil is allowed
// (no subcategory); a non-nil subcategory must itself be valid. Returns a
// descriptive error the caller wraps (service → ErrInvalidInput, MCP →
// errorResult) — this package stays free of the errors/response packages.
func ValidateDocumentPath(category, slug string, subcategory *string) error {
	if len(category) > MaxCategoryLen {
		return fmt.Errorf("category must be <= %d characters", MaxCategoryLen)
	}
	if !validPathSegment.MatchString(category) {
		return fmt.Errorf("category %q is not a valid path segment", category)
	}
	if len(slug) > MaxSlugLen {
		return fmt.Errorf("slug must be <= %d characters", MaxSlugLen)
	}
	if !validPathSegment.MatchString(slug) {
		return fmt.Errorf("slug %q is not a valid path segment", slug)
	}
	if subcategory != nil {
		if len(*subcategory) > MaxSubcategoryLen {
			return fmt.Errorf("subcategory must be <= %d characters", MaxSubcategoryLen)
		}
		if !validPathSegment.MatchString(*subcategory) {
			return fmt.Errorf("subcategory %q is not a valid path segment", *subcategory)
		}
	}
	return nil
}
