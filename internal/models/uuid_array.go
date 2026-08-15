package models

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// UUIDArray bridges Go []uuid.UUID and Postgres uuid[] (recall_receipts.section_ids),
// mirroring authletstore.StringArray's pq-array bridge. Falls back to JSON for
// non-Postgres (sqlite) test backends.
type UUIDArray []uuid.UUID

// Value implements driver.Valuer for Postgres uuid[] writes.
func (u UUIDArray) Value() (driver.Value, error) {
	strs := make([]string, len(u))
	for i, id := range u {
		strs[i] = id.String()
	}
	return pq.StringArray(strs).Value()
}

// Scan implements sql.Scanner. Tries the Postgres array wire format first
// (parsed generically as strings, then each parsed as a UUID); falls back to
// JSON unmarshalling so the same type works against sqlite test backends.
func (u *UUIDArray) Scan(src any) error {
	arr := pq.StringArray{}
	if err := arr.Scan(src); err != nil {
		switch v := src.(type) {
		case []byte:
			return json.Unmarshal(v, u)
		case string:
			return json.Unmarshal([]byte(v), u)
		}
		return errors.New("models: unsupported scan type for UUIDArray")
	}
	out := make(UUIDArray, len(arr))
	for i, s := range arr {
		id, err := uuid.Parse(s)
		if err != nil {
			return fmt.Errorf("parse uuid in array: %w", err)
		}
		out[i] = id
	}
	*u = out
	return nil
}
