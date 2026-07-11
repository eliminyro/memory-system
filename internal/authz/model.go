package authz

import "gorm.io/gorm"

// RelationTuple is the GORM row backing the Postgres tuple store. The full
// six-column tuple is the composite primary key, which doubles as the
// uniqueness constraint required by the design (a tuple is either present or
// absent; there are no duplicates). Two secondary indexes support the two hot
// read paths used by Check:
//
//   - idx_relation_tuples_object   (object_type, object_id, relation)  -> forward expansion / this + tuple_to_userset
//   - idx_relation_tuples_subject  (subject_type, subject_id)          -> reverse lookup / ReadBySubject
//
// SubjectRelation is stored as "" for direct (user / wildcard) subjects and set
// to the relation name for userset subjects.
type RelationTuple struct {
	ObjectType      string `gorm:"primaryKey;column:object_type;type:text;not null;index:idx_relation_tuples_object,priority:1"`
	ObjectID        string `gorm:"primaryKey;column:object_id;type:text;not null;index:idx_relation_tuples_object,priority:2"`
	Relation        string `gorm:"primaryKey;column:relation;type:text;not null;index:idx_relation_tuples_object,priority:3"`
	SubjectType     string `gorm:"primaryKey;column:subject_type;type:text;not null;index:idx_relation_tuples_subject,priority:1"`
	SubjectID       string `gorm:"primaryKey;column:subject_id;type:text;not null;index:idx_relation_tuples_subject,priority:2"`
	SubjectRelation string `gorm:"primaryKey;column:subject_relation;type:text;not null;default:''"`
}

// TableName returns the Postgres table name for RelationTuple.
func (RelationTuple) TableName() string { return "relation_tuples" }

// toTuple converts a persistence row into the store-facing value type. The two
// structs share identical field sequences (they differ only in GORM tags), so
// a direct conversion is equivalent to a field-by-field copy.
func (r RelationTuple) toTuple() Tuple {
	return Tuple(r)
}

// fromTuple converts a store-facing value into a persistence row.
func fromTuple(t Tuple) RelationTuple {
	return RelationTuple(t)
}

// Migrate creates or updates the relation_tuples table and its indexes. It is
// safe to call repeatedly. Wiring code (migrations, bootstrap) and integration
// tests call this to bring the schema up.
func Migrate(db *gorm.DB) error {
	return db.AutoMigrate(&RelationTuple{})
}
