package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/eliminyro/memory-system/internal/models"
	"github.com/eliminyro/memory-system/internal/service"
)

const benchCategory = "bench"

// canonicalMaxLen caps a canonicalized segment; subcategory and slug share
// one cap here since models.MaxSubcategoryLen == models.MaxSlugLen today.
var canonicalMaxLen = min(models.MaxSubcategoryLen, models.MaxSlugLen)

// invalidSegmentChar matches anything not allowed in a document path segment
// (mirrors internal/models' unexported validPathSegment character class).
var invalidSegmentChar = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

// sanitizeSegment makes s satisfy models.ValidateDocumentPath: disallowed
// runs become "_", a non-alphanumeric leading char gets an "x" prefix, and
// the result is capped at max. Deterministic, so re-runs produce stable ids.
func sanitizeSegment(s string, max int) string {
	if isValidSegment(s) && len(s) <= max {
		return s
	}
	s = invalidSegmentChar.ReplaceAllString(s, "_")
	if s == "" || !isAlnumByte(s[0]) {
		s = "x" + s
	}
	if len(s) > max {
		s = s[:max]
	}
	return s
}

func isAlnumByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func isValidSegment(s string) bool {
	return s != "" && isAlnumByte(s[0]) && !invalidSegmentChar.MatchString(s)
}

// CanonicalSegment is the ONE id->path-segment transform for the bench
// corpus. Contract every caller (this package and the later runner) must
// follow: search Subcategory = CanonicalSegment(question_id); gold set =
// {CanonicalSegment(id) for id in answer_session_ids}; a
// RetrievedSection.SessionID is already canonical (it's the doc slug). Use
// raw ids nowhere near scoring — mixing raw and canonical reads as zero recall.
func CanonicalSegment(raw string) string {
	return sanitizeSegment(raw, canonicalMaxLen)
}

// RenderSession turns one haystack session into a (path, content) document:
// path is bench/<CanonicalSegment(question_id)>/<CanonicalSegment(session_id)>,
// content is markdown with one "## turn <n> (<role>)" section per turn (the
// server splits markdown on "##" into separately-embedded sections). Errors
// if the canonicalized path still fails models.ValidateDocumentPath — a
// defensive check against sanitizeSegment drifting from the real validator.
func RenderSession(questionID, sessionID string, turns []Turn) (path, content string, err error) {
	subcat := CanonicalSegment(questionID)
	slug := CanonicalSegment(sessionID)
	if verr := models.ValidateDocumentPath(benchCategory, slug, &subcat); verr != nil {
		return "", "", fmt.Errorf("longmemeval: rendered path bench/%s/%s invalid: %w", subcat, slug, verr)
	}
	path = models.BuildPath(benchCategory, &subcat, slug)

	var b strings.Builder
	for i, t := range turns {
		fmt.Fprintf(&b, "## turn %d (%s)\n%s\n\n", i+1, t.Role, t.Content)
	}
	return path, b.String(), nil
}

// SessionTurn identifies one turn within one haystack session, keyed by
// CanonicalSegment(session_id) — see CanonicalSegment's contract.
type SessionTurn struct {
	SessionID string
	TurnIndex int // 1-based, matches the rendered "## turn N" heading
}

// AnswerMap is a set of has_answer:true (session, turn) pairs, keyed by
// question_id so per-instance maps from BuildDocSource can be merged.
type AnswerMap map[string]map[SessionTurn]struct{}

// BuildDocSource returns a DocSource emitting every haystack session of inst
// (via RenderSession) plus the AnswerMap of its has_answer:true turns. The
// DocSource errors on a haystack_session_ids/haystack_sessions length
// mismatch, or when two distinct raw session ids canonicalize to the same
// slug (silently merging them would corrupt gold-session scoring).
func BuildDocSource(inst Instance) (service.DocSource, AnswerMap) {
	answers := make(map[SessionTurn]struct{})
	for si, sessionID := range inst.HaystackSessionIDs {
		if si >= len(inst.HaystackSessions) {
			break
		}
		canonicalID := CanonicalSegment(sessionID)
		for ti, turn := range inst.HaystackSessions[si] {
			if turn.HasAnswer {
				answers[SessionTurn{SessionID: canonicalID, TurnIndex: ti + 1}] = struct{}{}
			}
		}
	}

	src := service.DocSource(func(emit func(path string, content []byte) error) error {
		if len(inst.HaystackSessionIDs) != len(inst.HaystackSessions) {
			return fmt.Errorf("longmemeval: question %s: %d haystack_session_ids but %d haystack_sessions",
				inst.QuestionID, len(inst.HaystackSessionIDs), len(inst.HaystackSessions))
		}

		seen := make(map[string]string, len(inst.HaystackSessionIDs)) // canonical -> first raw id seen
		for si, sessionID := range inst.HaystackSessionIDs {
			canonicalID := CanonicalSegment(sessionID)
			if prevRaw, ok := seen[canonicalID]; ok && prevRaw != sessionID {
				return fmt.Errorf("longmemeval: question %s: session ids %q and %q both canonicalize to %q",
					inst.QuestionID, prevRaw, sessionID, canonicalID)
			}
			seen[canonicalID] = sessionID

			path, content, err := RenderSession(inst.QuestionID, sessionID, inst.HaystackSessions[si])
			if err != nil {
				return err
			}
			if err := emit(path, []byte(content)); err != nil {
				return err
			}
		}
		return nil
	})

	return src, AnswerMap{inst.QuestionID: answers}
}
