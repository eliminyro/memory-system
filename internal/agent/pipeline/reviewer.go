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

For each candidate, you are given:
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

Respond with ONLY a JSON array of reviewed candidates. Each object has the original fields plus "verdict", "reason", and optionally "merge_target".`

func Review(ctx context.Context, client *claude.Client, model string, mcp *mcpclient.Client, candidates []Candidate) ([]ReviewedCandidate, error) {
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

	response, err := client.Complete(ctx, model, reviewerSystemPrompt, string(input))
	if err != nil {
		return nil, fmt.Errorf("reviewer: %w", err)
	}

	return ParseReviewerResponse(response)
}

func ParseReviewerResponse(raw string) ([]ReviewedCandidate, error) {
	raw = stripCodeFence(raw)

	var reviewed []ReviewedCandidate
	if err := json.Unmarshal([]byte(raw), &reviewed); err != nil {
		return nil, fmt.Errorf("parse reviewer response: %w (raw: %.200s)", err, raw)
	}
	return reviewed, nil
}
