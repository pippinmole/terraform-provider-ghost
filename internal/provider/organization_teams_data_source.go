package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

var (
	_ datasource.DataSource              = &OrganizationTeamsDataSource{}
	_ datasource.DataSourceWithConfigure = &OrganizationTeamsDataSource{}
)

// NewOrganizationTeamsDataSource returns the ghost_organization_teams data
// source.
func NewOrganizationTeamsDataSource() datasource.DataSource {
	return &OrganizationTeamsDataSource{}
}

// OrganizationTeamsDataSource lists the teams of a Ghost organization.
type OrganizationTeamsDataSource struct {
	client *client.Client
}

// organizationTeamsDataSourceModel maps the ghost_organization_teams data
// source schema.
type organizationTeamsDataSourceModel struct {
	ID           types.String `tfsdk:"id"`
	Organization types.String `tfsdk:"organization"`
	Teams        types.List   `tfsdk:"teams"`
}

func (d *OrganizationTeamsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization_teams"
}

func (d *OrganizationTeamsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists every team in an organization, with each team's member and repository counts. " +
			"Look a team up with the `ghost_team` data source for its members and repositories.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The organization slug, as configured.",
				Computed:            true,
			},
			"organization": schema.StringAttribute{
				MarkdownDescription: "The slug of the organization. Ghost matches it case-insensitively. The token's user must be a member of the organization: Ghost answers as if any other organization didn't exist.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"teams": schema.ListNestedAttribute{
				MarkdownDescription: "The organization's teams, sorted by slug. Empty when the organization has none.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							MarkdownDescription: "The team's ID. It never changes, and is what `team_id` arguments and the `id` argument of the `ghost_team` data source take.",
							Computed:            true,
						},
						"slug": schema.StringAttribute{
							MarkdownDescription: "The team's URL-safe name, used in URLs and `@org/team` mentions. It changes when the team is renamed.",
							Computed:            true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "The team's display name.",
							Computed:            true,
						},
						"member_count": schema.Int64Attribute{
							MarkdownDescription: "How many members the team has, maintainers included.",
							Computed:            true,
						},
						"repository_count": schema.Int64Attribute{
							MarkdownDescription: "How many organization repositories the team has a role on.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

func (d *OrganizationTeamsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *OrganizationTeamsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data organizationTeamsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := data.Organization.ValueString()
	teams, err := d.client.ListOrganizationTeams(ctx, org)
	if client.IsNotFound(err) {
		resp.Diagnostics.AddError("Organization not found",
			fmt.Sprintf("Can't list the teams of organization %q (%s). Check the organization slug, and that the token's user is a member of it: Ghost hides an organization's teams from non-members.", org, err))
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error listing organization teams", err.Error())
		return
	}

	resp.Diagnostics.Append(data.fromRemote(teams)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// fromRemote sets every computed attribute of m from teams.
func (m *organizationTeamsDataSourceModel) fromRemote(teams []client.TeamSummary) diag.Diagnostics {
	var diags diag.Diagnostics
	m.ID = m.Organization
	m.Teams, diags = teamSummariesValue(teams)

	return diags
}
