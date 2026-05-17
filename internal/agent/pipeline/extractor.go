package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/eliminyro/memory-system/internal/agent/claude"
	"github.com/eliminyro/memory-system/internal/agent/transcript"
)

const extractorSystemPrompt = `You are a knowledge extraction agent. Your job is to identify durable, reusable learnings from a Claude Code session transcript.

For each learning, output a JSON object with:
- "path": where it belongs in the knowledge hierarchy. MUST be exactly 3 parts: category/subcategory/slug
  Categories: learnings, preferences, projects
  Subcategories for learnings: go, infrastructure, cicd, observability, tools, homelab, security
  Subcategories for projects: hilo, a1, memory-mcp, claude-hook-engine, homelab, secretctl, ansible, karaoke
  The slug should be a short kebab-case identifier for this specific knowledge (e.g. "learnings/go/gorm-soft-deletes", "projects/hilo/auth-migration")
  NEVER use a 2-part path like "learnings/go" — that would overwrite an entire subcategory
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

const maxChunkRunes = 25_000

func Extract(ctx context.Context, llm claude.LLM, model string, session *transcript.Session) ([]Candidate, error) {
	if len(session.Messages) == 0 {
		return nil, nil
	}

	summary := session.Summary()
	chunks := chunkByRunes(summary, maxChunkRunes)

	slog.Info("extracting candidates", "chunks", len(chunks), "total_runes", len([]rune(summary)))

	var allCandidates []Candidate
	for i, chunk := range chunks {
		prompt := chunk
		if len(chunks) > 1 {
			prompt = fmt.Sprintf("[Chunk %d of %d]\n\n%s", i+1, len(chunks), chunk)
		}

		response, err := llm.Complete(ctx, model, extractorSystemPrompt, prompt)
		if err != nil {
			slog.Error("extractor chunk failed", "chunk", i+1, "error", err)
			continue // non-fatal: try remaining chunks
		}

		candidates, err := ParseExtractorResponse(response)
		if err != nil {
			slog.Error("parse extractor response failed", "chunk", i+1, "error", err)
			continue
		}

		allCandidates = append(allCandidates, candidates...)
	}

	// Deduplicate by path — later chunks win (more recent conversation context)
	return deduplicateCandidates(allCandidates), nil
}

// chunkByRunes splits text into chunks of at most maxRunes runes,
// breaking at the last paragraph boundary ("\n\n") within the limit.
func chunkByRunes(text string, maxRunes int) []string {
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return []string{text}
	}

	var chunks []string
	for len(runes) > 0 {
		end := maxRunes
		if end > len(runes) {
			end = len(runes)
		}

		// Try to break at a paragraph boundary
		chunk := string(runes[:end])
		if end < len(runes) {
			if idx := lastParagraphBreak(chunk); idx > len(chunk)/2 {
				chunk = chunk[:idx]
				end = len([]rune(chunk))
			}
		}

		chunks = append(chunks, chunk)
		runes = runes[end:]
	}

	return chunks
}

func lastParagraphBreak(s string) int {
	// Search backwards for "\n\n"
	for i := len(s) - 1; i > 0; i-- {
		if s[i] == '\n' && s[i-1] == '\n' {
			return i + 1
		}
	}
	return -1
}

// deduplicateCandidates keeps the last candidate for each path.
func deduplicateCandidates(candidates []Candidate) []Candidate {
	seen := make(map[string]int) // path -> index in result
	var result []Candidate

	for _, c := range candidates {
		if idx, ok := seen[c.Path]; ok {
			result[idx] = c // overwrite with later version
		} else {
			seen[c.Path] = len(result)
			result = append(result, c)
		}
	}
	return result
}

func ParseExtractorResponse(raw string) ([]Candidate, error) {
	raw = stripCodeFence(raw)

	var candidates []Candidate
	if err := json.Unmarshal([]byte(raw), &candidates); err != nil {
		return nil, fmt.Errorf("parse extractor response: %w (raw: %.200s)", err, raw)
	}
	return candidates, nil
}
