package diff

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// Filesystem is the read surface diff needs. It exists as an interface so
// tests can drive the diff against synthetic states without touching the
// real filesystem. Apply uses the same osFS at write time.
type Filesystem interface {
	// Stat returns presence and metadata. A non-existent path returns
	// FileInfo{Exists:false}, nil — not an error.
	Stat(path string) (FileInfo, error)
	// ReadFile returns the bytes at path. Used for content hashing.
	ReadFile(path string) ([]byte, error)
}

// FileInfo is the metadata diff needs for equivalence: existence, mode bits,
// and ownership. It deliberately omits size and mtime — equivalence is
// content-hash + permissions, not timestamps.
type FileInfo struct {
	Exists bool
	Mode   uint32 // permission bits only (file type stripped)
	UID    int
	GID    int
}

// OS returns a Filesystem backed by the real OS filesystem.
func OS() Filesystem { return osFS{} }

type osFS struct{}

func (osFS) Stat(path string) (FileInfo, error) {
	st, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return FileInfo{Exists: false}, nil
	}
	if err != nil {
		return FileInfo{}, err
	}
	info := FileInfo{
		Exists: true,
		Mode:   uint32(st.Mode().Perm()),
	}
	// UID/GID are only available via syscall.Stat_t on Unix. On non-Unix
	// platforms they remain zero, which is fine — magus runs on Linux.
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		info.UID = int(sys.Uid)
		info.GID = int(sys.Gid)
	}
	return info, nil
}

func (osFS) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
