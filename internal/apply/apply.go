// Package apply executes a plan: writes the changes diff computed and updates
// the manifest accordingly. Per-resource error handling per the spec — one
// failed resource does not halt the rest, and the worst outcome wins.
//
// See docs/spec-reconciler.md "Apply mechanics".
package apply

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/lazypower/magus/internal/diff"
	"github.com/lazypower/magus/internal/hostfs"
	"github.com/lazypower/magus/internal/ir"
	"github.com/lazypower/magus/internal/manifest"
	"github.com/lazypower/magus/internal/systemd"
)

// Status records what happened to one resource during apply.
type Status string

const (
	// StatusApplied — a change was successfully made (create, update, adopt,
	// delete, cleanup, daemon-reload, enable, restart). The summary
	// "Applied N changes" counts these.
	StatusApplied Status = "applied"
	// StatusUnchanged — the resource was already in its desired state. No-op.
	// Not surfaced in the summary; only in per-resource output.
	StatusUnchanged Status = "unchanged"
	// StatusSkipped — apply could not proceed (conflict, orphaned, or
	// adoption drift). Not an error; the resource simply couldn't converge.
	StatusSkipped Status = "skipped"
	// StatusErrored — a write or syscall failed mid-apply.
	StatusErrored Status = "errored"
)

// Outcome is the per-resource report apply emits. Reason is a one-line
// description suitable for human output; Err is set only for StatusErrored.
type Outcome struct {
	Path   string
	Action diff.Action
	Status Status
	Reason string
	Err    error
}

// Result is the collected outcome of one apply call.
type Result struct {
	Outcomes []Outcome
}

// Counts groups outcomes by Status for the summary line.
func (r *Result) Counts() (applied, unchanged, skipped, errored int) {
	for _, o := range r.Outcomes {
		switch o.Status {
		case StatusApplied:
			applied++
		case StatusUnchanged:
			unchanged++
		case StatusSkipped:
			skipped++
		case StatusErrored:
			errored++
		}
	}
	return
}

// ExitCode picks per the spec: errors > skips > clean.
func (r *Result) ExitCode() int {
	var hasSkip, hasError bool
	for _, o := range r.Outcomes {
		switch o.Status {
		case StatusSkipped:
			hasSkip = true
		case StatusErrored:
			hasError = true
		}
	}
	if hasError {
		return 1
	}
	if hasSkip {
		return 2
	}
	return 0
}

// pendingResource is a unified view of a path the IR declares. Files, units,
// and directories flatten to the same shape; Contents is empty for
// directories and the apply path branches on Kind when content matters
// (writes vs mkdir+chmod+chown).
type pendingResource struct {
	Path     string
	Mode     uint32
	UID, GID *int
	Contents []byte
	Kind     manifest.Kind
}

// Apply executes plan against w and sd. The manifest is mutated in place; the
// caller is responsible for persisting it after Apply returns. `now` is
// injected so applied_at timestamps are deterministic in tests.
//
// Apply runs in three phases per the spec:
//   1. Filesystem mutations. Unit body deletes are special-cased to
//      'systemctl disable --now' before unlink so enablement symlinks are
//      cleaned before the file disappears.
//   2. systemctl daemon-reload, exactly once, if any unit file was mutated.
//   3. Per-IR-unit state reconciliation: enablement, first-time start for
//      newly-created enabled units, and restart-if-active for content
//      changes.
//
// Per-resource errors do not halt — the reconciler-pattern posture from the
// spec. One bad resource does not take the system hostage.
func Apply(plan *diff.Plan, in *ir.IR, w hostfs.Writer, m *manifest.Manifest, sd systemd.Manager, now time.Time) *Result {
	resources := indexResources(in)
	r := &Result{Outcomes: make([]Outcome, 0, len(plan.Actions))}

	// Track which IR units had file mutations and what kind, so phase 3
	// can reconcile state intelligently.
	events := map[string]*unitEvents{}
	getEvents := func(name string) *unitEvents {
		if e, ok := events[name]; ok {
			return e
		}
		e := &unitEvents{}
		events[name] = e
		return e
	}

	// Phase 1a: unit body deletes need disable-now before unlink. Drop-in
	// deletes go through the standard path (just unlink + manifest cleanup).
	for _, a := range plan.Actions {
		if !isUnitBodyDelete(a) {
			continue
		}
		oc := applyUnitBodyDelete(a, w, m, sd)
		r.Outcomes = append(r.Outcomes, oc)
		if oc.Status == StatusApplied {
			getEvents(a.UnitName).bodyDeleted = true
		}
	}

	// Phase 1b: everything else (files + drop-ins + unit body create/update/adopt + skip/conflict/orphan/cleanup)
	for _, a := range plan.Actions {
		if isUnitBodyDelete(a) {
			continue // handled above
		}
		oc := applyOne(a, resources, w, m, now)
		r.Outcomes = append(r.Outcomes, oc)
		if a.Kind == diff.KindUnit && oc.Status == StatusApplied {
			ev := getEvents(a.UnitName)
			isBody := filepath.Base(a.Path) == a.UnitName
			recordUnitEvent(ev, a.Action, isBody)
		}
	}

	// Phase 2: daemon-reload if any unit file was mutated.
	if anyUnitMutation(events) {
		err := sd.DaemonReload()
		oc := Outcome{Path: "daemon-reload", Action: diff.ActionUpdate}
		if err != nil {
			oc.Status = StatusErrored
			oc.Err = err
		} else {
			oc.Status = StatusApplied
		}
		r.Outcomes = append(r.Outcomes, oc)
	}

	// Phase 3: per-IR-unit state reconciliation. Walks IR.Units (deleted
	// units are not in the IR, so they're not visited here).
	for _, u := range in.Units {
		ev := events[u.Name]
		if ev == nil {
			ev = &unitEvents{}
		}
		outcomes := reconcileUnitState(u, ev, sd)
		r.Outcomes = append(r.Outcomes, outcomes...)
	}

	return r
}

func indexResources(in *ir.IR) map[string]pendingResource {
	out := map[string]pendingResource{}
	for _, f := range in.Files {
		out[f.Path] = pendingResource{
			Path:     f.Path,
			Mode:     f.Mode,
			UID:      f.UID,
			GID:      f.GID,
			Contents: f.Contents,
			Kind:     manifest.KindFile,
		}
	}
	for _, u := range in.Units {
		if len(u.Contents) > 0 {
			path := diff.UnitPath(u.Name)
			out[path] = pendingResource{
				Path:     path,
				Mode:     0o644,
				Contents: []byte(u.Contents),
				Kind:     manifest.KindUnit,
			}
		}
		for _, di := range u.DropIns {
			path := diff.DropInPath(u.Name, di.Name)
			out[path] = pendingResource{
				Path:     path,
				Mode:     0o644,
				Contents: []byte(di.Contents),
				Kind:     manifest.KindUnit,
			}
		}
	}
	for _, d := range in.Directories {
		out[d.Path] = pendingResource{
			Path: d.Path,
			Mode: d.Mode,
			UID:  d.UID,
			GID:  d.GID,
			Kind: manifest.KindDirectory,
		}
	}
	return out
}

// isUnitBodyDelete reports whether a is the special "delete a unit's body
// file" case that requires disable-now before unlink. Drop-in deletes do
// not qualify — their parent unit's enablement is independent.
func isUnitBodyDelete(a diff.ResourceAction) bool {
	if a.Kind != diff.KindUnit || a.Action != diff.ActionDelete {
		return false
	}
	return filepath.Base(a.Path) == a.UnitName
}

// applyUnitBodyDelete runs disable --now then unlinks the unit body file.
// Either step's failure is reported as the outcome's error; if disable-now
// fails the file is NOT unlinked (that would orphan systemd's runtime state).
func applyUnitBodyDelete(a diff.ResourceAction, w hostfs.Writer, m *manifest.Manifest, sd systemd.Manager) Outcome {
	oc := Outcome{Path: a.Path, Action: a.Action}
	if err := sd.DisableNow(a.UnitName); err != nil {
		oc.Status = StatusErrored
		oc.Err = fmt.Errorf("disable --now %s: %w", a.UnitName, err)
		return oc
	}
	if err := w.Remove(a.Path); err != nil {
		oc.Status = StatusErrored
		oc.Err = err
		return oc
	}
	m.Delete(a.Path)
	oc.Status = StatusApplied
	oc.Reason = "disabled, stopped, removed"
	return oc
}

func applyOne(a diff.ResourceAction, resources map[string]pendingResource, w hostfs.Writer, m *manifest.Manifest, now time.Time) Outcome {
	oc := Outcome{Path: a.Path, Action: a.Action}

	switch a.Action {
	case diff.ActionSkip:
		oc.Status = StatusUnchanged
		oc.Reason = "unchanged"
		return oc

	case diff.ActionConflict, diff.ActionOrphaned:
		oc.Status = StatusSkipped
		oc.Reason = a.Reason
		return oc

	case diff.ActionCleanup:
		m.Delete(a.Path)
		oc.Status = StatusApplied
		oc.Reason = "manifest cleanup (file already gone)"
		return oc

	case diff.ActionDelete:
		// Plain file delete or drop-in delete. Unit body deletes are routed
		// through applyUnitBodyDelete by the caller.
		if err := w.Remove(a.Path); err != nil {
			oc.Status = StatusErrored
			oc.Err = err
			return oc
		}
		m.Delete(a.Path)
		oc.Status = StatusApplied
		oc.Reason = "removed"
		return oc

	case diff.ActionCreate, diff.ActionUpdate:
		r, ok := resources[a.Path]
		if !ok {
			oc.Status = StatusErrored
			oc.Err = fmt.Errorf("internal: %s action references unknown IR path %s", a.Action, a.Path)
			return oc
		}
		if r.Kind == manifest.KindDirectory {
			return applyDirectoryCreateOrUpdate(a, r, w, m, now)
		}
		if err := w.WriteFile(a.Path, r.Contents, r.Mode, r.UID, r.GID); err != nil {
			oc.Status = StatusErrored
			oc.Err = err
			return oc
		}
		// Updates preserve their original origin (create vs adopt) so the
		// audit trail isn't lost when content evolves.
		origin := manifest.OriginCreate
		if a.Action == diff.ActionUpdate {
			if existing, ok := m.Get(a.Path); ok {
				origin = existing.Origin
			}
		}
		m.PutActive(a.Path, r.Kind, diff.HashContent(r.Contents, diffKind(r.Kind)), origin, now)
		oc.Status = StatusApplied
		return oc

	case diff.ActionAdopt:
		r, ok := resources[a.Path]
		if !ok {
			oc.Status = StatusErrored
			oc.Err = fmt.Errorf("internal: adopt action references unknown IR path %s", a.Path)
			return oc
		}
		if r.Kind == manifest.KindDirectory {
			return applyDirectoryAdopt(a, r, w, m, now)
		}
		// Re-verify on-disk hash equals declared hash. Conditions may have
		// changed between plan and apply — adoption is bounded by exact
		// content match at apply-time, not just plan-time.
		body, err := w.ReadFile(a.Path)
		if err != nil {
			oc.Status = StatusErrored
			oc.Err = err
			return oc
		}
		if diff.HashContent(body, diffKind(r.Kind)) != diff.HashContent(r.Contents, diffKind(r.Kind)) {
			oc.Status = StatusSkipped
			oc.Reason = "drifted between plan and apply"
			return oc
		}
		m.PutActive(a.Path, r.Kind, diff.HashContent(r.Contents, diffKind(r.Kind)), manifest.OriginAdopt, now)
		oc.Status = StatusApplied
		oc.Reason = "adopted, no write"
		return oc

	default:
		oc.Status = StatusErrored
		oc.Err = fmt.Errorf("unknown action: %s", a.Action)
		return oc
	}
}

// applyDirectoryCreateOrUpdate handles the directory create and update paths.
// Create: mkdir -p with declared mode, chown if specified. Update: chmod
// and/or chown only — directory contents are never touched. The same hash
// sentinel ("sha256:dir") is recorded for all directory entries since
// content is not part of equivalence.
func applyDirectoryCreateOrUpdate(a diff.ResourceAction, r pendingResource, w hostfs.Writer, m *manifest.Manifest, now time.Time) Outcome {
	oc := Outcome{Path: a.Path, Action: a.Action}
	if a.Action == diff.ActionCreate {
		if err := w.Mkdir(a.Path, r.Mode, r.UID, r.GID); err != nil {
			oc.Status = StatusErrored
			oc.Err = err
			return oc
		}
	} else { // Update
		if err := w.Chmod(a.Path, r.Mode); err != nil {
			oc.Status = StatusErrored
			oc.Err = err
			return oc
		}
		if err := w.Chown(a.Path, r.UID, r.GID); err != nil {
			oc.Status = StatusErrored
			oc.Err = err
			return oc
		}
	}
	origin := manifest.OriginCreate
	if a.Action == diff.ActionUpdate {
		if existing, ok := m.Get(a.Path); ok {
			origin = existing.Origin
		}
	}
	m.PutActive(a.Path, manifest.KindDirectory, dirHash, origin, now)
	oc.Status = StatusApplied
	return oc
}

// applyDirectoryAdopt records ownership of an existing directory whose mode
// and ownership already match the IR. Adoption re-verifies metadata at apply
// time so a directory whose mode changed between plan and apply is skipped
// rather than silently taken over.
func applyDirectoryAdopt(a diff.ResourceAction, r pendingResource, w hostfs.Writer, m *manifest.Manifest, now time.Time) Outcome {
	oc := Outcome{Path: a.Path, Action: a.Action}
	st, err := w.Stat(a.Path)
	if err != nil {
		oc.Status = StatusErrored
		oc.Err = err
		return oc
	}
	if !st.Exists ||
		st.Mode != r.Mode ||
		(r.UID != nil && st.UID != *r.UID) ||
		(r.GID != nil && st.GID != *r.GID) {
		oc.Status = StatusSkipped
		oc.Reason = "drifted between plan and apply"
		return oc
	}
	m.PutActive(a.Path, manifest.KindDirectory, dirHash, manifest.OriginAdopt, now)
	oc.Status = StatusApplied
	oc.Reason = "adopted, no write"
	return oc
}

// dirHash is the sentinel manifest hash for directory entries. Directories
// have no content equivalence; the hash field is populated for schema
// consistency only.
const dirHash = "sha256:dir"

// diffKind translates a manifest.Kind to a diff.Kind for hash computation.
// They mirror each other but live in separate packages to avoid coupling.
func diffKind(k manifest.Kind) diff.Kind {
	switch k {
	case manifest.KindUnit:
		return diff.KindUnit
	case manifest.KindDirectory:
		return diff.KindDirectory
	default:
		return diff.KindFile
	}
}

// unitEvents tracks what happened to a unit's files during phase 1, so phase 3
// can decide whether to enable, disable, restart, or skip.
type unitEvents struct {
	bodyCreated   bool
	bodyUpdated   bool
	bodyAdopted   bool
	bodyDeleted   bool
	dropInChange  bool // any drop-in create/update/delete (not adopt)
	hasContentMut bool // any mutation that requires daemon-reload (excludes adopts)
}

func recordUnitEvent(ev *unitEvents, action diff.Action, isBody bool) {
	switch action {
	case diff.ActionCreate:
		if isBody {
			ev.bodyCreated = true
		} else {
			ev.dropInChange = true
		}
		ev.hasContentMut = true
	case diff.ActionUpdate:
		if isBody {
			ev.bodyUpdated = true
		} else {
			ev.dropInChange = true
		}
		ev.hasContentMut = true
	case diff.ActionAdopt:
		if isBody {
			ev.bodyAdopted = true
		}
		// adopts don't trigger daemon-reload or restart
	case diff.ActionDelete:
		// Drop-in deletes get here (body deletes are routed elsewhere)
		ev.dropInChange = true
		ev.hasContentMut = true
	}
}

func anyUnitMutation(events map[string]*unitEvents) bool {
	for _, ev := range events {
		if ev.hasContentMut || ev.bodyDeleted {
			return true
		}
	}
	return false
}

// reconcileUnitState drives systemd state for one IR unit after files +
// daemon-reload have settled. Returns one outcome per systemctl operation
// performed (so the apply output mirrors the spec example's per-line format).
func reconcileUnitState(u ir.Unit, ev *unitEvents, sd systemd.Manager) []Outcome {
	if ev.bodyDeleted {
		// Disable+stop already happened in phase 1; nothing more to do.
		return nil
	}
	var outcomes []Outcome

	// Newly-created units: combine enable + start if declared enabled.
	// Disabled-on-create units are written but not started.
	if ev.bodyCreated {
		if u.Enabled {
			err := sd.EnableNow(u.Name)
			outcomes = append(outcomes, unitOutcome(u.Name, "enable --now", err))
		}
		return outcomes
	}

	// Existing unit (not newly created): reconcile enablement every apply.
	current, err := sd.IsEnabled(u.Name)
	if err != nil {
		outcomes = append(outcomes, unitOutcome(u.Name, "is-enabled", err))
	} else {
		switch {
		case u.Enabled && (current == systemd.EnablementDisabled || current == systemd.EnablementUnknown):
			err := sd.Enable(u.Name)
			outcomes = append(outcomes, unitOutcome(u.Name, "enable", err))
		case !u.Enabled && current == systemd.EnablementEnabled:
			err := sd.Disable(u.Name)
			outcomes = append(outcomes, unitOutcome(u.Name, "disable", err))
		}
	}

	// Restart-if-active for content changes (excludes adopts and skips).
	// Inactive units whose content changed are rewritten only — the new
	// content takes effect on next start. Logged for visibility.
	if ev.hasContentMut {
		active, _ := sd.IsActive(u.Name)
		if active {
			err := sd.Restart(u.Name)
			outcomes = append(outcomes, unitOutcome(u.Name, "restart", err))
		} else {
			outcomes = append(outcomes, Outcome{
				Path:   u.Name,
				Action: diff.ActionUpdate,
				Status: StatusApplied,
				Reason: "content updated, inactive — change takes effect on next start",
			})
		}
	}

	return outcomes
}

func unitOutcome(unit, op string, err error) Outcome {
	oc := Outcome{Path: unit, Action: diff.ActionUpdate, Reason: op}
	if err != nil {
		oc.Status = StatusErrored
		oc.Err = err
	} else {
		oc.Status = StatusApplied
	}
	return oc
}
