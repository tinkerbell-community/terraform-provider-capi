# Talos BMC Bootstrapper Design

Date: 2026-09-12
Status: approved for planning

## 1. Goal

Add a second `capi.Bootstrapper` implementation that turns one bare-metal machine into a
single-node Talos Linux cluster by driving its BMC with bmclib, applying a Talos machine
configuration with the Talos machinery libraries, bootstrapping etcd, and installing addons
such as the Cilium CNI. The resulting cluster is used exactly like the kind bootstrap cluster
today: `clusterctl init` runs on it, the workload cluster template is applied to it, and after
the self-managed pivot it is torn down.

The bootstrap node is transient. Terraform stores nothing about it beyond the existing
`status.bootstrap_cluster` string. Talos secrets are generated in memory for the run and
discarded. Any failure or restart is recovered by observing the node and driving it back through
the installer, never by reading saved state.

Reference implementation being replaced: the Terraform module at
`/home/appkins/src/tfc/cluster-bootstrap/modules/bootstrap`, which sequences Redfish virtual
media, a one-time CD boot override, `talos_machine_configuration_apply`, and
`talos_machine_bootstrap` with no readiness gates and a fixed one-minute sleep before Helm.

## 2. Decisions already made

| Decision | Choice |
| --- | --- |
| Terraform surface | `capi.Bootstrapper` implementation only, selected by `management.bootstrap.type = "talos"`. No standalone resource. |
| Boot methods | Virtual media (ISO) and UEFI HTTP boot (UKI). `auto` tries virtual media and falls back to HTTP boot. No PXE. |
| Addons | Helm SDK for charts (OCI and HTTP repos) plus raw manifests through the existing `DynamicApplier`. |
| Node source | `management.bootstrap.machine` references an `inventory.machine[]` hostname and reuses its `bmc`, `network`, and `disk`. |
| State machine | Observed-state reconciler: observe, plan one action, act, repeat. Pure planner tested against a simulated node. |
| Node count | One machine. The loop is per-machine so a multi-node bootstrap cluster is a later loop, not a redesign. |

## 3. Terraform schema

New optional `management.bootstrap` nested attribute with `objectplanmodifier.RequiresReplace()`
on the whole object. `type` defaults to `kind`, so existing configurations are unchanged and the
schema version does not change.

```hcl
management = {
  self_managed = true
  bootstrap = {
    type    = "talos"            # "kind" (default) | "talos"
    machine = "cp-1"             # inventory.machine[].hostname
    boot = {
      method   = "auto"          # auto | virtual_media | http
      timeout  = "15m"           # per boot attempt, Go duration
      attempts = 3               # boot attempts before giving up
    }
    talos = {
      version      = "v1.13.6"
      architecture = "amd64"     # amd64 (default) | arm64
      endpoint     = "https://10.1.1.100:6443"   # optional, default https://<machine ip>:6443
      image = {
        factory     = "https://factory.talos.dev"  # optional
        schematic   = "<sha256 id>"                # optional precomputed schematic id
        extensions  = ["iscsi-tools", "nvme-cli"]  # official extension names
        kernel_args = ["net.ifnames=0"]
        iso         = "https://..."                # explicit override, pairs with installer
        installer   = "factory.talos.dev/metal-installer/<id>:<version>"
      }
      config_patches = [ file("cni-none.yaml") ]   # strategic merge YAML, applied in order
    }
    addons = {
      helm = [
        {
          name       = "cilium"
          namespace  = "kube-system"
          chart      = "oci://quay.io/cilium/charts/cilium"   # or a chart name with repository
          repository = ""                                       # HTTP repo URL when chart is a name
          version    = "1.18.0"
          values     = yamlencode({ kubeProxyReplacement = true })
          timeout    = "10m"
        }
      ]
      manifests = [ file("extra.yaml") ]
    }
  }
}
```

### 3.1 Attribute rules

| Attribute | Type | Required | Default | Notes |
| --- | --- | --- | --- | --- |
| `type` | string | no | `kind` | `kind` or `talos` |
| `machine` | string | when `type = talos` | | must match an `inventory.machine[].hostname` |
| `boot.method` | string | no | `auto` | `auto`, `virtual_media`, `http` |
| `boot.timeout` | string | no | `15m` | Go duration, per attempt, applies to boot and install waits |
| `boot.attempts` | int64 | no | `3` | at least 1 |
| `talos.version` | string | when `type = talos` | | `v`-prefixed semver |
| `talos.architecture` | string | no | `amd64` | `amd64` or `arm64` |
| `talos.endpoint` | string | no | `https://<ip>:6443` | cluster endpoint written into the config and certificates |
| `talos.image.factory` | string | no | `https://factory.talos.dev` | |
| `talos.image.schematic` | string | no | | conflicts with `extensions`, `kernel_args`, `iso`, `installer` |
| `talos.image.extensions` | list(string) | no | | conflicts with `iso`, `installer` |
| `talos.image.kernel_args` | list(string) | no | | conflicts with `iso`, `installer` |
| `talos.image.iso` | string | no | | must be set together with `installer` |
| `talos.image.installer` | string | no | | must be set together with `iso` |
| `talos.config_patches` | list(string) | no | | each must be valid YAML |
| `addons.helm[].name` | string | yes | | |
| `addons.helm[].namespace` | string | yes | | created if missing |
| `addons.helm[].chart` | string | yes | | `oci://` reference, or chart name used with `repository` |
| `addons.helm[].repository` | string | no | | HTTP repository URL; ignored for `oci://` charts |
| `addons.helm[].version` | string | no | | latest when empty |
| `addons.helm[].values` | string | no | | YAML |
| `addons.helm[].timeout` | string | no | `10m` | |
| `addons.manifests` | list(string) | no | | multi-document YAML strings |

Naming follows the repository schema rules: multi-word concepts become nested objects
(`boot { timeout }`, `talos { image { kernel_args } }`), simple properties inside their parent
stay flat (`config_patches`, `kernel_args`).

### 3.2 Validation

Performed in `validateLifecycleConfig` at the top of Create and Update:

- `type = talos` requires `machine`, `talos.version`, and an inventory entry with that hostname
  that has `bmc` (address, username, password), `network.ip_address`, and `disk.device`.
- `iso` and `installer` are both set or both empty. When set, `schematic`, `extensions`, and
  `kernel_args` must be empty. `schematic` also conflicts with `extensions` and `kernel_args`.
- `boot.method` is one of the three values. `boot.timeout` and every `helm[].timeout` parse as
  Go durations. `boot.attempts` is at least 1.
- `talos.architecture` is `amd64` or `arm64`.
- Every `config_patches` entry and every `helm[].values` string is valid YAML.
- Helm entries have unique `name` + `namespace` pairs.

`validateInventory` is unchanged. The bootstrap machine still counts toward inventory totals
because it is released and reclaimable after the pivot.

### 3.3 Models

New model structs and `attrTypes()` helpers in `cluster_resource_models.go`:
`ManagementBootstrapModel`, `BootModel`, `TalosModel`, `TalosImageModel`, `BootstrapAddonsModel`,
`HelmReleaseModel`. `ManagementModel` gains `Bootstrap types.Object`. `extractManagementBootstrap`
follows the existing extractor pattern.

### 3.4 Status

`status.bootstrap_cluster` holds the manager's bootstrap cluster name (`<name>-bootstrap`)
exactly as it does for kind, and is null after the pivot deletes the bootstrap cluster. No other
status field changes. No Talos secret, talosconfig, or bootstrap kubeconfig is
stored in state.

## 4. Wiring into the existing manager

`ClusterResource.Configure` keeps building a default manager. `Create` and the reconcile path in
Update call a new `managerFor(ctx, data)` helper that returns the default manager when
`management.bootstrap.type` is `kind` (or unset) and otherwise a manager built with
`capi.WithBootstrapper(talos.New(cfg))`, where `cfg` is assembled from the extracted
`management.bootstrap` model and the referenced inventory machine.

`capi.BootstrapOptions`, `Manager.CreateCluster`, the pivot (`clusterctl move`), and
`cleanupOnError` are unchanged. The manager already:

1. calls `bootstrapper.Create` and receives a `Cluster{Name, KubeconfigPath}`,
2. runs `clusterctl init` on it,
3. applies the workload template and waits,
4. pivots when `self_managed = true`,
5. calls `bootstrapper.Delete` after the pivot, or from `cleanupOnError` on failure.

`opts.KubernetesVersion` from `BootstrapOptions` is passed to Talos config generation. When empty,
Talos machinery's default for the chosen Talos version is used.

Inventory is schema-only in this repository today; nothing turns it into Tinkerbell Hardware
resources. When that applier is written, it must apply the bootstrap machine's Hardware only
after the bootstrapper has released the node, otherwise CAPT would try to claim a machine that
is still serving as the management cluster.

## 5. Package layout

New package `internal/capi/talos`.

| File | Responsibility |
| --- | --- |
| `bootstrapper.go` | `Bootstrapper` struct implementing `capi.Bootstrapper`; `Config`; `New(cfg, opts...)`; in-memory `session` (secrets bundle, talosconfig, kubeconfig path) populated by Create and consumed by Delete |
| `state.go` | `Observation`, `PowerState`, `TalosState`, `EtcdState`, `K8sState`, `History`, `Action` |
| `planner.go` | `Plan(obs Observation, hist History) Action`, pure function |
| `reconciler.go` | observe, plan, act loop with per-phase deadlines, attempt counters, and logging |
| `observe.go` | `Observer` combining BMC, Talos, and Kubernetes probes into one `Observation` |
| `bmc.go` | `BMC` interface and the bmclib implementation |
| `node.go` | `Node` interface (Talos API) and the machinery implementation, including maintenance-mode detection |
| `kube.go` | `Kube` interface (Kubernetes readiness probes) and the client-go implementation |
| `image.go` | Image Factory client and URL builders |
| `config.go` | secrets bundle, machine config generation, built-in and user patches, talosconfig |
| `addons.go` | `AddonInstaller`: Helm SDK releases and raw manifests |
| `backoff.go` | small jittered exponential backoff helper |
| `errors.go` | sentinel errors |
| `sim_test.go` | simulated node implementing `BMC`, `Node`, and `Kube` with injectable faults |

Provider-side changes live in `internal/provider/cluster_resource.go` (schema, validation,
`managerFor`) and `internal/provider/cluster_resource_models.go` (models, extractors).

## 6. Interfaces

```go
// BMC is the minimal generic surface used from bmclib. Every call opens a fresh
// bmclib session, applies per-provider timeouts, and closes it.
type BMC interface {
    PowerState(ctx context.Context) (PowerState, error)
    PowerOn(ctx context.Context) error
    PowerOff(ctx context.Context) error          // hard off
    PowerCycle(ctx context.Context) error
    SetBootDevice(ctx context.Context, dev BootDevice, persistent, efi bool) error
    InsertMedia(ctx context.Context, isoURL string) error     // SetVirtualMedia("CD", url)
    EjectMedia(ctx context.Context) error                     // SetVirtualMedia("CD", "")
    SetHTTPBootURI(ctx context.Context, uri string) error
    PostCode(ctx context.Context) (string, int, error)         // diagnostics only
}

// Node is the Talos API surface.
type Node interface {
    Probe(ctx context.Context, tc *clientconfig.Config) (TalosState, error)
    ApplyConfiguration(ctx context.Context, cfg []byte) error       // maintenance mode, reboot mode
    Bootstrap(ctx context.Context, tc *clientconfig.Config) error   // "already bootstrapped" is success
    EtcdState(ctx context.Context, tc *clientconfig.Config) (EtcdState, error)
    Kubeconfig(ctx context.Context, tc *clientconfig.Config) ([]byte, error)
    Reset(ctx context.Context, tc *clientconfig.Config) error       // non-graceful, halt
}

// Kube probes the bootstrap cluster through its kubeconfig.
type Kube interface {
    APIReachable(ctx context.Context) (bool, error)
    NodeReady(ctx context.Context) (bool, error)
}

// AddonInstaller installs Helm releases and raw manifests idempotently.
type AddonInstaller interface {
    Install(ctx context.Context, kubeconfigPath string, addons Addons) error
}
```

The bmclib implementation constructs the client as `bmclib.NewClient(address, user, pass,
bmclib.WithLogger(...), bmclib.WithPerProviderTimeout(...))`, calls `Open`, runs the one
operation, and `Close`s. Errors wrap `bmclib`'s `Metadata` (`ProvidersAttempted`,
`FailedProviderDetail`) into the message. Feature absence (bmclib's provider-implementation
error) is surfaced as `ErrUnsupported` so `auto` boot selection can fall back.

Maintenance-mode detection in `node.go`: dial `<ip>:50000` with `InsecureSkipVerify` and a
`VerifyConnection` callback that records the server certificate common name. If the `Version`
RPC succeeds and the CN equals `constants.MaintenanceServiceCommonName`
(`maintenance-service.talos.dev`), the state is `Maintenance`. Otherwise dial with the run's
talosconfig; success is `Ours`, a TLS or authentication failure is `Foreign`, and a dial timeout
or connection refusal is `Unreachable`. When the session has no talosconfig yet (`tc == nil`),
a configured node is reported as `Foreign`, which is correct: it cannot be ours.

## 7. Observation

```go
type Observation struct {
    Power        PowerState      // On, Off, Unknown
    Talos        TalosState      // Unreachable, Maintenance, Ours, Foreign
    Etcd         EtcdState       // Unknown, NotBootstrapped, Bootstrapped (only meaningful when Ours)
    Kubernetes   K8sState        // Unreachable, Reachable, NodeReady
    ObservedAt   time.Time
}

type History struct {
    BootAttempts     int  // BootInstaller cycles started
    ConfigApplied    bool // ApplyConfiguration succeeded this run
    WentDownAfterApply bool // Talos API observed Unreachable after ConfigApplied
    MediaAttached    bool // installer media is (believed) attached
    AddonsInstalled  bool
}
```

BMC-reported power is recorded for logging and for choosing between "power on" and
"power cycle", but every transition is confirmed through the Talos API, because some BMCs report
`On` unconditionally.

## 8. Planner

`Plan` returns exactly one `Action`:

| Talos | Other conditions | Action |
| --- | --- | --- |
| `Unreachable` or `Foreign` | `BootAttempts < attempts` | `BootInstaller` |
| `Unreachable` or `Foreign` | `BootAttempts >= attempts` | `Fail(ErrAttemptsExhausted)` |
| `Maintenance` | `!ConfigApplied` | `ApplyConfig` |
| `Maintenance` | `ConfigApplied` | `RebootToDisk` (detach media, one-time disk boot, power cycle) |
| `Ours` | `MediaAttached` | `DetachMedia` |
| `Ours` | `Etcd != Bootstrapped` | `BootstrapEtcd` |
| `Ours` | `Kubernetes == Unreachable` | `WaitKubernetes` |
| `Ours` | `!AddonsInstalled` | `InstallAddons` |
| `Ours` | addons given and `Kubernetes != NodeReady` | `WaitReady` |
| `Ours` | otherwise | `Done` |

`ApplyConfig` on a `Maintenance` node always installs with `wipe: true`, so a node in maintenance
mode from a disk-installed Talos (for example after an interrupted earlier run) is handled the
same way as one booted from the ISO.

## 9. Actions

Each action is a short sequence of idempotent steps. Steps are verified by the next observation,
not by BMC return values.

**BootInstaller**
1. `PowerOff`; wait up to 60 s for Talos `Unreachable` (skip if already unreachable).
2. `EjectMedia`; `SetHTTPBootURI("")` best effort.
3. Attach media by method: `virtual_media` → `InsertMedia(isoURL)` then `SetBootDevice(cdrom, once, efi)`;
   `http` → `SetHTTPBootURI(ukiURL)` then `SetBootDevice(uefi_http, once, efi)`;
   `auto` → virtual media first, on `ErrUnsupported` use `http`, on both unsupported `Fail(ErrNoBootMethod)`.
4. `PowerOn` (or `PowerCycle` if the BMC still reports `On`).
5. Wait up to `boot.timeout` for `Maintenance`. On timeout, collect `PostCode`, bmclib metadata,
   and the last observations into the attempt log, increment `BootAttempts`, and return.

**ApplyConfig**
1. Generate the config (section 10) with a fresh secrets bundle if the session has none.
2. `ApplyConfiguration` in `REBOOT` mode; set `ConfigApplied`.
3. Wait up to 2 min for `Unreachable`; set `WentDownAfterApply`. If the node never goes down,
   clear `ConfigApplied`, consume one boot attempt, and return so the next iteration re-applies.
4. Wait up to `boot.timeout` for `Ours`. If `Maintenance` is seen instead, return so the planner
   picks `RebootToDisk`.

**RebootToDisk**: `EjectMedia`, `SetHTTPBootURI("")`, `SetBootDevice(disk, once, efi)`,
`PowerCycle`, wait up to `boot.timeout` for `Ours`. Counts as a boot attempt.

**DetachMedia**: `EjectMedia` and `SetHTTPBootURI("")`, best effort, then clear `MediaAttached`.

**BootstrapEtcd**: `Bootstrap`; "already bootstrapped" errors are success. Wait up to 10 min for
`Bootstrapped`.

**WaitKubernetes**: fetch the kubeconfig through the Talos API, rewrite the server to
`https://<ip>:6443` (the cluster endpoint may be a VIP that does not exist yet), write it to
`<tmpdir>/<name>-bootstrap-kubeconfig` with mode 0600, wait up to 10 min for `APIReachable`.

**InstallAddons**: Helm releases in list order, then manifests in list order; set
`AddonsInstalled`. Overall deadline 15 min.

**WaitReady**: wait up to 10 min for `NodeReady`. Only planned when addons were given, because a
node without a CNI never becomes Ready.

**Done**: write the talosconfig to `<tmpdir>/<name>-bootstrap-talosconfig` (0600, for operator
debugging), return `capi.Cluster{Name, KubeconfigPath}`.

## 10. Configuration generation

1. `secrets.NewBundle(secrets.NewFixedClock(now), versionContract)` once per Create.
2. `generate.NewInput(clusterName, endpoint, kubernetesVersion,
   WithSecretsBundle, WithVersionContract, WithInstallDisk(disk.device),
   WithInstallImage(installerRef), WithAllowSchedulingOnControlPlanes(true),
   WithEndpointList([]string{ip}))`, then `Config(machine.TypeControlPlane)`.
3. Built-in patch: `machine.install.wipe: true`.
4. User `config_patches` via `configpatcher.LoadPatches` and `configpatcher.Apply`, in order.
5. `EncodeBytes()` for `ApplyConfiguration`.
6. `Talosconfig()` from the same input, endpoints `[ip]`.

The bootstrapper does not set CNI, kube-proxy, or any other cluster option. Those stay in
`config_patches`, as in the reference module, and the docs ship a Cilium-ready patch example
(`cluster.network.cni.name: none`, `cluster.proxy.disabled: true`).

## 11. Images

- Explicit `iso` + `installer` are used verbatim.
- Otherwise `schematic` is used when given, else created with `POST <factory>/schematics` and body
  `{customization: {systemExtensions: {officialExtensions: extensions}, extraKernelArgs: kernel_args}}`;
  the response `id` is used.
- URLs: ISO `<factory>/image/<id>/<version>/metal-<arch>.iso`, UKI
  `<factory>/image/<id>/<version>/metal-<arch>-uki.efi`, installer
  `<factory host>/metal-installer/<id>:<version>`.

## 12. Addons

Helm: `action.Configuration` initialised with a `RESTClientGetter` built from the bootstrap
kubeconfig path; `registry.NewClient` for `oci://` charts; `repo` + `downloader` for HTTP
repositories. For each release: `action.NewGet` to detect an existing release, then
`action.NewInstall` (with `CreateNamespace`, `Wait`, `Timeout`) or `action.NewUpgrade`. This makes
retries idempotent.

Manifests: each string is applied through `capi.DynamicApplier.Apply` (server-side apply, already
implemented).

## 13. Delete and Exists

**Delete** (called by the manager after the pivot and from `cleanupOnError`):
1. With a session and `Ours` observed: `Reset` with `Graceful: false`, `Reboot: false`
   (single-node etcd cannot leave gracefully; halt after wipe). Wait up to 5 min for `Unreachable`.
2. `PowerOff`, `EjectMedia`, `SetHTTPBootURI("")`, all best effort.
3. Remove the temp kubeconfig and talosconfig files.
Without a session (provider restarted, or `DeleteCluster` with `DeleteBootstrap`), only steps 2–3
run. Errors are returned; the manager already logs and continues.

**Exists** returns `true` only when `Probe` with the session talosconfig reports `Ours`. It is
implemented for interface completeness; `Create` always runs the reconciler because the
reconciler is idempotent from any state.

## 14. Fault tolerance rules

- Fresh bmclib session per action; `WithPerProviderTimeout(30s)`; three retries with jittered
  exponential backoff (500 ms base, 5 s cap) on any error other than `ErrUnsupported` or context
  cancellation.
- Every wait is a poll (5 s interval) with its own deadline. All deadlines are bounded by the
  context passed from the manager, which carries `wait.timeout`.
- BMC-reported power state never proves a transition. Power off is confirmed by Talos
  `Unreachable`; power on is confirmed by `Maintenance` or `Ours`.
- Attempt accounting: `BootInstaller` and `RebootToDisk` each consume one of `boot.attempts`.
  Exhaustion fails with `ErrAttemptsExhausted` and the attempt log.
- Context cancellation exits the loop immediately and leaves the node as-is. The next `Create`
  converges from observation.
- A `Foreign` node named in `machine` is re-imaged with wipe. The trust model is the same as
  Tinkerbell's: naming a machine's BMC is consent to provision it.
- Every iteration logs one line with the observation summary and the chosen action through the
  manager's logger.

## 15. Errors

`internal/capi/talos/errors.go`: `ErrMachineNotFound`, `ErrUnsupported`, `ErrNoBootMethod`,
`ErrBootTimeout`, `ErrInstallTimeout`, `ErrBootstrapTimeout`, `ErrKubernetesTimeout`,
`ErrAddons`, `ErrReadyTimeout`, `ErrAttemptsExhausted`. All are returned wrapped in
`capi.BootstrapError{ClusterName, Operation, Err}` with the last observation and bmclib metadata
in the message.

## 16. Testing

- `planner_test.go`: table-driven, every row of section 8, no I/O.
- `sim_test.go` + `reconciler_test.go`: an in-memory node implementing `BMC`, `Node`, and `Kube`
  whose state transitions when actions are applied, with injectable faults. Scenarios: happy path
  via virtual media; happy path via HTTP boot after virtual media unsupported; BMC always reports
  `On`; firmware boots the installer twice; insert fails once then succeeds; maintenance never
  appears until attempt two; `Foreign` node re-imaged; context cancelled mid-boot; Delete with and
  without a session; bootstrap already done.
- `image_test.go`: `httptest` server for `POST /schematics`; URL builders for both architectures;
  explicit override precedence.
- `config_test.go`: generated config contains install disk, installer image, `wipe: true`,
  `allowSchedulingOnControlPlanes: true`, endpoint; user patches applied in order; talosconfig
  endpoint is the node IP.
- `addons_test.go`: fake Helm client verifying ordering, install-vs-upgrade decision, manifest
  application after charts. A real Helm SDK test is env-gated.
- `bmc_test.go`: error wrapping includes bmclib metadata; `ErrUnsupported` mapping.
- Provider tests: schema shape (`management.bootstrap` is a `SingleNestedAttribute` with
  `RequiresReplace`), model extraction, validation cases from section 3.2, `managerFor` selection.
- Acceptance tests against real hardware are gated by `TF_ACC` and `CAPI_TALOS_BMC_ADDRESS`,
  `CAPI_TALOS_BMC_USERNAME`, `CAPI_TALOS_BMC_PASSWORD`, `CAPI_TALOS_NODE_IP`, `CAPI_TALOS_DISK`,
  and skip otherwise.

## 17. Dependencies

Added to `go.mod` on top of the pending k8s 0.36.4 / cluster-api 1.14.0 bump:

```
require (
    github.com/bmc-toolbox/bmclib/v2 v2.3.6-0.20260724022505-33fe4e06a8da
    github.com/siderolabs/talos/pkg/machinery v1.13.6
    helm.sh/helm/v3 v3.21.4
)

replace (
    github.com/bmc-toolbox/bmclib/v2 => github.com/tinkerbell-community/bmclib/v2 v2.0.0-20260910214435-7baaf88e8399
    github.com/jacobweinstock/iamt => github.com/tinkerbell-community/iamt v0.0.0-20260910214403-be779a24a30d
)
```

Helm v3.21.4 targets k8s 0.36.x, matching the repository. Backoff is a local helper, not a
dependency.

## 18. Documentation

- `docs/resources/cluster.md` regenerated with `tfplugindocs`.
- New example under `examples/` showing a Tinkerbell cluster with a Talos BMC bootstrap node and
  a Cilium Helm addon plus the CNI-none patch.
- `.claude/CLAUDE.md` section 3.2 extended with the `management.bootstrap` schema.

## 19. Out of scope

- Multi-node bootstrap clusters.
- PXE boot.
- Turning `inventory` into Tinkerbell Hardware, Machine, and Secret resources.
- Persisting Talos secrets, talosconfig, or the bootstrap kubeconfig in Terraform state.
- Reusing the bootstrap node's Talos identity after the pivot.
- BMC-specific tuning options (ports, cipher suites, driver preference). bmclib autodetection
  is used; options can be added to `inventory.machine[].bmc` later if needed.
