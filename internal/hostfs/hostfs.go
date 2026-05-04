// Package hostfs is the read/write filesystem surface magus uses.
//
// It exists as a thin interface so tests can drive diff and apply against
// in-memory state without touching the real filesystem. Production uses
// hostfs.OS() which delegates to syscall.
package hostfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// FileInfo carries the metadata diff and apply care about. It deliberately
// omits size and mtime — equivalence in magus is content-hash + permissions,
// not timestamps.
type FileInfo struct {
	Exists bool
	Mode   uint32 // permission bits only
	UID    int
	GID    int
}

// Reader is the read surface used by diff. Diff never writes; keeping the
// read interface separate from Writer lets the type system enforce that.
type Reader interface {
	// Stat returns presence and metadata. A non-existent path returns
	// FileInfo{Exists:false}, nil — not an error.
	Stat(path string) (FileInfo, error)
	// ReadFile returns the bytes at path. Used for content hashing.
	ReadFile(path string) ([]byte, error)
}

// Writer extends Reader with the mutating operations apply needs. Apply may
// read (for adoption re-verification) and write through the same value.
type Writer interface {
	Reader
	// WriteFile writes contents atomically to path, setting mode and (when
	// specified) ownership. The implementation uses the spec's tmp+rename
	// pattern: write to <path>.magus.tmp on the same directory, set perms,
	// then rename(2) into place.
	//
	// uid and gid are *int because the IR distinguishes "explicitly own this
	// as user X" (non-nil) from "let the writer's identity stand" (nil). When
	// nil, no chown call is made — matching Ignition's semantic and allowing
	// magus to run as a non-root user during development.
	//
	// Parent directories are auto-created with mode 0755 if missing.
	// Explicit directory declarations (PR 6) will reconcile mode and
	// ownership properly; this is the file-path-prep fallback.
	WriteFile(path string, contents []byte, mode uint32, uid, gid *int) error
	// Remove unlinks path. A missing file is not an error — apply may
	// race with out-of-band deletion and the desired state still holds.
	Remove(path string) error
}

// OS returns a Writer backed by the real operating system filesystem.
func OS() Writer { return osImpl{} }

type osImpl struct{}

func (osImpl) Stat(path string) (FileInfo, error) {
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
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		info.UID = int(sys.Uid)
		info.GID = int(sys.Gid)
	}
	return info, nil
}

func (osImpl) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (osImpl) WriteFile(path string, contents []byte, mode uint32, uid, gid *int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".magus.tmp"
	// Use O_TRUNC so a leftover tmp from a prior crashed apply is replaced
	// rather than appended to.
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(mode))
	if err != nil {
		return err
	}
	if _, err := f.Write(contents); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// Chmod again after close — OpenFile honors umask, which can clip modes
	// like 0666 unexpectedly.
	if err := os.Chmod(tmp, os.FileMode(mode)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// Chown only when the IR explicitly specifies ownership. -1 in os.Chown
	// is the "don't change" sentinel, matching chown(2)'s semantics.
	chownUID, chownGID := -1, -1
	if uid != nil {
		chownUID = *uid
	}
	if gid != nil {
		chownGID = *gid
	}
	if chownUID != -1 || chownGID != -1 {
		if err := os.Chown(tmp, chownUID, chownGID); err != nil {
			_ = os.Remove(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (osImpl) Remove(path string) error {
	err := os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
