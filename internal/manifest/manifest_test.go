package manifest

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadMissingReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	m, err := Load(filepath.Join(dir, "missing.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m.Version != CurrentVersion {
		t.Errorf("Version = %d, want %d", m.Version, CurrentVersion)
	}
	if len(m.Resources) != 0 {
		t.Errorf("Resources should be empty, got %v", m.Resources)
	}
}

func TestLoadRejectsWrongVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"resources":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("Load: want error on version mismatch, got nil")
	}
}

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")

	m := New()
	now := time.Date(2026, 5, 1, 18, 30, 0, 0, time.UTC)
	m.PutActive("/etc/magus.d/foo.env", KindFile, "sha256:abc", OriginCreate, now)
	m.PutActive("/etc/magus.d/bar.env", KindFile, "sha256:def", OriginAdopt, now)
	m.Orphan("/etc/magus.d/bar.env", "policy deny", now)

	if err := m.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !loaded.Owns("/etc/magus.d/foo.env") {
		t.Error("foo.env should be owned (active)")
	}
	if loaded.Owns("/etc/magus.d/bar.env") {
		t.Error("bar.env should NOT be owned (orphaned)")
	}
	r, ok := loaded.Get("/etc/magus.d/bar.env")
	if !ok {
		t.Fatal("bar.env should still exist in manifest as orphaned")
	}
	if r.State != StateOrphaned {
		t.Errorf("bar.env state = %s, want orphaned", r.State)
	}
	if r.OrphanedReason != "policy deny" {
		t.Errorf("bar.env orphaned_reason = %q", r.OrphanedReason)
	}
}

func TestSaveAtomic(t *testing.T) {
	// After Save, the .magus.tmp file should not be left behind.
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	m := New()
	m.PutActive("/x", KindFile, "sha256:abc", OriginCreate, time.Now())
	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".magus.tmp"); !os.IsNotExist(err) {
		t.Errorf("tmp file should be gone after Save, stat: %v", err)
	}
}
