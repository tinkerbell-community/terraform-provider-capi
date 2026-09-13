// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package capi

import (
	"context"
	"fmt"

	clusterctlclient "sigs.k8s.io/cluster-api/cmd/clusterctl/client"
)

// ClusterctlInstaller installs CAPI components using the clusterctl client
// library. It always injects an isolated config client (so provider URL
// overrides never touch global viper state) and a repository factory that
// applies capi-operator-style customizations to component YAML.
type ClusterctlInstaller struct {
	configPath string
}

// NewClusterctlInstaller creates a new installer with the given clusterctl
// config path. An empty configPath lets clusterctl fall back to its own
// default config file resolution.
func NewClusterctlInstaller(configPath string) *ClusterctlInstaller {
	return &ClusterctlInstaller{configPath: configPath}
}

// Init initializes CAPI providers on a cluster using clusterctl init.
// This corresponds to EKS Anywhere's installCAPIComponentsTask.
func (i *ClusterctlInstaller) Init(ctx context.Context, cluster *Cluster, opts InitOptions) error {
	initOpts, clientOpts, _, err := buildInitOptions(ctx, i.configPath, opts.Providers)
	if err != nil {
		return &CAPIError{Operation: "init", Cluster: cluster.Name, Err: fmt.Errorf("%w: %v", ErrCAPIInit, err)}
	}
	initOpts.Kubeconfig = clusterctlclient.Kubeconfig{Path: cluster.KubeconfigPath}

	client, err := clusterctlclient.New(ctx, i.configPath, clientOpts...)
	if err != nil {
		return &CAPIError{Operation: "init", Cluster: cluster.Name, Err: fmt.Errorf("%w: creating clusterctl client: %v", ErrCAPIInit, err)}
	}

	if _, err := client.Init(ctx, *initOpts); err != nil {
		return &CAPIError{Operation: "init", Cluster: cluster.Name, Err: fmt.Errorf("%w: %v", ErrCAPIInit, err)}
	}
	return nil
}

// buildInitOptions resolves provider URL overrides and assembles the
// clusterctl init options and client options for a ProviderSet. The returned
// reader is exposed for tests.
func buildInitOptions(ctx context.Context, configPath string, providers ProviderSet) (*clusterctlclient.InitOptions, []clusterctlclient.Option, *overlayReader, error) {
	configClient, reader, err := newOverlayConfigClient(ctx, configPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("creating clusterctl config client: %w", err)
	}

	for _, p := range providers.All() {
		def, exact := lookupDefaultProvider(configClient, p)
		// A bare entry clusterctl already knows by this exact name needs no
		// override. Everything else (pinned version, fetch config, or a name
		// clusterctl only knows in its "<name>-<name>" form) is registered.
		if exact && !p.NeedsURLOverride() {
			continue
		}
		defaultURL := ""
		if def != nil {
			defaultURL = def.URL()
		}
		u, err := ResolveFetchURL(p, defaultURL)
		if err != nil {
			return nil, nil, nil, err
		}
		reader.AddOverride(p.Name, p.Type.Clusterctl(), u)
	}

	initOpts := &clusterctlclient.InitOptions{
		// Wait for provider deployments (including webhook services) to become
		// ready before returning. Without this, callers can race ahead and
		// apply manifests that hit not-yet-ready admission webhooks.
		WaitProviders:           true,
		BootstrapProviders:      providers.InitStrings(ProviderTypeBootstrap),
		ControlPlaneProviders:   providers.InitStrings(ProviderTypeControlPlane),
		InfrastructureProviders: providers.InitStrings(ProviderTypeInfrastructure),
		IPAMProviders:           providers.InitStrings(ProviderTypeIPAM),
		AddonProviders:          providers.InitStrings(ProviderTypeAddon),
	}
	if core := providers.InitStrings(ProviderTypeCore); len(core) > 0 {
		initOpts.CoreProvider = core[0]
	}

	clientOpts := []clusterctlclient.Option{
		clusterctlclient.InjectConfig(configClient),
		clusterctlclient.InjectRepositoryFactory(NewCustomizingRepoFactory(configClient, providers.Customized())),
	}
	return initOpts, clientOpts, reader, nil
}
