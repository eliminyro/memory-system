package usagebench

import (
	"reflect"
	"sort"
	"testing"
)

func testParams(seed int64, gapFraction float64) GenParams {
	p := DefaultParams(seed, gapFraction)
	p.Dim = 256 // smaller than prod dim; keeps cross-topic cosine well below the gap
	p.Topics = 4
	p.HistoryQueries = 80
	p.HeldOutQueries = 40
	return p
}

func TestGenerate_Reproducible(t *testing.T) {
	a, err := Generate(testParams(7, 0.4))
	if err != nil {
		t.Fatalf("generate a: %v", err)
	}
	b, err := Generate(testParams(7, 0.4))
	if err != nil {
		t.Fatalf("generate b: %v", err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatal("same seed produced different corpora")
	}

	c, err := Generate(testParams(8, 0.4))
	if err != nil {
		t.Fatalf("generate c: %v", err)
	}
	if reflect.DeepEqual(a, c) {
		t.Fatal("different seed produced identical corpora")
	}
}

func TestGenerate_DisjointStreams(t *testing.T) {
	c, err := Generate(testParams(3, 0.4))
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := assertDisjointIDs(c.History, c.HeldOut); err != nil {
		t.Fatal(err)
	}
}

// assertDisjointIDs mirrors the harness invariant without the build tag.
func assertDisjointIDs(a, b []Query) error {
	seen := make(map[string]struct{}, len(a))
	for _, q := range a {
		seen[q.ID] = struct{}{}
	}
	for _, q := range b {
		if _, clash := seen[q.ID]; clash {
			return errDup(q.ID)
		}
	}
	return nil
}

type errDup string

func (e errDup) Error() string { return "duplicate query id: " + string(e) }

func TestGap_BelowTopK_AndRelevant(t *testing.T) {
	const topK = 10
	p := testParams(11, 0.4)
	c, err := Generate(p)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if gapPerTopic(p) == 0 {
		t.Fatal("expected some gap docs for gapFraction 0.4")
	}

	for topic := 0; topic < p.Topics; topic++ {
		q := c.TopicQuery(topic)
		gold := make(map[string]bool)
		for _, id := range c.RelevantDocIDs(topic) {
			gold[id] = true
		}

		// Rank every doc by cosine to this topic's query direction.
		type scored struct {
			doc Doc
			sim float64
		}
		ranked := make([]scored, 0, len(c.Docs))
		for _, d := range c.Docs {
			ranked = append(ranked, scored{d, cosine(d.Embedding, q)})
		}
		sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].sim > ranked[j].sim })

		rankOf := make(map[string]int, len(ranked))
		for i, s := range ranked {
			rankOf[s.doc.ID] = i // 0-based
		}

		for _, d := range c.Docs {
			switch d.Kind {
			case KindGap:
				if d.Topic != topic {
					continue
				}
				if !gold[d.ID] {
					t.Fatalf("gap doc %s not labeled relevant for its topic", d.ID)
				}
				if rankOf[d.ID] < topK {
					t.Fatalf("gap doc %s ranked %d (< top-%d) for topic %d — not below top-K",
						d.ID, rankOf[d.ID], topK, topic)
				}
			case KindDistractor:
				if d.Target == topic && gold[d.ID] {
					t.Fatalf("distractor %s must not be relevant", d.ID)
				}
			}
		}
	}
}

func TestControl_HasNoGapDocs(t *testing.T) {
	c, err := Generate(testParams(5, 0))
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	for _, d := range c.Docs {
		if d.Kind == KindGap {
			t.Fatalf("control workload (gapFraction 0) unexpectedly produced gap doc %s", d.ID)
		}
	}
}

// TestSecondaryGeometry_FailsAndServesDistractors verifies the D8 requirement
// that secondary recalls fail: only distractors sit near the secondary
// direction (above the 0.40 fusion floor), while relevant docs stay well below
// it — so a secondary recall surfaces no relevant doc and credits misses.
func TestSecondaryGeometry_FailsAndServesDistractors(t *testing.T) {
	const floor = 0.40
	p := testParams(9, 0.4)
	c, err := Generate(p)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	for topic := 0; topic < p.Topics; topic++ {
		qb := c.TopicQuerySecondary(topic)
		for _, d := range c.Docs {
			if d.Target != topic {
				continue
			}
			sim := cosine(d.Embedding, qb)
			switch d.Kind {
			case KindDistractor:
				if sim <= floor {
					t.Fatalf("distractor %s cosine to secondary %.3f <= floor — would not be served", d.ID, sim)
				}
			case KindEasy, KindGap:
				if sim >= floor {
					t.Fatalf("relevant %s cosine to secondary %.3f >= floor — would leak into a failing recall", d.ID, sim)
				}
			}
		}
	}
}

func TestHistoryMix_PrimaryAndSecondary(t *testing.T) {
	c, err := Generate(testParams(13, 0.4))
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	sec := 0
	for _, q := range c.History {
		if q.Secondary {
			sec++
		}
	}
	frac := float64(sec) / float64(len(c.History))
	if frac < 0.15 || frac > 0.55 {
		t.Fatalf("secondary fraction %.2f outside [0.15,0.55] — need a real success/fail mix", frac)
	}
	for _, q := range c.HeldOut {
		if q.Secondary {
			t.Fatal("held-out stream must be primary-only")
		}
	}
}
