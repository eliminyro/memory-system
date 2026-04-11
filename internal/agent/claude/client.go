package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

type Client struct {
	inner anthropic.Client
}

func NewClient(auth, apiKey string) (*Client, error) {
	var opts []option.RequestOption

	switch auth {
	case "api":
		if apiKey != "" {
			opts = append(opts, option.WithAPIKey(apiKey))
		}
	case "sdk":
		token, err := readSubscriptionToken()
		if err != nil {
			return nil, fmt.Errorf("read subscription token: %w", err)
		}
		opts = append(opts, option.WithAuthToken(token))
	default:
		return nil, fmt.Errorf("unsupported auth mode: %q (use \"api\" or \"sdk\")", auth)
	}

	return &Client{inner: anthropic.NewClient(opts...)}, nil
}

func (c *Client) Complete(ctx context.Context, model, systemPrompt, userMessage string) (string, error) {
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: 8192,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(userMessage)),
		},
	}
	if systemPrompt != "" {
		params.System = []anthropic.TextBlockParam{
			{Text: systemPrompt},
		}
	}

	msg, err := c.inner.Messages.New(ctx, params)
	if err != nil {
		return "", fmt.Errorf("claude API call: %w", err)
	}

	if len(msg.Content) == 0 {
		return "", fmt.Errorf("empty response from Claude")
	}

	return msg.Content[0].Text, nil
}

type credentialsFile struct {
	ClaudeAIOAuth *struct {
		AccessToken string `json:"accessToken"`
		ExpiresAt   int64  `json:"expiresAt"`
	} `json:"claudeAiOauth"`
}

func readSubscriptionToken() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, ".claude", ".credentials.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}

	var creds credentialsFile
	if err := json.Unmarshal(data, &creds); err != nil {
		return "", fmt.Errorf("parse credentials: %w", err)
	}
	if creds.ClaudeAIOAuth == nil || creds.ClaudeAIOAuth.AccessToken == "" {
		return "", fmt.Errorf("no OAuth token found in credentials")
	}

	if creds.ClaudeAIOAuth.ExpiresAt > 0 && time.Now().UnixMilli() > creds.ClaudeAIOAuth.ExpiresAt {
		return "", fmt.Errorf("subscription token expired at %s — re-authenticate with Claude Code",
			time.UnixMilli(creds.ClaudeAIOAuth.ExpiresAt).Format(time.RFC3339))
	}

	return creds.ClaudeAIOAuth.AccessToken, nil
}
