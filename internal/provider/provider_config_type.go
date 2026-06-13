// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/attr/xattr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// Compile-time interface checks.
var (
	_ basetypes.ObjectTypable  = ProviderConfigType{}
	_ basetypes.ObjectValuable = ProviderConfigValue{}

	// ProviderConfigValue implements attribute validation.
	_ xattr.ValidateableAttribute = ProviderConfigValue{}
)

// ---------------------------------------------------------------------------
// ProviderConfigType — schema type
// ---------------------------------------------------------------------------

// ProviderConfigType is a custom Object type shared by all provider maps
// (infrastructure, bootstrap, control_plane, core, addon).  It mirrors the
// capi-operator Helm chart values per-provider structure and carries all
// customization knobs (deployment, manager, patches, etc.).
type ProviderConfigType struct {
	basetypes.ObjectType
}

// NewProviderConfigType returns a ProviderConfigType pre-loaded with the
// canonical attribute type map.
func NewProviderConfigType() ProviderConfigType {
	return ProviderConfigType{
		ObjectType: basetypes.ObjectType{AttrTypes: providerConfigAttrTypes()},
	}
}

func (t ProviderConfigType) Equal(o attr.Type) bool {
	other, ok := o.(ProviderConfigType)
	if !ok {
		return false
	}
	return t.ObjectType.Equal(other.ObjectType)
}

func (t ProviderConfigType) String() string {
	return "ProviderConfigType"
}

func (t ProviderConfigType) ValueFromObject(ctx context.Context, in basetypes.ObjectValue) (basetypes.ObjectValuable, diag.Diagnostics) {
	return ProviderConfigValue{ObjectValue: in}, nil
}

func (t ProviderConfigType) ValueFromTerraform(ctx context.Context, in tftypes.Value) (attr.Value, error) {
	attrValue, err := t.ObjectType.ValueFromTerraform(ctx, in)
	if err != nil {
		return nil, err
	}

	objectValue, ok := attrValue.(basetypes.ObjectValue)
	if !ok {
		return nil, fmt.Errorf("unexpected value type %T", attrValue)
	}

	objectValuable, diags := t.ValueFromObject(ctx, objectValue)
	if diags.HasError() {
		return nil, fmt.Errorf("converting ObjectValue to ProviderConfigValue: %v", diags)
	}

	return objectValuable, nil
}

func (t ProviderConfigType) ValueType(_ context.Context) attr.Value {
	return ProviderConfigValue{}
}

// ---------------------------------------------------------------------------
// ProviderConfigValue — value type
// ---------------------------------------------------------------------------

// ProviderConfigValue is the custom value type for a single provider
// configuration entry.  It embeds basetypes.ObjectValue and adds built-in
// validation logic that is shared across all provider map attributes.
type ProviderConfigValue struct {
	basetypes.ObjectValue
}

func (v ProviderConfigValue) Equal(o attr.Value) bool {
	other, ok := o.(ProviderConfigValue)
	if !ok {
		return false
	}
	return v.ObjectValue.Equal(other.ObjectValue)
}

func (v ProviderConfigValue) Type(_ context.Context) attr.Type {
	return NewProviderConfigType()
}

// ValidateAttribute implements xattr.ValidateableAttribute and performs
// cross-field validation shared by all provider types:
//   - fetch_config: url and oci are mutually exclusive
//   - manifest_patches and patches are mutually exclusive
func (v ProviderConfigValue) ValidateAttribute(ctx context.Context, req xattr.ValidateAttributeRequest, resp *xattr.ValidateAttributeResponse) {
	if v.IsNull() || v.IsUnknown() {
		return
	}

	attrs := v.Attributes()

	// fetch_config: url and oci mutually exclusive.
	if fc, ok := attrs["fetch_config"]; ok {
		if objVal, isObj := fc.(basetypes.ObjectValue); isObj && !objVal.IsNull() && !objVal.IsUnknown() {
			fcAttrs := objVal.Attributes()
			urlAttr, _ := fcAttrs["url"].(basetypes.StringValue)
			ociAttr, _ := fcAttrs["oci"].(basetypes.StringValue)
			hasURL := !urlAttr.IsNull() && urlAttr.ValueString() != ""
			hasOCI := !ociAttr.IsNull() && ociAttr.ValueString() != ""
			if hasURL && hasOCI {
				resp.Diagnostics.AddAttributeError(
					req.Path.AtName("fetch_config"),
					"Invalid fetch configuration",
					"fetch_config must specify at most one of url or oci.",
				)
			}
		}
	}

	// manifest_patches and patches are mutually exclusive.
	mpAttr, _ := attrs["manifest_patches"].(basetypes.ListValue)
	pAttr, _ := attrs["patches"].(basetypes.ListValue)
	hasMP := !mpAttr.IsNull() && !mpAttr.IsUnknown() && len(mpAttr.Elements()) > 0
	hasP := !pAttr.IsNull() && !pAttr.IsUnknown() && len(pAttr.Elements()) > 0
	if hasMP && hasP {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid patch configuration",
			"manifest_patches and patches are mutually exclusive. Use one or the other.",
		)
	}
}

// ---------------------------------------------------------------------------
// Constructors
// ---------------------------------------------------------------------------

// NewProviderConfigNull returns a null ProviderConfigValue.
func NewProviderConfigNull() ProviderConfigValue {
	return ProviderConfigValue{
		ObjectValue: basetypes.NewObjectNull(providerConfigAttrTypes()),
	}
}

// NewProviderConfigValueFrom constructs a ProviderConfigValue from a model.
func NewProviderConfigValueFrom(ctx context.Context, model ProviderConfigModel) (ProviderConfigValue, diag.Diagnostics) {
	objVal, diags := types.ObjectValueFrom(ctx, providerConfigAttrTypes(), model)
	if diags.HasError() {
		return NewProviderConfigNull(), diags
	}
	return ProviderConfigValue{ObjectValue: objVal}, nil
}

// ---------------------------------------------------------------------------
// ProviderConfigModel — Go model for tfsdk deserialization
// ---------------------------------------------------------------------------

// ProviderConfigModel describes a single provider configuration entry,
// shared by infrastructure, bootstrap, control_plane, core, and addon
// MapNestedAttributes.  The map key serves as the provider name; version
// is an explicit field (matching the capi-operator Helm chart pattern).
type ProviderConfigModel struct {
	Version               types.String `tfsdk:"version"`
	Namespace             types.String `tfsdk:"namespace"`
	ConfigVariables       types.Map    `tfsdk:"config_variables"`
	SecretConfigVariables types.Map    `tfsdk:"secret_config_variables"`
	FetchConfig           types.Object `tfsdk:"fetch_config"`
	Deployment            types.Object `tfsdk:"deployment"`
	Manager               types.Object `tfsdk:"manager"`
	AdditionalManifests   types.String `tfsdk:"additional_manifests"`
	ManifestPatches       types.List   `tfsdk:"manifest_patches"`
	Patches               types.List   `tfsdk:"patches"`
}

// ---------------------------------------------------------------------------
// Attribute types & schema attributes
// ---------------------------------------------------------------------------

// providerConfigAttrTypes returns the attr.Type map for ProviderConfigModel.
func providerConfigAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"version":                 types.StringType,
		"namespace":               types.StringType,
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

func fetchConfigAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"url": types.StringType,
		"oci": types.StringType,
	}
}

func deploymentAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"replicas":             types.Int64Type,
		"node_selector":        types.MapType{ElemType: types.StringType},
		"service_account_name": types.StringType,
		"containers":           types.ListType{ElemType: types.ObjectType{AttrTypes: containerAttrTypes()}},
	}
}

func containerAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"name":      types.StringType,
		"image_url": types.StringType,
		"args":      types.MapType{ElemType: types.StringType},
		"command":   types.ListType{ElemType: types.StringType},
	}
}

func managerAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"profiler_address":          types.StringType,
		"max_concurrent_reconciles": types.Int64Type,
		"verbosity":                 types.Int64Type,
		"feature_gates":             types.MapType{ElemType: types.BoolType},
		"additional_args":           types.MapType{ElemType: types.StringType},
	}
}

func patchAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"patch":  types.StringType,
		"target": types.ObjectType{AttrTypes: patchSelectorAttrTypes()},
	}
}

func patchSelectorAttrTypes() map[string]attr.Type {
	return map[string]attr.Type{
		"group":          types.StringType,
		"version":        types.StringType,
		"kind":           types.StringType,
		"name":           types.StringType,
		"namespace":      types.StringType,
		"label_selector": types.StringType,
	}
}

// providerConfigSchemaAttributes returns the schema attribute map shared
// by every provider config MapNestedAttribute.  It is the single source
// of truth for the UX across infrastructure, bootstrap, control_plane,
// core, and addon blocks.
func providerConfigSchemaAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"version": schema.StringAttribute{
			MarkdownDescription: "Provider version (e.g., `v1.12.2`). Omit to use the default version from clusterctl.",
			Optional:            true,
		},
		"namespace": schema.StringAttribute{
			MarkdownDescription: "Namespace where the provider components are installed.",
			Optional:            true,
		},
		"config_variables": schema.MapAttribute{
			MarkdownDescription: "Template variables injected into the provider's component YAML during processing (`${VAR}` substitution). These take precedence over clusterctl config and environment variables.",
			ElementType:         types.StringType,
			Optional:            true,
		},
		"secret_config_variables": schema.MapAttribute{
			MarkdownDescription: "Sensitive template variables injected into the provider's component YAML. Same mechanism as `config_variables` but for secret values.",
			ElementType:         types.StringType,
			Optional:            true,
			Sensitive:           true,
		},
		"fetch_config": schema.SingleNestedAttribute{
			MarkdownDescription: "Determines how the provider fetches components and metadata. At most one of `url` or `oci` may be specified.",
			Optional:            true,
			Attributes: map[string]schema.Attribute{
				"url": schema.StringAttribute{
					MarkdownDescription: "URL for fetching provider components from a remote GitHub repository (e.g., `https://github.com/{owner}/{repo}/releases`).",
					Optional:            true,
				},
				"oci": schema.StringAttribute{
					MarkdownDescription: "OCI artifact reference for fetching provider components (e.g., `oci://ghcr.io/org/provider`).",
					Optional:            true,
				},
			},
		},
		"deployment": schema.SingleNestedAttribute{
			MarkdownDescription: "Deployment customization for the provider controller.",
			Optional:            true,
			Attributes: map[string]schema.Attribute{
				"replicas": schema.Int64Attribute{
					MarkdownDescription: "Number of desired pods. Defaults to 1.",
					Optional:            true,
				},
				"node_selector": schema.MapAttribute{
					MarkdownDescription: "Node selector labels for pod scheduling.",
					ElementType:         types.StringType,
					Optional:            true,
				},
				"service_account_name": schema.StringAttribute{
					MarkdownDescription: "Service account name for the provider pod.",
					Optional:            true,
				},
				"containers": schema.ListNestedAttribute{
					MarkdownDescription: "Container overrides for the provider deployment.",
					Optional:            true,
					NestedObject: schema.NestedAttributeObject{
						Attributes: map[string]schema.Attribute{
							"name": schema.StringAttribute{
								MarkdownDescription: "Container name. Must match an existing container in the deployment.",
								Required:            true,
							},
							"image_url": schema.StringAttribute{
								MarkdownDescription: "Container image URL override.",
								Optional:            true,
							},
							"args": schema.MapAttribute{
								MarkdownDescription: "Extra arguments passed to the container entrypoint.",
								ElementType:         types.StringType,
								Optional:            true,
							},
							"command": schema.ListAttribute{
								MarkdownDescription: "Override for the container entrypoint command.",
								ElementType:         types.StringType,
								Optional:            true,
							},
						},
					},
				},
			},
		},
		"manager": schema.SingleNestedAttribute{
			MarkdownDescription: "Controller manager configuration for the provider.",
			Optional:            true,
			Attributes: map[string]schema.Attribute{
				"profiler_address": schema.StringAttribute{
					MarkdownDescription: "Bind address for the pprof profiler (e.g., `localhost:6060`). Empty disables profiling.",
					Optional:            true,
				},
				"max_concurrent_reconciles": schema.Int64Attribute{
					MarkdownDescription: "Maximum number of concurrent reconciles.",
					Optional:            true,
				},
				"verbosity": schema.Int64Attribute{
					MarkdownDescription: "Log verbosity level. Defaults to 1.",
					Optional:            true,
				},
				"feature_gates": schema.MapAttribute{
					MarkdownDescription: "Provider-specific feature gates passed as `--feature-gates` to the controller manager.",
					ElementType:         types.BoolType,
					Optional:            true,
				},
				"additional_args": schema.MapAttribute{
					MarkdownDescription: "Additional arguments passed as container args to the controller manager.",
					ElementType:         types.StringType,
					Optional:            true,
				},
			},
		},
		"additional_manifests": schema.StringAttribute{
			MarkdownDescription: "Inline YAML content of additional manifests to apply along with the provider components. Supports multi-document YAML (separated by `---`).",
			Optional:            true,
		},
		"manifest_patches": schema.ListAttribute{
			MarkdownDescription: "JSON merge patches applied to rendered provider manifests. Each entry is an inline YAML/JSON blob string (RFC 7396). Cannot be used together with `patches`.",
			ElementType:         types.StringType,
			Optional:            true,
		},
		"patches": schema.ListNestedAttribute{
			MarkdownDescription: "Strategic merge patches or RFC 6902 JSON patches applied to rendered provider manifests. Cannot be used together with `manifest_patches`.",
			Optional:            true,
			NestedObject: schema.NestedAttributeObject{
				Attributes: map[string]schema.Attribute{
					"patch": schema.StringAttribute{
						MarkdownDescription: "Inline YAML/JSON patch content.",
						Optional:            true,
					},
					"target": schema.SingleNestedAttribute{
						MarkdownDescription: "Target object selector for the patch.",
						Optional:            true,
						Attributes: map[string]schema.Attribute{
							"group":          schema.StringAttribute{Optional: true, MarkdownDescription: "API group of the target."},
							"version":        schema.StringAttribute{Optional: true, MarkdownDescription: "API version of the target."},
							"kind":           schema.StringAttribute{Optional: true, MarkdownDescription: "Kind of the target."},
							"name":           schema.StringAttribute{Optional: true, MarkdownDescription: "Name of the target."},
							"namespace":      schema.StringAttribute{Optional: true, MarkdownDescription: "Namespace of the target."},
							"label_selector": schema.StringAttribute{Optional: true, MarkdownDescription: "Label selector expression."},
						},
					},
				},
			},
		},
	}
}
