//go:build integration

package authletstore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/eliminyro/authlet/pkg/storage"
)

// TestRefreshStore_MarkUsedAtomic_Postgres rotates the same refresh token
// from many goroutines at once against a real Postgres backend. Rotation
// must transition the token from active to replaced exactly once: exactly
// one MarkUsed may succeed, every other caller must see
// storage.ErrAlreadyConsumed so the caller triggers the reuse-detection
// revoke path. The non-atomic check-then-act version (read row, check
// ReplacedBy=="" in Go, then Update) lets two concurrent transactions both
// pass the check before either writes, so reuse escapes detection.
//
// This race cannot be reproduced on the sqlite harness (single connection
// serializes transactions); it requires Postgres READ COMMITTED.
func TestRefreshStore_MarkUsedAtomic_Postgres(t *testing.T) {
	db := openTestPG(t)
	s := New(db)
	ctx := context.Background()

	if err := s.RefreshTokens().Save(ctx, storage.RefreshToken{
		TokenHash: "rt", FamilyID: "fam", ClientID: "c", UserID: "u",
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	const n = 32
	warmPool(t, db, n)
	var (
		start  = make(chan struct{})
		wg     sync.WaitGroup
		mu     sync.Mutex
		wins   int
		reuses int
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release all goroutines together
			// Every caller proposes a distinct successor hash; only one may win.
			err := s.RefreshTokens().MarkUsed(ctx, "rt", replacement(i))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, storage.ErrAlreadyConsumed):
				reuses++
			default:
				t.Errorf("unexpected MarkUsed result: err=%v", err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if wins != 1 {
		t.Fatalf("refresh token rotated %d times, want exactly 1 (reuse-detected losers=%d)", wins, reuses)
	}
	if reuses != n-1 {
		t.Fatalf("reuse-detected losers = %d, want %d", reuses, n-1)
	}
}

func replacement(i int) string {
	return "succ-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
}
