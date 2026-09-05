package service_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/service"
)

func p[T any](v T) *T { return &v }

func TestValidateListOptions_Defaults(t *testing.T) {
	o, err := service.ValidateListOptions(nil, nil, nil, nil, nil, nil)
	require.NoError(t, err)
	require.Equal(t, service.ListOptions{Order: "asc"}, o)
}

func TestValidateListOptions_EscapesSlugPrefix(t *testing.T) {
	o, err := service.ValidateListOptions(p(`100%_x`), nil, nil, nil, nil, nil)
	require.NoError(t, err)
	require.Equal(t, `100\%\_x`, o.SlugPrefix)

	o, err = service.ValidateListOptions(p(`a\b`), nil, nil, nil, nil, nil)
	require.NoError(t, err)
	require.Equal(t, `a\\b`, o.SlugPrefix)
}

func TestValidateListOptions_SubcategoryPrefix(t *testing.T) {
	// A valid multi-segment prefix with no LIKE metacharacters passes through.
	o, err := service.ValidateListOptions(nil, p("a11s/platform"), nil, nil, nil, nil)
	require.NoError(t, err)
	require.Equal(t, "a11s/platform", o.SubcategoryPrefix)

	// An underscore is a legal segment char but a LIKE metacharacter, so it is escaped.
	o, err = service.ValidateListOptions(nil, p("a11s_x"), nil, nil, nil, nil)
	require.NoError(t, err)
	require.Equal(t, `a11s\_x`, o.SubcategoryPrefix)

	// A malformed prefix (empty segment) is rejected.
	_, err = service.ValidateListOptions(nil, p("a11s//x"), nil, nil, nil, nil)
	require.Error(t, err)
}

func TestValidateListOptions_AllowedOrderBy(t *testing.T) {
	for _, f := range []string{"slug", "created_at", "updated_at", "title"} {
		o, err := service.ValidateListOptions(nil, nil, p(f), p("desc"), nil, nil)
		require.NoError(t, err, f)
		require.Equal(t, f, o.OrderBy)
		require.Equal(t, "desc", o.Order)
	}
}

func TestValidateListOptions_Rejections(t *testing.T) {
	cases := []struct {
		name                 string
		slug, orderBy, order *string
		limit, offset        *int
	}{
		{name: "order_by outside allowlist", orderBy: p("name")},
		{name: "order_by carrying SQL", orderBy: p("slug; DROP TABLE documents")},
		{name: "order not asc/desc", order: p("up")},
		{name: "negative limit", limit: p(-1)},
		{name: "negative offset", offset: p(-3)},
		{name: "offset without limit", offset: p(5)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o, err := service.ValidateListOptions(c.slug, nil, c.orderBy, c.order, c.limit, c.offset)
			require.Error(t, err)
			require.Equal(t, service.ListOptions{}, o, "no partial options escape a rejected call")
		})
	}
}

func TestValidateListOptions_OffsetWithLimitOK(t *testing.T) {
	o, err := service.ValidateListOptions(nil, nil, nil, nil, p(10), p(5))
	require.NoError(t, err)
	require.Equal(t, 10, o.Limit)
	require.Equal(t, 5, o.Offset)
}
