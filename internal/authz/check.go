package authz

import (
	"context"
	"errors"
	"fmt"
)

// DefaultMaxDepth caps recursion in Check. The real graph is shallow (document
// editor -> tenant member -> tenant admin -> system admin is depth 3), so the
// cap is a backstop against misconfiguration; the visited-set is the primary
// cycle guard.
const DefaultMaxDepth = 16

var (
	// ErrDepthExceeded is returned (fail closed) when evaluation exceeds
	// MaxDepth. It is distinguishable so callers/tests can tell a hard deny
	// from a bounded-resource deny.
	ErrDepthExceeded = errors.New("authz: check depth limit exceeded")
	// ErrUnknownRelation is returned when a Check names an object type or
	// relation that is not part of the namespace configuration (a caller bug),
	// rather than silently allowing or denying.
	ErrUnknownRelation = errors.New("authz: unknown object type or relation")
)

// Engine evaluates Zanzibar-style authorization checks against a Store and a
// fixed Namespace. It is safe for concurrent use as long as the underlying
// Store is.
type Engine struct {
	store Store
	ns    Namespace

	// MaxDepth is the recursion cap. NewEngine sets it to DefaultMaxDepth;
	// tests may lower it to exercise the depth guard.
	MaxDepth int
}

// NewEngine returns an Engine over the given store using the default namespace
// and depth cap.
func NewEngine(store Store) *Engine {
	return &Engine{store: store, ns: DefaultNamespace(), MaxDepth: DefaultMaxDepth}
}

// Check reports whether the subject (subjType:subjID) holds the relation on the
// object (objType:objID#relation), resolving the namespace rewrite rules
// recursively. The subject is always a concrete principal (no subject
// relation). Evaluation is depth-bounded and cycle-safe; it fails closed.
func (e *Engine) Check(ctx context.Context, objType, objID, relation, subjType, subjID string) (bool, error) {
	return e.check(ctx, objType, objID, relation, subjType, subjID, 0, map[string]bool{})
}

// check is the recursive core. depth counts hops from the root Check; visited
// is the current DFS path of (object#relation) nodes, used to prune cycles.
func (e *Engine) check(ctx context.Context, objType, objID, relation, subjType, subjID string, depth int, visited map[string]bool) (bool, error) {
	if depth > e.MaxDepth {
		return false, ErrDepthExceeded
	}

	node := objType + ":" + objID + "#" + relation
	if visited[node] {
		// Cycle: this (object, relation) is already on the current path.
		// Prune this branch — it can grant nothing new — without erroring.
		return false, nil
	}

	def, ok := e.ns.Relation(objType, relation)
	if !ok {
		return false, fmt.Errorf("%w: %s#%s", ErrUnknownRelation, objType, relation)
	}

	visited[node] = true
	defer delete(visited, node)

	// Union rewrite: the subject holds the relation if ANY child grants it.
	// Errors from a branch are remembered but do not stop evaluation, so a
	// genuine grant from another branch still wins; if no branch grants, the
	// first error (if any) is surfaced to preserve fail-closed semantics.
	var firstErr error
	for _, rw := range def.Rewrites {
		granted, err := e.evalRewrite(ctx, rw, objType, objID, relation, subjType, subjID, depth, visited)
		if granted {
			return true, nil
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return false, firstErr
}

// evalRewrite evaluates a single child of the union. relation is the relation
// of the enclosing node (needed by the `this` clause to read its direct tuples).
func (e *Engine) evalRewrite(ctx context.Context, rw Rewrite, objType, objID, relation, subjType, subjID string, depth int, visited map[string]bool) (bool, error) {
	switch rw.Kind {
	case RewriteThis:
		return e.evalThis(ctx, objType, objID, relation, subjType, subjID, depth, visited)
	case RewriteComputedUserset:
		// Same object, sibling relation.
		return e.check(ctx, objType, objID, rw.Relation, subjType, subjID, depth+1, visited)
	case RewriteTupleToUserset:
		return e.evalTupleToUserset(ctx, rw, objType, objID, subjType, subjID, depth, visited)
	default:
		return false, nil
	}
}

// evalThis resolves the `this` clause: the direct tuples on
// (objType, objID, relation). A tuple grants access if:
//   - it names the subject directly (subject type + id match), or
//   - it is a wildcard for the subject's type (subject id == "*"), or
//   - it is a userset (type:id#rel) and the subject is a member of that userset
//     (resolved recursively).
func (e *Engine) evalThis(ctx context.Context, objType, objID, relation, subjType, subjID string, depth int, visited map[string]bool) (bool, error) {
	tuples, err := e.store.ReadByObjectRelation(ctx, objType, objID, relation)
	if err != nil {
		return false, err
	}
	var firstErr error
	for _, tp := range tuples {
		if tp.SubjectRelation == "" {
			// Direct or wildcard subject.
			if tp.SubjectType != subjType {
				continue
			}
			if tp.SubjectID == subjID || tp.SubjectID == Wildcard {
				return true, nil
			}
			continue
		}
		// Userset subject: is the query subject a member of tp's userset?
		granted, err := e.check(ctx, tp.SubjectType, tp.SubjectID, tp.SubjectRelation, subjType, subjID, depth+1, visited)
		if granted {
			return true, nil
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return false, firstErr
}

// evalTupleToUserset resolves `<ComputedRelation> from <Tupleset>`: read the
// parent-edge tuples on (objType, objID, Tupleset); each subject is a parent
// object; evaluate ComputedRelation on each parent.
func (e *Engine) evalTupleToUserset(ctx context.Context, rw Rewrite, objType, objID, subjType, subjID string, depth int, visited map[string]bool) (bool, error) {
	parents, err := e.store.ReadByObjectRelation(ctx, objType, objID, rw.Tupleset)
	if err != nil {
		return false, err
	}
	var firstErr error
	for _, p := range parents {
		if p.SubjectRelation != "" {
			// Parent edges point at objects, not usersets; skip malformed rows.
			continue
		}
		granted, err := e.check(ctx, p.SubjectType, p.SubjectID, rw.ComputedRelation, subjType, subjID, depth+1, visited)
		if granted {
			return true, nil
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return false, firstErr
}
