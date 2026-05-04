// Command magus is the day-2 reconciler for Magus OS images.
//
// magus consumes the IR subset of a Butane file and converges the running
// system toward the declared state. See docs/spec-reconciler.md for the
// authority model, manifest semantics, and equivalence rules.
package main

import (
	"fmt"
	"os"
)

const usage = `magus — Butane reconciler for Magus

Usage: magus <command> [flags]

Commands:
  validate    Parse a Butane file and check it against the policy
  plan        Show what apply would do (not implemented yet)
  apply       Reconcile the system toward the declared state (not implemented yet)
  status      Print reconciler state (not implemented yet)
  adopt       Take over an existing path that differs from the IR (not implemented yet)
  reclaim     Restore an orphaned path to active reconciliation (not implemented yet)

Run 'magus <command> -h' for command-specific flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}

	cmd, args := os.Args[1], os.Args[2:]

	switch cmd {
	case "validate":
		os.Exit(runValidate(args))
	case "plan":
		os.Exit(runPlan(args))
	case "apply":
		os.Exit(runApply(args))
	case "status":
		os.Exit(runStatus(args))
	case "adopt":
		os.Exit(runAdopt(args))
	case "reclaim":
		os.Exit(runReclaim(args))
	case "-h", "--help", "help":
		fmt.Print(usage)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "magus: unknown command %q\n\n%s", cmd, usage)
		os.Exit(1)
	}
}
