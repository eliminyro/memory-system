package service

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/eliminyro/memory-system/internal/repository"
)

// TestNewSearchResponse_Envelope is the golden-shape test for design D2: the
// envelope always carries recall_id + results keys, recall_id is JSON null
// (not the zero UUID string) when no receipt was issued, and results is [],
// never null, on an empty result set.
func TestNewSearchResponse_Envelope(t *testing.T) {
	t.Run("no receipt -> recall_id null, results []", func(t *testing.T) {
		resp := NewSearchResponse(nil, uuid.Nil)
		data, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if _, ok := raw["recall_id"]; !ok {
			t.Fatal("envelope missing recall_id key")
		}
		if string(raw["recall_id"]) != "null" {
			t.Errorf("recall_id = %s, want literal null", raw["recall_id"])
		}
		if string(raw["results"]) != "[]" {
			t.Errorf("results = %s, want []", raw["results"])
		}
	})

	t.Run("receipt issued -> recall_id is the id", func(t *testing.T) {
		id := uuid.New()
		results := []repository.SearchResult{{SectionID: uuid.New()}}
		resp := NewSearchResponse(results, id)
		data, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		want := `"` + id.String() + `"`
		if string(raw["recall_id"]) != want {
			t.Errorf("recall_id = %s, want %s", raw["recall_id"], want)
		}
		var got []repository.SearchResult
		if err := json.Unmarshal(raw["results"], &got); err != nil {
			t.Fatalf("results not an array: %v", err)
		}
		if len(got) != 1 {
			t.Errorf("results len = %d, want 1", len(got))
		}
	})
}
