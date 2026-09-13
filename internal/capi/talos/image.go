// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// DefaultFactoryURL is the public Talos Image Factory.
const DefaultFactoryURL = "https://factory.talos.dev"

// bootResetKernelArgs customize the boot image so an already-installed node can
// be re-provisioned. "-talos.halt_if_installed" (the "-" prefix removes an arg)
// drops the metal-ISO default that halts when Talos is already installed, so the
// ISO comes up in maintenance mode instead of halting.
//
// It deliberately does NOT set "talos.experimental.wipe=system". On a one-shot
// IDER boot that arg wipes the disk and then reboots, and because the one-shot
// boot entry is already consumed the node reboots into a now-blank disk with no
// bootable device (confirmed on hardware: the node reads the whole ISO into RAM,
// wipes, reboots, and is then stuck at "a bootable device has not been
// detected"). Wiping an installed node is handled out of band instead — an
// API-driven talosctl reset for nodes we own, otherwise the install writing a
// fresh system to disk once the node is in maintenance.
var bootResetKernelArgs = []string{"-talos.halt_if_installed"}

// ImageSpec describes which Talos images to boot and install.
type ImageSpec struct {
	// Factory is the Image Factory base URL. Empty means DefaultFactoryURL.
	Factory string
	// Schematic is a precomputed schematic id. Empty means create one.
	Schematic string
	// Extensions are official system extension names for a new schematic.
	Extensions []string
	// KernelArgs are extra kernel args for a new schematic.
	KernelArgs []string
	// ISO and Installer override everything else when both are set.
	ISO       string
	Installer string
	// Version is the Talos version, v-prefixed.
	Version string
	// Architecture is amd64 or arm64.
	Architecture string
}

// ImageURLs are the resolved artifacts. UKI is empty when explicit overrides
// were given, which disables UEFI HTTP boot.
type ImageURLs struct {
	ISO       string
	UKI       string
	Installer string
}

// ImageResolver turns an ImageSpec into concrete URLs.
type ImageResolver interface {
	Resolve(ctx context.Context, spec ImageSpec) (ImageURLs, error)
}

// FactoryResolver resolves images against a Talos Image Factory.
type FactoryResolver struct {
	http *http.Client
}

// NewFactoryResolver returns a resolver using httpClient (nil means http.DefaultClient).
func NewFactoryResolver(httpClient *http.Client) *FactoryResolver {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &FactoryResolver{http: httpClient}
}

type schematicRequest struct {
	Customization struct {
		SystemExtensions struct {
			OfficialExtensions []string `json:"officialExtensions,omitempty"`
		} `json:"systemExtensions"`
		ExtraKernelArgs []string `json:"extraKernelArgs,omitempty"`
	} `json:"customization"`
}

type schematicResponse struct {
	ID string `json:"id"`
}

// Resolve implements ImageResolver.
func (r *FactoryResolver) Resolve(ctx context.Context, spec ImageSpec) (ImageURLs, error) {
	if spec.ISO != "" && spec.Installer != "" {
		return ImageURLs{ISO: spec.ISO, Installer: spec.Installer}, nil
	}

	factory := strings.TrimRight(spec.Factory, "/")
	if factory == "" {
		factory = DefaultFactoryURL
	}
	u, err := url.Parse(factory)
	if err != nil || u.Host == "" {
		return ImageURLs{}, fmt.Errorf("invalid image factory URL %q", factory)
	}

	// The installer image (and the installed system) uses the caller's schematic.
	installerID := spec.Schematic
	if installerID == "" {
		installerID, err = r.createSchematic(ctx, factory, spec.Extensions, spec.KernelArgs)
		if err != nil {
			return ImageURLs{}, err
		}
	}

	// Boot media (ISO/UKI) uses a schematic that strips talos.halt_if_installed
	// (the metal ISO sets it by default). Without it, a node that already has
	// Talos on disk halts when booted from the ISO instead of entering
	// maintenance mode; with it removed the node boots to maintenance and the
	// reconciler re-installs, wiping the disk. Note: a caller that pins a
	// schematic id with system extensions does not carry those into this boot
	// image (the id's customization is not recoverable from the id), so the
	// maintenance environment is the base image plus this removal.
	bootArgs := append(append([]string(nil), spec.KernelArgs...), bootResetKernelArgs...)
	bootID, err := r.createSchematic(ctx, factory, spec.Extensions, bootArgs)
	if err != nil {
		return ImageURLs{}, err
	}

	arch := spec.Architecture
	if arch == "" {
		arch = "amd64"
	}
	return ImageURLs{
		ISO:       fmt.Sprintf("%s/image/%s/%s/metal-%s.iso", factory, bootID, spec.Version, arch),
		UKI:       fmt.Sprintf("%s/image/%s/%s/metal-%s-uki.efi", factory, bootID, spec.Version, arch),
		Installer: fmt.Sprintf("%s/installer/%s:%s", u.Host, installerID, spec.Version),
	}, nil
}

func (r *FactoryResolver) createSchematic(ctx context.Context, factory string, extensions, kernelArgs []string) (string, error) {
	var req schematicRequest
	req.Customization.SystemExtensions.OfficialExtensions = extensions
	req.Customization.ExtraKernelArgs = kernelArgs
	payload, err := json.Marshal(req)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, factory+"/schematics", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := r.http.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("creating image factory schematic: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("creating image factory schematic: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out schematicResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decoding schematic response: %w", err)
	}
	if out.ID == "" {
		return "", fmt.Errorf("image factory returned an empty schematic id")
	}
	return out.ID, nil
}
