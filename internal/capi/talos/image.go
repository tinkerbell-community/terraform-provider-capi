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

// resetImageKernelArgs customize a one-shot "reset" boot image used to force an
// already-installed node back to maintenance mode when we have no Talos API
// access to reset it gracefully:
//   - "-talos.halt_if_installed" (the "-" prefix removes an arg) drops the
//     metal-ISO default that halts when Talos is already installed, so the ISO
//     runs instead of halting.
//   - "talos.experimental.wipe=system" wipes the system disk on boot.
//
// This is deliberately NOT the default boot image. On a one-shot IDER boot,
// wipe=system wipes the disk and then reboots; because the one-shot boot entry
// is already consumed, the node reboots into a now-blank disk ("a bootable
// device has not been detected", confirmed on hardware: it reads the whole ISO
// into RAM, wipes, reboots, and is then stuck). The reconciler therefore streams
// this image for exactly one wipe boot and then boots the normal image, which
// now reaches maintenance on the blank disk.
var resetImageKernelArgs = []string{"-talos.halt_if_installed", "talos.experimental.wipe=system"}

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
// were given, which disables UEFI HTTP boot. ResetISO is the one-shot wipe image
// (see resetImageKernelArgs); it is empty when explicit ISO/Installer overrides
// are given, since a reset image cannot be derived from a pinned ISO URL.
type ImageURLs struct {
	ISO       string
	UKI       string
	ResetISO  string
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

	// The normal boot media (ISO/UKI), the installer image, and the installed
	// system all use the caller's schematic unchanged. A blank node boots this
	// straight to maintenance; an already-installed node may halt, which the
	// reconciler escalates from (API reset, else the reset image below).
	baseID := spec.Schematic
	if baseID == "" {
		baseID, err = r.createSchematic(ctx, factory, spec.Extensions, spec.KernelArgs)
		if err != nil {
			return ImageURLs{}, err
		}
	}

	// The reset media adds the wipe / halt-removal args (see
	// resetImageKernelArgs). It is streamed for a single wipe boot only when an
	// already-installed node cannot be reached to reset it gracefully. Note: a
	// caller that pins a schematic id with system extensions does not carry those
	// into this image (the id's customization is not recoverable from the id), so
	// the reset environment is the base image plus these args.
	resetArgs := append(append([]string(nil), spec.KernelArgs...), resetImageKernelArgs...)
	resetID, err := r.createSchematic(ctx, factory, spec.Extensions, resetArgs)
	if err != nil {
		return ImageURLs{}, err
	}

	arch := spec.Architecture
	if arch == "" {
		arch = "amd64"
	}
	return ImageURLs{
		ISO:       fmt.Sprintf("%s/image/%s/%s/metal-%s.iso", factory, baseID, spec.Version, arch),
		UKI:       fmt.Sprintf("%s/image/%s/%s/metal-%s-uki.efi", factory, baseID, spec.Version, arch),
		ResetISO:  fmt.Sprintf("%s/image/%s/%s/metal-%s.iso", factory, resetID, spec.Version, arch),
		Installer: fmt.Sprintf("%s/installer/%s:%s", u.Host, baseID, spec.Version),
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
