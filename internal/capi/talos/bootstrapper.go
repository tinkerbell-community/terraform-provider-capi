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

var (
	_ capi.Bootstrapper          = (*Bootstrapper)(nil)
	_ capi.PreTemplateManifester = (*Bootstrapper)(nil)
)

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

	// Restore persisted secrets, if any, so a node we previously provisioned is
	// recognized as ours across runs and can be reached over the Talos API.
	if s := opts.ProviderSecrets[ProviderSecretsKey]; s != "" {
		if err := r.seedSecrets(s); err != nil {
			return nil, &capi.BootstrapError{ClusterName: name, Operation: "seed-secrets", Err: err}
		}
	}

	cluster, err := r.run(ctx)
	if err != nil {
		return nil, &capi.BootstrapError{ClusterName: name, Operation: "create", Err: err}
	}

	// Surface the run's secrets bundle so the caller can persist it in state.
	if secretsYAML, serr := r.exportSecrets(); serr != nil {
		b.logger.Printf("%s: warning: exporting talos secrets for persistence: %v", name, serr)
	} else if secretsYAML != "" {
		if cluster.ProviderSecrets == nil {
			cluster.ProviderSecrets = map[string]string{}
		}
		cluster.ProviderSecrets[ProviderSecretsKey] = secretsYAML
	}
	return cluster, nil
}

// PreTemplateManifests implements capi.PreTemplateManifester. After Create, it
// returns the Talos machine-secrets Secret and the cluster kubeconfig Secret,
// both populated from the just-bootstrapped node, so the CAPI Talos providers
// adopt the node's PKI and kubeconfig and the cluster needs no pivot. It returns
// nil when there is no live session (e.g. Create was not run).
func (b *Bootstrapper) PreTemplateManifests(_ context.Context, clusterName, namespace string) ([][]byte, error) {
	b.mu.Lock()
	r := b.last
	b.mu.Unlock()
	if r == nil {
		return nil, nil
	}

	if r.sess.secretsBundle == nil {
		return nil, nil
	}

	var manifests [][]byte
	bundleYAML, err := MarshalSecretsBundle(r.sess.secretsBundle)
	if err != nil {
		return nil, err
	}
	secretManifest, err := talosSecretsBundleManifest(clusterName, namespace, bundleYAML)
	if err != nil {
		return nil, err
	}
	manifests = append(manifests, secretManifest)

	// Derive the cluster kubeconfig statically from the same bundle (no running
	// cluster needed), so CAPI has <cluster>-kubeconfig before the control plane
	// comes up.
	kubeconfig, err := deriveKubeconfigFromBundle(r.sess.secretsBundle, clusterName, r.cfg.Talos.Endpoint)
	if err != nil {
		return nil, err
	}
	kubeconfigManifest, err := clusterKubeconfigManifest(clusterName, namespace, kubeconfig)
	if err != nil {
		return nil, err
	}
	manifests = append(manifests, kubeconfigManifest)

	return manifests, nil
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
