package authz

import "gorm.io/gorm"

// RelationTuple is the GORM row backing the Postgres tuple store. The six-column
// tuple is the composite PK, doubling as the uniqueness constraint (a tuple is
// present or absent; no duplicates). Two secondary indexes serve Check's hot reads:
//
//   - idx_relation_tuples_object   (object_type, object_id, relation)  -> forward expansion / this + tuple_to_userset
//   - idx_relation_tuples_subject  (subject_type, subject_id)          -> reverse lookup / ReadBySubject
//
// SubjectRelation is "" for direct (user/wildcard) subjects, the relation name for usersets.
type RelationTuple struct {
	ObjectType      string `gorm:"primaryKey;column:object_type;type:text;not null;index:idx_relation_tuples_object,priority:1"`
	ObjectID        string `gorm:"primaryKey;column:object_id;type:text;not null;index:idx_relation_tuples_object,priority:2"`
	Relation        string `gorm:"primaryKey;column:relation;type:text;not null;index:idx_relation_tuples_object,priority:3"`
	SubjectType     string `gorm:"primaryKey;column:subject_type;type:text;not null;index:idx_relation_tuples_subject,priority:1"`
	SubjectID       string `gorm:"primaryKey;column:subject_id;type:text;not null;index:idx_relation_tuples_subject,priority:2"`
	SubjectRelation string `gorm:"primaryKey;column:subject_relation;type:text;not null;default:''"`
}

// TableName returns the Postgres table name.
func (RelationTuple) TableName() string { return "relation_tuples" }

// toTuple converts a persistence row to the store value type. Identical field
// order (only GORM tags differ) makes the conversion a field-by-field copy.
func (r RelationTuple) toTuple() Tuple {
	return Tuple(r)
}

// fromTuple converts a store-facing value into a persistence row.
func fromTuple(t Tuple) RelationTuple {
	return RelationTuple(t)
}

// Migrate creates/updates the relation_tuples table and indexes; safe to call repeatedly.
func Migrate(db *gorm.DB) error {
	return db.AutoMigrate(&RelationTuple{})
}
