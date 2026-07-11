package models

import "time"

// EmbeddingMetadata records the embedding identity — provider, model, and
// dimension — that produced the stored corpus. Exactly one row is maintained,
// keyed by EmbeddingMetadataSingletonID.
//
// The migration guard compares this against the running config and refuses to
// migrate when a populated corpus was built with a different provider or model:
// swapping either silently corrupts cosine similarity, the duplicate guard, and
// retention scoring even at the same dimension (audit #13/#16). An empty corpus
// or a legacy DB with no metadata row adopts the current identity instead.
type EmbeddingMetadata struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	Provider   string    `gorm:"not null" json:"provider"`
	Model      string    `gorm:"not null" json:"model"`
	Dimensions int       `gorm:"not null" json:"dimensions"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// EmbeddingMetadataSingletonID is the fixed primary key of the single
// embedding-metadata row.
const EmbeddingMetadataSingletonID = 1

func (EmbeddingMetadata) TableName() string { return "embedding_metadata" }
