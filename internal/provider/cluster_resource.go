// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/tinkerbell-community/terraform-provider-capi/internal/capi"
)

// Ensure provider defined types fully satisfy framework interfaces.
var _ resource.Resource = &ClusterResource{}
var _ resource.ResourceWithImportState = &ClusterResource{}
var _ resource.ResourceWithUpgradeState = &ClusterResource{}

func NewClusterResource() resource.Resource {
	return &ClusterResource{}
}

// ClusterResource defines the resource implementation.
type ClusterResource struct {
	providerData *CapiProviderModel
	manager      *capi.Manager
}

func (r *ClusterResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cluster"
}

func (r *ClusterResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	providerConfigNested := schema.NestedAttributeObject{
		CustomType: NewProviderConfigType(),
		Attributes: providerConfigSchemaAttributes(),
	}

	resp.Schema = schema.Schema{
		Version:             2,
		MarkdownDescription: "Manages a Cluster API cluster using the CAPI management workflow (bootstrap -> init -> apply -> wait -> move).",

		Attributes: map[string]schema.Attribute{
			// --- Top-level identity attributes ---
			"name": schema.StringAttribute{
				MarkdownDescription: "The name of the cluster. Must be a valid DNS-1123 subdomain.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"kubernetes_version": schema.StringAttribute{
				MarkdownDescription: "Kubernetes version for the workload cluster (e.g., `v1.31.0`).",
				Optional:            true,
			},
			"flavor": schema.StringAttribute{
				MarkdownDescription: "Cluster template flavor to use. Maps to clusterctl template flavors.",
				Optional:            true,
			},
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Cluster identifier.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			// --- management ---
			"management": schema.SingleNestedAttribute{
				MarkdownDescription: "Management cluster configuration. Controls how the CAPI lifecycle is managed.",
				Optional:            true,
				Computed:            true,
				Attributes: map[string]schema.Attribute{
					"kubeconfig": schema.StringAttribute{
						MarkdownDescription: "Path to the kubeconfig for an existing management cluster. If not provided, a bootstrap cluster (kind) is created automatically.",
						Optional:            true,
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
					"skip_init": schema.BoolAttribute{
						MarkdownDescription: "Skip running clusterctl init on the management cluster. Use when CAPI providers are already installed.",
						Optional:            true,
						Computed:            true,
						Default:             booldefault.StaticBool(false),
					},
					"self_managed": schema.BoolAttribute{
						MarkdownDescription: "Pivot CAPI management from bootstrap to workload cluster (clusterctl move). Required `true` for Tinkerbell provider.",
						Optional:            true,
						Computed:            true,
						Default:             booldefault.StaticBool(false),
						PlanModifiers: []planmodifier.Bool{
							boolplanmodifier.RequiresReplace(),
						},
					},
					"namespace": schema.StringAttribute{
						MarkdownDescription: "Namespace on the management cluster where CAPI resources are created.",
						Optional:            true,
						Computed:            true,
						PlanModifiers: []planmodifier.String{
							stringplanmodifier.RequiresReplace(),
						},
					},
				},
			},

			// --- infrastructure (map of provider configs, keyed by provider name) ---
			"infrastructure": schema.MapNestedAttribute{
				MarkdownDescription: "Infrastructure provider configurations, keyed by provider name (e.g., `docker`, `tinkerbell`). Maps to the capi-operator Helm chart infrastructure values.",
				Required:            true,
				NestedObject:        providerConfigNested,
			},

			// --- bootstrap (map of provider configs) ---
			"bootstrap": schema.MapNestedAttribute{
				MarkdownDescription: "Bootstrap provider configurations, keyed by provider name (e.g., `kubeadm`, `talos`).",
				Optional:            true,
				NestedObject:        providerConfigNested,
			},

			// --- control_plane (map of provider configs) ---
			"control_plane": schema.MapNestedAttribute{
				MarkdownDescription: "Control plane provider configurations, keyed by provider name (e.g., `kubeadm`, `talos`).",
				Optional:            true,
				NestedObject:        providerConfigNested,
			},

			// --- core (map of provider configs) ---
			"core": schema.MapNestedAttribute{
				MarkdownDescription: "Core CAPI provider configurations, keyed by provider name (e.g., `cluster-api`).",
				Optional:            true,
				NestedObject:        providerConfigNested,
			},

			// --- addon (map of provider configs) ---
			"addon": schema.MapNestedAttribute{
				MarkdownDescription: "Addon provider configurations, keyed by provider name (e.g., `helm`). Customizations (deployment, manager, patches) are applied natively by wrapping the clusterctl client's repository factory — the capi-operator itself is not required.",
				Optional:            true,
				NestedObject:        providerConfigNested,
			},

			// --- topology (machine counts, separate from provider config) ---
			"topology": schema.SingleNestedAttribute{
				MarkdownDescription: "Cluster topology configuration. Controls the number of control plane and worker machines.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"control_plane_count": schema.Int64Attribute{
						MarkdownDescription: "Number of control plane machines. Must be an odd number for HA (1, 3, 5).",
						Optional:            true,
					},
					"worker_count": schema.Int64Attribute{
						MarkdownDescription: "Number of worker machines.",
						Optional:            true,
					},
				},
			},

			// --- inventory ---
			"inventory": schema.SingleNestedAttribute{
				MarkdownDescription: "Hardware inventory for bare-metal provisioning.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"source": schema.StringAttribute{
						MarkdownDescription: "Path to a hardware inventory file (CSV or YAML).",
						Optional:            true,
					},
					"machine": schema.ListNestedAttribute{
						MarkdownDescription: "Inline machine definitions.",
						Optional:            true,
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"hostname": schema.StringAttribute{
									MarkdownDescription: "Machine hostname. Must be unique.",
									Required:            true,
								},
								"network": schema.SingleNestedAttribute{
									MarkdownDescription: "Network configuration.",
									Required:            true,
									Attributes: map[string]schema.Attribute{
										"ip_address":  schema.StringAttribute{Required: true, MarkdownDescription: "Primary IP address."},
										"netmask":     schema.StringAttribute{Required: true, MarkdownDescription: "Network mask."},
										"gateway":     schema.StringAttribute{Required: true, MarkdownDescription: "Default gateway."},
										"mac_address": schema.StringAttribute{Required: true, MarkdownDescription: "Primary NIC MAC address."},
										"nameservers": schema.ListAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: "DNS nameservers."},
										"vlan_id":     schema.StringAttribute{Optional: true, MarkdownDescription: "VLAN ID."},
									},
								},
								"disk": schema.SingleNestedAttribute{
									MarkdownDescription: "Boot disk configuration.",
									Optional:            true,
									Attributes: map[string]schema.Attribute{
										"device": schema.StringAttribute{Required: true, MarkdownDescription: "Disk device path."},
									},
								},
								"bmc": schema.SingleNestedAttribute{
									MarkdownDescription: "BMC configuration.",
									Optional:            true,
									Attributes: map[string]schema.Attribute{
										"address":  schema.StringAttribute{Required: true, MarkdownDescription: "BMC endpoint."},
										"username": schema.StringAttribute{Required: true, MarkdownDescription: "BMC username."},
										"password": schema.StringAttribute{Required: true, Sensitive: true, MarkdownDescription: "BMC password."},
									},
								},
								"labels": schema.MapAttribute{
									Optional:            true,
									ElementType:         types.StringType,
									MarkdownDescription: "Labels. Use `type=cp` for control plane, `type=worker` for workers.",
								},
							},
						},
					},
				},
			},

			// --- wait ---
			"wait": schema.SingleNestedAttribute{
				MarkdownDescription: "Readiness wait configuration.",
				Optional:            true,
				Computed:            true,
				Attributes: map[string]schema.Attribute{
					"enabled": schema.BoolAttribute{
						MarkdownDescription: "Wait for readiness.",
						Optional:            true,
						Computed:            true,
						Default:             booldefault.StaticBool(true),
					},
					"timeout": schema.StringAttribute{
						MarkdownDescription: "Max wait time (Go duration, e.g., `30m`). Default: `30m`.",
						Optional:            true,
						Computed:            true,
					},
				},
			},

			// --- output ---
			"output": schema.SingleNestedAttribute{
				MarkdownDescription: "Output configuration.",
				Optional:            true,
				Computed:            true,
				Attributes: map[string]schema.Attribute{
					"kubeconfig_path": schema.StringAttribute{
						MarkdownDescription: "File path for the workload cluster kubeconfig.",
						Optional:            true,
						Computed:            true,
					},
				},
			},

			// --- status ---
			"status": schema.SingleNestedAttribute{
				MarkdownDescription: "Computed cluster status.",
				Computed:            true,
				Attributes: map[string]schema.Attribute{
					"endpoint":          schema.StringAttribute{Computed: true, MarkdownDescription: "API server endpoint."},
					"kubeconfig":        schema.StringAttribute{Computed: true, Sensitive: true, MarkdownDescription: "Kubeconfig content."},
					"ca_certificate":    schema.StringAttribute{Computed: true, Sensitive: true, MarkdownDescription: "CA certificate (PEM)."},
					"description":       schema.StringAttribute{Computed: true, MarkdownDescription: "Cluster description."},
					"bootstrap_cluster": schema.StringAttribute{Computed: true, MarkdownDescription: "Bootstrap cluster name."},
				},
			},
		},
	}
}

func (r *ClusterResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	providerData, ok := req.ProviderData.(*CapiProviderModel)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *CapiProviderModel, got: %T.", req.ProviderData),
		)
		return
	}

	r.providerData = providerData
	r.manager = capi.NewManager(
		capi.WithLogger(log.New(os.Stderr, "[capi-tf] ", log.LstdFlags)),
	)
}

func (r *ClusterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data ClusterResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	r.validateLifecycleConfig(ctx, &data, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Info(ctx, "Creating CAPI cluster", map[string]interface{}{
		"name": data.Name.ValueString(),
	})

	createOpts, diags := buildCreateOptions(ctx, &data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Default kubeconfig output path
	if createOpts.KubeconfigOutputPath == "" {
		if home, err := os.UserHomeDir(); err == nil {
			createOpts.KubeconfigOutputPath = filepath.Join(home, ".kube", fmt.Sprintf("%s.kubeconfig", data.Name.ValueString()))
		}
	}

	result, err := r.manager.CreateCluster(ctx, *createOpts)
	if err != nil {
		resp.Diagnostics.AddError("Cluster Creation Error", fmt.Sprintf("Failed to create cluster: %s", err))
		return
	}

	data.Id = types.StringValue(data.Name.ValueString())
	r.ensureManagementComputed(ctx, &data, createOpts)
	r.ensureOutputComputed(ctx, &data, createOpts.KubeconfigOutputPath)
	r.ensureWaitComputed(ctx, &data)

	resp.Diagnostics.Append(setStatus(ctx, &data, result)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *ClusterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data ClusterResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	mgmtKubeconfig := r.resolveManagementKubeconfig(ctx, &data, nil)
	if mgmtKubeconfig == "" {
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		return
	}

	namespace := "default"
	mgmt, _ := extractManagement(ctx, &data)
	if mgmt != nil && !mgmt.Namespace.IsNull() {
		namespace = mgmt.Namespace.ValueString()
	}

	result, err := r.manager.GetClusterInfo(ctx, mgmtKubeconfig, data.Name.ValueString(), namespace)
	if err != nil {
		tflog.Warn(ctx, "Unable to read cluster info", map[string]interface{}{"error": err.Error()})
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		return
	}

	prevStatus, _ := extractStatus(ctx, &data)
	resp.Diagnostics.Append(setStatusWithFallback(ctx, &data, result, prevStatus)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *ClusterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state ClusterResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	r.validateLifecycleConfig(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	managementKubeconfig := r.resolveManagementKubeconfig(ctx, &plan, &state)
	if managementKubeconfig == "" {
		resp.Diagnostics.AddError("Cluster Update Error", "Unable to determine management kubeconfig.")
		return
	}

	namespace := "default"
	mgmt, _ := extractManagement(ctx, &plan)
	if mgmt != nil && !mgmt.Namespace.IsNull() && mgmt.Namespace.ValueString() != "" {
		namespace = mgmt.Namespace.ValueString()
	}

	reconcileOpts, diags := buildCreateOptions(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	reconcileOpts.ManagementKubeconfig = managementKubeconfig
	reconcileOpts.Namespace = namespace
	reconcileOpts.SkipInit = true
	reconcileOpts.SelfManaged = false

	result, err := r.manager.CreateCluster(ctx, *reconcileOpts)
	if err != nil {
		resp.Diagnostics.AddError("Cluster Update Error", fmt.Sprintf("Failed to reconcile cluster: %s", err))
		return
	}

	plan.Id = types.StringValue(plan.Name.ValueString())
	r.ensureManagementComputed(ctx, &plan, reconcileOpts)
	r.ensureOutputComputed(ctx, &plan, reconcileOpts.KubeconfigOutputPath)
	r.ensureWaitComputed(ctx, &plan)

	prevStatus, _ := extractStatus(ctx, &state)
	resp.Diagnostics.Append(setStatusWithFallback(ctx, &plan, result, prevStatus)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ClusterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data ClusterResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	mgmtKubeconfig := r.resolveManagementKubeconfig(ctx, &data, nil)

	namespace := "default"
	mgmt, _ := extractManagement(ctx, &data)
	if mgmt != nil && !mgmt.Namespace.IsNull() {
		namespace = mgmt.Namespace.ValueString()
	}

	deleteOpts := capi.DeleteClusterOptions{
		Name:                 data.Name.ValueString(),
		Namespace:            namespace,
		ManagementKubeconfig: mgmtKubeconfig,
	}

	status, _ := extractStatus(ctx, &data)
	if status != nil && !status.BootstrapCluster.IsNull() {
		deleteOpts.DeleteBootstrap = true
		deleteOpts.BootstrapName = status.BootstrapCluster.ValueString()
	}

	if mgmtKubeconfig != "" {
		if err := r.manager.DeleteCluster(ctx, deleteOpts); err != nil {
			resp.Diagnostics.AddWarning("Cluster Deletion Warning",
				fmt.Sprintf("Error deleting cluster (may already be removed): %s", err))
		}
	} else {
		tflog.Warn(ctx, "No management kubeconfig for deletion - manual cleanup may be needed")
	}
}

func (r *ClusterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// UpgradeState migrates v0 (flat) and v1 (nested-single) state to v2 (map-based providers + topology).
func (r *ClusterResource) UpgradeState(ctx context.Context) map[int64]resource.StateUpgrader {
	v0Schema := clusterResourceSchemaV0()
	v1Schema := clusterResourceSchemaV1()
	return map[int64]resource.StateUpgrader{
		0: {
			PriorSchema: &v0Schema,
			StateUpgrader: func(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
				var v0 clusterResourceModelV0
				resp.Diagnostics.Append(req.State.Get(ctx, &v0)...)
				if resp.Diagnostics.HasError() {
					return
				}

				// First migrate v0→v1 in-memory, then v1→v2.
				v1 := migrateV0ToV1(ctx, v0, &resp.Diagnostics)
				if resp.Diagnostics.HasError() {
					return
				}

				v2 := migrateV1ToV2(ctx, v1, &resp.Diagnostics)
				if resp.Diagnostics.HasError() {
					return
				}

				resp.Diagnostics.Append(resp.State.Set(ctx, v2)...)
			},
		},
		1: {
			PriorSchema: &v1Schema,
			StateUpgrader: func(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
				var v1 clusterResourceModelV1
				resp.Diagnostics.Append(req.State.Get(ctx, &v1)...)
				if resp.Diagnostics.HasError() {
					return
				}

				v2 := migrateV1ToV2(ctx, v1, &resp.Diagnostics)
				if resp.Diagnostics.HasError() {
					return
				}

				resp.Diagnostics.Append(resp.State.Set(ctx, v2)...)
			},
		},
	}
}

// --- Validation ---

func (r *ClusterResource) validateLifecycleConfig(ctx context.Context, data *ClusterResourceModel, diags *diag.Diagnostics) {
	infraProviders, d := extractProviderMap(ctx, data.Infrastructure)
	diags.Append(d...)
	if diags.HasError() || len(infraProviders) == 0 {
		return
	}

	supportedProviders := map[string]struct{}{
		"aws": {}, "azure": {}, "docker": {}, "openstack": {}, "tinkerbell": {}, "vsphere": {},
	}

	for providerName := range infraProviders {
		name := strings.ToLower(providerName)
		if _, ok := supportedProviders[name]; !ok {
			diags.AddError(
				"Unsupported infrastructure provider",
				fmt.Sprintf("infrastructure provider %q is not supported. Supported: aws, azure, docker, openstack, tinkerbell, vsphere", providerName),
			)
		}
	}

	// Tinkerbell-specific validation
	if _, hasTinkerbell := infraProviders["tinkerbell"]; hasTinkerbell {
		mgmt, d := extractManagement(ctx, data)
		diags.Append(d...)
		if mgmt == nil || mgmt.SelfManaged.IsNull() || !mgmt.SelfManaged.ValueBool() {
			diags.AddError("Invalid Tinkerbell configuration", "Tinkerbell clusters must have management.self_managed = true.")
		}

		bsProviders, d := extractProviderMap(ctx, data.Bootstrap)
		diags.Append(d...)
		for bsName := range bsProviders {
			if n := strings.ToLower(bsName); n != "kubeadm" && n != "talos" {
				diags.AddError("Invalid bootstrap provider for Tinkerbell",
					fmt.Sprintf("Tinkerbell supports bootstrap providers \"kubeadm\" or \"talos\", got %q.", bsName))
			}
		}

		cpProviders, d := extractProviderMap(ctx, data.ControlPlane)
		diags.Append(d...)
		for cpName := range cpProviders {
			if n := strings.ToLower(cpName); n != "kubeadm" && n != "talos" {
				diags.AddError("Invalid control plane provider for Tinkerbell",
					fmt.Sprintf("Tinkerbell supports control plane providers \"kubeadm\" or \"talos\", got %q.", cpName))
			}
		}
	}

	// Validate inventory
	inv, d := extractInventory(ctx, data)
	diags.Append(d...)
	if inv != nil {
		var cpCount, workerCount int64
		topo, d := extractTopology(ctx, data)
		diags.Append(d...)
		if topo != nil {
			if !topo.ControlPlaneCount.IsNull() {
				cpCount = topo.ControlPlaneCount.ValueInt64()
			}
			if !topo.WorkerCount.IsNull() {
				workerCount = topo.WorkerCount.ValueInt64()
			}
		}
		validateInventory(ctx, inv, cpCount, workerCount, diags)
	}
}

func (r *ClusterResource) resolveManagementKubeconfig(ctx context.Context, plan *ClusterResourceModel, state *ClusterResourceModel) string {
	mgmt, _ := extractManagement(ctx, plan)
	if mgmt != nil && !mgmt.Kubeconfig.IsNull() && mgmt.Kubeconfig.ValueString() != "" {
		return mgmt.Kubeconfig.ValueString()
	}

	if state != nil {
		stateMgmt, _ := extractManagement(ctx, state)
		if stateMgmt != nil && !stateMgmt.Kubeconfig.IsNull() && stateMgmt.Kubeconfig.ValueString() != "" {
			return stateMgmt.Kubeconfig.ValueString()
		}
	}

	if mgmt != nil && !mgmt.SelfManaged.IsNull() && mgmt.SelfManaged.ValueBool() {
		out, _ := extractOutput(ctx, plan)
		if out != nil && !out.KubeconfigPath.IsNull() && out.KubeconfigPath.ValueString() != "" {
			return out.KubeconfigPath.ValueString()
		}
		if state != nil {
			stateOut, _ := extractOutput(ctx, state)
			if stateOut != nil && !stateOut.KubeconfigPath.IsNull() && stateOut.KubeconfigPath.ValueString() != "" {
				return stateOut.KubeconfigPath.ValueString()
			}
		}
	}

	status, _ := extractStatus(ctx, plan)
	if status != nil && !status.BootstrapCluster.IsNull() {
		return filepath.Join(os.TempDir(), fmt.Sprintf("kind-%s-kubeconfig", status.BootstrapCluster.ValueString()))
	}
	if state != nil {
		stateStatus, _ := extractStatus(ctx, state)
		if stateStatus != nil && !stateStatus.BootstrapCluster.IsNull() {
			return filepath.Join(os.TempDir(), fmt.Sprintf("kind-%s-kubeconfig", stateStatus.BootstrapCluster.ValueString()))
		}
	}

	return ""
}

// --- Computed Field Helpers ---

func (r *ClusterResource) ensureManagementComputed(ctx context.Context, data *ClusterResourceModel, opts *capi.CreateClusterOptions) {
	mgmt, _ := extractManagement(ctx, data)

	namespace := "default"
	if opts.Namespace != "" {
		namespace = opts.Namespace
	}

	if mgmt == nil {
		mgmt = &ManagementModel{
			Kubeconfig:  types.StringNull(),
			SkipInit:    types.BoolValue(opts.SkipInit),
			SelfManaged: types.BoolValue(opts.SelfManaged),
			Namespace:   types.StringValue(namespace),
		}
	} else {
		if mgmt.Namespace.IsNull() || mgmt.Namespace.ValueString() == "" {
			mgmt.Namespace = types.StringValue(namespace)
		}
	}

	val, _ := types.ObjectValueFrom(ctx, managementAttrTypes(), mgmt)
	data.Management = val
}

func (r *ClusterResource) ensureOutputComputed(ctx context.Context, data *ClusterResourceModel, kubeconfigPath string) {
	out, _ := extractOutput(ctx, data)
	if out == nil {
		out = &OutputModel{KubeconfigPath: types.StringValue(kubeconfigPath)}
	} else if out.KubeconfigPath.IsNull() || out.KubeconfigPath.ValueString() == "" {
		out.KubeconfigPath = types.StringValue(kubeconfigPath)
	}

	val, _ := types.ObjectValueFrom(ctx, outputAttrTypes(), out)
	data.Output = val
}

func (r *ClusterResource) ensureWaitComputed(ctx context.Context, data *ClusterResourceModel) {
	wait, _ := extractWait(ctx, data)
	if wait == nil {
		wait = &WaitModel{Enabled: types.BoolValue(true), Timeout: types.StringValue("30m")}
	}
	if wait.Timeout.IsNull() || wait.Timeout.ValueString() == "" {
		wait.Timeout = types.StringValue("30m")
	}

	val, _ := types.ObjectValueFrom(ctx, waitAttrTypes(), wait)
	data.Wait = val
}

// --- v0 Schema (flat) for state migration ---

type clusterResourceModelV0 struct {
	Name                     types.String `tfsdk:"name"`
	KubeconfigPath           types.String `tfsdk:"kubeconfig_path"`
	ManagementKubeconfig     types.String `tfsdk:"management_kubeconfig"`
	SkipInit                 types.Bool   `tfsdk:"skip_init"`
	WaitForReady             types.Bool   `tfsdk:"wait_for_ready"`
	SelfManaged              types.Bool   `tfsdk:"self_managed"`
	InfrastructureProvider   types.String `tfsdk:"infrastructure_provider"`
	BootstrapProvider        types.String `tfsdk:"bootstrap_provider"`
	ControlPlaneProvider     types.String `tfsdk:"control_plane_provider"`
	CoreProvider             types.String `tfsdk:"core_provider"`
	TargetNamespace          types.String `tfsdk:"target_namespace"`
	KubernetesVersion        types.String `tfsdk:"kubernetes_version"`
	ControlPlaneMachineCount types.Int64  `tfsdk:"control_plane_machine_count"`
	WorkerMachineCount       types.Int64  `tfsdk:"worker_machine_count"`
	Flavor                   types.String `tfsdk:"flavor"`
	Id                       types.String `tfsdk:"id"`
	Endpoint                 types.String `tfsdk:"endpoint"`
	ClusterCACertificate     types.String `tfsdk:"cluster_ca_certificate"`
	Kubeconfig               types.String `tfsdk:"kubeconfig"`
	ClusterDescription       types.String `tfsdk:"cluster_description"`
	BootstrapClusterName     types.String `tfsdk:"bootstrap_cluster_name"`
}

func clusterResourceSchemaV0() schema.Schema {
	return schema.Schema{
		Attributes: map[string]schema.Attribute{
			"name":                        schema.StringAttribute{Required: true},
			"kubeconfig_path":             schema.StringAttribute{Optional: true, Computed: true},
			"management_kubeconfig":       schema.StringAttribute{Optional: true},
			"skip_init":                   schema.BoolAttribute{Optional: true, Computed: true},
			"wait_for_ready":              schema.BoolAttribute{Optional: true, Computed: true},
			"self_managed":                schema.BoolAttribute{Optional: true, Computed: true},
			"infrastructure_provider":     schema.StringAttribute{Required: true},
			"bootstrap_provider":          schema.StringAttribute{Optional: true},
			"control_plane_provider":      schema.StringAttribute{Optional: true},
			"core_provider":               schema.StringAttribute{Optional: true},
			"target_namespace":            schema.StringAttribute{Optional: true, Computed: true},
			"kubernetes_version":          schema.StringAttribute{Optional: true},
			"control_plane_machine_count": schema.Int64Attribute{Optional: true},
			"worker_machine_count":        schema.Int64Attribute{Optional: true},
			"flavor":                      schema.StringAttribute{Optional: true},
			"id":                          schema.StringAttribute{Computed: true},
			"endpoint":                    schema.StringAttribute{Computed: true},
			"cluster_ca_certificate":      schema.StringAttribute{Computed: true, Sensitive: true},
			"kubeconfig":                  schema.StringAttribute{Computed: true, Sensitive: true},
			"cluster_description":         schema.StringAttribute{Computed: true},
			"bootstrap_cluster_name":      schema.StringAttribute{Computed: true},
		},
	}
}

// --- v1 Schema (nested SingleNestedAttribute providers) for state migration ---

type clusterResourceModelV1 struct {
	Name              types.String `tfsdk:"name"`
	KubernetesVersion types.String `tfsdk:"kubernetes_version"`
	Flavor            types.String `tfsdk:"flavor"`
	Id                types.String `tfsdk:"id"`

	Management     types.Object `tfsdk:"management"`
	Infrastructure types.Object `tfsdk:"infrastructure"`
	Bootstrap      types.Object `tfsdk:"bootstrap"`
	ControlPlane   types.Object `tfsdk:"control_plane"`
	Core           types.Object `tfsdk:"core"`
	Workers        types.Object `tfsdk:"workers"`
	Addons         types.List   `tfsdk:"addons"`
	Inventory      types.Object `tfsdk:"inventory"`
	Wait           types.Object `tfsdk:"wait"`
	Output         types.Object `tfsdk:"output"`
	Status         types.Object `tfsdk:"status"`
}

func clusterResourceSchemaV1() schema.Schema {
	return schema.Schema{
		Attributes: map[string]schema.Attribute{
			"name":               schema.StringAttribute{Required: true},
			"kubernetes_version": schema.StringAttribute{Optional: true},
			"flavor":             schema.StringAttribute{Optional: true},
			"id":                 schema.StringAttribute{Computed: true},
			"management": schema.SingleNestedAttribute{
				Optional: true, Computed: true,
				Attributes: map[string]schema.Attribute{
					"kubeconfig":   schema.StringAttribute{Optional: true},
					"skip_init":    schema.BoolAttribute{Optional: true, Computed: true},
					"self_managed": schema.BoolAttribute{Optional: true, Computed: true},
					"namespace":    schema.StringAttribute{Optional: true, Computed: true},
				},
			},
			"infrastructure": schema.SingleNestedAttribute{
				Required: true,
				Attributes: map[string]schema.Attribute{
					"provider": schema.StringAttribute{Required: true},
				},
			},
			"bootstrap": schema.SingleNestedAttribute{
				Optional: true,
				Attributes: map[string]schema.Attribute{
					"provider": schema.StringAttribute{Required: true},
				},
			},
			"control_plane": schema.SingleNestedAttribute{
				Optional: true,
				Attributes: map[string]schema.Attribute{
					"provider":      schema.StringAttribute{Optional: true},
					"machine_count": schema.Int64Attribute{Optional: true},
				},
			},
			"core": schema.SingleNestedAttribute{
				Optional: true,
				Attributes: map[string]schema.Attribute{
					"provider": schema.StringAttribute{Required: true},
				},
			},
			"workers": schema.SingleNestedAttribute{
				Optional: true,
				Attributes: map[string]schema.Attribute{
					"machine_count": schema.Int64Attribute{Optional: true},
				},
			},
			"addons": schema.ListNestedAttribute{
				Optional: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"provider":                schema.StringAttribute{Required: true},
						"config_variables":        schema.MapAttribute{ElementType: types.StringType, Optional: true},
						"secret_config_variables": schema.MapAttribute{ElementType: types.StringType, Optional: true, Sensitive: true},
						"fetch_config": schema.SingleNestedAttribute{
							Optional: true,
							Attributes: map[string]schema.Attribute{
								"url": schema.StringAttribute{Optional: true},
								"oci": schema.StringAttribute{Optional: true},
							},
						},
						"deployment": schema.SingleNestedAttribute{
							Optional: true,
							Attributes: map[string]schema.Attribute{
								"replicas":             schema.Int64Attribute{Optional: true},
								"node_selector":        schema.MapAttribute{ElementType: types.StringType, Optional: true},
								"service_account_name": schema.StringAttribute{Optional: true},
								"containers": schema.ListNestedAttribute{
									Optional: true,
									NestedObject: schema.NestedAttributeObject{
										Attributes: map[string]schema.Attribute{
											"name":      schema.StringAttribute{Required: true},
											"image_url": schema.StringAttribute{Optional: true},
											"args":      schema.MapAttribute{ElementType: types.StringType, Optional: true},
											"command":   schema.ListAttribute{ElementType: types.StringType, Optional: true},
										},
									},
								},
							},
						},
						"manager": schema.SingleNestedAttribute{
							Optional: true,
							Attributes: map[string]schema.Attribute{
								"profiler_address":          schema.StringAttribute{Optional: true},
								"max_concurrent_reconciles": schema.Int64Attribute{Optional: true},
								"verbosity":                 schema.Int64Attribute{Optional: true},
								"feature_gates":             schema.MapAttribute{ElementType: types.BoolType, Optional: true},
								"additional_args":           schema.MapAttribute{ElementType: types.StringType, Optional: true},
							},
						},
						"additional_manifests": schema.StringAttribute{Optional: true},
						"manifest_patches":     schema.ListAttribute{ElementType: types.StringType, Optional: true},
						"patches": schema.ListNestedAttribute{
							Optional: true,
							NestedObject: schema.NestedAttributeObject{
								Attributes: map[string]schema.Attribute{
									"patch": schema.StringAttribute{Optional: true},
									"target": schema.SingleNestedAttribute{
										Optional: true,
										Attributes: map[string]schema.Attribute{
											"group": schema.StringAttribute{Optional: true}, "version": schema.StringAttribute{Optional: true},
											"kind": schema.StringAttribute{Optional: true}, "name": schema.StringAttribute{Optional: true},
											"namespace": schema.StringAttribute{Optional: true}, "label_selector": schema.StringAttribute{Optional: true},
										},
									},
								},
							},
						},
					},
				},
			},
			"inventory": schema.SingleNestedAttribute{
				Optional: true,
				Attributes: map[string]schema.Attribute{
					"source": schema.StringAttribute{Optional: true},
					"machine": schema.ListNestedAttribute{
						Optional: true,
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"hostname": schema.StringAttribute{Required: true},
								"network": schema.SingleNestedAttribute{
									Required: true,
									Attributes: map[string]schema.Attribute{
										"ip_address": schema.StringAttribute{Required: true}, "netmask": schema.StringAttribute{Required: true},
										"gateway": schema.StringAttribute{Required: true}, "mac_address": schema.StringAttribute{Required: true},
										"nameservers": schema.ListAttribute{Optional: true, ElementType: types.StringType},
										"vlan_id":     schema.StringAttribute{Optional: true},
									},
								},
								"disk": schema.SingleNestedAttribute{
									Optional:   true,
									Attributes: map[string]schema.Attribute{"device": schema.StringAttribute{Required: true}},
								},
								"bmc": schema.SingleNestedAttribute{
									Optional: true,
									Attributes: map[string]schema.Attribute{
										"address": schema.StringAttribute{Required: true}, "username": schema.StringAttribute{Required: true},
										"password": schema.StringAttribute{Required: true, Sensitive: true},
									},
								},
								"labels": schema.MapAttribute{Optional: true, ElementType: types.StringType},
							},
						},
					},
				},
			},
			"wait": schema.SingleNestedAttribute{
				Optional: true, Computed: true,
				Attributes: map[string]schema.Attribute{
					"enabled": schema.BoolAttribute{Optional: true, Computed: true},
					"timeout": schema.StringAttribute{Optional: true, Computed: true},
				},
			},
			"output": schema.SingleNestedAttribute{
				Optional: true, Computed: true,
				Attributes: map[string]schema.Attribute{
					"kubeconfig_path": schema.StringAttribute{Optional: true, Computed: true},
				},
			},
			"status": schema.SingleNestedAttribute{
				Computed: true,
				Attributes: map[string]schema.Attribute{
					"endpoint":          schema.StringAttribute{Computed: true},
					"kubeconfig":        schema.StringAttribute{Computed: true, Sensitive: true},
					"ca_certificate":    schema.StringAttribute{Computed: true, Sensitive: true},
					"description":       schema.StringAttribute{Computed: true},
					"bootstrap_cluster": schema.StringAttribute{Computed: true},
				},
			},
		},
	}
}

// -- Migration helpers --

// migrateV0ToV1 converts flat v0 state to the nested v1 intermediate model.
func migrateV0ToV1(ctx context.Context, v0 clusterResourceModelV0, diags *diag.Diagnostics) clusterResourceModelV1 {
	v1 := clusterResourceModelV1{
		Name:              v0.Name,
		KubernetesVersion: v0.KubernetesVersion,
		Flavor:            v0.Flavor,
		Id:                v0.Id,
	}

	ns := v0.TargetNamespace
	if ns.IsNull() {
		ns = types.StringValue("default")
	}
	mgmt := ManagementModel{Kubeconfig: v0.ManagementKubeconfig, SkipInit: v0.SkipInit, SelfManaged: v0.SelfManaged, Namespace: ns}
	mgmtVal, d := types.ObjectValueFrom(ctx, managementAttrTypes(), mgmt)
	diags.Append(d...)
	v1.Management = mgmtVal

	// Infrastructure as SingleNestedAttribute with "provider" key
	v1InfraAttrTypes := map[string]attr.Type{"provider": types.StringType}
	infraObj, d := types.ObjectValueFrom(ctx, v1InfraAttrTypes, map[string]attr.Value{"provider": v0.InfrastructureProvider})
	diags.Append(d...)
	v1.Infrastructure = infraObj

	// Bootstrap
	v1BsAttrTypes := map[string]attr.Type{"provider": types.StringType}
	if !v0.BootstrapProvider.IsNull() {
		bsObj, d := types.ObjectValueFrom(ctx, v1BsAttrTypes, map[string]attr.Value{"provider": v0.BootstrapProvider})
		diags.Append(d...)
		v1.Bootstrap = bsObj
	} else {
		v1.Bootstrap = types.ObjectNull(v1BsAttrTypes)
	}

	// Control plane
	v1CpAttrTypes := map[string]attr.Type{"provider": types.StringType, "machine_count": types.Int64Type}
	if !v0.ControlPlaneProvider.IsNull() || !v0.ControlPlaneMachineCount.IsNull() {
		cpAttrs := map[string]attr.Value{
			"provider":      v0.ControlPlaneProvider,
			"machine_count": v0.ControlPlaneMachineCount,
		}
		cpObj, d := types.ObjectValueFrom(ctx, v1CpAttrTypes, cpAttrs)
		diags.Append(d...)
		v1.ControlPlane = cpObj
	} else {
		v1.ControlPlane = types.ObjectNull(v1CpAttrTypes)
	}

	// Core
	v1CoreAttrTypes := map[string]attr.Type{"provider": types.StringType}
	if !v0.CoreProvider.IsNull() {
		coreObj, d := types.ObjectValueFrom(ctx, v1CoreAttrTypes, map[string]attr.Value{"provider": v0.CoreProvider})
		diags.Append(d...)
		v1.Core = coreObj
	} else {
		v1.Core = types.ObjectNull(v1CoreAttrTypes)
	}

	// Workers
	v1WorkersAttrTypes := map[string]attr.Type{"machine_count": types.Int64Type}
	if !v0.WorkerMachineCount.IsNull() {
		wObj, d := types.ObjectValueFrom(ctx, v1WorkersAttrTypes, map[string]attr.Value{"machine_count": v0.WorkerMachineCount})
		diags.Append(d...)
		v1.Workers = wObj
	} else {
		v1.Workers = types.ObjectNull(v1WorkersAttrTypes)
	}

	v1.Inventory = types.ObjectNull(inventoryAttrTypes())
	v1.Addons = types.ListNull(types.ObjectType{AttrTypes: v1AddonAttrTypes()})

	wait := WaitModel{Enabled: v0.WaitForReady, Timeout: types.StringNull()}
	waitVal, d := types.ObjectValueFrom(ctx, waitAttrTypes(), wait)
	diags.Append(d...)
	v1.Wait = waitVal

	out := OutputModel{KubeconfigPath: v0.KubeconfigPath}
	outVal, d := types.ObjectValueFrom(ctx, outputAttrTypes(), out)
	diags.Append(d...)
	v1.Output = outVal

	status := StatusModel{
		Endpoint: v0.Endpoint, Kubeconfig: v0.Kubeconfig,
		CACertificate: v0.ClusterCACertificate, Description: v0.ClusterDescription,
		BootstrapCluster: v0.BootstrapClusterName,
	}
	statusVal, d := types.ObjectValueFrom(ctx, statusAttrTypes(), status)
	diags.Append(d...)
	v1.Status = statusVal

	return v1
}

// v1AddonAttrTypes returns the attr types for the v1 addons list element.
func v1AddonAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"provider":                types.StringType,
		"config_variables":        types.MapType{ElemType: types.StringType},
		"secret_config_variables": types.MapType{ElemType: types.StringType},
		"fetch_config":            types.ObjectType{AttrTypes: fetchConfigAttrTypes()},
		"deployment":              types.ObjectType{AttrTypes: deploymentAttrTypes()},
		"manager":                 types.ObjectType{AttrTypes: managerAttrTypes()},
		"additional_manifests":    types.StringType,
		"manifest_patches":        types.ListType{ElemType: types.StringType},
		"patches":                 types.ListType{ElemType: types.ObjectType{AttrTypes: patchAttrTypes()}},
	}
}

// migrateV1ToV2 converts the v1 nested-single-attribute model to the v2
// map-based provider + topology model.
func migrateV1ToV2(ctx context.Context, v1 clusterResourceModelV1, diags *diag.Diagnostics) ClusterResourceModel {
	v2 := ClusterResourceModel{
		Name:              v1.Name,
		KubernetesVersion: v1.KubernetesVersion,
		Flavor:            v1.Flavor,
		Id:                v1.Id,
		Management:        v1.Management,
		Inventory:         v1.Inventory,
		Wait:              v1.Wait,
		Output:            v1.Output,
		Status:            v1.Status,
	}

	providerMapElemType := NewProviderConfigType()

	// Infrastructure: SingleNested{provider: "docker:v1.2.3"} → Map{"docker": {version: "v1.2.3"}}
	v2.Infrastructure = migrateProviderObjectToMap(ctx, v1.Infrastructure, "provider", providerMapElemType, diags)

	// Bootstrap
	v2.Bootstrap = migrateProviderObjectToMap(ctx, v1.Bootstrap, "provider", providerMapElemType, diags)

	// Control plane — has both "provider" and "machine_count"
	v2.ControlPlane = migrateProviderObjectToMap(ctx, v1.ControlPlane, "provider", providerMapElemType, diags)

	// Core
	v2.Core = migrateProviderObjectToMap(ctx, v1.Core, "provider", providerMapElemType, diags)

	// Addons (list) → Map
	v2.Addon = migrateAddonsListToMap(ctx, v1.Addons, providerMapElemType, diags)

	// Topology: extract machine_count from v1 control_plane and workers
	var cpCount, workerCount types.Int64
	cpCount = types.Int64Null()
	workerCount = types.Int64Null()

	if !v1.ControlPlane.IsNull() && !v1.ControlPlane.IsUnknown() {
		cpAttrs := v1.ControlPlane.Attributes()
		if mc, ok := cpAttrs["machine_count"]; ok {
			if intVal, isInt := mc.(basetypes.Int64Value); isInt && !intVal.IsNull() {
				cpCount = intVal
			}
		}
	}
	if !v1.Workers.IsNull() && !v1.Workers.IsUnknown() {
		wAttrs := v1.Workers.Attributes()
		if mc, ok := wAttrs["machine_count"]; ok {
			if intVal, isInt := mc.(basetypes.Int64Value); isInt && !intVal.IsNull() {
				workerCount = intVal
			}
		}
	}

	if !cpCount.IsNull() || !workerCount.IsNull() {
		topo := TopologyModel{ControlPlaneCount: cpCount, WorkerCount: workerCount}
		topoVal, d := types.ObjectValueFrom(ctx, topologyAttrTypes(), topo)
		diags.Append(d...)
		v2.Topology = topoVal
	} else {
		v2.Topology = types.ObjectNull(topologyAttrTypes())
	}

	return v2
}

// migrateProviderObjectToMap converts a v1 SingleNestedAttribute with a
// "provider" field (e.g., {provider: "docker:v1.2.3"}) to a v2
// MapNestedAttribute (e.g., {"docker": {version: "v1.2.3"}}).
func migrateProviderObjectToMap(ctx context.Context, obj types.Object, providerKey string, elemType ProviderConfigType, diags *diag.Diagnostics) types.Map {
	if obj.IsNull() || obj.IsUnknown() {
		return types.MapNull(elemType)
	}

	attrs := obj.Attributes()
	providerVal, ok := attrs[providerKey]
	if !ok {
		return types.MapNull(elemType)
	}
	strVal, isStr := providerVal.(basetypes.StringValue)
	if !isStr || strVal.IsNull() || strVal.ValueString() == "" {
		return types.MapNull(elemType)
	}

	providerStr := strVal.ValueString()
	name, version := parseProviderNameVersion(providerStr)

	model := ProviderConfigModel{
		Version:               stringOrNull(version),
		Namespace:             types.StringNull(),
		ConfigVariables:       types.MapNull(types.StringType),
		SecretConfigVariables: types.MapNull(types.StringType),
		FetchConfig:           types.ObjectNull(fetchConfigAttrTypes()),
		Deployment:            types.ObjectNull(deploymentAttrTypes()),
		Manager:               types.ObjectNull(managerAttrTypes()),
		AdditionalManifests:   types.StringNull(),
		ManifestPatches:       types.ListNull(types.StringType),
		Patches:               types.ListNull(types.ObjectType{AttrTypes: patchAttrTypes()}),
	}

	elemVal, d := NewProviderConfigValueFrom(ctx, model)
	diags.Append(d...)

	mapVal, d := types.MapValueFrom(ctx, elemType, map[string]ProviderConfigValue{name: elemVal})
	diags.Append(d...)
	return mapVal
}

// migrateAddonsListToMap converts a v1 addons list to a v2 addon map.
func migrateAddonsListToMap(ctx context.Context, addons types.List, elemType ProviderConfigType, diags *diag.Diagnostics) types.Map {
	if addons.IsNull() || addons.IsUnknown() {
		return types.MapNull(elemType)
	}

	elements := addons.Elements()
	if len(elements) == 0 {
		return types.MapNull(elemType)
	}

	result := make(map[string]ProviderConfigValue)
	for _, elem := range elements {
		objVal, ok := elem.(basetypes.ObjectValue)
		if !ok || objVal.IsNull() {
			continue
		}
		attrs := objVal.Attributes()
		providerVal, ok := attrs["provider"]
		if !ok {
			continue
		}
		strVal, isStr := providerVal.(basetypes.StringValue)
		if !isStr || strVal.IsNull() {
			continue
		}

		providerStr := strVal.ValueString()
		name, version := parseProviderNameVersion(providerStr)

		// Transfer customization fields from v1 addon to v2 ProviderConfigModel
		model := ProviderConfigModel{
			Version:               stringOrNull(version),
			Namespace:             types.StringNull(),
			AdditionalManifests:   types.StringNull(),
			ConfigVariables:       types.MapNull(types.StringType),
			SecretConfigVariables: types.MapNull(types.StringType),
			FetchConfig:           types.ObjectNull(fetchConfigAttrTypes()),
			Deployment:            types.ObjectNull(deploymentAttrTypes()),
			Manager:               types.ObjectNull(managerAttrTypes()),
			ManifestPatches:       types.ListNull(types.StringType),
			Patches:               types.ListNull(types.ObjectType{AttrTypes: patchAttrTypes()}),
		}

		// Copy over non-null fields from the v1 addon object
		if cv, ok := attrs["config_variables"]; ok {
			if mv, isMap := cv.(basetypes.MapValue); isMap && !mv.IsNull() {
				model.ConfigVariables = mv
			}
		}
		if scv, ok := attrs["secret_config_variables"]; ok {
			if mv, isMap := scv.(basetypes.MapValue); isMap && !mv.IsNull() {
				model.SecretConfigVariables = mv
			}
		}
		if fc, ok := attrs["fetch_config"]; ok {
			if ov, isObj := fc.(basetypes.ObjectValue); isObj && !ov.IsNull() {
				model.FetchConfig = ov
			}
		}
		if dep, ok := attrs["deployment"]; ok {
			if ov, isObj := dep.(basetypes.ObjectValue); isObj && !ov.IsNull() {
				model.Deployment = ov
			}
		}
		if mgr, ok := attrs["manager"]; ok {
			if ov, isObj := mgr.(basetypes.ObjectValue); isObj && !ov.IsNull() {
				model.Manager = ov
			}
		}
		if am, ok := attrs["additional_manifests"]; ok {
			if sv, isStr := am.(basetypes.StringValue); isStr && !sv.IsNull() {
				model.AdditionalManifests = sv
			}
		}
		if mp, ok := attrs["manifest_patches"]; ok {
			if lv, isList := mp.(basetypes.ListValue); isList && !lv.IsNull() {
				model.ManifestPatches = lv
			}
		}
		if p, ok := attrs["patches"]; ok {
			if lv, isList := p.(basetypes.ListValue); isList && !lv.IsNull() {
				model.Patches = lv
			}
		}

		elemVal, d := NewProviderConfigValueFrom(ctx, model)
		diags.Append(d...)
		result[name] = elemVal
	}

	mapVal, d := types.MapValueFrom(ctx, elemType, result)
	diags.Append(d...)
	return mapVal
}

// parseProviderNameVersion splits "name:version" into (name, version).
func parseProviderNameVersion(s string) (string, string) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return parts[0], ""
}
