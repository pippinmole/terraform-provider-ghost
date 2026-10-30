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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

var (
	_ resource.Resource                = &TeamMembershipResource{}
	_ resource.ResourceWithConfigure   = &TeamMembershipResource{}
	_ resource.ResourceWithImportState = &TeamMembershipResource{}
	_ resource.ResourceWithModifyPlan  = &TeamMembershipResource{}
)

// Team membership roles.
const (
	teamRoleMember     = "member"
	teamRoleMaintainer = "maintainer"
)

// NewTeamMembershipResource returns the ghost_team_membership resource.
func NewTeamMembershipResource() resource.Resource {
	return &TeamMembershipResource{}
}

// TeamMembershipResource manages a user's membership of a team, and whether
// they maintain it.
type TeamMembershipResource struct {
	client *client.Client
}

// teamMembershipResourceModel maps the ghost_team_membership schema.
type teamMembershipResourceModel struct {
	ID           types.String `tfsdk:"id"`
	Organization types.String `tfsdk:"organization"`
	TeamID       types.String `tfsdk:"team_id"`
	TeamSlug     types.String `tfsdk:"team_slug"`
	Username     types.String `tfsdk:"username"`
	Role         types.String `tfsdk:"role"`
}

func (r *TeamMembershipResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_team_membership"
}

func (r *TeamMembershipResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	teamChoice := []validator.String{
		stringvalidator.ExactlyOneOf(path.MatchRoot("team_id"), path.MatchRoot("team_slug")),
		stringvalidator.LengthAtLeast(1),
		stringvalidator.RegexMatches(usernamePattern, `must not contain ":" or "/"`),
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Adds a user to a team, as a member or a maintainer. **The user must already be a member of the " +
			"team's organization**: Ghost refuses anyone else, and organization invitations can't be sent with an API " +
			"token, so invite them in the web UI first. Teams can't be created with an API token either, so the team " +
			"must already exist: look it up with the `ghost_team` or `ghost_organization_teams` data source, or name it " +
			"with `team_slug`. You must be an organization admin, or a maintainer of the team to add and remove members; " +
			"only admins can change `role`. Adding someone who is already in the team adopts them and sets their role. " +
			"Destroying the resource removes the user from the team, not from the organization.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The organization, team ID and username, `organization:team_id:username`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"organization": schema.StringAttribute{
				MarkdownDescription: "The slug of the team's organization, matched case-insensitively and kept as configured. Changing it to another organization forces a new resource; changing only its case doesn't.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					stringvalidator.RegexMatches(usernamePattern, `must not contain ":" or "/"`),
				},
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
			"username": schema.StringAttribute{
				MarkdownDescription: "The username of the user to add, matched case-insensitively and kept as configured. They must already be a member of `organization`. Changing it to another user forces a new resource; changing only its case doesn't.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					stringvalidator.RegexMatches(usernamePattern, `must not contain ":" or "/"`),
				},
			},
			"role": schema.StringAttribute{
				MarkdownDescription: "`member` (the default) or `maintainer`. A maintainer can add and remove the team's members. Changing it updates the resource in place.",
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString(teamRoleMember),
				Validators:          []validator.String{stringvalidator.OneOf(teamRoleMember, teamRoleMaintainer)},
			},
		},
	}
}

func (r *TeamMembershipResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ModifyPlan forces a new resource for a different organization, team or
// user, but not for a change of case or for naming the same team another way.
func (r *TeamMembershipResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}

	var plan, state, config teamMembershipResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	idChanged := false
	if !plan.Organization.Equal(state.Organization) {
		idChanged = true
		if plan.Organization.IsUnknown() || !strings.EqualFold(plan.Organization.ValueString(), state.Organization.ValueString()) {
			resp.RequiresReplace.Append(path.Root("organization"))
		}
	}
	if !plan.Username.Equal(state.Username) {
		idChanged = true
		if plan.Username.IsUnknown() || !strings.EqualFold(plan.Username.ValueString(), state.Username.ValueString()) {
			resp.RequiresReplace.Append(path.Root("username"))
		}
	}

	switch {
	case !config.TeamID.IsNull():
		if !config.TeamID.Equal(state.TeamID) {
			idChanged = true
			resp.RequiresReplace.Append(path.Root("team_id"))
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("team_slug"), types.StringUnknown())...)
		}

	case !config.TeamSlug.IsNull():
		if config.TeamSlug.IsUnknown() || !strings.EqualFold(config.TeamSlug.ValueString(), state.TeamSlug.ValueString()) {
			teamID := types.StringUnknown()
			same := false
			if !config.TeamSlug.IsUnknown() && !plan.Organization.IsUnknown() {
				team, diags := findOrganizationTeam(ctx, r.client, plan.Organization.ValueString(), "", config.TeamSlug.ValueString())
				resp.Diagnostics.Append(diags...)
				if resp.Diagnostics.HasError() {
					return
				}
				// The same team under a new slug needs no replacement.
				same = team.ID == state.TeamID.ValueString()
				teamID = types.StringValue(team.ID)
			}
			if !same {
				idChanged = true
				resp.RequiresReplace.Append(path.Root("team_slug"))
				resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("team_id"), teamID)...)
			}
		}
	}

	if idChanged {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("id"), types.StringUnknown())...)
	}
}

func (r *TeamMembershipResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan teamMembershipResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	summary, diags := findOrganizationTeam(ctx, r.client, plan.Organization.ValueString(), plan.TeamID.ValueString(), plan.TeamSlug.ValueString())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	member, diags := r.apply(ctx, &plan, summary.Slug)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	plan.TeamID = types.StringValue(summary.ID)
	plan.TeamSlug = slugFromRemote(plan.TeamSlug, summary.Slug)
	plan.fromRemote(member)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *TeamMembershipResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state teamMembershipResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// A missing team, or an organization the token's user has left, is a
	// 404: the membership is gone.
	team, err := r.client.GetTeamByID(ctx, state.Organization.ValueString(), state.TeamID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading team", err.Error())
		return
	}
	member := findTeamMember(team.Members, state.Username.ValueString())
	if member == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	state.TeamSlug = slugFromRemote(state.TeamSlug, team.Slug)
	state.fromRemote(member)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *TeamMembershipResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state teamMembershipResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// ModifyPlan forces a new resource for a different team, so the team is
	// the one in state.
	summary, diags := findOrganizationTeam(ctx, r.client, plan.Organization.ValueString(), state.TeamID.ValueString(), "")
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	member, diags := r.apply(ctx, &plan, summary.Slug)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	plan.TeamID = state.TeamID
	if plan.TeamSlug.IsUnknown() {
		plan.TeamSlug = types.StringValue(summary.Slug)
	}
	plan.TeamSlug = slugFromRemote(plan.TeamSlug, summary.Slug)
	plan.fromRemote(member)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *TeamMembershipResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state teamMembershipResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := state.Organization.ValueString()
	teams, err := r.client.ListOrganizationTeams(ctx, org)
	if client.IsNotFound(err) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading the organization's teams", err.Error())
		return
	}
	var slug string
	for _, t := range teams {
		if t.ID == state.TeamID.ValueString() {
			slug = t.Slug
		}
	}
	if slug == "" {
		// The team is gone, and its memberships with it.
		return
	}

	// This also ends the user's role as a maintainer. Ghost returns 204 when
	// they're no longer in the team, and 404 when the user is gone.
	err = r.client.RemoveTeamMember(ctx, org, slug, state.Username.ValueString())
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error removing the team member", err.Error())
	}
}

// ImportState takes "organization:team:username", where team is the team's
// ID or slug.
func (r *TeamMembershipResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	org, team, username, err := parseTeamMembershipID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}

	teams, err := r.client.ListOrganizationTeams(ctx, org)
	if err != nil {
		resp.Diagnostics.AddError("Error reading the organization's teams", teamMembershipOrganizationError(org, err))
		return
	}
	found := teamByIDOrSlug(teams, team)
	if found == nil {
		resp.Diagnostics.AddError("Team not found",
			fmt.Sprintf("Organization %s has no team with the ID or slug %q.", org, team))
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), teamMembershipID(org, found.ID, username))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization"), org)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("team_id"), found.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("username"), username)...)
}

// apply adds the user in m to the team with the given slug, unless they're
// in it already, and gives them m's role. It returns the member as Ghost
// then lists them.
func (r *TeamMembershipResource) apply(ctx context.Context, m *teamMembershipResourceModel, teamSlug string) (*client.TeamMember, diag.Diagnostics) {
	var diags diag.Diagnostics
	org, username := m.Organization.ValueString(), m.Username.ValueString()

	team, err := r.client.GetTeam(ctx, org, teamSlug)
	if err != nil {
		diags.AddError("Error reading team", err.Error())
		return nil, diags
	}
	member := findTeamMember(team.Members, username)

	if member == nil {
		if err := r.client.AddTeamMember(ctx, org, teamSlug, username); err != nil {
			diags.Append(teamMembershipError("Error adding the team member", m, err)...)
			return nil, diags
		}
	}

	wantMaintainer := m.Role.ValueString() == teamRoleMaintainer
	isMaintainer := member != nil && member.Maintainer
	switch {
	case wantMaintainer && !isMaintainer:
		err = r.client.AppointTeamMaintainer(ctx, org, teamSlug, username)
	case !wantMaintainer && isMaintainer:
		err = r.client.DismissTeamMaintainer(ctx, org, teamSlug, username)
	}
	if err != nil {
		diags.Append(teamMembershipError("Error setting the team member's role", m, err)...)
		return nil, diags
	}

	if member != nil && wantMaintainer == isMaintainer {
		return member, diags
	}

	// Read the member back for the username as Ghost stores it.
	team, err = r.client.GetTeam(ctx, org, teamSlug)
	if err != nil {
		diags.AddError("Error reading team", err.Error())
		return nil, diags
	}
	if member = findTeamMember(team.Members, username); member == nil {
		diags.AddError("Team member not found",
			fmt.Sprintf("Ghost accepted %s as a member of team %s in %s, but doesn't list them.", username, teamSlug, org))
		return nil, diags
	}

	return member, diags
}

// teamMembershipError explains a failure to add a user to a team or change
// their role.
func teamMembershipError(summary string, m *teamMembershipResourceModel, err error) diag.Diagnostics {
	var diags diag.Diagnostics
	org, username := m.Organization.ValueString(), m.Username.ValueString()

	var apiErr *client.APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusBadRequest && strings.HasSuffix(apiErr.Message, "is not a member here"):
		diags.AddAttributeError(path.Root("username"), summary,
			fmt.Sprintf("%s isn't a member of organization %s (%s). Only members of an organization can join its teams. "+
				"Organization invitations can't be sent with an API token: invite them in the Ghost web UI, and apply "+
				"again once they've accepted.", username, org, apiErr.Message))
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound && strings.HasPrefix(apiErr.Message, "User "):
		diags.AddAttributeError(path.Root("username"), "User not found",
			fmt.Sprintf("Ghost has no user %q: %s. Use the username, not the display name or an email address.",
				username, apiErr.Message))
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusForbidden:
		diags.AddError(summary,
			fmt.Sprintf("You need to be an admin of %s, or a maintainer of the team, to manage its members, and an admin to "+
				"change their role: %s", org, apiErr.Message))
	default:
		diags.AddError(summary, err.Error())
	}

	return diags
}

// findOrganizationTeam finds the organization's team with the given ID or,
// when id is empty, the given slug, which is matched case-insensitively.
func findOrganizationTeam(ctx context.Context, c *client.Client, org, id, slug string) (*client.TeamSummary, diag.Diagnostics) {
	var diags diag.Diagnostics

	teams, err := c.ListOrganizationTeams(ctx, org)
	if err != nil {
		diags.AddAttributeError(path.Root("organization"), "Error reading the organization's teams", teamMembershipOrganizationError(org, err))
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

// teamMembershipOrganizationError explains a failure to list an
// organization's teams. Ghost returns 404 for a user, an unknown
// organization, and an organization the token's user isn't a member of.
func teamMembershipOrganizationError(org string, err error) string {
	if client.IsNotFound(err) {
		return fmt.Sprintf("Organization %s not found, or you're not a member of it. Teams belong to organizations, "+
			"not users. (%s)", org, err)
	}

	return err.Error()
}

// fromRemote copies member into m. The configured username spelling is kept
// when it names the same user.
func (m *teamMembershipResourceModel) fromRemote(member *client.TeamMember) {
	if !strings.EqualFold(m.Username.ValueString(), member.Username) {
		m.Username = types.StringValue(member.Username)
	}
	m.Role = types.StringValue(teamRoleMember)
	if member.Maintainer {
		m.Role = types.StringValue(teamRoleMaintainer)
	}
	m.ID = types.StringValue(teamMembershipID(m.Organization.ValueString(), m.TeamID.ValueString(), member.Username))
}

// findTeamMember finds the member with the given username, which is matched
// case-insensitively.
func findTeamMember(members []client.TeamMember, username string) *client.TeamMember {
	for i, m := range members {
		if strings.EqualFold(m.Username, username) {
			return &members[i]
		}
	}

	return nil
}

// teamMembershipID builds the "organization:team_id:username" ID.
func teamMembershipID(org, teamID, username string) string {
	return org + ":" + teamID + ":" + username
}

// parseTeamMembershipID parses an "organization:team:username" import ID.
func parseTeamMembershipID(id string) (org, team, username string, err error) {
	parts := strings.Split(id, ":")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" || strings.Contains(id, "/") {
		return "", "", "", fmt.Errorf("import ID must be \"organization:team_id:username\" or "+
			"\"organization:team_slug:username\", e.g. \"acme:core:octocat\", got %q", id)
	}

	return parts[0], parts[1], parts[2], nil
}
