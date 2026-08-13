package main

import "testing"

// TestCheckDriftNoDriftAtCandidatePoolDepth is the regression case for the
// mirror-depth bug: a session that would be absent from a maxK-deep mirror
// pull (it only ranks in the CTE's 11-20 candidate-pool range) must NOT be
// flagged, as long as the mirrors passed in were pulled at candidatePoolLimit
// depth — CheckDrift itself is depth-agnostic; callers own picking the depth
// (see checkMirrorDrift, which always pulls at candidatePoolLimit).
func TestCheckDriftNoDriftAtCandidatePoolDepth(t *testing.T) {
	hybrid := []RetrievedSection{{SessionID: "sess_1"}, {SessionID: "sess_11"}}
	// Simulates a candidatePoolLimit=20 pull: sess_11 only surfaces this deep,
	// not in a maxK=10 pull.
	vectorPool := []RetrievedSection{{SessionID: "sess_1"}, {SessionID: "sess_11"}}
	lexicalPool := []RetrievedSection{{SessionID: "sess_2"}}

	if got := CheckDrift(hybrid, vectorPool, lexicalPool); len(got) != 0 {
		t.Errorf("expected no drift when mirrors are pulled at candidate-pool depth, got %v", got)
	}
}

func TestCheckDriftDetectsMissingSession(t *testing.T) {
	hybrid := []RetrievedSection{{SessionID: "sess_1"}, {SessionID: "sess_2"}, {SessionID: "sess_1"}}
	vectorOnly := []RetrievedSection{{SessionID: "sess_1"}}
	lexicalOnly := []RetrievedSection{{SessionID: "sess_3"}}

	got := CheckDrift(hybrid, vectorOnly, lexicalOnly)
	if len(got) != 1 || got[0] != "sess_2" {
		t.Errorf("got %v, want [sess_2]", got)
	}
}

func TestCheckDriftEmptyHybrid(t *testing.T) {
	if got := CheckDrift(nil, nil, nil); len(got) != 0 {
		t.Errorf("expected no drift on empty input, got %v", got)
	}
}
