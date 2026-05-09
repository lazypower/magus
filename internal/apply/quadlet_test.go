package apply

import (
	"testing"
	"time"

	"github.com/lazypower/magus/internal/diff"
	"github.com/lazypower/magus/internal/ir"
	"github.com/lazypower/magus/internal/manifest"
	"github.com/lazypower/magus/internal/systemd"
)

const sampleContainer = `[Unit]
Description=Ollama
[Container]
Image=docker.io/ollama/ollama:latest
[Install]
WantedBy=default.target
`

func TestQuadletCreateRunsEnableNowOnGeneratedService(t *testing.T) {
	w := newMemWriter()
	sd := systemd.NewFake()
	in := &ir.IR{Quadlets: []ir.Quadlet{
		{
			Path:     "/etc/containers/systemd/ollama.container",
			Name:     "ollama.container",
			Mode:     0o644,
			Contents: []byte(sampleContainer),
		},
	}}
	plan, _ := diff.Compute(in, manifest.New(), w)
	m := manifest.New()
	r := Apply(plan, in, w, m, sd, time.Now())

	if r.ExitCode() != 0 {
		t.Fatalf("exit = %d; outcomes: %+v", r.ExitCode(), r.Outcomes)
	}
	calls := sd.Calls()
	if !containsCall(calls, "DaemonReload") {
		t.Errorf("expected DaemonReload (quadlet generator runs on reload), got: %v", calls)
	}
	// Generated service name for ollama.container is ollama.service.
	if !containsCall(calls, "EnableNow(ollama.service)") {
		t.Errorf("expected EnableNow on generated service, got: %v", calls)
	}
	// The quadlet itself (ollama.container) is NOT what gets enabled.
	for _, c := range calls {
		if c == "EnableNow(ollama.container)" {
			t.Errorf("EnableNow should target generated .service, not the quadlet name; got: %v", calls)
		}
	}
	// Manifest tracks it as a quadlet.
	entry, _ := m.Get("/etc/containers/systemd/ollama.container")
	if entry.Kind != manifest.KindQuadlet {
		t.Errorf("manifest Kind = %s, want quadlet", entry.Kind)
	}
}

func TestQuadletDeleteStopsGeneratedServiceBeforeUnlink(t *testing.T) {
	w := newMemWriter()
	sd := systemd.NewFake()
	sd.SetEnablement("ollama.service", systemd.EnablementEnabled)
	sd.SetActive("ollama.service", true)

	path := "/etc/containers/systemd/ollama.container"
	w.preload(path, memFile{contents: []byte(sampleContainer), mode: 0o644})

	m := manifest.New()
	m.PutActive(path, manifest.KindQuadlet, "sha256:x", manifest.OriginCreate, time.Now())

	plan, _ := diff.Compute(&ir.IR{}, m, w)
	r := Apply(plan, &ir.IR{}, w, m, sd, time.Now())

	if r.ExitCode() != 0 {
		t.Fatalf("exit = %d; outcomes: %+v", r.ExitCode(), r.Outcomes)
	}
	calls := sd.Calls()

	// DisableNow must target the generated service (ollama.service), not
	// the quadlet name.
	if !containsCall(calls, "DisableNow(ollama.service)") {
		t.Errorf("expected DisableNow on generated service, got: %v", calls)
	}
	// And it must come before DaemonReload (so systemd stops the running
	// container before the source is gone).
	disableIdx, reloadIdx := -1, -1
	for i, c := range calls {
		switch c {
		case "DisableNow(ollama.service)":
			disableIdx = i
		case "DaemonReload":
			reloadIdx = i
		}
	}
	if disableIdx < 0 || reloadIdx < 0 || disableIdx > reloadIdx {
		t.Errorf("DisableNow must precede DaemonReload, got: %v", calls)
	}
	if _, present := w.files[path]; present {
		t.Error("quadlet source file should be removed")
	}
}

func TestQuadletUpdateActiveTriggersRestart(t *testing.T) {
	w := newMemWriter()
	sd := systemd.NewFake()
	sd.SetEnablement("ollama.service", systemd.EnablementEnabled)
	sd.SetActive("ollama.service", true)

	path := "/etc/containers/systemd/ollama.container"
	w.preload(path, memFile{contents: []byte("[Container]\nImage=ollama:old\n"), mode: 0o644})

	in := &ir.IR{Quadlets: []ir.Quadlet{
		{Path: path, Name: "ollama.container", Mode: 0o644, Contents: []byte(sampleContainer)},
	}}
	m := manifest.New()
	m.PutActive(path, manifest.KindQuadlet, "sha256:old", manifest.OriginCreate, time.Now())

	plan, _ := diff.Compute(in, m, w)
	r := Apply(plan, in, w, m, sd, time.Now())

	if r.ExitCode() != 0 {
		t.Fatalf("exit = %d", r.ExitCode())
	}
	if !containsCall(sd.Calls(), "Restart(ollama.service)") {
		t.Errorf("expected Restart on generated service, got: %v", sd.Calls())
	}
}

func TestQuadletAdoptNoDaemonReloadNoRestart(t *testing.T) {
	w := newMemWriter()
	sd := systemd.NewFake()
	sd.SetEnablement("ollama.service", systemd.EnablementEnabled)
	sd.SetActive("ollama.service", true)

	path := "/etc/containers/systemd/ollama.container"
	w.preload(path, memFile{contents: []byte(sampleContainer), mode: 0o644})

	in := &ir.IR{Quadlets: []ir.Quadlet{
		{Path: path, Name: "ollama.container", Mode: 0o644, Contents: []byte(sampleContainer)},
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
	for _, c := range sd.Calls() {
		if c == "DaemonReload" || c == "Restart(ollama.service)" {
			t.Errorf("adopt should not trigger %s, got: %v", c, sd.Calls())
		}
	}
	entry, _ := m.Get(path)
	if entry.Origin != manifest.OriginAdopt || entry.Kind != manifest.KindQuadlet {
		t.Errorf("manifest entry = %+v, want origin=adopt kind=quadlet", entry)
	}
}

func TestQuadletAndUnitShareSingleDaemonReload(t *testing.T) {
	// One quadlet create + one unit create. daemon-reload must run exactly
	// once — both share the trigger.
	w := newMemWriter()
	sd := systemd.NewFake()
	in := &ir.IR{
		Quadlets: []ir.Quadlet{
			{Path: "/etc/containers/systemd/ollama.container", Name: "ollama.container", Mode: 0o644, Contents: []byte(sampleContainer)},
		},
		Units: []ir.Unit{
			{Name: "magus-foo.service", Enabled: true, Contents: "[Service]\nExecStart=/bin/foo\n"},
		},
	}
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
		t.Errorf("DaemonReload called %d times, want exactly 1 (shared between unit + quadlet)", count)
	}
}
