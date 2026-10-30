package provider

import (
	"context"
	"fmt"

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
	_ datasource.DataSource              = &GitignoreTemplatesDataSource{}
	_ datasource.DataSourceWithConfigure = &GitignoreTemplatesDataSource{}
)

// gitignoreTemplatesID is the ID of every ghost_gitignore_templates data
// source: the list is the same for everyone.
const gitignoreTemplatesID = "gitignore_templates"

// gitignoreTemplateAttrTypes is the object type of template.
var gitignoreTemplateAttrTypes = map[string]attr.Type{
	"name":   types.StringType,
	"source": types.StringType,
}

// NewGitignoreTemplatesDataSource returns the ghost_gitignore_templates data
// source.
func NewGitignoreTemplatesDataSource() datasource.DataSource {
	return &GitignoreTemplatesDataSource{}
}

// GitignoreTemplatesDataSource lists Ghost's .gitignore templates and
// optionally reads one of them.
type GitignoreTemplatesDataSource struct {
	client *client.Client
}

// gitignoreTemplatesDataSourceModel maps the ghost_gitignore_templates data
// source schema.
type gitignoreTemplatesDataSourceModel struct {
	ID       types.String `tfsdk:"id"`
	Name     types.String `tfsdk:"name"`
	Names    types.List   `tfsdk:"names"`
	Template types.Object `tfsdk:"template"`
}

func (d *GitignoreTemplatesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gitignore_templates"
}

func (d *GitignoreTemplatesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the `.gitignore` templates Ghost offers, the values `ghost_repository`'s " +
			"`gitignore_template` takes, and optionally reads one template's content with `name`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Always `" + gitignoreTemplatesID + "`.",
				Computed:            true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "A template to read into `template`, e.g. `Go`. Ghost matches it case-insensitively " +
					"(`go` finds `Go`). Omit it to only list the names.",
				Optional:   true,
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"names": schema.ListAttribute{
				MarkdownDescription: "The name of every template, in Ghost's order (sorted byte-wise, so upper case " +
					"comes first, e.g. `Zig` before `bun`).",
				ElementType: types.StringType,
				Computed:    true,
			},
			"template": schema.SingleNestedAttribute{
				MarkdownDescription: "The template named by `name`; null when `name` isn't set.",
				Computed:            true,
				Attributes: map[string]schema.Attribute{
					"name": schema.StringAttribute{
						MarkdownDescription: "Ghost's spelling of the template's name, which may differ in case from `name`.",
						Computed:            true,
					},
					"source": schema.StringAttribute{
						MarkdownDescription: "The template's content: exactly the `.gitignore` a repository created with " +
							"this `gitignore_template` (and `auto_init`) starts with.",
						Computed: true,
					},
				},
			},
		},
	}
}

func (d *GitignoreTemplatesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *GitignoreTemplatesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data gitignoreTemplatesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	names, err := d.client.ListGitignoreTemplates(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error listing .gitignore templates", err.Error())
		return
	}

	var tmpl *client.GitignoreTemplate
	if !data.Name.IsNull() {
		tmpl, err = d.client.GetGitignoreTemplate(ctx, data.Name.ValueString())
		if client.IsNotFound(err) {
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Gitignore template not found",
				fmt.Sprintf("Ghost has no .gitignore template named %q. Names are matched case-insensitively; "+
					"leave name unset to list every template in names.", data.Name.ValueString()))
			return
		}
		if err != nil {
			resp.Diagnostics.AddError("Error reading .gitignore template", err.Error())
			return
		}
	}

	resp.Diagnostics.Append(data.fromRemote(names, tmpl)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// fromRemote sets every computed attribute of m from the template names and
// the template looked up by name (nil when name isn't set).
func (m *gitignoreTemplatesDataSourceModel) fromRemote(names []string, t *client.GitignoreTemplate) diag.Diagnostics {
	if names == nil {
		names = []string{}
	}

	var diags diag.Diagnostics
	m.ID = types.StringValue(gitignoreTemplatesID)
	m.Names, diags = types.ListValueFrom(context.Background(), types.StringType, names)

	m.Template = types.ObjectNull(gitignoreTemplateAttrTypes)
	if t != nil {
		m.Template = types.ObjectValueMust(gitignoreTemplateAttrTypes, map[string]attr.Value{
			"name":   types.StringValue(t.Name),
			"source": types.StringValue(t.Source),
		})
	}

	return diags
}
