// Package authz implements a built-in Google-Zanzibar-style relationship-based
// authorization engine for memory-system.
//
// Authorization is expressed entirely as relation tuples of the form
// object#relation@subject, evaluated against a fixed, in-process namespace
// configuration (see config.go) by a recursive Check evaluator (see check.go).
// Nothing in this package is user-editable at runtime: the namespace is a
// compile-time constant and tuples are written only out-of-band.
//
// This package is intentionally self-contained. It depends only on the Store
// interface for tuple access, so callers can wire it against either the
// in-memory store (fast unit tests) or the Postgres/GORM store (production).
package authz

// Object types recognised by the namespace configuration.
const (
	TypeUser     = "user"     // humans + service principals (unified subject)
	TypeSystem   = "system"   // singleton root, object id == SystemObjectID
	TypeTenant   = "tenant"   // a memory-system workspace
	TypeDocument = "document" // a stored document
)

// Relation names.
const (
	RelAdmin  = "admin"
	RelMember = "member"
	RelViewer = "viewer"
	RelEditor = "editor"
	RelSystem = "system" // tenant -> system parent edge
	RelTenant = "tenant" // document -> tenant parent edge
)

const (
	// SystemObjectID is the id of the singleton system object. Global admins
	// are granted system:memory#admin@user:<id>.
	SystemObjectID = "memory"

	// Wildcard is the subject id that matches every subject of its type. A
	// tuple with subject user:* grants the relation to any user subject
	// (public access, e.g. common-pool read).
	Wildcard = "*"

	// ServicePrincipalPrefix is the subject-id prefix for a tenant's service
	// principal — the implicit unified subject an API key resolves to when it
	// carries no explicit subject_id. The full id is ServicePrincipalPrefix
	// followed by the tenant UUID (see ServicePrincipalID).
	ServicePrincipalPrefix = "svc:"
)

// ServicePrincipalID returns the user-type subject id for a tenant's service
// principal: ServicePrincipalPrefix + the tenant UUID string. It is the single
// source of truth for the "svc:<tenant_id>" convention shared by the API-key
// path and the tuple seeders.
func ServicePrincipalID(tenantID string) string {
	return ServicePrincipalPrefix + tenantID
}

// Tuple is the store-facing representation of a single relation tuple:
//
//	<ObjectType>:<ObjectID>#<Relation>@<SubjectType>:<SubjectID>[#<SubjectRelation>]
//
// SubjectRelation is empty ("") for a direct subject (a concrete user or the
// user:* wildcard) and non-empty when the subject is itself a userset
// (type:id#relation), i.e. "everyone who has <SubjectRelation> on
// <SubjectType>:<SubjectID>".
//
// Tuple is deliberately a plain value type with no persistence concerns so the
// Store interface stays database-agnostic; the GORM row type lives in model.go.
type Tuple struct {
	ObjectType      string
	ObjectID        string
	Relation        string
	SubjectType     string
	SubjectID       string
	SubjectRelation string
}

// IsWildcard reports whether the tuple's subject is the public wildcard
// (subject id == "*" with no subject relation).
func (t Tuple) IsWildcard() bool {
	return t.SubjectRelation == "" && t.SubjectID == Wildcard
}

// IsUserset reports whether the tuple's subject is a userset (type:id#relation)
// rather than a concrete/wildcard subject.
func (t Tuple) IsUserset() bool {
	return t.SubjectRelation != ""
}
