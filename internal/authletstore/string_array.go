package authletstore

import (
	"database/sql/driver"
	"encoding/json"
	"errors"

	"github.com/lib/pq"
)

// StringArray bridges Go []string and Postgres text[]. For non-Postgres
// backends (sqlite in tests) it falls back to JSON-encoded storage.
type StringArray []string

// Value implements driver.Valuer for Postgres text[] writes.
func (s StringArray) Value() (driver.Value, error) { return pq.StringArray(s).Value() }

// Scan implements sql.Scanner. Tries pq.StringArray first; falls back to
// JSON unmarshalling so the same type works against sqlite test backends.
func (s *StringArray) Scan(src any) error {
	arr := pq.StringArray{}
	if err := arr.Scan(src); err != nil {
		// fall back to JSON for sqlite test backends
		switch v := src.(type) {
		case []byte:
			return json.Unmarshal(v, s)
		case string:
			return json.Unmarshal([]byte(v), s)
		}
		return errors.New("authletstore: unsupported scan type for StringArray")
	}
	*s = StringArray(arr)
	return nil
}
