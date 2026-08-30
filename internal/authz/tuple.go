// Package authz is a Zanzibar-style relationship-based authorization engine:
// relation tuples (object#relation@subject) evaluated against a fixed,
// non-user-editable namespace by a recursive Check. Tuples are written only
// out-of-band; the Store interface backs either in-memory (tests) or Postgres.
package authz

// Object types.
const (
	TypeUser     = "user"     // humans + service principals (unified subject)
	TypeSystem   = "system"   // singleton root, object id == SystemObjectID
	TypeTenant   = "tenant"   // a memory-system workspace
	TypeDocument = "document" // a stored document
)

// Relation names.
const (
	RelAdmin   = "admin"
	RelOwner   = "owner" // personal-tenant owner: a manager (⊆ manager) without system-admin reach (⊄ admin)
	RelManager = "manager"
	RelMember  = "member"
	RelViewer  = "viewer"
	RelEditor  = "editor"
	RelSystem  = "system" // tenant -> system parent edge
	RelTenant  = "tenant" // document -> tenant parent edge
)

const (
	// SystemObjectID is the singleton system object's id; global admins hold
	// system:memory#admin@user:<id>.
	SystemObjectID = "memory"

	// Wildcard is the subject id matching every subject of its type; a user:*
	// tuple grants the relation to any user (public access, e.g. common-pool read).
	Wildcard = "*"

	// ServicePrincipalPrefix is the subject-id prefix for a tenant's service
	// principal — the unified subject an API key resolves to with no explicit
	// subject_id. Full id: prefix + tenant UUID (see ServicePrincipalID).
	ServicePrincipalPrefix = "svc:"
)

// ServicePrincipalID returns the service-principal subject id for a tenant:
// ServicePrincipalPrefix + tenant UUID. Single source of truth for the
// "svc:<tenant_id>" convention.
func ServicePrincipalID(tenantID string) string {
	return ServicePrincipalPrefix + tenantID
}

// Tuple is a single relation tuple:
//
//	<ObjectType>:<ObjectID>#<Relation>@<SubjectType>:<SubjectID>[#<SubjectRelation>]
//
// SubjectRelation is "" for a direct subject (concrete user or user:* wildcard)
// and non-empty when the subject is a userset (type:id#relation): "everyone who
// has <SubjectRelation> on <SubjectType>:<SubjectID>". Plain value type; the
// GORM row lives in model.go.
type Tuple struct {
	ObjectType      string
	ObjectID        string
	Relation        string
	SubjectType     string
	SubjectID       string
	SubjectRelation string
}

// IsWildcard reports whether the subject is the public wildcard (id "*", no subject relation).
func (t Tuple) IsWildcard() bool {
	return t.SubjectRelation == "" && t.SubjectID == Wildcard
}

// IsUserset reports whether the subject is a userset (type:id#relation) vs. a concrete/wildcard subject.
func (t Tuple) IsUserset() bool {
	return t.SubjectRelation != ""
}
