package transcript

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Session struct {
	Messages []Message
	Path     string
}

type jsonlEntry struct {
	Type    string `json:"type"`
	Message *struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	} `json:"message,omitempty"`
}

func Parse(path string) (*Session, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open transcript: %w", err)
	}
	defer func() { _ = f.Close() }()

	session := &Session{Path: path}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry jsonlEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			slog.Debug("skipping malformed JSONL line", "error", err)
			continue
		}

		if entry.Type != "human" && entry.Type != "assistant" {
			continue
		}
		if entry.Message == nil {
			continue
		}

		content := extractContent(entry.Message.Content)
		if content == "" {
			continue
		}

		session.Messages = append(session.Messages, Message{
			Role:    entry.Message.Role,
			Content: content,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan transcript: %w", err)
	}

	return session, nil
}

func extractContent(raw any) string {
	switch v := raw.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, block := range v {
			if m, ok := block.(map[string]any); ok {
				if text, ok := m["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return fmt.Sprintf("%v", raw)
	}
}

func (s *Session) Summary() string {
	var b strings.Builder
	for _, m := range s.Messages {
		b.WriteString(m.Role)
		b.WriteString(": ")
		b.WriteString(m.Content)
		b.WriteString("\n\n")
	}
	return b.String()
}
