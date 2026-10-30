package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

var (
	_ datasource.DataSource                     = &TeamDataSource{}
	_ datasource.DataSourceWithConfigure        = &TeamDataSource{}
	_ datasource.DataSourceWithConfigValidators = &TeamDataSource{}
)

// NewTeamDataSource returns the ghost_team data source.
func NewTeamDataSource() datasource.DataSource {
	return &TeamDataSource{}
}

// TeamDataSource looks up a team in a Ghost organization.
type TeamDataSource struct {
	client *client.Client
}

// teamDataSourceModel maps the ghost_team data source schema.
type teamDataSourceModel struct {
	ID              types.String `tfsdk:"id"`
	Organization    types.String `tfsdk:"organization"`
	Slug            types.String `tfsdk:"slug"`
	Name            types.String `tfsdk:"name"`
	Members         types.List   `tfsdk:"members"`
	Maintainers     types.List   `tfsdk:"maintainers"`
	Repositories    types.List   `tfsdk:"repositories"`
	ViewerCanManage types.Bool   `tfsdk:"viewer_can_manage"`
}

func (d *TeamDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_team"
}

func (d *TeamDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	nonEmpty := []validator.String{stringvalidator.LengthAtLeast(1)}

	attrs := map[string]schema.Attribute{
		"organization": schema.StringAttribute{
			MarkdownDescription: "The slug of the organization the team belongs to. Ghost matches it case-insensitively. The token's user must be a member of the organization.",
			Required:            true,
			Validators:          nonEmpty,
		},
		"id": schema.StringAttribute{
			MarkdownDescription: "The team's ID. It never changes, so prefer it over `slug` to keep finding a team that may be renamed. It is what `team_id` arguments, such as `ghost_repository_ruleset` bypass actors, take. Set exactly one of `id` and `slug`.",
			Optional:            true,
			Computed:            true,
			Validators:          nonEmpty,
		},
		"slug": schema.StringAttribute{
			MarkdownDescription: "The team's URL-safe name, used in URLs and `@org/team` mentions. Ghost matches it case-insensitively. It changes when the team is renamed, so a lookup by `slug` breaks on rename; look up by `id` to avoid that. Set exactly one of `id` and `slug`.",
			Optional:            true,
			Computed:            true,
			Validators:          nonEmpty,
		},
		"name": schema.StringAttribute{
			MarkdownDescription: "The team's display name.",
			Computed:            true,
		},
		"maintainers": schema.ListAttribute{
			MarkdownDescription: "The usernames of the team's maintainers, sorted.",
			ElementType:         types.StringType,
			Computed:            true,
		},
		"members":      teamMembersAttribute(),
		"repositories": teamRepositoriesAttribute(),
		"viewer_can_manage": schema.BoolAttribute{
			MarkdownDescription: "Whether the token's user may change the team's members: an organization admin or one of the team's maintainers.",
			Computed:            true,
		},
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a team in an organization, by `slug` or by `id`, with its members and the repositories it can reach. " +
			"Teams can't be created with a personal access token, so there is no `ghost_team` resource.",
		Attributes: attrs,
	}
}

func (d *TeamDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("slug")),
	}
}

func (d *TeamDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *TeamDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data teamDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := data.Organization.ValueString()
	var (
		team   *client.Team
		err    error
		lookup string
	)
	if !data.ID.IsNull() {
		lookup = fmt.Sprintf("with ID %q", data.ID.ValueString())
		team, err = d.client.GetTeamByID(ctx, org, data.ID.ValueString())
	} else {
		lookup = fmt.Sprintf("with slug %q", data.Slug.ValueString())
		team, err = d.client.GetTeam(ctx, org, data.Slug.ValueString())
	}
	if client.IsNotFound(err) {
		resp.Diagnostics.AddError("Team not found",
			fmt.Sprintf("Found no team %s in organization %q (%s). Check the organization slug, and that `slug` is the team's slug rather than its display name or ID.", lookup, org, err))
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading team", err.Error())
		return
	}

	resp.Diagnostics.Append(data.fromRemote(org, team)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// fromRemote sets every computed attribute of m from t, a team in the
// organization org.
func (m *teamDataSourceModel) fromRemote(org string, t *client.Team) diag.Diagnostics {
	var diags, d diag.Diagnostics

	m.ID = types.StringValue(t.ID)
	m.Slug = types.StringValue(t.Slug)
	m.Name = types.StringValue(t.Name)
	m.ViewerCanManage = types.BoolValue(t.ViewerCanManage)

	m.Members, d = teamMembersValue(t.Members)
	diags.Append(d...)
	m.Maintainers, d = types.ListValueFrom(context.Background(), types.StringType, teamMaintainers(t.Members))
	diags.Append(d...)
	m.Repositories, d = teamRepositoriesValue(org, t.Repositories)
	diags.Append(d...)

	return diags
}
