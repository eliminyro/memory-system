// Package usagebench is the synthetic usage-weighted-ranking benchmark
// (OpenSpec phase-b-usage-benchmark, design D4/D7/D8). The pure files here
// (corpus, metrics, verdict, output) build and unit-test with plain `go test`;
// the DB-driven runner lives in harness.go / usagebench_run_test.go behind the
// `usagebench` build tag so it never runs in the normal test path.
package usagebench

import (
	"context"
	"fmt"
	"math"
	"math/rand"

	"github.com/eliminyro/memory-system/internal/service"
)

// Kind classifies a generated document by its role in the engineered geometry.
const (
	KindEasy       = "easy"       // relevant, high cosine to the topic's PRIMARY query
	KindGap        = "gap"        // relevant, low cosine — below top-K without usage
	KindDistractor = "distractor" // non-relevant; served by BOTH primary and secondary queries
)

// Doc is one generated single-section document with its designed embedding.
type Doc struct {
	ID        string    `json:"id"`
	Topic     int       `json:"topic"`  // relevant topic; -1 for a distractor (relevant to nobody)
	Target    int       `json:"target"` // topic whose query region the embedding points at
	Kind      string    `json:"kind"`
	Subcat    string    `json:"subcat"` // unique per doc (path segment); one section per doc
	Text      string    `json:"text"`
	Embedding []float32 `json:"embedding"`
}

// Query is one recall in the stream. Primary queries point at a topic's main
// direction (where its relevant docs live); secondary queries point at a
// distractor-only direction, so they surface no relevant doc and become the
// FAILING recalls that give distractors misses under receipt-level crediting
// (design D8). History and held-out never share an ID.
type Query struct {
	ID        string    `json:"id"`
	Topic     int       `json:"topic"`
	Secondary bool      `json:"secondary"`
	Text      string    `json:"text"`
	Embedding []float32 `json:"embedding"` // = normalize(FakeEmbedder(Text)); informational
	Gold      []string  `json:"gold"`      // relevant doc IDs (empty for secondary — never measured)
}

// GenParams controls corpus generation. Same params + seed ⇒ identical output.
type GenParams struct {
	Seed                int64   `json:"seed"`
	Topics              int     `json:"topics"`
	EasyPerTopic        int     `json:"easy_per_topic"`
	DistractorsPerTopic int     `json:"distractors_per_topic"`
	GapFraction         float64 `json:"gap_fraction"`       // fraction of relevant docs that are gap; 0 = control
	SecondaryFraction   float64 `json:"secondary_fraction"` // share of history recalls that are failing (secondary)
	Dim                 int     `json:"dim"`
	HistoryQueries      int     `json:"history_queries"`
	HeldOutQueries      int     `json:"held_out_queries"`
	ZipfS               float64 `json:"zipf_s"` // Zipfian skew for topic popularity (>1)
	EasyCos             float64 `json:"easy_cos"`
	DistractorCosA      float64 `json:"distractor_cos_a"` // cosine to the primary direction
	DistractorCosB      float64 `json:"distractor_cos_b"` // cosine to the secondary direction
	GapCos              float64 `json:"gap_cos"`
}

// DefaultParams returns the tuned defaults. easy+distractor (12) >= K=10 so gap
// docs sit below top-K on the primary query; per-topic doc count (<= ~18) <= the
// servedDepth pool so every gap doc is served and can accrue usage. Distractors
// point at both directions so they are co-served (hits) on successful primary
// recalls AND served alone (misses) on failing secondary recalls.
func DefaultParams(seed int64, gapFraction float64) GenParams {
	return GenParams{
		Seed:                seed,
		Topics:              8,
		EasyPerTopic:        6,
		DistractorsPerTopic: 6,
		GapFraction:         gapFraction,
		SecondaryFraction:   0.35,
		Dim:                 768,
		HistoryQueries:      1600,
		HeldOutQueries:      240,
		ZipfS:               1.3,
		EasyCos:             0.92,
		DistractorCosA:      0.65, // above gap (0.50) so gap stays below top-K on primary
		DistractorCosB:      0.65, // above the 0.40 floor so secondary recalls serve distractors
		GapCos:              0.50, // safely above the 0.40 fusion score floor
	}
}

// Corpus is the generated workload: documents plus disjoint history / held-out
// query streams over the same topics.
type Corpus struct {
	Params  GenParams `json:"params"`
	Docs    []Doc     `json:"docs"`
	History []Query   `json:"history"`
	HeldOut []Query   `json:"held_out"`

	queryA [][]float32 // per-topic primary direction (unit)
	queryB [][]float32 // per-topic secondary direction (unit, as FakeEmbedder emits)
}

// gapPerTopic derives the gap count so gap/(easy+gap) ≈ GapFraction.
func gapPerTopic(p GenParams) int {
	f := p.GapFraction
	if f <= 0 {
		return 0
	}
	if f > 0.9 {
		f = 0.9
	}
	return int(math.Round(f / (1 - f) * float64(p.EasyPerTopic)))
}

// Generate builds the corpus deterministically from p. Query directions come
// from the real FakeEmbedder (so Search's text→vector path reproduces them);
// document embeddings are engineered relative to those directions and written
// directly at ingest, giving an exact semantic geometry (design D4).
func Generate(p GenParams) (*Corpus, error) {
	if p.Topics < 2 {
		return nil, fmt.Errorf("usagebench: need Topics >= 2, got %d", p.Topics)
	}
	if p.ZipfS <= 1 {
		return nil, fmt.Errorf("usagebench: need ZipfS > 1, got %v", p.ZipfS)
	}
	rng := rand.New(rand.NewSource(p.Seed))
	embedder := service.NewFakeEmbedder(p.Dim)

	c := &Corpus{Params: p, queryA: make([][]float32, p.Topics), queryB: make([][]float32, p.Topics)}

	textA := make([]string, p.Topics)
	textB := make([]string, p.Topics)
	qBperp := make([][]float32, p.Topics) // secondary direction orthogonalized against primary
	for t := 0; t < p.Topics; t++ {
		textA[t] = fmt.Sprintf("ubqAtopic%dprobe", t)
		textB[t] = fmt.Sprintf("ubqBtopic%dprobe", t)
		va, err := embedder.Embed(context.Background(), textA[t])
		if err != nil {
			return nil, fmt.Errorf("embed topic %d primary: %w", t, err)
		}
		vb, err := embedder.Embed(context.Background(), textB[t])
		if err != nil {
			return nil, fmt.Errorf("embed topic %d secondary: %w", t, err)
		}
		c.queryA[t] = normalize(va.Slice())
		c.queryB[t] = normalize(vb.Slice())
		qBperp[t] = orthogonalize(c.queryB[t], c.queryA[t])
	}

	gap := gapPerTopic(p)
	for t := 0; t < p.Topics; t++ {
		for i := 0; i < p.EasyPerTopic; i++ {
			c.Docs = append(c.Docs, c.makeDoc(rng, t, t, KindEasy, i, c.queryA[t], p.EasyCos))
		}
		for i := 0; i < p.DistractorsPerTopic; i++ {
			c.Docs = append(c.Docs, c.makeDistractor(rng, t, i, c.queryA[t], qBperp[t], p.DistractorCosA, p.DistractorCosB))
		}
		for i := 0; i < gap; i++ {
			c.Docs = append(c.Docs, c.makeDoc(rng, t, t, KindGap, i, c.queryA[t], p.GapCos))
		}
	}

	gold := c.goldByTopic()
	c.History = c.drawHistory(rng, textA, textB, gold)
	c.HeldOut = c.drawHeldOut(rng, textA, gold)
	return c, nil
}

func (c *Corpus) makeDoc(rng *rand.Rand, topic, target int, kind string, idx int, dir []float32, cos float64) Doc {
	ortho := orthoTo(rng, dir)
	id := fmt.Sprintf("t%d-%s%d", target, kind, idx)
	return Doc{
		ID: id, Topic: topic, Target: target, Kind: kind, Subcat: id,
		Text:      fmt.Sprintf("corpusdoc %s subjectword%d filler prose entry", id, target),
		Embedding: mix(dir, ortho, cos),
	}
}

// makeDistractor places a non-relevant doc at cosA to the primary direction and
// cosB to the (orthogonalized) secondary direction, so it is served by both.
func (c *Corpus) makeDistractor(rng *rand.Rand, target, idx int, dirA, dirBperp []float32, cosA, cosB float64) Doc {
	ortho := orthoTo2(rng, dirA, dirBperp)
	id := fmt.Sprintf("t%d-%s%d", target, KindDistractor, idx)
	return Doc{
		ID: id, Topic: -1, Target: target, Kind: KindDistractor, Subcat: id,
		Text:      fmt.Sprintf("corpusdoc %s subjectword%d filler prose entry", id, target),
		Embedding: mix2(dirA, dirBperp, ortho, cosA, cosB),
	}
}

// goldByTopic maps each topic to its relevant doc IDs (easy + gap; distractors excluded).
func (c *Corpus) goldByTopic() map[int][]string {
	m := make(map[int][]string, c.Params.Topics)
	for _, d := range c.Docs {
		if d.Kind == KindDistractor {
			continue
		}
		m[d.Topic] = append(m[d.Topic], d.ID)
	}
	return m
}

// RelevantDocIDs returns the gold doc IDs for a topic.
func (c *Corpus) RelevantDocIDs(topic int) []string { return c.goldByTopic()[topic] }

// TopicQuery returns the unit PRIMARY query direction for a topic.
func (c *Corpus) TopicQuery(topic int) []float32 { return c.queryA[topic] }

// TopicQuerySecondary returns the unit SECONDARY (distractor-only) direction.
func (c *Corpus) TopicQuerySecondary(topic int) []float32 { return c.queryB[topic] }

func (c *Corpus) drawHistory(rng *rand.Rand, textA, textB []string, gold map[int][]string) []Query {
	zipf := rand.NewZipf(rng, c.Params.ZipfS, 1, uint64(c.Params.Topics-1))
	out := make([]Query, 0, c.Params.HistoryQueries)
	for i := 0; i < c.Params.HistoryQueries; i++ {
		t := int(zipf.Uint64())
		secondary := rng.Float64() < c.Params.SecondaryFraction
		q := Query{ID: fmt.Sprintf("h-%d", i), Topic: t, Secondary: secondary}
		if secondary {
			q.Text, q.Embedding = textB[t], c.queryB[t]
		} else {
			q.Text, q.Embedding, q.Gold = textA[t], c.queryA[t], gold[t]
		}
		out = append(out, q)
	}
	return out
}

func (c *Corpus) drawHeldOut(rng *rand.Rand, textA []string, gold map[int][]string) []Query {
	zipf := rand.NewZipf(rng, c.Params.ZipfS, 1, uint64(c.Params.Topics-1))
	out := make([]Query, 0, c.Params.HeldOutQueries)
	for i := 0; i < c.Params.HeldOutQueries; i++ {
		t := int(zipf.Uint64())
		out = append(out, Query{
			ID: fmt.Sprintf("o-%d", i), Topic: t,
			Text: textA[t], Embedding: c.queryA[t], Gold: gold[t],
		})
	}
	return out
}

// ---- vector helpers (cosine geometry; magnitude is irrelevant under the `<=>` operator) ----

func normalize(v []float32) []float32 {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	n = math.Sqrt(n)
	if n == 0 {
		return v
	}
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) / n)
	}
	return out
}

func dot(a, b []float32) float64 {
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// cosine of two vectors (assumes finite, non-zero).
func cosine(a, b []float32) float64 {
	na, nb := math.Sqrt(dot(a, a)), math.Sqrt(dot(b, b))
	if na == 0 || nb == 0 {
		return 0
	}
	return dot(a, b) / (na * nb)
}

// orthogonalize returns unit(v - (v·base)·base): the component of v ⊥ base.
func orthogonalize(v, base []float32) []float32 {
	d := dot(v, base)
	out := make([]float32, len(v))
	for i := range v {
		out[i] = v[i] - float32(d)*base[i]
	}
	return normalize(out)
}

// orthoTo returns a random unit vector orthogonal to base.
func orthoTo(rng *rand.Rand, base []float32) []float32 {
	for attempt := 0; attempt < 8; attempt++ {
		r := randVec(rng, len(base))
		sub(r, base, dot(r, base))
		if dot(r, r) > 1e-9 {
			return normalize(r)
		}
	}
	return unitAxis(len(base))
}

// orthoTo2 returns a random unit vector orthogonal to both a and b (a ⊥ b).
func orthoTo2(rng *rand.Rand, a, b []float32) []float32 {
	for attempt := 0; attempt < 8; attempt++ {
		r := randVec(rng, len(a))
		sub(r, a, dot(r, a))
		sub(r, b, dot(r, b))
		if dot(r, r) > 1e-9 {
			return normalize(r)
		}
	}
	return unitAxis(len(a))
}

func randVec(rng *rand.Rand, dim int) []float32 {
	r := make([]float32, dim)
	for i := range r {
		r[i] = float32(rng.NormFloat64())
	}
	return r
}

// sub does r -= scale·base in place.
func sub(r, base []float32, scale float64) {
	for i := range r {
		r[i] -= float32(scale) * base[i]
	}
}

func unitAxis(dim int) []float32 {
	out := make([]float32, dim)
	out[0] = 1
	return out
}

// mix returns the unit vector at the given cosine to base, along ortho (⊥ base).
func mix(base, ortho []float32, cos float64) []float32 {
	sin := math.Sqrt(math.Max(0, 1-cos*cos))
	out := make([]float32, len(base))
	for i := range base {
		out[i] = float32(cos)*base[i] + float32(sin)*ortho[i]
	}
	return normalize(out)
}

// mix2 returns the unit vector at cosA to a and cosB to b (a ⊥ b ⊥ ortho), with
// the remaining magnitude on ortho. Requires cosA²+cosB² <= 1.
func mix2(a, b, ortho []float32, cosA, cosB float64) []float32 {
	rest := math.Sqrt(math.Max(0, 1-cosA*cosA-cosB*cosB))
	out := make([]float32, len(a))
	for i := range a {
		out[i] = float32(cosA)*a[i] + float32(cosB)*b[i] + float32(rest)*ortho[i]
	}
	return normalize(out)
}
