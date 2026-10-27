package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

var (
	_ resource.Resource                = &TeamRepositoryResource{}
	_ resource.ResourceWithConfigure   = &TeamRepositoryResource{}
	_ resource.ResourceWithImportState = &TeamRepositoryResource{}
	_ resource.ResourceWithModifyPlan  = &TeamRepositoryResource{}
)

// repositoryRoles are the roles Ghost accepts, from least to most access.
var repositoryRoles = []string{"read", "triage", "write", "maintain", "admin"}

// NewTeamRepositoryResource returns the ghost_team_repository resource.
func NewTeamRepositoryResource() resource.Resource {
	return &TeamRepositoryResource{}
}

// TeamRepositoryResource manages a team's role on an organization's repository.
type TeamRepositoryResource struct {
	client *client.Client
}

// teamRepositoryResourceModel maps the ghost_team_repository schema.
type teamRepositoryResourceModel struct {
	ID           types.String `tfsdk:"id"`
	Repository   types.String `tfsdk:"repository"`
	RepositoryID types.Int64  `tfsdk:"repository_id"`
	TeamID       types.String `tfsdk:"team_id"`
	TeamSlug     types.String `tfsdk:"team_slug"`
	Role         types.String `tfsdk:"role"`
}

func (r *TeamRepositoryResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_team_repository"
}

func (r *TeamRepositoryResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	teamChoice := []validator.String{
		stringvalidator.ExactlyOneOf(path.MatchRoot("team_id"), path.MatchRoot("team_slug")),
		stringvalidator.LengthAtLeast(1),
		stringvalidator.RegexMatches(noColonPattern, `must not contain ":"`),
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Gives a team a role on a repository owned by the team's organization. Every member of the team " +
			"gets the role on the repository. Teams can't be created with an API token, so the team must already exist: " +
			"look it up with the `ghost_team` or `ghost_organization_teams` data source, or name it with `team_slug`. " +
			"You must be an admin of the repository. Repositories owned by a user can't be shared with teams.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The repository's full name and the team ID, `owner/slug:team_id`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"repository": schema.StringAttribute{
				MarkdownDescription: "The repository's full name, `organization/slug`, e.g. `ghost_repository.example.full_name`. The repository must belong to the team's organization. Renaming the repository updates this in place; pointing it at a different repository forces a new resource.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(fullNamePattern, `must be "owner/slug", e.g. "acme/my-repo"`),
				},
			},
			"repository_id": schema.Int64Attribute{
				MarkdownDescription: "The numeric ID of `repository`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"team_id": schema.StringAttribute{
				MarkdownDescription: "The team's ID, e.g. `data.ghost_team.example.id`. It never changes, even if the team is renamed. Exactly one of `team_id` and `team_slug` must be set; when `team_slug` is set, this is the ID it names. Changing the team forces a new resource.",
				Optional:            true,
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Validators:          teamChoice,
			},
			"team_slug": schema.StringAttribute{
				MarkdownDescription: "The team's slug, matched case-insensitively. Exactly one of `team_id` and `team_slug` must be set; when `team_id` is set, this is the team's current slug. A slug changes when the team is renamed, so prefer `team_id` where you can. Changing it to another team's slug forces a new resource; changing it to the same team's new slug doesn't.",
				Optional:            true,
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				Validators:          teamChoice,
			},
			"role": schema.StringAttribute{
				MarkdownDescription: "The role every member of the team gets on the repository: `read`, `triage`, `write`, `maintain` or `admin`. Changing it updates the resource in place.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.OneOf(repositoryRoles...)},
			},
		},
	}
}

func (r *TeamRepositoryResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data",
			fmt.Sprintf("Expected *client.Client, got %T. Please report this issue to the provider developers.", req.ProviderData))
		return
	}

	r.client = c
}

// ModifyPlan applies a repository change in place when it is the same
// repository under a new name, and forces a new resource otherwise. A team
// change forces a new resource unless it names the same team.
func (r *TeamRepositoryResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}

	var plan, state, config teamRepositoryResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	replace := false
	if !plan.Repository.Equal(state.Repository) {
		if !plan.Repository.IsUnknown() {
			repo, err := lookupRepository(ctx, r.client, plan.Repository.ValueString())
			if client.IsNotFound(err) || (err == nil && !sameRepository(state.RepositoryID, repo)) {
				resp.RequiresReplace.Append(path.Root("repository"))
				replace = true
			} else if err != nil {
				resp.Diagnostics.AddAttributeError(path.Root("repository"), "Error reading repository", err.Error())
				return
			}
		}
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("id"), types.StringUnknown())...)
	}

	switch {
	case !config.TeamID.IsNull():
		if config.TeamID.Equal(state.TeamID) {
			return
		}
		resp.RequiresReplace.Append(path.Root("team_id"))
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("team_slug"), types.StringUnknown())...)

	case !config.TeamSlug.IsNull():
		if !config.TeamSlug.IsUnknown() && strings.EqualFold(config.TeamSlug.ValueString(), state.TeamSlug.ValueString()) {
			return
		}
		teamID := types.StringUnknown()
		if !config.TeamSlug.IsUnknown() && !plan.Repository.IsUnknown() {
			org, _, err := client.SplitFullName(plan.Repository.ValueString())
			if err != nil {
				return
			}
			team, diags := r.findTeam(ctx, org, "", config.TeamSlug.ValueString())
			resp.Diagnostics.Append(diags...)
			if resp.Diagnostics.HasError() {
				return
			}
			if team.ID == state.TeamID.ValueString() {
				// The same team under a new slug.
				return
			}
			teamID = types.StringValue(team.ID)
		}
		resp.RequiresReplace.Append(path.Root("team_slug"))
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("team_id"), teamID)...)

	default:
		return
	}

	if !replace {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("id"), types.StringUnknown())...)
	}
}

func (r *TeamRepositoryResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan teamRepositoryResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	owner, repo, err := client.SplitFullName(plan.Repository.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("repository"), "Invalid repository", err.Error())
		return
	}

	repository, err := r.client.GetRepository(ctx, owner, repo)
	if client.IsNotFound(err) {
		resp.Diagnostics.AddAttributeError(path.Root("repository"), "Repository not found",
			fmt.Sprintf("Repository %s not found. Owner and slug are case-sensitive.", plan.Repository.ValueString()))
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading repository", err.Error())
		return
	}
	plan.RepositoryID = types.Int64Value(repository.ID)

	team, diags := r.findTeam(ctx, owner, plan.TeamID.ValueString(), plan.TeamSlug.ValueString())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.TeamID = types.StringValue(team.ID)
	plan.TeamSlug = slugFromRemote(plan.TeamSlug, team.Slug)

	resp.Diagnostics.Append(r.setRole(ctx, owner, repo, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	plan.ID = types.StringValue(teamRepositoryID(plan.Repository.ValueString(), team.ID))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *TeamRepositoryResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state teamRepositoryResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	owner, repo, err := client.SplitFullName(state.Repository.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("repository"), "Invalid repository", err.Error())
		return
	}

	teams, err := r.client.ListRepositoryTeams(ctx, owner, repo)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading the repository's teams", err.Error())
		return
	}
	role := repositoryTeamRole(teams, state.TeamID.ValueString())
	if role == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	// The repository's team list has no slugs.
	orgTeams, err := r.client.ListOrganizationTeams(ctx, owner)
	if err != nil {
		resp.Diagnostics.AddError("Error reading the organization's teams", err.Error())
		return
	}
	for _, t := range orgTeams {
		if t.ID == state.TeamID.ValueString() {
			state.TeamSlug = slugFromRemote(state.TeamSlug, t.Slug)
		}
	}

	// An imported resource has no repository_id yet.
	if state.RepositoryID.IsNull() {
		repository, err := r.client.GetRepository(ctx, owner, repo)
		if err != nil {
			resp.Diagnostics.AddError("Error reading repository", err.Error())
			return
		}
		state.RepositoryID = types.Int64Value(repository.ID)
	}

	state.Role = types.StringValue(*role)
	state.ID = types.StringValue(teamRepositoryID(state.Repository.ValueString(), state.TeamID.ValueString()))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *TeamRepositoryResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state teamRepositoryResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	owner, repo, err := client.SplitFullName(plan.Repository.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("repository"), "Invalid repository", err.Error())
		return
	}

	if !plan.Repository.Equal(state.Repository) {
		repository, err := r.client.GetRepository(ctx, owner, repo)
		if err != nil {
			resp.Diagnostics.AddError("Error reading repository", err.Error())
			return
		}
		if !sameRepository(state.RepositoryID, repository) {
			resp.Diagnostics.AddAttributeError(path.Root("repository"), "Team access can't be moved",
				fmt.Sprintf("%s is a different repository from the one this resource was created for. "+
					"Replace it with terraform apply -replace=<address>.", plan.Repository.ValueString()))
			return
		}
		plan.RepositoryID = types.Int64Value(repository.ID)
	}

	// ModifyPlan forces a new resource for a different team, so the team is
	// the one in state.
	plan.TeamID = state.TeamID
	if plan.TeamSlug.IsUnknown() {
		plan.TeamSlug = state.TeamSlug
	}

	if !plan.Role.Equal(state.Role) {
		resp.Diagnostics.Append(r.setRole(ctx, owner, repo, &plan)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	plan.ID = types.StringValue(teamRepositoryID(plan.Repository.ValueString(), plan.TeamID.ValueString()))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *TeamRepositoryResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state teamRepositoryResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	owner, repo, err := client.SplitFullName(state.Repository.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("repository"), "Invalid repository", err.Error())
		return
	}

	// Ghost returns 404 when the team already has no access.
	err = r.client.RemoveRepositoryTeam(ctx, owner, repo, state.TeamID.ValueString())
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error removing the team's access", err.Error())
	}
}

// ImportState takes "owner/repo:team", where team is the team's ID or slug.
func (r *TeamRepositoryResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	repository, team, err := parseTeamRepositoryID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	owner, _, _ := client.SplitFullName(repository)

	teams, err := r.client.ListOrganizationTeams(ctx, owner)
	if err != nil {
		resp.Diagnostics.AddError("Error reading the organization's teams", organizationTeamsError(owner, err))
		return
	}
	found := teamByIDOrSlug(teams, team)
	if found == nil {
		resp.Diagnostics.AddError("Team not found",
			fmt.Sprintf("Organization %s has no team with the ID or slug %q.", owner, team))
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), teamRepositoryID(repository, found.ID))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("repository"), repository)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("team_id"), found.ID)...)
}

// findTeam finds the organization's team with the given ID or, when id is
// empty, the given slug.
func (r *TeamRepositoryResource) findTeam(ctx context.Context, org, id, slug string) (*client.TeamSummary, diag.Diagnostics) {
	var diags diag.Diagnostics

	teams, err := r.client.ListOrganizationTeams(ctx, org)
	if err != nil {
		diags.AddAttributeError(path.Root("repository"), "Error reading the organization's teams", organizationTeamsError(org, err))
		return nil, diags
	}

	attr, value := "team_id", id
	if id == "" {
		attr, value = "team_slug", slug
	}
	for i, t := range teams {
		if (id != "" && t.ID == id) || (id == "" && strings.EqualFold(t.Slug, slug)) {
			return &teams[i], diags
		}
	}

	diags.AddAttributeError(path.Root(attr), "Team not found",
		fmt.Sprintf("Organization %s has no team with the %s %q. Team IDs are case-sensitive; list the teams with the "+
			"ghost_organization_teams data source.", org, strings.TrimPrefix(attr, "team_"), value))
	return nil, diags
}

// setRole gives the team in m its role on owner/repo.
func (r *TeamRepositoryResource) setRole(ctx context.Context, owner, repo string, m *teamRepositoryResourceModel) diag.Diagnostics {
	var diags diag.Diagnostics

	err := r.client.SetRepositoryTeamRole(ctx, owner, repo, m.TeamID.ValueString(), m.Role.ValueString())
	var apiErr *client.APIError
	switch {
	case err == nil:
	case client.IsNotFound(err):
		diags.AddError("Error giving the team access",
			fmt.Sprintf("Team %s or repository %s not found: %s", m.TeamID.ValueString(), m.Repository.ValueString(), err))
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusBadRequest:
		diags.AddAttributeError(path.Root("repository"), "Error giving the team access",
			fmt.Sprintf("Ghost rejected the request: %s. Only repositories owned by an organization can be shared with "+
				"its teams.", apiErr.Message))
	default:
		diags.AddError("Error giving the team access", err.Error())
	}

	return diags
}

// organizationTeamsError explains a failure to list an organization's teams.
// Ghost returns 404 for a user, an unknown organization, and an organization
// the token's user isn't a member of.
func organizationTeamsError(org string, err error) string {
	if client.IsNotFound(err) {
		return fmt.Sprintf("Organization %s not found, or you're not a member of it. Only repositories owned by an "+
			"organization can be shared with teams, and a user's repositories can't. (%s)", org, err)
	}

	return err.Error()
}

// repositoryTeamRole returns the role of the team with the given ID, or nil
// when it has no access.
func repositoryTeamRole(teams []client.RepositoryTeam, id string) *string {
	for _, t := range teams {
		if t.ID == id {
			return t.Role
		}
	}

	return nil
}

// teamByIDOrSlug finds a team by its ID, or failing that by its slug, which
// is matched case-insensitively.
func teamByIDOrSlug(teams []client.TeamSummary, team string) *client.TeamSummary {
	for i, t := range teams {
		if t.ID == team {
			return &teams[i]
		}
	}
	for i, t := range teams {
		if strings.EqualFold(t.Slug, team) {
			return &teams[i]
		}
	}

	return nil
}

// slugFromRemote keeps the configured spelling of a slug when it names the
// same team.
func slugFromRemote(current types.String, remote string) types.String {
	if !current.IsNull() && !current.IsUnknown() && strings.EqualFold(current.ValueString(), remote) {
		return current
	}

	return types.StringValue(remote)
}

// teamRepositoryID builds the "owner/slug:team_id" ID.
func teamRepositoryID(repository, teamID string) string {
	return repository + ":" + teamID
}

// parseTeamRepositoryID parses an "owner/slug:team" import ID.
func parseTeamRepositoryID(id string) (repository, team string, err error) {
	repository, team, ok := strings.Cut(id, ":")
	if _, _, splitErr := client.SplitFullName(repository); !ok || team == "" || strings.ContainsAny(team, ":/") || splitErr != nil {
		return "", "", fmt.Errorf("import ID must be \"owner/repo:team_id\" or \"owner/repo:team_slug\", e.g. \"acme/my-repo:core\", got %q", id)
	}

	return repository, team, nil
}
