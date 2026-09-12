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
