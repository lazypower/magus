package apply

import (
	"testing"
	"time"

	"github.com/lazypower/magus/internal/diff"
	"github.com/lazypower/magus/internal/ir"
	"github.com/lazypower/magus/internal/manifest"
	"github.com/lazypower/magus/internal/systemd"
)

func TestDirectoryCreate(t *testing.T) {
	w := newMemWriter()
	in := &ir.IR{Directories: []ir.Directory{
		{Path: "/var/lib/magus", Mode: 0o755},
	}}
	plan, _ := diff.Compute(in, manifest.New(), w)
	m := manifest.New()
	r := Apply(plan, in, w, m, systemd.NewFake(), time.Now())

	if r.ExitCode() != 0 {
		t.Fatalf("exit = %d; outcomes: %+v", r.ExitCode(), r.Outcomes)
	}
	d, ok := w.dirs["/var/lib/magus"]
	if !ok {
		t.Fatal("directory not created in memWriter")
	}
	if d.mode != 0o755 {
		t.Errorf("mode = %#o, want 0755", d.mode)
	}
	entry, ok := m.Get("/var/lib/magus")
	if !ok {
		t.Fatal("manifest entry not created")
	}
	if entry.Kind != manifest.KindDirectory {
		t.Errorf("Kind = %s, want directory", entry.Kind)
	}
}

func TestDirectoryUpdateChangesModeOnly(t *testing.T) {
	// Existing directory with wrong mode: Apply must Chmod (not Mkdir)
	// and must NOT touch directory contents.
	w := newMemWriter()
	w.preloadDir("/var/lib/magus", memDir{mode: 0o755})

	in := &ir.IR{Directories: []ir.Directory{
		{Path: "/var/lib/magus", Mode: 0o700},
	}}
	m := manifest.New()
	m.PutActive("/var/lib/magus", manifest.KindDirectory, "sha256:dir", manifest.OriginCreate, time.Now())

	plan, _ := diff.Compute(in, m, w)
	r := Apply(plan, in, w, m, systemd.NewFake(), time.Now())

	if r.ExitCode() != 0 {
		t.Fatalf("exit = %d", r.ExitCode())
	}
	if w.dirs["/var/lib/magus"].mode != 0o700 {
		t.Errorf("mode after update = %#o, want 0700", w.dirs["/var/lib/magus"].mode)
	}
}

func TestDirectoryAdopt(t *testing.T) {
	w := newMemWriter()
	w.preloadDir("/var/lib/magus", memDir{mode: 0o755})

	in := &ir.IR{Directories: []ir.Directory{
		{Path: "/var/lib/magus", Mode: 0o755},
	}}
	plan, _ := diff.Compute(in, manifest.New(), w)
	if plan.Actions[0].Action != diff.ActionAdopt {
		t.Fatalf("expected adopt, got %s", plan.Actions[0].Action)
	}
	m := manifest.New()
	r := Apply(plan, in, w, m, systemd.NewFake(), time.Now())

	if r.ExitCode() != 0 {
		t.Fatalf("exit = %d", r.ExitCode())
	}
	entry, _ := m.Get("/var/lib/magus")
	if entry.Origin != manifest.OriginAdopt {
		t.Errorf("origin = %s, want adopt", entry.Origin)
	}
}

func TestDirectoryAdoptDriftSkipped(t *testing.T) {
	// Directory matched IR at plan time, but mode changed before apply.
	// Adoption must catch the drift and skip rather than silently take over.
	w := newMemWriter()
	w.preloadDir("/var/lib/magus", memDir{mode: 0o755})
	in := &ir.IR{Directories: []ir.Directory{
		{Path: "/var/lib/magus", Mode: 0o755},
	}}
	plan, _ := diff.Compute(in, manifest.New(), w)

	// Drift: mode changed between plan and apply.
	w.preloadDir("/var/lib/magus", memDir{mode: 0o700})

	m := manifest.New()
	r := Apply(plan, in, w, m, systemd.NewFake(), time.Now())

	if _, _, s, _ := r.Counts(); s != 1 {
		t.Errorf("skipped = %d, want 1", s)
	}
	if m.Owns("/var/lib/magus") {
		t.Error("manifest should NOT own directory after drift skip")
	}
}

func TestDirectoryNotDeletedOnIROmission(t *testing.T) {
	// Spec: v1 never deletes directories. The skip action emitted by diff
	// for this case must produce a no-op outcome — manifest entry preserved,
	// directory untouched.
	w := newMemWriter()
	w.preloadDir("/var/data", memDir{mode: 0o755})

	m := manifest.New()
	m.PutActive("/var/data", manifest.KindDirectory, "sha256:dir", manifest.OriginCreate, time.Now())

	plan, _ := diff.Compute(&ir.IR{}, m, w)
	Apply(plan, &ir.IR{}, w, m, systemd.NewFake(), time.Now())

	if _, ok := w.dirs["/var/data"]; !ok {
		t.Error("directory should not be removed")
	}
	if !m.Owns("/var/data") {
		t.Error("manifest entry should be preserved")
	}
}
