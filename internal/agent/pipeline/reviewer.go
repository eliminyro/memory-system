package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/eliminyro/memory-system/internal/agent/claude"
	"github.com/eliminyro/memory-system/internal/agent/mcpclient"
)

const reviewerSystemPrompt = `You are a knowledge review agent. Your job is to challenge candidate learnings extracted from a Claude Code session.

For each candidate (numbered starting at 0), you are given:
- The candidate itself (path, heading, content, type)
- Search results from the existing knowledge base (if any)

For each candidate, decide:
- "accept": new knowledge not in KB, worth storing
- "merge": overlaps with existing doc — should update existing section instead of creating new
- "reject": duplicate, too ephemeral, too specific to session, or not worth storing

Criteria:
- Is this durable knowledge? Would it be useful 3 months from now?
- Is it already in the KB? (check search results for high similarity)
- Is it too specific to this one session?
- Is it a complete thought or a fragment?

For "merge" verdicts, include the section_id of the target section in "merge_target".

Respond with ONLY a JSON array of verdict objects, one per candidate IN THE SAME ORDER as the input.
Each object has ONLY these fields: "verdict", "reason", and optionally "merge_target".
Do NOT include the original candidate fields (path, heading, content, type) — they are tracked separately.`

func Review(ctx context.Context, llm claude.LLM, model string, mcp *mcpclient.Client, candidates []Candidate) ([]ReviewedCandidate, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	type candidateWithContext struct {
		Candidate     Candidate                `json:"candidate"`
		SearchResults []mcpclient.SearchResult `json:"existing_knowledge"`
	}

	var enriched []candidateWithContext
	for _, c := range candidates {
		results, err := mcp.SearchMemory(ctx, c.Content, nil, nil, 3)
		if err != nil {
			slog.Warn("search failed for candidate, proceeding without context", "path", c.Path, "error", err)
			results = nil
		}
		enriched = append(enriched, candidateWithContext{
			Candidate:     c,
			SearchResults: results,
		})
	}

	input, err := json.MarshalIndent(enriched, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal reviewer input: %w", err)
	}

	response, err := llm.Complete(ctx, model, reviewerSystemPrompt, string(input))
	if err != nil {
		return nil, fmt.Errorf("reviewer: %w", err)
	}

	verdicts, err := ParseReviewerResponse(response)
	if err != nil {
		return nil, err
	}

	// Zip verdicts back onto original candidates so path/heading/content
	// are guaranteed correct (not dependent on LLM echoing them back).
	if len(verdicts) != len(candidates) {
		return nil, fmt.Errorf("reviewer returned %d verdicts for %d candidates", len(verdicts), len(candidates))
	}
	var reviewed []ReviewedCandidate
	for i, v := range verdicts {
		reviewed = append(reviewed, ReviewedCandidate{
			Candidate:   candidates[i],
			Verdict:     v.Verdict,
			Reason:      v.Reason,
			MergeTarget: v.MergeTarget,
		})
	}
	return reviewed, nil
}

// reviewVerdict holds only the reviewer's decision, not the original candidate fields.
type reviewVerdict struct {
	Verdict     Verdict `json:"verdict"`
	Reason      string  `json:"reason"`
	MergeTarget string  `json:"merge_target,omitempty"`
}

func ParseReviewerResponse(raw string) ([]reviewVerdict, error) {
	raw = stripCodeFence(raw)

	var verdicts []reviewVerdict
	if err := json.Unmarshal([]byte(raw), &verdicts); err != nil {
		return nil, fmt.Errorf("parse reviewer response: %w (raw: %.200s)", err, raw)
	}
	return verdicts, nil
}
