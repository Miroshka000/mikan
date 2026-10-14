package diag

import (
	"os"
	"syscall"
)

func diskSpace(dir string) (free, total uint64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, 0, false
	}
	bs := uint64(st.Bsize) // a block size is positive
	return st.Bavail * bs, st.Blocks * bs, true
}

func memory() (available, total uint64, ok bool) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	return ParseMeminfo(f)
}
