package authletstore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/eliminyro/authlet/pkg/storage"
)

func TestCodeStore_SaveAndConsumeSuccess(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()

	exp := time.Now().Add(time.Minute).UTC().Truncate(time.Second)
	if err := s.Codes().Save(ctx, storage.AuthCode{
		CodeHash: "h0", ClientID: "c", UserID: "u", Resource: "r", Scope: "s",
		PKCEChallenge: "ch", PKCEMethod: "S256", RedirectURI: "https://x/cb",
		ExpiresAt: exp,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.Codes().ConsumeOnce(ctx, "h0")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.CodeHash != "h0" || got.ClientID != "c" || got.PKCEChallenge != "ch" {
		t.Fatalf("got %+v", got)
	}

	// Second consume returns ErrNotFound (memstore semantics).
	if _, err := s.Codes().ConsumeOnce(ctx, "h0"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound on replay, got %v", err)
	}
}

func TestCodeStore_ConsumeOnceAtomic(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()
	if err := s.Codes().Save(ctx, storage.AuthCode{CodeHash: "h1", ExpiresAt: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wins := 0
	var mu sync.Mutex
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Codes().ConsumeOnce(ctx, "h1"); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("expected 1 winner, got %d", wins)
	}
}

func TestCodeStore_NotFound(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()
	if _, err := s.Codes().ConsumeOnce(ctx, "nope"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestCodeStore_ExpiredRejected(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()
	if err := s.Codes().Save(ctx, storage.AuthCode{CodeHash: "h2", ExpiresAt: time.Now().Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	_, err := s.Codes().ConsumeOnce(ctx, "h2")
	if !errors.Is(err, storage.ErrAlreadyConsumed) {
		t.Fatalf("got %v", err)
	}
}
