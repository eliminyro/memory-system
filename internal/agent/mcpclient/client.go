package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

type Client struct {
	url    string
	apiKey string
	http   *http.Client
	nextID atomic.Int64
}

func New(url, apiKey string) *Client {
	return &Client{
		url:    url,
		apiKey: apiKey,
		http:   &http.Client{Timeout: 30 * time.Second},
	}
}

type SearchResult struct {
	SectionID string  `json:"section_id"`
	Content   string  `json:"content"`
	Score     float64 `json:"score"`
	Category  string  `json:"category"`
	DocTitle  string  `json:"doc_title"`
}

func (c *Client) SearchMemory(ctx context.Context, query string, category, subcategory *string, limit int) ([]SearchResult, error) {
	args := map[string]any{"query": query, "limit": limit}
	if category != nil {
		args["category"] = *category
	}
	if subcategory != nil {
		args["subcategory"] = *subcategory
	}

	text, err := c.callTool(ctx, "search_memory", args)
	if err != nil {
		return nil, err
	}

	var results []SearchResult
	if err := json.Unmarshal([]byte(text), &results); err != nil {
		return nil, fmt.Errorf("parse search results: %w", err)
	}
	return results, nil
}

func (c *Client) StoreMemory(ctx context.Context, category string, subcategory *string, slug, content string) error {
	args := map[string]any{
		"category": category,
		"slug":     slug,
		"content":  content,
	}
	if subcategory != nil {
		args["subcategory"] = *subcategory
	}

	_, err := c.callTool(ctx, "store_memory", args)
	return err
}

func (c *Client) UpdateSection(ctx context.Context, sectionID, content string) error {
	args := map[string]any{
		"section_id": sectionID,
		"content":    content,
	}
	_, err := c.callTool(ctx, "update_section", args)
	return err
}

func (c *Client) callTool(ctx context.Context, toolName string, arguments map[string]any) (string, error) {
	id := c.nextID.Add(1)

	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"method":  "tools/call",
		"params": map[string]any{
			"name":      toolName,
			"arguments": arguments,
		},
		"id": id,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("MCP request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("MCP error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var rpcResp struct {
		Result *struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return "", fmt.Errorf("parse MCP response: %w", err)
	}
	if rpcResp.Error != nil {
		return "", fmt.Errorf("MCP tool error: %s", rpcResp.Error.Message)
	}
	if rpcResp.Result == nil || len(rpcResp.Result.Content) == 0 {
		return "", fmt.Errorf("empty MCP response")
	}
	if rpcResp.Result.IsError {
		return "", fmt.Errorf("MCP tool returned error: %s", rpcResp.Result.Content[0].Text)
	}

	return rpcResp.Result.Content[0].Text, nil
}
