package authz

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PostgresStore is the production GORM-backed Store, persisting to relation_tuples
// (model.go). Writes are idempotent via ON CONFLICT DO NOTHING on the composite-PK.
type PostgresStore struct {
	db *gorm.DB
}

// NewPostgresStore returns a Store over db; caller must have run Migrate on it.
func NewPostgresStore(db *gorm.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

var _ Store = (*PostgresStore)(nil)

func (s *PostgresStore) Write(ctx context.Context, t Tuple) error {
	row := fromTuple(t)
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&row).Error
}

func (s *PostgresStore) Delete(ctx context.Context, t Tuple) error {
	// Explicit column predicates (not struct-based Where) so an empty
	// SubjectRelation matches literally instead of being dropped as a zero value.
	return s.db.WithContext(ctx).
		Where(`object_type = ? AND object_id = ? AND relation = ? AND
		       subject_type = ? AND subject_id = ? AND subject_relation = ?`,
			t.ObjectType, t.ObjectID, t.Relation,
			t.SubjectType, t.SubjectID, t.SubjectRelation).
		Delete(&RelationTuple{}).Error
}

func (s *PostgresStore) ReadByObjectRelation(ctx context.Context, objType, objID, relation string) ([]Tuple, error) {
	var rows []RelationTuple
	if err := s.db.WithContext(ctx).
		Where("object_type = ? AND object_id = ? AND relation = ?", objType, objID, relation).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return toTuples(rows), nil
}

func (s *PostgresStore) ReadBySubject(ctx context.Context, subjType, subjID string) ([]Tuple, error) {
	var rows []RelationTuple
	if err := s.db.WithContext(ctx).
		Where("subject_type = ? AND subject_id = ?", subjType, subjID).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return toTuples(rows), nil
}

func toTuples(rows []RelationTuple) []Tuple {
	if len(rows) == 0 {
		return nil
	}
	out := make([]Tuple, len(rows))
	for i, r := range rows {
		out[i] = r.toTuple()
	}
	return out
}
