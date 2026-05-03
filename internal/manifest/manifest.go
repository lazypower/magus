// Package manifest persists magus's ownership claims.
//
// /var/lib/magus/manifest.json is the consent contract: every path magus
// placed (or adopted) is recorded here. The diff stage joins IR ∪ manifest ∪
// disk to compute actions; manifest is the post-hoc authority boundary.
//
// See docs/spec-reconciler.md "State tracking — the manifest".
package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// DefaultPath is where magus reads and writes the manifest by default.
const DefaultPath = "/var/lib/magus/manifest.json"

// CurrentVersion is the schema version this binary writes and accepts. A
// manifest with any other version is refused — see Load.
const CurrentVersion = 1

// State of a manifest entry. "active" means under reconciliation; "orphaned"
// means excluded by policy and held for audit. Orphan transitions are sticky
// — see Policy contention in the spec.
type State string

const (
	StateActive   State = "active"
	StateOrphaned State = "orphaned"
)

// Origin records how a resource entered the manifest. Reconciliation behavior
// depends only on State; Origin is metadata for audit.
type Origin string

const (
	OriginCreate      Origin = "create"
	OriginAdopt       Origin = "adopt"
	OriginForceAdopt  Origin = "force-adopt"
)

// Manifest is the on-disk shape. Resources is keyed by absolute path.
type Manifest struct {
	Version   int                 `json:"version"`
	Resources map[string]Resource `json:"resources"`
}

// Resource is one path under magus management.
type Resource struct {
	State          State      `json:"state"`
	Hash           string     `json:"hash"`
	AppliedAt      time.Time  `json:"applied_at"`
	Origin         Origin     `json:"origin"`
	OrphanedAt     *time.Time `json:"orphaned_at,omitempty"`
	OrphanedReason string     `json:"orphaned_reason,omitempty"`
}

// New returns an empty current-version manifest.
func New() *Manifest {
	return &Manifest{Version: CurrentVersion, Resources: map[string]Resource{}}
}

// Load reads and parses path. A missing file returns an empty manifest — magus
// being unaware of any path is the legitimate first-run state. Any other read
// error, parse error, or version mismatch is fatal: those are input-bad cases
// per the spec, and apply must halt before any reconciliation runs.
func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	if m.Version != CurrentVersion {
		return nil, fmt.Errorf("manifest %s: version %d, this binary only understands version %d",
			path, m.Version, CurrentVersion)
	}
	if m.Resources == nil {
		m.Resources = map[string]Resource{}
	}
	return &m, nil
}

// Save writes the manifest atomically: temp file in the same directory, then
// rename(2) into place. The directory is created if missing — magus has
// authority to materialize /var/lib/magus.
func (m *Manifest) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("manifest dir: %w", err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	tmp := path + ".magus.tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write tmp manifest: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename manifest: %w", err)
	}
	return nil
}

// Owns reports whether path is recorded in the manifest in the active state.
// Orphaned entries are *recorded* but not *owned* for reconciliation purposes;
// callers handling the orphan branch use Get directly.
func (m *Manifest) Owns(path string) bool {
	r, ok := m.Resources[path]
	return ok && r.State == StateActive
}

// Get returns the entry and whether it's present, regardless of state.
func (m *Manifest) Get(path string) (Resource, bool) {
	r, ok := m.Resources[path]
	return r, ok
}

// PutActive records (or replaces) an active entry for path. Used by apply
// after a successful create/update/adopt.
func (m *Manifest) PutActive(path, hash string, origin Origin, at time.Time) {
	m.Resources[path] = Resource{
		State:     StateActive,
		Hash:      hash,
		AppliedAt: at,
		Origin:    origin,
	}
}

// Delete removes path from the manifest. Used after a successful filesystem
// delete or for stale-entry cleanup.
func (m *Manifest) Delete(path string) {
	delete(m.Resources, path)
}

// Orphan transitions an active entry to orphaned with the given reason. The
// hash and origin are preserved — orphan state is sticky and audit-only.
func (m *Manifest) Orphan(path, reason string, at time.Time) {
	r, ok := m.Resources[path]
	if !ok {
		return
	}
	r.State = StateOrphaned
	r.OrphanedAt = &at
	r.OrphanedReason = reason
	m.Resources[path] = r
}
