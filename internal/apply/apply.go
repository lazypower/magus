// Package apply executes a plan: writes the changes diff computed and updates
// the manifest accordingly. Per-resource error handling per the spec — one
// failed resource does not halt the rest, and the worst outcome wins.
//
// See docs/spec-reconciler.md "Apply mechanics".
package apply

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/lazypower/magus/internal/diff"
	"github.com/lazypower/magus/internal/hostfs"
	"github.com/lazypower/magus/internal/ir"
	"github.com/lazypower/magus/internal/manifest"
)

// Status records what happened to one resource during apply.
type Status string

const (
	// StatusApplied — a change was successfully made (create, update, adopt,
	// delete, cleanup). The summary "Applied N changes" counts these.
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

// ExitCode picks per the spec: errors > skips > clean. Errors are exit 1,
// skips are exit 2, fully-converged is exit 0. Numeric ordering does not
// match severity (1 < 2 but 1 means worse) — that's intentional in the spec.
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

// Apply executes plan against w. The manifest is mutated in place; the caller
// is responsible for persisting it after Apply returns. `now` is injected so
// applied_at timestamps are deterministic in tests.
//
// Apply does not halt on per-resource failure — that's the reconciler-pattern
// posture from the spec. One bad resource doesn't take the system hostage.
func Apply(plan *diff.Plan, in *ir.IR, w hostfs.Writer, m *manifest.Manifest, now time.Time) *Result {
	files := indexFiles(in.Files)
	r := &Result{Outcomes: make([]Outcome, 0, len(plan.Actions))}
	for _, a := range plan.Actions {
		r.Outcomes = append(r.Outcomes, applyOne(a, files, w, m, now))
	}
	return r
}

func indexFiles(in []ir.File) map[string]ir.File {
	out := make(map[string]ir.File, len(in))
	for _, f := range in {
		out[f.Path] = f
	}
	return out
}

func applyOne(a diff.ResourceAction, files map[string]ir.File, w hostfs.Writer, m *manifest.Manifest, now time.Time) Outcome {
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
		f, ok := files[a.Path]
		if !ok {
			oc.Status = StatusErrored
			oc.Err = fmt.Errorf("internal: %s action references unknown IR path %s", a.Action, a.Path)
			return oc
		}
		if err := w.WriteFile(a.Path, f.Contents, f.Mode, f.UID, f.GID); err != nil {
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
		m.PutActive(a.Path, hashBytes(f.Contents), origin, now)
		oc.Status = StatusApplied
		return oc

	case diff.ActionAdopt:
		f, ok := files[a.Path]
		if !ok {
			oc.Status = StatusErrored
			oc.Err = fmt.Errorf("internal: adopt action references unknown IR path %s", a.Path)
			return oc
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
		if hashBytes(body) != hashBytes(f.Contents) {
			oc.Status = StatusSkipped
			oc.Reason = "drifted between plan and apply"
			return oc
		}
		m.PutActive(a.Path, hashBytes(f.Contents), manifest.OriginAdopt, now)
		oc.Status = StatusApplied
		oc.Reason = "adopted, no write"
		return oc

	default:
		oc.Status = StatusErrored
		oc.Err = fmt.Errorf("unknown action: %s", a.Action)
		return oc
	}
}

func hashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}
