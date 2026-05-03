// Package ir is the intermediate representation magus consumes.
//
// The Butane file is the input format. The IR is the strict subset magus
// reconciles: storage.files, storage.directories, systemd.units. Anything
// outside this subset is invisible to magus — see docs/spec-reconciler.md
// "IR contract".
package ir

// IR is the parsed, in-memory representation of a Butane file's reconcilable
// subset.
type IR struct {
	Files       []File
	Directories []Directory
	Units       []Unit
}

// File is a declared regular file under storage.files.
type File struct {
	Path     string
	Mode     uint32
	UID      int
	GID      int
	Contents []byte
}

// Directory is a declared directory under storage.directories.
type Directory struct {
	Path string
	Mode uint32
	UID  int
	GID  int
}

// Unit is a declared systemd unit. If DropIns is non-empty, the unit is
// extended via drop-ins rather than (or in addition to) replacing the unit
// file itself.
type Unit struct {
	Name     string
	Enabled  bool
	Mask     bool
	Contents string
	DropIns  []DropIn
}

// DropIn is a systemd drop-in fragment. Magus places all drop-ins as
// 10-magus.conf so they sort predictably.
type DropIn struct {
	Name     string
	Contents string
}
