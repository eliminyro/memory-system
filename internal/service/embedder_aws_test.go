package service

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubInvoker is an in-memory bedrockInvoker: it records the last request body
// it received and returns a canned output (or a canned error).
type stubInvoker struct {
	body    []byte // canned response Body
	err     error  // if non-nil, InvokeModel returns it
	gotBody []byte // captured request Body
}

func (s *stubInvoker) InvokeModel(ctx context.Context, in *bedrockruntime.InvokeModelInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
	s.gotBody = in.Body
	if s.err != nil {
		return nil, s.err
	}
	return &bedrockruntime.InvokeModelOutput{Body: s.body}, nil
}

func TestAWSEmbedder_Titan(t *testing.T) {
	stub := &stubInvoker{body: []byte(`{"embedding":[0.1,0.2,0.3]}`)}
	e := newAWSEmbedder(stub, "amazon.titan-embed-text-v2:0", 3)

	v, err := e.Embed(context.Background(), "hello")
	require.NoError(t, err)
	assert.Equal(t, []float32{0.1, 0.2, 0.3}, v.Slice())

	// Request must be Titan-shaped.
	assert.Contains(t, string(stub.gotBody), "inputText")
}

func TestAWSEmbedder_Cohere(t *testing.T) {
	stub := &stubInvoker{body: []byte(`{"embeddings":[[0.4,0.5]]}`)}
	e := newAWSEmbedder(stub, "cohere.embed-english-v3", 0)

	v, err := e.Embed(context.Background(), "hello")
	require.NoError(t, err)
	assert.Equal(t, []float32{0.4, 0.5}, v.Slice())

	// Request must be Cohere-shaped.
	assert.Contains(t, string(stub.gotBody), "input_type")
}

func TestAWSEmbedder_APIErrorReturnsSentinel(t *testing.T) {
	stub := &stubInvoker{err: errors.New("SENSITIVE-UPSTREAM-DETAIL-do-not-leak")}
	e := newAWSEmbedder(stub, "amazon.titan-embed-text-v2:0", 3)

	_, err := e.Embed(context.Background(), "hello")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEmbeddingUnavailable)
	require.NotContains(t, err.Error(), "SENSITIVE", "upstream detail must not leak into the returned error")
}

func TestAWSEmbedder_UnknownModelFamily(t *testing.T) {
	stub := &stubInvoker{body: []byte(`{}`)}
	e := newAWSEmbedder(stub, "openai.text-embedding-3-small", 3)

	_, err := e.Embed(context.Background(), "hello")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrEmbeddingUnavailable)
	assert.Contains(t, err.Error(), "openai.text-embedding-3-small")
}

// TestAWSEmbedder_MissingCredentials forces credential resolution to fail via the
// loadAWSConfig seam, so the test is deterministic regardless of host credentials.
func TestAWSEmbedder_MissingCredentials(t *testing.T) {
	orig := loadAWSConfig
	t.Cleanup(func() { loadAWSConfig = orig })

	loadAWSConfig = func(ctx context.Context, region string) (aws.Config, error) {
		return aws.Config{
			Region: region,
			Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{}, errors.New("no credentials found")
			}),
		}, nil
	}

	_, err := NewAWSEmbedder("us-east-1", "amazon.titan-embed-text-v2:0", 1024)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not resolve credentials")
}

func TestAWSEmbedder_ValidationErrors(t *testing.T) {
	_, err := NewAWSEmbedder("", "amazon.titan-embed-text-v2:0", 1024)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "region")

	_, err = NewAWSEmbedder("us-east-1", "", 1024)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model")
}
