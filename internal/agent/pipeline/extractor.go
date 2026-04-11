package pipeline

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/eliminyro/memory-system/internal/agent/claude"
	"github.com/eliminyro/memory-system/internal/agent/transcript"
)

const extractorSystemPrompt = `You are a knowledge extraction agent. Your job is to identify durable, reusable learnings from a Claude Code session transcript.

For each learning, output a JSON object with:
- "path": where it belongs in the knowledge hierarchy (category/subcategory/slug format)
  Categories: learnings, preferences, projects
  Subcategories for learnings: go, infrastructure, cicd, observability, tools, homelab
- "heading": a descriptive heading for the knowledge
- "content": the actual knowledge, written as a durable fact (NOT "we discussed X" but "X works by doing Y")
- "type": "new" (create new doc) or "update" (merge into existing doc)

Rules:
- Extract 0-7 candidates per session. Zero is valid — not every session produces learnings.
- Only extract durable knowledge that would be useful months from now.
- Skip ephemeral conversation, debugging steps, and session-specific details.
- Write content as standalone facts, not as references to the conversation.
- Use clear, concise language.

Respond with ONLY a JSON array of candidates. No other text.`

func Extract(ctx context.Context, client *claude.Client, model string, session *transcript.Session) ([]Candidate, error) {
	if len(session.Messages) == 0 {
		return nil, nil
	}

	summary := session.Summary()
	if len(summary) > 100_000 {
		// Truncate at rune boundary
		truncated := []rune(summary)
		if len(truncated) > 25_000 {
			summary = string(truncated[:25_000]) + "\n\n[TRUNCATED]"
		}
	}

	response, err := client.Complete(ctx, model, extractorSystemPrompt, summary)
	if err != nil {
		return nil, fmt.Errorf("extractor: %w", err)
	}

	return ParseExtractorResponse(response)
}

func ParseExtractorResponse(raw string) ([]Candidate, error) {
	raw = stripCodeFence(raw)

	var candidates []Candidate
	if err := json.Unmarshal([]byte(raw), &candidates); err != nil {
		return nil, fmt.Errorf("parse extractor response: %w (raw: %.200s)", err, raw)
	}
	return candidates, nil
}
