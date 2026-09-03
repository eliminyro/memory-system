package service

import (
	"fmt"
	"strings"

	"github.com/eliminyro/memory-system/internal/repository"
)

// ListOptions carries the validated browse parameters shared by the MCP
// list_documents tool and the REST browse endpoint. SlugPrefix is LIKE-escaped;
// an empty OrderBy keeps the legacy composite order.
type ListOptions struct {
	SlugPrefix string
	OrderBy    string
	Order      string
	Limit      int
	Offset     int
}

// ValidateListOptions validates the browse parameters both surfaces accept and
// returns the repository arguments, so escaping and the order_by allowlist can't
// diverge between them. A nil pointer means the caller omitted that parameter.
func ValidateListOptions(slugPrefix, orderBy, order *string, limit, offset *int) (ListOptions, error) {
	var o ListOptions
	if slugPrefix != nil {
		o.SlugPrefix = escapeLikePrefix(*slugPrefix)
	}
	if orderBy != nil && *orderBy != "" {
		if _, ok := repository.ListOrderColumns[*orderBy]; !ok {
			return ListOptions{}, fmt.Errorf("invalid order_by %q: must be one of slug, created_at, updated_at, title", *orderBy)
		}
		o.OrderBy = *orderBy
	}
	o.Order = "asc"
	if order != nil && *order != "" {
		if *order != "asc" && *order != "desc" {
			return ListOptions{}, fmt.Errorf("invalid order %q: must be asc or desc", *order)
		}
		o.Order = *order
	}
	if limit != nil {
		if *limit < 0 {
			return ListOptions{}, fmt.Errorf("limit must be >= 0")
		}
		o.Limit = *limit
	}
	if offset != nil {
		if *offset < 0 {
			return ListOptions{}, fmt.Errorf("offset must be >= 0")
		}
		o.Offset = *offset
	}
	// An offset into an unbounded result set is meaningless (design D4).
	if o.Offset > 0 && o.Limit == 0 {
		return ListOptions{}, fmt.Errorf("offset requires limit")
	}
	return o, nil
}

// escapeLikePrefix escapes LIKE metacharacters so a caller's slug_prefix cannot
// act as a wildcard; the backslash goes first so it does not double-escape.
func escapeLikePrefix(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	s = strings.ReplaceAll(s, "_", `\_`)
	return s
}
