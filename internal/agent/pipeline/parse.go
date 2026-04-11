package pipeline

import "strings"

// stripCodeFence removes markdown code fences from LLM responses.
func stripCodeFence(raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "```") {
		return raw
	}
	lines := strings.Split(raw, "\n")
	if len(lines) <= 2 {
		return raw
	}
	return strings.Join(lines[1:len(lines)-1], "\n")
}
