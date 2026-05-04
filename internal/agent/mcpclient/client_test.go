package mcpclient_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/eliminyro/memory-system/internal/agent/mcpclient"
)

func TestClient_SearchMemory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))

		var req map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "tools/call", req["method"])

		params := req["params"].(map[string]any)
		assert.Equal(t, "search_memory", params["name"])

		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req["id"],
			"result": map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": `[{"section_id":"abc","content":"existing knowledge","score":0.85}]`},
				},
			},
		}
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer srv.Close()

	client := mcpclient.New(srv.URL, "test-key")
	results, err := client.SearchMemory(context.Background(), "test query", nil, nil, 5)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Contains(t, results[0].Content, "existing knowledge")
}

func TestClient_StoreMemory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		params := req["params"].(map[string]any)
		assert.Equal(t, "store_memory", params["name"])

		args := params["arguments"].(map[string]any)
		assert.Equal(t, "learnings", args["category"])
		assert.Equal(t, "gorm", args["slug"])

		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req["id"],
			"result": map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": `{"id":"doc-123","path":"learnings/go/gorm","sections":2}`},
				},
			},
		}
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer srv.Close()

	client := mcpclient.New(srv.URL, "test-key")
	sub := "go"
	err := client.StoreMemory(context.Background(), "learnings", &sub, "gorm", "# GORM\n\n## Patterns\n\nContent here")
	require.NoError(t, err)
}
