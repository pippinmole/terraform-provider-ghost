package provider

import (
	"cmp"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

// organizationTeamAttrTypes is the object type of one element of the teams of
// ghost_organization_teams.
var organizationTeamAttrTypes = map[string]attr.Type{
	"id":               types.StringType,
	"slug":             types.StringType,
	"name":             types.StringType,
	"member_count":     types.Int64Type,
	"repository_count": types.Int64Type,
}

// teamMemberAttrTypes is the object type of one element of a team's members.
var teamMemberAttrTypes = map[string]attr.Type{
	"username":   types.StringType,
	"name":       types.StringType,
	"image":      types.StringType,
	"maintainer": types.BoolType,
}

// teamRepositoryAttrTypes is the object type of one element of a team's
// repositories.
var teamRepositoryAttrTypes = map[string]attr.Type{
	"slug":       types.StringType,
	"name":       types.StringType,
	"full_name":  types.StringType,
	"visibility": types.StringType,
	"role":       types.StringType,
}

// teamMembersAttribute returns the schema of a team's members.
func teamMembersAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		MarkdownDescription: "The team's members, sorted by username.",
		Computed:            true,
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
				"maintainer": schema.BoolAttribute{
					MarkdownDescription: "Whether the member is a team maintainer, who may manage the team's members.",
					Computed:            true,
				},
			},
		},
	}
}

// teamRepositoriesAttribute returns the schema of the repositories a team
// can reach.
func teamRepositoriesAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		MarkdownDescription: "The organization repositories the team has a role on, sorted by slug.",
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
					MarkdownDescription: "The repository's full name, `organization/slug`, with `organization` spelt as configured. Pass it to the `repository` argument of other resources.",
					Computed:            true,
				},
				"visibility": schema.StringAttribute{
					MarkdownDescription: "Either `public` or `private`.",
					Computed:            true,
				},
				"role": schema.StringAttribute{
					MarkdownDescription: "The team's role on the repository: `read`, `triage`, `write`, `maintain` or `admin`.",
					Computed:            true,
				},
			},
		},
	}
}

// teamMembersValue converts members to a list sorted by username.
func teamMembersValue(members []client.TeamMember) (types.List, diag.Diagnostics) {
	members = slices.Clone(members)
	slices.SortFunc(members, func(a, b client.TeamMember) int { return cmp.Compare(a.Username, b.Username) })

	elems := make([]attr.Value, 0, len(members))
	for _, m := range members {
		elems = append(elems, types.ObjectValueMust(teamMemberAttrTypes, map[string]attr.Value{
			"username":   types.StringValue(m.Username),
			"name":       types.StringValue(m.Name),
			"image":      types.StringPointerValue(m.Image),
			"maintainer": types.BoolValue(m.Maintainer),
		}))
	}

	return types.ListValue(types.ObjectType{AttrTypes: teamMemberAttrTypes}, elems)
}

// teamMaintainers returns the usernames of the team's maintainers, sorted.
func teamMaintainers(members []client.TeamMember) []string {
	out := []string{}
	for _, m := range members {
		if m.Maintainer {
			out = append(out, m.Username)
		}
	}
	slices.Sort(out)

	return out
}

// teamRepositoriesValue converts repos, which belong to the organization
// org, to a list sorted by slug.
func teamRepositoriesValue(org string, repos []client.TeamRepository) (types.List, diag.Diagnostics) {
	repos = slices.Clone(repos)
	slices.SortFunc(repos, func(a, b client.TeamRepository) int { return cmp.Compare(a.Slug, b.Slug) })

	elems := make([]attr.Value, 0, len(repos))
	for _, r := range repos {
		elems = append(elems, types.ObjectValueMust(teamRepositoryAttrTypes, map[string]attr.Value{
			"slug":       types.StringValue(r.Slug),
			"name":       types.StringValue(r.Name),
			"full_name":  types.StringValue(org + "/" + r.Slug),
			"visibility": types.StringValue(r.Visibility),
			"role":       types.StringValue(r.Role),
		}))
	}

	return types.ListValue(types.ObjectType{AttrTypes: teamRepositoryAttrTypes}, elems)
}

// teamSummariesValue converts teams to a list sorted by slug.
func teamSummariesValue(teams []client.TeamSummary) (types.List, diag.Diagnostics) {
	teams = slices.Clone(teams)
	slices.SortFunc(teams, func(a, b client.TeamSummary) int { return cmp.Compare(a.Slug, b.Slug) })

	elems := make([]attr.Value, 0, len(teams))
	for _, t := range teams {
		elems = append(elems, types.ObjectValueMust(organizationTeamAttrTypes, map[string]attr.Value{
			"id":               types.StringValue(t.ID),
			"slug":             types.StringValue(t.Slug),
			"name":             types.StringValue(t.Name),
			"member_count":     types.Int64Value(t.MemberCount),
			"repository_count": types.Int64Value(t.RepositoryCount),
		}))
	}

	return types.ListValue(types.ObjectType{AttrTypes: organizationTeamAttrTypes}, elems)
}
