// Package policy loads and enforces /etc/magus/policy.yaml.
//
// Policy is the pre-flight authority boundary: it gates what magus may
// attempt at all, before any diff or apply runs. See docs/spec-reconciler.md
// "Policy" section for the full contract.
package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultPath is where magus looks for the policy file when no override is
// passed. The spec requires this exact location.
const DefaultPath = "/etc/magus/policy.yaml"

// Policy is the parsed contents of policy.yaml.
type Policy struct {
	Version      int      `yaml:"version"`
	FileRoots    []string `yaml:"file_roots"`
	UnitPatterns []string `yaml:"unit_patterns"`
	Deny         Deny     `yaml:"deny"`
}

// Deny lists paths and unit patterns that are off-limits even when they fall
// inside FileRoots / UnitPatterns. Deny is the explicit "never touch this"
// override.
type Deny struct {
	Paths []string `yaml:"paths"`
	Units []string `yaml:"units"`
}

// Load reads and parses the policy at path. A missing file is an error —
// magus refuses to run without an explicit policy.
func Load(path string) (*Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read policy: %w", err)
	}
	var p Policy
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse policy %s: %w", path, err)
	}
	if err := p.validate(); err != nil {
		return nil, fmt.Errorf("invalid policy %s: %w", path, err)
	}
	return &p, nil
}

// validate checks that the policy itself is well-formed. Bad input here is an
// input-bad case — apply halts before any reconciliation runs.
func (p *Policy) validate() error {
	if p.Version != 1 {
		return fmt.Errorf("version: want 1, got %d", p.Version)
	}
	if len(p.FileRoots) == 0 {
		return fmt.Errorf("file_roots: at least one root required")
	}
	for _, r := range p.FileRoots {
		if !filepath.IsAbs(r) {
			return fmt.Errorf("file_roots: %q is not absolute", r)
		}
	}
	for _, d := range p.Deny.Paths {
		if !filepath.IsAbs(strings.TrimSuffix(d, "/*")) {
			return fmt.Errorf("deny.paths: %q is not absolute", d)
		}
	}
	return nil
}

// AllowsPath reports whether path is permitted to be written.
//
// A path is allowed when it falls under at least one file_root and is not
// matched by any deny rule. Symlinks are not resolved here — callers that
// need symlink-resolved checks should pass a resolved path.
func (p *Policy) AllowsPath(path string) bool {
	if !p.underFileRoots(path) {
		return false
	}
	for _, d := range p.Deny.Paths {
		if pathMatches(d, path) {
			return false
		}
	}
	return true
}

// AllowsUnit reports whether unit is permitted to be managed.
func (p *Policy) AllowsUnit(unit string) bool {
	matched := false
	for _, pat := range p.UnitPatterns {
		if globMatch(pat, unit) {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	for _, d := range p.Deny.Units {
		if globMatch(d, unit) {
			return false
		}
	}
	return true
}

// DenyPathReason returns the deny rule that blocks path, or "" if not denied.
// Useful for generating actionable error messages.
func (p *Policy) DenyPathReason(path string) string {
	if !p.underFileRoots(path) {
		return "outside file_roots"
	}
	for _, d := range p.Deny.Paths {
		if pathMatches(d, path) {
			return fmt.Sprintf("denied by policy (rule: %s)", d)
		}
	}
	return ""
}

// DenyUnitReason returns why unit is denied, or "" if it's allowed.
func (p *Policy) DenyUnitReason(unit string) string {
	matched := false
	for _, pat := range p.UnitPatterns {
		if globMatch(pat, unit) {
			matched = true
			break
		}
	}
	if !matched {
		return "does not match unit_patterns"
	}
	for _, d := range p.Deny.Units {
		if globMatch(d, unit) {
			return fmt.Sprintf("denied by policy (rule: %s)", d)
		}
	}
	return ""
}

func (p *Policy) underFileRoots(path string) bool {
	clean := filepath.Clean(path)
	for _, r := range p.FileRoots {
		root := filepath.Clean(r)
		if clean == root || strings.HasPrefix(clean, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// pathMatches handles deny-path entries, which may end in "/*" to match any
// child path. Otherwise it's an exact match.
func pathMatches(rule, path string) bool {
	if strings.HasSuffix(rule, "/*") {
		prefix := strings.TrimSuffix(rule, "/*")
		return path == prefix || strings.HasPrefix(path, prefix+"/")
	}
	return rule == path
}

// globMatch is a minimal glob: '*' matches any run of characters. Sufficient
// for unit patterns like "magus-*" and "sshd.*".
func globMatch(pattern, s string) bool {
	matched, err := filepath.Match(pattern, s)
	if err != nil {
		return false
	}
	return matched
}
