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
