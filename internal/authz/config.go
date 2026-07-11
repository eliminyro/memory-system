package authz

// RewriteKind identifies one child of a relation's userset-rewrite union.
type RewriteKind int

const (
	// RewriteThis is the set of direct tuples stored on (object, relation),
	// including a user:* wildcard tuple.
	RewriteThis RewriteKind = iota
	// RewriteComputedUserset is the set of subjects that have Relation on the
	// SAME object (a sibling relation, e.g. "member" includes "admin").
	RewriteComputedUserset
	// RewriteTupleToUserset follows a parent edge: read (object, Tupleset)
	// tuples to find parent objects, then evaluate ComputedRelation on each
	// parent (e.g. document editor == "member from tenant").
	RewriteTupleToUserset
)

// Rewrite is a single child of a relation's union rewrite rule.
type Rewrite struct {
	Kind RewriteKind

	// Relation is the sibling relation to evaluate (RewriteComputedUserset).
	Relation string

	// Tupleset is the parent-edge relation read on the object, and
	// ComputedRelation is the relation evaluated on each referenced parent
	// object (RewriteTupleToUserset).
	Tupleset         string
	ComputedRelation string
}

// RelationDef is the definition of one relation within a type: the set of
// direct subject specs allowed by its `this` clause (documentation only; Check
// does not enforce subject typing) and the union of rewrite rules that define
// its membership.
type RelationDef struct {
	Name           string
	DirectSubjects []string
	Rewrites       []Rewrite
}

// TypeDef is the set of relations defined on an object type.
type TypeDef struct {
	Name      string
	Relations map[string]RelationDef
}

// Namespace is the fixed, in-process authorization model. It is not
// user-editable; DefaultNamespace returns the single canonical instance.
type Namespace struct {
	Types map[string]TypeDef
}

// Relation returns the definition of objType#relation, or ok=false if the type
// or relation is not part of the namespace.
func (n Namespace) Relation(objType, relation string) (RelationDef, bool) {
	t, ok := n.Types[objType]
	if !ok {
		return RelationDef{}, false
	}
	rd, ok := t.Relations[relation]
	return rd, ok
}

func thisRewrite() Rewrite { return Rewrite{Kind: RewriteThis} }

func computed(relation string) Rewrite {
	return Rewrite{Kind: RewriteComputedUserset, Relation: relation}
}

func from(tupleset, computedRelation string) Rewrite {
	return Rewrite{Kind: RewriteTupleToUserset, Tupleset: tupleset, ComputedRelation: computedRelation}
}

// DefaultNamespace returns the fixed memory-system authorization model,
// mirroring design D1:
//
//	type user
//	type system
//	  admin:  [user]
//	type tenant
//	  system: [system]                       # parent edge (seeded at tenant create)
//	  admin:  [user] or admin from system     # tenant admins ∪ global admins
//	  member: [user] or admin                 # admins are members
//	  viewer: [user:*] or member              # wildcard enables public read
//	type document
//	  tenant: [tenant]                        # parent edge (set at document create)
//	  viewer: [user] or viewer from tenant
//	  editor: [user] or member from tenant
func DefaultNamespace() Namespace {
	return Namespace{
		Types: map[string]TypeDef{
			TypeUser: {
				Name:      TypeUser,
				Relations: map[string]RelationDef{},
			},
			TypeSystem: {
				Name: TypeSystem,
				Relations: map[string]RelationDef{
					RelAdmin: {
						Name:           RelAdmin,
						DirectSubjects: []string{TypeUser},
						Rewrites:       []Rewrite{thisRewrite()},
					},
				},
			},
			TypeTenant: {
				Name: TypeTenant,
				Relations: map[string]RelationDef{
					// Parent edge to the singleton system object.
					RelSystem: {
						Name:           RelSystem,
						DirectSubjects: []string{TypeSystem},
						Rewrites:       []Rewrite{thisRewrite()},
					},
					// Direct tenant admins ∪ global admins (admin from system).
					RelAdmin: {
						Name:           RelAdmin,
						DirectSubjects: []string{TypeUser},
						Rewrites:       []Rewrite{thisRewrite(), from(RelSystem, RelAdmin)},
					},
					// Direct members ∪ admins.
					RelMember: {
						Name:           RelMember,
						DirectSubjects: []string{TypeUser},
						Rewrites:       []Rewrite{thisRewrite(), computed(RelAdmin)},
					},
					// Public wildcard read ∪ members. this allows user:*.
					RelViewer: {
						Name:           RelViewer,
						DirectSubjects: []string{TypeUser, TypeUser + ":" + Wildcard},
						Rewrites:       []Rewrite{thisRewrite(), computed(RelMember)},
					},
				},
			},
			TypeDocument: {
				Name: TypeDocument,
				Relations: map[string]RelationDef{
					// Parent edge to the owning tenant.
					RelTenant: {
						Name:           RelTenant,
						DirectSubjects: []string{TypeTenant},
						Rewrites:       []Rewrite{thisRewrite()},
					},
					// Direct viewers ∪ viewers of the owning tenant.
					RelViewer: {
						Name:           RelViewer,
						DirectSubjects: []string{TypeUser},
						Rewrites:       []Rewrite{thisRewrite(), from(RelTenant, RelViewer)},
					},
					// Direct editors ∪ members of the owning tenant.
					RelEditor: {
						Name:           RelEditor,
						DirectSubjects: []string{TypeUser},
						Rewrites:       []Rewrite{thisRewrite(), from(RelTenant, RelMember)},
					},
				},
			},
		},
	}
}
