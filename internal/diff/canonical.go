package diff

import "strings"

// CanonicalizeUnit normalizes a systemd unit file or drop-in to its
// behavior-significant form. The output is what diff hashes for equivalence:
// two unit files that canonicalize to the same bytes have the same effect on
// systemd, even if they differ in whitespace or comments.
//
// The 7 rules, from docs/spec-reconciler.md "Equivalence":
//   1. Drop blank lines.
//   2. Drop comment lines (first non-whitespace character is # or ;).
//   3. Trim trailing whitespace from each line.
//   4. Normalize key=value spacing: collapse to "key=value" (no whitespace
//      around =).
//   5. Preserve section headers exactly — case-sensitive, brackets included.
//   6. Preserve key order within each section.
//   7. Preserve section order across the file.
//
// Rules 5–7 are "preserve, don't reorder" — order is behavior-significant in
// systemd (ExecStart*, Environment, etc.), so canonicalization is intentionally
// limited to dropping noise rather than rearranging structure.
func CanonicalizeUnit(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		// Rule 3: trim trailing whitespace first so "key = value   " becomes
		// "key = value", which then normalizes cleanly.
		line = strings.TrimRight(line, " \t\r")

		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" {
			continue // Rule 1: drop blank
		}
		if trimmed[0] == '#' || trimmed[0] == ';' {
			continue // Rule 2: drop comment
		}

		// Rule 5: section headers preserved exactly — no normalization.
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			out = append(out, trimmed)
			continue
		}

		// Rule 4: collapse whitespace around the first =. systemd parses
		// "key = value" identically to "key=value", but the canonical form
		// has no surrounding whitespace.
		if eq := strings.IndexByte(trimmed, '='); eq >= 0 {
			key := strings.TrimRight(trimmed[:eq], " \t")
			val := strings.TrimLeft(trimmed[eq+1:], " \t")
			out = append(out, key+"="+val)
			continue
		}

		// Bare line with no = and no section markers — rare but valid (e.g.,
		// continuation lines after a backslash). Preserve as-is.
		out = append(out, trimmed)
	}
	return strings.Join(out, "\n")
}
