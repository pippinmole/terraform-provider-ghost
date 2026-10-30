package provider

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

var (
	_ datasource.DataSource              = &OrganizationDataSource{}
	_ datasource.DataSourceWithConfigure = &OrganizationDataSource{}
)

// NewOrganizationDataSource returns the ghost_organization data source.
func NewOrganizationDataSource() datasource.DataSource {
	return &OrganizationDataSource{}
}

// OrganizationDataSource reads the profile of a Ghost organization.
type OrganizationDataSource struct {
	client *client.Client
}

// organizationDataSourceModel maps the ghost_organization data source
// schema.
type organizationDataSourceModel struct {
	ID                    types.String `tfsdk:"id"`
	Slug                  types.String `tfsdk:"slug"`
	Name                  types.String `tfsdk:"name"`
	Logo                  types.String `tfsdk:"logo"`
	CreatedAt             types.String `tfsdk:"created_at"`
	Description           types.String `tfsdk:"description"`
	Website               types.String `tfsdk:"website"`
	Location              types.String `tfsdk:"location"`
	Email                 types.String `tfsdk:"email"`
	PublicRepositoryCount types.Int64  `tfsdk:"public_repository_count"`
	Members               types.List   `tfsdk:"members"`
	PinnedRepositories    types.List   `tfsdk:"pinned_repositories"`
	ViewerRole            types.String `tfsdk:"viewer_role"`
}

// organizationMemberAttrTypes is the object type of one element of an
// organization's members.
var organizationMemberAttrTypes = map[string]attr.Type{
	"username": types.StringType,
	"name":     types.StringType,
	"image":    types.StringType,
	"public":   types.BoolType,
}

// pinnedRepositoryAttrTypes is the object type of one element of an
// organization's pinned repositories.
var pinnedRepositoryAttrTypes = map[string]attr.Type{
	"slug":        types.StringType,
	"name":        types.StringType,
	"full_name":   types.StringType,
	"description": types.StringType,
	"visibility":  types.StringType,
}

func (d *OrganizationDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization"
}

func (d *OrganizationDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads an organization's public profile: its name, details, members and pinned repositories. " +
			"Anyone can read a profile, but only members see the private members and their own role. " +
			"Use `ghost_organization_teams` for its teams.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The organization's slug as Ghost spells it (lower case).",
				Computed:            true,
			},
			"slug": schema.StringAttribute{
				MarkdownDescription: "The slug of the organization to look up. Ghost matches it case-insensitively. The token's user doesn't need to be a member.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The organization's display name.",
				Computed:            true,
			},
			"logo": schema.StringAttribute{
				MarkdownDescription: "The URL of the organization's logo, null when it has none.",
				Computed:            true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "When the organization was created, as an RFC 3339 timestamp.",
				Computed:            true,
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "The organization's description, null when none is set.",
				Computed:            true,
			},
			"website": schema.StringAttribute{
				MarkdownDescription: "The organization's website, null when none is set.",
				Computed:            true,
			},
			"location": schema.StringAttribute{
				MarkdownDescription: "The organization's location, null when none is set.",
				Computed:            true,
			},
			"email": schema.StringAttribute{
				MarkdownDescription: "The organization's public contact email, null when none is set.",
				Computed:            true,
			},
			"public_repository_count": schema.Int64Attribute{
				MarkdownDescription: "How many public repositories the organization has. Private repositories aren't counted, even for members.",
				Computed:            true,
			},
			"members": schema.ListNestedAttribute{
				MarkdownDescription: "The organization's members, sorted by username. Members of the organization see every member; " +
					"anyone else sees only the members who made their membership public. Roles aren't included.",
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"username": schema.StringAttribute{
							MarkdownDescription: "The member's username.",
							Computed:            true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "The member's display name.",
							Computed:            true,
						},
						"image": schema.StringAttribute{
							MarkdownDescription: "The member's avatar URL, null when they have none.",
							Computed:            true,
						},
						"public": schema.BoolAttribute{
							MarkdownDescription: "Whether the member shows their membership outside the organization.",
							Computed:            true,
						},
					},
				},
			},
			"pinned_repositories": schema.ListNestedAttribute{
				MarkdownDescription: "The repositories pinned to the organization's profile, in pinned order. Only repositories the token's user can read are listed.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"slug": schema.StringAttribute{
							MarkdownDescription: "The repository's slug.",
							Computed:            true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "The repository's display name.",
							Computed:            true,
						},
						"full_name": schema.StringAttribute{
							MarkdownDescription: "The repository's full name, `organization/slug`. Pass it to the `repository` argument of other resources.",
							Computed:            true,
						},
						"description": schema.StringAttribute{
							MarkdownDescription: "The repository's description, null when none is set.",
							Computed:            true,
						},
						"visibility": schema.StringAttribute{
							MarkdownDescription: "Either `public` or `private`.",
							Computed:            true,
						},
					},
				},
			},
			"viewer_role": schema.StringAttribute{
				MarkdownDescription: "The token user's role in the organization: `member`, `admin` or `owner`. " +
					"Null when the user isn't a member, or holds only custom roles.",
				Computed: true,
			},
		},
	}
}

func (d *OrganizationDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *OrganizationDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data organizationDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	slug := data.Slug.ValueString()
	org, err := d.client.GetOrganization(ctx, slug)
	if client.IsNotFound(err) {
		resp.Diagnostics.AddError("Organization not found",
			fmt.Sprintf("Ghost has no organization %q (%s). Check the slug: a user's login isn't an organization; use the `ghost_user` data source for users.", slug, err))
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading organization", err.Error())
		return
	}

	resp.Diagnostics.Append(data.fromRemote(org)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// fromRemote sets every computed attribute of m from o.
func (m *organizationDataSourceModel) fromRemote(o *client.Organization) diag.Diagnostics {
	var diags, d diag.Diagnostics
	m.ID = types.StringValue(o.Slug)
	m.Name = types.StringValue(o.Name)
	m.Logo = types.StringPointerValue(o.Logo)
	m.CreatedAt = types.StringValue(o.CreatedAt)
	m.Description = types.StringPointerValue(o.Description)
	m.Website = types.StringPointerValue(o.Website)
	m.Location = types.StringPointerValue(o.Location)
	m.Email = types.StringPointerValue(o.Email)
	m.PublicRepositoryCount = types.Int64Value(o.RepositoryCount)
	m.ViewerRole = types.StringPointerValue(o.ViewerRole)

	m.Members, d = organizationMembersValue(o.Members)
	diags.Append(d...)
	m.PinnedRepositories, d = pinnedRepositoriesValue(o.Slug, o.Pinned)
	diags.Append(d...)

	return diags
}

// organizationMembersValue converts members to a list sorted by username.
func organizationMembersValue(members []client.OrganizationMember) (types.List, diag.Diagnostics) {
	members = slices.Clone(members)
	slices.SortFunc(members, func(a, b client.OrganizationMember) int { return cmp.Compare(a.Username, b.Username) })

	elems := make([]attr.Value, 0, len(members))
	for _, m := range members {
		elems = append(elems, types.ObjectValueMust(organizationMemberAttrTypes, map[string]attr.Value{
			"username": types.StringValue(m.Username),
			"name":     types.StringValue(m.Name),
			"image":    types.StringPointerValue(m.Image),
			"public":   types.BoolValue(m.Public),
		}))
	}

	return types.ListValue(types.ObjectType{AttrTypes: organizationMemberAttrTypes}, elems)
}

// pinnedRepositoriesValue converts repos, which belong to the organization
// org, to a list in pinned order.
func pinnedRepositoriesValue(org string, repos []client.PinnedRepository) (types.List, diag.Diagnostics) {
	elems := make([]attr.Value, 0, len(repos))
	for _, r := range repos {
		elems = append(elems, types.ObjectValueMust(pinnedRepositoryAttrTypes, map[string]attr.Value{
			"slug":        types.StringValue(r.Slug),
			"name":        types.StringValue(r.Name),
			"full_name":   types.StringValue(org + "/" + r.Slug),
			"description": types.StringPointerValue(r.Description),
			"visibility":  types.StringValue(r.Visibility),
		}))
	}

	return types.ListValue(types.ObjectType{AttrTypes: pinnedRepositoryAttrTypes}, elems)
}
