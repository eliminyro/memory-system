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

// TestCodeStore_ConsumeOnceAtomic_Postgres redeems one auth code from many
// goroutines against real Postgres: exactly one ConsumeOnce may win, the rest
// see storage.ErrNotFound. Non-atomic read-then-Delete would let two txns both
// read and redeem twice. Needs Postgres READ COMMITTED (sqlite serializes txns).
func TestCodeStore_ConsumeOnceAtomic_Postgres(t *testing.T) {
	db := openTestPG(t)
	s := New(db)
	ctx := context.Background()

	if err := s.Codes().Save(ctx, storage.AuthCode{
		CodeHash: "race", ClientID: "c", UserID: "u",
		ExpiresAt: time.Now().Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	const n = 32
	warmPool(t, db, n)
	var (
		start = make(chan struct{})
		wg    sync.WaitGroup
		mu    sync.Mutex
		wins  int
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release all goroutines together
			ac, err := s.Codes().ConsumeOnce(ctx, "race")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil && ac != nil:
				wins++
			case errors.Is(err, storage.ErrNotFound):
				// expected loser
			default:
				t.Errorf("unexpected ConsumeOnce result: ac=%+v err=%v", ac, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if wins != 1 {
		t.Fatalf("auth code redeemed %d times, want exactly 1", wins)
	}
}
