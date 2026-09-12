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
