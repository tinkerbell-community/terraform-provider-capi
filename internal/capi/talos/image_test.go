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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
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
		ISO:       srv.URL + "/image/abc123/v1.13.6/metal-arm64.iso",
		UKI:       srv.URL + "/image/abc123/v1.13.6/metal-arm64-uki.efi",
		Installer: host + "/metal-installer/abc123:v1.13.6",
	}
	if got != want {
		t.Fatalf("Resolve() = %+v, want %+v", got, want)
	}
}

func TestFactoryResolver_CreatesSchematic(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/schematics" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("bad body: %v", err)
		}
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
	cust, ok := body["customization"].(map[string]any)
	if !ok {
		t.Fatalf("customization missing: %v", body)
	}
	sysExt, ok := cust["systemExtensions"].(map[string]any)
	if !ok {
		t.Fatalf("systemExtensions missing: %v", cust)
	}
	ext, ok := sysExt["officialExtensions"].([]any)
	if !ok || len(ext) != 1 || ext[0] != "siderolabs/iscsi-tools" {
		t.Fatalf("officialExtensions = %v", sysExt["officialExtensions"])
	}
	args, ok := cust["extraKernelArgs"].([]any)
	if !ok || len(args) != 1 || args[0] != "net.ifnames=0" {
		t.Fatalf("extraKernelArgs = %v", cust["extraKernelArgs"])
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
