package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
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
	out, err := exec.Command("security", "find-generic-password",
		"-s", "Claude Code-credentials", "-w").Output()
	if err != nil {
		return "", fmt.Errorf("read keychain (Claude Code-credentials): %w", err)
	}

	var creds credentialsFile
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &creds); err != nil {
		return "", fmt.Errorf("parse keychain credentials: %w", err)
	}
	if creds.ClaudeAIOAuth == nil || creds.ClaudeAIOAuth.AccessToken == "" {
		return "", fmt.Errorf("no OAuth token found in keychain")
	}

	if creds.ClaudeAIOAuth.ExpiresAt > 0 && time.Now().UnixMilli() > creds.ClaudeAIOAuth.ExpiresAt {
		return "", fmt.Errorf("subscription token expired at %s — re-authenticate with Claude Code",
			time.UnixMilli(creds.ClaudeAIOAuth.ExpiresAt).Format(time.RFC3339))
	}

	return creds.ClaudeAIOAuth.AccessToken, nil
}
