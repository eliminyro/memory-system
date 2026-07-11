package authz

import (
	"context"
	"strings"
	"sync"
)

// Store is the tuple persistence contract the Check evaluator depends on. It is
// deliberately small: Check only needs the two read paths, while Write/Delete
// exist for the out-of-band tuple writers wired later. Writes are idempotent —
// writing an already-present tuple is a no-op, never an error.
type Store interface {
	// Write persists t. If an identical tuple already exists it is a no-op.
	Write(ctx context.Context, t Tuple) error
	// Delete removes t. Deleting an absent tuple is a no-op.
	Delete(ctx context.Context, t Tuple) error
	// ReadByObjectRelation returns every tuple on (objType, objID, relation),
	// including wildcard and userset subjects.
	ReadByObjectRelation(ctx context.Context, objType, objID, relation string) ([]Tuple, error)
	// ReadBySubject returns every tuple whose subject is (subjType, subjID),
	// regardless of subject relation.
	ReadBySubject(ctx context.Context, subjType, subjID string) ([]Tuple, error)
}

// tupleKey returns a collision-free canonical key for a tuple. The NUL byte is
// used as the field separator because it cannot appear in the textual object /
// subject identifiers used by the model.
func tupleKey(t Tuple) string {
	return strings.Join([]string{
		t.ObjectType, t.ObjectID, t.Relation,
		t.SubjectType, t.SubjectID, t.SubjectRelation,
	}, "\x00")
}

// MemoryStore is an in-process, goroutine-safe Store used for fast pure-Check
// unit tests with no database. It is not intended for production use.
type MemoryStore struct {
	mu     sync.RWMutex
	tuples map[string]Tuple
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{tuples: make(map[string]Tuple)}
}

var _ Store = (*MemoryStore)(nil)

func (s *MemoryStore) Write(_ context.Context, t Tuple) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tuples[tupleKey(t)] = t
	return nil
}

func (s *MemoryStore) Delete(_ context.Context, t Tuple) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tuples, tupleKey(t))
	return nil
}

func (s *MemoryStore) ReadByObjectRelation(_ context.Context, objType, objID, relation string) ([]Tuple, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Tuple
	for _, t := range s.tuples {
		if t.ObjectType == objType && t.ObjectID == objID && t.Relation == relation {
			out = append(out, t)
		}
	}
	return out, nil
}

func (s *MemoryStore) ReadBySubject(_ context.Context, subjType, subjID string) ([]Tuple, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Tuple
	for _, t := range s.tuples {
		if t.SubjectType == subjType && t.SubjectID == subjID {
			out = append(out, t)
		}
	}
	return out, nil
}
