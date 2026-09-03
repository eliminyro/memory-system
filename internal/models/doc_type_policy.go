package models

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"gorm.io/datatypes"
)

var (
	dateSlugRe     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	datetimeSlugRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(:\d{2})?$`)
	kebabSlugRe    = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
)

// ValidateSlugFormat rejects (never rewrites) a slug that doesn't match the
// doc_type's format. InferDocType already consumed the slug to classify, so a
// changed slug would contradict that classification (spec "Identity validation").
func ValidateSlugFormat(format SlugFormat, slug string) error {
	switch format {
	case SlugFormatDate:
		if !dateSlugRe.MatchString(slug) {
			return fmt.Errorf("slug %q must be a date (YYYY-MM-DD)", slug)
		}
	case SlugFormatDateTime:
		if !datetimeSlugRe.MatchString(slug) {
			return fmt.Errorf("slug %q must be a timestamp (YYYY-MM-DD[T ]hh:mm)", slug)
		}
	case SlugFormatKebab:
		if !kebabSlugRe.MatchString(slug) {
			return fmt.Errorf("slug %q must be kebab-case", slug)
		}
	}
	return nil
}

// ValidateSubcategoryRule enforces the doc_type's subcategory requirement.
func ValidateSubcategoryRule(rule SubcategoryRule, subcategory *string) error {
	has := subcategory != nil && strings.TrimSpace(*subcategory) != ""
	switch rule {
	case SubcategoryRequired:
		if !has {
			return fmt.Errorf("subcategory is required for this doc_type")
		}
	case SubcategoryForbidden:
		if has {
			return fmt.Errorf("subcategory is not allowed for this doc_type")
		}
	}
	return nil
}

// WriteMode decides what happens to an existing document's sections on re-store.
type WriteMode string

const (
	WriteModeReplace       WriteMode = "replace"
	WriteModeMergeSections WriteMode = "merge_sections"
	WriteModeAppendOnly    WriteMode = "append_only"
)

// SlugFormat constrains the slug shape a doc_type accepts on write.
type SlugFormat string

const (
	SlugFormatAny      SlugFormat = "any"
	SlugFormatDate     SlugFormat = "date"
	SlugFormatDateTime SlugFormat = "datetime"
	SlugFormatKebab    SlugFormat = "kebab"
)

// SubcategoryRule requires, forbids, or allows a subcategory.
type SubcategoryRule string

const (
	SubcategoryOptional  SubcategoryRule = "optional"
	SubcategoryRequired  SubcategoryRule = "required"
	SubcategoryForbidden SubcategoryRule = "forbidden"
)

// DocTypePolicy is one row of doc_type_policies. Scalar rules are nullable: NULL
// means "inherit from the reference row", kept distinct from a set value (e.g.
// verification_age_days 0 = never nudge). rules holds non-scalar/experimental rules.
type DocTypePolicy struct {
	DocType             string           `gorm:"size:32;primaryKey" json:"doc_type"`
	VerificationAgeDays *int             `json:"verification_age_days"`
	ExpirationAgeDays   *int             `json:"expiration_age_days"`
	DuplicateGuard      *bool            `json:"duplicate_guard"`
	CleanupScan         *bool            `json:"cleanup_scan"`
	LintStaleCheck      *bool            `json:"lint_stale_check"`
	Embed               *bool            `json:"embed"`
	DefaultSearch       *bool            `json:"default_search"`
	Prunable            *bool            `json:"prunable"`
	WriteMode           *WriteMode       `gorm:"size:16" json:"write_mode"`
	SlugFormat          *SlugFormat      `gorm:"size:16" json:"slug_format"`
	Subcategory         *SubcategoryRule `gorm:"size:16" json:"subcategory"`
	Rules               datatypes.JSON   `gorm:"type:jsonb;not null;default:'{}'" json:"rules"`
}

func (DocTypePolicy) TableName() string { return "doc_type_policies" }

// DocTypePoliciesNotifyChannel is the LISTEN/NOTIFY channel the doc_type_policies
// trigger signals on write; the policy store registers a reload against it.
const DocTypePoliciesNotifyChannel = "doc_type_policies_changed"

func iptr(i int) *int { return &i }
func ptrOrZero(i *int) int {
	if i == nil {
		return 0
	}
	return *i
}
func bptr(b bool) *bool                        { return &b }
func wmptr(w WriteMode) *WriteMode             { return &w }
func sfptr(s SlugFormat) *SlugFormat           { return &s }
func scptr(s SubcategoryRule) *SubcategoryRule { return &s }

// DefaultDocTypePolicies is the seed set (spec "Seeded defaults"). reference sets
// every column; the rest set only what differs. NULL means inherit;
// expiration_age_days is NULL (disabled) everywhere — the hard withhold is opt-in.
var DefaultDocTypePolicies = []DocTypePolicy{
	{
		DocType: DocTypeReference, VerificationAgeDays: iptr(90),
		DuplicateGuard: bptr(true), CleanupScan: bptr(true), LintStaleCheck: bptr(true),
		Embed: bptr(true), DefaultSearch: bptr(true), Prunable: bptr(true),
		WriteMode: wmptr(WriteModeReplace), SlugFormat: sfptr(SlugFormatAny), Subcategory: scptr(SubcategoryOptional),
	},
	{DocType: DocTypeProjectState, VerificationAgeDays: iptr(14)},
	{DocType: DocTypeAudit, VerificationAgeDays: iptr(30)},
	{DocType: DocTypeLearning, VerificationAgeDays: iptr(180)},
	{DocType: DocTypePreference, VerificationAgeDays: iptr(365)},
	{DocType: DocTypeTool, VerificationAgeDays: iptr(90)},
	{
		DocType: DocTypeJournal, VerificationAgeDays: iptr(0),
		DuplicateGuard: bptr(false), CleanupScan: bptr(false), LintStaleCheck: bptr(false), DefaultSearch: bptr(false),
		WriteMode: wmptr(WriteModeMergeSections), SlugFormat: sfptr(SlugFormatDate), Subcategory: scptr(SubcategoryForbidden),
	},
	{
		DocType: DocTypeHandoff, VerificationAgeDays: iptr(0),
		DuplicateGuard: bptr(false), CleanupScan: bptr(false), LintStaleCheck: bptr(false), DefaultSearch: bptr(false),
		Prunable: bptr(false), Subcategory: scptr(SubcategoryRequired),
		Rules: datatypes.JSON([]byte(`{"chain_previous":{"scope":"subcategory","edge_type":"continues_from"}}`)),
	},
	{
		DocType: DocTypePrompt, VerificationAgeDays: iptr(0),
		DuplicateGuard: bptr(false), CleanupScan: bptr(false), LintStaleCheck: bptr(false),
		Prunable: bptr(false), Embed: bptr(false), DefaultSearch: bptr(false),
		WriteMode: wmptr(WriteModeReplace), Subcategory: scptr(SubcategoryRequired),
	},
}

// ChainPrevious, when present in rules, links a new document to the prior latest
// in its scope (as handoffs do). Scope is "subcategory"; EdgeType names the link.
type ChainPrevious struct {
	Scope    string `json:"scope"`
	EdgeType string `json:"edge_type"`
}

// EffectivePolicy is a doc_type's rule set after NULL inheritance is resolved —
// the in-memory value every mechanism reads. RawRules carries the JSONB verbatim
// so lint can flag keys the server does not implement.
type EffectivePolicy struct {
	VerificationAgeDays int
	ExpirationAgeDays   int
	DuplicateGuard      bool
	CleanupScan         bool
	LintStaleCheck      bool
	Embed               bool
	DefaultSearch       bool
	Prunable            bool
	WriteMode           WriteMode
	SlugFormat          SlugFormat
	Subcategory         SubcategoryRule
	ChainPrevious       *ChainPrevious
	RawRules            map[string]json.RawMessage
}

// KnownRuleKeys are the rules JSONB keys the server implements; anything else is
// an experimental typo lint should surface (design D1, task 9.1).
var KnownRuleKeys = map[string]struct{}{"chain_previous": {}}

// DefaultEffectivePolicies is the resolved seed set, so a store constructed but
// not yet Loaded (unit fixtures) still serves the seeded rules.
var DefaultEffectivePolicies = mustResolveDefaults()

func mustResolveDefaults() map[string]EffectivePolicy {
	m, err := ResolveDocTypePolicies(DefaultDocTypePolicies)
	if err != nil {
		panic("resolve default doc_type policies: " + err.Error())
	}
	return m
}

// DefaultEffectivePolicy is the reference-equivalent fallback used when no policy
// store is loaded (e.g. import CLI, unit fixtures) — behavior identical to today.
func DefaultEffectivePolicy() EffectivePolicy {
	return EffectivePolicy{
		VerificationAgeDays: 90, DuplicateGuard: true, CleanupScan: true, LintStaleCheck: true,
		Embed: true, DefaultSearch: true, Prunable: true,
		WriteMode: WriteModeReplace, SlugFormat: SlugFormatAny, Subcategory: SubcategoryOptional,
	}
}

var validWriteModes = map[WriteMode]struct{}{WriteModeReplace: {}, WriteModeMergeSections: {}, WriteModeAppendOnly: {}}
var validSlugFormats = map[SlugFormat]struct{}{SlugFormatAny: {}, SlugFormatDate: {}, SlugFormatDateTime: {}, SlugFormatKebab: {}}
var validSubcategoryRules = map[SubcategoryRule]struct{}{SubcategoryOptional: {}, SubcategoryRequired: {}, SubcategoryForbidden: {}}

// ValidateEffective checks a resolved policy's enums, ranges, and cross-field
// rules — run on the merged result so inheritance can't produce a bad combination
// no single row shows (design D4, spec "Rules are edited only by instance admins").
func ValidateEffective(docType string, eff EffectivePolicy) error {
	if _, ok := validWriteModes[eff.WriteMode]; !ok {
		return fmt.Errorf("doc_type %q: invalid write_mode %q", docType, eff.WriteMode)
	}
	if _, ok := validSlugFormats[eff.SlugFormat]; !ok {
		return fmt.Errorf("doc_type %q: invalid slug_format %q", docType, eff.SlugFormat)
	}
	if _, ok := validSubcategoryRules[eff.Subcategory]; !ok {
		return fmt.Errorf("doc_type %q: invalid subcategory %q", docType, eff.Subcategory)
	}
	if eff.VerificationAgeDays < 0 {
		return fmt.Errorf("doc_type %q: verification_age_days must be >= 0, got %d", docType, eff.VerificationAgeDays)
	}
	if eff.ExpirationAgeDays < 0 {
		return fmt.Errorf("doc_type %q: expiration_age_days must be >= 0, got %d", docType, eff.ExpirationAgeDays)
	}
	// 0 disables expiration; when set it must not sit below the verification age.
	if eff.ExpirationAgeDays != 0 && eff.ExpirationAgeDays < eff.VerificationAgeDays {
		return fmt.Errorf("doc_type %q: expiration_age_days (%d) must be >= verification_age_days (%d)", docType, eff.ExpirationAgeDays, eff.VerificationAgeDays)
	}
	if eff.DefaultSearch && !eff.Embed {
		return fmt.Errorf("doc_type %q: default_search requires embed (nothing to rank without a vector)", docType)
	}
	if eff.DuplicateGuard && eff.WriteMode != WriteModeReplace {
		return fmt.Errorf("doc_type %q: duplicate_guard requires write_mode=replace, got %q", docType, eff.WriteMode)
	}
	return nil
}

// ResolveDocTypePolicies builds the effective set: every row's NULL scalars
// inherit the reference row, which must exist and be fully specified. Returns a
// map keyed by doc_type; callers fall back to the reference entry for unknowns.
func ResolveDocTypePolicies(rows []DocTypePolicy) (map[string]EffectivePolicy, error) {
	byType := make(map[string]DocTypePolicy, len(rows))
	for _, r := range rows {
		byType[r.DocType] = r
	}
	ref, ok := byType[DocTypeReference]
	if !ok {
		return nil, fmt.Errorf("doc_type_policies: no %q row to inherit from", DocTypeReference)
	}
	base, err := effectiveFromReference(ref)
	if err != nil {
		return nil, err
	}
	out := make(map[string]EffectivePolicy, len(rows))
	for _, r := range rows {
		eff, err := resolveRow(r, base)
		if err != nil {
			return nil, fmt.Errorf("doc_type %q: %w", r.DocType, err)
		}
		out[r.DocType] = eff
	}
	return out, nil
}

// effectiveFromReference reads the reference row, which must set every scalar.
func effectiveFromReference(ref DocTypePolicy) (EffectivePolicy, error) {
	missing := func(field string) (EffectivePolicy, error) {
		return EffectivePolicy{}, fmt.Errorf("reference row must set %s (nothing to inherit from)", field)
	}
	switch {
	case ref.VerificationAgeDays == nil:
		return missing("verification_age_days")
	case ref.DuplicateGuard == nil:
		return missing("duplicate_guard")
	case ref.CleanupScan == nil:
		return missing("cleanup_scan")
	case ref.LintStaleCheck == nil:
		return missing("lint_stale_check")
	case ref.Embed == nil:
		return missing("embed")
	case ref.DefaultSearch == nil:
		return missing("default_search")
	case ref.Prunable == nil:
		return missing("prunable")
	case ref.WriteMode == nil:
		return missing("write_mode")
	case ref.SlugFormat == nil:
		return missing("slug_format")
	case ref.Subcategory == nil:
		return missing("subcategory")
	}
	return resolveRow(ref, EffectivePolicy{
		VerificationAgeDays: *ref.VerificationAgeDays,
		ExpirationAgeDays:   ptrOrZero(ref.ExpirationAgeDays),
		DuplicateGuard:      *ref.DuplicateGuard,
		CleanupScan:         *ref.CleanupScan,
		LintStaleCheck:      *ref.LintStaleCheck,
		Embed:               *ref.Embed,
		DefaultSearch:       *ref.DefaultSearch,
		Prunable:            *ref.Prunable,
		WriteMode:           *ref.WriteMode,
		SlugFormat:          *ref.SlugFormat,
		Subcategory:         *ref.Subcategory,
	})
}

// resolveRow overlays a row's set scalars on base and parses its rules JSONB.
func resolveRow(r DocTypePolicy, base EffectivePolicy) (EffectivePolicy, error) {
	eff := base
	if r.VerificationAgeDays != nil {
		eff.VerificationAgeDays = *r.VerificationAgeDays
	}
	if r.ExpirationAgeDays != nil {
		eff.ExpirationAgeDays = *r.ExpirationAgeDays
	}
	if r.DuplicateGuard != nil {
		eff.DuplicateGuard = *r.DuplicateGuard
	}
	if r.CleanupScan != nil {
		eff.CleanupScan = *r.CleanupScan
	}
	if r.LintStaleCheck != nil {
		eff.LintStaleCheck = *r.LintStaleCheck
	}
	if r.Embed != nil {
		eff.Embed = *r.Embed
	}
	if r.DefaultSearch != nil {
		eff.DefaultSearch = *r.DefaultSearch
	}
	if r.Prunable != nil {
		eff.Prunable = *r.Prunable
	}
	if r.WriteMode != nil {
		eff.WriteMode = *r.WriteMode
	}
	if r.SlugFormat != nil {
		eff.SlugFormat = *r.SlugFormat
	}
	if r.Subcategory != nil {
		eff.Subcategory = *r.Subcategory
	}
	raw, chain, err := parseRules(r.Rules)
	if err != nil {
		return EffectivePolicy{}, err
	}
	eff.RawRules, eff.ChainPrevious = raw, chain
	return eff, nil
}

// parseRules decodes the rules JSONB into a raw key map plus the typed
// chain_previous, if present.
func parseRules(js datatypes.JSON) (map[string]json.RawMessage, *ChainPrevious, error) {
	if len(js) == 0 {
		return map[string]json.RawMessage{}, nil, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(js, &raw); err != nil {
		return nil, nil, fmt.Errorf("rules is not a JSON object: %w", err)
	}
	var chain *ChainPrevious
	if v, ok := raw["chain_previous"]; ok {
		var cp ChainPrevious
		if err := json.Unmarshal(v, &cp); err != nil {
			return nil, nil, fmt.Errorf("rules.chain_previous malformed: %w", err)
		}
		chain = &cp
	}
	return raw, chain, nil
}
