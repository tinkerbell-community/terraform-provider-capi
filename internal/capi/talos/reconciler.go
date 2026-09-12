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
	// A BMC that arms the boot itself when media is attached (Intel AMT) must
	// not get a SetBootDevice afterwards: that would replace the armed boot.
	virtualMedia := func(ctx context.Context) error {
		if r.sess.images.ISO == "" {
			return fmt.Errorf("virtual media: no ISO URL: %w", ErrUnsupported)
		}
		var armed bool
		if err := r.bmcDo(ctx, func(ctx context.Context) error {
			var err error
			armed, err = r.bmc.InsertMedia(ctx, r.sess.images.ISO)
			return err
		}); err != nil {
			return err
		}
		if armed {
			return nil
		}
		return r.bmcDo(ctx, func(ctx context.Context) error { return r.bmc.SetBootDevice(ctx, BootDeviceCDROM, false, true) })
	}
	httpBoot := func(ctx context.Context) error {
		if r.sess.images.UKI == "" {
			return fmt.Errorf("uefi http boot: no UKI URL: %w", ErrUnsupported)
		}
		var armed bool
		if err := r.bmcDo(ctx, func(ctx context.Context) error {
			var err error
			armed, err = r.bmc.SetHTTPBootURI(ctx, r.sess.images.UKI)
			return err
		}); err != nil {
			return err
		}
		if armed {
			return nil
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
		// Alternate between the two payloads across attempts: firmware that
		// silently refuses one (an ISO over HTTP boot, say) often takes the
		// other, and a failed boot reports nothing we could branch on.
		first, second := virtualMedia, httpBoot
		firstMethod, secondMethod := BootMethodVirtualMedia, BootMethodHTTP
		if r.sess.method == BootMethodVirtualMedia && r.sess.images.UKI != "" {
			first, second = httpBoot, virtualMedia
			firstMethod, secondMethod = BootMethodHTTP, BootMethodVirtualMedia
		}
		if err = first(ctx); err == nil {
			r.sess.method = firstMethod
			return nil
		}
		if !errors.Is(err, ErrUnsupported) {
			return err
		}
		r.logger.Printf("%s: %s unsupported, trying %s", r.name, firstMethod, secondMethod)
		if err = second(ctx); err == nil {
			r.sess.method = secondMethod
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
	if err := r.bmcDo(ctx, func(ctx context.Context) error {
		_, err := r.bmc.SetHTTPBootURI(ctx, "")
		return err
	}); err != nil && !errors.Is(err, ErrUnsupported) {
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
