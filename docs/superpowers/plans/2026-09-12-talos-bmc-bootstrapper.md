# Talos BMC Bootstrapper Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `capi.Bootstrapper` that turns one bare-metal machine into a single-node Talos cluster by driving its BMC with bmclib, applying Talos machine config with the Talos machinery library, bootstrapping etcd, and installing Helm/manifest addons, then tears it down after the CAPI pivot.

**Architecture:** A new package `internal/capi/talos` implements an observed-state reconciler: each loop probes the BMC and the Talos API, a pure planner picks one action, an action layer executes small idempotent steps, and the loop repeats until the node is a ready cluster. Three interfaces (`BMC`, `Node`, `Kube`) hide bmclib, Talos machinery, and client-go so the whole loop runs against an in-memory simulated node in tests. The provider gains a `management.bootstrap` nested attribute and picks the Talos bootstrapper per operation.

**Tech Stack:** Go 1.26, terraform-plugin-framework v1.19, bmclib v2 (tinkerbell-community fork via `replace`), Talos machinery v1.13.6, Helm SDK v3.21.4, client-go v0.36.4, stdlib `testing` (no testify).

**Spec:** `docs/superpowers/specs/2026-09-12-talos-bmc-bootstrapper-design.md`

## Global Constraints

- Module path: `github.com/tinkerbell-community/terraform-provider-capi`. New package: `internal/capi/talos`.
- Every Go file starts with the repository header:
  ```go
  // Copyright IBM Corp. 2021, 2026
  // SPDX-License-Identifier: MPL-2.0
  ```
- Dependencies pinned exactly: `github.com/bmc-toolbox/bmclib/v2 v2.3.6-0.20260724022505-33fe4e06a8da` with `replace github.com/bmc-toolbox/bmclib/v2 => github.com/tinkerbell-community/bmclib/v2 v2.0.0-20260910214435-7baaf88e8399` and `replace github.com/jacobweinstock/iamt => github.com/tinkerbell-community/iamt v0.0.0-20260910214403-be779a24a30d`; `github.com/siderolabs/talos/pkg/machinery v1.13.6`; `helm.sh/helm/v3 v3.21.4`. No `terraform-plugin-framework-validators` (not in go.mod; validation is hand-written in `validateLifecycleConfig`).
- Tests use the standard library `testing` package only, following `internal/capi/manager_test.go` style (`t.Fatalf`, `t.Errorf`). Lint config is `.golangci.yml` (errcheck, staticcheck, godot: every comment ends with a period, unparam, unused, usetesting).
- Test command: `go test -v -cover -timeout=120s -parallel=10 ./...` (`make test`). Lint: `golangci-lint run`. Format: `gofmt -s -w -e .`.
- Schema naming per `.claude/CLAUDE.md`: nested attributes, snake_case, no blocks. `management.bootstrap` has `objectplanmodifier.RequiresReplace()` on the whole object.
- The bootstrap node is transient. Nothing about it enters Terraform state except the existing `status.bootstrap_cluster` string.
- Poll interval and every timeout must be injectable so unit tests run in milliseconds.

## File Structure

| File | Responsibility |
|---|---|
| `internal/capi/talos/state.go` | Enums (`PowerState`, `TalosState`, `EtcdState`, `K8sState`, `BootDevice`, `ActionKind`), `Observation`, `History`, `Action`, `BootOverride` |
| `internal/capi/talos/errors.go` | Sentinel errors and `BMCError` |
| `internal/capi/talos/planner.go` | `Plan(PlanInput) Action`, pure |
| `internal/capi/talos/backoff.go` | `retry` and `waitFor` helpers |
| `internal/capi/talos/bmc.go` | `BMC` interface, `BMCCredentials`, `BMCLib` implementation over bmclib |
| `internal/capi/talos/node.go` | `Node` interface, `MachineryNode` implementation over Talos machinery, maintenance-mode detection |
| `internal/capi/talos/kube.go` | `Kube` interface, `ClientGoKube` implementation |
| `internal/capi/talos/image.go` | `ImageSpec`, `ImageURLs`, `ImageResolver`, `FactoryResolver` (Image Factory client) |
| `internal/capi/talos/config.go` | `ConfigInput`, `GeneratedConfig`, `NewSecretsBundle`, `GenerateConfig` |
| `internal/capi/talos/addons.go` | `HelmRelease`, `Addons`, `HelmClient`, `AddonInstaller`, `DefaultAddonInstaller`, `SDKHelmClient` |
| `internal/capi/talos/reconciler.go` | `Timeouts`, `session`, `reconciler` loop and actions |
| `internal/capi/talos/bootstrapper.go` | `Config` and sub-structs, `Option`s, `Bootstrapper` (`Create`, `Delete`, `Exists`) |
| `internal/capi/talos/sim_test.go` | `simMachine` implementing `BMC`, `Node`, `Kube` with injectable faults; fake resolver and addon installer |
| `internal/capi/talos/acceptance_test.go` | Env-gated real-hardware test |
| `internal/provider/cluster_resource_models.go` | New models, attr types, extractors for `management.bootstrap` |
| `internal/provider/cluster_resource.go` | Schema for `management.bootstrap`, validation, `managerFor`, Delete guard |
| `internal/provider/talos_bootstrap.go` | `buildTalosBootstrapConfig` and `managerFor` |
| `examples/resources/capi_cluster/talos-bootstrap.tf`, `examples/resources/capi_cluster/cni-none.yaml` | Example |
| `.claude/CLAUDE.md` | Schema doc update |

---

### Task 0: Commit the pending dependency bump

`go.mod` and `go.sum` carry an uncommitted bump (Go 1.26, terraform-plugin-log 0.11, k8s 0.36.4, cluster-api 1.14.0, kind 0.32.0). Commit it on its own so later diffs stay readable.

**Files:**
- Modify: `go.mod`, `go.sum` (already modified)

- [ ] **Step 1: Verify the bump builds and tests pass**

Run: `go build ./... && go test -timeout=120s ./...`
Expected: build succeeds, all tests `ok`.

- [ ] **Step 2: Commit**

```bash
git add go.mod go.sum
git commit -m "Bump k8s, cluster-api, kind, and plugin-log dependencies"
```

---

### Task 1: State types, errors, and the pure planner

**Files:**
- Create: `internal/capi/talos/state.go`
- Create: `internal/capi/talos/errors.go`
- Create: `internal/capi/talos/planner.go`
- Test: `internal/capi/talos/planner_test.go`

**Interfaces:**
- Produces: all enums and structs below; `Plan(in PlanInput) Action`. Every later task uses these names verbatim.

- [ ] **Step 1: Write the failing planner test**

`internal/capi/talos/planner_test.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"errors"
	"testing"
)

func TestPlan(t *testing.T) {
	tests := []struct {
		name      string
		obs       Observation
		hist      History
		hasAddons bool
		want      ActionKind
		wantErr   error
	}{
		{name: "unreachable boots installer", obs: Observation{Talos: TalosUnreachable}, want: ActionBootInstaller},
		{name: "foreign boots installer", obs: Observation{Talos: TalosForeign}, want: ActionBootInstaller},
		{name: "unreachable after max attempts fails", obs: Observation{Talos: TalosUnreachable}, hist: History{BootAttempts: 3}, want: ActionFail, wantErr: ErrAttemptsExhausted},
		{name: "maintenance applies config", obs: Observation{Talos: TalosMaintenance}, want: ActionApplyConfig},
		{name: "maintenance after apply reboots to disk", obs: Observation{Talos: TalosMaintenance}, hist: History{ConfigApplied: true, BootAttempts: 1}, want: ActionRebootToDisk},
		{name: "maintenance after apply at max attempts fails", obs: Observation{Talos: TalosMaintenance}, hist: History{ConfigApplied: true, BootAttempts: 3}, want: ActionFail, wantErr: ErrAttemptsExhausted},
		{name: "ours with media attached detaches", obs: Observation{Talos: TalosOurs, Etcd: EtcdNotBootstrapped}, hist: History{MediaAttached: true}, want: ActionDetachMedia},
		{name: "ours not bootstrapped bootstraps", obs: Observation{Talos: TalosOurs, Etcd: EtcdNotBootstrapped}, want: ActionBootstrapEtcd},
		{name: "ours bootstrapped kube unreachable waits", obs: Observation{Talos: TalosOurs, Etcd: EtcdBootstrapped, Kubernetes: K8sUnreachable}, want: ActionWaitKubernetes},
		{name: "ours kube reachable installs addons", obs: Observation{Talos: TalosOurs, Etcd: EtcdBootstrapped, Kubernetes: K8sReachable}, want: ActionInstallAddons},
		{name: "addons installed with addons waits ready", obs: Observation{Talos: TalosOurs, Etcd: EtcdBootstrapped, Kubernetes: K8sReachable}, hist: History{AddonsInstalled: true}, hasAddons: true, want: ActionWaitReady},
		{name: "addons installed without addons is done", obs: Observation{Talos: TalosOurs, Etcd: EtcdBootstrapped, Kubernetes: K8sReachable}, hist: History{AddonsInstalled: true}, want: ActionDone},
		{name: "node ready is done", obs: Observation{Talos: TalosOurs, Etcd: EtcdBootstrapped, Kubernetes: K8sNodeReady}, hist: History{AddonsInstalled: true}, hasAddons: true, want: ActionDone},
		{name: "unknown state fails", obs: Observation{Talos: TalosState("bogus")}, want: ActionFail, wantErr: ErrUnknownState},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Plan(PlanInput{Observation: tt.obs, History: tt.hist, MaxAttempts: 3, HasAddons: tt.hasAddons})
			if got.Kind != tt.want {
				t.Fatalf("Plan() kind = %q, want %q", got.Kind, tt.want)
			}
			if tt.wantErr != nil && !errors.Is(got.Err, tt.wantErr) {
				t.Fatalf("Plan() err = %v, want %v", got.Err, tt.wantErr)
			}
			if tt.wantErr == nil && got.Err != nil {
				t.Fatalf("Plan() unexpected err = %v", got.Err)
			}
		})
	}
}

func TestObservationString(t *testing.T) {
	obs := Observation{Power: PowerOn, Talos: TalosOurs, Etcd: EtcdBootstrapped, Kubernetes: K8sReachable}
	got := obs.String()
	want := "power=on talos=ours etcd=bootstrapped kubernetes=reachable"
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/capi/talos/ -run 'TestPlan|TestObservationString' -v`
Expected: FAIL to compile with `undefined: Observation` (and friends).

- [ ] **Step 3: Write state.go**

`internal/capi/talos/state.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

// Package talos implements a capi.Bootstrapper that provisions a single-node
// Talos Linux cluster on a bare-metal machine through its BMC.
//
// The bootstrapper is an observed-state reconciler: each iteration probes the
// BMC and the Talos API, a pure planner chooses exactly one action from the
// observation, and the action layer executes small idempotent steps. Nothing
// about the node is persisted; a restart converges again from observation.
package talos

import (
	"fmt"
	"time"
)

// PowerState is the BMC-reported power state of the machine.
type PowerState string

// PowerState values.
const (
	PowerUnknown PowerState = "unknown"
	PowerOn      PowerState = "on"
	PowerOff     PowerState = "off"
)

// TalosState classifies what answers on the Talos API port.
type TalosState string

// TalosState values.
const (
	// TalosUnreachable means no TLS handshake completed on the API port.
	TalosUnreachable TalosState = "unreachable"
	// TalosMaintenance means the maintenance service (installer) answered.
	TalosMaintenance TalosState = "maintenance"
	// TalosOurs means a configured node accepted this run's talosconfig.
	TalosOurs TalosState = "ours"
	// TalosForeign means a configured node rejected this run's talosconfig.
	TalosForeign TalosState = "foreign"
)

// EtcdState is the etcd bootstrap status of a configured node.
type EtcdState string

// EtcdState values.
const (
	EtcdUnknown         EtcdState = "unknown"
	EtcdNotBootstrapped EtcdState = "not-bootstrapped"
	EtcdBootstrapped    EtcdState = "bootstrapped"
)

// K8sState is the Kubernetes readiness of the bootstrap cluster.
type K8sState string

// K8sState values.
const (
	K8sUnreachable K8sState = "unreachable"
	K8sReachable   K8sState = "reachable"
	K8sNodeReady   K8sState = "node-ready"
)

// BootDevice is a bmclib boot device name.
type BootDevice string

// BootDevice values used by the bootstrapper.
const (
	BootDeviceCDROM    BootDevice = "cdrom"
	BootDeviceUEFIHTTP BootDevice = "uefi_http"
	BootDeviceDisk     BootDevice = "disk"
)

// BootOverride describes the BMC's boot device override.
type BootOverride struct {
	Device     BootDevice
	Persistent bool
	EFI        bool
}

// Observation is a snapshot of the machine taken at the start of each loop.
type Observation struct {
	Power      PowerState
	Talos      TalosState
	Etcd       EtcdState
	Kubernetes K8sState
	ObservedAt time.Time
}

// String renders the observation for logs.
func (o Observation) String() string {
	return fmt.Sprintf("power=%s talos=%s etcd=%s kubernetes=%s", o.Power, o.Talos, o.Etcd, o.Kubernetes)
}

// History is the in-memory memory of what the reconciler has done this run.
type History struct {
	// BootAttempts counts BootInstaller and RebootToDisk cycles started.
	BootAttempts int
	// ConfigApplied is set once ApplyConfiguration succeeded this run.
	ConfigApplied bool
	// WentDownAfterApply is set once the Talos API was observed unreachable after ConfigApplied.
	WentDownAfterApply bool
	// MediaAttached is set while installer media is believed to be attached.
	MediaAttached bool
	// AddonsInstalled is set once the addon installer returned success.
	AddonsInstalled bool
}

// ActionKind identifies the next step the planner chose.
type ActionKind string

// ActionKind values.
const (
	ActionBootInstaller  ActionKind = "boot-installer"
	ActionApplyConfig    ActionKind = "apply-config"
	ActionRebootToDisk   ActionKind = "reboot-to-disk"
	ActionDetachMedia    ActionKind = "detach-media"
	ActionBootstrapEtcd  ActionKind = "bootstrap-etcd"
	ActionWaitKubernetes ActionKind = "wait-kubernetes"
	ActionInstallAddons  ActionKind = "install-addons"
	ActionWaitReady      ActionKind = "wait-ready"
	ActionDone           ActionKind = "done"
	ActionFail           ActionKind = "fail"
)

// Action is the planner's decision. Err is set only for ActionFail.
type Action struct {
	Kind ActionKind
	Err  error
}
```

- [ ] **Step 4: Write errors.go**

`internal/capi/talos/errors.go`:

```go
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
```

- [ ] **Step 5: Write planner.go**

`internal/capi/talos/planner.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

// PlanInput is everything the planner may look at.
type PlanInput struct {
	Observation Observation
	History     History
	// MaxAttempts is boot.attempts from the configuration.
	MaxAttempts int
	// HasAddons is true when at least one Helm release or manifest was configured.
	HasAddons bool
}

// Plan chooses exactly one action from the observation and history. It is a
// pure function: no I/O, no time, so every branch is table-testable.
func Plan(in PlanInput) Action {
	obs, h := in.Observation, in.History

	switch obs.Talos {
	case TalosUnreachable, TalosForeign:
		if h.BootAttempts >= in.MaxAttempts {
			return Action{Kind: ActionFail, Err: ErrAttemptsExhausted}
		}
		return Action{Kind: ActionBootInstaller}

	case TalosMaintenance:
		if !h.ConfigApplied {
			return Action{Kind: ActionApplyConfig}
		}
		if h.BootAttempts >= in.MaxAttempts {
			return Action{Kind: ActionFail, Err: ErrAttemptsExhausted}
		}
		return Action{Kind: ActionRebootToDisk}

	case TalosOurs:
		switch {
		case h.MediaAttached:
			return Action{Kind: ActionDetachMedia}
		case obs.Etcd != EtcdBootstrapped:
			return Action{Kind: ActionBootstrapEtcd}
		case obs.Kubernetes == K8sUnreachable:
			return Action{Kind: ActionWaitKubernetes}
		case !h.AddonsInstalled:
			return Action{Kind: ActionInstallAddons}
		case in.HasAddons && obs.Kubernetes != K8sNodeReady:
			return Action{Kind: ActionWaitReady}
		default:
			return Action{Kind: ActionDone}
		}
	}

	return Action{Kind: ActionFail, Err: ErrUnknownState}
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/capi/talos/ -run 'TestPlan|TestObservationString' -v`
Expected: PASS, all 14 subtests plus `TestObservationString`.

- [ ] **Step 7: Lint and commit**

Run: `gofmt -s -l internal/capi/talos/ && golangci-lint run ./internal/capi/talos/`
Expected: no output from gofmt, no lint findings.

```bash
git add internal/capi/talos/state.go internal/capi/talos/errors.go internal/capi/talos/planner.go internal/capi/talos/planner_test.go
git commit -m "Add Talos bootstrapper state types and pure planner"
```

---

### Task 2: Retry and wait helpers

**Files:**
- Create: `internal/capi/talos/backoff.go`
- Test: `internal/capi/talos/backoff_test.go`

**Interfaces:**
- Produces: `retry(ctx context.Context, attempts int, base, maxDelay time.Duration, fn func(context.Context) error) error` and `waitFor(ctx context.Context, timeout, interval time.Duration, cond func(context.Context) (bool, error)) error`. `waitFor` returns `context.DeadlineExceeded` on timeout and the condition's error if it returns one.

- [ ] **Step 1: Write the failing tests**

`internal/capi/talos/backoff_test.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetry_SucceedsAfterFailures(t *testing.T) {
	calls := 0
	err := retry(context.Background(), 3, time.Millisecond, 2*time.Millisecond, func(context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retry() error = %v", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestRetry_ReturnsLastErrorAfterAttempts(t *testing.T) {
	calls := 0
	want := errors.New("still broken")
	err := retry(context.Background(), 2, time.Millisecond, time.Millisecond, func(context.Context) error {
		calls++
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("retry() error = %v, want %v", err, want)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestRetry_DoesNotRetryUnsupported(t *testing.T) {
	calls := 0
	err := retry(context.Background(), 3, time.Millisecond, time.Millisecond, func(context.Context) error {
		calls++
		return ErrUnsupported
	})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("retry() error = %v, want ErrUnsupported", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestRetry_StopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := retry(ctx, 5, 50*time.Millisecond, 50*time.Millisecond, func(context.Context) error {
		calls++
		cancel()
		return errors.New("transient")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("retry() error = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestWaitFor_ConditionBecomesTrue(t *testing.T) {
	n := 0
	err := waitFor(context.Background(), time.Second, time.Millisecond, func(context.Context) (bool, error) {
		n++
		return n >= 3, nil
	})
	if err != nil {
		t.Fatalf("waitFor() error = %v", err)
	}
}

func TestWaitFor_Timeout(t *testing.T) {
	err := waitFor(context.Background(), 20*time.Millisecond, time.Millisecond, func(context.Context) (bool, error) {
		return false, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waitFor() error = %v, want DeadlineExceeded", err)
	}
}

func TestWaitFor_ConditionError(t *testing.T) {
	want := errors.New("boom")
	err := waitFor(context.Background(), time.Second, time.Millisecond, func(context.Context) (bool, error) {
		return false, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("waitFor() error = %v, want %v", err, want)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/capi/talos/ -run 'TestRetry|TestWaitFor' -v`
Expected: FAIL to compile with `undefined: retry`.

- [ ] **Step 3: Write backoff.go**

`internal/capi/talos/backoff.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"
)

// retry calls fn up to attempts times with jittered exponential backoff
// between calls. It stops early on context cancellation and on
// ErrUnsupported, which no retry can fix.
func retry(ctx context.Context, attempts int, base, maxDelay time.Duration, fn func(context.Context) error) error {
	var err error
	delay := base
	for i := 0; i < attempts; i++ {
		err = fn(ctx)
		if err == nil {
			return nil
		}
		if errors.Is(err, ErrUnsupported) || ctx.Err() != nil {
			break
		}
		if i == attempts-1 {
			break
		}
		jitter := time.Duration(rand.Int64N(int64(delay)/2 + 1)) //nolint:gosec // jitter, not security
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay + jitter):
		}
		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// waitFor polls cond every interval until it returns true, returns an error,
// or timeout elapses. A timeout surfaces as context.DeadlineExceeded.
func waitFor(ctx context.Context, timeout, interval time.Duration, cond func(context.Context) (bool, error)) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		ok, err := cond(ctx)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/capi/talos/ -run 'TestRetry|TestWaitFor' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/capi/talos/backoff.go internal/capi/talos/backoff_test.go
git commit -m "Add retry and waitFor helpers for Talos bootstrapper"
```

---
### Task 3: BMC interface and bmclib implementation

**Files:**
- Create: `internal/capi/talos/bmc.go`
- Test: `internal/capi/talos/bmc_test.go`
- Modify: `go.mod`, `go.sum` (bmclib + replaces)

**Interfaces:**
- Produces:
  ```go
  type BMCCredentials struct{ Address, Username, Password string }
  type BMC interface {
      PowerState(ctx context.Context) (PowerState, error)
      PowerOn(ctx context.Context) error
      PowerOff(ctx context.Context) error
      PowerCycle(ctx context.Context) error
      SetBootDevice(ctx context.Context, dev BootDevice, persistent, efi bool) error
      InsertMedia(ctx context.Context, isoURL string) error
      EjectMedia(ctx context.Context) error
      SetHTTPBootURI(ctx context.Context, uri string) error
      PostCode(ctx context.Context) (string, error)
  }
  func NewBMCLib(creds BMCCredentials, logger *log.Logger) *BMCLib
  ```
  Unsupported operations return an error wrapping `ErrUnsupported`. All other failures are `*BMCError`.

- [ ] **Step 1: Add the bmclib dependency with the fork replaces**

```bash
go mod edit -replace github.com/bmc-toolbox/bmclib/v2=github.com/tinkerbell-community/bmclib/v2@v2.0.0-20260910214435-7baaf88e8399
go mod edit -replace github.com/jacobweinstock/iamt=github.com/tinkerbell-community/iamt@v0.0.0-20260910214403-be779a24a30d
go get github.com/bmc-toolbox/bmclib/v2@v2.3.6-0.20260724022505-33fe4e06a8da
```

Expected: `go.mod` now has the require line and both `replace` lines. Do not run `go mod tidy` until bmc.go imports the package (tidy would drop it).

- [ ] **Step 2: Write the failing test**

The bmclib client is a concrete struct, so `BMCLib` talks to it through a tiny `bmcSession` interface that the test fakes.

`internal/capi/talos/bmc_test.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bmc-toolbox/bmclib/v2/bmc"
)

type fakeSession struct {
	opened, closed bool
	power          string
	powerErr       error
	setPowerCalls  []string
	bootCalls      []string
	mediaCalls     []string
	httpCalls      []string
	setErr         error
	metadata       bmc.Metadata
}

func (f *fakeSession) Open(context.Context) error  { f.opened = true; return nil }
func (f *fakeSession) Close(context.Context) error { f.closed = true; return nil }
func (f *fakeSession) GetPowerState(context.Context) (string, error) {
	return f.power, f.powerErr
}
func (f *fakeSession) SetPowerState(_ context.Context, state string) (bool, error) {
	f.setPowerCalls = append(f.setPowerCalls, state)
	return f.setErr == nil, f.setErr
}
func (f *fakeSession) SetBootDevice(_ context.Context, dev string, persistent, efi bool) (bool, error) {
	f.bootCalls = append(f.bootCalls, dev)
	return f.setErr == nil, f.setErr
}
func (f *fakeSession) SetVirtualMedia(_ context.Context, kind, url string) (bool, error) {
	f.mediaCalls = append(f.mediaCalls, kind+":"+url)
	return f.setErr == nil, f.setErr
}
func (f *fakeSession) SetHTTPBootURI(_ context.Context, uri string) (bool, error) {
	f.httpCalls = append(f.httpCalls, uri)
	return f.setErr == nil, f.setErr
}
func (f *fakeSession) PostCode(context.Context) (string, int, error) { return "ok", 0, nil }
func (f *fakeSession) GetMetadata() bmc.Metadata                   { return f.metadata }

func newTestBMC(s *fakeSession) *BMCLib {
	b := NewBMCLib(BMCCredentials{Address: "10.0.0.1", Username: "u", Password: "p"}, nil)
	b.newSession = func() bmcSession { return s }
	return b
}

func TestBMCLib_PowerStateNormalizesCase(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want PowerState
	}{{"On", PowerOn}, {"on", PowerOn}, {"Off", PowerOff}, {"off", PowerOff}, {"", PowerUnknown}, {"Paused", PowerUnknown}} {
		s := &fakeSession{power: tc.raw}
		got, err := newTestBMC(s).PowerState(context.Background())
		if err != nil {
			t.Fatalf("PowerState(%q) error = %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("PowerState(%q) = %q, want %q", tc.raw, got, tc.want)
		}
		if !s.opened || !s.closed {
			t.Fatalf("session should be opened and closed, got opened=%v closed=%v", s.opened, s.closed)
		}
	}
}

func TestBMCLib_PowerCommands(t *testing.T) {
	s := &fakeSession{}
	b := newTestBMC(s)
	ctx := context.Background()
	if err := b.PowerOn(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.PowerOff(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.PowerCycle(ctx); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s.setPowerCalls, ","); got != "on,off,cycle" {
		t.Fatalf("power calls = %q", got)
	}
}

func TestBMCLib_MediaAndBoot(t *testing.T) {
	s := &fakeSession{}
	b := newTestBMC(s)
	ctx := context.Background()
	if err := b.InsertMedia(ctx, "https://example.com/talos.iso"); err != nil {
		t.Fatal(err)
	}
	if err := b.EjectMedia(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.SetBootDevice(ctx, BootDeviceCDROM, false, true); err != nil {
		t.Fatal(err)
	}
	if err := b.SetHTTPBootURI(ctx, "https://example.com/uki.efi"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s.mediaCalls, ","); got != "CD:https://example.com/talos.iso,CD:" {
		t.Fatalf("media calls = %q", got)
	}
	if got := strings.Join(s.bootCalls, ","); got != "cdrom" {
		t.Fatalf("boot calls = %q", got)
	}
	if got := strings.Join(s.httpCalls, ","); got != "https://example.com/uki.efi" {
		t.Fatalf("http calls = %q", got)
	}
}

func TestBMCLib_UnsupportedMapsToErrUnsupported(t *testing.T) {
	s := &fakeSession{setErr: errors.New("1 error occurred: no VirtualMediaSetter implementations found")}
	err := newTestBMC(s).InsertMedia(context.Background(), "https://example.com/talos.iso")
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("InsertMedia() error = %v, want ErrUnsupported", err)
	}
}

func TestBMCLib_ErrorCarriesMetadata(t *testing.T) {
	s := &fakeSession{
		setErr: errors.New("boom"),
		metadata: bmc.Metadata{
			ProvidersAttempted:   []string{"gofish", "ipmitool"},
			FailedProviderDetail: map[string]string{"gofish": "401", "ipmitool": "timeout"},
		},
	}
	err := newTestBMC(s).PowerOn(context.Background())
	var bmcErr *BMCError
	if !errors.As(err, &bmcErr) {
		t.Fatalf("PowerOn() error = %T, want *BMCError", err)
	}
	msg := err.Error()
	for _, want := range []string{"bmc power-on: boom", "gofish, ipmitool", "gofish: 401", "ipmitool: timeout"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q missing %q", msg, want)
		}
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/capi/talos/ -run TestBMCLib -v`
Expected: FAIL to compile with `undefined: NewBMCLib`.

- [ ] **Step 4: Write bmc.go**

`internal/capi/talos/bmc.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	bmclib "github.com/bmc-toolbox/bmclib/v2"
	"github.com/bmc-toolbox/bmclib/v2/bmc"
	bmclibErrs "github.com/bmc-toolbox/bmclib/v2/errors"
)

// BMCCredentials identifies and authenticates to a machine's BMC.
type BMCCredentials struct {
	Address  string
	Username string
	Password string
}

// BMC is the generic out-of-band control surface the reconciler needs.
// Unsupported operations return an error wrapping ErrUnsupported.
type BMC interface {
	PowerState(ctx context.Context) (PowerState, error)
	PowerOn(ctx context.Context) error
	PowerOff(ctx context.Context) error
	PowerCycle(ctx context.Context) error
	SetBootDevice(ctx context.Context, dev BootDevice, persistent, efi bool) error
	InsertMedia(ctx context.Context, isoURL string) error
	EjectMedia(ctx context.Context) error
	SetHTTPBootURI(ctx context.Context, uri string) error
	PostCode(ctx context.Context) (string, error)
}

// bmcSession is the slice of *bmclib.Client the implementation uses, so tests
// can substitute a fake.
type bmcSession interface {
	Open(ctx context.Context) error
	Close(ctx context.Context) error
	GetPowerState(ctx context.Context) (string, error)
	SetPowerState(ctx context.Context, state string) (bool, error)
	SetBootDevice(ctx context.Context, bootDevice string, setPersistent, efiBoot bool) (bool, error)
	SetVirtualMedia(ctx context.Context, kind, mediaURL string) (bool, error)
	SetHTTPBootURI(ctx context.Context, uri string) (bool, error)
	PostCode(ctx context.Context) (string, int, error)
	GetMetadata() bmc.Metadata
}

const (
	defaultBMCTimeout = 90 * time.Second
	perProviderTimeout = 30 * time.Second
	virtualMediaKindCD = "CD"
)

// BMCLib implements BMC with a fresh bmclib session per operation.
type BMCLib struct {
	creds      BMCCredentials
	logger     *log.Logger
	timeout    time.Duration
	newSession func() bmcSession
}

// NewBMCLib returns a BMC backed by bmclib with provider autodetection.
func NewBMCLib(creds BMCCredentials, logger *log.Logger) *BMCLib {
	if logger == nil {
		logger = log.New(log.Writer(), "[talos-bmc] ", log.LstdFlags)
	}
	b := &BMCLib{creds: creds, logger: logger, timeout: defaultBMCTimeout}
	b.newSession = func() bmcSession {
		return bmclib.NewClient(creds.Address, creds.Username, creds.Password,
			bmclib.WithPerProviderTimeout(perProviderTimeout))
	}
	return b
}

// withSession opens a session, runs fn, and closes the session. Errors are
// annotated with bmclib's provider metadata.
func (b *BMCLib) withSession(ctx context.Context, op string, fn func(context.Context, bmcSession) error) error {
	s := b.newSession()
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()

	if err := s.Open(ctx); err != nil {
		return wrapBMCError(op, err, s.GetMetadata())
	}
	defer func() {
		if cerr := s.Close(ctx); cerr != nil {
			b.logger.Printf("bmc %s: close: %v", op, cerr)
		}
	}()

	if err := fn(ctx, s); err != nil {
		return wrapBMCError(op, err, s.GetMetadata())
	}
	return nil
}

// wrapBMCError maps "no provider implements this" to ErrUnsupported and
// everything else to a *BMCError carrying provider metadata.
func wrapBMCError(op string, err error, md bmc.Metadata) error {
	if errors.Is(err, bmclibErrs.ErrProviderImplementation) ||
		errors.Is(err, bmclibErrs.ErrNotImplemented) ||
		strings.Contains(err.Error(), "implementations found") {
		return fmt.Errorf("bmc %s: %w", op, ErrUnsupported)
	}
	return &BMCError{Op: op, Err: err, Attempted: md.ProvidersAttempted, Failed: md.FailedProviderDetail}
}

// PowerState reads the BMC-reported power state, normalized to on/off/unknown.
func (b *BMCLib) PowerState(ctx context.Context) (PowerState, error) {
	state := PowerUnknown
	err := b.withSession(ctx, "power-state", func(ctx context.Context, s bmcSession) error {
		raw, err := s.GetPowerState(ctx)
		if err != nil {
			return err
		}
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "on":
			state = PowerOn
		case "off":
			state = PowerOff
		}
		return nil
	})
	return state, err
}

func (b *BMCLib) setPower(ctx context.Context, op, state string) error {
	return b.withSession(ctx, op, func(ctx context.Context, s bmcSession) error {
		_, err := s.SetPowerState(ctx, state)
		return err
	})
}

// PowerOn powers the machine on.
func (b *BMCLib) PowerOn(ctx context.Context) error { return b.setPower(ctx, "power-on", "on") }

// PowerOff hard-powers the machine off.
func (b *BMCLib) PowerOff(ctx context.Context) error { return b.setPower(ctx, "power-off", "off") }

// PowerCycle hard-resets the machine.
func (b *BMCLib) PowerCycle(ctx context.Context) error { return b.setPower(ctx, "power-cycle", "cycle") }

// SetBootDevice sets the next-boot (or persistent) boot device.
func (b *BMCLib) SetBootDevice(ctx context.Context, dev BootDevice, persistent, efi bool) error {
	return b.withSession(ctx, "set-boot-device", func(ctx context.Context, s bmcSession) error {
		_, err := s.SetBootDevice(ctx, string(dev), persistent, efi)
		return err
	})
}

// InsertMedia attaches an ISO as virtual CD media.
func (b *BMCLib) InsertMedia(ctx context.Context, isoURL string) error {
	return b.withSession(ctx, "insert-media", func(ctx context.Context, s bmcSession) error {
		_, err := s.SetVirtualMedia(ctx, virtualMediaKindCD, isoURL)
		return err
	})
}

// EjectMedia detaches virtual CD media.
func (b *BMCLib) EjectMedia(ctx context.Context) error {
	return b.withSession(ctx, "eject-media", func(ctx context.Context, s bmcSession) error {
		_, err := s.SetVirtualMedia(ctx, virtualMediaKindCD, "")
		return err
	})
}

// SetHTTPBootURI sets the UEFI HTTP boot URI. An empty uri clears it.
func (b *BMCLib) SetHTTPBootURI(ctx context.Context, uri string) error {
	return b.withSession(ctx, "set-http-boot-uri", func(ctx context.Context, s bmcSession) error {
		_, err := s.SetHTTPBootURI(ctx, uri)
		return err
	})
}

// PostCode returns the BIOS POST code as a diagnostic string.
func (b *BMCLib) PostCode(ctx context.Context) (string, error) {
	var out string
	err := b.withSession(ctx, "post-code", func(ctx context.Context, s bmcSession) error {
		status, code, err := s.PostCode(ctx)
		if err != nil {
			return err
		}
		out = fmt.Sprintf("%s (0x%02x)", status, code)
		return nil
	})
	return out, err
}
```

- [ ] **Step 5: Tidy and run the tests**

Run: `go mod tidy && go test ./internal/capi/talos/ -run TestBMCLib -v`
Expected: PASS. Confirm `go.mod` still has the bmclib require and both replace lines (`grep -n bmclib go.mod`).

- [ ] **Step 6: Build everything and lint**

Run: `go build ./... && golangci-lint run ./internal/capi/talos/`
Expected: builds; no findings.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/capi/talos/bmc.go internal/capi/talos/bmc_test.go
git commit -m "Add bmclib-backed BMC interface for Talos bootstrapper"
```

---

### Task 4: Node interface and Talos machinery implementation

**Files:**
- Create: `internal/capi/talos/node.go`
- Test: `internal/capi/talos/node_test.go`
- Modify: `go.mod`, `go.sum` (machinery)

**Interfaces:**
- Produces:
  ```go
  type Node interface {
      Probe(ctx context.Context, tc *clientconfig.Config) (TalosState, error)
      ApplyConfiguration(ctx context.Context, cfg []byte) error
      Bootstrap(ctx context.Context, tc *clientconfig.Config) error
      EtcdState(ctx context.Context, tc *clientconfig.Config) (EtcdState, error)
      Kubeconfig(ctx context.Context, tc *clientconfig.Config) ([]byte, error)
      Reset(ctx context.Context, tc *clientconfig.Config) error
  }
  func NewMachineryNode(ip string) *MachineryNode
  ```
  `clientconfig` is `github.com/siderolabs/talos/pkg/machinery/client/config`.

- [ ] **Step 1: Add the machinery dependency**

```bash
go get github.com/siderolabs/talos/pkg/machinery@v1.13.6
```

- [ ] **Step 2: Write the failing test**

The test starts a real gRPC server with a self-signed certificate whose CN is the Talos maintenance CN, and a second server that requires client certificates, to exercise the three classifications.

`internal/capi/talos/node_test.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"

	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	"github.com/siderolabs/talos/pkg/machinery/constants"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type fakeMachineService struct {
	machineapi.UnimplementedMachineServiceServer
}

func (fakeMachineService) Version(context.Context, *machineapi.VersionRequest) (*machineapi.VersionResponse, error) {
	return &machineapi.VersionResponse{Messages: []*machineapi.Version{{Version: &machineapi.VersionInfo{Tag: "v1.13.6"}}}}, nil
}

func selfSignedCert(t *testing.T, cn string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// startTalosLike starts a gRPC MachineService on 127.0.0.1 with the given
// server cert and client-auth policy and returns "host:port".
func startTalosLike(t *testing.T, cert tls.Certificate, clientAuth tls.ClientAuthType) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   clientAuth,
		MinVersion:   tls.VersionTLS12,
	})))
	machineapi.RegisterMachineServiceServer(srv, fakeMachineService{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

func TestMachineryNode_ProbeUnreachable(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	_ = lis.Close() // nothing listens here now

	n := NewMachineryNode(addr)
	n.dialTimeout = 500 * time.Millisecond
	got, err := n.Probe(context.Background(), nil)
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	if got != TalosUnreachable {
		t.Fatalf("Probe() = %q, want unreachable", got)
	}
}

func TestMachineryNode_ProbeMaintenance(t *testing.T) {
	addr := startTalosLike(t, selfSignedCert(t, constants.MaintenanceServiceCommonName), tls.NoClientCert)
	n := NewMachineryNode(addr)
	got, err := n.Probe(context.Background(), nil)
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	if got != TalosMaintenance {
		t.Fatalf("Probe() = %q, want maintenance", got)
	}
}

func TestMachineryNode_ProbeConfiguredWithoutTalosconfigIsForeign(t *testing.T) {
	addr := startTalosLike(t, selfSignedCert(t, "apid"), tls.RequireAnyClientCert)
	n := NewMachineryNode(addr)
	got, err := n.Probe(context.Background(), nil)
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	if got != TalosForeign {
		t.Fatalf("Probe() = %q, want foreign", got)
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/capi/talos/ -run TestMachineryNode -v`
Expected: FAIL to compile with `undefined: NewMachineryNode`.

- [ ] **Step 4: Write node.go**

`internal/capi/talos/node.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	"github.com/siderolabs/talos/pkg/machinery/client"
	clientconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
	"github.com/siderolabs/talos/pkg/machinery/constants"
)

// Node is the Talos API surface the reconciler needs.
type Node interface {
	// Probe classifies what answers on the Talos API port. tc may be nil
	// before secrets exist; a configured node is then reported Foreign.
	Probe(ctx context.Context, tc *clientconfig.Config) (TalosState, error)
	// ApplyConfiguration applies a machine config to a node in maintenance
	// mode with reboot mode.
	ApplyConfiguration(ctx context.Context, cfg []byte) error
	// Bootstrap bootstraps etcd on a configured node.
	Bootstrap(ctx context.Context, tc *clientconfig.Config) error
	// EtcdState reports whether etcd has members on a configured node.
	EtcdState(ctx context.Context, tc *clientconfig.Config) (EtcdState, error)
	// Kubeconfig fetches the admin kubeconfig from a configured node.
	Kubeconfig(ctx context.Context, tc *clientconfig.Config) ([]byte, error)
	// Reset wipes the node's system disk without leaving etcd and halts it.
	Reset(ctx context.Context, tc *clientconfig.Config) error
}

// MachineryNode implements Node with the Talos machinery client.
type MachineryNode struct {
	// endpoint is "ip" or "ip:port"; the client appends port 50000 when missing.
	endpoint    string
	dialTimeout time.Duration
}

// NewMachineryNode returns a Node for the Talos API at ip (port 50000).
func NewMachineryNode(ip string) *MachineryNode {
	return &MachineryNode{endpoint: ip, dialTimeout: 10 * time.Second}
}

// insecureClient dials without verifying the server certificate and records
// the server certificate's common name into cn.
func (n *MachineryNode) insecureClient(ctx context.Context, cn *string) (*client.Client, error) {
	tlsCfg := &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // maintenance mode has no CA; identity is checked via CN
		MinVersion:         tls.VersionTLS12,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) > 0 {
				*cn = cs.PeerCertificates[0].Subject.CommonName
			}
			return nil
		},
	}
	return client.New(ctx, client.WithEndpoints(n.endpoint), client.WithTLSConfig(tlsCfg))
}

// authClient dials with this run's talosconfig, overriding endpoints to the node.
func (n *MachineryNode) authClient(ctx context.Context, tc *clientconfig.Config) (*client.Client, error) {
	return client.New(ctx, client.WithConfig(tc), client.WithEndpoints(n.endpoint))
}

// Probe implements Node.
func (n *MachineryNode) Probe(ctx context.Context, tc *clientconfig.Config) (TalosState, error) {
	ctx, cancel := context.WithTimeout(ctx, n.dialTimeout)
	defer cancel()

	var serverCN string
	c, err := n.insecureClient(ctx, &serverCN)
	if err != nil {
		return TalosUnreachable, nil
	}
	_, verr := c.Version(ctx)
	_ = c.Close()

	// No certificate seen means no TLS handshake completed: nothing is listening.
	if serverCN == "" {
		return TalosUnreachable, nil
	}
	if verr == nil && serverCN == constants.MaintenanceServiceCommonName {
		return TalosMaintenance, nil
	}
	if serverCN == constants.MaintenanceServiceCommonName {
		// Maintenance service answered TLS but the RPC failed; try again later.
		return TalosUnreachable, nil
	}

	// A configured node presented a certificate. Is it ours?
	if tc == nil {
		return TalosForeign, nil
	}
	ac, err := n.authClient(ctx, tc)
	if err != nil {
		return TalosForeign, nil
	}
	defer func() { _ = ac.Close() }()
	if _, err := ac.Version(ctx); err != nil {
		return TalosForeign, nil
	}
	return TalosOurs, nil
}

// ApplyConfiguration implements Node.
func (n *MachineryNode) ApplyConfiguration(ctx context.Context, cfg []byte) error {
	var cn string
	c, err := n.insecureClient(ctx, &cn)
	if err != nil {
		return fmt.Errorf("dialing maintenance service: %w", err)
	}
	defer func() { _ = c.Close() }()

	_, err = c.ApplyConfiguration(ctx, &machineapi.ApplyConfigurationRequest{
		Data: cfg,
		Mode: machineapi.ApplyConfigurationRequest_REBOOT,
	})
	if err != nil {
		return fmt.Errorf("applying configuration: %w", err)
	}
	return nil
}

// Bootstrap implements Node.
func (n *MachineryNode) Bootstrap(ctx context.Context, tc *clientconfig.Config) error {
	c, err := n.authClient(ctx, tc)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	return c.Bootstrap(ctx, &machineapi.BootstrapRequest{})
}

// EtcdState implements Node. Any error listing members is reported as not
// bootstrapped, because etcd is not serving.
func (n *MachineryNode) EtcdState(ctx context.Context, tc *clientconfig.Config) (EtcdState, error) {
	c, err := n.authClient(ctx, tc)
	if err != nil {
		return EtcdUnknown, err
	}
	defer func() { _ = c.Close() }()

	resp, err := c.EtcdMemberList(ctx, &machineapi.EtcdMemberListRequest{})
	if err != nil {
		return EtcdNotBootstrapped, nil
	}
	for _, msg := range resp.GetMessages() {
		if len(msg.GetMembers()) > 0 {
			return EtcdBootstrapped, nil
		}
	}
	return EtcdNotBootstrapped, nil
}

// Kubeconfig implements Node.
func (n *MachineryNode) Kubeconfig(ctx context.Context, tc *clientconfig.Config) ([]byte, error) {
	c, err := n.authClient(ctx, tc)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	return c.Kubeconfig(ctx)
}

// Reset implements Node.
func (n *MachineryNode) Reset(ctx context.Context, tc *clientconfig.Config) error {
	c, err := n.authClient(ctx, tc)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	return c.ResetGeneric(ctx, &machineapi.ResetRequest{Graceful: false, Reboot: false})
}
```

- [ ] **Step 5: Tidy, run the tests, build**

Run: `go mod tidy && go test ./internal/capi/talos/ -run TestMachineryNode -v && go build ./...`
Expected: PASS for all three probe tests; build ok. If `TestMachineryNode_ProbeConfiguredWithoutTalosconfigIsForeign` reports `unreachable`, the handshake aborted before `VerifyConnection`; in that case change the server's `ClientAuth` in the test to `tls.RequireAndVerifyClientCert` with an empty `ClientCAs` pool and re-run. The classification logic in `Probe` stays as written.

- [ ] **Step 6: Lint and commit**

Run: `golangci-lint run ./internal/capi/talos/`

```bash
git add go.mod go.sum internal/capi/talos/node.go internal/capi/talos/node_test.go
git commit -m "Add Talos machinery Node implementation with maintenance-mode detection"
```

---

### Task 5: Kube interface and client-go implementation

**Files:**
- Create: `internal/capi/talos/kube.go`
- Test: `internal/capi/talos/kube_test.go`

**Interfaces:**
- Produces:
  ```go
  type Kube interface {
      APIReachable(ctx context.Context) (bool, error)
      NodeReady(ctx context.Context) (bool, error)
  }
  type KubeFactory func(kubeconfigPath string) (Kube, error)
  func NewClientGoKube(kubeconfigPath string) (Kube, error)
  ```

- [ ] **Step 1: Write the failing test**

`internal/capi/talos/kube_test.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestClientGoKube_NodeReady(t *testing.T) {
	notReady := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "a"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}}}
	ready := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "b"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}

	k := &ClientGoKube{cs: fake.NewClientset(notReady)}
	got, err := k.NodeReady(context.Background())
	if err != nil || got {
		t.Fatalf("NodeReady() = %v, %v; want false, nil", got, err)
	}

	k = &ClientGoKube{cs: fake.NewClientset(notReady, ready)}
	got, err = k.NodeReady(context.Background())
	if err != nil || !got {
		t.Fatalf("NodeReady() = %v, %v; want true, nil", got, err)
	}
}

func TestClientGoKube_APIReachableWithFake(t *testing.T) {
	k := &ClientGoKube{cs: fake.NewClientset()}
	got, err := k.APIReachable(context.Background())
	if err != nil || !got {
		t.Fatalf("APIReachable() = %v, %v; want true, nil", got, err)
	}
}

func TestNewClientGoKube_BadPath(t *testing.T) {
	if _, err := NewClientGoKube("/definitely/missing/kubeconfig"); err == nil {
		t.Fatal("expected error for missing kubeconfig")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/capi/talos/ -run 'TestClientGoKube|TestNewClientGoKube' -v`
Expected: FAIL to compile with `undefined: ClientGoKube`.

- [ ] **Step 3: Write kube.go**

`internal/capi/talos/kube.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// Kube probes the bootstrap cluster's Kubernetes API.
type Kube interface {
	// APIReachable reports whether the API server answers a version request.
	APIReachable(ctx context.Context) (bool, error)
	// NodeReady reports whether at least one node has Ready=True.
	NodeReady(ctx context.Context) (bool, error)
}

// KubeFactory builds a Kube from a kubeconfig path once one exists.
type KubeFactory func(kubeconfigPath string) (Kube, error)

// ClientGoKube implements Kube with client-go.
type ClientGoKube struct {
	cs kubernetes.Interface
}

// NewClientGoKube builds a Kube from a kubeconfig file.
func NewClientGoKube(kubeconfigPath string) (Kube, error) {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return nil, err
	}
	cfg.Timeout = 10 * time.Second
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &ClientGoKube{cs: cs}, nil
}

// APIReachable implements Kube. Transport errors are reported as "not
// reachable", never as errors, so callers can keep polling.
func (k *ClientGoKube) APIReachable(context.Context) (bool, error) {
	_, err := k.cs.Discovery().ServerVersion()
	return err == nil, nil
}

// NodeReady implements Kube.
func (k *ClientGoKube) NodeReady(ctx context.Context) (bool, error) {
	nodes, err := k.cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, nil
	}
	for _, n := range nodes.Items {
		for _, c := range n.Status.Conditions {
			if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
				return true, nil
			}
		}
	}
	return false, nil
}
```

- [ ] **Step 4: Run the tests and lint**

Run: `go mod tidy && go test ./internal/capi/talos/ -run 'TestClientGoKube|TestNewClientGoKube' -v && golangci-lint run ./internal/capi/talos/`
Expected: PASS; no findings. (`k8s.io/api` moves from indirect to direct in go.mod; that is expected.)

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/capi/talos/kube.go internal/capi/talos/kube_test.go
git commit -m "Add client-go Kube probe for Talos bootstrapper"
```

---
### Task 6: Image Factory resolver

**Files:**
- Create: `internal/capi/talos/image.go`
- Test: `internal/capi/talos/image_test.go`

**Interfaces:**
- Produces:
  ```go
  type ImageSpec struct {
      Factory, Schematic string
      Extensions, KernelArgs []string
      ISO, Installer string
      Version, Architecture string
  }
  type ImageURLs struct{ ISO, UKI, Installer string }
  type ImageResolver interface { Resolve(ctx context.Context, spec ImageSpec) (ImageURLs, error) }
  func NewFactoryResolver(httpClient *http.Client) *FactoryResolver
  const DefaultFactoryURL = "https://factory.talos.dev"
  ```

- [ ] **Step 1: Write the failing test**

`internal/capi/talos/image_test.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFactoryResolver_ExplicitOverrides(t *testing.T) {
	r := NewFactoryResolver(nil)
	got, err := r.Resolve(context.Background(), ImageSpec{
		ISO: "https://mirror.example/talos.iso", Installer: "mirror.example/installer:v1", Version: "v1.13.6", Architecture: "amd64",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ISO != "https://mirror.example/talos.iso" || got.Installer != "mirror.example/installer:v1" || got.UKI != "" {
		t.Fatalf("Resolve() = %+v", got)
	}
}

func TestFactoryResolver_PrecomputedSchematic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	got, err := NewFactoryResolver(srv.Client()).Resolve(context.Background(), ImageSpec{
		Factory: srv.URL, Schematic: "abc123", Version: "v1.13.6", Architecture: "arm64",
	})
	if err != nil {
		t.Fatal(err)
	}
	host := srv.Listener.Addr().String()
	want := ImageURLs{
		ISO:       srv.URL + "/image/abc123/v1.13.6/metal-arm64.iso",
		UKI:       srv.URL + "/image/abc123/v1.13.6/metal-arm64-uki.efi",
		Installer: host + "/metal-installer/abc123:v1.13.6",
	}
	if got != want {
		t.Fatalf("Resolve() = %+v, want %+v", got, want)
	}
}

func TestFactoryResolver_CreatesSchematic(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/schematics" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("bad body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"deadbeef"}`))
	}))
	defer srv.Close()

	got, err := NewFactoryResolver(srv.Client()).Resolve(context.Background(), ImageSpec{
		Factory: srv.URL, Extensions: []string{"siderolabs/iscsi-tools"}, KernelArgs: []string{"net.ifnames=0"}, Version: "v1.13.6", Architecture: "amd64",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ISO != srv.URL+"/image/deadbeef/v1.13.6/metal-amd64.iso" {
		t.Fatalf("ISO = %q", got.ISO)
	}
	cust := body["customization"].(map[string]any)
	ext := cust["systemExtensions"].(map[string]any)["officialExtensions"].([]any)
	if len(ext) != 1 || ext[0] != "siderolabs/iscsi-tools" {
		t.Fatalf("officialExtensions = %v", ext)
	}
	args := cust["extraKernelArgs"].([]any)
	if len(args) != 1 || args[0] != "net.ifnames=0" {
		t.Fatalf("extraKernelArgs = %v", args)
	}
}

func TestFactoryResolver_SchematicHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadRequest)
	}))
	defer srv.Close()
	_, err := NewFactoryResolver(srv.Client()).Resolve(context.Background(), ImageSpec{Factory: srv.URL, Version: "v1.13.6", Architecture: "amd64"})
	if err == nil {
		t.Fatal("expected error from 400 response")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/capi/talos/ -run TestFactoryResolver -v`
Expected: FAIL to compile with `undefined: NewFactoryResolver`.

- [ ] **Step 3: Write image.go**

`internal/capi/talos/image.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// DefaultFactoryURL is the public Talos Image Factory.
const DefaultFactoryURL = "https://factory.talos.dev"

// ImageSpec describes which Talos images to boot and install.
type ImageSpec struct {
	// Factory is the Image Factory base URL. Empty means DefaultFactoryURL.
	Factory string
	// Schematic is a precomputed schematic id. Empty means create one.
	Schematic string
	// Extensions are official system extension names for a new schematic.
	Extensions []string
	// KernelArgs are extra kernel args for a new schematic.
	KernelArgs []string
	// ISO and Installer override everything else when both are set.
	ISO       string
	Installer string
	// Version is the Talos version, v-prefixed.
	Version string
	// Architecture is amd64 or arm64.
	Architecture string
}

// ImageURLs are the resolved artifacts. UKI is empty when explicit overrides
// were given, which disables UEFI HTTP boot.
type ImageURLs struct {
	ISO       string
	UKI       string
	Installer string
}

// ImageResolver turns an ImageSpec into concrete URLs.
type ImageResolver interface {
	Resolve(ctx context.Context, spec ImageSpec) (ImageURLs, error)
}

// FactoryResolver resolves images against a Talos Image Factory.
type FactoryResolver struct {
	http *http.Client
}

// NewFactoryResolver returns a resolver using httpClient (nil means http.DefaultClient).
func NewFactoryResolver(httpClient *http.Client) *FactoryResolver {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &FactoryResolver{http: httpClient}
}

type schematicRequest struct {
	Customization struct {
		SystemExtensions struct {
			OfficialExtensions []string `json:"officialExtensions,omitempty"`
		} `json:"systemExtensions"`
		ExtraKernelArgs []string `json:"extraKernelArgs,omitempty"`
	} `json:"customization"`
}

type schematicResponse struct {
	ID string `json:"id"`
}

// Resolve implements ImageResolver.
func (r *FactoryResolver) Resolve(ctx context.Context, spec ImageSpec) (ImageURLs, error) {
	if spec.ISO != "" && spec.Installer != "" {
		return ImageURLs{ISO: spec.ISO, Installer: spec.Installer}, nil
	}

	factory := strings.TrimRight(spec.Factory, "/")
	if factory == "" {
		factory = DefaultFactoryURL
	}
	u, err := url.Parse(factory)
	if err != nil || u.Host == "" {
		return ImageURLs{}, fmt.Errorf("invalid image factory URL %q", factory)
	}

	id := spec.Schematic
	if id == "" {
		id, err = r.createSchematic(ctx, factory, spec.Extensions, spec.KernelArgs)
		if err != nil {
			return ImageURLs{}, err
		}
	}

	arch := spec.Architecture
	if arch == "" {
		arch = "amd64"
	}
	return ImageURLs{
		ISO:       fmt.Sprintf("%s/image/%s/%s/metal-%s.iso", factory, id, spec.Version, arch),
		UKI:       fmt.Sprintf("%s/image/%s/%s/metal-%s-uki.efi", factory, id, spec.Version, arch),
		Installer: fmt.Sprintf("%s/metal-installer/%s:%s", u.Host, id, spec.Version),
	}, nil
}

func (r *FactoryResolver) createSchematic(ctx context.Context, factory string, extensions, kernelArgs []string) (string, error) {
	var req schematicRequest
	req.Customization.SystemExtensions.OfficialExtensions = extensions
	req.Customization.ExtraKernelArgs = kernelArgs
	payload, err := json.Marshal(req)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, factory+"/schematics", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := r.http.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("creating image factory schematic: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("creating image factory schematic: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out schematicResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decoding schematic response: %w", err)
	}
	if out.ID == "" {
		return "", fmt.Errorf("image factory returned an empty schematic id")
	}
	return out.ID, nil
}
```

- [ ] **Step 4: Run the tests and lint**

Run: `go test ./internal/capi/talos/ -run TestFactoryResolver -v && golangci-lint run ./internal/capi/talos/`
Expected: PASS; no findings.

- [ ] **Step 5: Commit**

```bash
git add internal/capi/talos/image.go internal/capi/talos/image_test.go
git commit -m "Add Talos Image Factory resolver"
```

---

### Task 7: Machine configuration generation

**Files:**
- Create: `internal/capi/talos/config.go`
- Test: `internal/capi/talos/config_test.go`

**Interfaces:**
- Produces:
  ```go
  type ConfigInput struct {
      ClusterName, Endpoint, KubernetesVersion, TalosVersion, NodeIP, InstallDisk, InstallerImage string
      Patches []string
  }
  type GeneratedConfig struct { MachineConfig []byte; Talosconfig *clientconfig.Config }
  func NewSecretsBundle(talosVersion string) (*secrets.Bundle, error)
  func GenerateConfig(in ConfigInput, bundle *secrets.Bundle) (*GeneratedConfig, error)
  ```
  `secrets` is `github.com/siderolabs/talos/pkg/machinery/config/generate/secrets`.

- [ ] **Step 1: Write the failing test**

`internal/capi/talos/config_test.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"slices"
	"testing"

	"github.com/siderolabs/talos/pkg/machinery/config/configloader"
)

func testConfigInput() ConfigInput {
	return ConfigInput{
		ClusterName:       "bootstrap",
		Endpoint:          "https://10.0.0.5:6443",
		KubernetesVersion: "v1.34.0",
		TalosVersion:      "v1.13.6",
		NodeIP:            "10.0.0.5",
		InstallDisk:       "/dev/sda",
		InstallerImage:    "factory.talos.dev/metal-installer/abc:v1.13.6",
	}
}

func TestGenerateConfig_SetsInstallAndScheduling(t *testing.T) {
	bundle, err := NewSecretsBundle("v1.13.6")
	if err != nil {
		t.Fatal(err)
	}
	gen, err := GenerateConfig(testConfigInput(), bundle)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := configloader.NewFromBytes(gen.MachineConfig)
	if err != nil {
		t.Fatalf("generated config does not load: %v", err)
	}
	if got := cfg.Machine().Install().Disk(); got != "/dev/sda" {
		t.Errorf("install disk = %q", got)
	}
	if got := cfg.Machine().Install().Image(); got != "factory.talos.dev/metal-installer/abc:v1.13.6" {
		t.Errorf("install image = %q", got)
	}
	if !cfg.Machine().Install().Zero() {
		t.Error("install wipe should be true")
	}
	if !cfg.Cluster().ScheduleOnControlPlanes() {
		t.Error("allowSchedulingOnControlPlanes should be true")
	}
	if got := cfg.Cluster().Endpoint().String(); got != "https://10.0.0.5:6443" {
		t.Errorf("endpoint = %q", got)
	}
	if !cfg.Machine().Type().IsControlPlane() {
		t.Error("machine type should be controlplane")
	}

	ctx := gen.Talosconfig.Contexts[gen.Talosconfig.Context]
	if !slices.Equal(ctx.Endpoints, []string{"10.0.0.5"}) {
		t.Errorf("talosconfig endpoints = %v", ctx.Endpoints)
	}
}

func TestGenerateConfig_AppliesUserPatchesInOrder(t *testing.T) {
	bundle, err := NewSecretsBundle("v1.13.6")
	if err != nil {
		t.Fatal(err)
	}
	in := testConfigInput()
	in.Patches = []string{
		"machine:\n  install:\n    extraKernelArgs: [first]\n",
		"machine:\n  install:\n    extraKernelArgs: [first, second]\n",
	}
	gen, err := GenerateConfig(in, bundle)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := configloader.NewFromBytes(gen.MachineConfig)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Machine().Install().ExtraKernelArgs(); !slices.Equal(got, []string{"first", "second"}) {
		t.Errorf("extraKernelArgs = %v, want [first second]", got)
	}
}

func TestGenerateConfig_DefaultsKubernetesVersion(t *testing.T) {
	bundle, err := NewSecretsBundle("v1.13.6")
	if err != nil {
		t.Fatal(err)
	}
	in := testConfigInput()
	in.KubernetesVersion = ""
	if _, err := GenerateConfig(in, bundle); err != nil {
		t.Fatalf("GenerateConfig() with empty kubernetes version: %v", err)
	}
}

func TestGenerateConfig_InvalidPatch(t *testing.T) {
	bundle, err := NewSecretsBundle("v1.13.6")
	if err != nil {
		t.Fatal(err)
	}
	in := testConfigInput()
	in.Patches = []string{"machine: [not, a, map"}
	if _, err := GenerateConfig(in, bundle); err == nil {
		t.Fatal("expected error for malformed patch")
	}
}

func TestNewSecretsBundle_BadVersion(t *testing.T) {
	if _, err := NewSecretsBundle("not-a-version"); err == nil {
		t.Fatal("expected error for bad Talos version")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/capi/talos/ -run 'TestGenerateConfig|TestNewSecretsBundle' -v`
Expected: FAIL to compile with `undefined: NewSecretsBundle`.

- [ ] **Step 3: Write config.go**

`internal/capi/talos/config.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"fmt"
	"strings"
	"time"

	clientconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
	"github.com/siderolabs/talos/pkg/machinery/config"
	"github.com/siderolabs/talos/pkg/machinery/config/configpatcher"
	"github.com/siderolabs/talos/pkg/machinery/config/generate"
	"github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
	"github.com/siderolabs/talos/pkg/machinery/config/machine"
	"github.com/siderolabs/talos/pkg/machinery/constants"
)

// ConfigInput is what the bootstrapper needs to render a controlplane config.
type ConfigInput struct {
	ClusterName       string
	Endpoint          string
	KubernetesVersion string
	TalosVersion      string
	NodeIP            string
	InstallDisk       string
	InstallerImage    string
	// Patches are strategic-merge or RFC6902 patches applied in order after
	// the built-in wipe patch.
	Patches []string
}

// GeneratedConfig is a rendered machine config and the matching talosconfig.
type GeneratedConfig struct {
	MachineConfig []byte
	Talosconfig   *clientconfig.Config
}

// wipePatch forces a clean install; the node is disposable.
const wipePatch = "machine:\n  install:\n    wipe: true\n"

// NewSecretsBundle creates fresh cluster secrets for the given Talos version.
func NewSecretsBundle(talosVersion string) (*secrets.Bundle, error) {
	contract, err := config.ParseContractFromVersion(talosVersion)
	if err != nil {
		return nil, fmt.Errorf("parsing talos version %q: %w", talosVersion, err)
	}
	return secrets.NewBundle(secrets.NewFixedClock(time.Now()), contract)
}

// GenerateConfig renders a single-node controlplane machine config.
func GenerateConfig(in ConfigInput, bundle *secrets.Bundle) (*GeneratedConfig, error) {
	contract, err := config.ParseContractFromVersion(in.TalosVersion)
	if err != nil {
		return nil, fmt.Errorf("parsing talos version %q: %w", in.TalosVersion, err)
	}

	k8sVersion := strings.TrimPrefix(in.KubernetesVersion, "v")
	if k8sVersion == "" {
		k8sVersion = constants.DefaultKubernetesVersion
	}

	input, err := generate.NewInput(in.ClusterName, in.Endpoint, k8sVersion,
		generate.WithSecretsBundle(bundle),
		generate.WithVersionContract(contract),
		generate.WithInstallDisk(in.InstallDisk),
		generate.WithInstallImage(in.InstallerImage),
		generate.WithAllowSchedulingOnControlPlanes(true),
		generate.WithEndpointList([]string{in.NodeIP}),
	)
	if err != nil {
		return nil, fmt.Errorf("building config input: %w", err)
	}

	provider, err := input.Config(machine.TypeControlPlane)
	if err != nil {
		return nil, fmt.Errorf("generating controlplane config: %w", err)
	}
	raw, err := provider.Bytes()
	if err != nil {
		return nil, fmt.Errorf("encoding config: %w", err)
	}

	patches, err := configpatcher.LoadPatches(append([]string{wipePatch}, in.Patches...))
	if err != nil {
		return nil, fmt.Errorf("loading config patches: %w", err)
	}
	out, err := configpatcher.Apply(configpatcher.WithBytes(raw), patches)
	if err != nil {
		return nil, fmt.Errorf("applying config patches: %w", err)
	}
	patched, err := out.Bytes()
	if err != nil {
		return nil, fmt.Errorf("encoding patched config: %w", err)
	}

	tc, err := input.Talosconfig()
	if err != nil {
		return nil, fmt.Errorf("generating talosconfig: %w", err)
	}

	return &GeneratedConfig{MachineConfig: patched, Talosconfig: tc}, nil
}
```

- [ ] **Step 4: Run the tests, tidy, lint**

Run: `go mod tidy && go test ./internal/capi/talos/ -run 'TestGenerateConfig|TestNewSecretsBundle' -v && golangci-lint run ./internal/capi/talos/`
Expected: PASS. If `Zero()` is false, the wipe patch key differs for this Talos version: inspect `gen.MachineConfig` for the install section and adjust `wipePatch` to the field name shown in the generated YAML (`wipe`).

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/capi/talos/config.go internal/capi/talos/config_test.go
git commit -m "Add Talos machine config generation for bootstrap node"
```

---

### Task 8: Addon installer (Helm SDK + manifests)

**Files:**
- Create: `internal/capi/talos/addons.go`
- Test: `internal/capi/talos/addons_test.go`
- Modify: `go.mod`, `go.sum` (helm)

**Interfaces:**
- Consumes: `capi.Applier` (`Apply(ctx, *capi.Cluster, []byte) error`), `capi.NewDynamicApplier()`.
- Produces:
  ```go
  type HelmRelease struct { Name, Namespace, Chart, Repository, Version, Values string; Timeout time.Duration }
  type Addons struct { Helm []HelmRelease; Manifests []string }
  func (a Addons) Empty() bool
  type HelmClient interface {
      Exists(ctx context.Context, kubeconfigPath string, rel HelmRelease) (bool, error)
      Install(ctx context.Context, kubeconfigPath string, rel HelmRelease) error
      Upgrade(ctx context.Context, kubeconfigPath string, rel HelmRelease) error
  }
  type AddonInstaller interface { Install(ctx context.Context, kubeconfigPath string, addons Addons) error }
  func NewAddonInstaller(helm HelmClient, applier capi.Applier, logger *log.Logger) *DefaultAddonInstaller
  func NewSDKHelmClient(logger *log.Logger) *SDKHelmClient
  ```

- [ ] **Step 1: Add the Helm dependency**

```bash
go get helm.sh/helm/v3@v3.21.4
```

- [ ] **Step 2: Write the failing test**

`internal/capi/talos/addons_test.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tinkerbell-community/terraform-provider-capi/internal/capi"
)

type fakeHelm struct {
	existing map[string]bool
	calls    []string
	failOn   string
}

func (f *fakeHelm) key(rel HelmRelease) string { return rel.Namespace + "/" + rel.Name }

func (f *fakeHelm) Exists(_ context.Context, _ string, rel HelmRelease) (bool, error) {
	return f.existing[f.key(rel)], nil
}

func (f *fakeHelm) Install(_ context.Context, _ string, rel HelmRelease) error {
	f.calls = append(f.calls, "install:"+f.key(rel))
	if f.failOn == rel.Name {
		return errors.New("chart pull failed")
	}
	return nil
}

func (f *fakeHelm) Upgrade(_ context.Context, _ string, rel HelmRelease) error {
	f.calls = append(f.calls, "upgrade:"+f.key(rel))
	return nil
}

type fakeApplier struct {
	manifests []string
	err       error
}

func (f *fakeApplier) Apply(_ context.Context, _ *capi.Cluster, manifest []byte) error {
	f.manifests = append(f.manifests, string(manifest))
	return f.err
}

func (f *fakeApplier) Delete(context.Context, *capi.Cluster, string, string) error { return nil }

func TestAddonInstaller_OrderAndInstallVsUpgrade(t *testing.T) {
	helm := &fakeHelm{existing: map[string]bool{"kube-system/cilium": true}}
	applier := &fakeApplier{}
	inst := NewAddonInstaller(helm, applier, nil)

	err := inst.Install(context.Background(), "/tmp/kc", Addons{
		Helm: []HelmRelease{
			{Name: "cilium", Namespace: "kube-system", Chart: "oci://quay.io/cilium/charts/cilium"},
			{Name: "metrics", Namespace: "monitoring", Chart: "metrics-server", Repository: "https://charts.example"},
		},
		Manifests: []string{"apiVersion: v1\nkind: Namespace\nmetadata:\n  name: a\n", "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: b\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(helm.calls, ","); got != "upgrade:kube-system/cilium,install:monitoring/metrics" {
		t.Fatalf("helm calls = %q", got)
	}
	if len(applier.manifests) != 2 || !strings.Contains(applier.manifests[1], "name: b") {
		t.Fatalf("manifests = %v", applier.manifests)
	}
}

func TestAddonInstaller_HelmFailureWrapsErrAddons(t *testing.T) {
	helm := &fakeHelm{existing: map[string]bool{}, failOn: "cilium"}
	inst := NewAddonInstaller(helm, &fakeApplier{}, nil)
	err := inst.Install(context.Background(), "/tmp/kc", Addons{Helm: []HelmRelease{{Name: "cilium", Namespace: "kube-system", Chart: "x"}}})
	if !errors.Is(err, ErrAddons) {
		t.Fatalf("error = %v, want ErrAddons", err)
	}
	if !strings.Contains(err.Error(), "cilium") {
		t.Fatalf("error %q should name the release", err)
	}
}

func TestAddonInstaller_ManifestFailureWrapsErrAddons(t *testing.T) {
	inst := NewAddonInstaller(&fakeHelm{existing: map[string]bool{}}, &fakeApplier{err: errors.New("apply failed")}, nil)
	err := inst.Install(context.Background(), "/tmp/kc", Addons{Manifests: []string{"kind: X"}})
	if !errors.Is(err, ErrAddons) {
		t.Fatalf("error = %v, want ErrAddons", err)
	}
}

func TestAddons_Empty(t *testing.T) {
	if !(Addons{}).Empty() {
		t.Fatal("zero Addons should be empty")
	}
	if (Addons{Manifests: []string{"x"}}).Empty() {
		t.Fatal("Addons with manifests should not be empty")
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/capi/talos/ -run 'TestAddonInstaller|TestAddons_Empty' -v`
Expected: FAIL to compile with `undefined: HelmRelease`.

- [ ] **Step 4: Write addons.go**

`internal/capi/talos/addons.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/kube"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/storage/driver"

	"github.com/tinkerbell-community/terraform-provider-capi/internal/capi"
)

// HelmRelease describes one chart to install on the bootstrap cluster.
type HelmRelease struct {
	Name      string
	Namespace string
	// Chart is an oci:// reference or a chart name used with Repository.
	Chart      string
	Repository string
	Version    string
	// Values is a YAML document of chart values.
	Values  string
	Timeout time.Duration
}

// Addons is everything to install after the cluster comes up, in order:
// Helm releases first, then raw manifests.
type Addons struct {
	Helm      []HelmRelease
	Manifests []string
}

// Empty reports whether nothing is configured.
func (a Addons) Empty() bool { return len(a.Helm) == 0 && len(a.Manifests) == 0 }

// HelmClient abstracts the Helm SDK for tests.
type HelmClient interface {
	Exists(ctx context.Context, kubeconfigPath string, rel HelmRelease) (bool, error)
	Install(ctx context.Context, kubeconfigPath string, rel HelmRelease) error
	Upgrade(ctx context.Context, kubeconfigPath string, rel HelmRelease) error
}

// AddonInstaller installs addons idempotently.
type AddonInstaller interface {
	Install(ctx context.Context, kubeconfigPath string, addons Addons) error
}

// DefaultAddonInstaller installs Helm releases through a HelmClient and raw
// manifests through a capi.Applier.
type DefaultAddonInstaller struct {
	helm    HelmClient
	applier capi.Applier
	logger  *log.Logger
}

// NewAddonInstaller wires a HelmClient and an Applier.
func NewAddonInstaller(helm HelmClient, applier capi.Applier, logger *log.Logger) *DefaultAddonInstaller {
	if logger == nil {
		logger = log.New(log.Writer(), "[talos-addons] ", log.LstdFlags)
	}
	return &DefaultAddonInstaller{helm: helm, applier: applier, logger: logger}
}

// Install implements AddonInstaller. A release that already exists is
// upgraded so retries converge instead of failing on "already exists".
func (d *DefaultAddonInstaller) Install(ctx context.Context, kubeconfigPath string, addons Addons) error {
	for _, rel := range addons.Helm {
		exists, err := d.helm.Exists(ctx, kubeconfigPath, rel)
		if err != nil {
			return fmt.Errorf("%w: checking release %s/%s: %v", ErrAddons, rel.Namespace, rel.Name, err)
		}
		if exists {
			d.logger.Printf("upgrading helm release %s/%s", rel.Namespace, rel.Name)
			err = d.helm.Upgrade(ctx, kubeconfigPath, rel)
		} else {
			d.logger.Printf("installing helm release %s/%s", rel.Namespace, rel.Name)
			err = d.helm.Install(ctx, kubeconfigPath, rel)
		}
		if err != nil {
			return fmt.Errorf("%w: helm release %s/%s: %v", ErrAddons, rel.Namespace, rel.Name, err)
		}
	}

	cluster := &capi.Cluster{Name: "bootstrap", KubeconfigPath: kubeconfigPath}
	for i, m := range addons.Manifests {
		d.logger.Printf("applying manifest %d/%d", i+1, len(addons.Manifests))
		if err := d.applier.Apply(ctx, cluster, []byte(m)); err != nil {
			return fmt.Errorf("%w: manifest %d: %v", ErrAddons, i+1, err)
		}
	}
	return nil
}

// SDKHelmClient implements HelmClient with the Helm SDK.
type SDKHelmClient struct {
	logger *log.Logger
}

// NewSDKHelmClient returns a Helm SDK client.
func NewSDKHelmClient(logger *log.Logger) *SDKHelmClient {
	if logger == nil {
		logger = log.New(log.Writer(), "[helm] ", log.LstdFlags)
	}
	return &SDKHelmClient{logger: logger}
}

func (h *SDKHelmClient) configuration(kubeconfigPath, namespace string) (*action.Configuration, error) {
	cfg := new(action.Configuration)
	getter := kube.GetConfig(kubeconfigPath, "", namespace)
	if err := cfg.Init(getter, namespace, "secret", h.logger.Printf); err != nil {
		return nil, fmt.Errorf("initializing helm: %w", err)
	}
	rc, err := registry.NewClient()
	if err != nil {
		return nil, fmt.Errorf("creating helm registry client: %w", err)
	}
	cfg.RegistryClient = rc
	return cfg, nil
}

func (h *SDKHelmClient) loadChart(cpo *action.ChartPathOptions, rel HelmRelease) (*chart.Chart, map[string]any, error) {
	cpo.Version = rel.Version
	if !registry.IsOCI(rel.Chart) {
		cpo.RepoURL = rel.Repository
	}
	path, err := cpo.LocateChart(rel.Chart, cli.New())
	if err != nil {
		return nil, nil, fmt.Errorf("locating chart %q: %w", rel.Chart, err)
	}
	ch, err := loader.Load(path)
	if err != nil {
		return nil, nil, fmt.Errorf("loading chart %q: %w", rel.Chart, err)
	}
	vals, err := chartutil.ReadValues([]byte(rel.Values))
	if err != nil {
		return nil, nil, fmt.Errorf("parsing values for %s: %w", rel.Name, err)
	}
	return ch, vals, nil
}

// Exists implements HelmClient.
func (h *SDKHelmClient) Exists(_ context.Context, kubeconfigPath string, rel HelmRelease) (bool, error) {
	cfg, err := h.configuration(kubeconfigPath, rel.Namespace)
	if err != nil {
		return false, err
	}
	_, err = action.NewGet(cfg).Run(rel.Name)
	if errors.Is(err, driver.ErrReleaseNotFound) {
		return false, nil
	}
	return err == nil, err
}

// Install implements HelmClient.
func (h *SDKHelmClient) Install(ctx context.Context, kubeconfigPath string, rel HelmRelease) error {
	cfg, err := h.configuration(kubeconfigPath, rel.Namespace)
	if err != nil {
		return err
	}
	inst := action.NewInstall(cfg)
	inst.ReleaseName = rel.Name
	inst.Namespace = rel.Namespace
	inst.CreateNamespace = true
	inst.Wait = true
	inst.Timeout = rel.Timeout
	inst.SetRegistryClient(cfg.RegistryClient)

	ch, vals, err := h.loadChart(&inst.ChartPathOptions, rel)
	if err != nil {
		return err
	}
	_, err = inst.RunWithContext(ctx, ch, vals)
	return err
}

// Upgrade implements HelmClient.
func (h *SDKHelmClient) Upgrade(ctx context.Context, kubeconfigPath string, rel HelmRelease) error {
	cfg, err := h.configuration(kubeconfigPath, rel.Namespace)
	if err != nil {
		return err
	}
	up := action.NewUpgrade(cfg)
	up.Namespace = rel.Namespace
	up.Wait = true
	up.Timeout = rel.Timeout
	up.SetRegistryClient(cfg.RegistryClient)

	ch, vals, err := h.loadChart(&up.ChartPathOptions, rel)
	if err != nil {
		return err
	}
	_, err = up.RunWithContext(ctx, rel.Name, ch, vals)
	return err
}
```

- [ ] **Step 5: Tidy, test, build, lint**

Run: `go mod tidy && go test ./internal/capi/talos/ -run 'TestAddonInstaller|TestAddons_Empty' -v && go build ./... && golangci-lint run ./internal/capi/talos/`
Expected: PASS; build ok; no findings. Confirm `go.mod` still pins `k8s.io/client-go v0.36.4` (Helm 3.21.4 must not have bumped it); if it did, run `go get k8s.io/client-go@v0.36.4 k8s.io/api@v0.36.4 k8s.io/apimachinery@v0.36.4 && go mod tidy`.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/capi/talos/addons.go internal/capi/talos/addons_test.go
git commit -m "Add Helm and manifest addon installer for Talos bootstrap cluster"
```

---
### Task 9: Reconciler loop with a simulated node

This is the core. The simulated node lives in `sim_test.go` and implements `BMC`, `Node`, and `Kube` with injectable faults so the real loop runs end to end in milliseconds. `bootstrapper.go` (Task 10) is what constructs the reconciler, so this task also creates the `Config` types and `New` so tests can drive the loop through the public API.

**Files:**
- Create: `internal/capi/talos/reconciler.go`
- Create: `internal/capi/talos/bootstrapper.go`
- Create: `internal/capi/talos/sim_test.go`
- Test: `internal/capi/talos/reconciler_test.go`

**Interfaces:**
- Consumes: everything from Tasks 1–8.
- Produces (public):
  ```go
  type BootMethod string // BootMethodAuto "auto", BootMethodVirtualMedia "virtual_media", BootMethodHTTP "http"
  type MachineConfig struct { Hostname, IP, Disk string; BMC BMCCredentials }
  type BootConfig struct { Method BootMethod; Timeout time.Duration; Attempts int }
  type TalosConfig struct { Version, Architecture, Endpoint string; Image ImageSpec; ConfigPatches []string }
  type Config struct { Machine MachineConfig; Boot BootConfig; Talos TalosConfig; Addons Addons }
  func (c Config) Validate() error
  type Timeouts struct { Boot, Install, PowerDown, Bootstrap, Kubernetes, Addons, Ready, Reset, Poll, RetryBase, RetryMax time.Duration; BMCRetries int }
  func DefaultTimeouts(boot time.Duration) Timeouts
  type Option func(*Bootstrapper)
  func WithBMC(BMC) Option; WithNode(Node) Option; WithKubeFactory(KubeFactory) Option; WithImageResolver(ImageResolver) Option; WithAddonInstaller(AddonInstaller) Option; WithLogger(*log.Logger) Option; WithTimeouts(Timeouts) Option; WithTempDir(string) Option
  func New(cfg Config, opts ...Option) *Bootstrapper
  func (b *Bootstrapper) Create(ctx, opts capi.BootstrapOptions) (*capi.Cluster, error)
  func (b *Bootstrapper) Delete(ctx, cluster *capi.Cluster) error
  func (b *Bootstrapper) Exists(ctx, name string) (bool, error)
  ```

- [ ] **Step 1: Write the simulated node**

`internal/capi/talos/sim_test.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"testing"
	"time"

	clientconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
)

// simFaults are the failure modes a test can inject.
type simFaults struct {
	// powerAlwaysOn makes PowerState report "on" regardless of reality.
	powerAlwaysOn bool
	// ignoreOneTimeBoot makes firmware boot the attached CD this many times
	// after install instead of the disk.
	ignoreOneTimeBoot int
	// insertFailures fails InsertMedia this many times before succeeding.
	insertFailures int
	// virtualMediaUnsupported makes Insert/Eject return ErrUnsupported.
	virtualMediaUnsupported bool
	// httpBootUnsupported makes SetHTTPBootURI return ErrUnsupported.
	httpBootUnsupported bool
	// hangBoots makes this many boots never reach any OS.
	hangBoots int
	// bootstrapErr is returned by the Bootstrap RPC even though etcd starts.
	bootstrapErr error
}

// simMachine is an in-memory bare-metal machine with a BMC, firmware, a
// disk, and a Talos-like API. It implements BMC, Node, and Kube.
type simMachine struct {
	mu     sync.Mutex
	faults simFaults

	power       PowerState
	media       string
	httpURI     string
	override    *BootOverride
	running     string // "off", "hung", "iso", "disk"
	rebootTicks int
	pendingBoot bool

	diskInstalled bool
	installedByUs bool
	etcd          bool
	nodeReady     bool

	calls []string
}

func newSim() *simMachine { return &simMachine{power: PowerOff, running: "off"} }

func (s *simMachine) record(c string) { s.calls = append(s.calls, c) }

func (s *simMachine) count(call string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.calls {
		if c == call {
			n++
		}
	}
	return n
}

// boot powers on and schedules firmware boot selection; caller holds mu.
func (s *simMachine) boot() {
	s.power = PowerOn
	s.rebootTicks = 1
	s.pendingBoot = true
}

// settle performs pending firmware boot selection once the reboot delay has
// elapsed; caller holds mu.
func (s *simMachine) settle() {
	if !s.pendingBoot {
		return
	}
	if s.rebootTicks > 0 {
		s.rebootTicks--
		return
	}
	s.pendingBoot = false
	if s.faults.hangBoots > 0 {
		s.faults.hangBoots--
		s.running = "hung"
		return
	}
	switch {
	case s.override != nil && s.override.Device == BootDeviceCDROM && s.media != "":
		s.running = "iso"
	case s.override != nil && s.override.Device == BootDeviceUEFIHTTP && s.httpURI != "":
		s.running = "iso"
	case s.override != nil && s.override.Device == BootDeviceDisk && s.diskInstalled:
		s.running = "disk"
	case s.faults.ignoreOneTimeBoot > 0 && s.media != "":
		s.faults.ignoreOneTimeBoot--
		s.running = "iso"
	case s.diskInstalled:
		s.running = "disk"
	default:
		s.running = "hung"
	}
	if s.override != nil && !s.override.Persistent {
		s.override = nil
	}
}

// --- BMC ---

func (s *simMachine) PowerState(context.Context) (PowerState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.faults.powerAlwaysOn {
		return PowerOn, nil
	}
	return s.power, nil
}

func (s *simMachine) PowerOn(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("power-on")
	s.boot()
	return nil
}

func (s *simMachine) PowerOff(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("power-off")
	s.power = PowerOff
	s.running = "off"
	s.pendingBoot = false
	return nil
}

func (s *simMachine) PowerCycle(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("power-cycle")
	s.boot()
	return nil
}

func (s *simMachine) SetBootDevice(_ context.Context, dev BootDevice, persistent, efi bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("boot-device:" + string(dev))
	s.override = &BootOverride{Device: dev, Persistent: persistent, EFI: efi}
	return nil
}

func (s *simMachine) InsertMedia(_ context.Context, iso string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("insert")
	if s.faults.virtualMediaUnsupported {
		return fmt.Errorf("insert: %w", ErrUnsupported)
	}
	if s.faults.insertFailures > 0 {
		s.faults.insertFailures--
		return errors.New("insert: transient BMC error")
	}
	s.media = iso
	return nil
}

func (s *simMachine) EjectMedia(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("eject")
	if s.faults.virtualMediaUnsupported {
		return fmt.Errorf("eject: %w", ErrUnsupported)
	}
	s.media = ""
	return nil
}

func (s *simMachine) SetHTTPBootURI(_ context.Context, uri string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("http-uri:" + uri)
	if s.faults.httpBootUnsupported {
		return fmt.Errorf("http boot: %w", ErrUnsupported)
	}
	s.httpURI = uri
	return nil
}

func (s *simMachine) PostCode(context.Context) (string, error) { return "sim", nil }

// --- Node ---

func (s *simMachine) Probe(_ context.Context, tc *clientconfig.Config) (TalosState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settle()
	if s.power != PowerOn || s.pendingBoot {
		return TalosUnreachable, nil
	}
	switch s.running {
	case "iso":
		return TalosMaintenance, nil
	case "disk":
		if tc != nil && s.installedByUs {
			return TalosOurs, nil
		}
		return TalosForeign, nil
	}
	return TalosUnreachable, nil
}

func (s *simMachine) ApplyConfiguration(_ context.Context, cfg []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("apply")
	if s.running != "iso" {
		return errors.New("apply: node not in maintenance mode")
	}
	if len(cfg) == 0 {
		return errors.New("apply: empty config")
	}
	s.diskInstalled = true
	s.installedByUs = true
	s.etcd = false
	s.boot()
	return nil
}

func (s *simMachine) Bootstrap(context.Context, *clientconfig.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("bootstrap")
	if s.running != "disk" || !s.installedByUs {
		return errors.New("bootstrap: node not configured")
	}
	s.etcd = true
	return s.faults.bootstrapErr
}

func (s *simMachine) EtcdState(context.Context, *clientconfig.Config) (EtcdState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.etcd {
		return EtcdBootstrapped, nil
	}
	return EtcdNotBootstrapped, nil
}

const simKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: bootstrap
  cluster:
    server: https://cluster.example:6443
contexts:
- name: admin@bootstrap
  context:
    cluster: bootstrap
    user: admin
current-context: admin@bootstrap
users:
- name: admin
  user:
    token: x
`

func (s *simMachine) Kubeconfig(context.Context, *clientconfig.Config) ([]byte, error) {
	return []byte(simKubeconfig), nil
}

func (s *simMachine) Reset(context.Context, *clientconfig.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("reset")
	if s.running != "disk" {
		return errors.New("reset: node not running from disk")
	}
	s.diskInstalled = false
	s.installedByUs = false
	s.etcd = false
	s.running = "off"
	s.power = PowerOff
	return nil
}

// --- Kube ---

func (s *simMachine) APIReachable(context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running == "disk" && s.etcd, nil
}

func (s *simMachine) NodeReady(context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nodeReady, nil
}

// --- fakes for the other collaborators ---

type fakeResolver struct{}

func (fakeResolver) Resolve(context.Context, ImageSpec) (ImageURLs, error) {
	return ImageURLs{ISO: "https://factory.example/talos.iso", UKI: "https://factory.example/talos-uki.efi", Installer: "factory.example/metal-installer/abc:v1.13.6"}, nil
}

type fakeInstaller struct {
	sim      *simMachine
	calls    int
	failures int
	paths    []string
}

func (f *fakeInstaller) Install(_ context.Context, kubeconfigPath string, addons Addons) error {
	f.calls++
	f.paths = append(f.paths, kubeconfigPath)
	if f.failures > 0 {
		f.failures--
		return fmt.Errorf("%w: simulated", ErrAddons)
	}
	if len(addons.Helm) > 0 {
		f.sim.mu.Lock()
		f.sim.nodeReady = true
		f.sim.mu.Unlock()
	}
	return nil
}

func fastTimeouts() Timeouts {
	return Timeouts{
		Boot: 100 * time.Millisecond, Install: 100 * time.Millisecond, PowerDown: 50 * time.Millisecond,
		Bootstrap: 100 * time.Millisecond, Kubernetes: 100 * time.Millisecond, Addons: time.Second,
		Ready: 100 * time.Millisecond, Reset: 100 * time.Millisecond, Poll: time.Millisecond,
		BMCRetries: 3, RetryBase: time.Millisecond, RetryMax: time.Millisecond,
	}
}

// newTestBootstrapper wires a Bootstrapper to the simulator with fast timeouts.
func newTestBootstrapper(t *testing.T, sim *simMachine, mutate func(*Config)) (*Bootstrapper, *fakeInstaller) {
	t.Helper()
	inst := &fakeInstaller{sim: sim}
	cfg := Config{
		Machine: MachineConfig{Hostname: "cp-1", IP: "10.0.0.5", Disk: "/dev/sda", BMC: BMCCredentials{Address: "10.0.1.5", Username: "u", Password: "p"}},
		Boot:    BootConfig{Method: BootMethodAuto, Attempts: 3},
		Talos:   TalosConfig{Version: "v1.13.6"},
		Addons:  Addons{Helm: []HelmRelease{{Name: "cilium", Namespace: "kube-system", Chart: "oci://quay.io/cilium/charts/cilium"}}},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	b := New(cfg,
		WithBMC(sim),
		WithNode(sim),
		WithKubeFactory(func(string) (Kube, error) { return sim, nil }),
		WithImageResolver(fakeResolver{}),
		WithAddonInstaller(inst),
		WithLogger(log.New(io.Discard, "", 0)),
		WithTempDir(t.TempDir()),
		WithTimeouts(fastTimeouts()),
	)
	return b, inst
}
```

- [ ] **Step 2: Write the failing reconciler tests**

`internal/capi/talos/reconciler_test.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tinkerbell-community/terraform-provider-capi/internal/capi"
)

func create(t *testing.T, b *Bootstrapper) *capi.Cluster {
	t.Helper()
	cluster, err := b.Create(context.Background(), capi.BootstrapOptions{Name: "test-bootstrap", KubernetesVersion: "v1.34.0"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return cluster
}

func TestBootstrapper_HappyPathVirtualMedia(t *testing.T) {
	sim := newSim()
	b, inst := newTestBootstrapper(t, sim, nil)
	cluster := create(t, b)

	if cluster.Name != "test-bootstrap" {
		t.Errorf("cluster name = %q", cluster.Name)
	}
	raw, err := os.ReadFile(cluster.KubeconfigPath)
	if err != nil {
		t.Fatalf("kubeconfig not written: %v", err)
	}
	if !strings.Contains(string(raw), "https://10.0.0.5:6443") {
		t.Errorf("kubeconfig server not rewritten to node IP:\n%s", raw)
	}
	if sim.media != "" {
		t.Error("installer media should be ejected")
	}
	if sim.running != "disk" || !sim.etcd {
		t.Errorf("node should run from disk with etcd, got running=%q etcd=%v", sim.running, sim.etcd)
	}
	if inst.calls != 1 {
		t.Errorf("addon installer calls = %d, want 1", inst.calls)
	}
	if b.last.hist.BootAttempts != 1 {
		t.Errorf("boot attempts = %d, want 1", b.last.hist.BootAttempts)
	}
	if b.last.sess.method != BootMethodVirtualMedia {
		t.Errorf("boot method = %q, want virtual_media", b.last.sess.method)
	}
	if _, err := os.Stat(b.last.sess.talosconfigPath); err != nil {
		t.Errorf("talosconfig not written: %v", err)
	}
}

func TestBootstrapper_FallsBackToHTTPBoot(t *testing.T) {
	sim := newSim()
	sim.faults.virtualMediaUnsupported = true
	b, _ := newTestBootstrapper(t, sim, nil)
	create(t, b)
	if b.last.sess.method != BootMethodHTTP {
		t.Errorf("boot method = %q, want http", b.last.sess.method)
	}
	if sim.httpURI != "" {
		t.Error("HTTP boot URI should be cleared after install")
	}
}

func TestBootstrapper_NoBootMethod(t *testing.T) {
	sim := newSim()
	sim.faults.virtualMediaUnsupported = true
	sim.faults.httpBootUnsupported = true
	b, _ := newTestBootstrapper(t, sim, nil)
	_, err := b.Create(context.Background(), capi.BootstrapOptions{Name: "x"})
	if !errors.Is(err, ErrNoBootMethod) {
		t.Fatalf("error = %v, want ErrNoBootMethod", err)
	}
}

func TestBootstrapper_PinnedMethodUnsupported(t *testing.T) {
	sim := newSim()
	sim.faults.virtualMediaUnsupported = true
	b, _ := newTestBootstrapper(t, sim, func(c *Config) { c.Boot.Method = BootMethodVirtualMedia })
	_, err := b.Create(context.Background(), capi.BootstrapOptions{Name: "x"})
	if !errors.Is(err, ErrNoBootMethod) {
		t.Fatalf("error = %v, want ErrNoBootMethod", err)
	}
}

func TestBootstrapper_BMCAlwaysReportsOn(t *testing.T) {
	sim := newSim()
	sim.faults.powerAlwaysOn = true
	b, _ := newTestBootstrapper(t, sim, nil)
	create(t, b)
	if sim.count("power-cycle") == 0 {
		t.Error("expected a power cycle when the BMC claims the node is already on")
	}
}

func TestBootstrapper_FirmwareBootsInstallerTwice(t *testing.T) {
	sim := newSim()
	sim.faults.ignoreOneTimeBoot = 1
	b, _ := newTestBootstrapper(t, sim, nil)
	create(t, b)
	if b.last.hist.BootAttempts != 2 {
		t.Errorf("boot attempts = %d, want 2 (one install, one reboot-to-disk)", b.last.hist.BootAttempts)
	}
	if sim.count("boot-device:disk") == 0 {
		t.Error("expected a one-time disk boot override")
	}
}

func TestBootstrapper_InsertFailsOnceThenSucceeds(t *testing.T) {
	sim := newSim()
	sim.faults.insertFailures = 1
	b, _ := newTestBootstrapper(t, sim, nil)
	create(t, b)
	if got := sim.count("insert"); got != 2 {
		t.Errorf("insert calls = %d, want 2", got)
	}
	if b.last.hist.BootAttempts != 1 {
		t.Errorf("boot attempts = %d, want 1", b.last.hist.BootAttempts)
	}
}

func TestBootstrapper_HungBootThenSuccess(t *testing.T) {
	sim := newSim()
	sim.faults.hangBoots = 1
	b, _ := newTestBootstrapper(t, sim, nil)
	create(t, b)
	if b.last.hist.BootAttempts != 2 {
		t.Errorf("boot attempts = %d, want 2", b.last.hist.BootAttempts)
	}
}

func TestBootstrapper_AttemptsExhausted(t *testing.T) {
	sim := newSim()
	sim.faults.hangBoots = 10
	b, _ := newTestBootstrapper(t, sim, func(c *Config) { c.Boot.Attempts = 2 })
	_, err := b.Create(context.Background(), capi.BootstrapOptions{Name: "x"})
	if !errors.Is(err, ErrAttemptsExhausted) {
		t.Fatalf("error = %v, want ErrAttemptsExhausted", err)
	}
	for _, want := range []string{"attempt 1", "attempt 2", "last observation"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should contain %q", err, want)
		}
	}
}

func TestBootstrapper_ForeignNodeIsReimaged(t *testing.T) {
	sim := newSim()
	sim.power = PowerOn
	sim.running = "disk"
	sim.diskInstalled = true
	sim.installedByUs = false
	b, _ := newTestBootstrapper(t, sim, nil)
	create(t, b)
	if b.last.hist.BootAttempts != 1 {
		t.Errorf("boot attempts = %d, want 1", b.last.hist.BootAttempts)
	}
	if sim.count("apply") != 1 {
		t.Error("expected the foreign node to be re-imaged")
	}
	if !sim.installedByUs {
		t.Error("node should now be ours")
	}
}

func TestBootstrapper_ContextCancelled(t *testing.T) {
	sim := newSim()
	sim.faults.hangBoots = 10
	b, _ := newTestBootstrapper(t, sim, func(c *Config) { c.Boot.Attempts = 100 })
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	_, err := b.Create(ctx, capi.BootstrapOptions{Name: "x"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestBootstrapper_BootstrapRPCErrorIgnoredWhenEtcdStarts(t *testing.T) {
	sim := newSim()
	sim.faults.bootstrapErr = errors.New("etcd is already bootstrapped")
	b, _ := newTestBootstrapper(t, sim, nil)
	create(t, b)
}

func TestBootstrapper_AddonsFailOnceThenSucceed(t *testing.T) {
	sim := newSim()
	b, inst := newTestBootstrapper(t, sim, nil)
	inst.failures = 1
	create(t, b)
	if inst.calls != 2 {
		t.Errorf("addon installer calls = %d, want 2", inst.calls)
	}
}

func TestBootstrapper_NoAddonsSkipsReadyWait(t *testing.T) {
	sim := newSim()
	b, inst := newTestBootstrapper(t, sim, func(c *Config) { c.Addons = Addons{} })
	create(t, b)
	if inst.calls != 0 {
		t.Errorf("addon installer should not be called, got %d", inst.calls)
	}
	if sim.nodeReady {
		t.Error("simulator should still report NotReady without a CNI")
	}
}

func TestBootstrapper_DeleteWithSession(t *testing.T) {
	sim := newSim()
	b, _ := newTestBootstrapper(t, sim, nil)
	cluster := create(t, b)
	kubeconfigPath := cluster.KubeconfigPath

	if err := b.Delete(context.Background(), cluster); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if sim.count("reset") != 1 {
		t.Error("expected a Talos reset")
	}
	if sim.count("power-off") == 0 {
		t.Error("expected a power off")
	}
	if sim.diskInstalled {
		t.Error("disk should be wiped")
	}
	if _, err := os.Stat(kubeconfigPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("kubeconfig should be removed, stat err = %v", err)
	}
}

func TestBootstrapper_DeleteWithoutSession(t *testing.T) {
	sim := newSim()
	sim.power = PowerOn
	b, _ := newTestBootstrapper(t, sim, nil)
	if err := b.Delete(context.Background(), &capi.Cluster{Name: "orphan"}); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if sim.count("reset") != 0 {
		t.Error("no reset possible without a talosconfig")
	}
	if sim.count("power-off") == 0 || sim.count("eject") == 0 {
		t.Error("expected power off and eject")
	}
}

func TestBootstrapper_Exists(t *testing.T) {
	sim := newSim()
	b, _ := newTestBootstrapper(t, sim, nil)
	ctx := context.Background()
	if ok, _ := b.Exists(ctx, "x"); ok {
		t.Error("Exists() should be false before Create")
	}
	cluster := create(t, b)
	if ok, _ := b.Exists(ctx, "x"); !ok {
		t.Error("Exists() should be true after Create")
	}
	if err := b.Delete(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	if ok, _ := b.Exists(ctx, "x"); ok {
		t.Error("Exists() should be false after Delete")
	}
}

func TestBootstrapper_ValidateConfig(t *testing.T) {
	sim := newSim()
	b, _ := newTestBootstrapper(t, sim, func(c *Config) { c.Machine.IP = "" })
	_, err := b.Create(context.Background(), capi.BootstrapOptions{Name: "x"})
	if err == nil || !strings.Contains(err.Error(), "machine ip") {
		t.Fatalf("error = %v, want validation error about machine ip", err)
	}
}

func TestRewriteKubeconfigServer(t *testing.T) {
	out, err := rewriteKubeconfigServer([]byte(simKubeconfig), "https://10.0.0.5:6443")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "server: https://10.0.0.5:6443") {
		t.Fatalf("server not rewritten:\n%s", out)
	}
	if _, err := rewriteKubeconfigServer([]byte("not: [valid"), "x"); err == nil {
		t.Fatal("expected error for malformed kubeconfig")
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/capi/talos/ -run 'TestBootstrapper|TestRewriteKubeconfigServer' -v`
Expected: FAIL to compile with `undefined: New` (and `Config`, `Timeouts`, …).

- [ ] **Step 4: Write bootstrapper.go**

`internal/capi/talos/bootstrapper.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"github.com/tinkerbell-community/terraform-provider-capi/internal/capi"
)

// BootMethod selects how the node is booted into the installer.
type BootMethod string

// BootMethod values.
const (
	BootMethodAuto         BootMethod = "auto"
	BootMethodVirtualMedia BootMethod = "virtual_media"
	BootMethodHTTP         BootMethod = "http"
)

// Defaults applied by Config.withDefaults.
const (
	DefaultBootTimeout  = 15 * time.Minute
	DefaultBootAttempts = 3
	DefaultArchitecture = "amd64"
	defaultClusterName  = "capi-bootstrap"
)

// MachineConfig identifies the bootstrap machine.
type MachineConfig struct {
	Hostname string
	IP       string
	Disk     string
	BMC      BMCCredentials
}

// BootConfig controls installer boot.
type BootConfig struct {
	Method   BootMethod
	Timeout  time.Duration
	Attempts int
}

// TalosConfig controls the Talos version, images, and machine config.
type TalosConfig struct {
	Version       string
	Architecture  string
	Endpoint      string
	Image         ImageSpec
	ConfigPatches []string
}

// Config is the complete bootstrapper configuration.
type Config struct {
	Machine MachineConfig
	Boot    BootConfig
	Talos   TalosConfig
	Addons  Addons
}

func (c Config) withDefaults() Config {
	if c.Boot.Method == "" {
		c.Boot.Method = BootMethodAuto
	}
	if c.Boot.Timeout == 0 {
		c.Boot.Timeout = DefaultBootTimeout
	}
	if c.Boot.Attempts <= 0 {
		c.Boot.Attempts = DefaultBootAttempts
	}
	if c.Talos.Architecture == "" {
		c.Talos.Architecture = DefaultArchitecture
	}
	if c.Talos.Endpoint == "" && c.Machine.IP != "" {
		c.Talos.Endpoint = "https://" + net.JoinHostPort(c.Machine.IP, "6443")
	}
	if c.Talos.Image.Factory == "" {
		c.Talos.Image.Factory = DefaultFactoryURL
	}
	c.Talos.Image.Version = c.Talos.Version
	c.Talos.Image.Architecture = c.Talos.Architecture
	return c
}

// Validate reports every missing or invalid field at once.
func (c Config) Validate() error {
	var errs []error
	if c.Machine.IP == "" {
		errs = append(errs, errors.New("machine ip is required"))
	}
	if c.Machine.Disk == "" {
		errs = append(errs, errors.New("machine disk is required"))
	}
	if c.Machine.BMC.Address == "" {
		errs = append(errs, errors.New("bmc address is required"))
	}
	if c.Talos.Version == "" {
		errs = append(errs, errors.New("talos version is required"))
	}
	switch c.Boot.Method {
	case BootMethodAuto, BootMethodVirtualMedia, BootMethodHTTP:
	default:
		errs = append(errs, fmt.Errorf("unknown boot method %q", c.Boot.Method))
	}
	switch c.Talos.Architecture {
	case "amd64", "arm64":
	default:
		errs = append(errs, fmt.Errorf("unknown architecture %q", c.Talos.Architecture))
	}
	return errors.Join(errs...)
}

// Option customizes a Bootstrapper; used by tests and by the provider.
type Option func(*Bootstrapper)

// WithBMC overrides the BMC implementation.
func WithBMC(b BMC) Option { return func(bs *Bootstrapper) { bs.bmc = b } }

// WithNode overrides the Talos API implementation.
func WithNode(n Node) Option { return func(bs *Bootstrapper) { bs.node = n } }

// WithKubeFactory overrides how the Kubernetes probe is built.
func WithKubeFactory(f KubeFactory) Option { return func(bs *Bootstrapper) { bs.kubeFactory = f } }

// WithImageResolver overrides image resolution.
func WithImageResolver(r ImageResolver) Option { return func(bs *Bootstrapper) { bs.images = r } }

// WithAddonInstaller overrides addon installation.
func WithAddonInstaller(a AddonInstaller) Option { return func(bs *Bootstrapper) { bs.addons = a } }

// WithLogger sets the logger.
func WithLogger(l *log.Logger) Option { return func(bs *Bootstrapper) { bs.logger = l } }

// WithTimeouts overrides phase timeouts; zero fields keep their defaults.
func WithTimeouts(t Timeouts) Option { return func(bs *Bootstrapper) { bs.timeouts = t } }

// WithTempDir sets where kubeconfig and talosconfig files are written.
func WithTempDir(dir string) Option { return func(bs *Bootstrapper) { bs.tempDir = dir } }

// Bootstrapper implements capi.Bootstrapper for a Talos bare-metal node.
type Bootstrapper struct {
	cfg         Config
	bmc         BMC
	node        Node
	kubeFactory KubeFactory
	images      ImageResolver
	addons      AddonInstaller
	logger      *log.Logger
	timeouts    Timeouts
	tempDir     string

	mu   sync.Mutex
	last *reconciler
}

var _ capi.Bootstrapper = (*Bootstrapper)(nil)

// New builds a Bootstrapper with real bmclib, Talos, Helm, and client-go
// implementations unless overridden by options.
func New(cfg Config, opts ...Option) *Bootstrapper {
	cfg = cfg.withDefaults()
	b := &Bootstrapper{cfg: cfg, tempDir: os.TempDir()}
	for _, o := range opts {
		o(b)
	}
	b.timeouts = b.timeouts.withDefaults(cfg.Boot.Timeout)
	if b.logger == nil {
		b.logger = log.New(os.Stderr, "[talos-bootstrap] ", log.LstdFlags)
	}
	if b.bmc == nil {
		b.bmc = NewBMCLib(cfg.Machine.BMC, b.logger)
	}
	if b.node == nil {
		b.node = NewMachineryNode(cfg.Machine.IP)
	}
	if b.kubeFactory == nil {
		b.kubeFactory = NewClientGoKube
	}
	if b.images == nil {
		b.images = NewFactoryResolver(nil)
	}
	if b.addons == nil {
		b.addons = NewAddonInstaller(NewSDKHelmClient(b.logger), capi.NewDynamicApplier(), b.logger)
	}
	return b
}

func (b *Bootstrapper) newReconciler(name, k8sVersion string) *reconciler {
	return &reconciler{
		name: name, k8sVersion: k8sVersion, cfg: b.cfg,
		bmc: b.bmc, node: b.node, kubeFactory: b.kubeFactory, addons: b.addons,
		logger: b.logger, timeouts: b.timeouts, tempDir: b.tempDir,
		sess: &session{}, failures: map[ActionKind]int{},
	}
}

// Create implements capi.Bootstrapper. It converges the node from whatever
// state it is in to a ready single-node cluster.
func (b *Bootstrapper) Create(ctx context.Context, opts capi.BootstrapOptions) (*capi.Cluster, error) {
	name := opts.Name
	if name == "" {
		name = defaultClusterName
	}
	if err := b.cfg.Validate(); err != nil {
		return nil, &capi.BootstrapError{ClusterName: name, Operation: "validate", Err: err}
	}

	r := b.newReconciler(name, opts.KubernetesVersion)
	b.mu.Lock()
	b.last = r
	b.mu.Unlock()

	urls, err := b.images.Resolve(ctx, b.cfg.Talos.Image)
	if err != nil {
		return nil, &capi.BootstrapError{ClusterName: name, Operation: "resolve-images", Err: err}
	}
	r.sess.images = urls

	cluster, err := r.run(ctx)
	if err != nil {
		return nil, &capi.BootstrapError{ClusterName: name, Operation: "create", Err: err}
	}
	return cluster, nil
}

// Delete implements capi.Bootstrapper. With a live session the node is reset
// and halted; in every case it is powered off and its media detached.
func (b *Bootstrapper) Delete(ctx context.Context, cluster *capi.Cluster) error {
	name := defaultClusterName
	if cluster != nil && cluster.Name != "" {
		name = cluster.Name
	}
	b.mu.Lock()
	r := b.last
	b.mu.Unlock()
	if r == nil {
		r = b.newReconciler(name, "")
	}
	if err := r.teardown(ctx); err != nil {
		return &capi.BootstrapError{ClusterName: name, Operation: "delete", Err: err}
	}
	b.mu.Lock()
	b.last = nil
	b.mu.Unlock()
	return nil
}

// Exists implements capi.Bootstrapper. It is true only while this process
// holds a session whose talosconfig the node accepts.
func (b *Bootstrapper) Exists(ctx context.Context, _ string) (bool, error) {
	b.mu.Lock()
	r := b.last
	b.mu.Unlock()
	if r == nil || r.sess.talosconfig == nil {
		return false, nil
	}
	return r.probe(ctx) == TalosOurs, nil
}
```

- [ ] **Step 5: Write reconciler.go**

`internal/capi/talos/reconciler.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	clientconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/tinkerbell-community/terraform-provider-capi/internal/capi"
)

// Timeouts bounds each phase of the reconciler.
type Timeouts struct {
	// Boot bounds the wait for maintenance mode after booting the installer.
	Boot time.Duration
	// Install bounds the wait for the node to return after ApplyConfiguration.
	Install time.Duration
	// PowerDown bounds the wait for the Talos API to disappear after apply or power off.
	PowerDown  time.Duration
	Bootstrap  time.Duration
	Kubernetes time.Duration
	Addons     time.Duration
	Ready      time.Duration
	Reset      time.Duration
	// Poll is the interval between observations inside a wait.
	Poll time.Duration
	// BMCRetries, RetryBase, and RetryMax configure retry() around BMC calls.
	BMCRetries int
	RetryBase  time.Duration
	RetryMax   time.Duration
}

// DefaultTimeouts returns production timeouts; boot doubles as the install timeout.
func DefaultTimeouts(boot time.Duration) Timeouts {
	return Timeouts{
		Boot: boot, Install: boot, PowerDown: 2 * time.Minute,
		Bootstrap: 10 * time.Minute, Kubernetes: 10 * time.Minute, Addons: 15 * time.Minute,
		Ready: 10 * time.Minute, Reset: 5 * time.Minute, Poll: 5 * time.Second,
		BMCRetries: 3, RetryBase: 500 * time.Millisecond, RetryMax: 5 * time.Second,
	}
}

func (t Timeouts) withDefaults(boot time.Duration) Timeouts {
	d := DefaultTimeouts(boot)
	pick := func(v, def time.Duration) time.Duration {
		if v == 0 {
			return def
		}
		return v
	}
	t.Boot = pick(t.Boot, d.Boot)
	t.Install = pick(t.Install, d.Install)
	t.PowerDown = pick(t.PowerDown, d.PowerDown)
	t.Bootstrap = pick(t.Bootstrap, d.Bootstrap)
	t.Kubernetes = pick(t.Kubernetes, d.Kubernetes)
	t.Addons = pick(t.Addons, d.Addons)
	t.Ready = pick(t.Ready, d.Ready)
	t.Reset = pick(t.Reset, d.Reset)
	t.Poll = pick(t.Poll, d.Poll)
	t.RetryBase = pick(t.RetryBase, d.RetryBase)
	t.RetryMax = pick(t.RetryMax, d.RetryMax)
	if t.BMCRetries <= 0 {
		t.BMCRetries = d.BMCRetries
	}
	return t
}

// session is the in-memory state of one Create run. It is never persisted.
type session struct {
	talosconfig     *clientconfig.Config
	machineConfig   []byte
	images          ImageURLs
	method          BootMethod
	kubeconfigPath  string
	talosconfigPath string
}

// maxActionFailures bounds consecutive failures of the same action before the run fails.
const maxActionFailures = 3

type reconciler struct {
	name        string
	k8sVersion  string
	cfg         Config
	bmc         BMC
	node        Node
	kubeFactory KubeFactory
	kube        Kube
	addons      AddonInstaller
	logger      *log.Logger
	timeouts    Timeouts
	tempDir     string

	sess       *session
	hist       History
	failures   map[ActionKind]int
	attemptLog []string
}

// run loops observe -> plan -> act until Done or a terminal failure.
func (r *reconciler) run(ctx context.Context) (*capi.Cluster, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		obs := r.observe(ctx)
		act := Plan(PlanInput{Observation: obs, History: r.hist, MaxAttempts: r.cfg.Boot.Attempts, HasAddons: !r.cfg.Addons.Empty()})
		r.logger.Printf("%s: observed %s -> %s (boot attempts %d/%d)", r.name, obs, act.Kind, r.hist.BootAttempts, r.cfg.Boot.Attempts)

		switch act.Kind {
		case ActionDone:
			return r.finish()
		case ActionFail:
			return nil, r.failure(act.Err, obs)
		}

		if err := r.act(ctx, act.Kind, obs); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if errors.Is(err, ErrNoBootMethod) {
				return nil, r.failure(err, obs)
			}
			r.failures[act.Kind]++
			r.logger.Printf("%s: action %s failed (%d/%d): %v", r.name, act.Kind, r.failures[act.Kind], maxActionFailures, err)
			if r.failures[act.Kind] >= maxActionFailures {
				return nil, r.failure(fmt.Errorf("%w: %s: %v", ErrActionFailed, act.Kind, err), obs)
			}
			continue
		}
		delete(r.failures, act.Kind)
	}
}

func (r *reconciler) probe(ctx context.Context) TalosState {
	st, err := r.node.Probe(ctx, r.sess.talosconfig)
	if err != nil {
		return TalosUnreachable
	}
	return st
}

func (r *reconciler) observe(ctx context.Context) Observation {
	obs := Observation{Power: PowerUnknown, Talos: TalosUnreachable, Etcd: EtcdUnknown, Kubernetes: K8sUnreachable, ObservedAt: time.Now()}
	if p, err := r.bmc.PowerState(ctx); err == nil {
		obs.Power = p
	} else {
		r.logger.Printf("%s: bmc power state: %v", r.name, err)
	}
	obs.Talos = r.probe(ctx)
	if obs.Talos != TalosOurs {
		return obs
	}
	if st, err := r.node.EtcdState(ctx, r.sess.talosconfig); err == nil {
		obs.Etcd = st
	} else {
		obs.Etcd = EtcdNotBootstrapped
	}
	if r.kube != nil {
		if ok, _ := r.kube.APIReachable(ctx); ok {
			obs.Kubernetes = K8sReachable
			if ready, _ := r.kube.NodeReady(ctx); ready {
				obs.Kubernetes = K8sNodeReady
			}
		}
	}
	return obs
}

func (r *reconciler) bmcDo(ctx context.Context, fn func(context.Context) error) error {
	return retry(ctx, r.timeouts.BMCRetries, r.timeouts.RetryBase, r.timeouts.RetryMax, fn)
}

func (r *reconciler) act(ctx context.Context, kind ActionKind, obs Observation) error {
	switch kind {
	case ActionBootInstaller:
		return r.bootInstaller(ctx, obs)
	case ActionApplyConfig:
		return r.applyConfig(ctx)
	case ActionRebootToDisk:
		return r.rebootToDisk(ctx)
	case ActionDetachMedia:
		r.detachMedia(ctx)
		return nil
	case ActionBootstrapEtcd:
		return r.bootstrapEtcd(ctx)
	case ActionWaitKubernetes:
		return r.waitKubernetes(ctx)
	case ActionInstallAddons:
		return r.installAddons(ctx)
	case ActionWaitReady:
		return r.waitReady(ctx)
	}
	return fmt.Errorf("%w: action %s", ErrUnknownState, kind)
}

// bootInstaller drives the node into the Talos installer (maintenance mode).
func (r *reconciler) bootInstaller(ctx context.Context, obs Observation) error {
	r.hist.BootAttempts++
	r.hist.ConfigApplied = false
	r.hist.WentDownAfterApply = false
	attempt := r.hist.BootAttempts
	r.logger.Printf("%s: boot attempt %d/%d", r.name, attempt, r.cfg.Boot.Attempts)

	if obs.Talos != TalosUnreachable {
		if err := r.bmcDo(ctx, r.bmc.PowerOff); err != nil {
			return err
		}
		if err := waitFor(ctx, r.timeouts.PowerDown, r.timeouts.Poll, func(ctx context.Context) (bool, error) {
			return r.probe(ctx) == TalosUnreachable, nil
		}); err != nil && ctx.Err() == nil {
			r.logger.Printf("%s: node still answering after power off; continuing with power cycle", r.name)
		}
	}

	r.detachMedia(ctx)
	if err := r.attachMedia(ctx); err != nil {
		return err
	}
	r.hist.MediaAttached = true

	// A BMC that reports "on" for an off machine is common; a cycle is correct either way.
	powerFn := r.bmc.PowerOn
	if obs.Power == PowerOn {
		powerFn = r.bmc.PowerCycle
	}
	if err := r.bmcDo(ctx, powerFn); err != nil {
		return err
	}

	err := waitFor(ctx, r.timeouts.Boot, r.timeouts.Poll, func(ctx context.Context) (bool, error) {
		return r.probe(ctx) == TalosMaintenance, nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.recordAttempt(ctx, attempt, ErrBootTimeout)
	}
	return nil
}

// attachMedia attaches installer media by the configured method. A pinned
// method that the BMC lacks, or auto with no usable method, is ErrNoBootMethod.
func (r *reconciler) attachMedia(ctx context.Context) error {
	virtualMedia := func(ctx context.Context) error {
		if r.sess.images.ISO == "" {
			return fmt.Errorf("virtual media: no ISO URL: %w", ErrUnsupported)
		}
		if err := r.bmcDo(ctx, func(ctx context.Context) error { return r.bmc.InsertMedia(ctx, r.sess.images.ISO) }); err != nil {
			return err
		}
		return r.bmcDo(ctx, func(ctx context.Context) error { return r.bmc.SetBootDevice(ctx, BootDeviceCDROM, false, true) })
	}
	httpBoot := func(ctx context.Context) error {
		if r.sess.images.UKI == "" {
			return fmt.Errorf("uefi http boot: no UKI URL: %w", ErrUnsupported)
		}
		if err := r.bmcDo(ctx, func(ctx context.Context) error { return r.bmc.SetHTTPBootURI(ctx, r.sess.images.UKI) }); err != nil {
			return err
		}
		return r.bmcDo(ctx, func(ctx context.Context) error { return r.bmc.SetBootDevice(ctx, BootDeviceUEFIHTTP, false, true) })
	}

	var err error
	switch r.cfg.Boot.Method {
	case BootMethodVirtualMedia:
		r.sess.method = BootMethodVirtualMedia
		err = virtualMedia(ctx)
	case BootMethodHTTP:
		r.sess.method = BootMethodHTTP
		err = httpBoot(ctx)
	default:
		if err = virtualMedia(ctx); err == nil {
			r.sess.method = BootMethodVirtualMedia
			return nil
		}
		if !errors.Is(err, ErrUnsupported) {
			return err
		}
		r.logger.Printf("%s: virtual media unsupported, trying UEFI HTTP boot", r.name)
		if err = httpBoot(ctx); err == nil {
			r.sess.method = BootMethodHTTP
			return nil
		}
	}
	if errors.Is(err, ErrUnsupported) {
		return fmt.Errorf("%w: %v", ErrNoBootMethod, err)
	}
	return err
}

// applyConfig generates (once) and applies the machine config, then waits for
// the node to reboot and come back.
func (r *reconciler) applyConfig(ctx context.Context) error {
	if r.sess.machineConfig == nil {
		bundle, err := NewSecretsBundle(r.cfg.Talos.Version)
		if err != nil {
			return err
		}
		gen, err := GenerateConfig(ConfigInput{
			ClusterName: r.name, Endpoint: r.cfg.Talos.Endpoint, KubernetesVersion: r.k8sVersion,
			TalosVersion: r.cfg.Talos.Version, NodeIP: r.cfg.Machine.IP, InstallDisk: r.cfg.Machine.Disk,
			InstallerImage: r.sess.images.Installer, Patches: r.cfg.Talos.ConfigPatches,
		}, bundle)
		if err != nil {
			return err
		}
		r.sess.machineConfig = gen.MachineConfig
		r.sess.talosconfig = gen.Talosconfig
	}

	r.logger.Printf("%s: applying machine configuration (reboot mode)", r.name)
	if err := r.node.ApplyConfiguration(ctx, r.sess.machineConfig); err != nil {
		return err
	}
	r.hist.ConfigApplied = true

	if err := waitFor(ctx, r.timeouts.PowerDown, r.timeouts.Poll, func(ctx context.Context) (bool, error) {
		return r.probe(ctx) != TalosMaintenance, nil
	}); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.hist.ConfigApplied = false
		r.hist.BootAttempts++
		r.recordAttempt(ctx, r.hist.BootAttempts, errors.New("node did not reboot after apply"))
		return nil
	}
	r.hist.WentDownAfterApply = true

	if err := waitFor(ctx, r.timeouts.Install, r.timeouts.Poll, func(ctx context.Context) (bool, error) {
		st := r.probe(ctx)
		return st == TalosOurs || st == TalosMaintenance, nil
	}); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.recordAttempt(ctx, r.hist.BootAttempts, ErrInstallTimeout)
	}
	return nil
}

// rebootToDisk handles firmware that booted the installer again after install.
func (r *reconciler) rebootToDisk(ctx context.Context) error {
	r.hist.BootAttempts++
	r.logger.Printf("%s: installer booted again after install; detaching media and rebooting to disk (attempt %d/%d)", r.name, r.hist.BootAttempts, r.cfg.Boot.Attempts)
	r.detachMedia(ctx)
	if err := r.bmcDo(ctx, func(ctx context.Context) error { return r.bmc.SetBootDevice(ctx, BootDeviceDisk, false, true) }); err != nil && !errors.Is(err, ErrUnsupported) {
		return err
	}
	if err := r.bmcDo(ctx, r.bmc.PowerCycle); err != nil {
		return err
	}
	if err := waitFor(ctx, r.timeouts.Install, r.timeouts.Poll, func(ctx context.Context) (bool, error) {
		return r.probe(ctx) == TalosOurs, nil
	}); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.recordAttempt(ctx, r.hist.BootAttempts, ErrInstallTimeout)
	}
	return nil
}

// detachMedia ejects virtual media and clears the HTTP boot URI, best effort.
func (r *reconciler) detachMedia(ctx context.Context) {
	if err := r.bmcDo(ctx, r.bmc.EjectMedia); err != nil && !errors.Is(err, ErrUnsupported) {
		r.logger.Printf("%s: eject media: %v", r.name, err)
	}
	if err := r.bmcDo(ctx, func(ctx context.Context) error { return r.bmc.SetHTTPBootURI(ctx, "") }); err != nil && !errors.Is(err, ErrUnsupported) {
		r.logger.Printf("%s: clear http boot uri: %v", r.name, err)
	}
	r.hist.MediaAttached = false
}

// bootstrapEtcd issues Bootstrap and waits for etcd. The RPC error is only
// logged: a node that is already bootstrapped rejects the call but passes the wait.
func (r *reconciler) bootstrapEtcd(ctx context.Context) error {
	if err := r.node.Bootstrap(ctx, r.sess.talosconfig); err != nil {
		r.logger.Printf("%s: bootstrap rpc: %v (ignored if etcd comes up)", r.name, err)
	}
	if err := waitFor(ctx, r.timeouts.Bootstrap, r.timeouts.Poll, func(ctx context.Context) (bool, error) {
		st, err := r.node.EtcdState(ctx, r.sess.talosconfig)
		return err == nil && st == EtcdBootstrapped, nil
	}); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrBootstrapTimeout
	}
	return nil
}

// waitKubernetes fetches the kubeconfig once, pins it to the node IP, and
// waits for the API server.
func (r *reconciler) waitKubernetes(ctx context.Context) error {
	if r.kube == nil {
		raw, err := r.node.Kubeconfig(ctx, r.sess.talosconfig)
		if err != nil {
			return fmt.Errorf("fetching kubeconfig: %w", err)
		}
		rewritten, err := rewriteKubeconfigServer(raw, "https://"+net.JoinHostPort(r.cfg.Machine.IP, "6443"))
		if err != nil {
			return err
		}
		path := filepath.Join(r.tempDir, r.name+"-kubeconfig")
		if err := os.WriteFile(path, rewritten, 0o600); err != nil {
			return fmt.Errorf("writing kubeconfig: %w", err)
		}
		r.sess.kubeconfigPath = path
		k, err := r.kubeFactory(path)
		if err != nil {
			return err
		}
		r.kube = k
	}
	if err := waitFor(ctx, r.timeouts.Kubernetes, r.timeouts.Poll, func(ctx context.Context) (bool, error) {
		return r.kube.APIReachable(ctx)
	}); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrKubernetesTimeout
	}
	return nil
}

// rewriteKubeconfigServer points every cluster entry at server. The Talos
// kubeconfig names the cluster endpoint, which may be a VIP that does not
// exist until the workload cluster is up.
func rewriteKubeconfigServer(raw []byte, server string) ([]byte, error) {
	cfg, err := clientcmd.Load(raw)
	if err != nil {
		return nil, fmt.Errorf("parsing kubeconfig: %w", err)
	}
	for _, c := range cfg.Clusters {
		c.Server = server
	}
	return clientcmd.Write(*cfg)
}

func (r *reconciler) installAddons(ctx context.Context) error {
	if !r.cfg.Addons.Empty() {
		actx, cancel := context.WithTimeout(ctx, r.timeouts.Addons)
		defer cancel()
		if err := r.addons.Install(actx, r.sess.kubeconfigPath, r.cfg.Addons); err != nil {
			return err
		}
	}
	r.hist.AddonsInstalled = true
	return nil
}

func (r *reconciler) waitReady(ctx context.Context) error {
	if err := waitFor(ctx, r.timeouts.Ready, r.timeouts.Poll, func(ctx context.Context) (bool, error) {
		return r.kube.NodeReady(ctx)
	}); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrReadyTimeout
	}
	return nil
}

func (r *reconciler) finish() (*capi.Cluster, error) {
	path := filepath.Join(r.tempDir, r.name+"-talosconfig")
	if err := r.sess.talosconfig.Save(path); err != nil {
		r.logger.Printf("%s: writing talosconfig: %v", r.name, err)
	} else {
		r.sess.talosconfigPath = path
	}
	r.logger.Printf("%s: bootstrap cluster ready (kubeconfig %s)", r.name, r.sess.kubeconfigPath)
	return &capi.Cluster{Name: r.name, KubeconfigPath: r.sess.kubeconfigPath}, nil
}

// recordAttempt logs a failed boot attempt with whatever diagnostics the BMC offers.
func (r *reconciler) recordAttempt(ctx context.Context, attempt int, cause error) {
	entry := fmt.Sprintf("attempt %d: %v", attempt, cause)
	if code, err := r.bmc.PostCode(ctx); err == nil && code != "" {
		entry += "; post code " + code
	}
	r.attemptLog = append(r.attemptLog, entry)
	r.logger.Printf("%s: %s", r.name, entry)
}

func (r *reconciler) failure(cause error, obs Observation) error {
	return fmt.Errorf("%w (last observation: %s; attempts: %s)", cause, obs, strings.Join(r.attemptLog, " | "))
}

// teardown resets the node when we can talk to it, then powers it off and
// detaches media. It removes the temp files written by this run.
func (r *reconciler) teardown(ctx context.Context) error {
	var errs []error
	if r.sess.talosconfig != nil && r.probe(ctx) == TalosOurs {
		r.logger.Printf("%s: resetting node", r.name)
		if err := r.node.Reset(ctx, r.sess.talosconfig); err != nil {
			errs = append(errs, fmt.Errorf("reset: %w", err))
		} else if err := waitFor(ctx, r.timeouts.Reset, r.timeouts.Poll, func(ctx context.Context) (bool, error) {
			return r.probe(ctx) != TalosOurs, nil
		}); err != nil {
			errs = append(errs, fmt.Errorf("waiting for reset: %w", err))
		}
	}
	if err := r.bmcDo(ctx, r.bmc.PowerOff); err != nil {
		errs = append(errs, err)
	}
	r.detachMedia(ctx)
	for _, p := range []string{r.sess.kubeconfigPath, r.sess.talosconfigPath} {
		if p != "" {
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
```

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/capi/talos/ -run 'TestBootstrapper|TestRewriteKubeconfigServer' -v -timeout 60s`
Expected: PASS for every scenario. Watch for two known pitfalls:
- If `TestBootstrapper_HappyPathVirtualMedia` hangs, `settle()` never cleared `pendingBoot`; check that `Probe` calls `settle()` before the power check.
- If `TestBootstrapper_FirmwareBootsInstallerTwice` reports 3 attempts, the reboot after apply was observed as "did not reboot"; increase `rebootTicks` in `boot()` to 2 so the down window is observable at 1 ms polling.

- [ ] **Step 7: Run the whole package with the race detector, then lint**

Run: `go test ./internal/capi/talos/ -race -count=1 && golangci-lint run ./internal/capi/talos/`
Expected: `ok`; no findings.

- [ ] **Step 8: Commit**

```bash
git add internal/capi/talos/reconciler.go internal/capi/talos/bootstrapper.go internal/capi/talos/sim_test.go internal/capi/talos/reconciler_test.go
git commit -m "Add Talos BMC bootstrapper reconciler with simulated-node tests"
```

---

### Task 10: Manager integration test

Prove the new bootstrapper drops into the existing `capi.Manager` flow: create, init, apply, wait, pivot, delete bootstrap.

**Files:**
- Test: `internal/capi/talos/manager_integration_test.go`

**Interfaces:**
- Consumes: `capi.NewManager`, `capi.WithBootstrapper`, `capi.WithInstaller`, `capi.WithTemplateGenerator`, `capi.WithApplier`, `capi.WithMover`, `capi.WithWaiter`, `capi.WithInfoRetriever`, `capi.WithDescriber`, `capi.CreateClusterOptions`. The `Mock*` types in `internal/capi/mock_test.go` are test-only and not importable, so this test defines its own minimal fakes.

- [ ] **Step 1: Write the test**

`internal/capi/talos/manager_integration_test.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"context"
	"io"
	"log"
	"testing"

	"github.com/tinkerbell-community/terraform-provider-capi/internal/capi"
)

type noopInstaller struct{ inits []string }

func (n *noopInstaller) Init(_ context.Context, c *capi.Cluster, _ capi.InitOptions) error {
	n.inits = append(n.inits, c.Name)
	return nil
}

type noopTemplate struct{}

func (noopTemplate) Generate(context.Context, *capi.Cluster, capi.TemplateOptions) ([]byte, error) {
	return []byte("kind: Cluster\n"), nil
}

type noopApplier struct{}

func (noopApplier) Apply(context.Context, *capi.Cluster, []byte) error          { return nil }
func (noopApplier) Delete(context.Context, *capi.Cluster, string, string) error { return nil }

type noopMover struct{ moves int }

func (m *noopMover) Move(context.Context, *capi.Cluster, *capi.Cluster, capi.MoveOptions) error {
	m.moves++
	return nil
}

type noopWaiter struct{}

func (noopWaiter) WaitForControlPlane(context.Context, *capi.Cluster, string, string, capi.WaitOptions) error {
	return nil
}
func (noopWaiter) WaitForWorkers(context.Context, *capi.Cluster, string, string, capi.WaitOptions) error {
	return nil
}
func (noopWaiter) WaitForClusterReady(context.Context, *capi.Cluster, string, string, capi.WaitOptions) error {
	return nil
}

type noopInfo struct{}

func (noopInfo) GetKubeconfig(context.Context, *capi.Cluster, string, string) (string, error) {
	return "apiVersion: v1\nkind: Config\n", nil
}
func (noopInfo) Describe(context.Context, *capi.Cluster, string, string) (string, error) {
	return "ok", nil
}

func TestManager_UsesTalosBootstrapperForSelfManagedCreate(t *testing.T) {
	sim := newSim()
	b, _ := newTestBootstrapper(t, sim, nil)
	installer := &noopInstaller{}
	mover := &noopMover{}

	mgr := capi.NewManager(
		capi.WithBootstrapper(b),
		capi.WithInstaller(installer),
		capi.WithTemplateGenerator(noopTemplate{}),
		capi.WithApplier(noopApplier{}),
		capi.WithMover(mover),
		capi.WithWaiter(noopWaiter{}),
		capi.WithInfoRetriever(noopInfo{}),
		capi.WithDescriber(noopInfo{}),
		capi.WithLogger(log.New(io.Discard, "", 0)),
	)

	result, err := mgr.CreateCluster(context.Background(), capi.CreateClusterOptions{
		Name: "prod", Namespace: "default", InfrastructureProvider: "tinkerbell",
		KubernetesVersion: "v1.34.0", SelfManaged: true, WaitForReady: true,
	})
	if err != nil {
		t.Fatalf("CreateCluster() error = %v", err)
	}
	if result.BootstrapCluster != nil {
		t.Error("bootstrap cluster should be deleted after the pivot")
	}
	if len(installer.inits) != 2 || installer.inits[0] != "prod-bootstrap" {
		t.Errorf("clusterctl init calls = %v, want [prod-bootstrap prod]", installer.inits)
	}
	if mover.moves != 1 {
		t.Errorf("moves = %d, want 1", mover.moves)
	}
	if sim.count("reset") != 1 || sim.diskInstalled {
		t.Error("bootstrap node should be reset and wiped after the pivot")
	}
	if sim.count("power-off") == 0 {
		t.Error("bootstrap node should be powered off after the pivot")
	}
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./internal/capi/talos/ -run TestManager_UsesTalosBootstrapperForSelfManagedCreate -v`
Expected: PASS. If the manager's step 6 warns that the kubeconfig could not be written, that is expected (no `ClusterctlInfoRetriever` in the fakes) and does not fail the test.

- [ ] **Step 3: Commit**

```bash
git add internal/capi/talos/manager_integration_test.go
git commit -m "Test Talos bootstrapper through the CAPI manager pivot flow"
```

---
### Task 11: Provider models, schema, and validation for `management.bootstrap`

**Files:**
- Modify: `internal/provider/cluster_resource_models.go` (models after `ManagementModel` at line 41; attr types after `managementAttrTypes()` at line 196; extractors after `extractManagement` at line 371)
- Modify: `internal/provider/cluster_resource.go` (schema inside `"management"` at lines 79–120; state upgrader `ManagementModel{` at line 676; `ensureManagementComputed` at line 885; validation call site before `validateAddons` at line 830)
- Modify: `internal/provider/cluster_resource_models_test.go` (5 existing `ManagementModel{` literals; `testMachine` and `buildTestMachineList` at line 715+)
- Test: `internal/provider/bootstrap_validation_test.go`

**Interfaces:**
- Produces (package `provider`):
  ```go
  type ManagementModel struct { Kubeconfig, ...; Bootstrap types.Object `tfsdk:"bootstrap"` }
  type ManagementBootstrapModel, BootModel, TalosModel, TalosImageModel, BootstrapAddonsModel, HelmReleaseModel
  func managementBootstrapAttrTypes(), bootAttrTypes(), talosAttrTypes(), talosImageAttrTypes(), bootstrapAddonsAttrTypes(), helmReleaseAttrTypes() map[string]attr.Type
  func extractManagementBootstrap(ctx, mgmt *ManagementModel) (*ManagementBootstrapModel, diag.Diagnostics)
  func extractBoot(ctx, bs *ManagementBootstrapModel) (*BootModel, diag.Diagnostics)
  func extractTalos(ctx, bs *ManagementBootstrapModel) (*TalosModel, diag.Diagnostics)
  func extractTalosImage(ctx, tm *TalosModel) (*TalosImageModel, diag.Diagnostics)
  func extractBootstrapAddons(ctx, bs *ManagementBootstrapModel) (*BootstrapAddonsModel, diag.Diagnostics)
  func findInventoryMachine(ctx, data *ClusterResourceModel, hostname string) (*MachineModel, diag.Diagnostics)
  func stringList(ctx, l types.List) ([]string, diag.Diagnostics)
  func validateManagementBootstrap(ctx, data *ClusterResourceModel, diags *diag.Diagnostics)
  ```

- [ ] **Step 1: Extend the test machine builder**

In `internal/provider/cluster_resource_models_test.go`, change `testMachine` and `buildTestMachineList` so machines can carry a disk and a BMC:

```go
type testMachine struct {
	hostname string
	ip       string
	mac      string
	role     string // "cp" or "worker"
	disk     string // e.g. "/dev/sda"; empty leaves disk null
	bmc      string // BMC address; empty leaves bmc null
}
```

and inside the loop in `buildTestMachineList`, replace the two `types.ObjectNull(...)` fields with:

```go
		diskVal := types.ObjectNull(diskAttrTypes())
		if m.disk != "" {
			v, d := types.ObjectValueFrom(ctx, diskAttrTypes(), DiskModel{Device: types.StringValue(m.disk)})
			if d.HasError() {
				t.Fatalf("build disk: %v", d)
			}
			diskVal = v
		}
		bmcVal := types.ObjectNull(bmcAttrTypes())
		if m.bmc != "" {
			v, d := types.ObjectValueFrom(ctx, bmcAttrTypes(), BMCModel{
				Address:  types.StringValue(m.bmc),
				Username: types.StringValue("admin"),
				Password: types.StringValue("secret"),
			})
			if d.HasError() {
				t.Fatalf("build bmc: %v", d)
			}
			bmcVal = v
		}

		machineObjects = append(machineObjects, MachineModel{
			Hostname: types.StringValue(m.hostname),
			Network:  netVal,
			Disk:     diskVal,
			BMC:      bmcVal,
			Labels:   labels,
		})
```

- [ ] **Step 2: Write the failing validation and schema tests**

`internal/provider/bootstrap_validation_test.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// --- builders ---

func testTalosModel(ctx context.Context, t *testing.T, version string, image *TalosImageModel, patches []string) *TalosModel {
	t.Helper()
	tm := &TalosModel{
		Version:       types.StringValue(version),
		Architecture:  types.StringValue("amd64"),
		Endpoint:      types.StringNull(),
		Image:         types.ObjectNull(talosImageAttrTypes()),
		ConfigPatches: types.ListNull(types.StringType),
	}
	if version == "" {
		tm.Version = types.StringNull()
	}
	if image != nil {
		v, d := types.ObjectValueFrom(ctx, talosImageAttrTypes(), *image)
		if d.HasError() {
			t.Fatalf("build image: %v", d)
		}
		tm.Image = v
	}
	if patches != nil {
		l, d := types.ListValueFrom(ctx, types.StringType, patches)
		if d.HasError() {
			t.Fatalf("build patches: %v", d)
		}
		tm.ConfigPatches = l
	}
	return tm
}

func testImageModel(ctx context.Context, t *testing.T, iso, installer, schematic string, extensions []string) *TalosImageModel {
	t.Helper()
	str := func(s string) types.String {
		if s == "" {
			return types.StringNull()
		}
		return types.StringValue(s)
	}
	img := &TalosImageModel{
		Factory:    types.StringValue("https://factory.talos.dev"),
		Schematic:  str(schematic),
		Extensions: types.ListNull(types.StringType),
		KernelArgs: types.ListNull(types.StringType),
		ISO:        str(iso),
		Installer:  str(installer),
	}
	if extensions != nil {
		l, d := types.ListValueFrom(ctx, types.StringType, extensions)
		if d.HasError() {
			t.Fatalf("build extensions: %v", d)
		}
		img.Extensions = l
	}
	return img
}

func testHelmList(ctx context.Context, t *testing.T, rels []HelmReleaseModel) types.List {
	t.Helper()
	l, d := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: helmReleaseAttrTypes()}, rels)
	if d.HasError() {
		t.Fatalf("build helm list: %v", d)
	}
	return l
}

func helmRel(name, ns, chart, timeout, values string) HelmReleaseModel {
	str := func(s string) types.String {
		if s == "" {
			return types.StringNull()
		}
		return types.StringValue(s)
	}
	return HelmReleaseModel{
		Name: types.StringValue(name), Namespace: types.StringValue(ns), Chart: types.StringValue(chart),
		Repository: types.StringNull(), Version: types.StringNull(), Values: str(values), Timeout: str(timeout),
	}
}

func buildTestBootstrap(ctx context.Context, t *testing.T, typ, machine string, boot *BootModel, talos *TalosModel, addons *BootstrapAddonsModel) types.Object {
	t.Helper()
	bs := ManagementBootstrapModel{
		Type:    types.StringValue(typ),
		Machine: types.StringValue(machine),
		Boot:    types.ObjectNull(bootAttrTypes()),
		Talos:   types.ObjectNull(talosAttrTypes()),
		Addons:  types.ObjectNull(bootstrapAddonsAttrTypes()),
	}
	if machine == "" {
		bs.Machine = types.StringNull()
	}
	if boot != nil {
		v, d := types.ObjectValueFrom(ctx, bootAttrTypes(), *boot)
		if d.HasError() {
			t.Fatalf("build boot: %v", d)
		}
		bs.Boot = v
	}
	if talos != nil {
		v, d := types.ObjectValueFrom(ctx, talosAttrTypes(), *talos)
		if d.HasError() {
			t.Fatalf("build talos: %v", d)
		}
		bs.Talos = v
	}
	if addons != nil {
		v, d := types.ObjectValueFrom(ctx, bootstrapAddonsAttrTypes(), *addons)
		if d.HasError() {
			t.Fatalf("build addons: %v", d)
		}
		bs.Addons = v
	}
	v, d := types.ObjectValueFrom(ctx, managementBootstrapAttrTypes(), bs)
	if d.HasError() {
		t.Fatalf("build bootstrap: %v", d)
	}
	return v
}

// talosTestData builds a Tinkerbell cluster model with the given bootstrap object and inventory.
func talosTestData(ctx context.Context, t *testing.T, bootstrap types.Object, machines []testMachine) *ClusterResourceModel {
	t.Helper()
	infraVal, _ := types.ObjectValueFrom(ctx, infrastructureAttrTypes(), InfrastructureModel{Provider: types.StringValue("tinkerbell:v0.5.4")})
	mgmtVal, d := types.ObjectValueFrom(ctx, managementAttrTypes(), ManagementModel{
		Kubeconfig: types.StringNull(), SkipInit: types.BoolValue(false), SelfManaged: types.BoolValue(true),
		Namespace: types.StringNull(), Bootstrap: bootstrap,
	})
	if d.HasError() {
		t.Fatalf("build management: %v", d)
	}
	inv := types.ObjectNull(inventoryAttrTypes())
	if machines != nil {
		invVal, d := types.ObjectValueFrom(ctx, inventoryAttrTypes(), InventoryModel{
			Source: types.StringNull(), Machine: buildTestMachineList(ctx, t, machines),
		})
		if d.HasError() {
			t.Fatalf("build inventory: %v", d)
		}
		inv = invVal
	}
	return &ClusterResourceModel{
		Name: types.StringValue("test"), Infrastructure: infraVal, Management: mgmtVal,
		Bootstrap: types.ObjectNull(bootstrapAttrTypes()), ControlPlane: types.ObjectNull(controlPlaneAttrTypes()),
		Workers: types.ObjectNull(workersAttrTypes()), Inventory: inv, Addons: types.ListNull(types.ObjectType{AttrTypes: addonAttrTypes()}),
	}
}

var goodMachine = []testMachine{{hostname: "cp-1", ip: "10.0.0.5", mac: "aa:bb:cc:dd:ee:01", role: "cp", disk: "/dev/sda", bmc: "10.0.1.5"}}

func runValidate(t *testing.T, data *ClusterResourceModel) diag.Diagnostics {
	t.Helper()
	var diags diag.Diagnostics
	validateManagementBootstrap(context.Background(), data, &diags)
	return diags
}

func assertErrorContains(t *testing.T, diags diag.Diagnostics, want string) {
	t.Helper()
	if !diags.HasError() {
		t.Fatalf("expected error containing %q, got none", want)
	}
	for _, d := range diags.Errors() {
		if strings.Contains(d.Detail(), want) || strings.Contains(d.Summary(), want) {
			return
		}
	}
	t.Fatalf("no error contains %q: %v", want, diags)
}

// --- tests ---

func TestValidateManagementBootstrap_KindIsNoop(t *testing.T) {
	ctx := context.Background()
	data := talosTestData(ctx, t, buildTestBootstrap(ctx, t, "kind", "", nil, nil, nil), nil)
	if diags := runValidate(t, data); diags.HasError() {
		t.Fatalf("kind should validate, got %v", diags)
	}
}

func TestValidateManagementBootstrap_NullBootstrapIsNoop(t *testing.T) {
	ctx := context.Background()
	data := talosTestData(ctx, t, types.ObjectNull(managementBootstrapAttrTypes()), nil)
	if diags := runValidate(t, data); diags.HasError() {
		t.Fatalf("null bootstrap should validate, got %v", diags)
	}
}

func TestValidateManagementBootstrap_UnknownType(t *testing.T) {
	ctx := context.Background()
	data := talosTestData(ctx, t, buildTestBootstrap(ctx, t, "vm", "", nil, nil, nil), nil)
	assertErrorContains(t, runValidate(t, data), "not supported")
}

func TestValidateManagementBootstrap_TalosRequiresMachine(t *testing.T) {
	ctx := context.Background()
	data := talosTestData(ctx, t, buildTestBootstrap(ctx, t, "talos", "", nil, testTalosModel(ctx, t, "v1.13.6", nil, nil), nil), goodMachine)
	assertErrorContains(t, runValidate(t, data), "machine is required")
}

func TestValidateManagementBootstrap_TalosMachineMustExist(t *testing.T) {
	ctx := context.Background()
	data := talosTestData(ctx, t, buildTestBootstrap(ctx, t, "talos", "cp-9", nil, testTalosModel(ctx, t, "v1.13.6", nil, nil), nil), goodMachine)
	assertErrorContains(t, runValidate(t, data), "does not match any inventory.machine")
}

func TestValidateManagementBootstrap_TalosMachineNeedsBMCAndDisk(t *testing.T) {
	ctx := context.Background()
	bare := []testMachine{{hostname: "cp-1", ip: "10.0.0.5", mac: "aa:bb:cc:dd:ee:01"}}
	data := talosTestData(ctx, t, buildTestBootstrap(ctx, t, "talos", "cp-1", nil, testTalosModel(ctx, t, "v1.13.6", nil, nil), nil), bare)
	diags := runValidate(t, data)
	assertErrorContains(t, diags, "bmc")
	assertErrorContains(t, diags, "disk.device")
}

func TestValidateManagementBootstrap_TalosRequiresVersion(t *testing.T) {
	ctx := context.Background()
	data := talosTestData(ctx, t, buildTestBootstrap(ctx, t, "talos", "cp-1", nil, nil, nil), goodMachine)
	assertErrorContains(t, runValidate(t, data), "talos.version is required")
}

func TestValidateManagementBootstrap_InvalidBoot(t *testing.T) {
	ctx := context.Background()
	boot := &BootModel{Method: types.StringValue("pxe"), Timeout: types.StringValue("soon"), Attempts: types.Int64Value(0)}
	data := talosTestData(ctx, t, buildTestBootstrap(ctx, t, "talos", "cp-1", boot, testTalosModel(ctx, t, "v1.13.6", nil, nil), nil), goodMachine)
	diags := runValidate(t, data)
	assertErrorContains(t, diags, "boot.method")
	assertErrorContains(t, diags, "boot.timeout")
	assertErrorContains(t, diags, "boot.attempts")
}

func TestValidateManagementBootstrap_InvalidArchitecture(t *testing.T) {
	ctx := context.Background()
	tm := testTalosModel(ctx, t, "v1.13.6", nil, nil)
	tm.Architecture = types.StringValue("riscv64")
	data := talosTestData(ctx, t, buildTestBootstrap(ctx, t, "talos", "cp-1", nil, tm, nil), goodMachine)
	assertErrorContains(t, runValidate(t, data), "architecture")
}

func TestValidateManagementBootstrap_ImageRules(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		img  *TalosImageModel
		want string
	}{
		{"iso without installer", testImageModel(ctx, t, "https://x/iso", "", "", nil), "set together"},
		{"iso and schematic", testImageModel(ctx, t, "https://x/iso", "x/inst:v1", "abc", nil), "conflict"},
		{"schematic and extensions", testImageModel(ctx, t, "", "", "abc", []string{"iscsi-tools"}), "conflicts with"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := talosTestData(ctx, t, buildTestBootstrap(ctx, t, "talos", "cp-1", nil, testTalosModel(ctx, t, "v1.13.6", tc.img, nil), nil), goodMachine)
			assertErrorContains(t, runValidate(t, data), tc.want)
		})
	}
}

func TestValidateManagementBootstrap_InvalidPatchYAML(t *testing.T) {
	ctx := context.Background()
	tm := testTalosModel(ctx, t, "v1.13.6", nil, []string{"machine: [broken"})
	data := talosTestData(ctx, t, buildTestBootstrap(ctx, t, "talos", "cp-1", nil, tm, nil), goodMachine)
	assertErrorContains(t, runValidate(t, data), "config_patches[0]")
}

func TestValidateManagementBootstrap_HelmRules(t *testing.T) {
	ctx := context.Background()
	addons := &BootstrapAddonsModel{
		Helm: testHelmList(ctx, t, []HelmReleaseModel{
			helmRel("cilium", "kube-system", "oci://x", "", ""),
			helmRel("cilium", "kube-system", "oci://y", "later", "a: [b"),
		}),
		Manifests: types.ListNull(types.StringType),
	}
	data := talosTestData(ctx, t, buildTestBootstrap(ctx, t, "talos", "cp-1", nil, testTalosModel(ctx, t, "v1.13.6", nil, nil), addons), goodMachine)
	diags := runValidate(t, data)
	assertErrorContains(t, diags, "duplicate")
	assertErrorContains(t, diags, "timeout")
	assertErrorContains(t, diags, "values")
}

func TestValidateManagementBootstrap_Valid(t *testing.T) {
	ctx := context.Background()
	boot := &BootModel{Method: types.StringValue("auto"), Timeout: types.StringValue("15m"), Attempts: types.Int64Value(3)}
	img := testImageModel(ctx, t, "", "", "", []string{"iscsi-tools"})
	tm := testTalosModel(ctx, t, "v1.13.6", img, []string{"cluster:\n  network:\n    cni:\n      name: none\n"})
	addons := &BootstrapAddonsModel{
		Helm:      testHelmList(ctx, t, []HelmReleaseModel{helmRel("cilium", "kube-system", "oci://quay.io/cilium/charts/cilium", "10m", "kubeProxyReplacement: true\n")}),
		Manifests: types.ListNull(types.StringType),
	}
	data := talosTestData(ctx, t, buildTestBootstrap(ctx, t, "talos", "cp-1", boot, tm, addons), goodMachine)
	if diags := runValidate(t, data); diags.HasError() {
		t.Fatalf("expected valid, got %v", diags)
	}
}

func TestValidateLifecycleConfig_CallsBootstrapValidation(t *testing.T) {
	ctx := context.Background()
	r := &ClusterResource{}
	data := talosTestData(ctx, t, buildTestBootstrap(ctx, t, "talos", "", nil, nil, nil), goodMachine)
	var diags diag.Diagnostics
	r.validateLifecycleConfig(ctx, data, &diags)
	assertErrorContains(t, diags, "machine is required")
}

func TestClusterResource_Schema_ManagementBootstrap(t *testing.T) {
	ctx := context.Background()
	r := NewClusterResource()
	resp := &resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Schema returned errors: %v", resp.Diagnostics)
	}

	mgmt, ok := resp.Schema.Attributes["management"].(schema.SingleNestedAttribute)
	if !ok {
		t.Fatal("management must be a SingleNestedAttribute")
	}
	bs, ok := mgmt.Attributes["bootstrap"].(schema.SingleNestedAttribute)
	if !ok {
		t.Fatal("management.bootstrap must be a SingleNestedAttribute")
	}
	if len(bs.PlanModifiers) != 1 {
		t.Errorf("management.bootstrap should have exactly one plan modifier (RequiresReplace), got %d", len(bs.PlanModifiers))
	}
	for _, name := range []string{"type", "machine", "boot", "talos", "addons"} {
		if _, ok := bs.Attributes[name]; !ok {
			t.Errorf("management.bootstrap missing %q", name)
		}
	}
	talosAttr := bs.Attributes["talos"].(schema.SingleNestedAttribute)
	for _, name := range []string{"version", "architecture", "endpoint", "image", "config_patches"} {
		if _, ok := talosAttr.Attributes[name]; !ok {
			t.Errorf("management.bootstrap.talos missing %q", name)
		}
	}
	addonsAttr := bs.Attributes["addons"].(schema.SingleNestedAttribute)
	if _, ok := addonsAttr.Attributes["helm"].(schema.ListNestedAttribute); !ok {
		t.Error("management.bootstrap.addons.helm must be a ListNestedAttribute")
	}
}

func TestExtractManagementBootstrap_Null(t *testing.T) {
	ctx := context.Background()
	mgmt := &ManagementModel{Bootstrap: types.ObjectNull(managementBootstrapAttrTypes())}
	bs, diags := extractManagementBootstrap(ctx, mgmt)
	if bs != nil || diags.HasError() {
		t.Fatalf("extract null: bs=%v diags=%v", bs, diags)
	}
}

func TestFindInventoryMachine(t *testing.T) {
	ctx := context.Background()
	data := talosTestData(ctx, t, types.ObjectNull(managementBootstrapAttrTypes()), goodMachine)
	m, diags := findInventoryMachine(ctx, data, "cp-1")
	if diags.HasError() || m == nil || m.Hostname.ValueString() != "cp-1" {
		t.Fatalf("findInventoryMachine(cp-1) = %v, %v", m, diags)
	}
	if m, _ := findInventoryMachine(ctx, data, "nope"); m != nil {
		t.Fatal("unknown hostname should return nil")
	}
	noInv := talosTestData(ctx, t, types.ObjectNull(managementBootstrapAttrTypes()), nil)
	if m, _ := findInventoryMachine(ctx, noInv, "cp-1"); m != nil {
		t.Fatal("nil inventory should return nil")
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/provider/ -run 'TestValidateManagementBootstrap|TestClusterResource_Schema_ManagementBootstrap|TestExtractManagementBootstrap|TestFindInventoryMachine' -v`
Expected: FAIL to compile with `unknown field Bootstrap in struct literal` and `undefined: ManagementBootstrapModel`.

- [ ] **Step 4: Add the models, attr types, and extractors**

In `internal/provider/cluster_resource_models.go`:

Add to `ManagementModel`:

```go
	Bootstrap   types.Object `tfsdk:"bootstrap"`
```

Add after `ManagementModel`:

```go
// ManagementBootstrapModel configures the transient bootstrap cluster.
type ManagementBootstrapModel struct {
	Type    types.String `tfsdk:"type"`
	Machine types.String `tfsdk:"machine"`
	Boot    types.Object `tfsdk:"boot"`   // BootModel
	Talos   types.Object `tfsdk:"talos"`  // TalosModel
	Addons  types.Object `tfsdk:"addons"` // BootstrapAddonsModel
}

// BootModel controls how the Talos bootstrap node boots the installer.
type BootModel struct {
	Method   types.String `tfsdk:"method"`
	Timeout  types.String `tfsdk:"timeout"`
	Attempts types.Int64  `tfsdk:"attempts"`
}

// TalosModel controls the Talos version, images, and config patches.
type TalosModel struct {
	Version       types.String `tfsdk:"version"`
	Architecture  types.String `tfsdk:"architecture"`
	Endpoint      types.String `tfsdk:"endpoint"`
	Image         types.Object `tfsdk:"image"` // TalosImageModel
	ConfigPatches types.List   `tfsdk:"config_patches"`
}

// TalosImageModel selects Image Factory artifacts or explicit overrides.
type TalosImageModel struct {
	Factory    types.String `tfsdk:"factory"`
	Schematic  types.String `tfsdk:"schematic"`
	Extensions types.List   `tfsdk:"extensions"`
	KernelArgs types.List   `tfsdk:"kernel_args"`
	ISO        types.String `tfsdk:"iso"`
	Installer  types.String `tfsdk:"installer"`
}

// BootstrapAddonsModel lists Helm releases and manifests for the bootstrap cluster.
type BootstrapAddonsModel struct {
	Helm      types.List `tfsdk:"helm"` // []HelmReleaseModel
	Manifests types.List `tfsdk:"manifests"`
}

// HelmReleaseModel describes one Helm release.
type HelmReleaseModel struct {
	Name       types.String `tfsdk:"name"`
	Namespace  types.String `tfsdk:"namespace"`
	Chart      types.String `tfsdk:"chart"`
	Repository types.String `tfsdk:"repository"`
	Version    types.String `tfsdk:"version"`
	Values     types.String `tfsdk:"values"`
	Timeout    types.String `tfsdk:"timeout"`
}
```

Change `managementAttrTypes()` to:

```go
func managementAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"kubeconfig":   types.StringType,
		"skip_init":    types.BoolType,
		"self_managed": types.BoolType,
		"namespace":    types.StringType,
		"bootstrap":    types.ObjectType{AttrTypes: managementBootstrapAttrTypes()},
	}
}

func managementBootstrapAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"type":    types.StringType,
		"machine": types.StringType,
		"boot":    types.ObjectType{AttrTypes: bootAttrTypes()},
		"talos":   types.ObjectType{AttrTypes: talosAttrTypes()},
		"addons":  types.ObjectType{AttrTypes: bootstrapAddonsAttrTypes()},
	}
}

func bootAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"method":   types.StringType,
		"timeout":  types.StringType,
		"attempts": types.Int64Type,
	}
}

func talosAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"version":        types.StringType,
		"architecture":   types.StringType,
		"endpoint":       types.StringType,
		"image":          types.ObjectType{AttrTypes: talosImageAttrTypes()},
		"config_patches": types.ListType{ElemType: types.StringType},
	}
}

func talosImageAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"factory":     types.StringType,
		"schematic":   types.StringType,
		"extensions":  types.ListType{ElemType: types.StringType},
		"kernel_args": types.ListType{ElemType: types.StringType},
		"iso":         types.StringType,
		"installer":   types.StringType,
	}
}

func bootstrapAddonsAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"helm":      types.ListType{ElemType: types.ObjectType{AttrTypes: helmReleaseAttrTypes()}},
		"manifests": types.ListType{ElemType: types.StringType},
	}
}

func helmReleaseAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"name":       types.StringType,
		"namespace":  types.StringType,
		"chart":      types.StringType,
		"repository": types.StringType,
		"version":    types.StringType,
		"values":     types.StringType,
		"timeout":    types.StringType,
	}
}
```

Add after `extractManagement`:

```go
func extractManagementBootstrap(ctx context.Context, mgmt *ManagementModel) (*ManagementBootstrapModel, diag.Diagnostics) {
	if mgmt == nil || mgmt.Bootstrap.IsNull() || mgmt.Bootstrap.IsUnknown() {
		return nil, nil
	}
	var bs ManagementBootstrapModel
	diags := mgmt.Bootstrap.As(ctx, &bs, basetypes.ObjectAsOptions{})
	return &bs, diags
}

func extractBoot(ctx context.Context, bs *ManagementBootstrapModel) (*BootModel, diag.Diagnostics) {
	if bs == nil || bs.Boot.IsNull() || bs.Boot.IsUnknown() {
		return nil, nil
	}
	var b BootModel
	diags := bs.Boot.As(ctx, &b, basetypes.ObjectAsOptions{})
	return &b, diags
}

func extractTalos(ctx context.Context, bs *ManagementBootstrapModel) (*TalosModel, diag.Diagnostics) {
	if bs == nil || bs.Talos.IsNull() || bs.Talos.IsUnknown() {
		return nil, nil
	}
	var tm TalosModel
	diags := bs.Talos.As(ctx, &tm, basetypes.ObjectAsOptions{})
	return &tm, diags
}

func extractTalosImage(ctx context.Context, tm *TalosModel) (*TalosImageModel, diag.Diagnostics) {
	if tm == nil || tm.Image.IsNull() || tm.Image.IsUnknown() {
		return nil, nil
	}
	var img TalosImageModel
	diags := tm.Image.As(ctx, &img, basetypes.ObjectAsOptions{})
	return &img, diags
}

func extractBootstrapAddons(ctx context.Context, bs *ManagementBootstrapModel) (*BootstrapAddonsModel, diag.Diagnostics) {
	if bs == nil || bs.Addons.IsNull() || bs.Addons.IsUnknown() {
		return nil, nil
	}
	var ad BootstrapAddonsModel
	diags := bs.Addons.As(ctx, &ad, basetypes.ObjectAsOptions{})
	return &ad, diags
}

// findInventoryMachine returns the inventory machine with the given hostname, or nil.
func findInventoryMachine(ctx context.Context, data *ClusterResourceModel, hostname string) (*MachineModel, diag.Diagnostics) {
	inv, diags := extractInventory(ctx, data)
	if inv == nil || inv.Machine.IsNull() || inv.Machine.IsUnknown() {
		return nil, diags
	}
	var machines []MachineModel
	diags.Append(inv.Machine.ElementsAs(ctx, &machines, false)...)
	if diags.HasError() {
		return nil, diags
	}
	for i := range machines {
		if machines[i].Hostname.ValueString() == hostname {
			return &machines[i], diags
		}
	}
	return nil, diags
}

// stringList converts a list of strings; null or unknown yields nil.
func stringList(ctx context.Context, l types.List) ([]string, diag.Diagnostics) {
	if l.IsNull() || l.IsUnknown() {
		return nil, nil
	}
	var out []string
	diags := l.ElementsAs(ctx, &out, false)
	return out, diags
}
```

- [ ] **Step 5: Set `Bootstrap` wherever `ManagementModel` is constructed**

In `internal/provider/cluster_resource.go`:
- State upgrader (around line 676): add `Bootstrap: types.ObjectNull(managementBootstrapAttrTypes()),` to the `ManagementModel{...}` literal.
- `ensureManagementComputed` (around line 885): add the same field to the literal in the `mgmt == nil` branch. In the `else` branch add, after the namespace fix-up:
  ```go
  		if mgmt.Bootstrap.IsUnknown() {
  			mgmt.Bootstrap = types.ObjectNull(managementBootstrapAttrTypes())
  		}
  ```

In `internal/provider/cluster_resource_models_test.go`, find every `ManagementModel{` literal (`grep -n 'ManagementModel{' internal/provider/cluster_resource_models_test.go`, five sites) and add `Bootstrap: types.ObjectNull(managementBootstrapAttrTypes()),` to each.

Run: `go build ./... && go vet ./internal/provider/`
Expected: builds.

- [ ] **Step 6: Add the schema**

In `internal/provider/cluster_resource.go`, add imports:

```go
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
```

Inside the `"management"` attribute's `Attributes` map, after `"namespace"`, add:

```go
					"bootstrap": schema.SingleNestedAttribute{
						MarkdownDescription: "Transient bootstrap cluster configuration. `type = \"kind\"` (default) creates a kind cluster. `type = \"talos\"` provisions one `inventory.machine` entry as a single-node Talos cluster through its BMC. The bootstrap cluster is torn down after the self-managed pivot and nothing about it is kept in state.",
						Optional:            true,
						PlanModifiers: []planmodifier.Object{
							objectplanmodifier.RequiresReplace(),
						},
						Attributes: map[string]schema.Attribute{
							"type": schema.StringAttribute{
								MarkdownDescription: "Bootstrap cluster type: `kind` or `talos`.",
								Optional:            true,
								Computed:            true,
								Default:             stringdefault.StaticString("kind"),
							},
							"machine": schema.StringAttribute{
								MarkdownDescription: "Hostname of the `inventory.machine` entry to use as the bootstrap node. Its `bmc`, `network.ip_address`, and `disk.device` are used. Required when `type = \"talos\"`.",
								Optional:            true,
							},
							"boot": schema.SingleNestedAttribute{
								MarkdownDescription: "How the node is booted into the Talos installer.",
								Optional:            true,
								Attributes: map[string]schema.Attribute{
									"method": schema.StringAttribute{
										MarkdownDescription: "`auto` (virtual media, then UEFI HTTP boot), `virtual_media`, or `http`.",
										Optional:            true,
										Computed:            true,
										Default:             stringdefault.StaticString("auto"),
									},
									"timeout": schema.StringAttribute{
										MarkdownDescription: "Per-attempt boot and install timeout as a Go duration (e.g. `15m`).",
										Optional:            true,
										Computed:            true,
										Default:             stringdefault.StaticString("15m"),
									},
									"attempts": schema.Int64Attribute{
										MarkdownDescription: "Boot attempts before giving up.",
										Optional:            true,
										Computed:            true,
										Default:             int64default.StaticInt64(3),
									},
								},
							},
							"talos": schema.SingleNestedAttribute{
								MarkdownDescription: "Talos version, images, and machine config patches.",
								Optional:            true,
								Attributes: map[string]schema.Attribute{
									"version": schema.StringAttribute{
										MarkdownDescription: "Talos version (e.g. `v1.13.6`). Required when `type = \"talos\"`.",
										Optional:            true,
									},
									"architecture": schema.StringAttribute{
										MarkdownDescription: "`amd64` or `arm64`.",
										Optional:            true,
										Computed:            true,
										Default:             stringdefault.StaticString("amd64"),
									},
									"endpoint": schema.StringAttribute{
										MarkdownDescription: "Cluster endpoint written into the machine config. Defaults to `https://<machine ip>:6443`.",
										Optional:            true,
									},
									"image": schema.SingleNestedAttribute{
										MarkdownDescription: "Image Factory schematic or explicit image overrides.",
										Optional:            true,
										Attributes: map[string]schema.Attribute{
											"factory": schema.StringAttribute{
												MarkdownDescription: "Image Factory base URL.",
												Optional:            true,
												Computed:            true,
												Default:             stringdefault.StaticString("https://factory.talos.dev"),
											},
											"schematic": schema.StringAttribute{
												MarkdownDescription: "Precomputed schematic id. Conflicts with `extensions`, `kernel_args`, `iso`, and `installer`.",
												Optional:            true,
											},
											"extensions": schema.ListAttribute{
												MarkdownDescription: "Official system extension names for a new schematic.",
												ElementType:         types.StringType,
												Optional:            true,
											},
											"kernel_args": schema.ListAttribute{
												MarkdownDescription: "Extra kernel arguments for a new schematic.",
												ElementType:         types.StringType,
												Optional:            true,
											},
											"iso": schema.StringAttribute{
												MarkdownDescription: "Explicit ISO URL. Must be set together with `installer`; disables UEFI HTTP boot.",
												Optional:            true,
											},
											"installer": schema.StringAttribute{
												MarkdownDescription: "Explicit installer image reference. Must be set together with `iso`.",
												Optional:            true,
											},
										},
									},
									"config_patches": schema.ListAttribute{
										MarkdownDescription: "Machine config patches (YAML strings) applied in order after the built-in install patch.",
										ElementType:         types.StringType,
										Optional:            true,
									},
								},
							},
							"addons": schema.SingleNestedAttribute{
								MarkdownDescription: "Helm releases and manifests installed on the bootstrap cluster before CAPI is initialized. Install a CNI such as Cilium here.",
								Optional:            true,
								Attributes: map[string]schema.Attribute{
									"helm": schema.ListNestedAttribute{
										MarkdownDescription: "Helm releases installed in order.",
										Optional:            true,
										NestedObject: schema.NestedAttributeObject{
											Attributes: map[string]schema.Attribute{
												"name":      schema.StringAttribute{MarkdownDescription: "Release name.", Required: true},
												"namespace": schema.StringAttribute{MarkdownDescription: "Release namespace (created if missing).", Required: true},
												"chart": schema.StringAttribute{
													MarkdownDescription: "`oci://` chart reference, or a chart name used with `repository`.",
													Required:            true,
												},
												"repository": schema.StringAttribute{MarkdownDescription: "HTTP chart repository URL. Ignored for `oci://` charts.", Optional: true},
												"version":    schema.StringAttribute{MarkdownDescription: "Chart version. Latest when empty.", Optional: true},
												"values":     schema.StringAttribute{MarkdownDescription: "Chart values as a YAML string.", Optional: true},
												"timeout": schema.StringAttribute{
													MarkdownDescription: "Install timeout as a Go duration.",
													Optional:            true,
													Computed:            true,
													Default:             stringdefault.StaticString("10m"),
												},
											},
										},
									},
									"manifests": schema.ListAttribute{
										MarkdownDescription: "Raw YAML manifests applied after the Helm releases.",
										ElementType:         types.StringType,
										Optional:            true,
									},
								},
							},
						},
					},
```

- [ ] **Step 7: Add validation**

In `internal/provider/cluster_resource.go`, add imports `"time"` and `"sigs.k8s.io/yaml"` (both already in go.mod). In `validateLifecycleConfig`, immediately before `validateAddons(ctx, data, diags)`, add:

```go
	// Validate bootstrap cluster configuration
	validateManagementBootstrap(ctx, data, diags)
```

Then add the function after `validateLifecycleConfig`:

```go
// validateManagementBootstrap checks management.bootstrap when type = "talos".
func validateManagementBootstrap(ctx context.Context, data *ClusterResourceModel, diags *diag.Diagnostics) {
	const summary = "Invalid bootstrap configuration"

	mgmt, d := extractManagement(ctx, data)
	diags.Append(d...)
	bs, d := extractManagementBootstrap(ctx, mgmt)
	diags.Append(d...)
	if bs == nil {
		return
	}

	typ := "kind"
	if !bs.Type.IsNull() && !bs.Type.IsUnknown() {
		typ = bs.Type.ValueString()
	}
	switch typ {
	case "kind":
		return
	case "talos":
	default:
		diags.AddError(summary, fmt.Sprintf("management.bootstrap.type %q is not supported. Supported: kind, talos.", typ))
		return
	}

	if bs.Machine.IsNull() || bs.Machine.ValueString() == "" {
		diags.AddError(summary, "management.bootstrap.machine is required when type = \"talos\".")
	} else {
		m, d := findInventoryMachine(ctx, data, bs.Machine.ValueString())
		diags.Append(d...)
		if m == nil {
			diags.AddError(summary, fmt.Sprintf("management.bootstrap.machine %q does not match any inventory.machine hostname.", bs.Machine.ValueString()))
		} else {
			if m.BMC.IsNull() || m.BMC.IsUnknown() {
				diags.AddError(summary, fmt.Sprintf("inventory machine %q must define bmc to be used as the bootstrap node.", bs.Machine.ValueString()))
			}
			if m.Disk.IsNull() || m.Disk.IsUnknown() {
				diags.AddError(summary, fmt.Sprintf("inventory machine %q must define disk.device to be used as the bootstrap node.", bs.Machine.ValueString()))
			}
			var n NetworkModel
			diags.Append(m.Network.As(ctx, &n, basetypes.ObjectAsOptions{})...)
			if n.IPAddress.IsNull() || n.IPAddress.ValueString() == "" {
				diags.AddError(summary, fmt.Sprintf("inventory machine %q must define network.ip_address to be used as the bootstrap node.", bs.Machine.ValueString()))
			}
		}
	}

	boot, d := extractBoot(ctx, bs)
	diags.Append(d...)
	if boot != nil {
		if !boot.Method.IsNull() && !boot.Method.IsUnknown() {
			switch boot.Method.ValueString() {
			case "auto", "virtual_media", "http":
			default:
				diags.AddError(summary, fmt.Sprintf("management.bootstrap.boot.method %q is not supported. Supported: auto, virtual_media, http.", boot.Method.ValueString()))
			}
		}
		if !boot.Timeout.IsNull() && !boot.Timeout.IsUnknown() {
			if _, err := time.ParseDuration(boot.Timeout.ValueString()); err != nil {
				diags.AddError(summary, fmt.Sprintf("management.bootstrap.boot.timeout %q is not a valid duration: %v", boot.Timeout.ValueString(), err))
			}
		}
		if !boot.Attempts.IsNull() && !boot.Attempts.IsUnknown() && boot.Attempts.ValueInt64() < 1 {
			diags.AddError(summary, "management.bootstrap.boot.attempts must be at least 1.")
		}
	}

	tm, d := extractTalos(ctx, bs)
	diags.Append(d...)
	if tm == nil || tm.Version.IsNull() || tm.Version.ValueString() == "" {
		diags.AddError(summary, "management.bootstrap.talos.version is required when type = \"talos\".")
	}
	if tm != nil {
		if !tm.Architecture.IsNull() && !tm.Architecture.IsUnknown() {
			switch tm.Architecture.ValueString() {
			case "amd64", "arm64":
			default:
				diags.AddError(summary, fmt.Sprintf("management.bootstrap.talos.architecture %q is not supported. Supported: amd64, arm64.", tm.Architecture.ValueString()))
			}
		}

		img, d := extractTalosImage(ctx, tm)
		diags.Append(d...)
		if img != nil {
			set := func(s types.String) bool { return !s.IsNull() && !s.IsUnknown() && s.ValueString() != "" }
			nonEmpty := func(l types.List) bool { return !l.IsNull() && !l.IsUnknown() && len(l.Elements()) > 0 }
			hasISO, hasInstaller := set(img.ISO), set(img.Installer)
			hasSchematic := set(img.Schematic)
			hasCustom := nonEmpty(img.Extensions) || nonEmpty(img.KernelArgs)
			if hasISO != hasInstaller {
				diags.AddError(summary, "management.bootstrap.talos.image.iso and installer must be set together.")
			}
			if hasISO && (hasSchematic || hasCustom) {
				diags.AddError(summary, "management.bootstrap.talos.image.iso/installer conflict with schematic, extensions, and kernel_args.")
			}
			if hasSchematic && hasCustom {
				diags.AddError(summary, "management.bootstrap.talos.image.schematic conflicts with extensions and kernel_args.")
			}
		}

		patches, d := stringList(ctx, tm.ConfigPatches)
		diags.Append(d...)
		for i, p := range patches {
			var v any
			if err := yaml.Unmarshal([]byte(p), &v); err != nil {
				diags.AddError(summary, fmt.Sprintf("management.bootstrap.talos.config_patches[%d] is not valid YAML: %v", i, err))
			}
		}
	}

	ad, d := extractBootstrapAddons(ctx, bs)
	diags.Append(d...)
	if ad != nil {
		var rels []HelmReleaseModel
		if !ad.Helm.IsNull() && !ad.Helm.IsUnknown() {
			diags.Append(ad.Helm.ElementsAs(ctx, &rels, false)...)
		}
		seen := map[string]bool{}
		for i, rel := range rels {
			key := rel.Namespace.ValueString() + "/" + rel.Name.ValueString()
			if seen[key] {
				diags.AddError(summary, fmt.Sprintf("management.bootstrap.addons.helm[%d] is a duplicate release %q.", i, key))
			}
			seen[key] = true
			if !rel.Timeout.IsNull() && !rel.Timeout.IsUnknown() && rel.Timeout.ValueString() != "" {
				if _, err := time.ParseDuration(rel.Timeout.ValueString()); err != nil {
					diags.AddError(summary, fmt.Sprintf("management.bootstrap.addons.helm[%d].timeout %q is not a valid duration: %v", i, rel.Timeout.ValueString(), err))
				}
			}
			if !rel.Values.IsNull() && !rel.Values.IsUnknown() && rel.Values.ValueString() != "" {
				var v any
				if err := yaml.Unmarshal([]byte(rel.Values.ValueString()), &v); err != nil {
					diags.AddError(summary, fmt.Sprintf("management.bootstrap.addons.helm[%d].values is not valid YAML: %v", i, err))
				}
			}
		}
	}
}
```

- [ ] **Step 8: Run the provider tests**

Run: `go test ./internal/provider/ -v -run 'TestValidateManagementBootstrap|TestValidateLifecycleConfig|TestClusterResource_Schema|TestExtractManagementBootstrap|TestFindInventoryMachine'`
Expected: PASS, including the pre-existing `TestValidateLifecycleConfig_*` tests (they now carry a null `Bootstrap`).

Then the whole package: `go test ./internal/provider/`
Expected: `ok`.

- [ ] **Step 9: Lint and commit**

Run: `gofmt -s -l internal/provider/ && golangci-lint run ./internal/provider/`

```bash
git add internal/provider/cluster_resource.go internal/provider/cluster_resource_models.go internal/provider/cluster_resource_models_test.go internal/provider/bootstrap_validation_test.go
git commit -m "Add management.bootstrap schema, models, and validation"
```

---

### Task 12: Wire the Talos bootstrapper into the resource

**Files:**
- Create: `internal/provider/talos_bootstrap.go`
- Modify: `internal/provider/cluster_resource.go` (`Create` at line 495, `Update` at line 586, `Delete` around line 628)
- Test: `internal/provider/talos_bootstrap_test.go`

**Interfaces:**
- Consumes: `talos.Config`, `talos.New`, `talos.WithLogger`, `capi.NewManager`, `capi.WithBootstrapper`, `capi.WithLogger`, and the extractors from Task 11.
- Produces:
  ```go
  func buildTalosBootstrapConfig(ctx context.Context, data *ClusterResourceModel) (*talos.Config, diag.Diagnostics) // nil when type != talos
  func (r *ClusterResource) managerFor(ctx context.Context, data *ClusterResourceModel) (*capi.Manager, diag.Diagnostics)
  ```

- [ ] **Step 1: Write the failing test**

`internal/provider/talos_bootstrap_test.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/tinkerbell-community/terraform-provider-capi/internal/capi"
	"github.com/tinkerbell-community/terraform-provider-capi/internal/capi/talos"
)

func TestBuildTalosBootstrapConfig_NilForKind(t *testing.T) {
	ctx := context.Background()
	data := talosTestData(ctx, t, buildTestBootstrap(ctx, t, "kind", "", nil, nil, nil), nil)
	cfg, diags := buildTalosBootstrapConfig(ctx, data)
	if cfg != nil || diags.HasError() {
		t.Fatalf("expected nil config for kind, got %+v, %v", cfg, diags)
	}
	data = talosTestData(ctx, t, types.ObjectNull(managementBootstrapAttrTypes()), nil)
	if cfg, _ := buildTalosBootstrapConfig(ctx, data); cfg != nil {
		t.Fatal("expected nil config for null bootstrap")
	}
}

func TestBuildTalosBootstrapConfig_Full(t *testing.T) {
	ctx := context.Background()
	boot := &BootModel{Method: types.StringValue("http"), Timeout: types.StringValue("20m"), Attempts: types.Int64Value(5)}
	img := testImageModel(ctx, t, "", "", "", []string{"iscsi-tools", "nvme-cli"})
	tm := testTalosModel(ctx, t, "v1.13.6", img, []string{"a: 1\n", "b: 2\n"})
	tm.Architecture = types.StringValue("arm64")
	tm.Endpoint = types.StringValue("https://10.1.1.100:6443")
	manifests, _ := types.ListValueFrom(ctx, types.StringType, []string{"kind: Namespace\n"})
	addons := &BootstrapAddonsModel{
		Helm:      testHelmList(ctx, t, []HelmReleaseModel{helmRel("cilium", "kube-system", "oci://quay.io/cilium/charts/cilium", "12m", "k: v\n")}),
		Manifests: manifests,
	}
	data := talosTestData(ctx, t, buildTestBootstrap(ctx, t, "talos", "cp-1", boot, tm, addons), goodMachine)

	cfg, diags := buildTalosBootstrapConfig(ctx, data)
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}
	if cfg == nil {
		t.Fatal("expected config")
	}
	if cfg.Machine.Hostname != "cp-1" || cfg.Machine.IP != "10.0.0.5" || cfg.Machine.Disk != "/dev/sda" {
		t.Errorf("machine = %+v", cfg.Machine)
	}
	if cfg.Machine.BMC != (talos.BMCCredentials{Address: "10.0.1.5", Username: "admin", Password: "secret"}) {
		t.Errorf("bmc = %+v", cfg.Machine.BMC)
	}
	if cfg.Boot.Method != talos.BootMethodHTTP || cfg.Boot.Timeout != 20*time.Minute || cfg.Boot.Attempts != 5 {
		t.Errorf("boot = %+v", cfg.Boot)
	}
	if cfg.Talos.Version != "v1.13.6" || cfg.Talos.Architecture != "arm64" || cfg.Talos.Endpoint != "https://10.1.1.100:6443" {
		t.Errorf("talos = %+v", cfg.Talos)
	}
	if len(cfg.Talos.Image.Extensions) != 2 || cfg.Talos.Image.Factory != "https://factory.talos.dev" {
		t.Errorf("image = %+v", cfg.Talos.Image)
	}
	if len(cfg.Talos.ConfigPatches) != 2 || cfg.Talos.ConfigPatches[1] != "b: 2\n" {
		t.Errorf("patches = %v", cfg.Talos.ConfigPatches)
	}
	if len(cfg.Addons.Helm) != 1 || cfg.Addons.Helm[0].Timeout != 12*time.Minute || cfg.Addons.Helm[0].Values != "k: v\n" {
		t.Errorf("helm = %+v", cfg.Addons.Helm)
	}
	if len(cfg.Addons.Manifests) != 1 {
		t.Errorf("manifests = %v", cfg.Addons.Manifests)
	}
}

func TestManagerFor(t *testing.T) {
	ctx := context.Background()
	def := capi.NewManager()
	r := &ClusterResource{manager: def}

	kind := talosTestData(ctx, t, buildTestBootstrap(ctx, t, "kind", "", nil, nil, nil), nil)
	mgr, diags := r.managerFor(ctx, kind)
	if diags.HasError() || mgr != def {
		t.Fatalf("kind should return the default manager, got %p vs %p, %v", mgr, def, diags)
	}

	tal := talosTestData(ctx, t, buildTestBootstrap(ctx, t, "talos", "cp-1", nil, testTalosModel(ctx, t, "v1.13.6", nil, nil), nil), goodMachine)
	mgr, diags = r.managerFor(ctx, tal)
	if diags.HasError() {
		t.Fatalf("diags: %v", diags)
	}
	if mgr == nil || mgr == def {
		t.Fatal("talos should return a new manager")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/provider/ -run 'TestBuildTalosBootstrapConfig|TestManagerFor' -v`
Expected: FAIL to compile with `undefined: buildTalosBootstrapConfig`.

- [ ] **Step 3: Write talos_bootstrap.go**

`internal/provider/talos_bootstrap.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/tinkerbell-community/terraform-provider-capi/internal/capi"
	"github.com/tinkerbell-community/terraform-provider-capi/internal/capi/talos"
)

const defaultHelmTimeout = 10 * time.Minute

// buildTalosBootstrapConfig assembles the Talos bootstrapper configuration
// from management.bootstrap and the referenced inventory machine. It returns
// nil when the bootstrap type is not "talos". Field validity is checked by
// validateManagementBootstrap, which runs first.
func buildTalosBootstrapConfig(ctx context.Context, data *ClusterResourceModel) (*talos.Config, diag.Diagnostics) {
	var diags diag.Diagnostics

	mgmt, d := extractManagement(ctx, data)
	diags.Append(d...)
	bs, d := extractManagementBootstrap(ctx, mgmt)
	diags.Append(d...)
	if bs == nil || bs.Type.IsNull() || bs.Type.IsUnknown() || bs.Type.ValueString() != "talos" {
		return nil, diags
	}

	m, d := findInventoryMachine(ctx, data, bs.Machine.ValueString())
	diags.Append(d...)
	if m == nil {
		diags.AddError("Invalid bootstrap configuration", fmt.Sprintf("management.bootstrap.machine %q not found in inventory.", bs.Machine.ValueString()))
		return nil, diags
	}
	var network NetworkModel
	var disk DiskModel
	var bmc BMCModel
	diags.Append(m.Network.As(ctx, &network, basetypes.ObjectAsOptions{})...)
	diags.Append(m.Disk.As(ctx, &disk, basetypes.ObjectAsOptions{})...)
	diags.Append(m.BMC.As(ctx, &bmc, basetypes.ObjectAsOptions{})...)
	if diags.HasError() {
		return nil, diags
	}

	cfg := &talos.Config{
		Machine: talos.MachineConfig{
			Hostname: m.Hostname.ValueString(),
			IP:       network.IPAddress.ValueString(),
			Disk:     disk.Device.ValueString(),
			BMC: talos.BMCCredentials{
				Address:  bmc.Address.ValueString(),
				Username: bmc.Username.ValueString(),
				Password: bmc.Password.ValueString(),
			},
		},
	}

	boot, d := extractBoot(ctx, bs)
	diags.Append(d...)
	if boot != nil {
		cfg.Boot.Method = talos.BootMethod(boot.Method.ValueString())
		if s := boot.Timeout.ValueString(); s != "" {
			if dur, err := time.ParseDuration(s); err == nil {
				cfg.Boot.Timeout = dur
			}
		}
		if !boot.Attempts.IsNull() && !boot.Attempts.IsUnknown() {
			cfg.Boot.Attempts = int(boot.Attempts.ValueInt64())
		}
	}

	tm, d := extractTalos(ctx, bs)
	diags.Append(d...)
	if tm != nil {
		cfg.Talos.Version = tm.Version.ValueString()
		cfg.Talos.Architecture = tm.Architecture.ValueString()
		cfg.Talos.Endpoint = tm.Endpoint.ValueString()
		patches, d := stringList(ctx, tm.ConfigPatches)
		diags.Append(d...)
		cfg.Talos.ConfigPatches = patches

		img, d := extractTalosImage(ctx, tm)
		diags.Append(d...)
		if img != nil {
			cfg.Talos.Image = talos.ImageSpec{
				Factory:   img.Factory.ValueString(),
				Schematic: img.Schematic.ValueString(),
				ISO:       img.ISO.ValueString(),
				Installer: img.Installer.ValueString(),
			}
			cfg.Talos.Image.Extensions, d = stringList(ctx, img.Extensions)
			diags.Append(d...)
			cfg.Talos.Image.KernelArgs, d = stringList(ctx, img.KernelArgs)
			diags.Append(d...)
		}
	}

	ad, d := extractBootstrapAddons(ctx, bs)
	diags.Append(d...)
	if ad != nil {
		var rels []HelmReleaseModel
		if !ad.Helm.IsNull() && !ad.Helm.IsUnknown() {
			diags.Append(ad.Helm.ElementsAs(ctx, &rels, false)...)
		}
		for _, rel := range rels {
			timeout := defaultHelmTimeout
			if s := rel.Timeout.ValueString(); s != "" {
				if dur, err := time.ParseDuration(s); err == nil {
					timeout = dur
				}
			}
			cfg.Addons.Helm = append(cfg.Addons.Helm, talos.HelmRelease{
				Name:       rel.Name.ValueString(),
				Namespace:  rel.Namespace.ValueString(),
				Chart:      rel.Chart.ValueString(),
				Repository: rel.Repository.ValueString(),
				Version:    rel.Version.ValueString(),
				Values:     rel.Values.ValueString(),
				Timeout:    timeout,
			})
		}
		cfg.Addons.Manifests, d = stringList(ctx, ad.Manifests)
		diags.Append(d...)
	}

	return cfg, diags
}

// managerFor returns the resource's default manager, or one wired with the
// Talos BMC bootstrapper when management.bootstrap.type = "talos".
func (r *ClusterResource) managerFor(ctx context.Context, data *ClusterResourceModel) (*capi.Manager, diag.Diagnostics) {
	cfg, diags := buildTalosBootstrapConfig(ctx, data)
	if diags.HasError() || cfg == nil {
		return r.manager, diags
	}
	logger := log.New(os.Stderr, "[capi-tf] ", log.LstdFlags)
	return capi.NewManager(
		capi.WithLogger(logger),
		capi.WithBootstrapper(talos.New(*cfg, talos.WithLogger(logger))),
	), diags
}
```

- [ ] **Step 4: Use `managerFor` in Create and Update, and guard Delete**

In `internal/provider/cluster_resource.go`:

`Create` (line 495), replace
```go
	result, err := r.manager.CreateCluster(ctx, *createOpts)
```
with
```go
	mgr, d := r.managerFor(ctx, &data)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	result, err := mgr.CreateCluster(ctx, *createOpts)
```

`Update` (line 586), replace
```go
	result, err := r.manager.CreateCluster(ctx, *reconcileOpts)
```
with
```go
	mgr, d := r.managerFor(ctx, &plan)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	result, err := mgr.CreateCluster(ctx, *reconcileOpts)
```

`Delete`, after the block that sets `deleteOpts.DeleteBootstrap = true`, add:
```go
	// A Talos bootstrap node is reset and released right after the pivot, and
	// CAPT may have reclaimed it into the workload cluster since. Never touch
	// it on destroy.
	if talosCfg, _ := buildTalosBootstrapConfig(ctx, &data); talosCfg != nil {
		deleteOpts.DeleteBootstrap = false
	}
```

- [ ] **Step 5: Run the tests, build, lint**

Run: `go test ./internal/provider/ -run 'TestBuildTalosBootstrapConfig|TestManagerFor' -v && go build ./... && go test ./... && golangci-lint run ./...`
Expected: PASS; build ok; every package `ok`; no findings.

- [ ] **Step 6: Commit**

```bash
git add internal/provider/talos_bootstrap.go internal/provider/talos_bootstrap_test.go internal/provider/cluster_resource.go
git commit -m "Select the Talos BMC bootstrapper from management.bootstrap"
```

---

### Task 13: Example, generated docs, and CLAUDE.md

**Files:**
- Create: `examples/resources/capi_cluster/talos-bootstrap.tf`
- Create: `examples/resources/capi_cluster/cni-none.yaml`
- Modify: `.claude/CLAUDE.md` (section 3.2)
- Regenerate: `docs/resources/cluster.md`
- Modify: `docs/superpowers/specs/2026-09-12-talos-bmc-bootstrapper-design.md` (section 3.4, one line)

- [ ] **Step 1: Write the example**

`examples/resources/capi_cluster/cni-none.yaml`:

```yaml
# Talos ships with Flannel and kube-proxy. Disable both so the Cilium Helm
# release below owns networking, with kube-proxy replacement via KubePrism.
cluster:
  network:
    cni:
      name: none
  proxy:
    disabled: true
machine:
  features:
    kubePrism:
      enabled: true
      port: 7445
```

`examples/resources/capi_cluster/talos-bootstrap.tf`:

```hcl
# A Tinkerbell cluster whose bootstrap (management) cluster is a single
# bare-metal machine provisioned with Talos through its BMC instead of kind.
# After the self-managed pivot the node is reset, powered off, and free for
# CAPT to reclaim into the workload cluster.
resource "capi_cluster" "bare_metal" {
  name               = "bm-cluster"
  kubernetes_version = "v1.34.0"

  infrastructure {
    provider = "tinkerbell:v0.5.4"
  }

  bootstrap {
    provider = "talos:v0.6.7"
  }

  control_plane {
    provider      = "talos:v0.6.7"
    machine_count = 3
  }

  workers {
    machine_count = 2
  }

  management {
    self_managed = true

    bootstrap {
      type    = "talos"
      machine = "cp-1"

      boot {
        method   = "auto" # virtual media first, then UEFI HTTP boot
        timeout  = "15m"
        attempts = 3
      }

      talos {
        version      = "v1.13.6"
        architecture = "amd64"

        image {
          extensions  = ["siderolabs/iscsi-tools"]
          kernel_args = ["net.ifnames=0"]
        }

        config_patches = [file("${path.module}/cni-none.yaml")]
      }

      addons {
        helm = [
          {
            name      = "cilium"
            namespace = "kube-system"
            chart     = "oci://quay.io/cilium/charts/cilium"
            version   = "1.18.0"
            values = yamlencode({
              kubeProxyReplacement = true
              k8sServiceHost       = "localhost"
              k8sServicePort       = 7445
              ipam                 = { mode = "kubernetes" }
            })
          }
        ]
      }
    }
  }

  inventory {
    machine = [
      {
        hostname = "cp-1"
        network = {
          ip_address  = "192.168.1.10"
          netmask     = "255.255.255.0"
          gateway     = "192.168.1.1"
          mac_address = "aa:bb:cc:dd:ee:01"
        }
        disk = { device = "/dev/nvme0n1" }
        bmc = {
          address  = "192.168.2.10"
          username = "admin"
          password = var.bmc_password
        }
        labels = { type = "cp" }
      },
    ]
  }
}

variable "bmc_password" {
  type      = string
  sensitive = true
}
```

Run: `terraform fmt -check examples/resources/capi_cluster/`
Expected: no output (formatted).

- [ ] **Step 2: Update CLAUDE.md**

In `.claude/CLAUDE.md`, section 3.2 (`management`), append after the Mutability list:

````markdown
**`management.bootstrap` — transient bootstrap cluster:**

```go
"bootstrap": schema.SingleNestedAttribute{
    Optional: true,
    PlanModifiers: []planmodifier.Object{objectplanmodifier.RequiresReplace()},
    Attributes: map[string]schema.Attribute{
        "type":    // "kind" (default) | "talos"
        "machine": // inventory.machine[].hostname; required for talos
        "boot":    // { method: auto|virtual_media|http, timeout: "15m", attempts: 3 }
        "talos":   // { version, architecture, endpoint, image { factory, schematic, extensions, kernel_args, iso, installer }, config_patches }
        "addons":  // { helm [ { name, namespace, chart, repository, version, values, timeout } ], manifests }
    },
},
```

The Talos bootstrapper (`internal/capi/talos`) implements `capi.Bootstrapper`. It is an
observed-state reconciler over bmclib and the Talos machinery API; nothing about the node
is stored in Terraform state. See `docs/superpowers/specs/2026-09-12-talos-bmc-bootstrapper-design.md`.
````

- [ ] **Step 3: Confirm the spec's status wording**

Confirm `docs/superpowers/specs/2026-09-12-talos-bmc-bootstrapper-design.md` section 3.4 says
"`status.bootstrap_cluster` holds the manager's bootstrap cluster name (`<name>-bootstrap`) exactly as it does for kind, and is null after the pivot deletes the bootstrap cluster." If it still mentions the machine hostname, replace that sentence.

- [ ] **Step 4: Regenerate the docs**

Run: `make generate && git diff --stat docs/`
Expected: `docs/resources/cluster.md` changes and now documents `management.bootstrap` with its nested attributes. If `make generate` fails because `tfplugindocs` cannot find the example, confirm the file lives under `examples/resources/capi_cluster/`.

- [ ] **Step 5: Commit**

```bash
git add examples/resources/capi_cluster/talos-bootstrap.tf examples/resources/capi_cluster/cni-none.yaml .claude/CLAUDE.md docs/ 
git commit -m "Document and exemplify the Talos BMC bootstrap cluster"
```

---

### Task 14: Env-gated acceptance test and final verification

**Files:**
- Create: `internal/capi/talos/acceptance_test.go`

- [ ] **Step 1: Write the acceptance test**

`internal/capi/talos/acceptance_test.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"context"
	"log"
	"os"
	"testing"
	"time"

	"github.com/tinkerbell-community/terraform-provider-capi/internal/capi"
)

// TestAcceptance_TalosBootstrap provisions and tears down a real machine.
// It runs only when TF_ACC and CAPI_TALOS_BMC_ADDRESS are set:
//
//	TF_ACC=1 CAPI_TALOS_BMC_ADDRESS=10.0.1.5 CAPI_TALOS_BMC_USERNAME=admin \
//	CAPI_TALOS_BMC_PASSWORD=... CAPI_TALOS_NODE_IP=10.0.0.5 CAPI_TALOS_DISK=/dev/sda \
//	CAPI_TALOS_VERSION=v1.13.6 CAPI_TALOS_ARCH=amd64 \
//	go test ./internal/capi/talos/ -run TestAcceptance -v -timeout 120m
func TestAcceptance_TalosBootstrap(t *testing.T) {
	if os.Getenv("TF_ACC") == "" || os.Getenv("CAPI_TALOS_BMC_ADDRESS") == "" {
		t.Skip("set TF_ACC and CAPI_TALOS_* to run against real hardware")
	}
	env := func(key, def string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return def
	}

	cfg := Config{
		Machine: MachineConfig{
			Hostname: "acceptance",
			IP:       os.Getenv("CAPI_TALOS_NODE_IP"),
			Disk:     env("CAPI_TALOS_DISK", "/dev/sda"),
			BMC: BMCCredentials{
				Address:  os.Getenv("CAPI_TALOS_BMC_ADDRESS"),
				Username: os.Getenv("CAPI_TALOS_BMC_USERNAME"),
				Password: os.Getenv("CAPI_TALOS_BMC_PASSWORD"),
			},
		},
		Boot:  BootConfig{Method: BootMethod(env("CAPI_TALOS_BOOT_METHOD", "auto"))},
		Talos: TalosConfig{Version: env("CAPI_TALOS_VERSION", "v1.13.6"), Architecture: env("CAPI_TALOS_ARCH", "amd64")},
	}
	b := New(cfg, WithLogger(log.New(os.Stderr, "[acceptance] ", log.LstdFlags)))

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Minute)
	defer cancel()

	cluster, err := b.Create(ctx, capi.BootstrapOptions{Name: "acc-bootstrap"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	t.Logf("bootstrap cluster ready, kubeconfig at %s", cluster.KubeconfigPath)

	if err := b.Delete(ctx, cluster); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
}
```

- [ ] **Step 2: Confirm it skips by default**

Run: `go test ./internal/capi/talos/ -run TestAcceptance -v`
Expected: `--- SKIP: TestAcceptance_TalosBootstrap`.

- [ ] **Step 3: Full verification**

Run, in order:

```bash
gofmt -s -l .
go vet ./...
golangci-lint run
go test -v -cover -timeout=120s -parallel=10 ./...
go build ./...
```

Expected: gofmt prints nothing; vet, lint clean; every package `ok`; build succeeds.

- [ ] **Step 4: Commit**

```bash
git add internal/capi/talos/acceptance_test.go
git commit -m "Add env-gated acceptance test for the Talos BMC bootstrapper"
```

---

## Plan self-review notes

- **Spec coverage:** §3 schema → Task 11; §3.2 validation → Task 11; §3.3 models → Task 11; §4 wiring → Task 12; §5 layout → Tasks 1–9; §6 interfaces → Tasks 3–5, 8; §7 observation → Task 1, 9; §8 planner → Task 1; §9 actions → Task 9; §10 config → Task 7; §11 images → Task 6; §12 addons → Task 8; §13 delete/exists → Task 9; §14 fault rules → Tasks 2, 3, 9; §15 errors → Task 1; §16 testing → every task plus Task 10 and 14; §17 dependencies → Tasks 3, 4, 8; §18 docs → Task 13; §19 out of scope → nothing planned for it.
- **Deviation from spec, recorded in Task 13:** `status.bootstrap_cluster` keeps the manager's `<name>-bootstrap` naming rather than the machine hostname, because the manager already sets it and the value is null after the pivot anyway.
- **Type consistency check:** `BMC`, `Node`, `Kube`, `KubeFactory`, `ImageResolver`, `AddonInstaller`, `HelmClient` names match between their defining tasks (3, 4, 5, 6, 8) and their consumers (9, 12). `Timeouts` fields used in `sim_test.go`'s `fastTimeouts()` match `reconciler.go`. `PlanInput` fields match between `planner.go` and `reconciler.run`. `capi.BootstrapError{ClusterName, Operation, Err}` matches `internal/capi/errors.go`.
