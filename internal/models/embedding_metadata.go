package models

import "time"

// EmbeddingMetadata records the embedding identity (provider, model, dimension)
// that built the corpus — single row keyed by EmbeddingMetadataSingletonID. The
// migration guard refuses a provider/model swap on a populated corpus: it silently
// corrupts similarity, the duplicate guard, and retention even at the same
// dimension (audit #13/#16). An empty/unrecorded corpus adopts the current identity.
type EmbeddingMetadata struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	Provider   string    `gorm:"not null" json:"provider"`
	Model      string    `gorm:"not null" json:"model"`
	Dimensions int       `gorm:"not null" json:"dimensions"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// EmbeddingMetadataSingletonID is the fixed primary key of the single metadata row.
const EmbeddingMetadataSingletonID = 1

func (EmbeddingMetadata) TableName() string { return "embedding_metadata" }
