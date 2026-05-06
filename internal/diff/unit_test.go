package diff

import (
	"testing"
	"time"

	"github.com/lazypower/magus/internal/ir"
	"github.com/lazypower/magus/internal/manifest"
)

func TestUnitCreate(t *testing.T) {
	in := &ir.IR{Units: []ir.Unit{
		{
			Name:     "magus-foo.service",
			Enabled:  true,
			Contents: "[Unit]\nDescription=foo\n[Service]\nExecStart=/bin/foo\n",
		},
	}}
	plan, err := Compute(in, manifest.New(), memFS{})
	if err != nil {
		t.Fatal(err)
	}
	a := findAction(t, plan, UnitPath("magus-foo.service"))
	if a.Action != ActionCreate {
		t.Errorf("Action = %s, want create", a.Action)
	}
	if a.Kind != KindUnit {
		t.Errorf("Kind = %s, want unit", a.Kind)
	}
	if a.UnitName != "magus-foo.service" {
		t.Errorf("UnitName = %q", a.UnitName)
	}
}

func TestUnitDropInPath(t *testing.T) {
	in := &ir.IR{Units: []ir.Unit{
		{
			Name: "ssh.service",
			DropIns: []ir.DropIn{
				{Name: "10-magus.conf", Contents: "[Service]\nEnvironment=X=1\n"},
			},
		},
	}}
	plan, err := Compute(in, manifest.New(), memFS{})
	if err != nil {
		t.Fatal(err)
	}
	expected := DropInPath("ssh.service", "10-magus.conf")
	a := findAction(t, plan, expected)
	if a.Action != ActionCreate {
		t.Errorf("Action = %s, want create", a.Action)
	}
	if a.UnitName != "ssh.service" {
		t.Errorf("UnitName = %q (drop-in must reference parent unit)", a.UnitName)
	}
}

func TestUnitSkipWhenCanonicallyEqual(t *testing.T) {
	// On-disk content has whitespace and comments that the IR doesn't, but
	// they canonicalize to the same bytes. Equivalence should hold and the
	// action should be skip — not update or conflict.
	irContent := "[Unit]\nDescription=foo\n[Service]\nExecStart=/bin/foo\n"
	diskContent := "# managed by magus\n[Unit]\nDescription = foo  \n\n[Service]\nExecStart = /bin/foo\n"
	in := &ir.IR{Units: []ir.Unit{
		{Name: "magus-foo.service", Contents: irContent},
	}}
	path := UnitPath("magus-foo.service")
	m := manifest.New()
	m.PutActive(path, manifest.KindUnit, HashContent([]byte(irContent), KindUnit), manifest.OriginCreate, time.Now())

	plan, err := Compute(in, m, memFS{
		path: {contents: []byte(diskContent), mode: 0o644},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := findAction(t, plan, path)
	if a.Action != ActionSkip {
		t.Errorf("Action = %s (%s), want skip — canonicalization should mask whitespace/comments",
			a.Action, a.Reason)
	}
}

func TestUnitUpdateWhenCanonicallyDifferent(t *testing.T) {
	// On-disk has different ExecStart — that's behavior-significant and
	// should NOT canonicalize away.
	in := &ir.IR{Units: []ir.Unit{
		{Name: "magus-foo.service", Contents: "[Service]\nExecStart=/bin/new\n"},
	}}
	path := UnitPath("magus-foo.service")
	m := manifest.New()
	m.PutActive(path, manifest.KindUnit, "sha256:old", manifest.OriginCreate, time.Now())
	plan, err := Compute(in, m, memFS{
		path: {contents: []byte("[Service]\nExecStart=/bin/old\n"), mode: 0o644},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := findAction(t, plan, path)
	if a.Action != ActionUpdate {
		t.Errorf("Action = %s (%s), want update", a.Action, a.Reason)
	}
}

func TestUnitDeleteFromManifestSweep(t *testing.T) {
	// Manifest has a unit entry, IR no longer declares it. The orphan
	// sweep must produce a delete action with KindUnit + UnitName so apply
	// can disable+stop the unit before unlinking.
	path := UnitPath("magus-old.service")
	m := manifest.New()
	m.PutActive(path, manifest.KindUnit, "sha256:x", manifest.OriginCreate, time.Now())

	plan, err := Compute(&ir.IR{}, m, memFS{
		path: {contents: []byte("[Service]\nExecStart=/bin/x\n"), mode: 0o644},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := findAction(t, plan, path)
	if a.Action != ActionDelete {
		t.Errorf("Action = %s, want delete", a.Action)
	}
	if a.Kind != KindUnit {
		t.Errorf("Kind = %s, want unit", a.Kind)
	}
	if a.UnitName != "magus-old.service" {
		t.Errorf("UnitName = %q (must be derived from path for orphan-swept units)", a.UnitName)
	}
}

func TestUnitNameFromPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/etc/systemd/system/foo.service", "foo.service"},
		{"/etc/systemd/system/magus-healthcheck.timer", "magus-healthcheck.timer"},
		{"/etc/systemd/system/foo.service.d/10-magus.conf", "foo.service"},
		{"/etc/systemd/system/sshd.service.d/10-magus.conf", "sshd.service"},
	}
	for _, c := range cases {
		if got := UnitNameFromPath(c.path); got != c.want {
			t.Errorf("UnitNameFromPath(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}
