//go:build !linux

package diag

// The servers mikan runs on are Linux; elsewhere (a developer's machine) these are not known.
func diskSpace(string) (free, total uint64, ok bool) { return 0, 0, false }

func memory() (available, total uint64, ok bool) { return 0, 0, false }
