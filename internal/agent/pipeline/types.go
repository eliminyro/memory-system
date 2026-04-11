package pipeline

import "strings"

type Candidate struct {
	Path    string `json:"path"`
	Heading string `json:"heading"`
	Content string `json:"content"`
	Type    string `json:"type"`
}

type Verdict string

const (
	VerdictAccept Verdict = "accept"
	VerdictMerge  Verdict = "merge"
	VerdictReject Verdict = "reject"
)

type ReviewedCandidate struct {
	Candidate
	Verdict     Verdict `json:"verdict"`
	Reason      string  `json:"reason"`
	MergeTarget string  `json:"merge_target,omitempty"`
}

func ParsePath(path string) (category string, subcategory *string, slug string) {
	parts := splitPath(path)
	switch len(parts) {
	case 3:
		return parts[0], &parts[1], parts[2]
	case 2:
		return parts[0], nil, parts[1]
	default:
		return "misc", nil, path
	}
}

func splitPath(path string) []string {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}
