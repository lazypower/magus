package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/lazypower/magus/internal/diff"
	"github.com/lazypower/magus/internal/hostfs"
	"github.com/lazypower/magus/internal/ir"
	"github.com/lazypower/magus/internal/manifest"
	"github.com/lazypower/magus/internal/policy"
)

const planUsage = `magus plan — show what apply would do

Usage: magus plan [--policy <path>] [--manifest <path>] <butane-file>

Flags:
  --policy <path>     Override policy file (default: /etc/magus/policy.yaml)
  --manifest <path>   Override manifest file (default: /var/lib/magus/manifest.json)

Exit codes:
  0   no changes needed
  2   changes pending or conflicts present
  1   input-bad (parse error, policy/IR contradiction, manifest version mismatch)
`

func runPlan(args []string) int {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, planUsage) }
	policyPath := fs.String("policy", policy.DefaultPath, "policy file path")
	manifestPath := fs.String("manifest", manifest.DefaultPath, "manifest file path")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() != 1 {
		fmt.Fprint(os.Stderr, planUsage)
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

	plan, err := diff.Compute(parsed, m, hostfs.OS())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	printPlan(os.Stdout, butanePath, plan)

	if plan.HasChanges() {
		return 2
	}
	return 0
}

// printPlan renders the spec-shaped plan output. The order is stable: actions
// are printed in the order Compute emitted them (IR order, then orphans).
func printPlan(w io.Writer, butanePath string, p *diff.Plan) {
	fmt.Fprintf(w, "%s → %d resources\n\n", butanePath, len(p.Actions))

	for _, a := range p.Actions {
		fmt.Fprintf(w, "  %-12s%s", actionTag(a.Action), a.Path)
		if a.Reason != "" {
			fmt.Fprintf(w, "  (%s)", a.Reason)
		}
		fmt.Fprintln(w)
	}

	if p.Deferred > 0 {
		fmt.Fprintf(w, "\n  %d resources deferred (units, directories — not yet implemented)\n",
			p.Deferred)
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, summary(p))
}

func actionTag(a diff.Action) string {
	return fmt.Sprintf("[%s]", a)
}

func summary(p *diff.Plan) string {
	var c, u, ad, d, s, cf, or, sc int
	for _, a := range p.Actions {
		switch a.Action {
		case diff.ActionCreate:
			c++
		case diff.ActionUpdate:
			u++
		case diff.ActionAdopt:
			ad++
		case diff.ActionDelete:
			d++
		case diff.ActionSkip:
			s++
		case diff.ActionConflict:
			cf++
		case diff.ActionOrphaned:
			or++
		case diff.ActionCleanup:
			sc++
		}
	}
	out := fmt.Sprintf("%d creates, %d updates, %d adopts, %d deletes, %d skipped",
		c, u, ad, d, s)
	if cf > 0 {
		out += fmt.Sprintf(", %d conflicts", cf)
	}
	if or > 0 {
		out += fmt.Sprintf(", %d orphaned", or)
	}
	if sc > 0 {
		out += fmt.Sprintf(", %d manifest cleanup", sc)
	}
	return out
}
