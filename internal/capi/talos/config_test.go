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
	// Scalars are overridden by later patches; lists are appended by Talos'
	// strategic merge, so ordering is asserted on the scalar.
	in.Patches = []string{
		"machine:\n  install:\n    disk: /dev/sdb\n    extraKernelArgs: [first]\n",
		"machine:\n  install:\n    disk: /dev/sdc\n",
	}
	gen, err := GenerateConfig(in, bundle)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := configloader.NewFromBytes(gen.MachineConfig)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Machine().Install().Disk(); got != "/dev/sdc" {
		t.Errorf("install disk = %q, want /dev/sdc (last patch wins)", got)
	}
	if got := cfg.Machine().Install().ExtraKernelArgs(); !slices.Equal(got, []string{"first"}) {
		t.Errorf("extraKernelArgs = %v, want [first]", got)
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
