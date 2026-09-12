// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strconv"
	"testing"
	"time"

	bmclib "github.com/bmc-toolbox/bmclib/v2"
)

// TestDebugBMCProbe is an env-gated, read-only diagnostic for a real BMC. It
// goes through BMCLib (so the address URL form and provider pinning are
// exercised), reports the power state and which provider answered, then dumps
// the bmclib inventory. It never changes machine state.
//
//	CAPI_TALOS_DEBUG_PROBE=1 CAPI_TALOS_BMC_ADDRESS=https://10.0.0.160:16993 \
//	CAPI_TALOS_BMC_USERNAME=admin CAPI_TALOS_BMC_PASSWORD=... \
//	go test ./internal/capi/talos/ -run TestDebugBMCProbe -v
func TestDebugBMCProbe(t *testing.T) {
	addr := os.Getenv("CAPI_TALOS_BMC_ADDRESS")
	if os.Getenv("CAPI_TALOS_DEBUG_PROBE") == "" || addr == "" {
		t.Skip("set CAPI_TALOS_DEBUG_PROBE=1 and CAPI_TALOS_BMC_* to probe a BMC")
	}
	user, pass := os.Getenv("CAPI_TALOS_BMC_USERNAME"), os.Getenv("CAPI_TALOS_BMC_PASSWORD")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	b := NewBMCLib(BMCCredentials{Address: addr, Username: user, Password: pass}, log.New(os.Stderr, "[probe] ", log.LstdFlags))

	state, err := b.PowerState(ctx)
	if err != nil {
		t.Fatalf("power state: %v", err)
	}
	t.Logf("power state: %s (provider %q)", state, b.preferredProvider())

	// The second session should reuse the pinned provider and be fast.
	start := time.Now()
	if _, err := b.PowerState(ctx); err != nil {
		t.Fatalf("second power state: %v", err)
	}
	t.Logf("second session took %s", time.Since(start).Round(time.Millisecond))

	// Inventory is not part of the BMC interface; use bmclib directly with the
	// same address handling and provider pin.
	host, port, scheme, err := parseAddress(addr)
	if err != nil {
		t.Fatal(err)
	}
	opts := []bmclib.Option{bmclib.WithPerProviderTimeout(perProviderTimeout)}
	if port > 0 {
		opts = append(opts, bmclib.WithIntelAMTPort(uint32(port)), bmclib.WithRedfishPort(strconv.Itoa(port)))
	}
	if scheme != "" {
		opts = append(opts, bmclib.WithIntelAMTHostScheme(scheme))
	}
	c := bmclib.NewClient(host, user, pass, opts...)
	if p := b.preferredProvider(); p != "" {
		c.Registry.Drivers = c.Registry.For(p)
	}
	if err := c.Open(ctx); err != nil {
		t.Fatalf("open: %v (%v)", err, c.GetMetadata().FailedProviderDetail)
	}
	defer func() { _ = c.Close(ctx) }()
	inv, err := c.Inventory(ctx)
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	raw, _ := json.MarshalIndent(inv, "", "  ")
	if len(raw) > 6000 {
		raw = append(raw[:6000], []byte("\n...(truncated)")...)
	}
	t.Logf("inventory (provider %s):\n%s", c.GetMetadata().SuccessfulProvider, raw)
}
