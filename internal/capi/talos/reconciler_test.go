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
