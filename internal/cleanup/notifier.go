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

// Notifier POSTs a cleanup-scan JSON summary to the webhook URL from the live
// config. An empty URL disables it: no request is made.
type Notifier struct {
	gc     GlobalConfig
	client *http.Client
}

// NewNotifier returns a Notifier that reads its target webhook URL live from gc.
func NewNotifier(gc GlobalConfig) *Notifier {
	return &Notifier{gc: gc, client: &http.Client{Timeout: 10 * time.Second}}
}

// scanSummary is the JSON body POSTed to the webhook on each completed scan.
type scanSummary struct {
	Event          string    `json:"event"`
	Timestamp      time.Time `json:"timestamp"`
	TenantsScanned int       `json:"tenants_scanned"`
	PairsFound     int       `json:"pairs_found"`
	PairsInserted  int       `json:"pairs_inserted"`
	PairsSkipped   int       `json:"pairs_skipped"`
	Errors         int       `json:"errors"`
	HistoryPruned  int       `json:"history_pruned"`
}

// SendScanSummary POSTs a JSON summary of one sweep; a no-op when no URL is set.
func (n *Notifier) SendScanSummary(ctx context.Context, stats ScanStats) error {
	target := n.gc.WebhookURL()
	if target == "" {
		return nil
	}
	body, err := json.Marshal(scanSummary{
		Event:          "cleanup_scan",
		Timestamp:      time.Now().UTC(),
		TenantsScanned: stats.TenantsScanned,
		PairsFound:     stats.PairsFound,
		PairsInserted:  stats.PairsInserted,
		PairsSkipped:   stats.PairsSkipped,
		Errors:         stats.Errors,
		HistoryPruned:  stats.HistoryPruned,
	})
	if err != nil {
		return fmt.Errorf("marshal webhook payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		// *url.Error embeds the full request URL, which may carry a token; return
		// only the underlying cause so a configured secret never reaches a log.
		var ue *url.Error
		if errors.As(err, &ue) {
			return fmt.Errorf("webhook request failed: %w", ue.Err)
		}
		return fmt.Errorf("webhook request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("webhook returned %d: %s", resp.StatusCode, string(snippet))
	}
	return nil
}
