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

// TestCodeStore_ConsumeOnceAtomic_Postgres redeems the same authorization
// code from many goroutines at once against a real Postgres backend. An
// auth code must yield exactly one token grant: exactly one ConsumeOnce may
// succeed, every other caller must see storage.ErrNotFound. The non-atomic
// check-then-act version (read row, then unconditional Delete) lets two
// concurrent transactions both read the row before either deletes, so both
// return a valid *storage.AuthCode -> the code is redeemed twice.
//
// This race cannot be reproduced on the sqlite harness (single connection
// serializes transactions); it requires Postgres READ COMMITTED.
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
