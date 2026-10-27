package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
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
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

var (
	_ resource.Resource                = &RepositoryCollaboratorResource{}
	_ resource.ResourceWithConfigure   = &RepositoryCollaboratorResource{}
	_ resource.ResourceWithImportState = &RepositoryCollaboratorResource{}
	_ resource.ResourceWithModifyPlan  = &RepositoryCollaboratorResource{}
)

// usernamePattern rejects the characters the import ID and URL paths use as
// separators.
var usernamePattern = regexp.MustCompile(`^[^:/]+$`)

// NewRepositoryCollaboratorResource returns the ghost_repository_collaborator
// resource.
func NewRepositoryCollaboratorResource() resource.Resource {
	return &RepositoryCollaboratorResource{}
}

// RepositoryCollaboratorResource manages a user's invitation to, and role on,
// a repository.
type RepositoryCollaboratorResource struct {
	client *client.Client
}

// repositoryCollaboratorResourceModel maps the ghost_repository_collaborator
// schema.
type repositoryCollaboratorResourceModel struct {
	ID           types.String `tfsdk:"id"`
	Repository   types.String `tfsdk:"repository"`
	RepositoryID types.Int64  `tfsdk:"repository_id"`
	Username     types.String `tfsdk:"username"`
	Role         types.String `tfsdk:"role"`
	Status       types.String `tfsdk:"status"`
	InvitedAt    types.String `tfsdk:"invited_at"`
	ExpiresAt    types.String `tfsdk:"expires_at"`
}

func (r *RepositoryCollaboratorResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_repository_collaborator"
}

func (r *RepositoryCollaboratorResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Invites a user to collaborate on a repository with a role, and manages that role. " +
			"Ghost sends the user an invitation, which they must accept before they get access; until then `status` " +
			"is `pending`. A pending invitation is a normal end state for this resource. Invitations expire after " +
			"seven days: an expired or declined invitation is removed from state, so the next apply invites the user " +
			"again. Destroying the resource removes the collaborator, or withdraws the invitation if it is still pending. " +
			"You must be an admin of the repository. Inviting someone who is already a collaborator adopts them and " +
			"sets their role.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The repository's full name and the user's username as Ghost stores it, `owner/slug:username`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"repository": schema.StringAttribute{
				MarkdownDescription: "The repository's full name, `owner/slug`, e.g. `ghost_repository.example.full_name`. Renaming the repository updates this in place; pointing it at a different repository forces a new resource.",
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
			"username": schema.StringAttribute{
				MarkdownDescription: "The username of the user to invite, matched case-insensitively and kept as configured. It can't be the repository's owner. Changing it to another user forces a new resource; changing only its case doesn't.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					stringvalidator.RegexMatches(usernamePattern, `must not contain ":" or "/"`),
				},
			},
			"role": schema.StringAttribute{
				MarkdownDescription: "The user's role on the repository: `read`, `triage`, `write`, `maintain` or `admin`. Changing it updates the resource in place, whether or not the invitation has been accepted.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.OneOf(repositoryRoles...)},
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "`pending` until the user accepts the invitation, then `accepted`. Only an accepted collaborator has access.",
				Computed:            true,
			},
			"invited_at": schema.StringAttribute{
				MarkdownDescription: "When the user was invited, as an RFC 3339 timestamp. Changing the role keeps it.",
				Computed:            true,
			},
			"expires_at": schema.StringAttribute{
				MarkdownDescription: "When a pending invitation expires, as an RFC 3339 timestamp, seven days after `invited_at`. Null once the invitation is accepted.",
				Computed:            true,
			},
		},
	}
}

func (r *RepositoryCollaboratorResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
// repository under a new name, and forces a new resource otherwise. A
// username change forces a new resource unless only its case changed.
func (r *RepositoryCollaboratorResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}

	var plan, state repositoryCollaboratorResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !plan.Username.IsUnknown() && !strings.EqualFold(plan.Username.ValueString(), state.Username.ValueString()) {
		resp.RequiresReplace.Append(path.Root("username"))
	}

	if plan.Repository.Equal(state.Repository) {
		return
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("id"), types.StringUnknown())...)
	if plan.Repository.IsUnknown() {
		return
	}
	repo, err := lookupRepository(ctx, r.client, plan.Repository.ValueString())
	switch {
	case client.IsNotFound(err) || (err == nil && !sameRepository(state.RepositoryID, repo)):
		resp.RequiresReplace.Append(path.Root("repository"))
	case err != nil:
		resp.Diagnostics.AddAttributeError(path.Root("repository"), "Error reading repository", err.Error())
	}
}

func (r *RepositoryCollaboratorResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan repositoryCollaboratorResourceModel
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

	collaborator, diags := r.invite(ctx, owner, repo, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	plan.fromRemote(collaborator)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *RepositoryCollaboratorResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state repositoryCollaboratorResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	owner, repo, err := client.SplitFullName(state.Repository.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("repository"), "Invalid repository", err.Error())
		return
	}

	collaborators, err := r.client.ListRepositoryCollaborators(ctx, owner, repo)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading the repository's collaborators", err.Error())
		return
	}
	collaborator := findCollaborator(collaborators, state.Username.ValueString())
	if collaborator == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	if collaborator.Status == client.CollaboratorExpired {
		tflog.Info(ctx, "Invitation expired; removing it from state so the next apply invites the user again",
			map[string]any{"repository": state.Repository.ValueString(), "username": collaborator.Username})
		resp.State.RemoveResource(ctx)
		return
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

	state.fromRemote(collaborator)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *RepositoryCollaboratorResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state repositoryCollaboratorResourceModel
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
			resp.Diagnostics.AddAttributeError(path.Root("repository"), "A collaborator can't be moved",
				fmt.Sprintf("%s is a different repository from the one this resource was created for. "+
					"Replace it with terraform apply -replace=<address>.", plan.Repository.ValueString()))
			return
		}
		plan.RepositoryID = types.Int64Value(repository.ID)
	}

	var collaborator *client.Collaborator
	if plan.Role.Equal(state.Role) {
		// Only the repository's name or the username's case changed, so
		// there's nothing to send; read the invitation's current state.
		collaborators, err := r.client.ListRepositoryCollaborators(ctx, owner, repo)
		if err != nil {
			resp.Diagnostics.AddError("Error reading the repository's collaborators", err.Error())
			return
		}
		collaborator = findCollaborator(collaborators, plan.Username.ValueString())
	}
	// Invite for a new role, or when the invitation was withdrawn, declined
	// or expired since the plan.
	if collaborator == nil || collaborator.Status == client.CollaboratorExpired {
		var diags diag.Diagnostics
		collaborator, diags = r.invite(ctx, owner, repo, &plan)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	plan.fromRemote(collaborator)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *RepositoryCollaboratorResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state repositoryCollaboratorResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	owner, repo, err := client.SplitFullName(state.Repository.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("repository"), "Invalid repository", err.Error())
		return
	}

	// This also withdraws a pending invitation. Ghost returns 404 when the
	// user is no longer a collaborator, or the repository is gone.
	err = r.client.RemoveRepositoryCollaborator(ctx, owner, repo, state.Username.ValueString())
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error removing the collaborator", err.Error())
	}
}

// ImportState takes "owner/repo:username".
func (r *RepositoryCollaboratorResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	repository, username, err := parseRepositoryCollaboratorID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("repository"), repository)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("username"), username)...)
}

// invite invites the user in m to owner/repo with m's role, or changes the
// role they have.
func (r *RepositoryCollaboratorResource) invite(ctx context.Context, owner, repo string, m *repositoryCollaboratorResourceModel) (*client.Collaborator, diag.Diagnostics) {
	var diags diag.Diagnostics

	collaborator, err := r.client.SetRepositoryCollaborator(ctx, owner, repo, m.Username.ValueString(), m.Role.ValueString())
	var apiErr *client.APIError
	switch {
	case err == nil:
		return collaborator, diags
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound && strings.HasPrefix(apiErr.Message, "User "):
		diags.AddAttributeError(path.Root("username"), "User not found",
			fmt.Sprintf("Ghost has no user %q: %s. Use the username, not the display name or an email address.",
				m.Username.ValueString(), apiErr.Message))
	case client.IsNotFound(err):
		diags.AddAttributeError(path.Root("repository"), "Repository not found",
			fmt.Sprintf("Repository %s not found: %s", m.Repository.ValueString(), err))
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusBadRequest:
		diags.AddAttributeError(path.Root("username"), "Error inviting the collaborator",
			fmt.Sprintf("Ghost rejected the invitation of %s to %s: %s", m.Username.ValueString(), m.Repository.ValueString(), apiErr.Message))
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusForbidden:
		diags.AddError("Error inviting the collaborator",
			fmt.Sprintf("You need to be an admin of %s to manage its collaborators: %s", m.Repository.ValueString(), apiErr.Message))
	default:
		diags.AddError("Error inviting the collaborator", err.Error())
	}

	return nil, diags
}

// fromRemote copies collaborator into m. The configured username spelling is
// kept when it names the same user.
func (m *repositoryCollaboratorResourceModel) fromRemote(collaborator *client.Collaborator) {
	if !strings.EqualFold(m.Username.ValueString(), collaborator.Username) {
		m.Username = types.StringValue(collaborator.Username)
	}
	m.ID = types.StringValue(repositoryCollaboratorID(m.Repository.ValueString(), collaborator.Username))
	m.Role = types.StringValue(collaborator.Role)
	m.Status = types.StringValue(collaborator.Status)
	m.InvitedAt = types.StringValue(collaborator.InvitedAt)
	m.ExpiresAt = types.StringPointerValue(collaborator.ExpiresAt)
}

// findCollaborator finds the collaborator with the given username, which is
// matched case-insensitively.
func findCollaborator(collaborators []client.Collaborator, username string) *client.Collaborator {
	for i, c := range collaborators {
		if strings.EqualFold(c.Username, username) {
			return &collaborators[i]
		}
	}

	return nil
}

// repositoryCollaboratorID builds the "owner/slug:username" ID.
func repositoryCollaboratorID(repository, username string) string {
	return repository + ":" + username
}

// parseRepositoryCollaboratorID parses an "owner/slug:username" import ID.
func parseRepositoryCollaboratorID(id string) (repository, username string, err error) {
	repository, username, ok := strings.Cut(id, ":")
	if _, _, splitErr := client.SplitFullName(repository); !ok || !usernamePattern.MatchString(username) || splitErr != nil {
		return "", "", fmt.Errorf("import ID must be \"owner/repo:username\", e.g. \"acme/my-repo:octocat\", got %q", id)
	}

	return repository, username, nil
}
