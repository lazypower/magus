// Package diff joins IR ∪ manifest ∪ disk and computes the per-resource
// action magus should take. The join is the central decision point of the
// reconciler — see docs/spec-reconciler.md "Diff model".
package diff

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lazypower/magus/internal/hostfs"
	"github.com/lazypower/magus/internal/ir"
	"github.com/lazypower/magus/internal/manifest"
)

// Action is the per-resource verb the planner picks.
type Action string

const (
	ActionCreate   Action = "create"
	ActionUpdate   Action = "update"
	ActionAdopt    Action = "adopt"
	ActionDelete   Action = "delete"
	ActionSkip     Action = "skip"
	ActionConflict Action = "conflict"
	ActionOrphaned Action = "orphaned"
	ActionCleanup  Action = "cleanup"
)

// Kind identifies what type of resource this action targets.
type Kind string

const (
	KindFile      Kind = "file"
	KindDirectory Kind = "directory"
	KindUnit      Kind = "unit"
)

// ResourceAction is one row of the plan. Hashes and modes are populated when
// they're useful for explaining the action — empty otherwise. UnitName is set
// only for KindUnit actions so apply can bucket drop-ins with their parent
// unit for daemon-reload + restart semantics.
type ResourceAction struct {
	Path       string
	Kind       Kind
	UnitName   string // populated only for KindUnit actions
	Action     Action
	Reason     string
	OnDiskHash string
	IRHash     string
	OnDiskMode uint32
	IRMode     uint32
}

// Plan is the full set of actions for one diff. Counts() summarizes for the
// CLI footer.
type Plan struct {
	Actions []ResourceAction
	// Deferred is a count of IR resources whose Kind isn't yet implemented.
	// In v1 with units now in scope, only directories appear here.
	Deferred int
}

// HasChanges reports whether anything other than skips/cleanup would run.
// Used to pick exit codes (0 vs 2).
func (p *Plan) HasChanges() bool {
	for _, a := range p.Actions {
		switch a.Action {
		case ActionCreate, ActionUpdate, ActionAdopt, ActionDelete, ActionConflict, ActionOrphaned:
			return true
		}
	}
	return false
}

// HasConflicts reports whether any resource is in conflict or orphaned —
// both states block apply progress for the affected resource.
func (p *Plan) HasConflicts() bool {
	for _, a := range p.Actions {
		if a.Action == ActionConflict || a.Action == ActionOrphaned {
			return true
		}
	}
	return false
}

// declared is the desired-state record for one path: what the IR says should
// be on disk, plus how to hash it for equivalence (raw bytes for files, post-
// canonicalization bytes for units).
type declaredResource struct {
	Path     string
	Mode     uint32
	UID      *int
	GID      *int
	Contents []byte
	Kind     Kind
	UnitName string // "" for KindFile
}

// hash returns the equivalence hash of this resource's IR-declared content.
// Units canonicalize before hashing so behavior-preserving formatting changes
// in the unit file do not register as drift.
func (d declaredResource) hash() string {
	return HashContent(d.Contents, d.Kind)
}

// HashContent is the cross-package canonical hash. For units, it canonicalizes
// before hashing so apply records the same hash diff would compute on the
// next read. Exported so apply (and any future package) can use the same
// equivalence rule.
func HashContent(b []byte, kind Kind) string {
	if kind == KindUnit {
		b = []byte(CanonicalizeUnit(string(b)))
	}
	return hashBytes(b)
}

// Compute joins the three inputs and produces a plan. fsys reads disk state;
// in normal operation pass hostfs.OS().
//
// Files and units are diffed; directories are not yet (PR 6).
func Compute(in *ir.IR, m *manifest.Manifest, fsys hostfs.Reader) (*Plan, error) {
	plan := &Plan{}
	declared := map[string]bool{}

	for _, f := range in.Files {
		declared[f.Path] = true
		ra, err := diffDeclared(declaredResource{
			Path:     f.Path,
			Mode:     f.Mode,
			UID:      f.UID,
			GID:      f.GID,
			Contents: f.Contents,
			Kind:     KindFile,
		}, m, fsys)
		if err != nil {
			return nil, err
		}
		plan.Actions = append(plan.Actions, ra)
	}

	for _, u := range in.Units {
		// The unit body file at /etc/systemd/system/<name>. Only emitted
		// when the IR provides body content; a Unit with only drop-ins
		// extends a system-shipped unit and magus does not own the body.
		if len(u.Contents) > 0 {
			path := UnitPath(u.Name)
			declared[path] = true
			ra, err := diffDeclared(declaredResource{
				Path:     path,
				Mode:     0o644,
				Contents: []byte(u.Contents),
				Kind:     KindUnit,
				UnitName: u.Name,
			}, m, fsys)
			if err != nil {
				return nil, err
			}
			plan.Actions = append(plan.Actions, ra)
		}
		for _, di := range u.DropIns {
			path := DropInPath(u.Name, di.Name)
			declared[path] = true
			ra, err := diffDeclared(declaredResource{
				Path:     path,
				Mode:     0o644,
				Contents: []byte(di.Contents),
				Kind:     KindUnit,
				UnitName: u.Name,
			}, m, fsys)
			if err != nil {
				return nil, err
			}
			plan.Actions = append(plan.Actions, ra)
		}
	}

	// Manifest sweep: anything magus owns (or has orphaned) that isn't in
	// the IR needs an action — delete, stale-clean, or orphaned-skip.
	for path, entry := range m.Resources {
		if declared[path] {
			continue
		}
		ra, err := diffOrphan(path, entry, fsys)
		if err != nil {
			return nil, err
		}
		plan.Actions = append(plan.Actions, ra)
	}

	plan.Deferred = len(in.Directories)
	return plan, nil
}

func diffDeclared(d declaredResource, m *manifest.Manifest, fsys hostfs.Reader) (ResourceAction, error) {
	ra := ResourceAction{
		Path:     d.Path,
		Kind:     d.Kind,
		UnitName: d.UnitName,
		IRHash:   d.hash(),
		IRMode:   d.Mode,
	}

	// Orphan check first: a manifest orphan dominates everything else.
	if entry, ok := m.Get(d.Path); ok && entry.State == manifest.StateOrphaned {
		ra.Action = ActionOrphaned
		ra.Reason = "orphaned: " + entry.OrphanedReason
		return ra, nil
	}

	st, err := fsys.Stat(d.Path)
	if err != nil {
		return ra, fmt.Errorf("stat %s: %w", d.Path, err)
	}

	if !st.Exists {
		ra.Action = ActionCreate
		return ra, nil
	}

	ra.OnDiskMode = st.Mode

	body, err := fsys.ReadFile(d.Path)
	if err != nil {
		return ra, fmt.Errorf("read %s: %w", d.Path, err)
	}
	ra.OnDiskHash = HashContent(body, d.Kind)

	contentMatch := ra.OnDiskHash == ra.IRHash
	metaMatch := st.Mode == d.Mode &&
		(d.UID == nil || st.UID == *d.UID) &&
		(d.GID == nil || st.GID == *d.GID)
	owned := m.Owns(d.Path)

	switch {
	case contentMatch && metaMatch && owned:
		ra.Action = ActionSkip
		ra.Reason = "unchanged"
	case contentMatch && metaMatch && !owned:
		ra.Action = ActionAdopt
		ra.Reason = "matches IR, claiming ownership"
	case owned:
		ra.Action = ActionUpdate
		ra.Reason = explainDiff(contentMatch, metaMatch, st, d)
	default:
		ra.Action = ActionConflict
		ra.Reason = "exists, " + explainDiff(contentMatch, metaMatch, st, d) + ", not in manifest"
	}
	return ra, nil
}

func diffOrphan(path string, entry manifest.Resource, fsys hostfs.Reader) (ResourceAction, error) {
	ra := ResourceAction{Path: path, Kind: kindFromManifest(entry.Kind)}
	if ra.Kind == KindUnit {
		ra.UnitName = UnitNameFromPath(path)
	}

	if entry.State == manifest.StateOrphaned {
		ra.Action = ActionOrphaned
		ra.Reason = "orphaned: " + entry.OrphanedReason
		return ra, nil
	}

	// Active manifest entry, no IR declaration → delete or stale-clean.
	st, err := fsys.Stat(path)
	if err != nil {
		return ra, fmt.Errorf("stat %s: %w", path, err)
	}
	if !st.Exists {
		ra.Action = ActionCleanup
		ra.Reason = "manifest entry without on-disk file"
		return ra, nil
	}
	ra.Action = ActionDelete
	ra.Reason = "owned, no longer declared"
	return ra, nil
}

// kindFromManifest converts a manifest-tracked Kind to the diff Kind. The
// manifest stores its own Kind constants for forwards/backwards compat;
// translating here keeps the manifest package independent of diff.
func kindFromManifest(k manifest.Kind) Kind {
	switch k {
	case manifest.KindUnit:
		return KindUnit
	default:
		return KindFile
	}
}

// UnitPath is the on-disk location magus owns a unit body at.
func UnitPath(unitName string) string {
	return filepath.Join("/etc/systemd/system", unitName)
}

// DropInPath is the on-disk location magus owns a drop-in at. Drop-ins live
// in <unit>.d/ siblings and are named per the policy precedence rule
// (10-magus.conf only) so they sort predictably.
func DropInPath(unitName, dropInName string) string {
	return filepath.Join("/etc/systemd/system", unitName+".d", dropInName)
}

// UnitNameFromPath recovers the systemd unit name from a managed path.
// Returns the unit's name for both unit-body paths
// (/etc/systemd/system/foo.service) and drop-in paths
// (/etc/systemd/system/foo.service.d/10-magus.conf).
func UnitNameFromPath(p string) string {
	parent := filepath.Base(filepath.Dir(p))
	if strings.HasSuffix(parent, ".d") {
		return strings.TrimSuffix(parent, ".d")
	}
	return filepath.Base(p)
}

// explainDiff produces a short reason string for update/conflict rows.
// Keeps things grep-friendly: "content differs", "mode differs", or both.
func explainDiff(contentMatch, metaMatch bool, st hostfs.FileInfo, d declaredResource) string {
	switch {
	case !contentMatch && !metaMatch:
		return "content and metadata differ"
	case !contentMatch:
		return "content differs"
	case st.Mode != d.Mode:
		return fmt.Sprintf("mode %#o → %#o", st.Mode, d.Mode)
	case d.UID != nil && st.UID != *d.UID, d.GID != nil && st.GID != *d.GID:
		return fmt.Sprintf("ownership %d:%d → %s", st.UID, st.GID, ownershipDesc(d.UID, d.GID))
	default:
		// Should not happen — caller only invokes this when something differs.
		return "differs"
	}
}

func hashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

// ownershipDesc renders an IR-side uid:gid where either may be unspecified
// ("*" means "no change") for explainDiff output.
func ownershipDesc(uid, gid *int) string {
	u := "*"
	if uid != nil {
		u = fmt.Sprintf("%d", *uid)
	}
	g := "*"
	if gid != nil {
		g = fmt.Sprintf("%d", *gid)
	}
	return u + ":" + g
}
