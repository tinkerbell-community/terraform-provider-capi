// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"encoding/json"
	"encoding/xml"
	"os"
	"testing"

	"github.com/device-management-toolkit/go-wsman-messages/v2/pkg/wsman"
	amtboot "github.com/device-management-toolkit/go-wsman-messages/v2/pkg/wsman/amt/boot"
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

// TestDebugAMTArmIDER dumps the current AMT_BootSettingData and attempts one
// IDER-arming Put, printing the exact request XML and any error, to diagnose
// InvalidRepresentation. Read-mostly: the single Put does not reboot anything.
//
//	CAPI_TALOS_DEBUG_AMT=1 CAPI_TALOS_AMT_TARGET=10.0.0.160 \
//	CAPI_TALOS_BMC_USERNAME=admin CAPI_TALOS_BMC_PASSWORD=... \
//	go test ./internal/capi/talos/ -run TestDebugAMTArmIDER -v
func TestDebugAMTArmIDER(t *testing.T) {
	target := os.Getenv("CAPI_TALOS_AMT_TARGET")
	if os.Getenv("CAPI_TALOS_DEBUG_AMT") == "" || target == "" {
		t.Skip("set CAPI_TALOS_DEBUG_AMT=1 and CAPI_TALOS_AMT_TARGET")
	}
	msg := wsman.NewMessages(client.Parameters{
		Target: target, Username: os.Getenv("CAPI_TALOS_BMC_USERNAME"), Password: os.Getenv("CAPI_TALOS_BMC_PASSWORD"),
		UseDigest: true, UseTLS: true, SelfSignedAllowed: true,
	})

	cur, err := msg.AMT.BootSettingData.Get()
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp := cur.Body.BootSettingDataGetResponse
	j, _ := json.MarshalIndent(resp, "", "  ")
	t.Logf("current BootSettingData:\n%s", j)

	req := amtboot.BootSettingDataRequest{
		H:              "http://intel.com/wbem/wscim/1/amt-schema/1/AMT_BootSettingData",
		InstanceID:     "Intel(r) AMT:BootSettingData 0",
		ElementName:    resp.ElementName,
		OwningEntity:   resp.OwningEntity,
		UseIDER:        true,
		IDERBootDevice: amtboot.CDBoot,
		BootMediaIndex: 0,
	}
	xmlBytes, _ := xml.MarshalIndent(req, "", "  ")
	t.Logf("request XML:\n%s", xmlBytes)

	// Variant A: plain Put.
	if putResp, err := msg.AMT.BootSettingData.Put(req); err != nil {
		t.Logf("A: plain Put err: %v", err)
	} else {
		t.Logf("A: plain Put OK\n%s", putResp.XMLOutput)
	}

	// Variant B: clear any boot source first, then Put (a boot source conflicts
	// with UseIDER per the CIM_BootConfigSetting docs).
	if _, err := msg.CIM.BootConfigSetting.ChangeBootOrder(""); err != nil {
		t.Logf("B: ChangeBootOrder(clear) err: %v", err)
	} else {
		t.Log("B: ChangeBootOrder(clear) OK")
	}
	if putResp, err := msg.AMT.BootSettingData.Put(req); err != nil {
		t.Logf("B: Put-after-clear err: %v", err)
	} else {
		t.Logf("B: Put-after-clear OK\n%s", putResp.XMLOutput)
		if _, err := msg.CIM.BootService.SetBootConfigRole("Intel(r) AMT: Boot Configuration 0", 1); err != nil {
			t.Logf("B: SetBootConfigRole err: %v", err)
		} else {
			t.Log("B: SetBootConfigRole(1) OK - IDER armed")
		}
	}
}
