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
	// Installed is set once the node has been observed as ours (a successful
	// install). A node that is later briefly unreachable is then treated as
	// rebooting (the post-install kexec) rather than re-imaged.
	Installed bool
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
	ActionAwaitNode      ActionKind = "await-node"
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
