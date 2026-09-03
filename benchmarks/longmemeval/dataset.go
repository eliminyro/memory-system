package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// Turn is one message in a haystack session. HasAnswer marks a turn as
// containing the gold evidence for its question (absent JSON field = false).
type Turn struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	HasAnswer bool   `json:"has_answer,omitempty"`
}

// Instance is one LongMemEval question plus its haystack. HaystackSessionIDs,
// HaystackDates, and HaystackSessions are index-aligned (same session order).
type Instance struct {
	QuestionID   string `json:"question_id"`
	QuestionType string `json:"question_type"`
	Question     string `json:"question"`
	// Answer is unused for retrieval scoring; typed as RawMessage because the
	// dataset's answer is polymorphic (string OR number OR list).
	Answer             json.RawMessage `json:"answer"`
	QuestionDate       string          `json:"question_date"`
	HaystackSessionIDs []string        `json:"haystack_session_ids"`
	HaystackDates      []string        `json:"haystack_dates"`
	HaystackSessions   [][]Turn        `json:"haystack_sessions"`
	AnswerSessionIDs   []string        `json:"answer_session_ids"`
}

// EffectiveType returns "abstention" for questions whose id ends in "_abs"
// (no answerable evidence in the haystack by design), else QuestionType.
func EffectiveType(inst Instance) string {
	if strings.HasSuffix(inst.QuestionID, "_abs") {
		return "abstention"
	}
	return inst.QuestionType
}

// LoadDataset streams a JSON array of Instances from path, which is either a
// local file path or an http(s):// URL — the harness fetches the dataset
// itself so the in-cluster distroless image (no shell/wget) can run it.
func LoadDataset(path string) ([]Instance, error) {
	var r io.Reader
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		resp, err := http.Get(path) //nolint:gosec // dataset URL is an operator-supplied benchmark flag
		if err != nil {
			return nil, fmt.Errorf("fetch dataset %s: %w", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("fetch dataset %s: status %s", path, resp.Status)
		}
		r = resp.Body
	} else {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open dataset %s: %w", path, err)
		}
		defer func() { _ = f.Close() }()
		r = f
	}

	var instances []Instance
	if err := json.NewDecoder(r).Decode(&instances); err != nil {
		return nil, fmt.Errorf("decode dataset %s: %w", path, err)
	}
	return instances, nil
}
