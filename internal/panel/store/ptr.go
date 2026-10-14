package store

import (
	"database/sql"
	"time"
)

// Int64Ptr is a nullable column as a pointer: nil for NULL.
func Int64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

// TimePtr is a nullable unix-seconds column as a UTC time: nil for NULL.
func TimePtr(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(v.Int64, 0).UTC()
	return &t
}
