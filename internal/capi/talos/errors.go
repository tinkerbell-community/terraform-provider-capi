// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Sentinel errors returned by the bootstrapper. They are always wrapped in
// capi.BootstrapError by the public Bootstrapper methods.
var (
	ErrMachineNotFound   = errors.New("bootstrap machine not found in inventory")
	ErrUnsupported       = errors.New("operation not supported by this BMC")
	ErrNoBootMethod      = errors.New("no supported boot method: BMC supports neither virtual media nor UEFI HTTP boot")
	ErrBootTimeout       = errors.New("node did not enter Talos maintenance mode within the boot timeout")
	ErrInstallTimeout    = errors.New("node did not come back with the applied configuration within the install timeout")
	ErrBootstrapTimeout  = errors.New("etcd did not bootstrap within the timeout")
	ErrKubernetesTimeout = errors.New("kubernetes API did not become reachable within the timeout")
	ErrAddons            = errors.New("addon installation failed")
	ErrReadyTimeout      = errors.New("node did not become Ready within the timeout")
	ErrAttemptsExhausted = errors.New("boot attempts exhausted")
	ErrUnknownState      = errors.New("unknown node state")
	ErrActionFailed      = errors.New("action failed repeatedly")
)

// BMCError carries bmclib's per-provider metadata alongside the failure.
type BMCError struct {
	Op        string
	Err       error
	Attempted []string
	Failed    map[string]string
}

// Error renders the operation, the underlying error, and which providers failed.
func (e *BMCError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "bmc %s: %v", e.Op, e.Err)
	if len(e.Attempted) > 0 {
		fmt.Fprintf(&b, " (providers attempted: %s)", strings.Join(e.Attempted, ", "))
	}
	if len(e.Failed) > 0 {
		names := make([]string, 0, len(e.Failed))
		for name := range e.Failed {
			names = append(names, name)
		}
		sort.Strings(names)
		parts := make([]string, 0, len(names))
		for _, name := range names {
			parts = append(parts, name+": "+e.Failed[name])
		}
		fmt.Fprintf(&b, " (provider failures: %s)", strings.Join(parts, "; "))
	}
	return b.String()
}

// Unwrap exposes the underlying error for errors.Is.
func (e *BMCError) Unwrap() error { return e.Err }
