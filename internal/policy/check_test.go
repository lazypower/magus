package policy

import (
	"strings"
	"testing"

	"github.com/lazypower/magus/internal/ir"
)

func TestCheckCleanIR(t *testing.T) {
	p := mustLoad(t, `
version: 1
file_roots: ["/etc/magus.d", "/etc/systemd/system"]
unit_patterns: ["magus-*"]
`)
	in := &ir.IR{
		Files: []ir.File{
			{Path: "/etc/magus.d/ollama.env", Mode: 0o644},
		},
		Units: []ir.Unit{
			{Name: "magus-healthcheck.timer", Enabled: true},
		},
	}
	if v := Check(p, in); len(v) != 0 {
		t.Errorf("Check: want 0 violations, got %d: %v", len(v), v)
	}
}

func TestCheckCatchesEverything(t *testing.T) {
	p := mustLoad(t, `
version: 1
file_roots: ["/etc/magus.d"]
unit_patterns: ["magus-*"]
deny:
  paths: ["/etc/magus.d/secret"]
  units: ["magus-secret-*"]
`)
	in := &ir.IR{
		Files: []ir.File{
			{Path: "/etc/passwd"},                       // outside file_roots
			{Path: "/etc/magus.d/secret"},                // explicit deny
			{Path: "/etc/magus.d/world", Mode: 0o646},    // world-writable
			{Path: "/etc/magus.d/setuid", Mode: 0o4755},  // setuid
		},
		Units: []ir.Unit{
			{Name: "ollama.service"},      // doesn't match unit_patterns
			{Name: "magus-secret-thing"},  // explicit deny
			{Name: "magus-ok", DropIns: []ir.DropIn{
				{Name: "20-other.conf"}, // wrong drop-in name
			}},
		},
	}
	v := Check(p, in)
	wantSubstrings := []string{
		"/etc/passwd", "outside file_roots",
		"/etc/magus.d/secret", "denied by policy",
		"world-writable",
		"setuid",
		"ollama.service", "unit_patterns",
		"magus-secret-thing", "denied by policy",
		"20-other.conf", "10-magus.conf",
	}
	joined := ""
	for _, x := range v {
		joined += x.String() + "\n"
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(joined, want) {
			t.Errorf("missing violation containing %q\nall violations:\n%s", want, joined)
		}
	}
}
