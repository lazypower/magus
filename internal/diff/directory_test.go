package diff

import (
	"testing"
	"time"

	"github.com/lazypower/magus/internal/ir"
	"github.com/lazypower/magus/internal/manifest"
)

func TestDirectoryCreate(t *testing.T) {
	in := &ir.IR{Directories: []ir.Directory{
		{Path: "/var/lib/magus", Mode: 0o755},
	}}
	plan, err := Compute(in, manifest.New(), memFS{})
	if err != nil {
		t.Fatal(err)
	}
	a := findAction(t, plan, "/var/lib/magus")
	if a.Action != ActionCreate {
		t.Errorf("Action = %s, want create", a.Action)
	}
	if a.Kind != KindDirectory {
		t.Errorf("Kind = %s, want directory", a.Kind)
	}
}

func TestDirectorySkipWhenMetadataMatches(t *testing.T) {
	in := &ir.IR{Directories: []ir.Directory{
		{Path: "/var/lib/magus", Mode: 0o755},
	}}
	m := manifest.New()
	m.PutActive("/var/lib/magus", manifest.KindDirectory, "sha256:dir", manifest.OriginCreate, time.Now())

	plan, err := Compute(in, m, memFS{
		"/var/lib/magus": {isDir: true, mode: 0o755},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := findAction(t, plan, "/var/lib/magus")
	if a.Action != ActionSkip {
		t.Errorf("Action = %s (%s), want skip", a.Action, a.Reason)
	}
}

func TestDirectoryAdoptWhenMatchingAndUnowned(t *testing.T) {
	in := &ir.IR{Directories: []ir.Directory{
		{Path: "/var/lib/magus", Mode: 0o755},
	}}
	plan, err := Compute(in, manifest.New(), memFS{
		"/var/lib/magus": {isDir: true, mode: 0o755},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := findAction(t, plan, "/var/lib/magus")
	if a.Action != ActionAdopt {
		t.Errorf("Action = %s (%s), want adopt", a.Action, a.Reason)
	}
}

func TestDirectoryUpdateWhenModeDiffers(t *testing.T) {
	in := &ir.IR{Directories: []ir.Directory{
		{Path: "/var/lib/magus", Mode: 0o700},
	}}
	m := manifest.New()
	m.PutActive("/var/lib/magus", manifest.KindDirectory, "sha256:dir", manifest.OriginCreate, time.Now())

	plan, _ := Compute(in, m, memFS{
		"/var/lib/magus": {isDir: true, mode: 0o755},
	})
	a := findAction(t, plan, "/var/lib/magus")
	if a.Action != ActionUpdate {
		t.Errorf("Action = %s (%s), want update", a.Action, a.Reason)
	}
}

func TestDirectoryConflictWhenMetadataDiffersAndUnowned(t *testing.T) {
	in := &ir.IR{Directories: []ir.Directory{
		{Path: "/var/lib/magus", Mode: 0o755},
	}}
	plan, _ := Compute(in, manifest.New(), memFS{
		"/var/lib/magus": {isDir: true, mode: 0o700},
	})
	a := findAction(t, plan, "/var/lib/magus")
	if a.Action != ActionConflict {
		t.Errorf("Action = %s (%s), want conflict", a.Action, a.Reason)
	}
}

func TestDirectoryNotDeletedFromIROmission(t *testing.T) {
	// Manifest has a directory entry, IR doesn't declare it, dir still
	// exists on disk. Per spec: directories are never deleted in v1.
	// Action must be Skip with a reason explaining the asymmetry.
	m := manifest.New()
	m.PutActive("/var/lib/magus", manifest.KindDirectory, "sha256:dir", manifest.OriginCreate, time.Now())

	plan, _ := Compute(&ir.IR{}, m, memFS{
		"/var/lib/magus": {isDir: true, mode: 0o755},
	})
	a := findAction(t, plan, "/var/lib/magus")
	if a.Action != ActionSkip {
		t.Errorf("Action = %s (%s), want skip — directories must not be deleted", a.Action, a.Reason)
	}
}

func TestDirectoryStaleCleanupWhenManuallyRemoved(t *testing.T) {
	// Manifest has a directory, IR omits it, AND the dir is gone from
	// disk (operator removed it manually). That's stale-clean: the manifest
	// entry can be removed without filesystem action.
	m := manifest.New()
	m.PutActive("/var/lib/magus", manifest.KindDirectory, "sha256:dir", manifest.OriginCreate, time.Now())

	plan, _ := Compute(&ir.IR{}, m, memFS{})
	a := findAction(t, plan, "/var/lib/magus")
	if a.Action != ActionCleanup {
		t.Errorf("Action = %s (%s), want cleanup (stale manifest entry)", a.Action, a.Reason)
	}
}
