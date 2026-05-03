package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/lazypower/magus/internal/ir"
	"github.com/lazypower/magus/internal/policy"
)

const validateUsage = `magus validate — parse a Butane file and check it against the policy

Usage: magus validate [--policy <path>] <butane-file>

Flags:
  --policy <path>   Override the policy file location
                    (default: /etc/magus/policy.yaml)
`

func runValidate(args []string) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, validateUsage) }
	policyPath := fs.String("policy", policy.DefaultPath, "policy file path")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() != 1 {
		fmt.Fprint(os.Stderr, validateUsage)
		return 1
	}
	butanePath := fs.Arg(0)

	p, err := policy.Load(*policyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	parsed, warnings, err := ir.LoadButane(butanePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}

	violations := policy.Check(p, parsed)
	for _, v := range violations {
		fmt.Fprintf(os.Stderr, "error: %s\n", v)
	}

	resourceCount := len(parsed.Files) + len(parsed.Directories) + len(parsed.Units)
	if len(violations) > 0 {
		fmt.Fprintf(os.Stderr, "%d resources, %d policy violations\n", resourceCount, len(violations))
		return 1
	}
	fmt.Printf("ok: %d resources, 0 policy violations\n", resourceCount)
	return 0
}
