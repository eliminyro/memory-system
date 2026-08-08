package service

import (
	"context"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
)

// bodyPrefix is a valid start of an OpenAI-compatible embeddings response whose
// array is deliberately never closed, so the JSON value is unbounded.
var bodyPrefix = []byte(`{"data":[{"embedding":[`)

// openArrayBody streams bodyPrefix then an endless run of "1," — a single JSON
// value that never terminates. A decoder with no read cap would buffer it until
// the hard ceiling (max), i.e. effectively unbounded / OOM; the embedder's
// io.LimitReader cap must stop the read at maxEmbedResponseBytes instead.
type openArrayBody struct {
	read *int64 // bytes handed to the caller so far
	max  int64  // hard ceiling so a regressed (uncapped) embedder can't hang forever
	pos  int64
}

func (b *openArrayBody) Read(p []byte) (int, error) {
	if b.pos >= b.max {
		return 0, io.EOF
	}
	n := 0
	for n < len(p) && b.pos < b.max {
		if b.pos < int64(len(bodyPrefix)) {
			p[n] = bodyPrefix[b.pos]
		} else if (b.pos-int64(len(bodyPrefix)))%2 == 0 {
			p[n] = '1'
		} else {
			p[n] = ','
		}
		n++
		b.pos++
	}
	atomic.AddInt64(b.read, int64(n))
	return n, nil
}

func (b *openArrayBody) Close() error { return nil }

type oversizedRT struct {
	read *int64
	max  int64
}

func (rt oversizedRT) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       &openArrayBody{read: rt.read, max: rt.max},
	}, nil
}

// TestOpenAIEmbedder_CapsSuccessBody proves the success path bounds how much of
// a 200 body it decodes: a hostile endpoint streaming a never-terminating JSON
// value makes Embed fail the decode, and the total bytes read stay pinned near
// maxEmbedResponseBytes rather than climbing to the (much larger) ceiling the
// endpoint would otherwise serve — i.e. the read is capped, not unbounded.
func TestOpenAIEmbedder_CapsSuccessBody(t *testing.T) {
	var read int64
	const ceiling = 40 << 20 // 40 MiB — well above the 8 MiB cap

	e := NewOpenAIEmbedder("http://embed.test", "", "m", 0)
	e.client.Transport = oversizedRT{read: &read, max: ceiling}

	if _, err := e.Embed(context.Background(), "x"); err == nil {
		t.Fatal("expected error: a truncated oversized body must fail to decode")
	}

	got := atomic.LoadInt64(&read)
	if got > int64(maxEmbedResponseBytes)+(1<<20) {
		t.Fatalf("read %d bytes, want <= ~%d — success-body read is not capped (unbounded/OOM risk)", got, maxEmbedResponseBytes)
	}
}
