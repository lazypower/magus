// Package systemd is the interaction surface apply uses to drive systemd.
//
// The Manager interface keeps the call sites narrow and testable: real
// production paths shell out to /usr/bin/systemctl via osManager; unit tests
// use a fake that records calls and returns scripted responses.
//
// Why shell-out instead of dbus: dbus would be more idiomatic, but it
// requires a live bus connection and a much more elaborate fake to drive
// reliably in tests. Shell-out has well-defined exit codes per systemctl(1)
// and runs anywhere systemctl exists. The Manager interface is opaque enough
// that swapping in dbus later is a private implementation change.
package systemd

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Enablement is the subset of systemctl is-enabled output magus reasons over.
// systemd's full vocabulary (enabled-runtime, indirect, alias, generated, ...)
// collapses into one of these for reconciliation purposes.
type Enablement string

const (
	EnablementEnabled  Enablement = "enabled"
	EnablementDisabled Enablement = "disabled"
	EnablementMasked   Enablement = "masked"
	EnablementStatic   Enablement = "static" // can't be enabled/disabled
	EnablementNotFound Enablement = "not-found"
	EnablementUnknown  Enablement = "unknown"
)

// ErrUnavailable is returned by all Manager methods when systemctl cannot be
// invoked — typically because magus is running on a system without systemd
// (development on macOS, for example). Apply turns this into a per-resource
// error rather than halting the whole apply.
var ErrUnavailable = errors.New("systemctl is unavailable on this host")

// Manager is the operation surface apply needs. Keep it narrow — every method
// here corresponds to a step in the spec's apply mechanics for units.
type Manager interface {
	// DaemonReload re-reads unit files. Run exactly once per apply, after
	// all unit filesystem mutations and before enablement reconciliation.
	DaemonReload() error
	// IsEnabled returns the current enablement state.
	IsEnabled(unit string) (Enablement, error)
	// IsActive reports whether the unit is currently running.
	IsActive(unit string) (bool, error)
	// Enable persists the enablement symlinks but does not start the unit.
	Enable(unit string) error
	// EnableNow enables and starts the unit in one operation.
	EnableNow(unit string) error
	// Disable removes enablement symlinks but does not stop the unit.
	Disable(unit string) error
	// DisableNow disables and stops the unit in one operation.
	DisableNow(unit string) error
	// Restart restarts the unit. Used on content change for already-active
	// units.
	Restart(unit string) error
}

// OS returns a Manager backed by the real systemctl binary. If systemctl
// isn't on PATH at construction time, the returned Manager will return
// ErrUnavailable for every call — this lets magus run on dev machines
// without systemd and surface unit operations as per-resource errors rather
// than crashing.
func OS() Manager {
	path, err := exec.LookPath("systemctl")
	if err != nil {
		return unavailableManager{}
	}
	return &osManager{path: path}
}

type osManager struct{ path string }

func (m *osManager) run(args ...string) error {
	cmd := exec.Command(m.path, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (m *osManager) runOutput(args ...string) (string, error) {
	cmd := exec.Command(m.path, args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (m *osManager) DaemonReload() error { return m.run("daemon-reload") }
func (m *osManager) Enable(unit string) error      { return m.run("enable", unit) }
func (m *osManager) EnableNow(unit string) error   { return m.run("enable", "--now", unit) }
func (m *osManager) Disable(unit string) error     { return m.run("disable", unit) }
func (m *osManager) DisableNow(unit string) error  { return m.run("disable", "--now", unit) }
func (m *osManager) Restart(unit string) error     { return m.run("restart", unit) }

func (m *osManager) IsEnabled(unit string) (Enablement, error) {
	out, _ := m.runOutput("is-enabled", unit)
	// systemctl is-enabled exits non-zero for "disabled" but still prints
	// the state on stdout — so we ignore the error and parse the text.
	switch out {
	case "enabled", "enabled-runtime", "alias", "indirect":
		return EnablementEnabled, nil
	case "disabled":
		return EnablementDisabled, nil
	case "masked", "masked-runtime":
		return EnablementMasked, nil
	case "static", "transient", "generated":
		return EnablementStatic, nil
	}
	if strings.Contains(out, "not loaded") || strings.Contains(out, "No such file") {
		return EnablementNotFound, nil
	}
	return EnablementUnknown, nil
}

func (m *osManager) IsActive(unit string) (bool, error) {
	out, _ := m.runOutput("is-active", unit)
	// is-active prints "active" for active and otherwise — we only care
	// about the boolean. Non-active values include "inactive", "failed",
	// "activating", "deactivating".
	return out == "active", nil
}

// unavailableManager is the substitute returned when systemctl isn't on PATH.
type unavailableManager struct{}

func (unavailableManager) DaemonReload() error                 { return ErrUnavailable }
func (unavailableManager) IsEnabled(string) (Enablement, error) { return EnablementUnknown, ErrUnavailable }
func (unavailableManager) IsActive(string) (bool, error)        { return false, ErrUnavailable }
func (unavailableManager) Enable(string) error                  { return ErrUnavailable }
func (unavailableManager) EnableNow(string) error               { return ErrUnavailable }
func (unavailableManager) Disable(string) error                 { return ErrUnavailable }
func (unavailableManager) DisableNow(string) error              { return ErrUnavailable }
func (unavailableManager) Restart(string) error                 { return ErrUnavailable }
