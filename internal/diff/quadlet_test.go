package diff

import (
	"testing"
	"time"

	"github.com/lazypower/magus/internal/ir"
	"github.com/lazypower/magus/internal/manifest"
)

const sampleContainer = `[Unit]
Description=Ollama
[Container]
Image=docker.io/ollama/ollama:latest
PublishPort=11434:11434
[Install]
WantedBy=default.target
`

func TestQuadletCreate(t *testing.T) {
	in := &ir.IR{Quadlets: []ir.Quadlet{
		{
			Path:     "/etc/containers/systemd/ollama.container",
			Name:     "ollama.container",
			Mode:     0o644,
			Contents: []byte(sampleContainer),
		},
	}}
	plan, err := Compute(in, manifest.New(), memFS{})
	if err != nil {
		t.Fatal(err)
	}
	a := findAction(t, plan, "/etc/containers/systemd/ollama.container")
	if a.Action != ActionCreate {
		t.Errorf("Action = %s, want create", a.Action)
	}
	if a.Kind != KindQuadlet {
		t.Errorf("Kind = %s, want quadlet", a.Kind)
	}
	if a.UnitName != "ollama.container" {
		t.Errorf("UnitName = %q, want ollama.container", a.UnitName)
	}
}

func TestQuadletSkipWhenCanonicallyEqual(t *testing.T) {
	// Quadlets share the unit canonicalization — whitespace and comment
	// differences must collapse to the same hash.
	disk := "# managed by magus\n[Unit]\nDescription = Ollama  \n\n[Container]\nImage = docker.io/ollama/ollama:latest\nPublishPort=11434:11434\n[Install]\nWantedBy=default.target\n"
	path := "/etc/containers/systemd/ollama.container"

	m := manifest.New()
	m.PutActive(path, manifest.KindQuadlet, HashContent([]byte(sampleContainer), KindQuadlet), manifest.OriginCreate, time.Now())

	in := &ir.IR{Quadlets: []ir.Quadlet{
		{Path: path, Name: "ollama.container", Mode: 0o644, Contents: []byte(sampleContainer)},
	}}
	plan, _ := Compute(in, m, memFS{
		path: {contents: []byte(disk), mode: 0o644},
	})
	a := findAction(t, plan, path)
	if a.Action != ActionSkip {
		t.Errorf("Action = %s (%s), want skip — quadlets must canonicalize like units",
			a.Action, a.Reason)
	}
}

func TestQuadletGeneratedServiceMapping(t *testing.T) {
	cases := []struct {
		quadlet string
		want    string
	}{
		{"ollama.container", "ollama.service"},
		{"models-data.volume", "models-data-volume.service"},
		{"podnet.network", "podnet-network.service"},
	}
	for _, c := range cases {
		got, err := QuadletGeneratedService(c.quadlet)
		if err != nil {
			t.Errorf("QuadletGeneratedService(%q) returned error: %v", c.quadlet, err)
			continue
		}
		if got != c.want {
			t.Errorf("QuadletGeneratedService(%q) = %q, want %q", c.quadlet, got, c.want)
		}
	}
}

func TestQuadletGeneratedServiceRejectsUnsupported(t *testing.T) {
	cases := []string{"foo.pod", "foo.kube", "foo.image", "foo.build", "foo.txt"}
	for _, q := range cases {
		if _, err := QuadletGeneratedService(q); err == nil {
			t.Errorf("QuadletGeneratedService(%q): want error, got nil", q)
		}
	}
}

func TestQuadletDeleteFromManifestSweep(t *testing.T) {
	// Manifest has a quadlet entry, IR no longer declares it. Delete action
	// must surface with Kind=KindQuadlet so apply can stop the generated
	// service before unlinking the source.
	path := "/etc/containers/systemd/old.container"
	m := manifest.New()
	m.PutActive(path, manifest.KindQuadlet, "sha256:x", manifest.OriginCreate, time.Now())

	plan, err := Compute(&ir.IR{}, m, memFS{
		path: {contents: []byte(sampleContainer), mode: 0o644},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := findAction(t, plan, path)
	if a.Action != ActionDelete {
		t.Errorf("Action = %s, want delete", a.Action)
	}
	if a.Kind != KindQuadlet {
		t.Errorf("Kind = %s, want quadlet", a.Kind)
	}
	if a.UnitName != "old.container" {
		t.Errorf("UnitName = %q (must derive from filename for generated-service mapping)", a.UnitName)
	}
}
