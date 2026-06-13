// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/tinkerbell-community/terraform-provider-capi/internal/capi"
)

// ClusterResourceModel describes the resource data model using map-based provider
// configurations. This is the v2 schema model matching the capi-operator Helm
// chart pattern where each provider type is a map keyed by provider name.
type ClusterResourceModel struct {
	Name              types.String `tfsdk:"name"`
	KubernetesVersion types.String `tfsdk:"kubernetes_version"`
	Flavor            types.String `tfsdk:"flavor"`
	Id                types.String `tfsdk:"id"`

	Management     types.Object `tfsdk:"management"`
	Infrastructure types.Map    `tfsdk:"infrastructure"`
	Bootstrap      types.Map    `tfsdk:"bootstrap"`
	ControlPlane   types.Map    `tfsdk:"control_plane"`
	Core           types.Map    `tfsdk:"core"`
	Addon          types.Map    `tfsdk:"addon"`
	Topology       types.Object `tfsdk:"topology"`
	Inventory      types.Object `tfsdk:"inventory"`
	Wait           types.Object `tfsdk:"wait"`
	Output         types.Object `tfsdk:"output"`
	Status         types.Object `tfsdk:"status"`
}

// ManagementModel groups attributes related to cluster management configuration.
type ManagementModel struct {
	Kubeconfig  types.String `tfsdk:"kubeconfig"`
	SkipInit    types.Bool   `tfsdk:"skip_init"`
	SelfManaged types.Bool   `tfsdk:"self_managed"`
	Namespace   types.String `tfsdk:"namespace"`
}

// TopologyModel groups cluster topology configuration (machine counts).
type TopologyModel struct {
	ControlPlaneCount types.Int64 `tfsdk:"control_plane_count"`
	WorkerCount       types.Int64 `tfsdk:"worker_count"`
}

// InventoryModel groups attributes for hardware inventory.
type InventoryModel struct {
	Source  types.String `tfsdk:"source"`
	Machine types.List   `tfsdk:"machine"`
}

// MachineModel describes a single machine in the inventory.
type MachineModel struct {
	Hostname types.String `tfsdk:"hostname"`
	Network  types.Object `tfsdk:"network"`
	Disk     types.Object `tfsdk:"disk"`
	BMC      types.Object `tfsdk:"bmc"`
	Labels   types.Map    `tfsdk:"labels"`
}

// NetworkModel describes machine network configuration.
type NetworkModel struct {
	IPAddress   types.String `tfsdk:"ip_address"`
	Netmask     types.String `tfsdk:"netmask"`
	Gateway     types.String `tfsdk:"gateway"`
	MACAddress  types.String `tfsdk:"mac_address"`
	Nameservers types.List   `tfsdk:"nameservers"`
	VLANID      types.String `tfsdk:"vlan_id"`
}

// DiskModel describes machine disk configuration.
type DiskModel struct {
	Device types.String `tfsdk:"device"`
}

// BMCModel describes BMC (Baseboard Management Controller) configuration.
type BMCModel struct {
	Address  types.String `tfsdk:"address"`
	Username types.String `tfsdk:"username"`
	Password types.String `tfsdk:"password"`
}

// WaitModel groups attributes for readiness wait configuration.
type WaitModel struct {
	Enabled types.Bool   `tfsdk:"enabled"`
	Timeout types.String `tfsdk:"timeout"`
}

// OutputModel groups attributes for output configuration.
type OutputModel struct {
	KubeconfigPath types.String `tfsdk:"kubeconfig_path"`
}

// StatusModel groups all computed cluster status outputs.
type StatusModel struct {
	Endpoint         types.String `tfsdk:"endpoint"`
	Kubeconfig       types.String `tfsdk:"kubeconfig"`
	CACertificate    types.String `tfsdk:"ca_certificate"`
	Description      types.String `tfsdk:"description"`
	BootstrapCluster types.String `tfsdk:"bootstrap_cluster"`
}

// --- Attribute Type Maps ---
// Each nested model requires an attrTypes() function for types.ObjectValueFrom() and types.ObjectNull().

func managementAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"kubeconfig":   types.StringType,
		"skip_init":    types.BoolType,
		"self_managed": types.BoolType,
		"namespace":    types.StringType,
	}
}

func topologyAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"control_plane_count": types.Int64Type,
		"worker_count":        types.Int64Type,
	}
}

func inventoryAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"source":  types.StringType,
		"machine": types.ListType{ElemType: types.ObjectType{AttrTypes: machineAttrTypes()}},
	}
}

func machineAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"hostname": types.StringType,
		"network":  types.ObjectType{AttrTypes: networkAttrTypes()},
		"disk":     types.ObjectType{AttrTypes: diskAttrTypes()},
		"bmc":      types.ObjectType{AttrTypes: bmcAttrTypes()},
		"labels":   types.MapType{ElemType: types.StringType},
	}
}

func networkAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"ip_address":  types.StringType,
		"netmask":     types.StringType,
		"gateway":     types.StringType,
		"mac_address": types.StringType,
		"nameservers": types.ListType{ElemType: types.StringType},
		"vlan_id":     types.StringType,
	}
}

func diskAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"device": types.StringType,
	}
}

func bmcAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"address":  types.StringType,
		"username": types.StringType,
		"password": types.StringType,
	}
}

func waitAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"enabled": types.BoolType,
		"timeout": types.StringType,
	}
}

func outputAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"kubeconfig_path": types.StringType,
	}
}

func statusAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"endpoint":          types.StringType,
		"kubeconfig":        types.StringType,
		"ca_certificate":    types.StringType,
		"description":       types.StringType,
		"bootstrap_cluster": types.StringType,
	}
}

// --- Extraction Helpers ---
// Each extraction helper safely reads a nested object from the model.

func extractManagement(ctx context.Context, data *ClusterResourceModel) (*ManagementModel, diag.Diagnostics) {
	if data.Management.IsNull() || data.Management.IsUnknown() {
		return nil, nil
	}
	var mgmt ManagementModel
	diags := data.Management.As(ctx, &mgmt, basetypes.ObjectAsOptions{})
	return &mgmt, diags
}

func extractProviderMap(ctx context.Context, m types.Map) (map[string]ProviderConfigModel, diag.Diagnostics) {
	if m.IsNull() || m.IsUnknown() {
		return nil, nil
	}
	var providers map[string]ProviderConfigModel
	diags := m.ElementsAs(ctx, &providers, false)
	return providers, diags
}

func extractTopology(ctx context.Context, data *ClusterResourceModel) (*TopologyModel, diag.Diagnostics) {
	if data.Topology.IsNull() || data.Topology.IsUnknown() {
		return nil, nil
	}
	var topo TopologyModel
	diags := data.Topology.As(ctx, &topo, basetypes.ObjectAsOptions{})
	return &topo, diags
}

func extractInventory(ctx context.Context, data *ClusterResourceModel) (*InventoryModel, diag.Diagnostics) {
	if data.Inventory.IsNull() || data.Inventory.IsUnknown() {
		return nil, nil
	}
	var inv InventoryModel
	diags := data.Inventory.As(ctx, &inv, basetypes.ObjectAsOptions{})
	return &inv, diags
}

func extractWait(ctx context.Context, data *ClusterResourceModel) (*WaitModel, diag.Diagnostics) {
	if data.Wait.IsNull() || data.Wait.IsUnknown() {
		return nil, nil
	}
	var w WaitModel
	diags := data.Wait.As(ctx, &w, basetypes.ObjectAsOptions{})
	return &w, diags
}

func extractOutput(ctx context.Context, data *ClusterResourceModel) (*OutputModel, diag.Diagnostics) {
	if data.Output.IsNull() || data.Output.IsUnknown() {
		return nil, nil
	}
	var out OutputModel
	diags := data.Output.As(ctx, &out, basetypes.ObjectAsOptions{})
	return &out, diags
}

func extractStatus(ctx context.Context, data *ClusterResourceModel) (*StatusModel, diag.Diagnostics) {
	if data.Status.IsNull() || data.Status.IsUnknown() {
		return nil, nil
	}
	var st StatusModel
	diags := data.Status.As(ctx, &st, basetypes.ObjectAsOptions{})
	return &st, diags
}

// --- State Population Helpers ---

func stringOrNull(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

func setStatus(ctx context.Context, data *ClusterResourceModel, result *capi.ClusterResult) diag.Diagnostics {
	bootstrapName := ""
	if result.BootstrapCluster != nil {
		bootstrapName = result.BootstrapCluster.Name
	}

	status := StatusModel{
		Endpoint:         stringOrNull(result.Endpoint),
		Kubeconfig:       stringOrNull(result.Kubeconfig),
		CACertificate:    stringOrNull(result.CACertificate),
		Description:      stringOrNull(result.ClusterDescription),
		BootstrapCluster: stringOrNull(bootstrapName),
	}

	val, diags := types.ObjectValueFrom(ctx, statusAttrTypes(), status)
	if diags.HasError() {
		return diags
	}
	data.Status = val
	return nil
}

func setStatusWithFallback(ctx context.Context, data *ClusterResourceModel, result *capi.ClusterResult, prevStatus *StatusModel) diag.Diagnostics {
	bootstrapName := ""
	if result.BootstrapCluster != nil {
		bootstrapName = result.BootstrapCluster.Name
	} else if prevStatus != nil && !prevStatus.BootstrapCluster.IsNull() {
		bootstrapName = prevStatus.BootstrapCluster.ValueString()
	}

	endpoint := result.Endpoint
	if endpoint == "" && prevStatus != nil && !prevStatus.Endpoint.IsNull() {
		endpoint = prevStatus.Endpoint.ValueString()
	}

	kubeconfig := result.Kubeconfig
	if kubeconfig == "" && prevStatus != nil && !prevStatus.Kubeconfig.IsNull() {
		kubeconfig = prevStatus.Kubeconfig.ValueString()
	}

	caCert := result.CACertificate
	if caCert == "" && prevStatus != nil && !prevStatus.CACertificate.IsNull() {
		caCert = prevStatus.CACertificate.ValueString()
	}

	desc := result.ClusterDescription
	if desc == "" && prevStatus != nil && !prevStatus.Description.IsNull() {
		desc = prevStatus.Description.ValueString()
	}

	status := StatusModel{
		Endpoint:         stringOrNull(endpoint),
		Kubeconfig:       stringOrNull(kubeconfig),
		CACertificate:    stringOrNull(caCert),
		Description:      stringOrNull(desc),
		BootstrapCluster: stringOrNull(bootstrapName),
	}

	val, diags := types.ObjectValueFrom(ctx, statusAttrTypes(), status)
	if diags.HasError() {
		return diags
	}
	data.Status = val
	return nil
}

func nullStatus(ctx context.Context, data *ClusterResourceModel) diag.Diagnostics {
	status := StatusModel{
		Endpoint:         types.StringNull(),
		Kubeconfig:       types.StringNull(),
		CACertificate:    types.StringNull(),
		Description:      types.StringNull(),
		BootstrapCluster: types.StringNull(),
	}
	val, diags := types.ObjectValueFrom(ctx, statusAttrTypes(), status)
	if diags.HasError() {
		return diags
	}
	data.Status = val
	return nil
}

// --- Option Builders ---

func buildCreateOptions(ctx context.Context, data *ClusterResourceModel) (*capi.CreateClusterOptions, diag.Diagnostics) {
	var diags diag.Diagnostics
	opts := &capi.CreateClusterOptions{
		Name: data.Name.ValueString(),
		Wait: capi.DefaultWaitOptions(),
	}

	if !data.KubernetesVersion.IsNull() {
		opts.KubernetesVersion = data.KubernetesVersion.ValueString()
	}
	if !data.Flavor.IsNull() {
		opts.Flavor = data.Flavor.ValueString()
	}

	// Management
	mgmt, d := extractManagement(ctx, data)
	diags.Append(d...)
	if mgmt != nil {
		if !mgmt.Kubeconfig.IsNull() {
			opts.ManagementKubeconfig = mgmt.Kubeconfig.ValueString()
		}
		opts.SkipInit = mgmt.SkipInit.ValueBool()
		opts.SelfManaged = mgmt.SelfManaged.ValueBool()
		if !mgmt.Namespace.IsNull() {
			opts.Namespace = mgmt.Namespace.ValueString()
		}
	}

	// Infrastructure providers (map)
	infraMap, d := extractProviderMap(ctx, data.Infrastructure)
	diags.Append(d...)
	if len(infraMap) > 0 {
		opts.InfrastructureProviders = make(map[string]*capi.ProviderConfig)
		for name, model := range infraMap {
			pc, d := providerConfigModelToAPI(ctx, model)
			diags.Append(d...)
			opts.InfrastructureProviders[name] = pc
			// Set legacy field for template generation (uses first infra provider)
			if opts.InfrastructureProvider == "" {
				opts.InfrastructureProvider = capi.ProviderString(name, pc)
			}
		}
	}

	// Bootstrap providers (map)
	bsMap, d := extractProviderMap(ctx, data.Bootstrap)
	diags.Append(d...)
	if len(bsMap) > 0 {
		opts.BootstrapProviders = make(map[string]*capi.ProviderConfig)
		for name, model := range bsMap {
			pc, d := providerConfigModelToAPI(ctx, model)
			diags.Append(d...)
			opts.BootstrapProviders[name] = pc
			if opts.BootstrapProvider == "" {
				opts.BootstrapProvider = capi.ProviderString(name, pc)
			}
		}
	}

	// Control plane providers (map)
	cpMap, d := extractProviderMap(ctx, data.ControlPlane)
	diags.Append(d...)
	if len(cpMap) > 0 {
		opts.ControlPlaneProviders = make(map[string]*capi.ProviderConfig)
		for name, model := range cpMap {
			pc, d := providerConfigModelToAPI(ctx, model)
			diags.Append(d...)
			opts.ControlPlaneProviders[name] = pc
			if opts.ControlPlaneProvider == "" {
				opts.ControlPlaneProvider = capi.ProviderString(name, pc)
			}
		}
	}

	// Core providers (map)
	coreMap, d := extractProviderMap(ctx, data.Core)
	diags.Append(d...)
	if len(coreMap) > 0 {
		opts.CoreProviders = make(map[string]*capi.ProviderConfig)
		for name, model := range coreMap {
			pc, d := providerConfigModelToAPI(ctx, model)
			diags.Append(d...)
			opts.CoreProviders[name] = pc
			if opts.CoreProvider == "" {
				opts.CoreProvider = capi.ProviderString(name, pc)
			}
		}
	}

	// Addon providers (map)
	addonMap, d := extractProviderMap(ctx, data.Addon)
	diags.Append(d...)
	if len(addonMap) > 0 {
		opts.AddonProviders = make(map[string]*capi.ProviderConfig)
		for name, model := range addonMap {
			pc, d := providerConfigModelToAPI(ctx, model)
			diags.Append(d...)
			opts.AddonProviders[name] = pc
		}
	}

	// Topology
	topo, d := extractTopology(ctx, data)
	diags.Append(d...)
	if topo != nil {
		if !topo.ControlPlaneCount.IsNull() {
			count := topo.ControlPlaneCount.ValueInt64()
			opts.ControlPlaneMachineCount = &count
		}
		if !topo.WorkerCount.IsNull() {
			count := topo.WorkerCount.ValueInt64()
			opts.WorkerMachineCount = &count
		}
	}

	// Wait
	wait, d := extractWait(ctx, data)
	diags.Append(d...)
	if wait != nil {
		opts.WaitForReady = wait.Enabled.ValueBool()
		if !wait.Timeout.IsNull() && wait.Timeout.ValueString() != "" {
			timeout, err := time.ParseDuration(wait.Timeout.ValueString())
			if err != nil {
				diags.AddError("Invalid wait timeout", fmt.Sprintf("Cannot parse timeout %q: %s", wait.Timeout.ValueString(), err))
			} else {
				opts.Wait.Timeout = timeout
			}
		}
	} else {
		opts.WaitForReady = true
	}

	// Output
	out, d := extractOutput(ctx, data)
	diags.Append(d...)
	if out != nil && !out.KubeconfigPath.IsNull() {
		opts.KubeconfigOutputPath = out.KubeconfigPath.ValueString()
	}

	return opts, diags
}

// providerConfigModelToAPI converts a ProviderConfigModel (Terraform model)
// to a capi.ProviderConfig (API struct).
func providerConfigModelToAPI(ctx context.Context, model ProviderConfigModel) (*capi.ProviderConfig, diag.Diagnostics) {
	var diags diag.Diagnostics
	pc := &capi.ProviderConfig{}

	if !model.Version.IsNull() {
		pc.Version = model.Version.ValueString()
	}
	if !model.Namespace.IsNull() {
		pc.Namespace = model.Namespace.ValueString()
	}

	// ConfigVariables
	if !model.ConfigVariables.IsNull() && !model.ConfigVariables.IsUnknown() {
		vars := map[string]string{}
		diags.Append(model.ConfigVariables.ElementsAs(ctx, &vars, false)...)
		pc.ConfigVariables = vars
	}

	// SecretConfigVariables
	if !model.SecretConfigVariables.IsNull() && !model.SecretConfigVariables.IsUnknown() {
		vars := map[string]string{}
		diags.Append(model.SecretConfigVariables.ElementsAs(ctx, &vars, false)...)
		pc.SecretConfigVariables = vars
	}

	// FetchConfig
	if !model.FetchConfig.IsNull() && !model.FetchConfig.IsUnknown() {
		var fc FetchConfigModel
		diags.Append(model.FetchConfig.As(ctx, &fc, basetypes.ObjectAsOptions{})...)
		pc.FetchConfig = &capi.FetchConfig{}
		if !fc.URL.IsNull() {
			pc.FetchConfig.URL = fc.URL.ValueString()
		}
		if !fc.OCI.IsNull() {
			pc.FetchConfig.OCI = fc.OCI.ValueString()
		}
	}

	// Deployment
	if !model.Deployment.IsNull() && !model.Deployment.IsUnknown() {
		var dep DeploymentModel
		diags.Append(model.Deployment.As(ctx, &dep, basetypes.ObjectAsOptions{})...)
		pc.Deployment = &capi.DeploymentConfig{}
		if !dep.Replicas.IsNull() {
			r := dep.Replicas.ValueInt64()
			pc.Deployment.Replicas = &r
		}
		if !dep.NodeSelector.IsNull() {
			ns := map[string]string{}
			diags.Append(dep.NodeSelector.ElementsAs(ctx, &ns, false)...)
			pc.Deployment.NodeSelector = ns
		}
		if !dep.ServiceAccountName.IsNull() {
			pc.Deployment.ServiceAccountName = dep.ServiceAccountName.ValueString()
		}
		if !dep.Containers.IsNull() && !dep.Containers.IsUnknown() {
			var containers []ContainerModel
			diags.Append(dep.Containers.ElementsAs(ctx, &containers, false)...)
			for _, c := range containers {
				cc := capi.ContainerConfig{Name: c.Name.ValueString()}
				if !c.ImageURL.IsNull() {
					cc.ImageURL = c.ImageURL.ValueString()
				}
				if !c.Args.IsNull() {
					args := map[string]string{}
					diags.Append(c.Args.ElementsAs(ctx, &args, false)...)
					cc.Args = args
				}
				if !c.Command.IsNull() {
					var cmd []string
					diags.Append(c.Command.ElementsAs(ctx, &cmd, false)...)
					cc.Command = cmd
				}
				pc.Deployment.Containers = append(pc.Deployment.Containers, cc)
			}
		}
	}

	// Manager
	if !model.Manager.IsNull() && !model.Manager.IsUnknown() {
		var mgr ManagerModel
		diags.Append(model.Manager.As(ctx, &mgr, basetypes.ObjectAsOptions{})...)
		pc.Manager = &capi.ManagerConfig{}
		if !mgr.ProfilerAddress.IsNull() {
			pc.Manager.ProfilerAddress = mgr.ProfilerAddress.ValueString()
		}
		if !mgr.MaxConcurrentReconciles.IsNull() {
			v := mgr.MaxConcurrentReconciles.ValueInt64()
			pc.Manager.MaxConcurrentReconciles = &v
		}
		if !mgr.Verbosity.IsNull() {
			v := mgr.Verbosity.ValueInt64()
			pc.Manager.Verbosity = &v
		}
		if !mgr.FeatureGates.IsNull() {
			gates := map[string]bool{}
			diags.Append(mgr.FeatureGates.ElementsAs(ctx, &gates, false)...)
			pc.Manager.FeatureGates = gates
		}
		if !mgr.AdditionalArgs.IsNull() {
			args := map[string]string{}
			diags.Append(mgr.AdditionalArgs.ElementsAs(ctx, &args, false)...)
			pc.Manager.AdditionalArgs = args
		}
	}

	// AdditionalManifests
	if !model.AdditionalManifests.IsNull() && !model.AdditionalManifests.IsUnknown() {
		pc.AdditionalManifests = model.AdditionalManifests.ValueString()
	}

	// ManifestPatches
	if !model.ManifestPatches.IsNull() && !model.ManifestPatches.IsUnknown() {
		var patches []string
		diags.Append(model.ManifestPatches.ElementsAs(ctx, &patches, false)...)
		pc.ManifestPatches = patches
	}

	// Patches
	if !model.Patches.IsNull() && !model.Patches.IsUnknown() {
		var patchModels []PatchModel
		diags.Append(model.Patches.ElementsAs(ctx, &patchModels, false)...)
		for _, pm := range patchModels {
			patchCfg := capi.PatchConfig{}
			if !pm.Patch.IsNull() {
				patchCfg.Patch = pm.Patch.ValueString()
			}
			if !pm.Target.IsNull() && !pm.Target.IsUnknown() {
				var sel PatchSelectorModel
				diags.Append(pm.Target.As(ctx, &sel, basetypes.ObjectAsOptions{})...)
				patchCfg.Target = &capi.PatchSelector{}
				if !sel.Group.IsNull() {
					patchCfg.Target.Group = sel.Group.ValueString()
				}
				if !sel.Version.IsNull() {
					patchCfg.Target.Version = sel.Version.ValueString()
				}
				if !sel.Kind.IsNull() {
					patchCfg.Target.Kind = sel.Kind.ValueString()
				}
				if !sel.Name.IsNull() {
					patchCfg.Target.Name = sel.Name.ValueString()
				}
				if !sel.Namespace.IsNull() {
					patchCfg.Target.Namespace = sel.Namespace.ValueString()
				}
				if !sel.LabelSelector.IsNull() {
					patchCfg.Target.LabelSelector = sel.LabelSelector.ValueString()
				}
			}
			pc.Patches = append(pc.Patches, patchCfg)
		}
	}

	return pc, diags
}

// --- Inventory Validation ---

func validateInventory(ctx context.Context, inv *InventoryModel, cpCount, workerCount int64, diags *diag.Diagnostics) {
	if inv == nil {
		return
	}

	hasSource := !inv.Source.IsNull() && inv.Source.ValueString() != ""
	hasMachines := !inv.Machine.IsNull() && !inv.Machine.IsUnknown()
	if hasSource && hasMachines {
		diags.AddError("Invalid inventory", "Specify either source or machine, not both.")
		return
	}

	if !hasMachines {
		return
	}

	var machines []MachineModel
	diags.Append(inv.Machine.ElementsAs(ctx, &machines, false)...)
	if diags.HasError() {
		return
	}

	hostnames := map[string]bool{}
	ips := map[string]bool{}
	macs := map[string]bool{}

	for _, m := range machines {
		h := m.Hostname.ValueString()
		if hostnames[h] {
			diags.AddError("Duplicate hostname", fmt.Sprintf("hostname %q appears more than once", h))
		}
		hostnames[h] = true

		var net NetworkModel
		diags.Append(m.Network.As(ctx, &net, basetypes.ObjectAsOptions{})...)
		if diags.HasError() {
			return
		}

		ip := net.IPAddress.ValueString()
		if ips[ip] {
			diags.AddError("Duplicate IP", fmt.Sprintf("ip_address %q appears more than once", ip))
		}
		ips[ip] = true

		mac := net.MACAddress.ValueString()
		if macs[mac] {
			diags.AddError("Duplicate MAC", fmt.Sprintf("mac_address %q appears more than once", mac))
		}
		macs[mac] = true
	}

	// Role counting
	cpMachines := 0
	workerMachines := 0
	for _, m := range machines {
		labels := map[string]string{}
		if !m.Labels.IsNull() {
			diags.Append(m.Labels.ElementsAs(ctx, &labels, false)...)
		}
		switch labels["type"] {
		case "cp":
			cpMachines++
		default:
			workerMachines++
		}
	}
	if int64(cpMachines) < cpCount {
		diags.AddError("Insufficient hardware",
			fmt.Sprintf("Need %d control plane machines (type=cp label), have %d", cpCount, cpMachines))
	}
	if int64(workerMachines) < workerCount {
		diags.AddError("Insufficient hardware",
			fmt.Sprintf("Need %d worker machines, have %d", workerCount, workerMachines))
	}
}

// --- Sub-model types for provider config deserialization ---
// These map 1:1 with the nested attributes in ProviderConfigModel.

// FetchConfigModel maps to FetchConfiguration (oci or url).
type FetchConfigModel struct {
	URL types.String `tfsdk:"url"`
	OCI types.String `tfsdk:"oci"`
}

// DeploymentModel maps to DeploymentSpec.
type DeploymentModel struct {
	Replicas           types.Int64  `tfsdk:"replicas"`
	NodeSelector       types.Map    `tfsdk:"node_selector"`
	ServiceAccountName types.String `tfsdk:"service_account_name"`
	Containers         types.List   `tfsdk:"containers"`
}

// ContainerModel maps to ContainerSpec.
type ContainerModel struct {
	Name     types.String `tfsdk:"name"`
	ImageURL types.String `tfsdk:"image_url"`
	Args     types.Map    `tfsdk:"args"`
	Command  types.List   `tfsdk:"command"`
}

// ManagerModel maps to ManagerSpec.
type ManagerModel struct {
	ProfilerAddress         types.String `tfsdk:"profiler_address"`
	MaxConcurrentReconciles types.Int64  `tfsdk:"max_concurrent_reconciles"`
	Verbosity               types.Int64  `tfsdk:"verbosity"`
	FeatureGates            types.Map    `tfsdk:"feature_gates"`
	AdditionalArgs          types.Map    `tfsdk:"additional_args"`
}

// PatchModel maps to Patch.
type PatchModel struct {
	Patch  types.String `tfsdk:"patch"`
	Target types.Object `tfsdk:"target"`
}

// PatchSelectorModel maps to PatchSelector.
type PatchSelectorModel struct {
	Group         types.String `tfsdk:"group"`
	Version       types.String `tfsdk:"version"`
	Kind          types.String `tfsdk:"kind"`
	Name          types.String `tfsdk:"name"`
	Namespace     types.String `tfsdk:"namespace"`
	LabelSelector types.String `tfsdk:"label_selector"`
}
