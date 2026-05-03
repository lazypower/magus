package policy

import (
	"fmt"

	"github.com/lazypower/magus/internal/ir"
)

// Violation is a single policy-vs-IR conflict reported by Check. Each
// violation is independent; magus collects all violations before halting,
// so the operator sees the full picture rather than one error at a time.
type Violation struct {
	// Resource identifies the offending IR element ("file:/path" or
	// "unit:foo.service") so output groups cleanly.
	Resource string
	// Reason is a single-line explanation suitable for `error: <reason>`
	// surfacing.
	Reason string
}

func (v Violation) String() string {
	return v.Resource + ": " + v.Reason
}

// Check validates ir against p. The slice is empty when ir is fully permitted.
//
// Check enforces the hard rules from the spec's Policy section: path allowlist,
// unit namespace, deny lists, and (where applicable) mode caps. It does not
// touch the filesystem or check on-disk state — that's the diff stage.
func Check(p *Policy, ir *ir.IR) []Violation {
	var v []Violation

	for _, f := range ir.Files {
		if reason := p.DenyPathReason(f.Path); reason != "" {
			v = append(v, Violation{
				Resource: "file:" + f.Path,
				Reason:   reason,
			})
		}
		if escalated := modeEscalation(f.Mode); escalated != "" {
			v = append(v, Violation{
				Resource: "file:" + f.Path,
				Reason:   escalated,
			})
		}
	}

	for _, d := range ir.Directories {
		if reason := p.DenyPathReason(d.Path); reason != "" {
			v = append(v, Violation{
				Resource: "dir:" + d.Path,
				Reason:   reason,
			})
		}
		if escalated := modeEscalation(d.Mode); escalated != "" {
			v = append(v, Violation{
				Resource: "dir:" + d.Path,
				Reason:   escalated,
			})
		}
	}

	for _, u := range ir.Units {
		if reason := p.DenyUnitReason(u.Name); reason != "" {
			v = append(v, Violation{
				Resource: "unit:" + u.Name,
				Reason:   reason,
			})
		}
		for _, di := range u.DropIns {
			// Drop-in precedence rule: all magus drop-ins must be 10-magus.conf
			// so they sort predictably and are identifiable on disk.
			if di.Name != "10-magus.conf" {
				v = append(v, Violation{
					Resource: fmt.Sprintf("unit:%s/%s", u.Name, di.Name),
					Reason:   `drop-in must be named "10-magus.conf" (drop-in precedence rule)`,
				})
			}
		}
	}

	return v
}

// modeEscalation enforces "no privilege escalation" — setuid, setgid, and
// world-writable bits are off-limits regardless of where the path falls.
func modeEscalation(mode uint32) string {
	if mode&0o4000 != 0 {
		return fmt.Sprintf("mode %#o: setuid bit not permitted", mode)
	}
	if mode&0o2000 != 0 {
		return fmt.Sprintf("mode %#o: setgid bit not permitted", mode)
	}
	if mode&0o0002 != 0 {
		return fmt.Sprintf("mode %#o: world-writable not permitted", mode)
	}
	return ""
}
