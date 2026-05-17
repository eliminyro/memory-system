package e2etest

import (
	"encoding/json"
	"strings"

	"github.com/eliminyro/memory-system/internal/agent/transcript"
)

// SessionWithText returns a transcript.Session containing a single user
// message with the given body. Long bodies (>25k runes) exercise the
// extractor's chunking path.
func SessionWithText(body string) *transcript.Session {
	return &transcript.Session{
		Messages: []transcript.Message{
			{Role: "user", Content: body},
		},
	}
}

// EmptySession is a session with no messages — exercises the extractor's
// "0 messages, short-circuit" path.
func EmptySession() *transcript.Session {
	return &transcript.Session{}
}

// LongSession returns a session whose body crosses the chunk boundary
// (default maxChunkRunes = 25_000). The body is padded with a unique marker
// near the chunk-2 region so matchers can target a specific chunk.
func LongSession(uniqueMarker string) *transcript.Session {
	// ~30k runes of filler with the marker embedded near the end.
	filler := strings.Repeat("Lorem ipsum dolor sit amet. ", 1100) // ~30k chars
	body := filler[:25_500] + " " + uniqueMarker + " " + filler[:5_000]
	return SessionWithText(body)
}

// ExtractorJSON returns the raw JSON string the FakeLLM should emit for an
// extractor Complete call. Pass any number of candidate objects.
func ExtractorJSON(candidates ...map[string]string) string {
	out, _ := json.Marshal(candidates)
	return string(out)
}

// ReviewerJSON returns the raw JSON string the FakeLLM should emit for a
// reviewer Complete call. Each verdict map should contain "verdict" and
// optionally "reason" and "merge_target".
func ReviewerJSON(verdicts ...map[string]string) string {
	out, _ := json.Marshal(verdicts)
	return string(out)
}

// Candidate builds a candidate map for ExtractorJSON.
func Candidate(path, heading, content, ctype string) map[string]string {
	return map[string]string{
		"path":    path,
		"heading": heading,
		"content": content,
		"type":    ctype,
	}
}

// Accept builds an accept verdict map for ReviewerJSON.
func Accept(reason string) map[string]string {
	return map[string]string{"verdict": "accept", "reason": reason}
}

// Merge builds a merge verdict map. mergeTarget should be a section UUID.
func Merge(reason, mergeTarget string) map[string]string {
	return map[string]string{"verdict": "merge", "reason": reason, "merge_target": mergeTarget}
}

// Reject builds a reject verdict map.
func Reject(reason string) map[string]string {
	return map[string]string{"verdict": "reject", "reason": reason}
}
