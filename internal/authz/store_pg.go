package authz

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PostgresStore is the GORM-backed Store used in production. It persists tuples
// into the relation_tuples table (see model.go). All writes are idempotent via
// ON CONFLICT DO NOTHING against the composite-primary-key uniqueness
// constraint.
type PostgresStore struct {
	db *gorm.DB
}

// NewPostgresStore returns a Store backed by the given *gorm.DB. Callers are
// responsible for having run Migrate on the same database.
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
	// Explicit column predicates (not a struct-based Where) so an empty
	// SubjectRelation is matched literally rather than dropped as a zero value.
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
