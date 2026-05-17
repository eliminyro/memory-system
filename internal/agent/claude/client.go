package claude

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Client wraps Claude API access. Supports two auth modes:
//   - "api": direct Anthropic API via anthropic-sdk-go (requires API key)
//   - "sdk": Claude Code CLI subprocess (uses subscription auth)
type Client struct {
	mode  string
	inner *anthropic.Client // non-nil for "api" mode
}

// LLM is the surface the pipeline uses. *Client satisfies it via Complete.
type LLM interface {
	Complete(ctx context.Context, model, system, user string) (string, error)
}

var _ LLM = (*Client)(nil)

func NewClient(auth, apiKey string) (*Client, error) {
	switch auth {
	case "api":
		var opts []option.RequestOption
		if apiKey != "" {
			opts = append(opts, option.WithAPIKey(apiKey))
		}
		c := anthropic.NewClient(opts...)
		return &Client{mode: "api", inner: &c}, nil

	case "sdk":
		return &Client{mode: "sdk"}, nil

	default:
		return nil, fmt.Errorf("unsupported auth mode: %q (use \"api\" or \"sdk\")", auth)
	}
}

func (c *Client) Complete(ctx context.Context, model, systemPrompt, userMessage string) (string, error) {
	switch c.mode {
	case "api":
		return c.completeAPI(ctx, model, systemPrompt, userMessage)
	case "sdk":
		return c.completeSDK(ctx, model, systemPrompt, userMessage)
	default:
		return "", fmt.Errorf("unknown mode: %q", c.mode)
	}
}

func (c *Client) completeAPI(ctx context.Context, model, systemPrompt, userMessage string) (string, error) {
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

func (c *Client) completeSDK(ctx context.Context, model, systemPrompt, userMessage string) (string, error) {
	args := []string{
		"-p",
		"--model", model,
		"--output-format", "text",
		"--max-turns", "1",
	}
	if systemPrompt != "" {
		args = append(args, "--system-prompt", systemPrompt)
	}

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Stdin = strings.NewReader(userMessage)
	cmd.Env = append(cmd.Environ(), "MEMORY_AGENT_INVOKED=1")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("claude CLI: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}

	result := strings.TrimSpace(stdout.String())
	if result == "" {
		return "", fmt.Errorf("empty response from claude CLI")
	}
	return result, nil
}
