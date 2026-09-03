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

// TestRefreshStore_MarkUsedAtomic_Postgres rotates one refresh token from many
// goroutines against real Postgres: exactly one MarkUsed may win, the rest must
// see storage.ErrAlreadyConsumed (triggering reuse-detection revoke). Non-atomic
// read-check-Update would let two txns both pass the check, so reuse escapes
// detection. Needs Postgres READ COMMITTED (sqlite serializes txns).
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
