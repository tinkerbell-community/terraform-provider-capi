// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/device-management-toolkit/go-wsman-messages/v2/pkg/wsman"
	"github.com/device-management-toolkit/go-wsman-messages/v2/pkg/wsman/client"
)

// TestDebugAMTBootState is an env-gated, read-only diagnostic that dumps the
// Intel AMT boot capabilities and boot setting data (including the BIOS's
// last boot status) so a silent OCR boot failure can be explained.
//
//	CAPI_TALOS_DEBUG_AMT=1 CAPI_TALOS_AMT_TARGET=10.0.0.160:16993 \
//	CAPI_TALOS_BMC_USERNAME=admin CAPI_TALOS_BMC_PASSWORD=... \
//	go test ./internal/capi/talos/ -run TestDebugAMTBootState -v
func TestDebugAMTBootState(t *testing.T) {
	target := os.Getenv("CAPI_TALOS_AMT_TARGET")
	if os.Getenv("CAPI_TALOS_DEBUG_AMT") == "" || target == "" {
		t.Skip("set CAPI_TALOS_DEBUG_AMT=1 and CAPI_TALOS_AMT_TARGET to dump AMT boot state")
	}
	msg := wsman.NewMessages(client.Parameters{
		Target:            target,
		Username:          os.Getenv("CAPI_TALOS_BMC_USERNAME"),
		Password:          os.Getenv("CAPI_TALOS_BMC_PASSWORD"),
		UseDigest:         true,
		UseTLS:            true,
		SelfSignedAllowed: true,
	})

	caps, err := msg.AMT.BootCapabilities.Get()
	if err != nil {
		t.Fatalf("boot capabilities: %v", err)
	}
	raw, _ := json.MarshalIndent(caps.Body.BootCapabilitiesGetResponse, "", "  ")
	t.Logf("AMT_BootCapabilities:\n%s", raw)

	bsd, err := msg.AMT.BootSettingData.Get()
	if err != nil {
		t.Fatalf("boot setting data: %v", err)
	}
	raw, _ = json.MarshalIndent(bsd.Body.BootSettingDataGetResponse, "", "  ")
	t.Logf("AMT_BootSettingData:\n%s", raw)
}
