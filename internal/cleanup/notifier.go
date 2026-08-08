package cleanup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Notifier posts cleanup summaries to a Telegram chat. Both token and chat ID
// must be set; if either is empty, pass nil (Scanner treats nil as silent).
type Notifier struct {
	botToken string
	chatID   string
	client   *http.Client
}

// NewNotifier returns a Notifier, or nil if either credential is empty (pass the
// nil to Scanner unchanged — it treats nil as silent mode).
func NewNotifier(botToken, chatID string) *Notifier {
	if botToken == "" || chatID == "" {
		return nil
	}
	return &Notifier{
		botToken: botToken,
		chatID:   chatID,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

// SendScanSummary posts a human-readable summary of one scan sweep.
func (n *Notifier) SendScanSummary(ctx context.Context, stats ScanStats) error {
	text := fmt.Sprintf(
		"memory-mcp cleanup scan\n"+
			"tenants: %d\n"+
			"pairs found: %d\n"+
			"newly enqueued: %d\n"+
			"already pending: %d\n"+
			"docs archived: %d\n"+
			"docs deleted: %d\n"+
			"errors: %d",
		stats.TenantsScanned, stats.PairsFound, stats.PairsInserted, stats.PairsSkipped, stats.DocsArchived, stats.DocsDeleted, stats.Errors,
	)
	return n.send(ctx, text)
}

func (n *Notifier) send(ctx context.Context, text string) error {
	endpoint := "https://api.telegram.org/bot" + n.botToken + "/sendMessage"
	payload := map[string]string{
		"chat_id": n.chatID,
		"text":    text,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal telegram payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build telegram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		// net/http returns *url.Error here, whose Error() embeds the full request
		// URL — which contains the bot token. Return only the underlying cause so
		// the token never reaches an error string / the scanner's WARN log.
		var ue *url.Error
		if errors.As(err, &ue) {
			return fmt.Errorf("telegram request failed: %w", ue.Err)
		}
		return fmt.Errorf("telegram request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("telegram returned %d: %s", resp.StatusCode, string(snippet))
	}
	return nil
}
