package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/lazypower/magus/internal/hostfs"
	"github.com/lazypower/magus/internal/ir"
	"github.com/lazypower/magus/internal/manifest"
	"github.com/lazypower/magus/internal/policy"
)

const adoptUsage = `magus adopt — take over an existing path that differs from the IR

Usage: magus adopt [--yes] [--policy <path>] [--manifest <path>] <butane-file> <path>

Adopt overwrites the existing content with the IR's content and records the
path in the manifest with origin=force-adopt. Use this when you want magus to
own a path you're willing to replace — terraform import with a write step.

Adoption of *matching* content (where on-disk already equals IR) happens
silently during 'magus apply' and does not require this command.

Flags:
  --yes               Skip the confirmation prompt
  --policy <path>     Override policy file (default: /etc/magus/policy.yaml)
  --manifest <path>   Override manifest file (default: /var/lib/magus/manifest.json)
`

func runAdopt(args []string) int {
	fs := flag.NewFlagSet("adopt", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, adoptUsage) }
	policyPath := fs.String("policy", policy.DefaultPath, "policy file path")
	manifestPath := fs.String("manifest", manifest.DefaultPath, "manifest file path")
	yes := fs.Bool("yes", false, "skip confirmation prompt")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() != 2 {
		fmt.Fprint(os.Stderr, adoptUsage)
		return 1
	}
	butanePath, target := fs.Arg(0), fs.Arg(1)

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

	declared, ok := findFile(parsed, target)
	if !ok {
		fmt.Fprintf(os.Stderr, "error: %s is not declared in %s\n", target, butanePath)
		return 1
	}

	w := hostfs.OS()
	st, err := w.Stat(target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: stat %s: %v\n", target, err)
		return 1
	}
	if !st.Exists {
		fmt.Fprintf(os.Stderr, "error: %s does not exist on disk — use 'magus apply' to create it\n", target)
		return 1
	}

	if entry, exists := m.Get(target); exists {
		switch entry.State {
		case manifest.StateActive:
			fmt.Fprintf(os.Stderr, "error: %s is already managed by magus — use 'magus apply' to update\n", target)
			return 1
		case manifest.StateOrphaned:
			fmt.Fprintf(os.Stderr, "error: %s is orphaned — use 'magus reclaim' to restore reconciliation\n", target)
			return 1
		}
	}

	body, err := w.ReadFile(target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: read %s: %v\n", target, err)
		return 1
	}
	onDisk := hashContent(body)
	declared_ := hashContent(declared.Contents)

	if onDisk == declared_ {
		fmt.Fprintf(os.Stderr, "error: %s already matches the IR — 'magus apply' will adopt it silently\n", target)
		return 1
	}

	fmt.Println("The path exists with content that differs from the IR.")
	fmt.Println()
	fmt.Printf("  - existing hash: %s\n", onDisk)
	fmt.Printf("  - declared hash: %s\n", declared_)
	fmt.Println()
	fmt.Println("Overwriting will replace the existing content with the IR's declared content.")

	if !*yes {
		if !confirmAdopt(os.Stdin, os.Stdout, target) {
			fmt.Println("Aborted.")
			return 0
		}
	}
	fmt.Println()

	if err := w.WriteFile(target, declared.Contents, declared.Mode, declared.UID, declared.GID); err != nil {
		fmt.Fprintf(os.Stderr, "error: write %s: %v\n", target, err)
		return 1
	}
	m.PutActive(target, manifest.KindFile, declared_, manifest.OriginForceAdopt, time.Now().UTC())
	if err := m.Save(*manifestPath); err != nil {
		fmt.Fprintf(os.Stderr, "error: save manifest: %v\n", err)
		return 1
	}
	fmt.Printf("  ✓ %s  (rewrote, recorded in manifest)\n", target)
	return 0
}

func findFile(in *ir.IR, path string) (ir.File, bool) {
	for _, f := range in.Files {
		if f.Path == path {
			return f, true
		}
	}
	return ir.File{}, false
}

func hashContent(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

func confirmAdopt(in io.Reader, out io.Writer, path string) bool {
	fmt.Fprintf(out, "\nTake over %s? [y/N] ", path)
	r := bufio.NewReader(in)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}
