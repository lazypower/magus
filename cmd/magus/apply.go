package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/lazypower/magus/internal/apply"
	"github.com/lazypower/magus/internal/diff"
	"github.com/lazypower/magus/internal/hostfs"
	"github.com/lazypower/magus/internal/ir"
	"github.com/lazypower/magus/internal/manifest"
	"github.com/lazypower/magus/internal/policy"
)

const applyUsage = `magus apply — reconcile the system toward the declared state

Usage: magus apply [--yes] [--policy <path>] [--manifest <path>] <butane-file>

Flags:
  --yes               Skip the confirmation prompt
  --policy <path>     Override policy file (default: /etc/magus/policy.yaml)
  --manifest <path>   Override manifest file (default: /var/lib/magus/manifest.json)

Exit codes:
  0   all declared resources are in their desired state
  2   one or more resources skipped (conflicts, orphaned, drift)
  1   one or more resources errored mid-apply, OR input-bad (parse, policy)
`

func runApply(args []string) int {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, applyUsage) }
	policyPath := fs.String("policy", policy.DefaultPath, "policy file path")
	manifestPath := fs.String("manifest", manifest.DefaultPath, "manifest file path")
	yes := fs.Bool("yes", false, "skip confirmation prompt")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() != 1 {
		fmt.Fprint(os.Stderr, applyUsage)
		return 1
	}
	butanePath := fs.Arg(0)

	p, err := policy.Load(*policyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	parsed, _, err := ir.LoadButane(butanePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if violations := policy.Check(p, parsed); len(violations) > 0 {
		for _, v := range violations {
			fmt.Fprintf(os.Stderr, "error: %s\n", v)
		}
		return 1
	}
	m, err := manifest.Load(*manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	w := hostfs.OS()
	plan, err := diff.Compute(parsed, m, w)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	printPlan(os.Stdout, butanePath, plan)

	changes, conflicts := planCounts(plan)
	if changes == 0 && conflicts == 0 {
		fmt.Println("\nNothing to apply.")
		return 0
	}

	if !*yes {
		if !confirm(os.Stdin, os.Stdout, changes, conflicts) {
			fmt.Println("Aborted.")
			return 0
		}
	}
	fmt.Println()

	result := apply.Apply(plan, parsed, w, m, time.Now().UTC())
	for _, oc := range result.Outcomes {
		printOutcome(os.Stdout, oc)
	}

	if err := m.Save(*manifestPath); err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to save manifest: %v\n", err)
		return 1
	}

	a, _, s, e := result.Counts()
	fmt.Println()
	fmt.Printf("Applied %d changes, %d skipped, %d errors.  exit %d\n",
		a, s, e, result.ExitCode())
	return result.ExitCode()
}

// planCounts splits actions into "changes that will run" vs "conflicts that
// will be skipped" — the two numbers the prompt needs.
func planCounts(p *diff.Plan) (changes, conflicts int) {
	for _, a := range p.Actions {
		switch a.Action {
		case diff.ActionCreate, diff.ActionUpdate, diff.ActionAdopt,
			diff.ActionDelete, diff.ActionCleanup:
			changes++
		case diff.ActionConflict, diff.ActionOrphaned:
			conflicts++
		}
	}
	return
}

// confirm prompts the user with the spec-shaped message. Anything other than
// "y" / "yes" (case-insensitive) is treated as decline. EOF (e.g., piped
// input) is also a decline — we never proceed without explicit consent.
func confirm(in io.Reader, out io.Writer, changes, conflicts int) bool {
	switch {
	case changes > 0 && conflicts > 0:
		fmt.Fprintf(out, "\nApply %d changes? (%d conflict%s will be skipped) [y/N] ",
			changes, conflicts, plural(conflicts))
	case changes > 0:
		fmt.Fprintf(out, "\nApply %d changes? [y/N] ", changes)
	default:
		// Conflicts only and no changes — nothing to apply, no prompt needed.
		// printPlan already showed the conflicts; just decline.
		fmt.Fprintf(out, "\n%d conflict%s present, nothing to apply.\n",
			conflicts, plural(conflicts))
		return false
	}
	r := bufio.NewReader(in)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// printOutcome renders one apply outcome with ✓ for applied/unchanged and ✗
// for skipped/errored, matching the spec example.
func printOutcome(w io.Writer, oc apply.Outcome) {
	mark := "✓"
	if oc.Status == apply.StatusSkipped || oc.Status == apply.StatusErrored {
		mark = "✗"
	}
	suffix := ""
	switch {
	case oc.Err != nil:
		suffix = fmt.Sprintf("  (errored: %v)", oc.Err)
	case oc.Status == apply.StatusSkipped:
		suffix = fmt.Sprintf("  (skipped: %s)", oc.Reason)
	case oc.Reason != "":
		suffix = fmt.Sprintf("  (%s)", oc.Reason)
	}
	fmt.Fprintf(w, "  %s %s%s\n", mark, oc.Path, suffix)
}
