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

// BoolPtr is a nullable column as a pointer: nil for NULL.
func BoolPtr(v sql.NullBool) *bool {
	if !v.Valid {
		return nil
	}
	return &v.Bool
}

const (
	// MinUnix and MaxUnix bound RFC 3339 4-digit years [0000, 9999].
	// Outside this range, time.Time.MarshalJSON fails with "year outside of range [0,9999]".
	MinUnix = -62167219200 // 0000-01-01T00:00:00Z
	MaxUnix = 253402300799 // 9999-12-31T23:59:59Z
)

// Time turns unix seconds into a UTC time clamped to [0000, 9999] so JSON serialization never fails.
func Time(sec int64) time.Time {
	if sec < MinUnix {
		return time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	if sec > MaxUnix {
		return time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	}
	return time.Unix(sec, 0).UTC()
}

// ClampTime clamps t to [0000-01-01, 9999-12-31] so JSON serialization never fails.
func ClampTime(t time.Time) time.Time {
	return Time(t.Unix())
}

// ClampUnix clamps unix seconds to [MinUnix, MaxUnix].
func ClampUnix(sec int64) int64 {
	if sec < MinUnix {
		return MinUnix
	}
	if sec > MaxUnix {
		return MaxUnix
	}
	return sec
}

// TimePtr is a nullable unix-seconds column as a UTC time clamped to [0000, 9999]: nil for NULL.
func TimePtr(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := Time(v.Int64)
	return &t
}
