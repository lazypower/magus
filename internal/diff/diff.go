// Package diff joins IR ∪ manifest ∪ disk and computes the per-resource
// action magus should take. The join is the central decision point of the
// reconciler — see docs/spec-reconciler.md "Diff model".
package diff

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/lazypower/magus/internal/hostfs"
	"github.com/lazypower/magus/internal/ir"
	"github.com/lazypower/magus/internal/manifest"
)

// Action is the per-resource verb the planner picks.
type Action string

const (
	ActionCreate     Action = "create"
	ActionUpdate     Action = "update"
	ActionAdopt      Action = "adopt"
	ActionDelete     Action = "delete"
	ActionSkip       Action = "skip"
	ActionConflict Action = "conflict"
	ActionOrphaned Action = "orphaned"
	ActionCleanup  Action = "cleanup"
)

// Kind identifies what type of resource this action targets. v1 plans only
// emit "file" actions; units and directories surface as "deferred" so the
// plan output is honest about what's not yet implemented.
type Kind string

const (
	KindFile      Kind = "file"
	KindDirectory Kind = "directory"
	KindUnit      Kind = "unit"
)

// ResourceAction is one row of the plan. Hashes and modes are populated when
// they're useful for explaining the action — empty otherwise.
type ResourceAction struct {
	Path       string
	Kind       Kind
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
	// Deferred is a count of IR resources whose Kind isn't yet implemented
	// (directories, units in PR 2). Surfaced so the plan output is honest.
	Deferred int
}

// HasChanges reports whether anything other than skips/stale-clean would run.
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

// Compute joins the three inputs and produces a plan. fsys reads disk state;
// in normal operation pass hostfs.OS().
//
// v1 only diffs files. Directories and units in the IR are counted in
// Plan.Deferred so the CLI can disclose what's not yet handled.
func Compute(in *ir.IR, m *manifest.Manifest, fsys hostfs.Reader) (*Plan, error) {
	plan := &Plan{}
	declared := map[string]bool{}

	for _, f := range in.Files {
		declared[f.Path] = true
		ra, err := diffFile(f, m, fsys)
		if err != nil {
			return nil, err
		}
		plan.Actions = append(plan.Actions, ra)
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

	plan.Deferred = len(in.Directories) + len(in.Units)
	return plan, nil
}

func diffFile(f ir.File, m *manifest.Manifest, fsys hostfs.Reader) (ResourceAction, error) {
	ra := ResourceAction{
		Path:   f.Path,
		Kind:   KindFile,
		IRHash: hashBytes(f.Contents),
		IRMode: f.Mode,
	}

	// Orphan check first: a manifest orphan dominates everything else.
	if entry, ok := m.Get(f.Path); ok && entry.State == manifest.StateOrphaned {
		ra.Action = ActionOrphaned
		ra.Reason = "orphaned: " + entry.OrphanedReason
		return ra, nil
	}

	st, err := fsys.Stat(f.Path)
	if err != nil {
		return ra, fmt.Errorf("stat %s: %w", f.Path, err)
	}

	if !st.Exists {
		ra.Action = ActionCreate
		return ra, nil
	}

	ra.OnDiskMode = st.Mode

	body, err := fsys.ReadFile(f.Path)
	if err != nil {
		return ra, fmt.Errorf("read %s: %w", f.Path, err)
	}
	ra.OnDiskHash = hashBytes(body)

	contentMatch := ra.OnDiskHash == ra.IRHash
	metaMatch := st.Mode == f.Mode &&
		(f.UID == nil || st.UID == *f.UID) &&
		(f.GID == nil || st.GID == *f.GID)
	owned := m.Owns(f.Path)

	switch {
	case contentMatch && metaMatch && owned:
		ra.Action = ActionSkip
		ra.Reason = "unchanged"
	case contentMatch && metaMatch && !owned:
		ra.Action = ActionAdopt
		ra.Reason = "matches IR, claiming ownership"
	case owned:
		ra.Action = ActionUpdate
		ra.Reason = explainDiff(contentMatch, metaMatch, st, f)
	default:
		ra.Action = ActionConflict
		ra.Reason = "exists, " + explainDiff(contentMatch, metaMatch, st, f) + ", not in manifest"
	}
	return ra, nil
}

func diffOrphan(path string, entry manifest.Resource, fsys hostfs.Reader) (ResourceAction, error) {
	ra := ResourceAction{Path: path, Kind: KindFile}

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

// explainDiff produces a short reason string for update/conflict rows.
// Keeps things grep-friendly: "content differs", "mode differs", or both.
func explainDiff(contentMatch, metaMatch bool, st hostfs.FileInfo, f ir.File) string {
	switch {
	case !contentMatch && !metaMatch:
		return "content and metadata differ"
	case !contentMatch:
		return "content differs"
	case st.Mode != f.Mode:
		return fmt.Sprintf("mode %#o → %#o", st.Mode, f.Mode)
	case f.UID != nil && st.UID != *f.UID, f.GID != nil && st.GID != *f.GID:
		return fmt.Sprintf("ownership %d:%d → %s", st.UID, st.GID, ownershipDesc(f.UID, f.GID))
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
