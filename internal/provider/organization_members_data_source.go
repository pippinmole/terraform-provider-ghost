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
	_ datasource.DataSource              = &OrganizationMembersDataSource{}
	_ datasource.DataSourceWithConfigure = &OrganizationMembersDataSource{}
)

// NewOrganizationMembersDataSource returns the ghost_organization_members
// data source.
func NewOrganizationMembersDataSource() datasource.DataSource {
	return &OrganizationMembersDataSource{}
}

// OrganizationMembersDataSource lists the members, and optionally the
// outside collaborators, of a Ghost organization.
type OrganizationMembersDataSource struct {
	client *client.Client
}

// organizationMembersDataSourceModel maps the ghost_organization_members
// data source schema.
type organizationMembersDataSourceModel struct {
	ID                          types.String `tfsdk:"id"`
	Organization                types.String `tfsdk:"organization"`
	IncludeOutsideCollaborators types.Bool   `tfsdk:"include_outside_collaborators"`
	Members                     types.List   `tfsdk:"members"`
	Usernames                   types.List   `tfsdk:"usernames"`
	ViewerRole                  types.String `tfsdk:"viewer_role"`
	OutsideCollaborators        types.List   `tfsdk:"outside_collaborators"`
}

// outsideCollaboratorRepositoryAttrTypes is the object type of one
// repository of an outside collaborator.
var outsideCollaboratorRepositoryAttrTypes = map[string]attr.Type{
	"slug":      types.StringType,
	"full_name": types.StringType,
	"role":      types.StringType,
	"pending":   types.BoolType,
}

// outsideCollaboratorAttrTypes is the object type of one outside
// collaborator.
var outsideCollaboratorAttrTypes = map[string]attr.Type{
	"username":     types.StringType,
	"name":         types.StringType,
	"image":        types.StringType,
	"repositories": types.ListType{ElemType: types.ObjectType{AttrTypes: outsideCollaboratorRepositoryAttrTypes}},
}

func (d *OrganizationMembersDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization_members"
}

func (d *OrganizationMembersDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the members of an organization and, for its admins, its outside collaborators. " +
			"Members of the organization see every member; anyone else sees only the members who made their membership public. " +
			"Ghost doesn't return members' roles (`viewer_role` is the token user's own), and the list isn't paginated.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The organization's slug as Ghost spells it (lower case).",
				Computed:            true,
			},
			"organization": schema.StringAttribute{
				MarkdownDescription: "The slug of the organization. Ghost matches it case-insensitively. The token's user doesn't need to be a member, unless `include_outside_collaborators` is set.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"include_outside_collaborators": schema.BoolAttribute{
				MarkdownDescription: "Also list the organization's outside collaborators into `outside_collaborators`. " +
					"Only admins of the organization can list them, so reading fails for anyone else. Defaults to `false`.",
				Optional: true,
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
			"usernames": schema.ListAttribute{
				MarkdownDescription: "The usernames of `members`, in the same order.",
				Computed:            true,
				ElementType:         types.StringType,
			},
			"viewer_role": schema.StringAttribute{
				MarkdownDescription: "The token user's role in the organization: `member`, `admin` or `owner`. " +
					"Null when the user isn't a member (so `members` lists only public members), or holds only custom roles.",
				Computed: true,
			},
			"outside_collaborators": schema.ListNestedAttribute{
				MarkdownDescription: "With `include_outside_collaborators`, the people who aren't members but have, or are invited to, a role on the organization's repositories, " +
					"sorted by username. Members invited to a repository aren't listed. Null unless `include_outside_collaborators` is `true`.",
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"username": schema.StringAttribute{
							MarkdownDescription: "The collaborator's username.",
							Computed:            true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "The collaborator's display name.",
							Computed:            true,
						},
						"image": schema.StringAttribute{
							MarkdownDescription: "The collaborator's avatar URL, null when they have none.",
							Computed:            true,
						},
						"repositories": schema.ListNestedAttribute{
							MarkdownDescription: "The organization's repositories the collaborator has, or is invited to, a role on, sorted by slug.",
							Computed:            true,
							NestedObject: schema.NestedAttributeObject{
								Attributes: map[string]schema.Attribute{
									"slug": schema.StringAttribute{
										MarkdownDescription: "The repository's slug.",
										Computed:            true,
									},
									"full_name": schema.StringAttribute{
										MarkdownDescription: "The repository's full name, `organization/slug`. Pass it to the `repository` argument of other resources.",
										Computed:            true,
									},
									"role": schema.StringAttribute{
										MarkdownDescription: "The collaborator's role on the repository: `read`, `triage`, `write`, `maintain` or `admin`.",
										Computed:            true,
									},
									"pending": schema.BoolAttribute{
										MarkdownDescription: "Whether the user was invited and hasn't accepted yet.",
										Computed:            true,
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func (d *OrganizationMembersDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *OrganizationMembersDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data organizationMembersDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	slug := data.Organization.ValueString()
	org, err := d.client.GetOrganization(ctx, slug)
	if client.IsNotFound(err) {
		resp.Diagnostics.AddError("Organization not found",
			fmt.Sprintf("Ghost has no organization %q (%s). Check the slug: a user's login isn't an organization.", slug, err))
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading organization", err.Error())
		return
	}

	var outside []client.OutsideCollaborator
	if data.IncludeOutsideCollaborators.ValueBool() {
		outside, err = d.client.ListOutsideCollaborators(ctx, org.Slug)
		if client.IsForbidden(err) || client.IsNotFound(err) {
			resp.Diagnostics.AddError("Cannot list outside collaborators", outsideCollaboratorsDeniedDetail(org, err))
			return
		}
		if err != nil {
			resp.Diagnostics.AddError("Error listing outside collaborators", err.Error())
			return
		}
	}

	resp.Diagnostics.Append(data.fromRemote(org, outside)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// outsideCollaboratorsDeniedDetail explains a 403 or 404 from the outside
// collaborators endpoint of org, whose profile was read. Ghost answers 404
// "Organization not found" to non-members, so the token user's role says
// which it was.
func outsideCollaboratorsDeniedDetail(org *client.Organization, err error) string {
	who := "the token's user isn't a member of it"
	if org.ViewerRole != nil {
		who = fmt.Sprintf("the token's user is a %s of it", *org.ViewerRole)
	}

	return fmt.Sprintf("Only admins of the organization %q can list its outside collaborators, and %s (%s). "+
		"Remove `include_outside_collaborators`, or use a token of one of its admins.", org.Slug, who, err)
}

// fromRemote sets every computed attribute of m from the profile o and,
// when they were requested, its outside collaborators.
func (m *organizationMembersDataSourceModel) fromRemote(o *client.Organization, outside []client.OutsideCollaborator) diag.Diagnostics {
	var diags, d diag.Diagnostics
	m.ID = types.StringValue(o.Slug)
	m.ViewerRole = types.StringPointerValue(o.ViewerRole)

	m.Members, d = organizationMembersValue(o.Members)
	diags.Append(d...)

	// The same order as organizationMembersValue sorts the members in.
	names := make([]string, 0, len(o.Members))
	for _, member := range o.Members {
		names = append(names, member.Username)
	}
	slices.Sort(names)
	usernames := make([]attr.Value, 0, len(names))
	for _, n := range names {
		usernames = append(usernames, types.StringValue(n))
	}
	m.Usernames, d = types.ListValue(types.StringType, usernames)
	diags.Append(d...)

	if m.IncludeOutsideCollaborators.ValueBool() {
		m.OutsideCollaborators, d = outsideCollaboratorsValue(o.Slug, outside)
		diags.Append(d...)
	} else {
		m.OutsideCollaborators = types.ListNull(types.ObjectType{AttrTypes: outsideCollaboratorAttrTypes})
	}

	return diags
}

// outsideCollaboratorsValue converts the outside collaborators of the
// organization org to a list sorted by username, with each one's
// repositories sorted by slug.
func outsideCollaboratorsValue(org string, collaborators []client.OutsideCollaborator) (types.List, diag.Diagnostics) {
	var diags diag.Diagnostics
	collaborators = slices.Clone(collaborators)
	slices.SortFunc(collaborators, func(a, b client.OutsideCollaborator) int { return cmp.Compare(a.Username, b.Username) })

	repoType := types.ObjectType{AttrTypes: outsideCollaboratorRepositoryAttrTypes}
	elems := make([]attr.Value, 0, len(collaborators))
	for _, c := range collaborators {
		repos := slices.Clone(c.Repositories)
		slices.SortFunc(repos, func(a, b client.OutsideCollaboratorRepository) int { return cmp.Compare(a.Slug, b.Slug) })

		repoElems := make([]attr.Value, 0, len(repos))
		for _, r := range repos {
			repoElems = append(repoElems, types.ObjectValueMust(outsideCollaboratorRepositoryAttrTypes, map[string]attr.Value{
				"slug":      types.StringValue(r.Slug),
				"full_name": types.StringValue(org + "/" + r.Slug),
				"role":      types.StringValue(r.Role),
				"pending":   types.BoolValue(r.Pending),
			}))
		}
		repoList, d := types.ListValue(repoType, repoElems)
		diags.Append(d...)

		elems = append(elems, types.ObjectValueMust(outsideCollaboratorAttrTypes, map[string]attr.Value{
			"username":     types.StringValue(c.Username),
			"name":         types.StringValue(c.Name),
			"image":        types.StringPointerValue(c.Image),
			"repositories": repoList,
		}))
	}

	list, d := types.ListValue(types.ObjectType{AttrTypes: outsideCollaboratorAttrTypes}, elems)
	diags.Append(d...)

	return list, diags
}
