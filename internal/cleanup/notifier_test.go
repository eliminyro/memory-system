package cleanup

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeConfig satisfies GlobalConfig for the notifier tests; only WebhookURL varies.
type fakeConfig struct{ webhookURL string }

func (f fakeConfig) CleanupEnabled() bool        { return true }
func (f fakeConfig) CleanupIntervalHours() int   { return 24 }
func (f fakeConfig) HistoryRetentionDays() int   { return 90 }
func (f fakeConfig) RetentionSweepEnabled() bool { return false }
func (f fakeConfig) RetentionGraceDays() int     { return 30 }
func (f fakeConfig) MetricsRetentionDays() int   { return 90 }
func (f fakeConfig) WebhookURL() string          { return f.webhookURL }

// rtFunc adapts a func to http.RoundTripper.
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestSendScanSummary_EmptyURLDisables: an empty webhook URL makes no request.
func TestSendScanSummary_EmptyURLDisables(t *testing.T) {
	called := false
	n := &Notifier{
		gc: fakeConfig{webhookURL: ""},
		client: &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
			called = true
			return nil, errors.New("must not be called")
		})},
	}
	if err := n.SendScanSummary(context.Background(), ScanStats{}); err != nil {
		t.Fatalf("empty URL should be a no-op, got %v", err)
	}
	if called {
		t.Fatal("empty URL must not make a request")
	}
}

// TestSendScanSummary_PostsJSON: a configured URL receives the summary as JSON.
func TestSendScanSummary_PostsJSON(t *testing.T) {
	var got scanSummary
	var method, contentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		contentType = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier(fakeConfig{webhookURL: srv.URL})
	stats := ScanStats{TenantsScanned: 3, PairsFound: 5, PairsInserted: 4, PairsSkipped: 1, HistoryPruned: 2}
	if err := n.SendScanSummary(context.Background(), stats); err != nil {
		t.Fatalf("send: %v", err)
	}
	if method != http.MethodPost {
		t.Errorf("method = %q, want POST", method)
	}
	if contentType != "application/json" {
		t.Errorf("content-type = %q, want application/json", contentType)
	}
	if got.Event != "cleanup_scan" || got.Timestamp.IsZero() {
		t.Errorf("event/timestamp missing: %+v", got)
	}
	if got.TenantsScanned != 3 || got.PairsFound != 5 || got.PairsInserted != 4 || got.PairsSkipped != 1 || got.HistoryPruned != 2 {
		t.Errorf("summary body mismatch: %+v", got)
	}
}

// TestSendScanSummary_TransportErrorDoesNotLeakURL proves a transport failure
// never surfaces a secret embedded in the webhook URL: net/http returns a
// *url.Error carrying the full URL, and send must unwrap it before it logs.
func TestSendScanSummary_TransportErrorDoesNotLeakURL(t *testing.T) {
	const secret = "SECRETWEBHOOKTOKEN"
	target := "https://example.com/hook/" + secret
	n := &Notifier{
		gc: fakeConfig{webhookURL: target},
		client: &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("simulated transport failure")
		})},
	}

	// Control: the raw *url.Error from client.Do WOULD embed the secret URL.
	rawReq, _ := http.NewRequest(http.MethodPost, target, nil)
	if _, rawErr := n.client.Do(rawReq); rawErr == nil || !strings.Contains(rawErr.Error(), secret) {
		t.Fatalf("precondition: raw client.Do error should embed the URL, got %v", rawErr)
	}

	err := n.SendScanSummary(context.Background(), ScanStats{})
	if err == nil {
		t.Fatal("expected a transport error, got nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaks webhook secret: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "webhook request failed") {
		t.Errorf("error = %q, want it to convey a webhook request failure", err.Error())
	}
}
