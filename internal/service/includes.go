package service

import (
	"context"
	"path"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/repository"
)

// Include-resolution bounds reuse the resume traversal budget.
const (
	defaultIncludeDepth = defaultResumeDepth
	maxIncludeNodes     = 500  // cap on resolved documents
	maxIncludeVisits    = 2000 // cap on edges examined (bounds fan-out work)
)

// Include manifest statuses — one per resolved includes edge.
const (
	IncludeIncluded       = "included"
	IncludeSkippedScope   = "skipped_scope"
	IncludeSkippedCycle   = "skipped_cycle"
	IncludeSkippedDepth   = "skipped_depth"
	IncludeSkippedMissing = "skipped_missing"
)

// IncludeRef records how one includes edge resolved.
type IncludeRef struct {
	DocumentID    uuid.UUID `json:"document_id"`
	ViaDocumentID uuid.UUID `json:"via_document_id"`
	Status        string    `json:"status"`
}

// scopeMatches reports whether the read-time scope selects an include: any
// whitespace-separated token in values matches any pattern in patterns via a
// hierarchical "/"-glob (globMatch) — "**" crosses segments, "*" within one.
func scopeMatches(patterns, values string) bool {
	for _, val := range strings.Fields(values) {
		for _, pat := range strings.Fields(patterns) {
			if pat == val || globMatch(pat, val) {
				return true
			}
		}
	}
	return false
}

// globMatch matches one "/"-delimited pattern against one token: "**" consumes
// zero or more segments, each other segment is path.Match'd ("*"/"?" within a
// segment, never across "/"). Standard two-pointer star glob.
func globMatch(pattern, name string) bool {
	pat := strings.Split(pattern, "/")
	seg := strings.Split(name, "/")
	px, sx, starPx, starSx := 0, 0, -1, -1
	for sx < len(seg) {
		switch {
		case px < len(pat) && pat[px] == "**":
			starPx, starSx = px, sx
			px++
		case px < len(pat) && segOK(pat[px], seg[sx]):
			px, sx = px+1, sx+1
		case starPx >= 0:
			starSx++
			px, sx = starPx+1, starSx
		default:
			return false
		}
	}
	for px < len(pat) && pat[px] == "**" {
		px++
	}
	return px == len(pat)
}

// segOK matches one pattern segment; a malformed pattern (path.Match error) is
// treated as no match, never a panic.
func segOK(patSeg, nameSeg string) bool {
	ok, err := path.Match(patSeg, nameSeg)
	return err == nil && ok
}

// includeResolver walks a document's outgoing includes edges through the read
// path — bounded, cycle-safe — collecting a flat, de-duplicated view list.
type includeResolver struct {
	s         *MemoryService
	ctx       context.Context
	scope     []uuid.UUID // caller's readable tenant set
	condScope string      // read-time scope for conditional includes
	seen      map[uuid.UUID]bool
	onPath    map[uuid.UUID]bool
	included  []DocumentView
	manifest  []IncludeRef
	visits    int
}

// resolveIncludes expands root's outgoing includes into root.Includes (flat,
// ordered, de-duplicated) and root.IncludeManifest. Every target is read through
// the caller's scope, so nothing unreadable surfaces.
func (s *MemoryService) resolveIncludes(ctx context.Context, root *DocumentView, scope []uuid.UUID, condScope string) {
	if s.edges == nil {
		return
	}
	r := &includeResolver{
		s: s, ctx: ctx, scope: scope, condScope: condScope,
		seen:   map[uuid.UUID]bool{root.ID: true},
		onPath: map[uuid.UUID]bool{root.ID: true},
	}
	r.walk(root.ID, defaultIncludeDepth)
	root.Includes = r.included
	root.IncludeManifest = r.manifest
}

// GetDocumentExpanded is GetDocument with includes resolved: the returned view
// carries Includes + IncludeManifest. condScope gates conditional (scoped) includes.
func (s *MemoryService) GetDocumentExpanded(ctx context.Context, category string, subcategory *string, slug string, forceRead bool, reason, condScope string, overrideID *uuid.UUID) (*DocumentView, error) {
	view, err := s.GetDocument(ctx, category, subcategory, slug, forceRead, reason, overrideID)
	if err != nil {
		return nil, err
	}
	scope, err := s.readScope(ctx, overrideID)
	if err != nil {
		return nil, err
	}
	s.resolveIncludes(ctx, view, scope, condScope)
	return view, nil
}

// GetDocumentByIDExpanded is GetDocumentByID with includes resolved.
func (s *MemoryService) GetDocumentByIDExpanded(ctx context.Context, id uuid.UUID, forceRead bool, reason, condScope string, overrideID *uuid.UUID) (*DocumentView, error) {
	view, err := s.GetDocumentByID(ctx, id, forceRead, reason, overrideID)
	if err != nil {
		return nil, err
	}
	scope, err := s.readScope(ctx, overrideID)
	if err != nil {
		return nil, err
	}
	s.resolveIncludes(ctx, view, scope, condScope)
	return view, nil
}

// walk resolves docID's outgoing includes edges in link order, recursing pre-order.
func (r *includeResolver) walk(docID uuid.UUID, depth int) {
	edges, err := r.s.edges.ListByDocument(r.ctx, docID, r.scope)
	if err != nil {
		return
	}
	inc := make([]repository.EdgeListItem, 0, len(edges))
	for _, e := range edges {
		if e.Direction == "outgoing" && e.EdgeType == models.EdgeIncludes {
			inc = append(inc, e)
		}
	}
	sort.SliceStable(inc, func(i, j int) bool {
		if !inc[i].CreatedAt.Equal(inc[j].CreatedAt) {
			return inc[i].CreatedAt.Before(inc[j].CreatedAt)
		}
		return inc[i].EdgeID.String() < inc[j].EdgeID.String()
	})
	for _, e := range inc {
		tid := e.OtherDocumentID
		r.visits++
		switch {
		case r.onPath[tid]:
			r.manifest = append(r.manifest, IncludeRef{tid, docID, IncludeSkippedCycle})
			continue
		case r.seen[tid]:
			r.manifest = append(r.manifest, IncludeRef{tid, docID, IncludeIncluded})
			continue
		case depth <= 0 || len(r.included) >= maxIncludeNodes || r.visits > maxIncludeVisits:
			r.manifest = append(r.manifest, IncludeRef{tid, docID, IncludeSkippedDepth})
			continue
		}
		view, status := r.resolveOne(tid)
		r.manifest = append(r.manifest, IncludeRef{tid, docID, status})
		if status != IncludeIncluded {
			continue
		}
		r.seen[tid] = true
		r.included = append(r.included, *view)
		r.onPath[tid] = true
		r.walk(tid, depth-1)
		r.onPath[tid] = false
	}
}

// resolveOne reads one include target through the caller's scope, then applies the
// scope condition. Returns a skip status instead of a view when the target is
// scope-filtered, archived, or gone.
func (r *includeResolver) resolveOne(id uuid.UUID) (*DocumentView, string) {
	doc, err := r.s.docs.GetByID(r.ctx, r.scope, id)
	if err != nil {
		return nil, IncludeSkippedMissing
	}
	if doc.Scope != nil && strings.TrimSpace(*doc.Scope) != "" && !scopeMatches(*doc.Scope, r.condScope) {
		return nil, IncludeSkippedScope
	}
	mode, name, typ := r.s.tenantModeAndLabel(r.ctx, doc.TenantID)
	view, err := buildDocumentView(r.ctx, r.s.thresholds, doc, mode, false)
	if err != nil {
		return nil, IncludeSkippedMissing
	}
	view.TenantName, view.TenantType = name, typ
	return &view, IncludeIncluded
}
