package apply

import (
	"errors"
	"io/fs"
	"sync"
	"testing"
	"time"

	"github.com/lazypower/magus/internal/diff"
	"github.com/lazypower/magus/internal/hostfs"
	"github.com/lazypower/magus/internal/ir"
	"github.com/lazypower/magus/internal/manifest"
	"github.com/lazypower/magus/internal/systemd"
)

// memFile is one entry in the in-memory test filesystem.
type memFile struct {
	contents []byte
	mode     uint32
	uid, gid int
}

// memDir is one directory entry in the in-memory test filesystem.
type memDir struct {
	mode     uint32
	uid, gid int
}

// memWriter is a hostfs.Writer backed by maps. Test code can preload state,
// drive Apply, and inspect the resulting state. Optional injectErr causes the
// next mutating call against the matching path to fail — used to test
// per-resource error isolation.
type memWriter struct {
	mu        sync.Mutex
	files     map[string]memFile
	dirs      map[string]memDir
	injectErr map[string]error
}

func newMemWriter() *memWriter {
	return &memWriter{
		files:     map[string]memFile{},
		dirs:      map[string]memDir{},
		injectErr: map[string]error{},
	}
}

func (m *memWriter) preload(path string, f memFile) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[path] = f
}

func (m *memWriter) preloadDir(path string, d memDir) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dirs[path] = d
}

func (m *memWriter) injectError(path string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.injectErr[path] = err
}

func (m *memWriter) Stat(path string) (hostfs.FileInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f, ok := m.files[path]; ok {
		return hostfs.FileInfo{Exists: true, Mode: f.mode, UID: f.uid, GID: f.gid}, nil
	}
	if d, ok := m.dirs[path]; ok {
		return hostfs.FileInfo{Exists: true, Mode: d.mode, UID: d.uid, GID: d.gid}, nil
	}
	return hostfs.FileInfo{Exists: false}, nil
}

func (m *memWriter) ReadFile(path string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.files[path]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: path, Err: errors.New("not found")}
	}
	return append([]byte(nil), f.contents...), nil
}

func (m *memWriter) WriteFile(path string, contents []byte, mode uint32, uid, gid *int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err, ok := m.injectErr[path]; ok {
		delete(m.injectErr, path)
		return err
	}
	stored := memFile{contents: append([]byte(nil), contents...), mode: mode}
	if uid != nil {
		stored.uid = *uid
	}
	if gid != nil {
		stored.gid = *gid
	}
	m.files[path] = stored
	return nil
}

func (m *memWriter) Remove(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err, ok := m.injectErr[path]; ok {
		delete(m.injectErr, path)
		return err
	}
	delete(m.files, path)
	return nil
}

func (m *memWriter) Mkdir(path string, mode uint32, uid, gid *int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err, ok := m.injectErr[path]; ok {
		delete(m.injectErr, path)
		return err
	}
	d := memDir{mode: mode}
	if uid != nil {
		d.uid = *uid
	}
	if gid != nil {
		d.gid = *gid
	}
	m.dirs[path] = d
	return nil
}

func (m *memWriter) Chmod(path string, mode uint32) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err, ok := m.injectErr[path]; ok {
		delete(m.injectErr, path)
		return err
	}
	if d, ok := m.dirs[path]; ok {
		d.mode = mode
		m.dirs[path] = d
		return nil
	}
	if f, ok := m.files[path]; ok {
		f.mode = mode
		m.files[path] = f
		return nil
	}
	return &fs.PathError{Op: "chmod", Path: path, Err: errors.New("not found")}
}

func (m *memWriter) Chown(path string, uid, gid *int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err, ok := m.injectErr[path]; ok {
		delete(m.injectErr, path)
		return err
	}
	if d, ok := m.dirs[path]; ok {
		if uid != nil {
			d.uid = *uid
		}
		if gid != nil {
			d.gid = *gid
		}
		m.dirs[path] = d
		return nil
	}
	if f, ok := m.files[path]; ok {
		if uid != nil {
			f.uid = *uid
		}
		if gid != nil {
			f.gid = *gid
		}
		m.files[path] = f
		return nil
	}
	return &fs.PathError{Op: "chown", Path: path, Err: errors.New("not found")}
}

func TestCreate(t *testing.T) {
	w := newMemWriter()
	in := &ir.IR{Files: []ir.File{
		{Path: "/etc/magus.d/foo", Mode: 0o644, Contents: []byte("hello")},
	}}
	plan, _ := diff.Compute(in, manifest.New(), w)
	m := manifest.New()
	now := time.Now()
	r := Apply(plan, in, w, m, systemd.NewFake(), now)

	if a, _, _, _ := r.Counts(); a != 1 {
		t.Errorf("applied = %d, want 1", a)
	}
	if r.ExitCode() != 0 {
		t.Errorf("exit = %d, want 0", r.ExitCode())
	}
	got := w.files["/etc/magus.d/foo"]
	if string(got.contents) != "hello" {
		t.Errorf("contents = %q", got.contents)
	}
	if !m.Owns("/etc/magus.d/foo") {
		t.Error("manifest should own foo after create")
	}
	if entry, _ := m.Get("/etc/magus.d/foo"); entry.Origin != manifest.OriginCreate {
		t.Errorf("origin = %s, want create", entry.Origin)
	}
}

func TestUpdatePreservesAdoptOrigin(t *testing.T) {
	// A file originally adopted, then later updated, should retain
	// origin=adopt — the audit trail of how it entered the manifest is
	// content-independent.
	w := newMemWriter()
	w.preload("/etc/magus.d/foo", memFile{contents: []byte("old"), mode: 0o644})

	in := &ir.IR{Files: []ir.File{
		{Path: "/etc/magus.d/foo", Mode: 0o644, Contents: []byte("new")},
	}}
	m := manifest.New()
	m.PutActive("/etc/magus.d/foo", manifest.KindFile, "sha256:old", manifest.OriginAdopt, time.Now())

	plan, _ := diff.Compute(in, m, w)
	r := Apply(plan, in, w, m, systemd.NewFake(), time.Now())

	if r.ExitCode() != 0 {
		t.Fatalf("exit = %d, want 0", r.ExitCode())
	}
	if string(w.files["/etc/magus.d/foo"].contents) != "new" {
		t.Errorf("contents not updated")
	}
	entry, _ := m.Get("/etc/magus.d/foo")
	if entry.Origin != manifest.OriginAdopt {
		t.Errorf("origin = %s, want adopt", entry.Origin)
	}
}

func TestAdoptRecordsManifestNoWrite(t *testing.T) {
	w := newMemWriter()
	w.preload("/etc/magus.d/foo", memFile{contents: []byte("hi"), mode: 0o644})

	in := &ir.IR{Files: []ir.File{
		{Path: "/etc/magus.d/foo", Mode: 0o644, Contents: []byte("hi")},
	}}
	m := manifest.New()
	plan, _ := diff.Compute(in, m, w)
	if plan.Actions[0].Action != diff.ActionAdopt {
		t.Fatalf("plan action = %s, want adopt", plan.Actions[0].Action)
	}

	// Track whether WriteFile was called by injecting an error that would
	// surface if it were touched.
	w.injectError("/etc/magus.d/foo", errors.New("apply tried to write during adopt"))

	r := Apply(plan, in, w, m, systemd.NewFake(), time.Now())
	if r.ExitCode() != 0 {
		t.Fatalf("exit = %d, errors: %v", r.ExitCode(), r.Outcomes)
	}
	if !m.Owns("/etc/magus.d/foo") {
		t.Error("manifest should own foo after adopt")
	}
	entry, _ := m.Get("/etc/magus.d/foo")
	if entry.Origin != manifest.OriginAdopt {
		t.Errorf("origin = %s, want adopt", entry.Origin)
	}
}

func TestAdoptDriftSkipped(t *testing.T) {
	// File matches IR at plan time, but changes before apply runs. Adoption
	// must catch the drift and skip rather than silently take over divergent
	// content.
	w := newMemWriter()
	w.preload("/etc/magus.d/foo", memFile{contents: []byte("hi"), mode: 0o644})
	in := &ir.IR{Files: []ir.File{
		{Path: "/etc/magus.d/foo", Mode: 0o644, Contents: []byte("hi")},
	}}
	plan, _ := diff.Compute(in, manifest.New(), w)

	// Simulate drift: rewrite the file between plan and apply.
	w.preload("/etc/magus.d/foo", memFile{contents: []byte("DRIFTED"), mode: 0o644})

	m := manifest.New()
	r := Apply(plan, in, w, m, systemd.NewFake(), time.Now())
	if _, _, s, _ := r.Counts(); s != 1 {
		t.Errorf("skipped = %d, want 1", s)
	}
	if m.Owns("/etc/magus.d/foo") {
		t.Error("manifest should NOT own foo after drift-skip")
	}
}

func TestDelete(t *testing.T) {
	w := newMemWriter()
	w.preload("/etc/magus.d/gone", memFile{contents: []byte("x"), mode: 0o644})
	m := manifest.New()
	m.PutActive("/etc/magus.d/gone", manifest.KindFile, "sha256:x", manifest.OriginCreate, time.Now())

	plan, _ := diff.Compute(&ir.IR{}, m, w)
	r := Apply(plan, &ir.IR{}, w, m, systemd.NewFake(), time.Now())

	if r.ExitCode() != 0 {
		t.Fatalf("exit = %d", r.ExitCode())
	}
	if _, present := w.files["/etc/magus.d/gone"]; present {
		t.Error("file should be removed")
	}
	if _, ok := m.Get("/etc/magus.d/gone"); ok {
		t.Error("manifest entry should be removed")
	}
}

func TestCleanup(t *testing.T) {
	// Manifest claims a path that's already gone from disk. Apply should
	// clean the manifest entry without erroring.
	w := newMemWriter()
	m := manifest.New()
	m.PutActive("/etc/magus.d/ghost", manifest.KindFile, "sha256:x", manifest.OriginCreate, time.Now())

	plan, _ := diff.Compute(&ir.IR{}, m, w)
	r := Apply(plan, &ir.IR{}, w, m, systemd.NewFake(), time.Now())

	if r.ExitCode() != 0 {
		t.Fatalf("exit = %d", r.ExitCode())
	}
	if _, ok := m.Get("/etc/magus.d/ghost"); ok {
		t.Error("manifest entry should be cleaned")
	}
}

func TestConflictSkippedAndUntouched(t *testing.T) {
	w := newMemWriter()
	w.preload("/etc/magus.d/contested", memFile{contents: []byte("foreign"), mode: 0o644})
	in := &ir.IR{Files: []ir.File{
		{Path: "/etc/magus.d/contested", Mode: 0o644, Contents: []byte("ours")},
	}}
	plan, _ := diff.Compute(in, manifest.New(), w)
	m := manifest.New()
	r := Apply(plan, in, w, m, systemd.NewFake(), time.Now())

	if r.ExitCode() != 2 {
		t.Errorf("exit = %d, want 2 (conflict)", r.ExitCode())
	}
	if string(w.files["/etc/magus.d/contested"].contents) != "foreign" {
		t.Error("conflict file should be untouched")
	}
	if m.Owns("/etc/magus.d/contested") {
		t.Error("manifest should not record conflict path")
	}
}

func TestOrphanedSkipped(t *testing.T) {
	w := newMemWriter()
	w.preload("/etc/secret", memFile{contents: []byte("x"), mode: 0o600})
	m := manifest.New()
	m.PutActive("/etc/secret", manifest.KindFile, "sha256:x", manifest.OriginCreate, time.Now())
	m.Orphan("/etc/secret", "policy deny", time.Now())

	plan, _ := diff.Compute(&ir.IR{}, m, w)
	r := Apply(plan, &ir.IR{}, w, m, systemd.NewFake(), time.Now())

	if r.ExitCode() != 2 {
		t.Errorf("exit = %d, want 2", r.ExitCode())
	}
	// Orphaned files are never removed, regardless of IR state.
	if _, present := w.files["/etc/secret"]; !present {
		t.Error("orphaned file should not be deleted")
	}
}

func TestErrorIsolation(t *testing.T) {
	// One write fails — others must still run, manifest must reflect what
	// did succeed, and exit code must be 1 (errors dominate skips).
	w := newMemWriter()
	in := &ir.IR{Files: []ir.File{
		{Path: "/etc/magus.d/will-fail", Mode: 0o644, Contents: []byte("a")},
		{Path: "/etc/magus.d/will-succeed", Mode: 0o644, Contents: []byte("b")},
	}}
	w.injectError("/etc/magus.d/will-fail", errors.New("disk full"))

	plan, _ := diff.Compute(in, manifest.New(), w)
	m := manifest.New()
	r := Apply(plan, in, w, m, systemd.NewFake(), time.Now())

	if r.ExitCode() != 1 {
		t.Errorf("exit = %d, want 1 (errors dominate)", r.ExitCode())
	}
	if _, present := w.files["/etc/magus.d/will-succeed"]; !present {
		t.Error("non-failing resource should still be applied")
	}
	if m.Owns("/etc/magus.d/will-fail") {
		t.Error("failed resource should NOT be in manifest")
	}
	if !m.Owns("/etc/magus.d/will-succeed") {
		t.Error("succeeded resource SHOULD be in manifest")
	}
}

func TestExitCodePriorities(t *testing.T) {
	// Errors dominate skips dominate clean. Verify the priority order.
	cases := []struct {
		name     string
		statuses []Status
		want     int
	}{
		{"clean", []Status{StatusApplied, StatusUnchanged}, 0},
		{"skips-only", []Status{StatusApplied, StatusSkipped}, 2},
		{"errors-only", []Status{StatusApplied, StatusErrored}, 1},
		{"errors-and-skips", []Status{StatusSkipped, StatusErrored}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &Result{}
			for _, s := range c.statuses {
				r.Outcomes = append(r.Outcomes, Outcome{Status: s})
			}
			if got := r.ExitCode(); got != c.want {
				t.Errorf("ExitCode = %d, want %d", got, c.want)
			}
		})
	}
}
