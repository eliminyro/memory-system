package staleness

import (
	"testing"

	"github.com/eliminyro/memory-system/internal/models"
)

func TestEffectiveFor_FallsBackToReference(t *testing.T) {
	store := NewPolicyStoreFromEffective(map[string]models.EffectivePolicy{
		models.DocTypeReference: {DefaultSearch: true, Embed: true},
		models.DocTypeLearning:  {DefaultSearch: false, Embed: true},
	})
	if got := store.EffectiveFor(models.DocTypeLearning); got.DefaultSearch {
		t.Error("learning policy must serve its own DefaultSearch=false")
	}
	// An unknown doc_type falls back to the reference row.
	if got := store.EffectiveFor("bogus"); !got.DefaultSearch {
		t.Error("unknown doc_type must fall back to reference (DefaultSearch=true)")
	}
}

func TestDocTypesWhere_FiltersBySortedPredicate(t *testing.T) {
	store := NewPolicyStoreFromEffective(map[string]models.EffectivePolicy{
		models.DocTypeReference: {DefaultSearch: true},
		models.DocTypeJournal:   {DefaultSearch: false},
		models.DocTypeHandoff:   {DefaultSearch: false},
	})
	got := store.DocTypesWhere(func(p models.EffectivePolicy) bool { return !p.DefaultSearch })
	want := []string{models.DocTypeHandoff, models.DocTypeJournal}
	if len(got) != len(want) {
		t.Fatalf("DocTypesWhere = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("DocTypesWhere = %v, want sorted %v", got, want)
		}
	}
}
