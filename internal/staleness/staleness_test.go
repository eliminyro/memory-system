package staleness

import (
	"testing"
)

func TestMentionsCodePath(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"plain prose", "the user wants better memory hygiene", false},
		{"file extension", "see handler.go for details", true},
		{"internal path", "logic lives in internal/service/memory.go", true},
		{"src path", "frontend is in src/components/Auth.tsx", true},
		{"file with line", "bug at foo.go:142 needs a look", true},
		{"just a dot", "version 1.2.3 is out", false},
		{"yaml config", "configured via config.yaml", true},
		{"terraform", "module in ops/terraform/main.tf", true},
		{"mixed", "fix in cmd/server/main.go around line 65", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MentionsCodePath(tc.content)
			if got != tc.want {
				t.Errorf("MentionsCodePath(%q) = %v, want %v", tc.content, got, tc.want)
			}
		})
	}
}

func TestExtractVerifyHints(t *testing.T) {
	content := "the fix is in internal/service/memory.go and also internal/service/memory.go (duplicate) and cmd/server/main.go"
	got := ExtractVerifyHints(content, 5)
	if len(got) != 2 {
		t.Fatalf("expected 2 unique hints, got %d: %v", len(got), got)
	}
	if got[0] != "internal/service/memory.go" {
		t.Errorf("first hint = %q, want internal/service/memory.go", got[0])
	}
}

func TestExtractVerifyHints_RespectsMax(t *testing.T) {
	content := "foo.go bar.ts baz.py qux.rs quux.java corge.kt"
	got := ExtractVerifyHints(content, 3)
	if len(got) != 3 {
		t.Errorf("expected 3 hints (max), got %d: %v", len(got), got)
	}
}

func TestPreview(t *testing.T) {
	short := "short content"
	if got := Preview(short, 200); got != short {
		t.Errorf("Preview(short) = %q, want %q", got, short)
	}

	long := make([]byte, 300)
	for i := range long {
		long[i] = 'a'
	}
	got := Preview(string(long), 200)
	// "…" is 3 bytes in UTF-8
	if len(got) != 200+len("…") {
		t.Errorf("Preview byte length = %d, want %d (200 + ellipsis)", len(got), 200+len("…"))
	}
	if got[:200] != string(long)[:200] {
		t.Errorf("Preview did not preserve first 200 chars")
	}
}
