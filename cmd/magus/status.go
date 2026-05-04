package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/lazypower/magus/internal/manifest"
)

const statusUsage = `magus status — print reconciler state

Usage: magus status [--json] [--manifest <path>]

Status reads the manifest only — it does not parse the Butane file or stat
the disk. Use 'magus plan' for live diff state (conflicts, pending changes).

Flags:
  --json              Emit machine-readable JSON
  --manifest <path>   Override manifest file (default: /var/lib/magus/manifest.json)
`

// statusReport is the JSON shape emitted by 'magus status --json'. It maps
// closely to the spec example, minus the conflicts/errors sections which
// require state we don't currently persist (tracked as follow-up).
type statusReport struct {
	LastApply        *time.Time            `json:"last_apply"`
	ManagedResources int                   `json:"managed_resources"`
	Result           string                `json:"result"`
	Files            map[string]string     `json:"files"`
	Orphaned         []orphanedReportEntry `json:"orphaned"`
}

type orphanedReportEntry struct {
	Path       string    `json:"path"`
	Reason     string    `json:"reason"`
	OrphanedAt time.Time `json:"orphaned_at"`
}

func runStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, statusUsage) }
	manifestPath := fs.String("manifest", manifest.DefaultPath, "manifest file path")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() != 0 {
		fmt.Fprint(os.Stderr, statusUsage)
		return 1
	}

	m, err := manifest.Load(*manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	report := buildStatus(m)
	if *asJSON {
		return emitStatusJSON(os.Stdout, report)
	}
	emitStatusHuman(os.Stdout, report)
	return 0
}

func buildStatus(m *manifest.Manifest) statusReport {
	// Initialize Orphaned as an empty slice (not nil) so JSON output emits
	// `[]` rather than `null` — friendlier for downstream consumers that
	// expect to iterate without a null check.
	r := statusReport{
		Files:    map[string]string{},
		Orphaned: []orphanedReportEntry{},
	}
	var lastApply time.Time

	paths := make([]string, 0, len(m.Resources))
	for p := range m.Resources {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, p := range paths {
		entry := m.Resources[p]
		switch entry.State {
		case manifest.StateActive:
			r.Files[p] = "ok"
			r.ManagedResources++
			if entry.AppliedAt.After(lastApply) {
				lastApply = entry.AppliedAt
			}
		case manifest.StateOrphaned:
			oa := time.Time{}
			if entry.OrphanedAt != nil {
				oa = *entry.OrphanedAt
			}
			r.Orphaned = append(r.Orphaned, orphanedReportEntry{
				Path:       p,
				Reason:     entry.OrphanedReason,
				OrphanedAt: oa,
			})
		}
	}

	if !lastApply.IsZero() {
		r.LastApply = &lastApply
	}
	switch {
	case len(r.Orphaned) > 0:
		r.Result = "ok-with-orphans"
	default:
		r.Result = "ok"
	}
	return r
}

func emitStatusJSON(w io.Writer, r statusReport) int {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func emitStatusHuman(w io.Writer, r statusReport) {
	if r.LastApply != nil {
		fmt.Fprintf(w, "last apply:  %s\n", r.LastApply.Format(time.RFC3339))
	} else {
		fmt.Fprintln(w, "last apply:  (never)")
	}
	fmt.Fprintf(w, "managed:     %d resources\n", r.ManagedResources)
	fmt.Fprintf(w, "result:      %s\n", r.Result)

	if r.ManagedResources > 0 {
		fmt.Fprintln(w, "\nfiles:")
		paths := make([]string, 0, len(r.Files))
		for p := range r.Files {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			fmt.Fprintf(w, "  ✓ %s\n", p)
		}
	}

	if len(r.Orphaned) > 0 {
		fmt.Fprintln(w, "\norphaned:")
		for _, o := range r.Orphaned {
			fmt.Fprintf(w, "  ! %s  (%s, since %s)\n",
				o.Path, o.Reason, o.OrphanedAt.Format(time.RFC3339))
		}
	}
}
