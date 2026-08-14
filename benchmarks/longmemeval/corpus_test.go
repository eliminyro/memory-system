package main

import (
	"strings"
	"testing"

	"github.com/eliminyro/memory-system/internal/models"
)

func TestRenderSession(t *testing.T) {
	turns := []Turn{
		{Role: "user", Content: "Hello there."},
		{Role: "assistant", Content: "Hi, how can I help?"},
	}
	path, content, err := RenderSession("q_001", "sess_1", turns)
	if err != nil {
		t.Fatalf("RenderSession: %v", err)
	}

	if want := "bench/q_001/sess_1"; path != want {
		t.Errorf("path = %q, want %q", path, want)
	}

	wantHeadings := []string{"## turn 1 (user)", "## turn 2 (assistant)"}
	pos := 0
	for _, want := range wantHeadings {
		idx := strings.Index(content[pos:], want)
		if idx < 0 {
			t.Fatalf("content missing heading %q; got:\n%s", want, content)
		}
		pos += idx + len(want)
	}
}

// TestRenderSessionSanitizesAndValidates covers point 3 of the adversarial
// review: raw ids with disallowed characters must still render to a path
// that the REAL internal/models validator (not just our mirrored regex)
// accepts.
func TestRenderSessionSanitizesAndValidates(t *testing.T) {
	path, _, err := RenderSession("weird question!", "sess one: two", nil)
	if err != nil {
		t.Fatalf("RenderSession: %v", err)
	}

	category, subcategory, slug := models.ParsePath(path)
	if err := models.ValidateDocumentPath(category, slug, subcategory); err != nil {
		t.Errorf("rendered path %q failed models.ValidateDocumentPath: %v", path, err)
	}
}

func TestBuildDocSource(t *testing.T) {
	instances, err := LoadDataset(fixturePath)
	if err != nil {
		t.Fatalf("LoadDataset: %v", err)
	}

	// has_answer turns from testdata/fixture.json: single_session_001 sess_1
	// turn 1; multi_session_002 sess_3 turn 1 and sess_4 turn 1.
	wantAnswers := map[string]map[SessionTurn]struct{}{
		"single_session_001": {{SessionID: "sess_1", TurnIndex: 1}: {}},
		"multi_session_002": {
			{SessionID: "sess_3", TurnIndex: 1}: {},
			{SessionID: "sess_4", TurnIndex: 1}: {},
		},
		"knowledge_update_003_abs": {},
	}

	for _, inst := range instances {
		src, answers := BuildDocSource(inst)

		var emitted int
		if err := src(func(path string, content []byte) error {
			emitted++
			return nil
		}); err != nil {
			t.Fatalf("%s: DocSource: %v", inst.QuestionID, err)
		}
		if want := len(inst.HaystackSessionIDs); emitted != want {
			t.Errorf("%s: emitted %d docs, want %d", inst.QuestionID, emitted, want)
		}

		want := wantAnswers[inst.QuestionID]
		got := answers[inst.QuestionID]
		if len(got) != len(want) {
			t.Errorf("%s: answers = %+v, want %+v", inst.QuestionID, got, want)
			continue
		}
		for k := range want {
			if _, ok := got[k]; !ok {
				t.Errorf("%s: missing answer entry %+v", inst.QuestionID, k)
			}
		}
	}
}

// TestBuildDocSourceSanitizedIDStillScores is the regression test for the
// IMPORTANT adversarial-review bug: the AnswerMap key must be built from the
// same CanonicalSegment transform as the emitted doc slug, or gold lookups
// silently miss for any id needing sanitization.
func TestBuildDocSourceSanitizedIDStillScores(t *testing.T) {
	rawSessionID := "sess one: two"
	inst := Instance{
		QuestionID:         "weird question!",
		HaystackSessionIDs: []string{rawSessionID},
		HaystackSessions: [][]Turn{
			{{Role: "user", Content: "hi", HasAnswer: true}},
		},
	}

	src, answers := BuildDocSource(inst)

	var gotPath string
	if err := src(func(path string, content []byte) error {
		gotPath = path
		return nil
	}); err != nil {
		t.Fatalf("DocSource: %v", err)
	}

	wantSlug := CanonicalSegment(rawSessionID)
	if !strings.HasSuffix(gotPath, "/"+wantSlug) {
		t.Fatalf("path = %q, want slug suffix %q", gotPath, wantSlug)
	}

	key := SessionTurn{SessionID: wantSlug, TurnIndex: 1}
	if _, ok := answers[inst.QuestionID][key]; !ok {
		t.Errorf("answer map missing canonical key %+v; got %+v", key, answers[inst.QuestionID])
	}
}

func TestBuildDocSourceCollisionError(t *testing.T) {
	inst := Instance{
		QuestionID:         "collision_q",
		HaystackSessionIDs: []string{"sess#1", "sess@1"}, // both canonicalize to "sess_1"
		HaystackSessions: [][]Turn{
			{{Role: "user", Content: "a"}},
			{{Role: "user", Content: "b"}},
		},
	}

	src, _ := BuildDocSource(inst)
	if err := src(func(path string, content []byte) error { return nil }); err == nil {
		t.Fatal("expected collision error, got nil")
	}
}

func TestBuildDocSourceLengthMismatchError(t *testing.T) {
	inst := Instance{
		QuestionID:         "mismatch_q",
		HaystackSessionIDs: []string{"sess_1", "sess_2"},
		HaystackSessions: [][]Turn{
			{{Role: "user", Content: "only one session provided"}},
		},
	}

	src, _ := BuildDocSource(inst)
	if err := src(func(path string, content []byte) error { return nil }); err == nil {
		t.Fatal("expected length-mismatch error, got nil")
	}
}
