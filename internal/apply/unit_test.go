package apply

import (
	"errors"
	"testing"
	"time"

	"github.com/lazypower/magus/internal/diff"
	"github.com/lazypower/magus/internal/ir"
	"github.com/lazypower/magus/internal/manifest"
	"github.com/lazypower/magus/internal/systemd"
)

const sampleUnitBody = "[Unit]\nDescription=test\n[Service]\nExecStart=/bin/foo\n[Install]\nWantedBy=multi-user.target\n"

// containsCall reports whether systemd's call log contains the given call.
func containsCall(calls []string, want string) bool {
	for _, c := range calls {
		if c == want {
			return true
		}
	}
	return false
}

func TestUnitCreateEnabledRunsEnableNow(t *testing.T) {
	w := newMemWriter()
	sd := systemd.NewFake()
	in := &ir.IR{Units: []ir.Unit{
		{Name: "magus-foo.service", Enabled: true, Contents: sampleUnitBody},
	}}
	plan, _ := diff.Compute(in, manifest.New(), w)
	m := manifest.New()
	r := Apply(plan, in, w, m, sd, time.Now())

	if r.ExitCode() != 0 {
		t.Fatalf("exit = %d, want 0; outcomes: %v", r.ExitCode(), r.Outcomes)
	}
	calls := sd.Calls()
	if !containsCall(calls, "DaemonReload") {
		t.Errorf("expected DaemonReload, got: %v", calls)
	}
	if !containsCall(calls, "EnableNow(magus-foo.service)") {
		t.Errorf("expected EnableNow, got: %v", calls)
	}
	if !m.Owns(diff.UnitPath("magus-foo.service")) {
		t.Error("manifest should own unit body after create")
	}
	entry, _ := m.Get(diff.UnitPath("magus-foo.service"))
	if entry.Kind != manifest.KindUnit {
		t.Errorf("manifest entry Kind = %s, want unit", entry.Kind)
	}
}

func TestUnitCreateDisabledDoesNotStart(t *testing.T) {
	w := newMemWriter()
	sd := systemd.NewFake()
	in := &ir.IR{Units: []ir.Unit{
		{Name: "magus-foo.service", Enabled: false, Contents: sampleUnitBody},
	}}
	plan, _ := diff.Compute(in, manifest.New(), w)
	r := Apply(plan, in, w, manifest.New(), sd, time.Now())

	if r.ExitCode() != 0 {
		t.Fatalf("exit = %d", r.ExitCode())
	}
	calls := sd.Calls()
	for _, c := range calls {
		if c == "EnableNow(magus-foo.service)" || c == "Enable(magus-foo.service)" {
			t.Errorf("disabled-on-create unit should not be enabled, got: %v", calls)
		}
	}
	if !containsCall(calls, "DaemonReload") {
		t.Error("daemon-reload should still run for the file write")
	}
}

func TestUnitUpdateActiveTriggersRestart(t *testing.T) {
	// Unit owned, content differs, IsActive=true → after daemon-reload,
	// Restart should be called.
	w := newMemWriter()
	sd := systemd.NewFake()
	sd.SetEnablement("magus-foo.service", systemd.EnablementEnabled)
	sd.SetActive("magus-foo.service", true)

	path := diff.UnitPath("magus-foo.service")
	w.preload(path, memFile{contents: []byte("[Service]\nExecStart=/bin/old\n"), mode: 0o644})

	in := &ir.IR{Units: []ir.Unit{
		{Name: "magus-foo.service", Enabled: true, Contents: sampleUnitBody},
	}}
	m := manifest.New()
	m.PutActive(path, manifest.KindUnit, "sha256:old", manifest.OriginCreate, time.Now())

	plan, _ := diff.Compute(in, m, w)
	r := Apply(plan, in, w, m, sd, time.Now())

	if r.ExitCode() != 0 {
		t.Fatalf("exit = %d; outcomes: %+v", r.ExitCode(), r.Outcomes)
	}
	calls := sd.Calls()
	// Must run daemon-reload first, then Restart. Order matters.
	reloadIdx, restartIdx := -1, -1
	for i, c := range calls {
		switch c {
		case "DaemonReload":
			reloadIdx = i
		case "Restart(magus-foo.service)":
			restartIdx = i
		}
	}
	if reloadIdx < 0 || restartIdx < 0 {
		t.Errorf("expected DaemonReload + Restart, got: %v", calls)
	}
	if reloadIdx > restartIdx {
		t.Errorf("DaemonReload must come before Restart, got: %v", calls)
	}
}

func TestUnitUpdateInactiveDefersStart(t *testing.T) {
	w := newMemWriter()
	sd := systemd.NewFake()
	sd.SetEnablement("magus-foo.service", systemd.EnablementEnabled)
	sd.SetActive("magus-foo.service", false)

	path := diff.UnitPath("magus-foo.service")
	w.preload(path, memFile{contents: []byte("[Service]\nExecStart=/bin/old\n"), mode: 0o644})

	in := &ir.IR{Units: []ir.Unit{
		{Name: "magus-foo.service", Enabled: true, Contents: sampleUnitBody},
	}}
	m := manifest.New()
	m.PutActive(path, manifest.KindUnit, "sha256:old", manifest.OriginCreate, time.Now())

	plan, _ := diff.Compute(in, m, w)
	r := Apply(plan, in, w, m, sd, time.Now())

	calls := sd.Calls()
	for _, c := range calls {
		if c == "Restart(magus-foo.service)" {
			t.Errorf("inactive unit should NOT be restarted, got: %v", calls)
		}
	}
	// Should emit a deferred-content outcome for visibility.
	found := false
	for _, oc := range r.Outcomes {
		if oc.Reason == "content updated, inactive — change takes effect on next start" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected deferred-content outcome, got none")
	}
}

func TestUnitAdoptNoDaemonReloadNoRestart(t *testing.T) {
	// Unit content already matches IR; adoption records ownership but
	// performs no write, no daemon-reload, no restart.
	w := newMemWriter()
	sd := systemd.NewFake()
	sd.SetEnablement("magus-foo.service", systemd.EnablementEnabled)
	sd.SetActive("magus-foo.service", true)

	path := diff.UnitPath("magus-foo.service")
	w.preload(path, memFile{contents: []byte(sampleUnitBody), mode: 0o644})

	in := &ir.IR{Units: []ir.Unit{
		{Name: "magus-foo.service", Enabled: true, Contents: sampleUnitBody},
	}}
	plan, _ := diff.Compute(in, manifest.New(), w)
	if plan.Actions[0].Action != diff.ActionAdopt {
		t.Fatalf("expected adopt, got: %s", plan.Actions[0].Action)
	}

	m := manifest.New()
	r := Apply(plan, in, w, m, sd, time.Now())

	if r.ExitCode() != 0 {
		t.Fatalf("exit = %d", r.ExitCode())
	}
	calls := sd.Calls()
	for _, c := range calls {
		if c == "DaemonReload" || c == "Restart(magus-foo.service)" {
			t.Errorf("adopt should not trigger %s, got: %v", c, calls)
		}
	}
	// Manifest should now own the path with origin=adopt.
	entry, ok := m.Get(path)
	if !ok || entry.Origin != manifest.OriginAdopt {
		t.Errorf("expected adopt origin, got: %+v", entry)
	}
}

func TestUnitDeleteRunsDisableNowBeforeUnlink(t *testing.T) {
	w := newMemWriter()
	sd := systemd.NewFake()
	sd.SetEnablement("magus-old.service", systemd.EnablementEnabled)
	sd.SetActive("magus-old.service", true)

	path := diff.UnitPath("magus-old.service")
	w.preload(path, memFile{contents: []byte(sampleUnitBody), mode: 0o644})

	m := manifest.New()
	m.PutActive(path, manifest.KindUnit, "sha256:x", manifest.OriginCreate, time.Now())

	plan, _ := diff.Compute(&ir.IR{}, m, w)
	r := Apply(plan, &ir.IR{}, w, m, sd, time.Now())

	if r.ExitCode() != 0 {
		t.Fatalf("exit = %d; outcomes: %+v", r.ExitCode(), r.Outcomes)
	}
	calls := sd.Calls()

	// DisableNow must precede the unlink (file is still in memWriter at
	// the time DisableNow runs). The fake doesn't enforce ordering
	// directly, but the call sequence we can observe must have
	// DisableNow come before DaemonReload (which only runs after files
	// are written/unlinked).
	disableIdx, reloadIdx := -1, -1
	for i, c := range calls {
		switch c {
		case "DisableNow(magus-old.service)":
			disableIdx = i
		case "DaemonReload":
			reloadIdx = i
		}
	}
	if disableIdx < 0 {
		t.Fatalf("expected DisableNow, got: %v", calls)
	}
	if reloadIdx < 0 {
		t.Fatalf("expected DaemonReload after delete, got: %v", calls)
	}
	if disableIdx > reloadIdx {
		t.Errorf("DisableNow must come before DaemonReload, got: %v", calls)
	}

	// File should be removed and manifest entry gone.
	if _, present := w.files[path]; present {
		t.Error("file should be removed")
	}
	if _, ok := m.Get(path); ok {
		t.Error("manifest entry should be removed")
	}
}

func TestDropInChangeTriggersDaemonReloadAndRestartParent(t *testing.T) {
	// Only a drop-in changed; the parent unit's body wasn't touched. Apply
	// must still daemon-reload and restart the active parent unit.
	w := newMemWriter()
	sd := systemd.NewFake()
	sd.SetEnablement("ssh.service", systemd.EnablementEnabled)
	sd.SetActive("ssh.service", true)

	in := &ir.IR{Units: []ir.Unit{
		{
			Name:    "ssh.service",
			Enabled: true,
			DropIns: []ir.DropIn{
				{Name: "10-magus.conf", Contents: "[Service]\nEnvironment=X=1\n"},
			},
		},
	}}
	plan, _ := diff.Compute(in, manifest.New(), w)
	m := manifest.New()
	r := Apply(plan, in, w, m, sd, time.Now())

	if r.ExitCode() != 0 {
		t.Fatalf("exit = %d", r.ExitCode())
	}
	calls := sd.Calls()
	if !containsCall(calls, "DaemonReload") {
		t.Error("drop-in change must trigger DaemonReload")
	}
	if !containsCall(calls, "Restart(ssh.service)") {
		t.Error("active parent unit must be restarted on drop-in change")
	}
}

func TestSingleDaemonReloadAcrossMultipleUnits(t *testing.T) {
	// Two units, both new — daemon-reload must run exactly once.
	w := newMemWriter()
	sd := systemd.NewFake()
	in := &ir.IR{Units: []ir.Unit{
		{Name: "magus-a.service", Enabled: false, Contents: sampleUnitBody},
		{Name: "magus-b.service", Enabled: false, Contents: sampleUnitBody},
	}}
	plan, _ := diff.Compute(in, manifest.New(), w)
	r := Apply(plan, in, w, manifest.New(), sd, time.Now())

	if r.ExitCode() != 0 {
		t.Fatalf("exit = %d", r.ExitCode())
	}
	count := 0
	for _, c := range sd.Calls() {
		if c == "DaemonReload" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("DaemonReload called %d times, want exactly 1", count)
	}
}

func TestEnablementDriftReconciledOnExistingUnit(t *testing.T) {
	// Unit owned, no content change, but on-disk says "disabled" while IR
	// declares enabled=true. Apply should call Enable to converge.
	w := newMemWriter()
	sd := systemd.NewFake()
	sd.SetEnablement("magus-foo.service", systemd.EnablementDisabled)

	path := diff.UnitPath("magus-foo.service")
	w.preload(path, memFile{contents: []byte(sampleUnitBody), mode: 0o644})

	in := &ir.IR{Units: []ir.Unit{
		{Name: "magus-foo.service", Enabled: true, Contents: sampleUnitBody},
	}}
	m := manifest.New()
	m.PutActive(path, manifest.KindUnit, diff.HashContent([]byte(sampleUnitBody), diff.KindUnit), manifest.OriginCreate, time.Now())

	plan, _ := diff.Compute(in, m, w)
	r := Apply(plan, in, w, m, sd, time.Now())

	if r.ExitCode() != 0 {
		t.Fatalf("exit = %d", r.ExitCode())
	}
	if !containsCall(sd.Calls(), "Enable(magus-foo.service)") {
		t.Errorf("expected Enable to be called for drift, got: %v", sd.Calls())
	}
}

func TestSystemdErrorIsolation(t *testing.T) {
	// EnableNow fails for unit A; unit B should still be processed.
	w := newMemWriter()
	sd := systemd.NewFake()
	sd.FailNext("EnableNow(magus-a.service)", errors.New("boom"))

	in := &ir.IR{Units: []ir.Unit{
		{Name: "magus-a.service", Enabled: true, Contents: sampleUnitBody},
		{Name: "magus-b.service", Enabled: true, Contents: sampleUnitBody},
	}}
	plan, _ := diff.Compute(in, manifest.New(), w)
	r := Apply(plan, in, w, manifest.New(), sd, time.Now())

	if r.ExitCode() != 1 {
		t.Errorf("exit = %d, want 1 (errors dominate)", r.ExitCode())
	}
	// b should still be enabled+started despite a's failure.
	if !containsCall(sd.Calls(), "EnableNow(magus-b.service)") {
		t.Errorf("unit b must still be processed, got: %v", sd.Calls())
	}
}
