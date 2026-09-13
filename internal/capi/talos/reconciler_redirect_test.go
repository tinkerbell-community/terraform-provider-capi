// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/tinkerbell-community/terraform-provider-capi/internal/capi"
)

// staticResolver returns fixed image URLs.
type staticResolver struct{ urls ImageURLs }

func (s staticResolver) Resolve(context.Context, ImageSpec) (ImageURLs, error) { return s.urls, nil }

// redirectSim is a simMachine that also serves storage redirection (Intel AMT),
// so the reconciler takes the RedirectISO path instead of InsertMedia.
type redirectSim struct {
	*simMachine
	mu          sync.Mutex
	redirects   int
	redirectISO string
	closed      int
}

func (r *redirectSim) RedirectSupported(context.Context) bool { return true }

func (r *redirectSim) RedirectISO(_ context.Context, isoPath string) (RedirectHandle, error) {
	r.mu.Lock()
	r.redirects++
	r.redirectISO = isoPath
	r.mu.Unlock()
	// Arm the boot the way AMT redirection does: the media is now attached and
	// selected, no SetBootDevice needed.
	r.simMachine.mu.Lock()
	r.media = "redirect"
	r.armedMedia = true
	r.simMachine.mu.Unlock()
	return redirectCloser{r}, nil
}

type redirectCloser struct{ r *redirectSim }

func (c redirectCloser) Close() error {
	c.r.mu.Lock()
	c.r.closed++
	c.r.mu.Unlock()
	c.r.simMachine.mu.Lock()
	c.r.media = ""
	c.r.armedMedia = false
	c.r.simMachine.mu.Unlock()
	return nil
}

func TestBootstrapper_AMTRedirectionPath(t *testing.T) {
	iso := make([]byte, 4*2048)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(iso)
	}))
	defer srv.Close()

	sim := newSim()
	rsim := &redirectSim{simMachine: sim}
	inst := &fakeInstaller{sim: sim}
	cfg := Config{
		Machine: MachineConfig{Hostname: "cp-1", IP: "10.0.0.5", Disk: "/dev/sda", BMC: BMCCredentials{Address: "https://10.0.1.5:16993", Username: "u", Password: "p"}},
		Boot:    BootConfig{Method: BootMethodAuto, Attempts: 3},
		Talos:   TalosConfig{Version: "v1.13.6"},
		Addons:  Addons{Helm: []HelmRelease{{Name: "cilium", Namespace: "kube-system", Chart: "oci://quay.io/cilium/charts/cilium"}}},
	}
	resolver := staticResolver{urls: ImageURLs{ISO: srv.URL + "/talos.iso", Installer: "factory.example/metal-installer/abc:v1.13.6"}}
	b := New(cfg,
		WithBMC(rsim),
		WithNode(sim),
		WithKubeFactory(func(string) (Kube, error) { return sim, nil }),
		WithImageResolver(resolver),
		WithAddonInstaller(inst),
		WithLogger(log.New(io.Discard, "", 0)),
		WithTempDir(t.TempDir()),
		WithTimeouts(fastTimeouts()),
	)

	cluster, err := b.Create(context.Background(), capi.BootstrapOptions{Name: "test-bootstrap", KubernetesVersion: "v1.34.0"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if cluster == nil {
		t.Fatal("expected a cluster")
	}
	if rsim.redirects == 0 {
		t.Error("expected RedirectISO to be used for an AMT host")
	}
	if rsim.redirectISO == "" {
		t.Error("expected a downloaded ISO path")
	}
	if rsim.closed == 0 {
		t.Error("expected the redirection session to be closed by detach/teardown")
	}
	if sim.count("boot-device:cdrom") != 0 {
		t.Error("SetBootDevice(cdrom) must be skipped when redirection arms the boot")
	}
}
