package models

import "testing"

func TestDefaultPolicies_EveryDocTypeHasRow(t *testing.T) {
	for dt := range ValidDocTypes {
		if _, ok := DefaultEffectivePolicies[dt]; !ok {
			t.Errorf("no default policy resolved for doc_type %q", dt)
		}
	}
}

func TestResolve_NullInheritsZeroDoesNot(t *testing.T) {
	ps := DefaultEffectivePolicies[DocTypeProjectState]
	if ps.VerificationAgeDays != 14 {
		t.Errorf("project_state verification_age_days = %d, want 14", ps.VerificationAgeDays)
	}
	if ps.ExpirationAgeDays != 0 {
		t.Errorf("project_state expiration_age_days = %d, want 0 (disabled by default)", ps.ExpirationAgeDays)
	}
	if !ps.Embed || !ps.DuplicateGuard {
		t.Error("project_state must inherit reference's embed/duplicate_guard = true")
	}
	if j := DefaultEffectivePolicies[DocTypeJournal]; j.VerificationAgeDays != 0 {
		t.Errorf("journal verification_age_days = %d, want 0 (never), not the inherited 90", j.VerificationAgeDays)
	}
}

func TestResolve_JournalHandoffRules(t *testing.T) {
	j := DefaultEffectivePolicies[DocTypeJournal]
	if j.WriteMode != WriteModeMergeSections || j.SlugFormat != SlugFormatDate || j.Subcategory != SubcategoryForbidden {
		t.Errorf("journal rules = %+v", j)
	}
	if j.DefaultSearch {
		t.Error("journal default_search must be false")
	}
	if !j.Embed {
		t.Error("journal must inherit embed=true")
	}
	h := DefaultEffectivePolicies[DocTypeHandoff]
	if h.Subcategory != SubcategoryRequired || h.Prunable {
		t.Errorf("handoff rules = %+v", h)
	}
	if h.ChainPrevious == nil || h.ChainPrevious.EdgeType != "continues_from" || h.ChainPrevious.Scope != "subcategory" {
		t.Errorf("handoff chain_previous = %+v", h.ChainPrevious)
	}
}

func TestResolve_NoReferenceRowErrors(t *testing.T) {
	_, err := ResolveDocTypePolicies([]DocTypePolicy{{DocType: DocTypeLearning, VerificationAgeDays: iptr(1)}})
	if err == nil {
		t.Error("resolving without a reference row must error")
	}
}

func TestValidateEffective_Rejections(t *testing.T) {
	base := EffectivePolicy{VerificationAgeDays: 1, WriteMode: WriteModeReplace, SlugFormat: SlugFormatAny, Subcategory: SubcategoryOptional, Embed: true, DefaultSearch: true}
	ok := func(p EffectivePolicy) EffectivePolicy { return p }

	cases := map[string]EffectivePolicy{
		"default_search without embed":  ok(EffectivePolicy{WriteMode: WriteModeReplace, SlugFormat: SlugFormatAny, Subcategory: SubcategoryOptional, DefaultSearch: true, Embed: false}),
		"duplicate_guard with merge":    ok(EffectivePolicy{WriteMode: WriteModeMergeSections, SlugFormat: SlugFormatAny, Subcategory: SubcategoryOptional, DuplicateGuard: true, Embed: true}),
		"bad write_mode":                ok(EffectivePolicy{WriteMode: "bogus", SlugFormat: SlugFormatAny, Subcategory: SubcategoryOptional}),
		"negative verification age":     ok(EffectivePolicy{VerificationAgeDays: -1, WriteMode: WriteModeReplace, SlugFormat: SlugFormatAny, Subcategory: SubcategoryOptional}),
		"negative expiration age":       ok(EffectivePolicy{ExpirationAgeDays: -1, WriteMode: WriteModeReplace, SlugFormat: SlugFormatAny, Subcategory: SubcategoryOptional}),
		"expiration below verification": ok(EffectivePolicy{VerificationAgeDays: 30, ExpirationAgeDays: 10, WriteMode: WriteModeReplace, SlugFormat: SlugFormatAny, Subcategory: SubcategoryOptional}),
	}
	for name, p := range cases {
		if err := ValidateEffective("x", p); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
	if err := ValidateEffective("x", base); err != nil {
		t.Errorf("valid policy rejected: %v", err)
	}
	// expiration 0 disables the tier; expiration >= verification is accepted.
	disabled := EffectivePolicy{VerificationAgeDays: 30, ExpirationAgeDays: 0, WriteMode: WriteModeReplace, SlugFormat: SlugFormatAny, Subcategory: SubcategoryOptional}
	if err := ValidateEffective("x", disabled); err != nil {
		t.Errorf("expiration 0 (disabled) must pass: %v", err)
	}
	atOrAbove := EffectivePolicy{VerificationAgeDays: 30, ExpirationAgeDays: 30, WriteMode: WriteModeReplace, SlugFormat: SlugFormatAny, Subcategory: SubcategoryOptional}
	if err := ValidateEffective("x", atOrAbove); err != nil {
		t.Errorf("expiration == verification must pass: %v", err)
	}
}

func TestValidateSlugFormat(t *testing.T) {
	if ValidateSlugFormat(SlugFormatDate, "2026-09-01") != nil {
		t.Error("YYYY-MM-DD must pass date")
	}
	if ValidateSlugFormat(SlugFormatDate, "sept-1") == nil {
		t.Error("sept-1 must fail date")
	}
	if ValidateSlugFormat(SlugFormatKebab, "My_Slug") == nil {
		t.Error("My_Slug must fail kebab")
	}
	if ValidateSlugFormat(SlugFormatAny, "anything at all") != nil {
		t.Error("any accepts everything")
	}
}

func TestValidateSubcategoryRule(t *testing.T) {
	sub := "proj"
	if ValidateSubcategoryRule(SubcategoryRequired, nil) == nil {
		t.Error("required with no subcategory must fail")
	}
	if ValidateSubcategoryRule(SubcategoryForbidden, &sub) == nil {
		t.Error("forbidden with a subcategory must fail")
	}
	if ValidateSubcategoryRule(SubcategoryRequired, &sub) != nil {
		t.Error("required with a subcategory must pass")
	}
	if ValidateSubcategoryRule(SubcategoryForbidden, nil) != nil {
		t.Error("forbidden with no subcategory must pass")
	}
}
