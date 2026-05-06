package systemd

import (
	"fmt"
	"sync"
)

// Fake is an in-memory Manager for tests. It records every call into Calls()
// in order so tests can assert sequencing (e.g., daemon-reload happens once,
// after all writes), and it lets tests preload enablement / activity state.
type Fake struct {
	mu          sync.Mutex
	calls       []string
	enablement  map[string]Enablement
	activity    map[string]bool
	failOn      map[string]error // method-name → error to return on next call
	failCounter map[string]int
}

// NewFake returns a Fake with empty state and reasonable defaults: every
// unit reports EnablementDisabled and inactive until explicitly set.
func NewFake() *Fake {
	return &Fake{
		enablement:  map[string]Enablement{},
		activity:    map[string]bool{},
		failOn:      map[string]error{},
		failCounter: map[string]int{},
	}
}

// SetEnablement preloads what IsEnabled returns for unit.
func (f *Fake) SetEnablement(unit string, e Enablement) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enablement[unit] = e
}

// SetActive preloads what IsActive returns for unit.
func (f *Fake) SetActive(unit string, active bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activity[unit] = active
}

// FailNext makes the next call to method (e.g., "Enable", "Restart") return
// err. Used to test per-resource error isolation.
func (f *Fake) FailNext(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failOn[method] = err
}

// Calls returns the call log in invocation order. Each entry is the method
// name plus its argument(s), e.g., "DaemonReload" or "Enable(magus-foo.service)".
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *Fake) record(method string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, method)
	if err, ok := f.failOn[method]; ok {
		delete(f.failOn, method)
		return err
	}
	return nil
}

func (f *Fake) DaemonReload() error {
	return f.record("DaemonReload")
}

func (f *Fake) IsEnabled(unit string) (Enablement, error) {
	if err := f.record(fmt.Sprintf("IsEnabled(%s)", unit)); err != nil {
		return EnablementUnknown, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if e, ok := f.enablement[unit]; ok {
		return e, nil
	}
	return EnablementDisabled, nil
}

func (f *Fake) IsActive(unit string) (bool, error) {
	if err := f.record(fmt.Sprintf("IsActive(%s)", unit)); err != nil {
		return false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.activity[unit], nil
}

func (f *Fake) Enable(unit string) error {
	if err := f.record(fmt.Sprintf("Enable(%s)", unit)); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enablement[unit] = EnablementEnabled
	return nil
}

func (f *Fake) EnableNow(unit string) error {
	if err := f.record(fmt.Sprintf("EnableNow(%s)", unit)); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enablement[unit] = EnablementEnabled
	f.activity[unit] = true
	return nil
}

func (f *Fake) Disable(unit string) error {
	if err := f.record(fmt.Sprintf("Disable(%s)", unit)); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enablement[unit] = EnablementDisabled
	return nil
}

func (f *Fake) DisableNow(unit string) error {
	if err := f.record(fmt.Sprintf("DisableNow(%s)", unit)); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enablement[unit] = EnablementDisabled
	f.activity[unit] = false
	return nil
}

func (f *Fake) Restart(unit string) error {
	if err := f.record(fmt.Sprintf("Restart(%s)", unit)); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activity[unit] = true
	return nil
}
