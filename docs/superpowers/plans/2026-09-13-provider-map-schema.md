# Provider-Map Schema (v2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `provider = "name:version"` strings on `capi_cluster` with six provider maps (`core`, `infrastructure`, `bootstrap`, `control_plane`, `ipam`, `addon`) sharing one customization object, plus an owner-only `fetch_config` and a `topology` block for machine counts.

**Architecture:** `internal/capi` gains a `ProviderSet` (type → configs) that feeds `clusterctl init`, a `ResolveFetchURL` that builds GitHub release URLs from owner/repository/version with clusterctl's built-in defaults, and an `overlayReader` that registers those URLs with clusterctl's config without global state. `internal/provider` swaps the object attributes for `MapNestedAttribute`s, adds `topology`, and upgrades v0 and v1 state to v2.

**Tech Stack:** Go 1.2x, terraform-plugin-framework v1.19, terraform-plugin-go tfprotov6, cluster-api v1.14 clusterctl client library, spf13/viper (already an indirect dependency).

**Spec:** `docs/superpowers/specs/2026-09-13-provider-map-schema-design.md`

## Global Constraints

- All attribute names are snake_case (`fetch_config`, `feature_gates`); no camelCase from the Helm chart.
- Schema `Version: 2`. Both v0 and v1 upgraders must emit v2 state.
- Provider maps use `mapplanmodifier.RequiresReplace()`; `topology` counts stay mutable.
- No new direct dependency except promoting `github.com/spf13/viper` from indirect to direct.
- Lint gate: `rtk proxy golangci-lint run ./...` must exit 0 before each commit (RTK masks the exit code otherwise).
- Run unit tests with `go test ./internal/... -run '<Name>' -count=1`. Acceptance tests (`TestAcc*`) stay env-gated and are not run.
- Docs regenerate with `go generate ./...` from the repo root; afterwards `git checkout docs/guides` restores guides tfplugindocs deletes.

---

## Amendment A: `topology` mirrors the Cluster CRD (supersedes the `topology` parts of Tasks 1, 5–9)

Requested after the plan was drafted: `topology.workers` carries a
`machine_deployments` list shaped like `Cluster.spec.topology.workers.machineDeployments[]`,
and counts are called `replicas` as in the CRD. Wherever a task below says
`machine_count`, `MachineCountModel`, `machineCountObject`, `machineCountAttrTypes`
or `WorkerMachineCount` on `CreateClusterOptions`, use the definitions here instead.

**Schema (Task 6):**

```go
"topology": schema.SingleNestedAttribute{
	MarkdownDescription: "Workload cluster topology. Mirrors `Cluster.spec.topology`.",
	Optional:            true,
	Attributes: map[string]schema.Attribute{
		"control_plane": schema.SingleNestedAttribute{
			MarkdownDescription: "Control plane topology.",
			Optional:            true,
			Attributes: map[string]schema.Attribute{
				"replicas": schema.Int64Attribute{
					MarkdownDescription: "Number of control plane machines. Use an odd number for HA (1, 3, 5).",
					Optional:            true,
				},
			},
		},
		"workers": schema.SingleNestedAttribute{
			MarkdownDescription: "Worker topology.",
			Optional:            true,
			Attributes: map[string]schema.Attribute{
				"machine_deployments": schema.ListNestedAttribute{
					MarkdownDescription: "Worker MachineDeployments, as in `Cluster.spec.topology.workers.machineDeployments`. clusterctl flavor templates expose a single `WORKER_MACHINE_COUNT`, which is taken from the first entry's `replicas`.",
					Optional:            true,
					NestedObject: schema.NestedAttributeObject{
						Attributes: map[string]schema.Attribute{
							"name": schema.StringAttribute{
								MarkdownDescription: "MachineDeployment name. Must be unique within the cluster.",
								Required:            true,
							},
							"class": schema.StringAttribute{
								MarkdownDescription: "ClusterClass worker class (`machineDeploymentClass` name). Unused by flavor templates.",
								Optional:            true,
							},
							"replicas": schema.Int64Attribute{
								MarkdownDescription: "Number of machines.",
								Optional:            true,
							},
							"failure_domain": schema.StringAttribute{
								MarkdownDescription: "Failure domain the machines are placed in.",
								Optional:            true,
							},
							"metadata": schema.SingleNestedAttribute{
								MarkdownDescription: "Labels and annotations applied to the machines.",
								Optional:            true,
								Attributes: map[string]schema.Attribute{
									"labels":      schema.MapAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "Machine labels."},
									"annotations": schema.MapAttribute{ElementType: types.StringType, Optional: true, MarkdownDescription: "Machine annotations."},
								},
							},
						},
					},
				},
			},
		},
	},
},
```

**Models and attr types (Task 6, `cluster_resource_models.go`):**

```go
type TopologyModel struct {
	ControlPlane types.Object `tfsdk:"control_plane"` // ControlPlaneTopologyModel
	Workers      types.Object `tfsdk:"workers"`       // WorkersTopologyModel
}
type ControlPlaneTopologyModel struct {
	Replicas types.Int64 `tfsdk:"replicas"`
}
type WorkersTopologyModel struct {
	MachineDeployments types.List `tfsdk:"machine_deployments"` // []MachineDeploymentModel
}
type MachineDeploymentModel struct {
	Name          types.String `tfsdk:"name"`
	Class         types.String `tfsdk:"class"`
	Replicas      types.Int64  `tfsdk:"replicas"`
	FailureDomain types.String `tfsdk:"failure_domain"`
	Metadata      types.Object `tfsdk:"metadata"` // TopologyMetadataModel
}
type TopologyMetadataModel struct {
	Labels      types.Map `tfsdk:"labels"`
	Annotations types.Map `tfsdk:"annotations"`
}

func topologyAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"control_plane": types.ObjectType{AttrTypes: controlPlaneTopologyAttrTypes()},
		"workers":       types.ObjectType{AttrTypes: workersTopologyAttrTypes()},
	}
}
func controlPlaneTopologyAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{"replicas": types.Int64Type}
}
func workersTopologyAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{"machine_deployments": types.ListType{ElemType: types.ObjectType{AttrTypes: machineDeploymentAttrTypes()}}}
}
func machineDeploymentAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"name":           types.StringType,
		"class":          types.StringType,
		"replicas":       types.Int64Type,
		"failure_domain": types.StringType,
		"metadata":       types.ObjectType{AttrTypes: topologyMetadataAttrTypes()},
	}
}
func topologyMetadataAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"labels":      types.MapType{ElemType: types.StringType},
		"annotations": types.MapType{ElemType: types.StringType},
	}
}

// extractTopology returns the control plane replicas and the machine deployments.
func extractTopology(ctx context.Context, data *ClusterResourceModel) (cp *int64, mds []capi.MachineDeploymentTopology, diags diag.Diagnostics) {
	if data.Topology.IsNull() || data.Topology.IsUnknown() {
		return nil, nil, nil
	}
	var topo TopologyModel
	diags.Append(data.Topology.As(ctx, &topo, basetypes.ObjectAsOptions{})...)
	if diags.HasError() {
		return nil, nil, diags
	}
	if !topo.ControlPlane.IsNull() && !topo.ControlPlane.IsUnknown() {
		var c ControlPlaneTopologyModel
		diags.Append(topo.ControlPlane.As(ctx, &c, basetypes.ObjectAsOptions{})...)
		if !c.Replicas.IsNull() && !c.Replicas.IsUnknown() {
			v := c.Replicas.ValueInt64()
			cp = &v
		}
	}
	if !topo.Workers.IsNull() && !topo.Workers.IsUnknown() {
		var w WorkersTopologyModel
		diags.Append(topo.Workers.As(ctx, &w, basetypes.ObjectAsOptions{})...)
		if !w.MachineDeployments.IsNull() && !w.MachineDeployments.IsUnknown() {
			var models []MachineDeploymentModel
			diags.Append(w.MachineDeployments.ElementsAs(ctx, &models, false)...)
			for _, m := range models {
				md := capi.MachineDeploymentTopology{Name: m.Name.ValueString()}
				if !m.Class.IsNull() {
					md.Class = m.Class.ValueString()
				}
				if !m.Replicas.IsNull() && !m.Replicas.IsUnknown() {
					v := m.Replicas.ValueInt64()
					md.Replicas = &v
				}
				if !m.FailureDomain.IsNull() {
					md.FailureDomain = m.FailureDomain.ValueString()
				}
				if !m.Metadata.IsNull() && !m.Metadata.IsUnknown() {
					var meta TopologyMetadataModel
					diags.Append(m.Metadata.As(ctx, &meta, basetypes.ObjectAsOptions{})...)
					if !meta.Labels.IsNull() {
						md.Labels = map[string]string{}
						diags.Append(meta.Labels.ElementsAs(ctx, &md.Labels, false)...)
					}
					if !meta.Annotations.IsNull() {
						md.Annotations = map[string]string{}
						diags.Append(meta.Annotations.ElementsAs(ctx, &md.Annotations, false)...)
					}
				}
				mds = append(mds, md)
			}
		}
	}
	return cp, mds, diags
}

// legacyTopologyObject builds a topology from the pre-v2 two counts: the
// worker count becomes one MachineDeployment named "md-0". Used by upgraders.
func legacyTopologyObject(ctx context.Context, cpCount, workerCount types.Int64) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics
	cpObj := types.ObjectNull(controlPlaneTopologyAttrTypes())
	if !cpCount.IsNull() && !cpCount.IsUnknown() {
		var d diag.Diagnostics
		cpObj, d = types.ObjectValueFrom(ctx, controlPlaneTopologyAttrTypes(), ControlPlaneTopologyModel{Replicas: cpCount})
		diags.Append(d...)
	}
	wObj := types.ObjectNull(workersTopologyAttrTypes())
	if !workerCount.IsNull() && !workerCount.IsUnknown() {
		md := MachineDeploymentModel{
			Name: types.StringValue("md-0"), Class: types.StringNull(), Replicas: workerCount,
			FailureDomain: types.StringNull(), Metadata: types.ObjectNull(topologyMetadataAttrTypes()),
		}
		list, d := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: machineDeploymentAttrTypes()}, []MachineDeploymentModel{md})
		diags.Append(d...)
		wObj, d = types.ObjectValueFrom(ctx, workersTopologyAttrTypes(), WorkersTopologyModel{MachineDeployments: list})
		diags.Append(d...)
	}
	if cpObj.IsNull() && wObj.IsNull() {
		return types.ObjectNull(topologyAttrTypes()), diags
	}
	obj, d := types.ObjectValueFrom(ctx, topologyAttrTypes(), TopologyModel{ControlPlane: cpObj, Workers: wObj})
	diags.Append(d...)
	return obj, diags
}
```

In `buildCreateOptions`: `opts.ControlPlaneMachineCount, opts.MachineDeployments = cp, mds`.

**Backend (Task 1 / Task 5, `internal/capi`):** add to `types.go`

```go
// MachineDeploymentTopology mirrors Cluster.spec.topology.workers.machineDeployments[].
type MachineDeploymentTopology struct {
	Name          string
	Class         string
	Replicas      *int64
	FailureDomain string
	Labels        map[string]string
	Annotations   map[string]string
}
```

`CreateClusterOptions.WorkerMachineCount` is replaced by `MachineDeployments []MachineDeploymentTopology`
with the doc comment "Worker MachineDeployments. clusterctl flavor templates expose a single
WORKER_MACHINE_COUNT, filled from the first entry's Replicas." Add
`func (o CreateClusterOptions) WorkerMachineCount() *int64` returning
`o.MachineDeployments[0].Replicas` when present, else nil; `manager.go` passes
`WorkerMachineCount: opts.WorkerMachineCount()` to `TemplateOptions`. Tests in
`manager_test.go` / `docker` that set `WorkerMachineCount:` set
`MachineDeployments: []MachineDeploymentTopology{{Name: "md-0", Replicas: &n}}` instead.

**Validation (Task 7):** worker count for `validateInventory` is the sum of
`Replicas` over `mds`; add a duplicate-name check:

```go
	seen := map[string]bool{}
	for _, md := range mds {
		if seen[md.Name] {
			diags.AddError("Duplicate machine deployment", fmt.Sprintf("topology.workers.machine_deployments name %q appears more than once.", md.Name))
		}
		seen[md.Name] = true
	}
```

**Upgraders (Task 8):** the v0 upgrader calls
`v2.Topology, d = legacyTopologyObject(ctx, v0.ControlPlaneMachineCount, v0.WorkerMachineCount)`.
In `upgradeV1JSON`, replace `machineCountJSON` with:

```go
func controlPlaneTopologyJSON(count json.RawMessage) json.RawMessage {
	if len(count) == 0 || string(count) == "null" {
		return nullJSON
	}
	b, _ := json.Marshal(map[string]json.RawMessage{"replicas": count})
	return b
}

func workersTopologyJSON(count json.RawMessage) json.RawMessage {
	if len(count) == 0 || string(count) == "null" {
		return nullJSON
	}
	md := map[string]json.RawMessage{
		"name": json.RawMessage(`"md-0"`), "class": nullJSON, "replicas": count,
		"failure_domain": nullJSON, "metadata": nullJSON,
	}
	b, _ := json.Marshal(map[string]interface{}{"machine_deployments": []interface{}{md}})
	return b
}
```

and the topology expectation in `TestUpgradeV1JSON` becomes
`{"control_plane":{"replicas":1},"workers":{"machine_deployments":[{"class":null,"failure_domain":null,"metadata":null,"name":"md-0","replicas":2}]}}`.

**Tests (Task 6):** `TestBuildCreateOptions_Full` builds
`topology = { control_plane = { replicas = 3 }, workers = { machine_deployments = [{ name = "md-0", replicas = 5, metadata = { labels = { tier = "worker" } } }] } }`
and asserts `opts.ControlPlaneMachineCount == 3`, `len(opts.MachineDeployments) == 1`,
`*opts.MachineDeployments[0].Replicas == 5`, `opts.MachineDeployments[0].Labels["tier"] == "worker"`,
`*opts.WorkerMachineCount() == 5`. The schema test checks `topology.control_plane.replicas` (Int64)
and `topology.workers.machine_deployments` (ListNestedAttribute with `name`, `class`, `replicas`, `failure_domain`, `metadata`).

**Examples/docs (Task 9):** every `topology` block uses
`control_plane = { replicas = N }` and
`workers = { machine_deployments = [{ name = "md-0", replicas = M }] }`.

---

### Task 1: ProviderType, ProviderConfig, ProviderSet in `internal/capi/types.go`

**Files:**
- Modify: `internal/capi/types.go` (replace `AddonConfig`, `FetchConfig`; edit `InitOptions`, `CreateClusterOptions`)
- Modify: `internal/capi/types_test.go`

**Interfaces:**
- Produces:
  ```go
  type ProviderType string
  const (
      ProviderTypeCore           ProviderType = "core"
      ProviderTypeInfrastructure ProviderType = "infrastructure"
      ProviderTypeBootstrap      ProviderType = "bootstrap"
      ProviderTypeControlPlane   ProviderType = "control-plane"
      ProviderTypeIPAM           ProviderType = "ipam"
      ProviderTypeAddon          ProviderType = "addon"
  )
  func AllProviderTypes() []ProviderType
  func (t ProviderType) ComponentsFile() string            // "<t>-components.yaml"
  func (t ProviderType) Clusterctl() clusterctlv1.ProviderType
  func ProviderTypeFromClusterctl(clusterctlv1.ProviderType) (ProviderType, bool)

  type FetchConfig struct{ Owner, Repository, URL, OCI string }
  type ProviderConfig struct {
      Name, Version string; Type ProviderType; FetchConfig *FetchConfig
      ConfigVariables, SecretConfigVariables map[string]string
      Deployment *DeploymentConfig; Manager *ManagerConfig
      AdditionalManifests string; ManifestPatches []string; Patches []PatchConfig
  }
  func (p ProviderConfig) InitString() string
  func (p ProviderConfig) HasCustomizations() bool
  func (p ProviderConfig) NeedsURLOverride() bool

  type ProviderKey struct{ Type ProviderType; Name string }
  type ProviderSet map[ProviderType][]ProviderConfig
  func (s ProviderSet) Add(p ProviderConfig)
  func (s ProviderSet) Infrastructure() (ProviderConfig, bool)
  func (s ProviderSet) InitStrings(t ProviderType) []string
  func (s ProviderSet) Customized() map[ProviderKey]ProviderConfig
  func (s ProviderSet) All() []ProviderConfig   // ordered by AllProviderTypes then insertion
  ```
- `InitOptions{ Kubeconfig string; Providers ProviderSet }`
- `CreateClusterOptions` loses `InfrastructureProvider`, `BootstrapProvider`, `ControlPlaneProvider`, `CoreProvider`, `Addons`; gains `Providers ProviderSet`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/capi/types_test.go`:

```go
func TestProviderType_ComponentsFile(t *testing.T) {
	cases := map[ProviderType]string{
		ProviderTypeCore:           "core-components.yaml",
		ProviderTypeInfrastructure: "infrastructure-components.yaml",
		ProviderTypeBootstrap:      "bootstrap-components.yaml",
		ProviderTypeControlPlane:   "control-plane-components.yaml",
		ProviderTypeIPAM:           "ipam-components.yaml",
		ProviderTypeAddon:          "addon-components.yaml",
	}
	for typ, want := range cases {
		if got := typ.ComponentsFile(); got != want {
			t.Errorf("%s.ComponentsFile() = %q, want %q", typ, got, want)
		}
		back, ok := ProviderTypeFromClusterctl(typ.Clusterctl())
		if !ok || back != typ {
			t.Errorf("round trip through clusterctl type failed for %s: %q %v", typ, back, ok)
		}
	}
}

func TestProviderConfig_InitString(t *testing.T) {
	if got := (ProviderConfig{Name: "helm"}).InitString(); got != "helm" {
		t.Errorf("got %q", got)
	}
	if got := (ProviderConfig{Name: "talos", Version: "v0.8.2"}).InitString(); got != "talos:v0.8.2" {
		t.Errorf("got %q", got)
	}
}

func TestProviderConfig_NeedsURLOverride(t *testing.T) {
	if (ProviderConfig{Name: "helm"}).NeedsURLOverride() {
		t.Error("bare provider must not need an override")
	}
	if !(ProviderConfig{Name: "helm", Version: "v0.2.12"}).NeedsURLOverride() {
		t.Error("versioned provider needs an override")
	}
	if !(ProviderConfig{Name: "helm", FetchConfig: &FetchConfig{Owner: "me"}}).NeedsURLOverride() {
		t.Error("fetch config needs an override")
	}
}

func TestProviderConfig_HasCustomizations_IgnoresFetchAndVersion(t *testing.T) {
	p := ProviderConfig{Name: "helm", Version: "v1", FetchConfig: &FetchConfig{Owner: "me"}}
	if p.HasCustomizations() {
		t.Error("version/fetch config are not component customizations")
	}
	p.Manager = &ManagerConfig{FeatureGates: map[string]bool{"X": true}}
	if !p.HasCustomizations() {
		t.Error("manager config is a customization")
	}
}

func TestProviderSet(t *testing.T) {
	s := ProviderSet{}
	s.Add(ProviderConfig{Type: ProviderTypeInfrastructure, Name: "tinkerbell", Version: "v0.7.9"})
	s.Add(ProviderConfig{Type: ProviderTypeBootstrap, Name: "talos", Manager: &ManagerConfig{Verbosity: ptrInt64(2)}})
	s.Add(ProviderConfig{Type: ProviderTypeControlPlane, Name: "talos", Manager: &ManagerConfig{Verbosity: ptrInt64(5)}})
	s.Add(ProviderConfig{Type: ProviderTypeAddon, Name: "helm"})

	infra, ok := s.Infrastructure()
	if !ok || infra.Name != "tinkerbell" {
		t.Fatalf("Infrastructure() = %+v, %v", infra, ok)
	}
	if got := s.InitStrings(ProviderTypeInfrastructure); len(got) != 1 || got[0] != "tinkerbell:v0.7.9" {
		t.Errorf("InitStrings(infra) = %v", got)
	}
	if got := s.InitStrings(ProviderTypeIPAM); len(got) != 0 {
		t.Errorf("InitStrings(ipam) = %v, want empty", got)
	}
	c := s.Customized()
	if len(c) != 2 {
		t.Fatalf("Customized() has %d entries, want 2", len(c))
	}
	if *c[ProviderKey{ProviderTypeBootstrap, "talos"}].Manager.Verbosity != 2 ||
		*c[ProviderKey{ProviderTypeControlPlane, "talos"}].Manager.Verbosity != 5 {
		t.Error("bootstrap and control-plane talos must be keyed independently")
	}
	if got := s.All(); len(got) != 4 || got[0].Name != "tinkerbell" || got[3].Name != "helm" {
		t.Errorf("All() order = %v", got)
	}
}

func ptrInt64(v int64) *int64 { return &v }
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/capi/ -run 'TestProviderType|TestProviderConfig|TestProviderSet' -count=1`
Expected: compile errors (`undefined: ProviderType` etc.).

- [ ] **Step 3: Implement the types**

In `internal/capi/types.go`, add the import `clusterctlv1 "sigs.k8s.io/cluster-api/cmd/clusterctl/api/v1alpha3"`, then replace the `InitOptions` body with:

```go
// InitOptions configures CAPI provider installation.
type InitOptions struct {
	// Kubeconfig is the path to the cluster's kubeconfig.
	Kubeconfig string

	// Providers lists every provider to install, grouped by type. Entries with
	// a version or fetch config register a URL override with clusterctl;
	// entries with customizations have their component YAML altered through
	// the injected repository factory.
	Providers ProviderSet
}
```

In `CreateClusterOptions` delete the `InfrastructureProvider`, `BootstrapProvider`, `ControlPlaneProvider`, `CoreProvider` and `Addons` fields and add:

```go
	// Providers are the CAPI providers to install, grouped by type. Exactly one
	// infrastructure provider is required; it also selects the cluster template.
	Providers ProviderSet
```

Replace the `AddonConfig` type, its `HasCustomizations` method, and the `FetchConfig` type with:

```go
// ProviderType is a CAPI provider category, spelled the way clusterctl names
// component files (e.g. "control-plane" -> control-plane-components.yaml).
type ProviderType string

const (
	ProviderTypeCore           ProviderType = "core"
	ProviderTypeInfrastructure ProviderType = "infrastructure"
	ProviderTypeBootstrap      ProviderType = "bootstrap"
	ProviderTypeControlPlane   ProviderType = "control-plane"
	ProviderTypeIPAM           ProviderType = "ipam"
	ProviderTypeAddon          ProviderType = "addon"
)

// AllProviderTypes returns every ProviderType in clusterctl install order.
func AllProviderTypes() []ProviderType {
	return []ProviderType{
		ProviderTypeCore, ProviderTypeBootstrap, ProviderTypeControlPlane,
		ProviderTypeInfrastructure, ProviderTypeIPAM, ProviderTypeAddon,
	}
}

// ComponentsFile returns the release asset name clusterctl fetches for this type.
func (t ProviderType) ComponentsFile() string {
	return string(t) + "-components.yaml"
}

// Clusterctl maps to the clusterctl API enum.
func (t ProviderType) Clusterctl() clusterctlv1.ProviderType {
	switch t {
	case ProviderTypeCore:
		return clusterctlv1.CoreProviderType
	case ProviderTypeInfrastructure:
		return clusterctlv1.InfrastructureProviderType
	case ProviderTypeBootstrap:
		return clusterctlv1.BootstrapProviderType
	case ProviderTypeControlPlane:
		return clusterctlv1.ControlPlaneProviderType
	case ProviderTypeIPAM:
		return clusterctlv1.IPAMProviderType
	case ProviderTypeAddon:
		return clusterctlv1.AddonProviderType
	}
	return clusterctlv1.ProviderTypeUnknown
}

// ProviderTypeFromClusterctl is the inverse of Clusterctl. ok is false for
// types this provider does not manage (runtime extensions, unknown).
func ProviderTypeFromClusterctl(t clusterctlv1.ProviderType) (ProviderType, bool) {
	for _, pt := range AllProviderTypes() {
		if pt.Clusterctl() == t {
			return pt, true
		}
	}
	return "", false
}

// FetchConfig configures where provider components are fetched from.
// URL and OCI are verbatim clusterctl references. Owner and Repository build
// a GitHub releases URL together with the provider version; both default from
// clusterctl's built-in entry for the provider when unset.
type FetchConfig struct {
	Owner      string
	Repository string
	URL        string
	OCI        string
}

// ProviderConfig carries the full configuration for one CAPI provider,
// modeled after the cluster-api-operator provider CRDs. Customizations are
// applied natively by wrapping the clusterctl client's repository factory;
// the operator itself is not required.
type ProviderConfig struct {
	// Name is the provider name as clusterctl knows it (e.g. "tinkerbell").
	Name string

	// Type is the provider category.
	Type ProviderType

	// Version is the release tag (e.g. "v0.7.9"). Empty means clusterctl's latest.
	Version string

	// FetchConfig overrides where components are fetched from. Nil uses clusterctl defaults.
	FetchConfig *FetchConfig

	// ConfigVariables are template variables injected into the provider's
	// component YAML during processing (${VAR} substitution).
	ConfigVariables map[string]string

	// SecretConfigVariables are sensitive template variables.
	SecretConfigVariables map[string]string

	// Deployment customizes the provider controller deployment.
	Deployment *DeploymentConfig

	// Manager configures the controller manager.
	Manager *ManagerConfig

	// AdditionalManifests is inline YAML applied along with the provider components.
	AdditionalManifests string

	// ManifestPatches are JSON merge patches (RFC 7396). Mutually exclusive with Patches.
	ManifestPatches []string

	// Patches are strategic merge or RFC6902 patches with target selectors.
	Patches []PatchConfig
}

// InitString renders the "name" or "name:version" form clusterctl init expects.
func (p ProviderConfig) InitString() string {
	if p.Version == "" {
		return p.Name
	}
	return p.Name + ":" + p.Version
}

// HasCustomizations reports whether the component YAML must be altered.
// Version and FetchConfig only affect where components come from.
func (p ProviderConfig) HasCustomizations() bool {
	return len(p.ConfigVariables) > 0 || len(p.SecretConfigVariables) > 0 ||
		p.Deployment != nil || p.Manager != nil || p.AdditionalManifests != "" ||
		len(p.ManifestPatches) > 0 || len(p.Patches) > 0
}

// NeedsURLOverride reports whether a clusterctl provider URL must be
// registered for this entry (a pinned version or any fetch config).
func (p ProviderConfig) NeedsURLOverride() bool {
	return p.Version != "" || p.FetchConfig != nil
}

// ProviderKey identifies a provider by type and name. Bootstrap "talos" and
// control-plane "talos" are different providers.
type ProviderKey struct {
	Type ProviderType
	Name string
}

// ProviderSet groups providers by type, preserving insertion order per type.
type ProviderSet map[ProviderType][]ProviderConfig

// Add appends p under p.Type.
func (s ProviderSet) Add(p ProviderConfig) {
	s[p.Type] = append(s[p.Type], p)
}

// Infrastructure returns the single infrastructure provider, if present.
func (s ProviderSet) Infrastructure() (ProviderConfig, bool) {
	if infra := s[ProviderTypeInfrastructure]; len(infra) > 0 {
		return infra[0], true
	}
	return ProviderConfig{}, false
}

// InitStrings returns the clusterctl init strings for one type.
func (s ProviderSet) InitStrings(t ProviderType) []string {
	out := make([]string, 0, len(s[t]))
	for _, p := range s[t] {
		out = append(out, p.InitString())
	}
	return out
}

// Customized returns providers whose component YAML must be altered, keyed by type and name.
func (s ProviderSet) Customized() map[ProviderKey]ProviderConfig {
	out := map[ProviderKey]ProviderConfig{}
	for _, p := range s.All() {
		if p.HasCustomizations() {
			out[ProviderKey{Type: p.Type, Name: p.Name}] = p
		}
	}
	return out
}

// All flattens the set in clusterctl install order.
func (s ProviderSet) All() []ProviderConfig {
	var out []ProviderConfig
	for _, t := range AllProviderTypes() {
		out = append(out, s[t]...)
	}
	return out
}
```

- [ ] **Step 4: Run the new tests**

Run: `go test ./internal/capi/ -run 'TestProviderType|TestProviderConfig|TestProviderSet' -count=1`
Expected: PASS for these tests. Other packages will not compile yet; that is fixed in Tasks 4–5.

- [ ] **Step 5: Commit (types only; the build is intentionally red until Task 5)**

```bash
git add internal/capi/types.go internal/capi/types_test.go
git commit -m "Introduce ProviderType, ProviderConfig and ProviderSet"
```

---

### Task 2: `ResolveFetchURL` in `internal/capi/fetch.go`

**Files:**
- Create: `internal/capi/fetch.go`
- Create: `internal/capi/fetch_test.go`

**Interfaces:**
- Consumes: `ProviderConfig`, `FetchConfig`, `ProviderType` from Task 1.
- Produces:
  ```go
  // defaultURL is clusterctl's built-in URL for the provider ("" when unknown).
  func ResolveFetchURL(p ProviderConfig, defaultURL string) (string, error)
  var ErrUnknownProviderRepository error
  ```

- [ ] **Step 1: Write the failing tests**

`internal/capi/fetch_test.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package capi

import (
	"errors"
	"testing"
)

const tinkerbellDefault = "https://github.com/tinkerbell/cluster-api-provider-tinkerbell/releases/latest/infrastructure-components.yaml"

func TestResolveFetchURL(t *testing.T) {
	tests := []struct {
		name       string
		p          ProviderConfig
		defaultURL string
		want       string
		wantErr    error
	}{
		{
			name:       "owner override keeps default repo and pins version",
			p:          ProviderConfig{Name: "tinkerbell", Type: ProviderTypeInfrastructure, Version: "v0.7.9", FetchConfig: &FetchConfig{Owner: "tinkerbell-community"}},
			defaultURL: tinkerbellDefault,
			want:       "https://github.com/tinkerbell-community/cluster-api-provider-tinkerbell/releases/v0.7.9/infrastructure-components.yaml",
		},
		{
			name:       "version only keeps default owner and repo",
			p:          ProviderConfig{Name: "tinkerbell", Type: ProviderTypeInfrastructure, Version: "v0.7.9"},
			defaultURL: tinkerbellDefault,
			want:       "https://github.com/tinkerbell/cluster-api-provider-tinkerbell/releases/v0.7.9/infrastructure-components.yaml",
		},
		{
			name:       "owner without version uses latest",
			p:          ProviderConfig{Name: "tinkerbell", Type: ProviderTypeInfrastructure, FetchConfig: &FetchConfig{Owner: "me"}},
			defaultURL: tinkerbellDefault,
			want:       "https://github.com/me/cluster-api-provider-tinkerbell/releases/latest/infrastructure-components.yaml",
		},
		{
			name: "unknown provider with owner and repository",
			p:    ProviderConfig{Name: "unifi", Type: ProviderTypeIPAM, Version: "v0.4.1", FetchConfig: &FetchConfig{Owner: "ubiquiti-community", Repository: "cluster-api-ipam-provider-unifi"}},
			want: "https://github.com/ubiquiti-community/cluster-api-ipam-provider-unifi/releases/v0.4.1/ipam-components.yaml",
		},
		{
			name:    "unknown provider without repository errors",
			p:       ProviderConfig{Name: "unifi", Type: ProviderTypeIPAM, Version: "v0.4.1", FetchConfig: &FetchConfig{Owner: "ubiquiti-community"}},
			wantErr: ErrUnknownProviderRepository,
		},
		{
			name:    "unknown provider with only a version errors",
			p:       ProviderConfig{Name: "unifi", Type: ProviderTypeIPAM, Version: "v0.4.1"},
			wantErr: ErrUnknownProviderRepository,
		},
		{
			name:       "url is verbatim",
			p:          ProviderConfig{Name: "tinkerbell", Type: ProviderTypeInfrastructure, Version: "v9", FetchConfig: &FetchConfig{URL: "https://example.com/x/infrastructure-components.yaml"}},
			defaultURL: tinkerbellDefault,
			want:       "https://example.com/x/infrastructure-components.yaml",
		},
		{
			name: "oci is verbatim",
			p:    ProviderConfig{Name: "unifi", Type: ProviderTypeIPAM, FetchConfig: &FetchConfig{OCI: "oci://ghcr.io/org/unifi"}},
			want: "oci://ghcr.io/org/unifi",
		},
		{
			name:       "non-github default with owner override errors",
			p:          ProviderConfig{Name: "x", Type: ProviderTypeAddon, FetchConfig: &FetchConfig{Owner: "me"}},
			defaultURL: "https://example.com/releases/latest/addon-components.yaml",
			wantErr:    ErrUnknownProviderRepository,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveFetchURL(tt.p, tt.defaultURL)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/capi/ -run TestResolveFetchURL -count=1`
Expected: `undefined: ResolveFetchURL`.

- [ ] **Step 3: Implement**

`internal/capi/fetch.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package capi

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ErrUnknownProviderRepository is returned when a GitHub releases URL cannot
// be built because neither the user nor clusterctl's defaults name the repository.
var ErrUnknownProviderRepository = errors.New("provider repository unknown")

const githubLatest = "latest"

// ResolveFetchURL returns the clusterctl provider URL for p.
//
// URL and OCI in the fetch config are returned verbatim. Otherwise the URL is
// https://github.com/{owner}/{repository}/releases/{version|latest}/{type}-components.yaml
// with owner and repository falling back to those parsed from defaultURL,
// clusterctl's built-in entry for the provider ("" when clusterctl does not
// know it). Pinning the version in the URL keeps clusterctl from calling the
// GitHub API to resolve "latest".
func ResolveFetchURL(p ProviderConfig, defaultURL string) (string, error) {
	fc := p.FetchConfig
	if fc == nil {
		fc = &FetchConfig{}
	}
	if fc.URL != "" {
		return fc.URL, nil
	}
	if fc.OCI != "" {
		return fc.OCI, nil
	}

	owner, repo := fc.Owner, fc.Repository
	if owner == "" || repo == "" {
		defOwner, defRepo := githubOwnerRepo(defaultURL)
		if owner == "" {
			owner = defOwner
		}
		if repo == "" {
			repo = defRepo
		}
	}
	if owner == "" || repo == "" {
		return "", fmt.Errorf("%w: %s provider %q is not built into clusterctl; set fetch_config.owner and fetch_config.repository, or fetch_config.url",
			ErrUnknownProviderRepository, p.Type, p.Name)
	}

	version := p.Version
	if version == "" {
		version = githubLatest
	}
	return fmt.Sprintf("https://github.com/%s/%s/releases/%s/%s", owner, repo, version, p.Type.ComponentsFile()), nil
}

// githubOwnerRepo extracts owner and repository from a GitHub releases URL.
// Both are "" when raw is empty, not GitHub, or not in the releases form.
func githubOwnerRepo(raw string) (string, string) {
	if raw == "" {
		return "", ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host != "github.com" {
		return "", ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 3 || parts[2] != "releases" {
		return "", ""
	}
	return parts[0], parts[1]
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/capi/ -run TestResolveFetchURL -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/capi/fetch.go internal/capi/fetch_test.go
git commit -m "Add ResolveFetchURL for owner/repository provider overrides"
```

---

### Task 3: `overlayReader` clusterctl config reader

**Files:**
- Create: `internal/capi/clusterctl_config.go`
- Create: `internal/capi/clusterctl_config_test.go`
- Modify: `go.mod` (viper becomes a direct dependency via `go mod tidy`)

**Interfaces:**
- Produces:
  ```go
  type providerOverride struct{ Name string; Type clusterctlv1.ProviderType; URL string }
  type overlayReader struct{ ... }          // implements config.Reader
  func newOverlayReader() *overlayReader
  func (r *overlayReader) AddOverride(name string, t clusterctlv1.ProviderType, url string)
  ```

- [ ] **Step 1: Write the failing tests**

`internal/capi/clusterctl_config_test.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package capi

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	clusterctlv1 "sigs.k8s.io/cluster-api/cmd/clusterctl/api/v1alpha3"
	"sigs.k8s.io/cluster-api/cmd/clusterctl/client/config"
)

func writeClusterctlConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "clusterctl.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOverlayReader_MergesFileAndOverrides(t *testing.T) {
	path := writeClusterctlConfig(t, `
providers:
  - name: tinkerbell
    type: InfrastructureProvider
    url: https://github.com/file-owner/cluster-api-provider-tinkerbell/releases/latest/infrastructure-components.yaml
  - name: custom
    type: AddonProvider
    url: https://github.com/file-owner/custom/releases/latest/addon-components.yaml
FOO: bar
`)
	r := newOverlayReader()
	r.AddOverride("tinkerbell", clusterctlv1.InfrastructureProviderType,
		"https://github.com/overlay/cluster-api-provider-tinkerbell/releases/v1/infrastructure-components.yaml")
	r.AddOverride("unifi", clusterctlv1.IPAMProviderType,
		"https://github.com/ubiquiti-community/cluster-api-ipam-provider-unifi/releases/v0.4.1/ipam-components.yaml")

	client, err := config.New(context.Background(), path, config.InjectReader(r))
	if err != nil {
		t.Fatal(err)
	}

	got, err := client.Providers().Get("tinkerbell", clusterctlv1.InfrastructureProviderType)
	if err != nil {
		t.Fatal(err)
	}
	if got.URL() != "https://github.com/overlay/cluster-api-provider-tinkerbell/releases/v1/infrastructure-components.yaml" {
		t.Errorf("overlay should win over file: %s", got.URL())
	}
	got, err = client.Providers().Get("custom", clusterctlv1.AddonProviderType)
	if err != nil || got.URL() != "https://github.com/file-owner/custom/releases/latest/addon-components.yaml" {
		t.Errorf("file-only provider must survive: %v %v", got, err)
	}
	if _, err := client.Providers().Get("unifi", clusterctlv1.IPAMProviderType); err != nil {
		t.Errorf("overlay-only provider must be registered: %v", err)
	}
	if v, err := client.Variables().Get("FOO"); err != nil || v != "bar" {
		t.Errorf("plain variables must pass through: %q %v", v, err)
	}
}

func TestOverlayReader_OverridesAddedAfterInitAreVisible(t *testing.T) {
	r := newOverlayReader()
	client, err := config.New(context.Background(), writeClusterctlConfig(t, "{}\n"), config.InjectReader(r))
	if err != nil {
		t.Fatal(err)
	}
	def, err := client.Providers().Get("tinkerbell", clusterctlv1.InfrastructureProviderType)
	if err != nil {
		t.Fatal(err)
	}
	if def.URL() == "" {
		t.Fatal("expected clusterctl default URL")
	}
	r.AddOverride("tinkerbell", clusterctlv1.InfrastructureProviderType, "https://github.com/x/y/releases/v2/infrastructure-components.yaml")
	after, err := client.Providers().Get("tinkerbell", clusterctlv1.InfrastructureProviderType)
	if err != nil || after.URL() != "https://github.com/x/y/releases/v2/infrastructure-components.yaml" {
		t.Errorf("override added after Init must be visible: %v %v", after, err)
	}
}

func TestOverlayReader_MissingDefaultConfigIsFine(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	r := newOverlayReader()
	if err := r.Init(context.Background(), ""); err != nil {
		t.Fatalf("missing default config must not error: %v", err)
	}
}

func TestOverlayReader_EnvVariables(t *testing.T) {
	t.Setenv("MY_TEST_VAR", "from-env")
	r := newOverlayReader()
	if err := r.Init(context.Background(), writeClusterctlConfig(t, "{}\n")); err != nil {
		t.Fatal(err)
	}
	if v, err := r.Get("my-test-var"); err != nil || v != "from-env" {
		t.Errorf("dash keys must map to underscore env vars: %q %v", v, err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/capi/ -run TestOverlayReader -count=1`
Expected: `undefined: newOverlayReader`.

- [ ] **Step 3: Implement**

`internal/capi/clusterctl_config.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package capi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/adrg/xdg"
	"github.com/spf13/viper"
	clusterctlv1 "sigs.k8s.io/cluster-api/cmd/clusterctl/api/v1alpha3"
	"sigs.k8s.io/cluster-api/cmd/clusterctl/client/config"
)

// providerOverride is one entry of the clusterctl "providers" config list.
type providerOverride struct {
	Name string                    `json:"name" mapstructure:"name"`
	Type clusterctlv1.ProviderType `json:"type" mapstructure:"type"`
	URL  string                    `json:"url" mapstructure:"url"`
}

// overlayReader is a clusterctl config.Reader backed by its own viper
// instance (clusterctl's default reader uses the global one, which is unsafe
// with concurrent resources). It reads the user's clusterctl.yaml and
// environment like clusterctl does, and layers provider URL overrides on top
// of the file's "providers" list. Overrides may be added after Init; clusterctl
// re-reads the key on every lookup.
type overlayReader struct {
	v *viper.Viper

	mu        sync.Mutex
	overrides []providerOverride
}

var _ config.Reader = &overlayReader{}

func newOverlayReader() *overlayReader {
	return &overlayReader{v: viper.New()}
}

// AddOverride registers (or replaces) the URL for a provider.
func (r *overlayReader) AddOverride(name string, t clusterctlv1.ProviderType, url string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.overrides {
		if r.overrides[i].Name == name && r.overrides[i].Type == t {
			r.overrides[i].URL = url
			return
		}
	}
	r.overrides = append(r.overrides, providerOverride{Name: name, Type: t, URL: url})
}

// Init mirrors clusterctl's viper reader: env vars with "-" mapped to "_",
// an explicit file path, or the first default config file that exists.
func (r *overlayReader) Init(_ context.Context, path string) error {
	r.v.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	r.v.AllowEmptyEnv(true)
	r.v.AutomaticEnv()

	if path != "" {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("clusterctl config file: %w", err)
		}
		r.v.SetConfigFile(path)
		return r.v.ReadInConfig()
	}

	r.v.SetConfigName(config.ConfigName)
	if dir, err := xdg.ConfigFile(config.ConfigFolderXDG); err == nil {
		r.v.AddConfigPath(dir)
	}
	r.v.AddConfigPath(filepath.Join(xdg.Home, config.ConfigFolder))

	err := r.v.ReadInConfig()
	var notFound viper.ConfigFileNotFoundError
	if errors.As(err, &notFound) {
		return nil
	}
	return err
}

func (r *overlayReader) Get(key string) (string, error) {
	if r.v.Get(key) == nil {
		return "", fmt.Errorf("failed to get value for variable %q. Please set the variable value using os env variables or using the .clusterctl config file", key)
	}
	return r.v.GetString(key), nil
}

func (r *overlayReader) Set(key, value string) {
	r.v.Set(key, value)
}

// UnmarshalKey delegates to viper except for the providers list, where the
// overrides are merged over the file entries (same name+type wins).
func (r *overlayReader) UnmarshalKey(key string, rawval interface{}) error {
	if key != config.ProvidersConfigKey {
		return r.v.UnmarshalKey(key, rawval)
	}

	var fromFile []providerOverride
	if err := r.v.UnmarshalKey(key, &fromFile); err != nil {
		return err
	}

	r.mu.Lock()
	merged := make([]providerOverride, 0, len(fromFile)+len(r.overrides))
	for _, f := range fromFile {
		overridden := false
		for _, o := range r.overrides {
			if o.Name == f.Name && o.Type == f.Type {
				overridden = true
				break
			}
		}
		if !overridden {
			merged = append(merged, f)
		}
	}
	merged = append(merged, r.overrides...)
	r.mu.Unlock()

	// Round-trip through viper's decoder so rawval can be any slice type
	// clusterctl uses (its configProvider is unexported).
	tmp := viper.New()
	tmp.Set(key, toMaps(merged))
	return tmp.UnmarshalKey(key, rawval)
}

func toMaps(in []providerOverride) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(in))
	for _, p := range in {
		out = append(out, map[string]interface{}{"name": p.Name, "type": string(p.Type), "url": p.URL})
	}
	return out
}
```

Then run `go mod tidy` so `github.com/spf13/viper` and `github.com/adrg/xdg` move to the direct require block.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/capi/ -run TestOverlayReader -count=1 -v`
Expected: 4 PASS. If `Get` on a missing key does not return an error, clusterctl's `Variables().Get` contract is broken; keep the error.

- [ ] **Step 5: Commit**

```bash
git add internal/capi/clusterctl_config.go internal/capi/clusterctl_config_test.go go.mod go.sum
git commit -m "Add isolated clusterctl config reader with provider URL overlay"
```

---

### Task 4: Rename `addon.go` to `customize.go` and key customizations by type+name

**Files:**
- Rename: `internal/capi/addon.go` → `internal/capi/customize.go`
- Rename: `internal/capi/addon_test.go` → `internal/capi/customize_test.go`

**Interfaces:**
- Produces:
  ```go
  func BuildComponentsAlterFn(p ProviderConfig) repository.ComponentsAlterFn
  func NewCustomizingRepoFactory(configClient config.Client, customizations map[ProviderKey]ProviderConfig) clusterctlclient.RepositoryClientFactory
  ```
- Removed: `AddonProviderStrings`, `CustomizedAddons`, `parseProviderNameVersion` (moved to the provider package in Task 8 as `splitNameVersion`).

- [ ] **Step 1: Rename files and update the tests**

```bash
git mv internal/capi/addon.go internal/capi/customize.go
git mv internal/capi/addon_test.go internal/capi/customize_test.go
```

In `customize_test.go`: replace every `AddonConfig{Provider: "x:v"}` with `ProviderConfig{Name: "x", Version: "v"}` (drop the `Provider` field everywhere). Delete `TestAddonProviderStrings`, `TestCustomizedAddons`, `TestParseProviderNameVersion`. Update `TestHasCustomizations` (the table at the old lines ~530–545) to use `ProviderConfig` and add a row `{"fetch config only", ProviderConfig{Name: "helm", FetchConfig: &FetchConfig{Owner: "me"}}, false}`.

Add this factory test:

```go
func TestNewCustomizingRepoFactory_KeysByTypeAndName(t *testing.T) {
	ctx := context.Background()
	configClient, err := config.New(ctx, "", config.InjectReader(config.NewMemoryReader()))
	if err != nil {
		t.Fatal(err)
	}
	two, five := int64(2), int64(5)
	customizations := map[ProviderKey]ProviderConfig{
		{ProviderTypeBootstrap, "talos"}:    {Name: "talos", Type: ProviderTypeBootstrap, Manager: &ManagerConfig{Verbosity: &two}},
		{ProviderTypeControlPlane, "talos"}: {Name: "talos", Type: ProviderTypeControlPlane, Manager: &ManagerConfig{Verbosity: &five}},
	}
	factory := NewCustomizingRepoFactory(configClient, customizations)

	bootstrap := config.NewProvider("talos", "https://github.com/siderolabs/cluster-api-bootstrap-provider-talos/releases/latest/bootstrap-components.yaml", clusterctlv1.BootstrapProviderType)
	client, err := factory(ctx, clusterctlclient.RepositoryClientFactoryInput{Provider: bootstrap})
	if err != nil {
		t.Fatal(err)
	}
	wrapped, ok := client.(*customizingRepoClient)
	if !ok {
		t.Fatal("bootstrap talos should be wrapped")
	}
	if wrapped.key != (ProviderKey{ProviderTypeBootstrap, "talos"}) {
		t.Errorf("wrapped key = %+v", wrapped.key)
	}

	plain := config.NewProvider("kubeadm", "https://github.com/kubernetes-sigs/cluster-api/releases/latest/bootstrap-components.yaml", clusterctlv1.BootstrapProviderType)
	client, err = factory(ctx, clusterctlclient.RepositoryClientFactoryInput{Provider: plain})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := client.(*customizingRepoClient); ok {
		t.Error("kubeadm has no customizations and must not be wrapped")
	}
}
```

Add imports `clusterctlclient "sigs.k8s.io/cluster-api/cmd/clusterctl/client"`, `clusterctlv1 "sigs.k8s.io/cluster-api/cmd/clusterctl/api/v1alpha3"`, `"sigs.k8s.io/cluster-api/cmd/clusterctl/client/config"` to the test if missing.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/capi/ -run 'TestNewCustomizingRepoFactory|TestHasCustomizations|TestBuildComponentsAlterFn' -count=1`
Expected: compile errors (`AddonConfig` undefined, `wrapped.key` undefined).

- [ ] **Step 3: Update `customize.go`**

- `BuildComponentsAlterFn(addon AddonConfig)` → `BuildComponentsAlterFn(p ProviderConfig)`; rename the parameter uses inside (`addon.` → `p.`).
- Add a `key ProviderKey` field to `customizingRepoClient`.
- Replace `NewCustomizingRepoFactory` with:

```go
// NewCustomizingRepoFactory creates a RepositoryClientFactory that wraps the
// default repository client with provider-specific customizations. Providers
// are matched by type and name, so bootstrap "talos" and control-plane "talos"
// are configured independently. For matching providers it:
//  1. Injects a custom Processor that adds ConfigVariables/SecretConfigVariables
//  2. Wraps the ComponentsClient to apply deployment/manager/patch customizations
//
// Other providers pass through to the default factory unchanged.
func NewCustomizingRepoFactory(configClient config.Client, customizations map[ProviderKey]ProviderConfig) clusterctlclient.RepositoryClientFactory {
	return func(ctx context.Context, input clusterctlclient.RepositoryClientFactoryInput) (repository.Client, error) {
		var (
			p   ProviderConfig
			key ProviderKey
			ok  bool
		)
		if typ, known := ProviderTypeFromClusterctl(input.Provider.Type()); known {
			key = ProviderKey{Type: typ, Name: input.Provider.Name()}
			p, ok = customizations[key]
		}

		var repoOpts []repository.Option
		if ok && (len(p.ConfigVariables) > 0 || len(p.SecretConfigVariables) > 0) {
			repoOpts = append(repoOpts, repository.InjectYamlProcessor(
				newCustomProcessor(p.ConfigVariables, p.SecretConfigVariables),
			))
		}

		repoClient, err := repository.New(ctx, input.Provider, configClient, repoOpts...)
		if err != nil {
			return nil, err
		}

		needsAlter := ok && (p.Deployment != nil || p.Manager != nil ||
			len(p.ManifestPatches) > 0 || len(p.Patches) > 0 || p.AdditionalManifests != "")
		if needsAlter {
			return &customizingRepoClient{Client: repoClient, alterFn: BuildComponentsAlterFn(p), key: key}, nil
		}
		return repoClient, nil
	}
}
```

- Delete `AddonProviderStrings`, `CustomizedAddons`, `parseProviderNameVersion`.
- Update the file's header comment and any remaining "addon" wording to "provider".

- [ ] **Step 4: Run tests**

Run: `go test ./internal/capi/ -run 'TestNewCustomizingRepoFactory|TestHasCustomizations|TestBuildComponentsAlterFn|TestCustomize|TestApply' -count=1`
Expected: PASS (package still fails to build if other files reference removed fields; `installer.go` and `manager.go` are next).

- [ ] **Step 5: Commit**

```bash
git add -A internal/capi/customize.go internal/capi/customize_test.go
git commit -m "Key provider customizations by type and name"
```

---

### Task 5: Installer, manager, docker defaults use `ProviderSet`

**Files:**
- Modify: `internal/capi/installer.go`
- Modify: `internal/capi/manager.go:165-185, 195-200, 310-325`
- Modify: `internal/capi/docker/provider.go:36-45`, `internal/capi/docker/provider_test.go`
- Modify: `internal/capi/manager_test.go`, `internal/capi/mock_test.go`, `internal/capi/talos/manager_integration_test.go`
- Create: `internal/capi/installer_test.go`

**Interfaces:**
- Consumes: `ProviderSet`, `ResolveFetchURL`, `overlayReader`, `NewCustomizingRepoFactory`.
- Produces: `func buildInitOptions(ctx, configPath string, providers ProviderSet) (*clusterctlclient.InitOptions, []clusterctlclient.Option, error)` (unit-testable core of `Init`).

- [ ] **Step 1: Write the failing installer test**

`internal/capi/installer_test.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package capi

import (
	"context"
	"errors"
	"testing"
)

func TestBuildInitOptions_FillsEveryTypeAndRegistersOverrides(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	providers := ProviderSet{}
	providers.Add(ProviderConfig{Type: ProviderTypeCore, Name: "cluster-api"})
	providers.Add(ProviderConfig{Type: ProviderTypeInfrastructure, Name: "tinkerbell", Version: "v0.7.9", FetchConfig: &FetchConfig{Owner: "tinkerbell-community"}})
	providers.Add(ProviderConfig{Type: ProviderTypeBootstrap, Name: "talos", Version: "v0.8.2"})
	providers.Add(ProviderConfig{Type: ProviderTypeControlPlane, Name: "talos", Version: "v0.7.1"})
	providers.Add(ProviderConfig{Type: ProviderTypeIPAM, Name: "unifi", Version: "v0.4.1", FetchConfig: &FetchConfig{Owner: "ubiquiti-community", Repository: "cluster-api-ipam-provider-unifi"}})
	providers.Add(ProviderConfig{Type: ProviderTypeAddon, Name: "helm"})

	initOpts, _, reader, err := buildInitOptions(context.Background(), "", providers)
	if err != nil {
		t.Fatal(err)
	}
	if initOpts.CoreProvider != "cluster-api" {
		t.Errorf("core = %q", initOpts.CoreProvider)
	}
	if len(initOpts.InfrastructureProviders) != 1 || initOpts.InfrastructureProviders[0] != "tinkerbell:v0.7.9" {
		t.Errorf("infra = %v", initOpts.InfrastructureProviders)
	}
	if len(initOpts.BootstrapProviders) != 1 || initOpts.BootstrapProviders[0] != "talos:v0.8.2" {
		t.Errorf("bootstrap = %v", initOpts.BootstrapProviders)
	}
	if len(initOpts.ControlPlaneProviders) != 1 || initOpts.ControlPlaneProviders[0] != "talos:v0.7.1" {
		t.Errorf("control plane = %v", initOpts.ControlPlaneProviders)
	}
	if len(initOpts.IPAMProviders) != 1 || initOpts.IPAMProviders[0] != "unifi:v0.4.1" {
		t.Errorf("ipam = %v", initOpts.IPAMProviders)
	}
	if len(initOpts.AddonProviders) != 1 || initOpts.AddonProviders[0] != "helm" {
		t.Errorf("addon = %v", initOpts.AddonProviders)
	}
	if !initOpts.WaitProviders {
		t.Error("WaitProviders must stay on")
	}

	want := map[string]string{
		"tinkerbell": "https://github.com/tinkerbell-community/cluster-api-provider-tinkerbell/releases/v0.7.9/infrastructure-components.yaml",
		"unifi":      "https://github.com/ubiquiti-community/cluster-api-ipam-provider-unifi/releases/v0.4.1/ipam-components.yaml",
	}
	got := map[string]string{}
	for _, o := range reader.overrides {
		got[o.Name+"/"+string(o.Type)] = o.URL
	}
	if got["tinkerbell/InfrastructureProvider"] != want["tinkerbell"] {
		t.Errorf("tinkerbell override = %q", got["tinkerbell/InfrastructureProvider"])
	}
	if got["unifi/IPAMProvider"] != want["unifi"] {
		t.Errorf("unifi override = %q", got["unifi/IPAMProvider"])
	}
	if _, ok := got["helm/AddonProvider"]; ok {
		t.Error("bare providers must not get an override")
	}
	// Both talos entries pin a version, so both types get overrides from clusterctl defaults.
	if got["talos/BootstrapProvider"] != "https://github.com/siderolabs/cluster-api-bootstrap-provider-talos/releases/v0.8.2/bootstrap-components.yaml" {
		t.Errorf("talos bootstrap override = %q", got["talos/BootstrapProvider"])
	}
	if got["talos/ControlPlaneProvider"] != "https://github.com/siderolabs/cluster-api-control-plane-provider-talos/releases/v0.7.1/control-plane-components.yaml" {
		t.Errorf("talos control-plane override = %q", got["talos/ControlPlaneProvider"])
	}
}

func TestBuildInitOptions_UnknownProviderWithoutRepository(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	providers := ProviderSet{}
	providers.Add(ProviderConfig{Type: ProviderTypeIPAM, Name: "unifi", Version: "v0.4.1"})
	_, _, _, err := buildInitOptions(context.Background(), "", providers)
	if !errors.Is(err, ErrUnknownProviderRepository) {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/capi/ -run TestBuildInitOptions -count=1`
Expected: `undefined: buildInitOptions`.

- [ ] **Step 3: Rewrite `installer.go`**

Replace everything below the imports with:

```go
// ClusterctlInstaller installs CAPI components using the clusterctl client
// library. It always injects an isolated config client (so provider URL
// overrides never touch global viper state) and a repository factory that
// applies capi-operator-style customizations to component YAML.
type ClusterctlInstaller struct {
	configPath string
}

// NewClusterctlInstaller creates a new installer with the given clusterctl
// config path. An empty configPath lets clusterctl fall back to its own
// default config file resolution.
func NewClusterctlInstaller(configPath string) *ClusterctlInstaller {
	return &ClusterctlInstaller{configPath: configPath}
}

// Init initializes CAPI providers on a cluster using clusterctl init.
func (i *ClusterctlInstaller) Init(ctx context.Context, cluster *Cluster, opts InitOptions) error {
	initOpts, clientOpts, _, err := buildInitOptions(ctx, i.configPath, opts.Providers)
	if err != nil {
		return &CAPIError{Operation: "init", Cluster: cluster.Name, Err: fmt.Errorf("%w: %v", ErrCAPIInit, err)}
	}
	initOpts.Kubeconfig = clusterctlclient.Kubeconfig{Path: cluster.KubeconfigPath}

	client, err := clusterctlclient.New(ctx, i.configPath, clientOpts...)
	if err != nil {
		return &CAPIError{Operation: "init", Cluster: cluster.Name, Err: fmt.Errorf("%w: creating clusterctl client: %v", ErrCAPIInit, err)}
	}

	if _, err := client.Init(ctx, *initOpts); err != nil {
		return &CAPIError{Operation: "init", Cluster: cluster.Name, Err: fmt.Errorf("%w: %v", ErrCAPIInit, err)}
	}
	return nil
}

// buildInitOptions resolves provider URL overrides and assembles the
// clusterctl init options and client options for a ProviderSet. The returned
// reader is exposed for tests.
func buildInitOptions(ctx context.Context, configPath string, providers ProviderSet) (*clusterctlclient.InitOptions, []clusterctlclient.Option, *overlayReader, error) {
	reader := newOverlayReader()
	configClient, err := config.New(ctx, configPath, config.InjectReader(reader))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("creating clusterctl config client: %w", err)
	}

	for _, p := range providers.All() {
		if !p.NeedsURLOverride() {
			continue
		}
		defaultURL := ""
		if def, err := configClient.Providers().Get(p.Name, p.Type.Clusterctl()); err == nil {
			defaultURL = def.URL()
		}
		u, err := ResolveFetchURL(p, defaultURL)
		if err != nil {
			return nil, nil, nil, err
		}
		reader.AddOverride(p.Name, p.Type.Clusterctl(), u)
	}

	initOpts := &clusterctlclient.InitOptions{
		// Wait for provider deployments (including webhook services) to become
		// ready before returning. Without this, callers can race ahead and
		// apply manifests that hit not-yet-ready admission webhooks.
		WaitProviders:           true,
		BootstrapProviders:      providers.InitStrings(ProviderTypeBootstrap),
		ControlPlaneProviders:   providers.InitStrings(ProviderTypeControlPlane),
		InfrastructureProviders: providers.InitStrings(ProviderTypeInfrastructure),
		IPAMProviders:           providers.InitStrings(ProviderTypeIPAM),
		AddonProviders:          providers.InitStrings(ProviderTypeAddon),
	}
	if core := providers.InitStrings(ProviderTypeCore); len(core) > 0 {
		initOpts.CoreProvider = core[0]
	}

	clientOpts := []clusterctlclient.Option{
		clusterctlclient.InjectConfig(configClient),
		clusterctlclient.InjectRepositoryFactory(NewCustomizingRepoFactory(configClient, providers.Customized())),
	}
	return initOpts, clientOpts, reader, nil
}
```

Note: `configClient.Providers().Get` for a versioned provider clusterctl already knows returns its default URL; `ResolveFetchURL` then pins the version. That is why both talos entries get overrides in the test.

- [ ] **Step 4: Update `manager.go`**

Both init sites (in `CreateCluster` around line 170 and the pivot around line 313) become:

```go
		initOpts := InitOptions{Providers: opts.Providers}
```

The template options (around line 199) become:

```go
	infra, _ := opts.Providers.Infrastructure()
	templateOpts := TemplateOptions{
		ClusterName:              opts.Name,
		Namespace:                namespace,
		KubernetesVersion:        opts.KubernetesVersion,
		InfrastructureProvider:   infra.InitString(),
		Flavor:                   opts.Flavor,
		ControlPlaneMachineCount: opts.ControlPlaneMachineCount,
		WorkerMachineCount:       opts.WorkerMachineCount,
	}
```

Add a guard at the top of `CreateCluster`, before the bootstrap step:

```go
	if _, ok := opts.Providers.Infrastructure(); !ok {
		return nil, fmt.Errorf("creating cluster %s: no infrastructure provider configured", opts.Name)
	}
```

- [ ] **Step 5: Update docker defaults and the tests**

`internal/capi/docker/provider.go` (the `DefaultCreateOptions`-style function at lines ~36–45): replace the three string fields with

```go
		Providers: capi.ProviderSet{
			capi.ProviderTypeInfrastructure: {{Type: capi.ProviderTypeInfrastructure, Name: InfrastructureProviderName}},
			capi.ProviderTypeBootstrap:      {{Type: capi.ProviderTypeBootstrap, Name: BootstrapProviderName}},
			capi.ProviderTypeControlPlane:   {{Type: capi.ProviderTypeControlPlane, Name: ControlPlaneProviderName}},
		},
```

and fix `provider_test.go` assertions to read `opts.Providers.Infrastructure()` etc.

In `internal/capi/manager_test.go` and `internal/capi/talos/manager_integration_test.go`, every `CreateClusterOptions{InfrastructureProvider: "docker", BootstrapProvider: "kubeadm", ControlPlaneProvider: "kubeadm", ...}` becomes `Providers: dockerProviders(), ...`. Add to `manager_test.go`:

```go
func dockerProviders() ProviderSet {
	return ProviderSet{
		ProviderTypeInfrastructure: {{Type: ProviderTypeInfrastructure, Name: "docker"}},
		ProviderTypeBootstrap:      {{Type: ProviderTypeBootstrap, Name: "kubeadm"}},
		ProviderTypeControlPlane:   {{Type: ProviderTypeControlPlane, Name: "kubeadm"}},
	}
}
```

(the talos integration test lives in another package; inline the literal there with the `capi.` prefix). The assertion at `manager_test.go:62` becomes:

```go
	if got := installer.InitCalls[0].Opts.Providers.InitStrings(ProviderTypeInfrastructure); len(got) != 1 || got[0] != "docker" {
		t.Errorf("expected infrastructure provider 'docker', got %v", got)
	}
```

`mock_test.go` needs no field changes unless it references removed names; check with the build.

- [ ] **Step 6: Build and test the capi packages**

Run: `go build ./internal/capi/... && go test ./internal/capi/... -count=1`
Expected: build OK; all tests PASS (the installer test hits clusterctl's in-memory defaults only, no network).

- [ ] **Step 7: Commit**

```bash
git add internal/capi
git commit -m "Drive clusterctl init from a ProviderSet with URL overrides and IPAM"
```

---

### Task 6: Provider-map schema, models and `buildCreateOptions`

**Files:**
- Modify: `internal/provider/cluster_resource.go:56-470` (schema)
- Modify: `internal/provider/cluster_resource_models.go` (models, attr types, extractors, `buildCreateOptions`)
- Modify: `internal/provider/cluster_resource_models_test.go`
- Modify: `internal/provider/bootstrap_validation_test.go` (fixtures that build `ClusterResourceModel`)

**Interfaces:**
- Produces (provider package):
  ```go
  type ProviderModel struct{ Version types.String; FetchConfig types.Object; ConfigVariables, SecretConfigVariables types.Map; Deployment, Manager types.Object; AdditionalManifests types.String; ManifestPatches, Patches types.List }
  type FetchConfigModel struct{ Owner, Repository, URL, OCI types.String }
  type TopologyModel struct{ ControlPlane, Workers types.Object }
  type MachineCountModel struct{ MachineCount types.Int64 }
  func providerAttrTypes() map[string]attr.Type
  func providerMapType() types.MapType
  func fetchConfigAttrTypes(), topologyAttrTypes(), machineCountAttrTypes() map[string]attr.Type
  func providerNestedObject() schema.NestedAttributeObject
  func providerMapAttribute(desc string, required bool) schema.MapNestedAttribute
  func extractProviders(ctx, m types.Map) (map[string]ProviderModel, diag.Diagnostics)          // nil map when null/unknown; keys sorted by callers
  func buildProviderConfig(ctx, name string, typ capi.ProviderType, pm ProviderModel) (capi.ProviderConfig, diag.Diagnostics)
  func extractTopology(ctx, data) (cp *int64, workers *int64, diags)
  func providerMapValue(ctx, nameVersion string) (types.Map, diag.Diagnostics)                    // used by upgraders in Task 8
  func machineCountObject(ctx, v types.Int64) (types.Object, diag.Diagnostics)
  ```
- `ClusterResourceModel` fields: `Core, Infrastructure, Bootstrap, ControlPlane, IPAM, Addon types.Map`, `Topology types.Object`; `Workers` and `Addons` removed.

- [ ] **Step 1: Write the failing tests**

In `cluster_resource_models_test.go` replace `TestClusterResource_Schema`, `TestExtractInfrastructure_Populated`, `TestExtractControlPlane_WithMachineCount`, `TestExtractWorkers_Null`, `TestBuildCreateOptions_Minimal`, `TestBuildCreateOptions_Full` with:

```go
func TestClusterResource_Schema_ProviderMaps(t *testing.T) {
	ctx := context.Background()
	r := NewClusterResource()
	resp := &resource.SchemaResponse{}
	r.Schema(ctx, resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", resp.Diagnostics)
	}
	if resp.Schema.Version != 2 {
		t.Errorf("schema version = %d, want 2", resp.Schema.Version)
	}

	var reference map[string]schema.Attribute
	for _, name := range []string{"core", "infrastructure", "bootstrap", "control_plane", "ipam", "addon"} {
		attr, ok := resp.Schema.Attributes[name]
		if !ok {
			t.Fatalf("%s attribute missing", name)
		}
		m, ok := attr.(schema.MapNestedAttribute)
		if !ok {
			t.Fatalf("%s must be MapNestedAttribute, got %T", name, attr)
		}
		if name == "infrastructure" && !m.Required {
			t.Error("infrastructure must be required")
		}
		if name != "infrastructure" && !m.Optional {
			t.Errorf("%s must be optional", name)
		}
		if len(m.PlanModifiers) == 0 {
			t.Errorf("%s must RequiresReplace", name)
		}
		if reference == nil {
			reference = m.NestedObject.Attributes
			continue
		}
		if len(reference) != len(m.NestedObject.Attributes) {
			t.Errorf("%s nested object differs from the shared provider object", name)
		}
	}
	for _, name := range []string{"version", "fetch_config", "config_variables", "secret_config_variables", "deployment", "manager", "additional_manifests", "manifest_patches", "patches"} {
		if _, ok := reference[name]; !ok {
			t.Errorf("provider object missing %s", name)
		}
	}
	fc := reference["fetch_config"].(schema.SingleNestedAttribute)
	for _, name := range []string{"owner", "repository", "url", "oci"} {
		if _, ok := fc.Attributes[name]; !ok {
			t.Errorf("fetch_config missing %s", name)
		}
	}

	for _, gone := range []string{"workers", "addons"} {
		if _, ok := resp.Schema.Attributes[gone]; ok {
			t.Errorf("%s must be removed", gone)
		}
	}
	topo, ok := resp.Schema.Attributes["topology"].(schema.SingleNestedAttribute)
	if !ok {
		t.Fatal("topology must be a SingleNestedAttribute")
	}
	for _, name := range []string{"control_plane", "workers"} {
		sub, ok := topo.Attributes[name].(schema.SingleNestedAttribute)
		if !ok {
			t.Fatalf("topology.%s must be a SingleNestedAttribute", name)
		}
		if _, ok := sub.Attributes["machine_count"].(schema.Int64Attribute); !ok {
			t.Errorf("topology.%s.machine_count must be Int64", name)
		}
	}
}

func mustProviderMap(t *testing.T, ctx context.Context, entries map[string]ProviderModel) types.Map {
	t.Helper()
	v, diags := types.MapValueFrom(ctx, types.ObjectType{AttrTypes: providerAttrTypes()}, entries)
	if diags.HasError() {
		t.Fatalf("building provider map: %v", diags)
	}
	return v
}

func emptyProviderModel() ProviderModel {
	return ProviderModel{
		Version:               types.StringNull(),
		FetchConfig:           types.ObjectNull(fetchConfigAttrTypes()),
		ConfigVariables:       types.MapNull(types.StringType),
		SecretConfigVariables: types.MapNull(types.StringType),
		Deployment:            types.ObjectNull(deploymentAttrTypes()),
		Manager:               types.ObjectNull(managerAttrTypes()),
		AdditionalManifests:   types.StringNull(),
		ManifestPatches:       types.ListNull(types.StringType),
		Patches:               types.ListNull(types.ObjectType{AttrTypes: patchAttrTypes()}),
	}
}

func baseModel(t *testing.T, ctx context.Context) *ClusterResourceModel {
	t.Helper()
	return &ClusterResourceModel{
		Name:           types.StringValue("test"),
		Core:           types.MapNull(providerMapType().ElemType),
		Infrastructure: mustProviderMap(t, ctx, map[string]ProviderModel{"docker": emptyProviderModel()}),
		Bootstrap:      types.MapNull(providerMapType().ElemType),
		ControlPlane:   types.MapNull(providerMapType().ElemType),
		IPAM:           types.MapNull(providerMapType().ElemType),
		Addon:          types.MapNull(providerMapType().ElemType),
		Topology:       types.ObjectNull(topologyAttrTypes()),
		Management:     types.ObjectNull(managementAttrTypes()),
		Inventory:      types.ObjectNull(inventoryAttrTypes()),
		Wait:           types.ObjectNull(waitAttrTypes()),
		Output:         types.ObjectNull(outputAttrTypes()),
	}
}

func TestBuildCreateOptions_Minimal(t *testing.T) {
	ctx := context.Background()
	opts, diags := buildCreateOptions(ctx, baseModel(t, ctx))
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	infra, ok := opts.Providers.Infrastructure()
	if !ok || infra.InitString() != "docker" {
		t.Errorf("infra = %+v %v", infra, ok)
	}
	if !opts.WaitForReady {
		t.Error("WaitForReady should default to true")
	}
	if opts.ControlPlaneMachineCount != nil || opts.WorkerMachineCount != nil {
		t.Error("counts must be nil without topology")
	}
}

func TestBuildCreateOptions_Full(t *testing.T) {
	ctx := context.Background()
	data := baseModel(t, ctx)
	data.KubernetesVersion = types.StringValue("v1.34.0")
	data.Flavor = types.StringValue("ha")

	fetch, _ := types.ObjectValueFrom(ctx, fetchConfigAttrTypes(), FetchConfigModel{
		Owner: types.StringValue("tinkerbell-community"), Repository: types.StringNull(), URL: types.StringNull(), OCI: types.StringNull(),
	})
	gates, _ := types.MapValueFrom(ctx, types.BoolType, map[string]bool{"ClusterTopology": true})
	mgr, _ := types.ObjectValueFrom(ctx, managerAttrTypes(), ManagerModel{
		ProfilerAddress: types.StringNull(), MaxConcurrentReconciles: types.Int64Null(), Verbosity: types.Int64Null(),
		FeatureGates: gates, AdditionalArgs: types.MapNull(types.StringType),
	})
	tink := emptyProviderModel()
	tink.Version = types.StringValue("v0.7.9")
	tink.FetchConfig = fetch
	tink.Manager = mgr
	data.Infrastructure = mustProviderMap(t, ctx, map[string]ProviderModel{"tinkerbell": tink})

	talosBS := emptyProviderModel()
	talosBS.Version = types.StringValue("v0.8.2")
	data.Bootstrap = mustProviderMap(t, ctx, map[string]ProviderModel{"talos": talosBS})
	talosCP := emptyProviderModel()
	talosCP.Version = types.StringValue("v0.7.1")
	data.ControlPlane = mustProviderMap(t, ctx, map[string]ProviderModel{"talos": talosCP})
	core := emptyProviderModel()
	core.Version = types.StringValue("v1.12.2")
	data.Core = mustProviderMap(t, ctx, map[string]ProviderModel{"cluster-api": core})
	data.Addon = mustProviderMap(t, ctx, map[string]ProviderModel{"helm": emptyProviderModel()})

	cp, _ := machineCountObject(ctx, types.Int64Value(3))
	w, _ := machineCountObject(ctx, types.Int64Value(5))
	data.Topology, _ = types.ObjectValueFrom(ctx, topologyAttrTypes(), TopologyModel{ControlPlane: cp, Workers: w})

	opts, diags := buildCreateOptions(ctx, data)
	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	infra, _ := opts.Providers.Infrastructure()
	if infra.InitString() != "tinkerbell:v0.7.9" || infra.FetchConfig == nil || infra.FetchConfig.Owner != "tinkerbell-community" {
		t.Errorf("infra = %+v", infra)
	}
	if infra.Manager == nil || !infra.Manager.FeatureGates["ClusterTopology"] {
		t.Errorf("infra manager = %+v", infra.Manager)
	}
	if got := opts.Providers.InitStrings(capi.ProviderTypeBootstrap); len(got) != 1 || got[0] != "talos:v0.8.2" {
		t.Errorf("bootstrap = %v", got)
	}
	if got := opts.Providers.InitStrings(capi.ProviderTypeControlPlane); len(got) != 1 || got[0] != "talos:v0.7.1" {
		t.Errorf("control plane = %v", got)
	}
	if got := opts.Providers.InitStrings(capi.ProviderTypeCore); len(got) != 1 || got[0] != "cluster-api:v1.12.2" {
		t.Errorf("core = %v", got)
	}
	if got := opts.Providers.InitStrings(capi.ProviderTypeAddon); len(got) != 1 || got[0] != "helm" {
		t.Errorf("addon = %v", got)
	}
	if opts.ControlPlaneMachineCount == nil || *opts.ControlPlaneMachineCount != 3 {
		t.Error("control plane machine count should be 3")
	}
	if opts.WorkerMachineCount == nil || *opts.WorkerMachineCount != 5 {
		t.Error("worker machine count should be 5")
	}
	if opts.KubernetesVersion != "v1.34.0" || opts.Flavor != "ha" {
		t.Errorf("k8s/flavor = %q/%q", opts.KubernetesVersion, opts.Flavor)
	}
}

func TestBuildCreateOptions_ProviderOrderIsDeterministic(t *testing.T) {
	ctx := context.Background()
	data := baseModel(t, ctx)
	data.Addon = mustProviderMap(t, ctx, map[string]ProviderModel{"zeta": emptyProviderModel(), "alpha": emptyProviderModel(), "mid": emptyProviderModel()})
	opts, diags := buildCreateOptions(ctx, data)
	if diags.HasError() {
		t.Fatal(diags)
	}
	got := opts.Providers.InitStrings(capi.ProviderTypeAddon)
	if len(got) != 3 || got[0] != "alpha" || got[1] != "mid" || got[2] != "zeta" {
		t.Errorf("addon order = %v, want sorted by name", got)
	}
}
```

Fix the other tests in the file that construct `ClusterResourceModel` (`TestValidateLifecycleConfig_*`, `TestSetStatusWithFallback_UsesResultFirst`, `TestValidateInventory_*`) to start from `baseModel(t, ctx)` and set provider maps via `mustProviderMap`; likewise `bootstrap_validation_test.go` fixtures (`ControlPlane`/`Workers` usages become `Topology`). Rename `AddonDeploymentModel`→`DeploymentModel`, `AddonManagerModel`→`ManagerModel`, `AddonPatchModel`→`PatchModel`, `AddonPatchSelectorModel`→`PatchSelectorModel`, `AddonContainerModel`→`ContainerModel` and their `*AttrTypes()` functions (`deploymentAttrTypes`, `managerAttrTypes`, `patchAttrTypes`, `patchSelectorAttrTypes`, `containerAttrTypes`) everywhere in tests.

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./internal/provider/ -run 'TestClusterResource_Schema_ProviderMaps|TestBuildCreateOptions' -count=1`
Expected: compile errors (`providerAttrTypes` undefined, no field `IPAM`, etc.).

- [ ] **Step 3: Models and attr types (`cluster_resource_models.go`)**

Replace the provider-related model section (from `InfrastructureModel` through `AddonPatchSelectorModel`, and `WorkersModel`) with:

```go
// ProviderModel is the shared nested object of every provider map (core,
// infrastructure, bootstrap, control_plane, ipam, addon), modeled after the
// cluster-api-operator provider CRDs.
type ProviderModel struct {
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

// FetchConfigModel controls where provider components are fetched from.
type FetchConfigModel struct {
	Owner      types.String `tfsdk:"owner"`
	Repository types.String `tfsdk:"repository"`
	URL        types.String `tfsdk:"url"`
	OCI        types.String `tfsdk:"oci"`
}

// TopologyModel holds machine counts for the control plane and workers.
type TopologyModel struct {
	ControlPlane types.Object `tfsdk:"control_plane"` // MachineCountModel
	Workers      types.Object `tfsdk:"workers"`       // MachineCountModel
}

// MachineCountModel is one topology group.
type MachineCountModel struct {
	MachineCount types.Int64 `tfsdk:"machine_count"`
}
```

Keep `DeploymentModel`, `ContainerModel`, `ManagerModel`, `PatchModel`, `PatchSelectorModel` (renamed from the `Addon*` versions, fields unchanged).

Attr types, replacing `infrastructureAttrTypes`, `bootstrapAttrTypes`, `controlPlaneAttrTypes`, `coreAttrTypes`, `workersAttrTypes`, `addonAttrTypes`, `addonFetchConfigAttrTypes`:

```go
func providerAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"version":                 types.StringType,
		"fetch_config":            types.ObjectType{AttrTypes: fetchConfigAttrTypes()},
		"config_variables":        types.MapType{ElemType: types.StringType},
		"secret_config_variables": types.MapType{ElemType: types.StringType},
		"deployment":              types.ObjectType{AttrTypes: deploymentAttrTypes()},
		"manager":                 types.ObjectType{AttrTypes: managerAttrTypes()},
		"additional_manifests":    types.StringType,
		"manifest_patches":        types.ListType{ElemType: types.StringType},
		"patches":                 types.ListType{ElemType: types.ObjectType{AttrTypes: patchAttrTypes()}},
	}
}

// providerMapType is the Terraform type of every provider map attribute.
func providerMapType() types.MapType {
	return types.MapType{ElemType: types.ObjectType{AttrTypes: providerAttrTypes()}}
}

func fetchConfigAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"owner":      types.StringType,
		"repository": types.StringType,
		"url":        types.StringType,
		"oci":        types.StringType,
	}
}

func topologyAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"control_plane": types.ObjectType{AttrTypes: machineCountAttrTypes()},
		"workers":       types.ObjectType{AttrTypes: machineCountAttrTypes()},
	}
}

func machineCountAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{"machine_count": types.Int64Type}
}
```

Extractors, replacing `extractInfrastructure`, `extractBootstrap`, `extractControlPlane`, `extractCore`, `extractWorkers`, `extractAddons`:

```go
// extractProviders decodes a provider map. It returns nil for a null or
// unknown map so callers can treat "not configured" uniformly.
func extractProviders(ctx context.Context, m types.Map) (map[string]ProviderModel, diag.Diagnostics) {
	if m.IsNull() || m.IsUnknown() {
		return nil, nil
	}
	out := map[string]ProviderModel{}
	diags := m.ElementsAs(ctx, &out, false)
	return out, diags
}

// sortedProviderNames returns map keys in a stable order.
func sortedProviderNames(m map[string]ProviderModel) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// providerMaps pairs each provider map attribute with its type.
func (data *ClusterResourceModel) providerMaps() []struct {
	Type capi.ProviderType
	Map  types.Map
} {
	return []struct {
		Type capi.ProviderType
		Map  types.Map
	}{
		{capi.ProviderTypeCore, data.Core},
		{capi.ProviderTypeBootstrap, data.Bootstrap},
		{capi.ProviderTypeControlPlane, data.ControlPlane},
		{capi.ProviderTypeInfrastructure, data.Infrastructure},
		{capi.ProviderTypeIPAM, data.IPAM},
		{capi.ProviderTypeAddon, data.Addon},
	}
}

// extractProviderSet flattens all six maps into a capi.ProviderSet.
func extractProviderSet(ctx context.Context, data *ClusterResourceModel) (capi.ProviderSet, diag.Diagnostics) {
	var diags diag.Diagnostics
	set := capi.ProviderSet{}
	for _, pm := range data.providerMaps() {
		entries, d := extractProviders(ctx, pm.Map)
		diags.Append(d...)
		for _, name := range sortedProviderNames(entries) {
			cfg, d := buildProviderConfig(ctx, name, pm.Type, entries[name])
			diags.Append(d...)
			set.Add(cfg)
		}
	}
	return set, diags
}

func extractTopology(ctx context.Context, data *ClusterResourceModel) (cp *int64, workers *int64, diags diag.Diagnostics) {
	if data.Topology.IsNull() || data.Topology.IsUnknown() {
		return nil, nil, nil
	}
	var topo TopologyModel
	diags.Append(data.Topology.As(ctx, &topo, basetypes.ObjectAsOptions{})...)
	if diags.HasError() {
		return nil, nil, diags
	}
	read := func(obj types.Object) *int64 {
		if obj.IsNull() || obj.IsUnknown() {
			return nil
		}
		var mc MachineCountModel
		diags.Append(obj.As(ctx, &mc, basetypes.ObjectAsOptions{})...)
		if mc.MachineCount.IsNull() || mc.MachineCount.IsUnknown() {
			return nil
		}
		v := mc.MachineCount.ValueInt64()
		return &v
	}
	return read(topo.ControlPlane), read(topo.Workers), diags
}

// machineCountObject builds a topology group object; a null count yields a null object.
func machineCountObject(ctx context.Context, v types.Int64) (types.Object, diag.Diagnostics) {
	if v.IsNull() || v.IsUnknown() {
		return types.ObjectNull(machineCountAttrTypes()), nil
	}
	return types.ObjectValueFrom(ctx, machineCountAttrTypes(), MachineCountModel{MachineCount: v})
}

// splitNameVersion splits "name:version" (version optional).
func splitNameVersion(s string) (string, string) {
	parts := strings.SplitN(s, ":", 2)
	name := strings.TrimSpace(parts[0])
	if len(parts) == 2 {
		return name, strings.TrimSpace(parts[1])
	}
	return name, ""
}

// nullProviderModel is a ProviderModel with every attribute null.
func nullProviderModel() ProviderModel {
	return ProviderModel{
		Version:               types.StringNull(),
		FetchConfig:           types.ObjectNull(fetchConfigAttrTypes()),
		ConfigVariables:       types.MapNull(types.StringType),
		SecretConfigVariables: types.MapNull(types.StringType),
		Deployment:            types.ObjectNull(deploymentAttrTypes()),
		Manager:               types.ObjectNull(managerAttrTypes()),
		AdditionalManifests:   types.StringNull(),
		ManifestPatches:       types.ListNull(types.StringType),
		Patches:               types.ListNull(types.ObjectType{AttrTypes: patchAttrTypes()}),
	}
}

// providerMapValue builds a one-entry provider map from a legacy
// "name:version" string. Used by the state upgraders.
func providerMapValue(ctx context.Context, nameVersion string) (types.Map, diag.Diagnostics) {
	name, version := splitNameVersion(nameVersion)
	pm := nullProviderModel()
	if version != "" {
		pm.Version = types.StringValue(version)
	}
	return types.MapValueFrom(ctx, providerMapType().ElemType, map[string]ProviderModel{name: pm})
}
```

`buildProviderConfig` is the body of today's per-addon loop in `buildCreateOptions` (lines ~885–1000), with the signature `func buildProviderConfig(ctx context.Context, name string, typ capi.ProviderType, pm ProviderModel) (capi.ProviderConfig, diag.Diagnostics)`, returning `capi.ProviderConfig{Name: name, Type: typ}` filled from `pm.Version`, `pm.FetchConfig` (now `FetchConfigModel` with owner/repository/url/oci), and the unchanged config-variable, deployment, manager, manifests, patch conversions.

In `buildCreateOptions`, replace the Infrastructure/Bootstrap/Control Plane/Core/Workers/Addons sections with:

```go
	// Providers
	providers, d := extractProviderSet(ctx, data)
	diags.Append(d...)
	opts.Providers = providers

	// Topology
	cpCount, workerCount, d := extractTopology(ctx, data)
	diags.Append(d...)
	opts.ControlPlaneMachineCount = cpCount
	opts.WorkerMachineCount = workerCount
```

Add `"sort"` to imports; keep `"strings"`.

- [ ] **Step 4: Schema (`cluster_resource.go`)**

Set `Version: 2`. Replace the `infrastructure`, `bootstrap`, `control_plane`, `core`, `workers`, `addons` attributes with:

```go
			// --- providers (cluster-api-operator style maps keyed by provider name) ---
			"core":           providerMapAttribute("Core CAPI provider, keyed by name (`cluster-api`). Omit to let clusterctl install the default.", false),
			"infrastructure": providerMapAttribute("Infrastructure provider, keyed by name (e.g. `docker`, `tinkerbell`). Exactly one entry is required; it also selects the cluster template.", true),
			"bootstrap":      providerMapAttribute("Bootstrap providers, keyed by name (e.g. `kubeadm`, `talos`).", false),
			"control_plane":  providerMapAttribute("Control plane providers, keyed by name (e.g. `kubeadm`, `talos`).", false),
			"ipam":           providerMapAttribute("IPAM providers, keyed by name (e.g. `in-cluster`, `unifi`).", false),
			"addon":          providerMapAttribute("Addon providers, keyed by name (e.g. `helm`). An empty object installs the default release.", false),

			// --- topology ---
			"topology": schema.SingleNestedAttribute{
				MarkdownDescription: "Machine counts for the workload cluster. Mirrors `Cluster.spec.topology`.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"control_plane": schema.SingleNestedAttribute{
						MarkdownDescription: "Control plane machines.",
						Optional:            true,
						Attributes: map[string]schema.Attribute{
							"machine_count": schema.Int64Attribute{
								MarkdownDescription: "Number of control plane machines. Use an odd number for HA (1, 3, 5).",
								Optional:            true,
							},
						},
					},
					"workers": schema.SingleNestedAttribute{
						MarkdownDescription: "Worker machines.",
						Optional:            true,
						Attributes: map[string]schema.Attribute{
							"machine_count": schema.Int64Attribute{
								MarkdownDescription: "Number of worker machines.",
								Optional:            true,
							},
						},
					},
				},
			},
```

Add these helpers below `Schema` (moving the existing addon nested attribute definitions into `providerNestedObject`):

```go
// providerMapAttribute builds one of the six provider maps. Every map shares
// providerNestedObject and is immutable: providers are installed once by
// clusterctl init, so any change recreates the cluster.
func providerMapAttribute(desc string, required bool) schema.MapNestedAttribute {
	return schema.MapNestedAttribute{
		MarkdownDescription: desc + " Each value is a provider object modeled after the cluster-api-operator provider CRDs; customizations are applied natively by wrapping the clusterctl repository factory.",
		Required:            required,
		Optional:            !required,
		NestedObject:        providerNestedObject(),
		PlanModifiers: []planmodifier.Map{
			mapplanmodifier.RequiresReplace(),
		},
	}
}

// providerNestedObject is the shared provider object.
func providerNestedObject() schema.NestedAttributeObject {
	return schema.NestedAttributeObject{
		Attributes: map[string]schema.Attribute{
			"version": schema.StringAttribute{
				MarkdownDescription: "Release tag to install (e.g. `v0.7.9`). Omit for clusterctl's latest release.",
				Optional:            true,
			},
			"fetch_config": schema.SingleNestedAttribute{
				MarkdownDescription: "Where provider components are fetched from. Set `owner` (and `repository` for providers clusterctl does not know) to build `https://github.com/{owner}/{repository}/releases/{version}/{type}-components.yaml`; `url` and `oci` are used verbatim and exclude the other attributes.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"owner": schema.StringAttribute{
						MarkdownDescription: "GitHub owner or organization. Defaults to clusterctl's built-in repository for this provider.",
						Optional:            true,
					},
					"repository": schema.StringAttribute{
						MarkdownDescription: "GitHub repository name. Defaults to clusterctl's built-in repository for this provider; required for providers clusterctl does not know.",
						Optional:            true,
					},
					"url": schema.StringAttribute{
						MarkdownDescription: "Full clusterctl components URL (e.g. `https://github.com/{owner}/{repo}/releases/v1.0.0/infrastructure-components.yaml`).",
						Optional:            true,
					},
					"oci": schema.StringAttribute{
						MarkdownDescription: "OCI artifact reference (e.g. `oci://ghcr.io/org/provider`).",
						Optional:            true,
					},
				},
			},
			"config_variables":        /* unchanged from the old addons.config_variables */,
			"secret_config_variables": /* unchanged */,
			"deployment":              /* unchanged */,
			"manager":                 /* unchanged */,
			"additional_manifests":    /* unchanged */,
			"manifest_patches":        /* unchanged */,
			"patches":                 /* unchanged */,
		},
	}
}
```

(The seven "unchanged" attributes are cut verbatim from the old `addons` `NestedObject.Attributes` at `cluster_resource.go:361-470`; only `fetch_config` is replaced.) Add the import `"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"`.

Update `ClusterResourceModel`:

```go
	Management     types.Object `tfsdk:"management"`
	Core           types.Map    `tfsdk:"core"`
	Infrastructure types.Map    `tfsdk:"infrastructure"`
	Bootstrap      types.Map    `tfsdk:"bootstrap"`
	ControlPlane   types.Map    `tfsdk:"control_plane"`
	IPAM           types.Map    `tfsdk:"ipam"`
	Addon          types.Map    `tfsdk:"addon"`
	Topology       types.Object `tfsdk:"topology"`
	Inventory      types.Object `tfsdk:"inventory"`
```

Temporarily make `validateLifecycleConfig` and the upgraders compile by stubbing (Task 7 and 8 rewrite them): in `validateLifecycleConfig` replace the infra/bootstrap/cp extraction with `providers, d := extractProviderSet(ctx, data)` and derive `provider` from `providers.Infrastructure()`; read counts through `extractTopology`. Delete `validateAddons` (its patch exclusivity check moves into Task 7). In `UpgradeState`, replace the v1 model construction for the changed attributes with calls to `providerMapValue` and `machineCountObject` (Task 8 finishes and tests this).

- [ ] **Step 5: Build and run the tests**

Run: `go build ./... && go test ./internal/provider/ -run 'TestClusterResource_Schema|TestBuildCreateOptions|TestExtract|TestSetStatus|TestNullStatus' -count=1`
Expected: PASS.

- [ ] **Step 6: Lint and commit**

Run: `if ! rtk proxy golangci-lint run ./...; then echo LINT FAILED; fi`
Expected: no output besides the lint summary; fix anything reported.

```bash
git add internal/provider
git commit -m "Model providers as cluster-api-operator style maps with topology counts"
```

---

### Task 7: Validation over provider maps

**Files:**
- Modify: `internal/provider/cluster_resource.go` (`validateLifecycleConfig`)
- Modify: `internal/provider/cluster_resource_models.go` (add `validateProviderEntry`)
- Modify: `internal/provider/cluster_resource_models_test.go`

**Interfaces:**
- Produces: `func validateProviderEntry(ctx, typ capi.ProviderType, name string, pm ProviderModel, diags *diag.Diagnostics)`; `validateLifecycleConfig` semantics per spec.

- [ ] **Step 1: Write the failing tests**

Replace `TestValidateLifecycleConfig_*` in `cluster_resource_models_test.go` with:

```go
func validateWith(t *testing.T, ctx context.Context, mutate func(*ClusterResourceModel)) diag.Diagnostics {
	t.Helper()
	data := baseModel(t, ctx)
	mutate(data)
	var diags diag.Diagnostics
	(&ClusterResource{}).validateLifecycleConfig(ctx, data, &diags)
	return diags
}

func hasErrorContaining(diags diag.Diagnostics, s string) bool {
	for _, d := range diags.Errors() {
		if strings.Contains(d.Summary()+" "+d.Detail(), s) {
			return true
		}
	}
	return false
}

func selfManaged(t *testing.T, ctx context.Context) types.Object {
	t.Helper()
	v, d := types.ObjectValueFrom(ctx, managementAttrTypes(), ManagementModel{
		Kubeconfig: types.StringNull(), SkipInit: types.BoolValue(false), SelfManaged: types.BoolValue(true),
		Namespace: types.StringNull(), Bootstrap: types.ObjectNull(managementBootstrapAttrTypes()),
	})
	if d.HasError() {
		t.Fatal(d)
	}
	return v
}

func TestValidateLifecycleConfig_Docker(t *testing.T) {
	ctx := context.Background()
	if diags := validateWith(t, ctx, func(*ClusterResourceModel) {}); diags.HasError() {
		t.Errorf("docker should be valid: %v", diags)
	}
}

func TestValidateLifecycleConfig_InfrastructureExactlyOne(t *testing.T) {
	ctx := context.Background()
	diags := validateWith(t, ctx, func(d *ClusterResourceModel) {
		d.Infrastructure = mustProviderMap(t, ctx, map[string]ProviderModel{"docker": emptyProviderModel(), "aws": emptyProviderModel()})
	})
	if !hasErrorContaining(diags, "exactly one") {
		t.Errorf("two infrastructure providers must be rejected: %v", diags)
	}
	diags = validateWith(t, ctx, func(d *ClusterResourceModel) {
		d.Infrastructure = mustProviderMap(t, ctx, map[string]ProviderModel{})
	})
	if !hasErrorContaining(diags, "exactly one") {
		t.Errorf("empty infrastructure map must be rejected: %v", diags)
	}
}

func TestValidateLifecycleConfig_UnsupportedProvider(t *testing.T) {
	ctx := context.Background()
	diags := validateWith(t, ctx, func(d *ClusterResourceModel) {
		d.Infrastructure = mustProviderMap(t, ctx, map[string]ProviderModel{"gcp": emptyProviderModel()})
	})
	if !hasErrorContaining(diags, "not supported") {
		t.Errorf("gcp must be rejected: %v", diags)
	}
}

func TestValidateLifecycleConfig_TinkerbellRequiresSelfManaged(t *testing.T) {
	ctx := context.Background()
	diags := validateWith(t, ctx, func(d *ClusterResourceModel) {
		d.Infrastructure = mustProviderMap(t, ctx, map[string]ProviderModel{"tinkerbell": emptyProviderModel()})
	})
	if !hasErrorContaining(diags, "self_managed") {
		t.Errorf("tinkerbell without self_managed must fail: %v", diags)
	}
}

func TestValidateLifecycleConfig_TinkerbellWithTalos(t *testing.T) {
	ctx := context.Background()
	diags := validateWith(t, ctx, func(d *ClusterResourceModel) {
		d.Infrastructure = mustProviderMap(t, ctx, map[string]ProviderModel{"tinkerbell": emptyProviderModel()})
		d.Management = selfManaged(t, ctx)
		d.Bootstrap = mustProviderMap(t, ctx, map[string]ProviderModel{"talos": emptyProviderModel()})
		d.ControlPlane = mustProviderMap(t, ctx, map[string]ProviderModel{"talos": emptyProviderModel()})
	})
	if diags.HasError() {
		t.Errorf("tinkerbell+talos should be valid: %v", diags)
	}
}

func TestValidateLifecycleConfig_TinkerbellInvalidBootstrap(t *testing.T) {
	ctx := context.Background()
	diags := validateWith(t, ctx, func(d *ClusterResourceModel) {
		d.Infrastructure = mustProviderMap(t, ctx, map[string]ProviderModel{"tinkerbell": emptyProviderModel()})
		d.Management = selfManaged(t, ctx)
		d.Bootstrap = mustProviderMap(t, ctx, map[string]ProviderModel{"microk8s": emptyProviderModel()})
	})
	if !hasErrorContaining(diags, "bootstrap") {
		t.Errorf("microk8s bootstrap must be rejected for tinkerbell: %v", diags)
	}
}

func TestValidateLifecycleConfig_FetchConfigExclusivity(t *testing.T) {
	ctx := context.Background()
	mk := func(fc FetchConfigModel) func(*ClusterResourceModel) {
		return func(d *ClusterResourceModel) {
			obj, _ := types.ObjectValueFrom(ctx, fetchConfigAttrTypes(), fc)
			pm := emptyProviderModel()
			pm.FetchConfig = obj
			d.Addon = mustProviderMap(t, ctx, map[string]ProviderModel{"helm": pm})
		}
	}
	null := types.StringNull()
	if diags := validateWith(t, ctx, mk(FetchConfigModel{Owner: types.StringValue("a"), Repository: null, URL: types.StringValue("https://x"), OCI: null})); !hasErrorContaining(diags, "fetch_config") {
		t.Errorf("owner+url must fail: %v", diags)
	}
	if diags := validateWith(t, ctx, mk(FetchConfigModel{Owner: null, Repository: null, URL: types.StringValue("https://x"), OCI: types.StringValue("oci://y")})); !hasErrorContaining(diags, "fetch_config") {
		t.Errorf("url+oci must fail: %v", diags)
	}
	if diags := validateWith(t, ctx, mk(FetchConfigModel{Owner: types.StringValue("a"), Repository: types.StringValue("b"), URL: null, OCI: null})); diags.HasError() {
		t.Errorf("owner+repository is valid: %v", diags)
	}
}

func TestValidateLifecycleConfig_PatchExclusivity(t *testing.T) {
	ctx := context.Background()
	diags := validateWith(t, ctx, func(d *ClusterResourceModel) {
		pm := emptyProviderModel()
		pm.ManifestPatches, _ = types.ListValueFrom(ctx, types.StringType, []string{"{}"})
		target := types.ObjectNull(patchSelectorAttrTypes())
		pm.Patches, _ = types.ListValueFrom(ctx, types.ObjectType{AttrTypes: patchAttrTypes()}, []PatchModel{{Patch: types.StringValue("{}"), Target: target}})
		d.Addon = mustProviderMap(t, ctx, map[string]ProviderModel{"helm": pm})
	})
	if !hasErrorContaining(diags, "manifest_patches") {
		t.Errorf("patches and manifest_patches together must fail: %v", diags)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/provider/ -run TestValidateLifecycleConfig -count=1`
Expected: FAIL on `InfrastructureExactlyOne`, `FetchConfigExclusivity`, `PatchExclusivity` (the others may pass from the Task 6 stub).

- [ ] **Step 3: Implement**

Add to `cluster_resource_models.go`:

```go
// validateProviderEntry checks the static rules of one provider map entry.
func validateProviderEntry(ctx context.Context, typ capi.ProviderType, name string, pm ProviderModel, diags *diag.Diagnostics) {
	where := fmt.Sprintf("%s[%q]", providerAttrName(typ), name)

	if !pm.FetchConfig.IsNull() && !pm.FetchConfig.IsUnknown() {
		var fc FetchConfigModel
		diags.Append(pm.FetchConfig.As(ctx, &fc, basetypes.ObjectAsOptions{})...)
		set := func(s types.String) bool { return !s.IsNull() && !s.IsUnknown() && s.ValueString() != "" }
		hasURL, hasOCI := set(fc.URL), set(fc.OCI)
		hasRepoParts := set(fc.Owner) || set(fc.Repository)
		switch {
		case hasURL && hasOCI:
			diags.AddError("Invalid fetch_config", where+".fetch_config: url and oci are mutually exclusive.")
		case (hasURL || hasOCI) && hasRepoParts:
			diags.AddError("Invalid fetch_config", where+".fetch_config: owner/repository cannot be combined with url or oci.")
		}
	}

	hasManifestPatches := !pm.ManifestPatches.IsNull() && !pm.ManifestPatches.IsUnknown() && len(pm.ManifestPatches.Elements()) > 0
	hasPatches := !pm.Patches.IsNull() && !pm.Patches.IsUnknown() && len(pm.Patches.Elements()) > 0
	if hasManifestPatches && hasPatches {
		diags.AddError("Invalid provider patches", where+": manifest_patches and patches cannot be used together.")
	}
}

// providerAttrName maps a provider type to its schema attribute name.
func providerAttrName(typ capi.ProviderType) string {
	switch typ {
	case capi.ProviderTypeControlPlane:
		return "control_plane"
	default:
		return string(typ)
	}
}
```

Rewrite `validateLifecycleConfig` in `cluster_resource.go`:

```go
func (r *ClusterResource) validateLifecycleConfig(ctx context.Context, data *ClusterResourceModel, diags *diag.Diagnostics) {
	// Per-entry rules and name collection for every provider map.
	names := map[capi.ProviderType][]string{}
	for _, pm := range data.providerMaps() {
		entries, d := extractProviders(ctx, pm.Map)
		diags.Append(d...)
		for _, name := range sortedProviderNames(entries) {
			names[pm.Type] = append(names[pm.Type], name)
			validateProviderEntry(ctx, pm.Type, name, entries[name], diags)
		}
	}
	if diags.HasError() {
		return
	}

	if data.Infrastructure.IsUnknown() {
		return
	}
	infraNames := names[capi.ProviderTypeInfrastructure]
	if len(infraNames) != 1 {
		diags.AddError("Invalid infrastructure configuration",
			fmt.Sprintf("infrastructure must contain exactly one provider, got %d.", len(infraNames)))
		return
	}
	provider := strings.ToLower(infraNames[0])

	supportedProviders := map[string]struct{}{
		"aws": {}, "azure": {}, "docker": {}, "openstack": {}, "tinkerbell": {}, "vsphere": {},
	}
	if _, ok := supportedProviders[provider]; !ok {
		diags.AddError("Unsupported infrastructure provider",
			fmt.Sprintf("infrastructure provider %q is not supported. Supported: aws, azure, docker, openstack, tinkerbell, vsphere", provider))
	}

	if provider == "tinkerbell" {
		mgmt, d := extractManagement(ctx, data)
		diags.Append(d...)
		if mgmt == nil || mgmt.SelfManaged.IsNull() || !mgmt.SelfManaged.ValueBool() {
			diags.AddError("Invalid Tinkerbell configuration", "Tinkerbell clusters must have management.self_managed = true.")
		}
		for _, n := range names[capi.ProviderTypeBootstrap] {
			if l := strings.ToLower(n); l != "kubeadm" && l != "talos" {
				diags.AddError("Invalid bootstrap provider for Tinkerbell", fmt.Sprintf("Tinkerbell supports bootstrap providers kubeadm or talos, got %q.", n))
			}
		}
		for _, n := range names[capi.ProviderTypeControlPlane] {
			if l := strings.ToLower(n); l != "kubeadm" && l != "talos" {
				diags.AddError("Invalid control plane provider for Tinkerbell", fmt.Sprintf("Tinkerbell supports control_plane providers kubeadm or talos, got %q.", n))
			}
		}
	}

	// Validate inventory against topology counts.
	inv, d := extractInventory(ctx, data)
	diags.Append(d...)
	if inv != nil {
		cpCount, workerCount, d := extractTopology(ctx, data)
		diags.Append(d...)
		var cp, w int64
		if cpCount != nil {
			cp = *cpCount
		}
		if workerCount != nil {
			w = *workerCount
		}
		validateInventory(ctx, inv, cp, w, diags)
	}

	validateManagementBootstrap(ctx, data, diags)
}
```

(Keep whatever the existing function does after inventory validation, such as the `validateManagementBootstrap` call, exactly as it is today.)

- [ ] **Step 4: Run tests**

Run: `go test ./internal/provider/ -run 'TestValidate' -count=1`
Expected: PASS, including the bootstrap validation tests.

- [ ] **Step 5: Lint and commit**

```bash
if ! rtk proxy golangci-lint run ./...; then echo LINT FAILED; fi
git add internal/provider
git commit -m "Validate provider maps, fetch_config exclusivity and topology counts"
```

---

### Task 8: State upgraders v0→v2 and v1→v2

**Files:**
- Modify: `internal/provider/cluster_resource.go` (`UpgradeState`, `ClusterResourceModelV0`)
- Create: `internal/provider/cluster_resource_upgrade.go` (`upgradeV1JSON`)
- Modify: `internal/provider/cluster_resource_models_test.go` (`TestClusterResource_UpgradeState`)
- Create: `internal/provider/cluster_resource_upgrade_test.go`

**Interfaces:**
- Consumes: `providerMapValue`, `machineCountObject`, `splitNameVersion`, `nullProviderModel` (Task 6).
- Produces: `func upgradeV1JSON(raw []byte) ([]byte, error)`.

- [ ] **Step 1: Write the failing tests**

`internal/provider/cluster_resource_upgrade_test.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

const v1State = `{
  "id": "acc-talos", "name": "acc-talos", "kubernetes_version": "v1.34.0", "flavor": null,
  "provider_secrets": null,
  "infrastructure": {"provider": "tinkerbell:v0.5.4"},
  "bootstrap": {"provider": "talos:v0.6.7"},
  "control_plane": {"provider": "talos:v0.6.7", "machine_count": 1},
  "core": null,
  "workers": {"machine_count": 2},
  "addons": [{
    "provider": "helm:v0.2.12",
    "config_variables": {"A": "b"}, "secret_config_variables": null,
    "fetch_config": {"url": "https://example.com/addon-components.yaml", "oci": null},
    "deployment": null,
    "manager": {"profiler_address": null, "max_concurrent_reconciles": null, "verbosity": 3, "feature_gates": {"X": true}, "additional_args": null},
    "additional_manifests": null, "manifest_patches": null, "patches": null
  }],
  "management": null, "inventory": null, "wait": null, "output": null, "status": null
}`

func TestUpgradeV1JSON(t *testing.T) {
	out, err := upgradeV1JSON([]byte(v1State))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"workers", "addons"} {
		if _, ok := got[gone]; ok {
			t.Errorf("%s must be removed", gone)
		}
	}
	var infra map[string]map[string]json.RawMessage
	if err := json.Unmarshal(got["infrastructure"], &infra); err != nil {
		t.Fatal(err)
	}
	if string(infra["tinkerbell"]["version"]) != `"v0.5.4"` {
		t.Errorf("infrastructure = %s", got["infrastructure"])
	}
	if len(infra["tinkerbell"]) != 9 {
		t.Errorf("provider object must carry all 9 attributes, got %d", len(infra["tinkerbell"]))
	}
	var addon map[string]map[string]json.RawMessage
	if err := json.Unmarshal(got["addon"], &addon); err != nil {
		t.Fatal(err)
	}
	if string(addon["helm"]["version"]) != `"v0.2.12"` || string(addon["helm"]["config_variables"]) != `{"A":"b"}` {
		t.Errorf("addon = %s", got["addon"])
	}
	var fc map[string]json.RawMessage
	if err := json.Unmarshal(addon["helm"]["fetch_config"], &fc); err != nil {
		t.Fatal(err)
	}
	if string(fc["url"]) != `"https://example.com/addon-components.yaml"` || string(fc["owner"]) != "null" || string(fc["repository"]) != "null" {
		t.Errorf("fetch_config = %s", addon["helm"]["fetch_config"])
	}
	if string(got["topology"]) != `{"control_plane":{"machine_count":1},"workers":{"machine_count":2}}` {
		t.Errorf("topology = %s", got["topology"])
	}
	if string(got["ipam"]) != "null" || string(got["core"]) != "null" {
		t.Errorf("ipam/core = %s/%s", got["ipam"], got["core"])
	}
	if string(got["management"]) != "null" || string(got["name"]) != `"acc-talos"` {
		t.Error("untouched attributes must pass through")
	}
}

// decodeAgainstV2 proves the upgraded JSON is valid v2 state.
func decodeAgainstV2(t *testing.T, raw []byte) tftypes.Value {
	t.Helper()
	resp := &resource.SchemaResponse{}
	NewClusterResource().Schema(context.Background(), resource.SchemaRequest{}, resp)
	typ := resp.Schema.Type().TerraformType(context.Background())
	v, err := tftypes.ValueFromJSON(raw, typ)
	if err != nil {
		t.Fatalf("upgraded state does not decode against v2 schema: %v", err)
	}
	return v
}

func TestUpgradeState_V1ToV2_ThroughFramework(t *testing.T) {
	ctx := context.Background()
	r := &ClusterResource{}
	up, ok := r.UpgradeState(ctx)[1]
	if !ok {
		t.Fatal("expected v1 upgrader")
	}
	resp := &resource.UpgradeStateResponse{}
	up.StateUpgrader(ctx, resource.UpgradeStateRequest{RawState: &tfprotov6.RawState{JSON: []byte(v1State)}}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if resp.DynamicValue == nil {
		t.Fatal("v1 upgrader must set DynamicValue")
	}
	decodeAgainstV2(t, resp.DynamicValue.JSON)
}

func TestUpgradeState_V0ToV2(t *testing.T) {
	ctx := context.Background()
	r := &ClusterResource{}
	upgraders := r.UpgradeState(ctx)
	up, ok := upgraders[0]
	if !ok || up.PriorSchema == nil {
		t.Fatal("expected v0 upgrader with PriorSchema")
	}

	v0Type := up.PriorSchema.Type().TerraformType(ctx)
	v0JSON := []byte(`{
	  "id": "c", "name": "c", "kubernetes_version": "v1.31.0", "flavor": null,
	  "management_kubeconfig": null, "skip_init": false, "self_managed": true, "target_namespace": null,
	  "infrastructure_provider": "tinkerbell:v0.5.4", "bootstrap_provider": "kubeadm", "control_plane_provider": "kubeadm:v1.12.2",
	  "control_plane_machine_count": 3, "core_provider": "cluster-api:v1.12.2", "worker_machine_count": 2,
	  "wait_for_ready": true, "kubeconfig_path": null,
	  "endpoint": null, "kubeconfig": null, "cluster_ca_certificate": null, "cluster_description": null, "bootstrap_cluster_name": null
	}`)
	v0Val, err := tftypes.ValueFromJSON(v0JSON, v0Type)
	if err != nil {
		t.Fatalf("fixture does not match v0 schema (adjust the fixture to the PriorSchema attributes): %v", err)
	}
	dv, err := tfprotov6.NewDynamicValue(v0Type, v0Val)
	if err != nil {
		t.Fatal(err)
	}

	v2Resp := &resource.SchemaResponse{}
	NewClusterResource().Schema(ctx, resource.SchemaRequest{}, v2Resp)
	resp := &resource.UpgradeStateResponse{State: tfsdk.State{Schema: v2Resp.Schema, Raw: tftypes.NewValue(v2Resp.Schema.Type().TerraformType(ctx), nil)}}
	up.StateUpgrader(ctx, resource.UpgradeStateRequest{State: &tfsdk.State{Schema: *up.PriorSchema, Raw: v0Val}}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}

	var out ClusterResourceModel
	if d := resp.State.Get(ctx, &out); d.HasError() {
		t.Fatal(d)
	}
	infra, _ := extractProviders(ctx, out.Infrastructure)
	if infra["tinkerbell"].Version.ValueString() != "v0.5.4" {
		t.Errorf("infrastructure = %v", out.Infrastructure)
	}
	bs, _ := extractProviders(ctx, out.Bootstrap)
	if _, ok := bs["kubeadm"]; !ok || !bs["kubeadm"].Version.IsNull() {
		t.Errorf("bootstrap = %v", out.Bootstrap)
	}
	cp, w, _ := extractTopology(ctx, &out)
	if cp == nil || *cp != 3 || w == nil || *w != 2 {
		t.Errorf("topology counts = %v %v", cp, w)
	}
	_ = dv
}
```

Add `"github.com/hashicorp/terraform-plugin-framework/tfsdk"` to the imports. Replace the old `TestClusterResource_UpgradeState` with one asserting both keys `0` and `1` exist.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/provider/ -run 'TestUpgrade' -count=1`
Expected: `undefined: upgradeV1JSON`; v0 test fails.

- [ ] **Step 3: Implement `upgradeV1JSON`**

`internal/provider/cluster_resource_upgrade.go`:

```go
// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"fmt"
)

// providerAttrNames lists every attribute of the v2 provider object. The JSON
// handed back to Terraform must carry all of them (null when unset) or it
// will not decode against the schema.
var providerAttrNames = []string{
	"version", "fetch_config", "config_variables", "secret_config_variables",
	"deployment", "manager", "additional_manifests", "manifest_patches", "patches",
}

var nullJSON = json.RawMessage("null")

// upgradeV1JSON rewrites raw v1 state (object-with-provider-string attributes
// and an addons list) into v2 state (provider maps and topology).
func upgradeV1JSON(raw []byte) ([]byte, error) {
	var state map[string]json.RawMessage
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("decoding v1 state: %w", err)
	}

	var cpCount, workerCount json.RawMessage = nullJSON, nullJSON

	for _, attr := range []string{"infrastructure", "bootstrap", "control_plane", "core"} {
		obj, err := rawObject(state[attr])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", attr, err)
		}
		if obj == nil {
			state[attr] = nullJSON
			continue
		}
		if attr == "control_plane" {
			if mc, ok := obj["machine_count"]; ok {
				cpCount = mc
			}
		}
		m, err := providerMapJSON(obj["provider"], nil)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", attr, err)
		}
		state[attr] = m
	}

	if workers, err := rawObject(state["workers"]); err != nil {
		return nil, fmt.Errorf("workers: %w", err)
	} else if workers != nil {
		if mc, ok := workers["machine_count"]; ok {
			workerCount = mc
		}
	}
	delete(state, "workers")

	addonMap := map[string]json.RawMessage{}
	if len(state["addons"]) > 0 && string(state["addons"]) != "null" {
		var addons []map[string]json.RawMessage
		if err := json.Unmarshal(state["addons"], &addons); err != nil {
			return nil, fmt.Errorf("addons: %w", err)
		}
		for _, a := range addons {
			name, entry, err := providerEntryJSON(a["provider"], a)
			if err != nil {
				return nil, fmt.Errorf("addons: %w", err)
			}
			addonMap[name] = entry
		}
	}
	delete(state, "addons")
	if len(addonMap) == 0 {
		state["addon"] = nullJSON
	} else {
		b, err := json.Marshal(addonMap)
		if err != nil {
			return nil, err
		}
		state["addon"] = b
	}
	state["ipam"] = nullJSON

	topology := map[string]json.RawMessage{
		"control_plane": machineCountJSON(cpCount),
		"workers":       machineCountJSON(workerCount),
	}
	if string(topology["control_plane"]) == "null" && string(topology["workers"]) == "null" {
		state["topology"] = nullJSON
	} else {
		b, err := json.Marshal(topology)
		if err != nil {
			return nil, err
		}
		state["topology"] = b
	}

	return json.Marshal(state)
}

func rawObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// providerMapJSON builds {name: providerObject} from a "name:version" string.
func providerMapJSON(providerRaw json.RawMessage, extra map[string]json.RawMessage) (json.RawMessage, error) {
	name, entry, err := providerEntryJSON(providerRaw, extra)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]json.RawMessage{name: entry})
}

// providerEntryJSON returns the provider name and a full v2 provider object,
// copying any matching attributes from extra (a v1 addons element).
func providerEntryJSON(providerRaw json.RawMessage, extra map[string]json.RawMessage) (string, json.RawMessage, error) {
	var providerStr string
	if err := json.Unmarshal(providerRaw, &providerStr); err != nil {
		return "", nil, fmt.Errorf("provider must be a string: %w", err)
	}
	name, version := splitNameVersion(providerStr)
	if name == "" {
		return "", nil, fmt.Errorf("empty provider name in %q", providerStr)
	}

	entry := map[string]json.RawMessage{}
	for _, a := range providerAttrNames {
		entry[a] = nullJSON
		if v, ok := extra[a]; ok && len(v) > 0 {
			entry[a] = v
		}
	}
	if version != "" {
		b, _ := json.Marshal(version)
		entry["version"] = b
	}

	// v1 fetch_config had only url/oci; v2 adds owner/repository.
	if fc, err := rawObject(entry["fetch_config"]); err != nil {
		return "", nil, fmt.Errorf("fetch_config: %w", err)
	} else if fc != nil {
		for _, k := range []string{"owner", "repository", "url", "oci"} {
			if _, ok := fc[k]; !ok {
				fc[k] = nullJSON
			}
		}
		b, err := json.Marshal(fc)
		if err != nil {
			return "", nil, err
		}
		entry["fetch_config"] = b
	}

	b, err := json.Marshal(entry)
	if err != nil {
		return "", nil, err
	}
	return name, b, nil
}

func machineCountJSON(count json.RawMessage) json.RawMessage {
	if len(count) == 0 || string(count) == "null" {
		return nullJSON
	}
	b, _ := json.Marshal(map[string]json.RawMessage{"machine_count": count})
	return b
}
```

- [ ] **Step 4: Wire `UpgradeState`**

In `cluster_resource.go`, the v0 upgrader keeps its `PriorSchema` and model reading. Replace its `infrastructure`/`bootstrap`/`control_plane`/`core`/`workers` construction with:

```go
				v1.Core = types.MapNull(providerMapType().ElemType)
				v1.Bootstrap = types.MapNull(providerMapType().ElemType)
				v1.ControlPlane = types.MapNull(providerMapType().ElemType)
				v1.IPAM = types.MapNull(providerMapType().ElemType)
				v1.Addon = types.MapNull(providerMapType().ElemType)

				var d diag.Diagnostics
				v1.Infrastructure, d = providerMapValue(ctx, v0.InfrastructureProvider.ValueString())
				resp.Diagnostics.Append(d...)
				if !v0.BootstrapProvider.IsNull() && v0.BootstrapProvider.ValueString() != "" {
					v1.Bootstrap, d = providerMapValue(ctx, v0.BootstrapProvider.ValueString())
					resp.Diagnostics.Append(d...)
				}
				if !v0.ControlPlaneProvider.IsNull() && v0.ControlPlaneProvider.ValueString() != "" {
					v1.ControlPlane, d = providerMapValue(ctx, v0.ControlPlaneProvider.ValueString())
					resp.Diagnostics.Append(d...)
				}
				if !v0.CoreProvider.IsNull() && v0.CoreProvider.ValueString() != "" {
					v1.Core, d = providerMapValue(ctx, v0.CoreProvider.ValueString())
					resp.Diagnostics.Append(d...)
				}

				cpObj, d := machineCountObject(ctx, v0.ControlPlaneMachineCount)
				resp.Diagnostics.Append(d...)
				wObj, d := machineCountObject(ctx, v0.WorkerMachineCount)
				resp.Diagnostics.Append(d...)
				if cpObj.IsNull() && wObj.IsNull() {
					v1.Topology = types.ObjectNull(topologyAttrTypes())
				} else {
					v1.Topology, d = types.ObjectValueFrom(ctx, topologyAttrTypes(), TopologyModel{ControlPlane: cpObj, Workers: wObj})
					resp.Diagnostics.Append(d...)
				}
```

(rename the local `v1` to `v2` for clarity if you like; keep the rest of that upgrader as is). Add the v1 entry:

```go
		1: {
			// No PriorSchema: the v1 schema was large and only five attributes
			// change shape, so the upgrade rewrites the raw JSON directly.
			StateUpgrader: func(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
				if req.RawState == nil || len(req.RawState.JSON) == 0 {
					resp.Diagnostics.AddError("Unable to upgrade state", "Missing raw state for schema version 1.")
					return
				}
				out, err := upgradeV1JSON(req.RawState.JSON)
				if err != nil {
					resp.Diagnostics.AddError("Unable to upgrade state", fmt.Sprintf("Upgrading capi_cluster state from schema version 1 to 2: %s", err))
					return
				}
				resp.DynamicValue = &tfprotov6.DynamicValue{JSON: out}
			},
		},
```

Add the import `"github.com/hashicorp/terraform-plugin-go/tfprotov6"`. Update the doc comment: "UpgradeState migrates v0 (flat) and v1 (nested objects) state to v2 (provider maps)."

- [ ] **Step 5: Run tests**

Run: `go test ./internal/provider/ -run 'TestUpgrade|TestClusterResource_UpgradeState' -count=1 -v`
Expected: PASS. If `TestUpgradeState_V0ToV2` reports the fixture does not match the v0 schema, align the fixture keys with `v0Schema`'s attribute names (they are the flat names from the CLAUDE.md table).

- [ ] **Step 6: Lint and commit**

```bash
if ! rtk proxy golangci-lint run ./...; then echo LINT FAILED; fi
git add internal/provider
git commit -m "Upgrade v0 and v1 cluster state to the provider-map schema"
```

---

### Task 9: Examples, acceptance configs, docs and CLAUDE.md

**Files:**
- Modify: `examples/resources/capi_cluster/resource.tf`, `examples/resources/capi_cluster/talos-bootstrap.tf`
- Modify: `.test/main.tf`
- Modify: `internal/provider/cluster_resource_test.go:55-75, 130-140` (acceptance HCL)
- Regenerate: `docs/resources/cluster.md`
- Modify: `.claude/CLAUDE.md` §2, §3.3–3.7, §4.1–4.2, §10

- [ ] **Step 1: Rewrite the example configs**

`examples/resources/capi_cluster/resource.tf`:

```hcl
resource "capi_cluster" "example" {
  name               = "my-cluster"
  kubernetes_version = "v1.31.0"

  infrastructure = { docker = {} }
  bootstrap      = { kubeadm = {} }
  control_plane  = { kubeadm = {} }

  topology = {
    control_plane = { machine_count = 1 }
    workers       = { machine_count = 2 }
  }

  wait = {
    enabled = true
    timeout = "30m"
  }

  output = {
    kubeconfig_path = "/tmp/my-cluster-kubeconfig"
  }
}
```

In `talos-bootstrap.tf` replace the `infrastructure`/`bootstrap`/`control_plane`/`workers` blocks with:

```hcl
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
    control_plane = { machine_count = 3 }
    workers       = { machine_count = 2 }
  }
```

`.test/main.tf`: same provider blocks as above with `topology = { control_plane = { machine_count = 1 } }`, keeping its `management`, `inventory` and `wait` blocks.

`cluster_resource_test.go`: the docker acceptance HCL uses `infrastructure = { docker = {} }` and `topology = { control_plane = { machine_count = 1 }, workers = { machine_count = 1 } }`; the Talos acceptance HCL uses the tinkerbell/talos blocks above with `topology = { control_plane = { machine_count = 1 } }`. Update any `resource.TestCheckResourceAttr` paths (`infrastructure.provider` → `infrastructure.tinkerbell.version`, `control_plane.machine_count` → `topology.control_plane.machine_count`).

Run: `terraform fmt -recursive examples .test` and `go vet ./internal/provider/` (vet compiles the acceptance tests).

- [ ] **Step 2: Regenerate docs**

```bash
go generate ./...
git checkout docs/guides
git status --short docs
```

Expected: `docs/resources/cluster.md` modified; guides unchanged. Open the file and confirm the provider maps show as "Attributes Map" and `topology` appears.

- [ ] **Step 3: Update CLAUDE.md**

In `.claude/CLAUDE.md`:
- §2.1/2.2/2.3 HCL: replace `infrastructure { provider = ... }` style blocks with the map form and `topology`; drop `workers`.
- §3.3–3.7: replace with one section "3.3 Provider maps (`core`, `infrastructure`, `bootstrap`, `control_plane`, `ipam`, `addon`)" describing `providerMapAttribute`, the shared object attributes, `fetch_config` resolution (owner/repository/url/oci; defaults from clusterctl; version pinned in the URL), the exactly-one rule for `infrastructure`, and `RequiresReplace`; and "3.4 `topology`" with the two machine counts.
- §4.1/4.2: model fields `Core/Infrastructure/Bootstrap/ControlPlane/IPAM/Addon types.Map`, `Topology types.Object`; `ProviderModel`, `FetchConfigModel`, `TopologyModel`, `MachineCountModel`.
- §8.2 version table: add row `2 | Provider maps, fetch_config owner/repository, topology, ipam | provider strings become map keys; workers/addons removed`.
- §10 location map: rows for `infrastructure.provider` → `infrastructure.<name>` (Map, immutable), `control_plane.machine_count` → `topology.control_plane.machine_count` (mutable), `workers.machine_count` → `topology.workers.machine_count`, `addons[]` → `addon.<name>`, new `ipam.<name>`, new `<map>.<name>.fetch_config.{owner,repository,url,oci}`.

- [ ] **Step 4: Full verification**

```bash
go build ./... && go test ./... -count=1
if ! rtk proxy golangci-lint run ./...; then echo LINT FAILED; fi
terraform fmt -check -recursive examples .test
```

Expected: build OK, all unit tests PASS (acceptance tests skip without their env vars), lint clean, fmt clean.

- [ ] **Step 5: Commit**

```bash
git add examples .test internal/provider/cluster_resource_test.go docs .claude/CLAUDE.md
git commit -m "Document and exemplify the provider-map schema"
```

---

## Self-review notes

- Spec coverage: provider maps (T6), fetch_config resolution (T2, T5), overlay reader (T3), type+name keying (T4), IPAM (T5), topology (T6), validation incl. exactly-one and exclusivity (T7), v0/v1 upgraders (T8), docs/examples/CLAUDE.md (T9). Unknown-provider check surfaces at create time from `buildInitOptions` (T5), as the spec allows.
- Names used across tasks: `ProviderSet.InitStrings/Infrastructure/Customized/All`, `ResolveFetchURL`, `ErrUnknownProviderRepository`, `newOverlayReader/AddOverride`, `buildInitOptions`, `providerAttrTypes/providerMapType/fetchConfigAttrTypes/topologyAttrTypes/machineCountAttrTypes`, `extractProviders/extractProviderSet/extractTopology/providerMapValue/machineCountObject/splitNameVersion/nullProviderModel`, `deploymentAttrTypes/managerAttrTypes/patchAttrTypes/patchSelectorAttrTypes/containerAttrTypes`, `DeploymentModel/ManagerModel/PatchModel/PatchSelectorModel/ContainerModel`, `upgradeV1JSON`.
