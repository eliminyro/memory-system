package pipeline

// Candidate represents a knowledge item extracted from a session.
type Candidate struct {
	Path    string `json:"path"`
	Heading string `json:"heading"`
	Content string `json:"content"`
	Type    string `json:"type"`
}

// Verdict is the reviewer's decision on a candidate.
type Verdict string

const (
	VerdictAccept Verdict = "accept"
	VerdictMerge  Verdict = "merge"
	VerdictReject Verdict = "reject"
)

// ReviewedCandidate is a candidate with its review verdict.
type ReviewedCandidate struct {
	Candidate
	Verdict     Verdict `json:"verdict"`
	Reason      string  `json:"reason"`
	MergeTarget string  `json:"merge_target,omitempty"`
}
