// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFactoryResolver_ExplicitOverrides(t *testing.T) {
	r := NewFactoryResolver(nil)
	got, err := r.Resolve(context.Background(), ImageSpec{
		ISO: "https://mirror.example/talos.iso", Installer: "mirror.example/installer:v1", Version: "v1.13.6", Architecture: "amd64",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ISO != "https://mirror.example/talos.iso" || got.Installer != "mirror.example/installer:v1" || got.UKI != "" {
		t.Fatalf("Resolve() = %+v", got)
	}
}

func TestFactoryResolver_PrecomputedSchematic(t *testing.T) {
	// The installer keeps the pinned schematic (no POST for it); the boot ISO
	// uses a derived schematic that strips talos.halt_if_installed, which is one
	// POST.
	var bootBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/schematics" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &bootBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"boot123"}`))
	}))
	defer srv.Close()

	got, err := NewFactoryResolver(srv.Client()).Resolve(context.Background(), ImageSpec{
		Factory: srv.URL, Schematic: "abc123", Version: "v1.13.6", Architecture: "arm64",
	})
	if err != nil {
		t.Fatal(err)
	}
	host := srv.Listener.Addr().String()
	want := ImageURLs{
		ISO:       srv.URL + "/image/boot123/v1.13.6/metal-arm64.iso",
		UKI:       srv.URL + "/image/boot123/v1.13.6/metal-arm64-uki.efi",
		Installer: host + "/installer/abc123:v1.13.6",
	}
	if got != want {
		t.Fatalf("Resolve() = %+v, want %+v", got, want)
	}
	if args := kernelArgsOf(t, bootBody); len(args) != 1 || args[0] != "-talos.halt_if_installed" {
		t.Fatalf("boot extraKernelArgs = %v, want [-talos.halt_if_installed]", args)
	}
}

// kernelArgsOf extracts customization.extraKernelArgs from a schematic POST body.
func kernelArgsOf(t *testing.T, body map[string]any) []string {
	t.Helper()
	cust, _ := body["customization"].(map[string]any)
	raw, _ := cust["extraKernelArgs"].([]any)
	out := make([]string, 0, len(raw))
	for _, a := range raw {
		out = append(out, a.(string))
	}
	return out
}

func TestFactoryResolver_CreatesSchematic(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/schematics" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		var b map[string]any
		if err := json.Unmarshal(raw, &b); err != nil {
			t.Errorf("bad body: %v", err)
		}
		bodies = append(bodies, b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"deadbeef"}`))
	}))
	defer srv.Close()

	got, err := NewFactoryResolver(srv.Client()).Resolve(context.Background(), ImageSpec{
		Factory: srv.URL, Extensions: []string{"siderolabs/iscsi-tools"}, KernelArgs: []string{"net.ifnames=0"}, Version: "v1.13.6", Architecture: "amd64",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ISO != srv.URL+"/image/deadbeef/v1.13.6/metal-amd64.iso" {
		t.Fatalf("ISO = %q", got.ISO)
	}
	// Two schematics are created: the installer schematic, then the boot
	// schematic (which additionally removes talos.halt_if_installed).
	if len(bodies) != 2 {
		t.Fatalf("expected 2 schematic POSTs (installer + boot), got %d", len(bodies))
	}
	for i, b := range bodies {
		cust, _ := b["customization"].(map[string]any)
		sysExt, _ := cust["systemExtensions"].(map[string]any)
		ext, _ := sysExt["officialExtensions"].([]any)
		if len(ext) != 1 || ext[0] != "siderolabs/iscsi-tools" {
			t.Fatalf("POST %d officialExtensions = %v", i, sysExt["officialExtensions"])
		}
	}
	if args := kernelArgsOf(t, bodies[0]); len(args) != 1 || args[0] != "net.ifnames=0" {
		t.Fatalf("installer extraKernelArgs = %v, want [net.ifnames=0]", args)
	}
	if args := kernelArgsOf(t, bodies[1]); len(args) != 2 || args[0] != "net.ifnames=0" || args[1] != "-talos.halt_if_installed" {
		t.Fatalf("boot extraKernelArgs = %v, want [net.ifnames=0 -talos.halt_if_installed]", args)
	}
}

func TestFactoryResolver_SchematicHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadRequest)
	}))
	defer srv.Close()
	_, err := NewFactoryResolver(srv.Client()).Resolve(context.Background(), ImageSpec{Factory: srv.URL, Version: "v1.13.6", Architecture: "amd64"})
	if err == nil {
		t.Fatal("expected error from 400 response")
	}
}
