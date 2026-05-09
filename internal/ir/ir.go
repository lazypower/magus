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
	Quadlets    []Quadlet
}

// File is a declared regular file under storage.files.
//
// UID and GID are *int rather than int so the IR can distinguish "owned by
// nobody in particular — let the writer decide" (nil) from "explicitly owned
// by user 0" (non-nil pointer to 0). This mirrors Ignition's actual semantic
// and lets magus run as a non-root user during development without forcing
// every fixture to enumerate ownership.
type File struct {
	Path     string
	Mode     uint32
	UID      *int
	GID      *int
	Contents []byte
}

// Directory is a declared directory under storage.directories. Same UID/GID
// semantics as File.
type Directory struct {
	Path string
	Mode uint32
	UID  *int
	GID  *int
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

// Quadlet is a podman-managed container declaration auto-promoted from a
// storage.files entry whose path falls under /etc/containers/systemd/ and
// whose extension is one of v1's supported quadlet types (.container,
// .volume, .network). The systemd-quadlet generator runs at daemon-reload
// time and materializes a .service from each quadlet source — that .service
// is what magus enables, starts, and (on content change) restarts.
//
// Name is the basename of Path (e.g., "ollama.container"). The generated
// .service name is derived from Name via QuadletGeneratedService.
type Quadlet struct {
	Path     string
	Name     string
	Mode     uint32
	UID      *int
	GID      *int
	Contents []byte
}
