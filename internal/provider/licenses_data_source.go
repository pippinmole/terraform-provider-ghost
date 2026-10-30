package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

var (
	_ datasource.DataSource              = &LicensesDataSource{}
	_ datasource.DataSourceWithConfigure = &LicensesDataSource{}
)

// licensesID is the ID of every ghost_licenses data source: the list is the
// same for everyone.
const licensesID = "licenses"

// licenseSummaryAttrTypes is the object type of each element of licenses.
var licenseSummaryAttrTypes = map[string]attr.Type{
	"key":     types.StringType,
	"name":    types.StringType,
	"spdx_id": types.StringType,
}

// licenseAttrTypes is the object type of license.
var licenseAttrTypes = map[string]attr.Type{
	"key":            types.StringType,
	"name":           types.StringType,
	"spdx_id":        types.StringType,
	"html_url":       types.StringType,
	"description":    types.StringType,
	"implementation": types.StringType,
	"permissions":    types.ListType{ElemType: types.StringType},
	"conditions":     types.ListType{ElemType: types.StringType},
	"limitations":    types.ListType{ElemType: types.StringType},
	"body":           types.StringType,
	"featured":       types.BoolType,
}

// NewLicensesDataSource returns the ghost_licenses data source.
func NewLicensesDataSource() datasource.DataSource {
	return &LicensesDataSource{}
}

// LicensesDataSource lists the licenses Ghost offers and optionally reads one
// of them.
type LicensesDataSource struct {
	client *client.Client
}

// licensesDataSourceModel maps the ghost_licenses data source schema.
type licensesDataSourceModel struct {
	ID       types.String `tfsdk:"id"`
	Key      types.String `tfsdk:"key"`
	Keys     types.List   `tfsdk:"keys"`
	Licenses types.List   `tfsdk:"licenses"`
	License  types.Object `tfsdk:"license"`
}

func (d *LicensesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_licenses"
}

func (d *LicensesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the licenses Ghost offers, the values `ghost_repository`'s `license_template` " +
			"takes, and optionally reads one license's details and text with `key`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Always `" + licensesID + "`.",
				Computed:            true,
			},
			"key": schema.StringAttribute{
				MarkdownDescription: "A license to read into `license`, e.g. `mit`. Ghost matches it case-insensitively, " +
					"so an SPDX ID such as `MIT` or `Apache-2.0` works too. Omit it to only list the licenses.",
				Optional:   true,
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"keys": schema.ListAttribute{
				MarkdownDescription: "The key of every license, in Ghost's order (sorted by key).",
				ElementType:         types.StringType,
				Computed:            true,
			},
			"licenses": schema.ListNestedAttribute{
				MarkdownDescription: "Every license, in the same order as `keys`.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"key": schema.StringAttribute{
							MarkdownDescription: "The lower-case key `license_template` takes, e.g. `apache-2.0`.",
							Computed:            true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "The license's full name, e.g. `Apache License 2.0`.",
							Computed:            true,
						},
						"spdx_id": schema.StringAttribute{
							MarkdownDescription: "The license's SPDX identifier, e.g. `Apache-2.0`.",
							Computed:            true,
						},
					},
				},
			},
			"license": schema.SingleNestedAttribute{
				MarkdownDescription: "The license named by `key`; null when `key` isn't set.",
				Computed:            true,
				Attributes: map[string]schema.Attribute{
					"key": schema.StringAttribute{
						MarkdownDescription: "Ghost's lower-case key, which may differ in case from `key`.",
						Computed:            true,
					},
					"name": schema.StringAttribute{
						MarkdownDescription: "The license's full name.",
						Computed:            true,
					},
					"spdx_id": schema.StringAttribute{
						MarkdownDescription: "The license's SPDX identifier.",
						Computed:            true,
					},
					"html_url": schema.StringAttribute{
						MarkdownDescription: "The license's page on choosealicense.com.",
						Computed:            true,
					},
					"description": schema.StringAttribute{
						MarkdownDescription: "A short summary of the license.",
						Computed:            true,
					},
					"implementation": schema.StringAttribute{
						MarkdownDescription: "How to apply the license to a project.",
						Computed:            true,
					},
					"permissions": schema.ListAttribute{
						MarkdownDescription: "What the license permits, as choosealicense.com rule tags, e.g. `commercial-use`.",
						ElementType:         types.StringType,
						Computed:            true,
					},
					"conditions": schema.ListAttribute{
						MarkdownDescription: "What the license requires, e.g. `include-copyright`.",
						ElementType:         types.StringType,
						Computed:            true,
					},
					"limitations": schema.ListAttribute{
						MarkdownDescription: "What the license limits, e.g. `liability`.",
						ElementType:         types.StringType,
						Computed:            true,
					},
					"body": schema.StringAttribute{
						MarkdownDescription: "The license text. A repository created with this `license_template` (and " +
							"`auto_init`) starts with it as its `LICENSE`, with `[year]` replaced by the current year and " +
							"`[fullname]` by the owner's name.",
						Computed: true,
					},
					"featured": schema.BoolAttribute{
						MarkdownDescription: "Whether Ghost features the license (`mit`, `apache-2.0` and `gpl-3.0`).",
						Computed:            true,
					},
				},
			},
		},
	}
}

func (d *LicensesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data",
			fmt.Sprintf("Expected *client.Client, got %T. Please report this issue to the provider developers.", req.ProviderData))
		return
	}

	d.client = c
}

func (d *LicensesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data licensesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	licenses, err := d.client.ListLicenses(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error listing licenses", err.Error())
		return
	}

	var license *client.License
	if !data.Key.IsNull() {
		license, err = d.client.GetLicense(ctx, data.Key.ValueString())
		if client.IsNotFound(err) {
			resp.Diagnostics.AddAttributeError(path.Root("key"), "License not found",
				licenseNotFoundDetail(data.Key.ValueString(), licenses))
			return
		}
		if err != nil {
			resp.Diagnostics.AddError("Error reading license", err.Error())
			return
		}
	}

	resp.Diagnostics.Append(data.fromRemote(licenses, license)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// licenseNotFoundDetail explains that Ghost has no license with key, and lists
// the valid keys.
func licenseNotFoundDetail(key string, licenses []client.LicenseSummary) string {
	keys := make([]string, len(licenses))
	for i, l := range licenses {
		keys[i] = l.Key
	}

	return fmt.Sprintf("Ghost has no license with key %q. Keys are matched case-insensitively; valid keys are: %s.",
		key, strings.Join(keys, ", "))
}

// fromRemote sets every computed attribute of m from the listed licenses and
// the license looked up by key (nil when key isn't set).
func (m *licensesDataSourceModel) fromRemote(licenses []client.LicenseSummary, l *client.License) diag.Diagnostics {
	var diags diag.Diagnostics
	ctx := context.Background()

	keys := make([]string, len(licenses))
	elems := make([]attr.Value, len(licenses))
	for i, s := range licenses {
		keys[i] = s.Key
		elems[i] = types.ObjectValueMust(licenseSummaryAttrTypes, map[string]attr.Value{
			"key":     types.StringValue(s.Key),
			"name":    types.StringValue(s.Name),
			"spdx_id": types.StringValue(s.SPDXID),
		})
	}

	m.ID = types.StringValue(licensesID)
	var d diag.Diagnostics
	m.Keys, d = types.ListValueFrom(ctx, types.StringType, keys)
	diags.Append(d...)
	m.Licenses, d = types.ListValue(types.ObjectType{AttrTypes: licenseSummaryAttrTypes}, elems)
	diags.Append(d...)

	m.License = types.ObjectNull(licenseAttrTypes)
	if l == nil {
		return diags
	}

	lists := map[string][]string{"permissions": l.Permissions, "conditions": l.Conditions, "limitations": l.Limitations}
	values := map[string]attr.Value{
		"key":            types.StringValue(l.Key),
		"name":           types.StringValue(l.Name),
		"spdx_id":        types.StringValue(l.SPDXID),
		"html_url":       types.StringValue(l.HTMLURL),
		"description":    types.StringValue(l.Description),
		"implementation": types.StringValue(l.Implementation),
		"body":           types.StringValue(l.Body),
		"featured":       types.BoolValue(l.Featured),
	}
	for name, list := range lists {
		if list == nil {
			list = []string{}
		}
		values[name], d = types.ListValueFrom(ctx, types.StringType, list)
		diags.Append(d...)
	}
	if diags.HasError() {
		return diags
	}
	m.License = types.ObjectValueMust(licenseAttrTypes, values)

	return diags
}
