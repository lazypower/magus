package policy

import "testing"

func mustLoad(t *testing.T, yaml string) *Policy {
	t.Helper()
	path := writeTemp(t, yaml)
	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return p
}

func TestAllowsPath(t *testing.T) {
	p := mustLoad(t, `
version: 1
file_roots:
  - /etc/magus.d
  - /var/lib/magus
unit_patterns: ["magus-*"]
deny:
  paths:
    - /etc/magus.d/secret
    - /etc/magus.d/secrets/*
`)

	cases := []struct {
		path string
		want bool
	}{
		{"/etc/magus.d/ollama.env", true},
		{"/var/lib/magus/state.json", true},
		{"/etc/passwd", false},      // outside file_roots
		{"/etc/magus.d", true},       // root path itself
		{"/etc/magus.dother/x", false}, // prefix-without-separator should not match
		{"/etc/magus.d/secret", false}, // exact deny
		{"/etc/magus.d/secrets/foo", false}, // glob deny
		{"/etc/magus.d/secrets", false},     // glob deny base
	}
	for _, c := range cases {
		if got := p.AllowsPath(c.path); got != c.want {
			t.Errorf("AllowsPath(%q) = %v, want %v (reason: %q)",
				c.path, got, c.want, p.DenyPathReason(c.path))
		}
	}
}

func TestAllowsUnit(t *testing.T) {
	p := mustLoad(t, `
version: 1
file_roots: ["/etc/magus.d"]
unit_patterns:
  - "magus-*"
  - "ollama.service"
deny:
  units:
    - "magus-secret-*"
    - "sshd.*"
`)

	cases := []struct {
		name string
		want bool
	}{
		{"magus-healthcheck.timer", true},
		{"ollama.service", true},
		{"sshd.service", false},   // no pattern match (also denied)
		{"foo.service", false},    // no pattern match
		{"magus-secret-x", false}, // matches pattern but denied
	}
	for _, c := range cases {
		if got := p.AllowsUnit(c.name); got != c.want {
			t.Errorf("AllowsUnit(%q) = %v, want %v (reason: %q)",
				c.name, got, c.want, p.DenyUnitReason(c.name))
		}
	}
}

func TestLoadRejectsBadInput(t *testing.T) {
	cases := []string{
		// Wrong version.
		`version: 2
file_roots: ["/etc/magus.d"]`,
		// No file_roots.
		`version: 1`,
		// Relative path.
		`version: 1
file_roots: ["etc/magus.d"]`,
		// Relative deny path.
		`version: 1
file_roots: ["/etc/magus.d"]
deny:
  paths: ["relative/path"]`,
	}
	for i, in := range cases {
		path := writeTemp(t, in)
		if _, err := Load(path); err == nil {
			t.Errorf("case %d: Load succeeded on bad input, want error", i)
		}
	}
}
