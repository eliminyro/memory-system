package e2etest

import (
	"context"
	"errors"
	"sync"

	"github.com/eliminyro/memory-system/internal/agent/claude"
)

// FakeResponse is one entry queued onto a FakeLLM. Match optionally gates
// whether this entry is allowed to satisfy an incoming Complete call.
type FakeResponse struct {
	// Match, if non-nil, must return true for this entry to be consumed.
	// nil means "match anything." Entries are searched in FIFO order; the
	// first matching entry wins and is removed from the queue.
	Match func(system, user string) bool
	Out   string
	Err   error
}

// FakeCall is a captured invocation, available via Calls() for assertions.
type FakeCall struct {
	Model  string
	System string
	User   string
}

// FakeLLM implements claude.LLM with a queue of pre-loaded responses.
// Thread-safe; tests don't need to coordinate goroutines.
type FakeLLM struct {
	mu        sync.Mutex
	responses []FakeResponse
	calls     []FakeCall
}

func NewFakeLLM() *FakeLLM { return &FakeLLM{} }

func (f *FakeLLM) Queue(r FakeResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses = append(f.responses, r)
}

func (f *FakeLLM) Complete(_ context.Context, model, system, user string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, FakeCall{Model: model, System: system, User: user})

	for i, r := range f.responses {
		if r.Match != nil && !r.Match(system, user) {
			continue
		}
		f.responses = append(f.responses[:i], f.responses[i+1:]...)
		if r.Err != nil {
			return "", r.Err
		}
		return r.Out, nil
	}
	return "", errors.New("no FakeLLM response matched this Complete call")
}

func (f *FakeLLM) Calls() []FakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]FakeCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// Compile-time assertion that *FakeLLM satisfies claude.LLM.
var _ claude.LLM = (*FakeLLM)(nil)
