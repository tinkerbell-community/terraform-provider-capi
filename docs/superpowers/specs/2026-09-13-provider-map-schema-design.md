# Provider-map schema for `capi_cluster` (schema v2)

**Date:** 2026-09-13
**Status:** Approved (design), implementation pending

## Goal

Replace the per-type `provider = "name:version"` strings on `capi_cluster` with
maps keyed by provider name, matching the cluster-api-operator Helm chart
values layout (`core`, `infrastructure`, `bootstrap`, `control_plane`, `ipam`,
`addon`). Every provider entry gets the full customization surface the
`addons` list already has (fetch config, config variables, deployment and
manager overrides, patches). Add a friendlier `fetch_config` that only needs a
GitHub owner override, so users do not repeat the version inside a URL.

## Non-goals

- Running the cluster-api-operator. Everything is still `clusterctl init`
  with the existing repository-factory customization pipeline.
- Runtime-extension providers. Cheap to add later via the same map shape.
- Applying provider changes in place. Update already runs with `SkipInit`,
  so all provider maps are `RequiresReplace`.

## HCL

```hcl
resource "capi_cluster" "talos" {
  name               = "acc-talos"
  kubernetes_version = "v1.34.0"

  core = {
    cluster-api = {
      manager = {
        feature_gates = { ClusterTopology = true, MachinePool = true }
      }
    }
  }

  infrastructure = {
    tinkerbell = {
      version      = "v0.7.9"
      fetch_config = { owner = "tinkerbell-community" }
      manager      = { feature_gates = { ClusterTopology = true } }
    }
  }

  bootstrap = {
    talos = {
      version      = "v0.8.2"
      fetch_config = { owner = "sidero-community" }
    }
  }

  control_plane = {
    talos = {
      version      = "v0.7.1"
      fetch_config = { owner = "sidero-community" }
    }
  }

  ipam = {
    unifi = {
      version      = "v0.4.1"
      fetch_config = { owner = "ubiquiti-community", repository = "cluster-api-ipam-provider-unifi" }
    }
  }

  addon = { helm = {} }

  topology = {
    control_plane = { machine_count = 1 }
    workers       = { machine_count = 0 }
  }

  management = { self_managed = true, bootstrap = { ... } }
  inventory  = { ... }
}
```

Attribute names stay snake_case (`fetch_config`, `feature_gates`) to match
Terraform convention and the existing addon attributes; the Helm chart's
camelCase keys are not carried over.

## Schema

### Provider maps

Six top-level `schema.MapNestedAttribute`s: `core`, `infrastructure`,
`bootstrap`, `control_plane`, `ipam`, `addon`. Map keys are provider names as
clusterctl knows them (`cluster-api`, `tinkerbell`, `talos`, `kubeadm`, `helm`,
`unifi`). All six share one nested object built by a single
`providerNestedObject()` helper:

| Attribute | Type | Notes |
| --- | --- | --- |
| `version` | String, Optional | Release tag (`v0.7.9`). Omitted means clusterctl's `latest`. |
| `fetch_config` | SingleNested, Optional | See below. |
| `config_variables` | Map(String), Optional | Unchanged from addons. |
| `secret_config_variables` | Map(String), Optional, Sensitive | Unchanged. |
| `deployment` | SingleNested, Optional | Unchanged. |
| `manager` | SingleNested, Optional | Unchanged (`feature_gates` is Map(Bool)). |
| `additional_manifests` | String, Optional | Unchanged. |
| `manifest_patches` | List(String), Optional | Unchanged. |
| `patches` | ListNested, Optional | Unchanged. |

An empty object (`helm = {}`) is valid and means "install the default release".

Plan modifiers: `mapplanmodifier.RequiresReplace()` on all six maps.

Required-ness: `infrastructure` is Required and must contain exactly one
entry (template generation needs a single infrastructure provider). The other
five are Optional and may contain several entries.

### `fetch_config`

```go
"fetch_config": schema.SingleNestedAttribute{
    Optional: true,
    Attributes: {
        "owner":      String Optional  // GitHub owner/org
        "repository": String Optional  // GitHub repository name
        "url":        String Optional  // verbatim clusterctl URL (escape hatch)
        "oci":        String Optional  // verbatim OCI reference
    },
}
```

Resolution order, implemented in `capi.ResolveFetchURL(cfg ProviderConfig, defaults config.Provider) (string, error)`:

1. `url` set: use verbatim.
2. `oci` set: use verbatim (`oci://...`, as clusterctl already accepts).
3. Otherwise build
   `https://github.com/{owner}/{repository}/releases/{version|latest}/{type}-components.yaml`
   where:
   - `owner` and `repository` default from clusterctl's built-in entry for
     this name and type (parsed out of its default URL);
   - `{type}-components.yaml` is `core`, `infrastructure`, `bootstrap`,
     `control-plane`, `ipam`, or `addon` per the map the entry lives in;
   - the version segment is the entry's `version`, or `latest` when unset.
4. If no built-in entry exists and `repository` is empty, validation fails:
   "provider X is not known to clusterctl; set fetch_config.repository (and
   owner) or fetch_config.url".

Setting `url`/`oci` together with `owner`/`repository` is a validation error.
Pinning the version inside the URL avoids the GitHub "latest release" API
call clusterctl performs when the URL says `latest`.

A provider entry with no `fetch_config` and no `version` needs no override
at all and is passed to clusterctl as a bare name, exactly as today.

### `topology`

Replaces `control_plane.machine_count` and top-level `workers`:

```go
"topology": schema.SingleNestedAttribute{
    Optional: true,
    Attributes: {
        "control_plane": SingleNested Optional { "machine_count": Int64 Optional },
        "workers":       SingleNested Optional { "machine_count": Int64 Optional },
    },
}
```

Both counts stay mutable (no RequiresReplace), as before.

### Removed

`infrastructure.provider`, `bootstrap.provider`, `control_plane.provider`,
`control_plane.machine_count`, `core.provider`, `workers`, and the `addons`
list.

### Unchanged

`name`, `kubernetes_version`, `flavor`, `id`, `management` (including
`management.bootstrap`), `inventory`, `wait`, `output`, `status`,
`provider_secrets`.

## Go models (`internal/provider`)

```go
type ClusterResourceModel struct {
    ...
    Core           types.Map    `tfsdk:"core"`
    Infrastructure types.Map    `tfsdk:"infrastructure"`
    Bootstrap      types.Map    `tfsdk:"bootstrap"`
    ControlPlane   types.Map    `tfsdk:"control_plane"`
    IPAM           types.Map    `tfsdk:"ipam"`
    Addon          types.Map    `tfsdk:"addon"`
    Topology       types.Object `tfsdk:"topology"`
    ...
}

type ProviderModel struct {            // was AddonModel, minus Provider, plus Version
    Version               types.String `tfsdk:"version"`
    FetchConfig           types.Object `tfsdk:"fetch_config"`
    ConfigVariables       types.Map    `tfsdk:"config_variables"`
    SecretConfigVariables types.Map    `tfsdk:"secret_config_variables"`
    Deployment            types.Object `tfsdk:"deployment"`
    Manager               types.Object `tfsdk:"manager"`
    AdditionalManifests   types.String `tfsdk:"additional_manifests"`
    ManifestPatches       types.List   `tfsdk:"manifest_patches"`
    Patches               types.List   `tfsdk:"patches"`
}

type FetchConfigModel struct {
    Owner      types.String `tfsdk:"owner"`
    Repository types.String `tfsdk:"repository"`
    URL        types.String `tfsdk:"url"`
    OCI        types.String `tfsdk:"oci"`
}

type TopologyModel struct {
    ControlPlane types.Object `tfsdk:"control_plane"` // MachineCountModel
    Workers      types.Object `tfsdk:"workers"`       // MachineCountModel
}
type MachineCountModel struct { MachineCount types.Int64 `tfsdk:"machine_count"` }
```

The existing `AddonDeploymentModel`, `AddonManagerModel`, `AddonPatchModel`,
`AddonPatchSelectorModel`, `AddonContainerModel` are renamed to drop the
`Addon` prefix. One `providerAttrTypes()` serves all six maps.

Helpers:

- `extractProviders(ctx, m types.Map) (map[string]ProviderModel, diag.Diagnostics)`
- `buildProviderConfig(ctx, name string, typ capi.ProviderType, pm ProviderModel) (capi.ProviderConfig, diag.Diagnostics)`
  (the body of today's per-addon conversion loop).
- `extractTopology(ctx, data)` returning the two counts.

## Backend (`internal/capi`)

### Types

```go
type ProviderType string // "core" | "infrastructure" | "bootstrap" | "control-plane" | "ipam" | "addon"

func (t ProviderType) ComponentsFile() string     // "<t>-components.yaml"
func (t ProviderType) Clusterctl() clusterctlv1.ProviderType

type ProviderConfig struct {         // was AddonConfig
    Name    string
    Type    ProviderType
    Version string                    // "" means latest
    FetchConfig *FetchConfig          // Owner, Repository, URL, OCI
    ConfigVariables, SecretConfigVariables map[string]string
    Deployment *DeploymentConfig
    Manager    *ManagerConfig
    AdditionalManifests string
    ManifestPatches []string
    Patches []PatchConfig
}

func (p ProviderConfig) InitString() string        // "name" or "name:version"
func (p ProviderConfig) HasCustomizations() bool   // excludes FetchConfig and Version
func (p ProviderConfig) NeedsURLOverride() bool    // FetchConfig != nil || Version != ""

type ProviderSet map[ProviderType][]ProviderConfig

func (s ProviderSet) Infrastructure() (ProviderConfig, bool) // the single infra entry
func (s ProviderSet) InitStrings(t ProviderType) []string
func (s ProviderSet) Customized() map[providerKey]ProviderConfig // key = {Type, Name}
```

`CreateClusterOptions` drops `InfrastructureProvider`, `BootstrapProvider`,
`ControlPlaneProvider`, `CoreProvider`, `Addons` and gains
`Providers ProviderSet`. `InitOptions` drops the per-type string slices and
`Addons` and gains `Providers ProviderSet` (it also now covers IPAM). Both
manager call sites build `InitOptions{Providers: opts.Providers}`.
`TemplateOptions.InfrastructureProvider` stays a string, filled from
`Providers.Infrastructure().InitString()`.

### Installer

`ClusterctlInstaller.Init` always builds a config client and always injects
the repository factory (dropping the "only when customized" branch). Steps:

1. `reader := newOverlayReader(providerOverrides)`; `configClient, _ := config.New(ctx, configPath, config.InjectReader(reader))`.
2. For each `ProviderConfig` with `NeedsURLOverride()`, look up
   `configClient.Providers().Get(name, type)` for defaults, call
   `ResolveFetchURL`, and add `{name, type, url}` to the overlay. Unknown
   providers without a resolvable URL return a clear error before init.
3. `clusterctlclient.New(ctx, configPath, InjectConfig(configClient), InjectRepositoryFactory(NewCustomizingRepoFactory(configClient, providers.Customized())))`.
4. Fill `clusterctlclient.InitOptions` per type from `InitStrings`, including
   `IPAMProviders`.

The overlay lookup in step 2 needs the defaults list before the overlay is
populated. The overlay reader is therefore created empty, used for the
lookup, then has overrides appended; `providersClient` re-reads the key on
every `List()`/`Get()`, so no re-creation is needed.

### Overlay config reader

`internal/capi/clusterctl_config.go`: `overlayReader` implements
`config.Reader` with its own `viper.New()` instance (no global state, safe
for concurrent resources):

- `Init(ctx, path)`: explicit path, else the first existing of
  `$XDG_CONFIG_HOME/cluster-api/clusterctl.yaml` and
  `~/.cluster-api/clusterctl.yaml`; `AutomaticEnv` with the same key replacer
  clusterctl uses; a missing file is not an error.
- `Get`/`Set`: delegate to the viper instance.
- `UnmarshalKey(key, out)`: for `providers`, decode the file's list, append
  the overlay entries (overlay wins on same name+type), and decode the merged
  list into `out`; every other key delegates.

### Customization keying

`NewCustomizingRepoFactory` looks up `{input.Provider.Type(), input.Provider.Name()}`
so bootstrap `talos` and control-plane `talos` get independent settings.

## Validation (`validateLifecycleConfig`)

- `infrastructure` has exactly one key; name must be one of the supported
  set (unchanged list).
- Tinkerbell: `management.self_managed` must be true; every key in
  `bootstrap` and `control_plane` must be `kubeadm` or `talos`.
- Per provider entry: `fetch_config` mutual exclusion (`url`/`oci` vs
  `owner`/`repository`; `url` vs `oci`); `manifest_patches` vs `patches`
  (moved from `validateAddons`).
- Unknown-to-clusterctl providers need `fetch_config.repository` or
  `url`/`oci`. This check runs in the installer (it needs clusterctl's
  defaults list), so the provider surfaces it as a create error, not a plan
  error. A plan-time approximation is not attempted.
- Inventory counts now read from `topology`.

## State upgrade v1 → v2

`UpgradeState` gains a `1:` entry. To avoid duplicating the large v1 schema as
`PriorSchema`, it works on `req.RawState.JSON`:

1. Unmarshal into `map[string]json.RawMessage`.
2. For `infrastructure`, `bootstrap`, `control_plane`, `core`: read
   `.provider`, split on `:` into name and version, emit
   `{name: {version: v|null, <all other provider attrs null>}}`. Carry
   `control_plane.machine_count` and `workers.machine_count` into `topology`.
3. `addons` list: each element becomes an `addon` map entry keyed by the
   parsed name, with `version` from the string and the remaining fields
   copied; `fetch_config` gains null `owner`/`repository`.
4. Add `ipam: null`; delete `workers`, `addons`.
5. Marshal and return via `resp.DynamicValue = &tfprotov6.DynamicValue{JSON: ...}`.

Nested null placeholders are emitted for every attribute of the new provider
object so the JSON decodes against the v2 schema.

Terraform does not chain upgraders: whichever upgrader matches the stored
version must emit state in the *current* schema. The existing v0 upgrader
therefore changes to produce v2 directly. It keeps its `PriorSchema` and v0
model, and builds v2 values with shared helpers: `providerMapValue(ctx, "name:version")`
returns a one-entry `types.Map` for the provider maps, and the two machine
counts go into `topology`. The v1→v2 upgrader is the raw-JSON transform above.
Tests cover both: a v0 fixture and a v1 fixture each decode against the v2
schema with the expected map keys and versions.

## Files

| File | Change |
| --- | --- |
| `internal/capi/types.go` | `ProviderType`, `ProviderConfig`, `ProviderSet`; drop string fields from `CreateClusterOptions`/`InitOptions`. |
| `internal/capi/fetch.go` (new) | `ResolveFetchURL`, default owner/repo parsing. |
| `internal/capi/clusterctl_config.go` (new) | `overlayReader`. |
| `internal/capi/installer.go` | Always inject config + factory; IPAM; URL overrides. |
| `internal/capi/addon.go` → `internal/capi/customize.go` | Rename; key by type+name; `ProviderConfig`. |
| `internal/capi/manager.go` | Two init sites use `Providers`; template infra from `ProviderSet`. |
| `internal/capi/docker/provider.go` | Build a `ProviderSet`. |
| `internal/provider/cluster_resource.go` | Schema v2, validation, v1→v2 upgrader. |
| `internal/provider/cluster_resource_models.go` | Models, attr types, `buildCreateOptions`. |
| `internal/provider/*_test.go`, `internal/capi/*_test.go` | Update fixtures; new tests below. |
| `examples/resources/capi_cluster/*.tf`, `.test/main.tf` | New shape. |
| `docs/resources/cluster.md` | Regenerate (`go generate ./...` at repo root; restore `docs/guides`). |
| `.claude/CLAUDE.md` §2, §3.3–3.7, §4, §10 | Describe the provider maps, `fetch_config`, `topology`. |

## Tests

Unit (`internal/capi`):

- `ResolveFetchURL`: owner-only with known provider; owner+repository for
  unknown; version vs latest; url/oci verbatim; unknown without repository
  errors; every `ProviderType.ComponentsFile()` value.
- `overlayReader`: file providers + overlay merge, overlay wins on same
  name+type, other keys pass through, missing file OK.
- `ProviderSet.InitStrings`, `Customized` keyed by type+name (talos twice).
- Factory: bootstrap `talos` and control-plane `talos` get different alter
  functions.
- Installer test with the mock: IPAM strings reach `InitOptions`.

Unit (`internal/provider`):

- Schema: six maps are `MapNestedAttribute`, share the same nested object,
  `RequiresReplace`; `topology` shape.
- `buildCreateOptions`: full config produces the expected `ProviderSet`,
  topology counts, `addon = { helm = {} }` yields a bare `helm` init string.
- Validation: infrastructure exactly one; tinkerbell rules over map keys;
  fetch_config exclusivity.
- Upgrade: v1 JSON fixture (the current `.test/main.tf` shape plus an addons
  entry) → v2 JSON matches expected.

Acceptance (`TestAccClusterResource*`): configs rewritten to the new shape;
run only when the existing env gates are set.

## Rollout

Schema version 2. No releases exist, so no compatibility shims beyond the
state upgrader.
