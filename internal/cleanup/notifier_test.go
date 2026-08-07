package cleanup

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// errRoundTripper is an http.RoundTripper that always fails, so client.Do wraps
// its error in a *url.Error carrying the full request URL (token included).
type errRoundTripper struct{ err error }

func (rt errRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, rt.err
}

// TestSend_TransportErrorDoesNotLeakToken proves that a transport failure never
// surfaces the bot token: net/http embeds the full request URL (which holds the
// token in its path) in the *url.Error it returns, and send must unwrap that to
// the underlying cause before it reaches an error string / the scanner WARN log.
func TestSend_TransportErrorDoesNotLeakToken(t *testing.T) {
	const token = "123456:SECRETTOKENVALUE"
	n := &Notifier{
		botToken: token,
		chatID:   "chat-1",
		client:   &http.Client{Transport: errRoundTripper{err: errors.New("simulated transport failure")}},
	}

	// Control: the raw *url.Error from client.Do WOULD embed the token, so this
	// test guards a real leak vector rather than a hypothetical one.
	rawReq, _ := http.NewRequest(http.MethodPost, "https://api.telegram.org/bot"+token+"/sendMessage", nil)
	if _, rawErr := n.client.Do(rawReq); rawErr == nil || !strings.Contains(rawErr.Error(), "SECRETTOKENVALUE") {
		t.Fatalf("precondition: raw client.Do error should embed the token URL, got %v", rawErr)
	}

	err := n.SendScanSummary(context.Background(), ScanStats{})
	if err == nil {
		t.Fatal("expected a transport error, got nil")
	}
	if strings.Contains(err.Error(), "SECRETTOKENVALUE") {
		t.Fatalf("error leaks bot token: %q", err.Error())
	}
	// The error must still convey that the request failed.
	if !strings.Contains(err.Error(), "telegram request failed") {
		t.Errorf("error = %q, want it to convey a telegram request failure", err.Error())
	}
}
