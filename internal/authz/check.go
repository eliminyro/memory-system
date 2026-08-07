package authz

import (
	"context"
	"errors"
	"fmt"
)

// DefaultMaxDepth caps Check recursion. The real graph is shallow (doc editor ->
// tenant member -> tenant admin -> system admin = depth 3); the cap is a
// misconfiguration backstop, the visited-set is the primary cycle guard.
const DefaultMaxDepth = 16

var (
	// ErrDepthExceeded is returned (fail closed) when evaluation exceeds MaxDepth;
	// distinguishable so callers can tell a resource deny from a hard deny.
	ErrDepthExceeded = errors.New("authz: check depth limit exceeded")
	// ErrUnknownRelation is returned when a Check names a type/relation not in the
	// namespace (a caller bug), rather than silently allowing or denying.
	ErrUnknownRelation = errors.New("authz: unknown object type or relation")
)

// Engine evaluates Zanzibar-style checks against a Store and fixed Namespace.
// Concurrency-safe if the Store is.
type Engine struct {
	store Store
	ns    Namespace

	// MaxDepth is the recursion cap (NewEngine: DefaultMaxDepth; tests may lower it).
	MaxDepth int
}

// NewEngine returns an Engine over store with the default namespace and depth cap.
func NewEngine(store Store) *Engine {
	return &Engine{store: store, ns: DefaultNamespace(), MaxDepth: DefaultMaxDepth}
}

// Check reports whether subjType:subjID holds relation on objType:objID,
// resolving rewrite rules recursively. Subject is always a concrete principal
// (no subject relation). Depth-bounded, cycle-safe, fails closed.
func (e *Engine) Check(ctx context.Context, objType, objID, relation, subjType, subjID string) (bool, error) {
	// visited prunes cycles on the current DFS path; memo caches definitive,
	// error-free results across the whole Check so diamond re-convergences aren't
	// recomputed. Both are keyed by object#relation — within one Check the subject
	// is fixed, so a node's definitive grant/deny is path-independent.
	return e.check(ctx, objType, objID, relation, subjType, subjID, 0, map[string]bool{}, map[string]bool{})
}

// check is the recursive core. depth counts hops from root; visited is the
// current DFS path of (object#relation) nodes, used to prune cycles; memo caches
// definitive (error-free) results for the duration of one Check.
func (e *Engine) check(ctx context.Context, objType, objID, relation, subjType, subjID string, depth int, visited, memo map[string]bool) (bool, error) {
	if depth > e.MaxDepth {
		return false, ErrDepthExceeded
	}

	node := objType + ":" + objID + "#" + relation
	// A node already resolved to a definitive result this Check returns it
	// directly (diamond re-convergence). Only definitive, error-free outcomes are
	// ever stored (see below), so this never short-circuits a cycle-prune or an
	// error path.
	if g, ok := memo[node]; ok {
		return g, nil
	}
	if visited[node] {
		// Cycle: node already on the current path. Prune (grants nothing new), no error.
		// NOT cached: a prune is a path-dependent under-approximation.
		return false, nil
	}

	def, ok := e.ns.Relation(objType, relation)
	if !ok {
		return false, fmt.Errorf("%w: %s#%s", ErrUnknownRelation, objType, relation)
	}

	visited[node] = true
	defer delete(visited, node)

	// Union rewrite: granted if ANY child grants. A branch error is remembered
	// but doesn't stop evaluation (a grant elsewhere still wins); with no grant,
	// the first error is surfaced to stay fail-closed.
	var firstErr error
	for _, rw := range def.Rewrites {
		granted, err := e.evalRewrite(ctx, rw, objType, objID, relation, subjType, subjID, depth, visited, memo)
		if granted {
			// Definitive grant: safe to cache.
			memo[node] = true
			return true, nil
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	// Cache a deny ONLY on a clean, error-free full sweep. An error (transient
	// store failure, or a depth cap hit in a child) must stay fail-closed AND
	// retryable on the next Check, so it is never cached.
	if firstErr == nil {
		memo[node] = false
	}
	return false, firstErr
}

// evalRewrite evaluates one union child. relation is the enclosing node's
// relation (the `this` clause reads its direct tuples).
func (e *Engine) evalRewrite(ctx context.Context, rw Rewrite, objType, objID, relation, subjType, subjID string, depth int, visited, memo map[string]bool) (bool, error) {
	switch rw.Kind {
	case RewriteThis:
		return e.evalThis(ctx, objType, objID, relation, subjType, subjID, depth, visited, memo)
	case RewriteComputedUserset:
		// Same object, sibling relation.
		return e.check(ctx, objType, objID, rw.Relation, subjType, subjID, depth+1, visited, memo)
	case RewriteTupleToUserset:
		return e.evalTupleToUserset(ctx, rw, objType, objID, subjType, subjID, depth, visited, memo)
	default:
		return false, nil
	}
}

// evalThis resolves the `this` clause: direct tuples on (objType, objID, relation).
// A tuple grants if it names the subject directly, is a wildcard for the
// subject's type (id "*"), or is a userset the subject belongs to (resolved recursively).
func (e *Engine) evalThis(ctx context.Context, objType, objID, relation, subjType, subjID string, depth int, visited, memo map[string]bool) (bool, error) {
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
		granted, err := e.check(ctx, tp.SubjectType, tp.SubjectID, tp.SubjectRelation, subjType, subjID, depth+1, visited, memo)
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
func (e *Engine) evalTupleToUserset(ctx context.Context, rw Rewrite, objType, objID, subjType, subjID string, depth int, visited, memo map[string]bool) (bool, error) {
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
		granted, err := e.check(ctx, p.SubjectType, p.SubjectID, rw.ComputedRelation, subjType, subjID, depth+1, visited, memo)
		if granted {
			return true, nil
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return false, firstErr
}
