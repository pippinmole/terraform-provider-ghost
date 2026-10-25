package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

var (
	_ resource.Resource                   = &RepositoryResource{}
	_ resource.ResourceWithConfigure      = &RepositoryResource{}
	_ resource.ResourceWithImportState    = &RepositoryResource{}
	_ resource.ResourceWithModifyPlan     = &RepositoryResource{}
	_ resource.ResourceWithValidateConfig = &RepositoryResource{}
)

// NewRepositoryResource returns the ghost_repository resource.
func NewRepositoryResource() resource.Resource {
	return &RepositoryResource{}
}

// RepositoryResource manages a Ghost repository.
type RepositoryResource struct {
	client *client.Client
}

// repositoryResourceModel maps the ghost_repository schema.
type repositoryResourceModel struct {
	repositoryRemoteModel

	Name              types.String `tfsdk:"name"`
	Owner             types.String `tfsdk:"owner"`
	Description       types.String `tfsdk:"description"`
	AutoInit          types.Bool   `tfsdk:"auto_init"`
	GitignoreTemplate types.String `tfsdk:"gitignore_template"`
	LicenseTemplate   types.String `tfsdk:"license_template"`
}

func (r *RepositoryResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_repository"
}

func (r *RepositoryResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	stableString := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	createOnlyString := []planmodifier.String{stringplanmodifier.RequiresReplaceIf(createOnlyStringReplace,
		createOnlyDescription, createOnlyDescription)}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a Ghost repository.\n\n" +
			"Repositories are created through the GitHub-compatible API and updated or deleted through Ghost's native API.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The repository's full name, `owner/slug`.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The repository name. Ghost derives the slug from it, so renaming also changes `slug` and the repository URL. The Ghost API reports the slug rather than the display name, so importing a repository whose display name differs from its slug shows a one-time in-place rename.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"owner": schema.StringAttribute{
				MarkdownDescription: "The user or organization that owns the repository. Defaults to the authenticated user. Changing it (other than its letter case) forces a new repository.",
				Optional:            true,
				Computed:            true,
				// UseStateForUnknown must run first: when owner is omitted the
				// framework marks it unknown on any change, and the replace
				// check would treat that as a new owner.
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplaceIf(ownerReplace,
						"Changing the owner, other than its letter case, forces a new repository.",
						"Changing the owner, other than its letter case, forces a new repository."),
				},
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "A short description of the repository.",
				Optional:            true,
			},
			"visibility": schema.StringAttribute{
				MarkdownDescription: "Either `public` or `private`. Defaults to `private`.",
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("private"),
				Validators:          []validator.String{stringvalidator.OneOf("public", "private")},
			},
			"default_branch": schema.StringAttribute{
				MarkdownDescription: "The default branch. The branch must already exist, so on a new repository this usually needs `auto_init`.",
				Optional:            true,
				Computed:            true,
				PlanModifiers:       stableString,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"auto_init": schema.BoolAttribute{
				MarkdownDescription: "Create an initial commit when the repository is created. Changing it forces a new repository, except when it was unset (e.g. after import).",
				Optional:            true,
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplaceIf(createOnlyBoolReplace,
					createOnlyDescription, createOnlyDescription)},
			},
			"gitignore_template": schema.StringAttribute{
				MarkdownDescription: "A `.gitignore` template to apply at creation, e.g. `Go`. Changing it forces a new repository, except when it was unset (e.g. after import).",
				Optional:            true,
				PlanModifiers:       createOnlyString,
			},
			"license_template": schema.StringAttribute{
				MarkdownDescription: "A license template to apply at creation, e.g. `mit`. Changing it forces a new repository, except when it was unset (e.g. after import).",
				Optional:            true,
				PlanModifiers:       createOnlyString,
			},
			"slug": schema.StringAttribute{
				MarkdownDescription: "The URL-safe name Ghost derived from `name`.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
			"full_name": schema.StringAttribute{
				MarkdownDescription: "The repository's full name, `owner/slug`.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
			"repo_id": schema.Int64Attribute{
				MarkdownDescription: "The numeric repository ID.",
				Computed:            true,
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"node_id": schema.StringAttribute{
				MarkdownDescription: "The GraphQL node ID.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
			"html_url": schema.StringAttribute{
				MarkdownDescription: "The repository's web URL.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
			"clone_url": schema.StringAttribute{
				MarkdownDescription: "The HTTPS clone URL.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
			"ssh_url": schema.StringAttribute{
				MarkdownDescription: "The SSH clone URL, empty when SSH is unavailable.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
			"private": schema.BoolAttribute{
				MarkdownDescription: "Whether the repository is private.",
				Computed:            true,
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"fork": schema.BoolAttribute{
				MarkdownDescription: "Whether the repository is a fork.",
				Computed:            true,
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *RepositoryResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ValidateConfig rejects an empty owner, which would otherwise silently mean
// "the authenticated user" and then never match state.
func (r *RepositoryResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var owner types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("owner"), &owner)...)
	if !owner.IsNull() && !owner.IsUnknown() && owner.ValueString() == "" {
		resp.Diagnostics.AddAttributeError(path.Root("owner"), "Empty owner",
			"Omit owner to create the repository for the authenticated user, or set it to a user or organization login.")
	}
}

// ModifyPlan marks the attributes derived from the name or visibility as
// unknown when those change, since UseStateForUnknown would otherwise keep
// their stale values.
func (r *RepositoryResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}

	var plan, state repositoryResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !plan.Name.Equal(state.Name) {
		for _, attr := range []string{"id", "slug", "full_name", "html_url", "clone_url", "ssh_url"} {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(attr), types.StringUnknown())...)
		}
	}

	if !plan.Visibility.Equal(state.Visibility) {
		private := types.BoolUnknown()
		if !plan.Visibility.IsUnknown() {
			private = types.BoolValue(plan.Visibility.ValueString() == "private")
		}
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("private"), private)...)
	}
}

func (r *RepositoryResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan repositoryResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org, login := "", ""
	if owner := plan.Owner.ValueString(); owner != "" {
		user, err := r.client.GetAuthenticatedUser(ctx)
		if err != nil {
			resp.Diagnostics.AddError("Error reading authenticated user",
				"Could not determine whether owner "+owner+" is the authenticated user: "+err.Error())
			return
		}
		login = user.Login
		if !strings.EqualFold(owner, login) {
			org = owner
		}
	}

	createReq := client.CreateRepositoryRequest{
		Name:              plan.Name.ValueString(),
		Visibility:        plan.Visibility.ValueString(),
		AutoInit:          plan.AutoInit.ValueBoolPointer(),
		GitignoreTemplate: plan.GitignoreTemplate.ValueString(),
		LicenseTemplate:   plan.LicenseTemplate.ValueString(),
	}
	if d := plan.Description.ValueString(); d != "" {
		createReq.Description = &d
	}

	repo, err := r.client.CreateRepository(ctx, org, createReq)
	if err != nil {
		resp.Diagnostics.AddError("Error creating repository", err.Error())
		return
	}

	// Record the repository straight away so a failure below leaves it in
	// state (tainted) rather than orphaned.
	wantBranch := plan.DefaultBranch
	if err := plan.fromRemote(repo); err != nil {
		resp.Diagnostics.AddError("Error reading created repository",
			"The repository was created, but its response could not be read: "+err.Error())
		r.saveCreatedFallback(ctx, resp, plan, repo, org, login)
		return
	}
	plan.Description = repositoryDescriptionFromRemote(plan.Description, repo.Description)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The default branch cannot be chosen at creation time.
	if !wantBranch.IsNull() && !wantBranch.IsUnknown() && wantBranch.ValueString() != repo.DefaultBranch {
		branch := wantBranch.ValueString()
		owner, slug := plan.apiLocation()
		if _, err := r.client.UpdateRepository(ctx, owner, slug, client.UpdateRepositoryRequest{DefaultBranch: &branch}); err != nil {
			resp.Diagnostics.AddError("Error setting default branch",
				fmt.Sprintf("Repository %s/%s was created but setting default_branch to %q failed: %s", owner, slug, branch, err))
			return
		}
		if err := r.refresh(ctx, &plan, owner, slug); err != nil {
			resp.Diagnostics.AddError("Error reading repository after setting default branch", err.Error())
			return
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
}

// saveCreatedFallback records a just-created repository whose response had an
// unusable full_name, so it is tracked (and tainted) rather than orphaned. The
// owner comes from the response, the requested org, or the authenticated user,
// and the slug from the response's name, which the v3 API sets to the slug.
func (r *RepositoryResource) saveCreatedFallback(ctx context.Context, resp *resource.CreateResponse,
	plan repositoryResourceModel, repo *client.Repository, org, login string) {
	owner := repo.Owner.Login
	if owner == "" {
		owner = org
	}
	if owner == "" && login == "" {
		if user, err := r.client.GetAuthenticatedUser(ctx); err == nil {
			login = user.Login
		}
	}
	if owner == "" {
		owner = login
	}
	if owner == "" || repo.Name == "" {
		resp.Diagnostics.AddError("Repository may be orphaned",
			fmt.Sprintf("Could not work out the created repository's owner and slug, so it is not in Terraform state. "+
				"Find it in Ghost (requested name %q) and import or delete it.", plan.Name.ValueString()))
		return
	}

	plan.set(repo, owner, repo.Name)
	plan.keepOwnerCase(owner)
	plan.Description = repositoryDescriptionFromRemote(plan.Description, repo.Description)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *RepositoryResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state repositoryResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	owner, slug := state.apiLocation()
	repo, err := r.client.GetRepository(ctx, owner, slug)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading repository", err.Error())
		return
	}

	priorSlug := state.Slug.ValueString()
	if err := state.fromRemote(repo); err != nil {
		resp.Diagnostics.AddError("Error reading repository", err.Error())
		return
	}
	state.Name = repositoryNameFromRemote(state.Name, priorSlug, repo.Name)
	state.Description = repositoryDescriptionFromRemote(state.Description, repo.Description)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *RepositoryResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state repositoryResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	owner, slug := state.apiLocation()

	var update client.UpdateRepositoryRequest
	changed := false
	if !plan.Name.Equal(state.Name) {
		update.Name = plan.Name.ValueStringPointer()
		changed = true
	}
	if plan.Description.ValueString() != state.Description.ValueString() {
		d := plan.Description.ValueString() // "" clears the description
		update.Description = &d
		changed = true
	}
	if !plan.Visibility.Equal(state.Visibility) {
		update.Visibility = plan.Visibility.ValueStringPointer()
		changed = true
	}
	if !plan.DefaultBranch.IsUnknown() && !plan.DefaultBranch.Equal(state.DefaultBranch) {
		update.DefaultBranch = plan.DefaultBranch.ValueStringPointer()
		changed = true
	}

	if changed {
		updated, err := r.client.UpdateRepository(ctx, owner, slug, update)
		if err != nil {
			resp.Diagnostics.AddError("Error updating repository", err.Error())
			return
		}
		if updated.Slug != "" {
			slug = updated.Slug
		}
	}

	if err := r.refresh(ctx, &plan, owner, slug); err != nil {
		resp.Diagnostics.AddError("Error reading repository after update", err.Error())
		if changed {
			// The PATCH applied, so record what is known (notably a new slug)
			// rather than leaving state pointing at the old URL.
			resp.Diagnostics.Append(resp.State.Set(ctx, state.withAppliedUpdate(plan, owner, slug))...)
		}
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// withAppliedUpdate returns m (the prior state) with the updatable arguments
// from plan and the owner/slug returned by the update applied. Every value
// stays known, so it can be saved when the post-update read fails.
func (m repositoryResourceModel) withAppliedUpdate(plan repositoryResourceModel, owner, slug string) *repositoryResourceModel {
	m.Name = plan.Name
	m.Description = plan.Description
	m.Visibility = plan.Visibility
	m.Private = types.BoolValue(plan.Visibility.ValueString() == "private")
	if !plan.DefaultBranch.IsUnknown() {
		m.DefaultBranch = plan.DefaultBranch
	}
	m.Slug = types.StringValue(slug)
	m.FullName = types.StringValue(owner + "/" + slug)
	m.ID = m.FullName

	return &m
}

func (r *RepositoryResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state repositoryResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	owner, slug := state.apiLocation()
	err := r.client.DeleteRepository(ctx, owner, slug)
	if err == nil || client.IsNotFound(err) {
		return
	}

	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict {
		resp.Diagnostics.AddError("Repository cannot be deleted yet",
			fmt.Sprintf("Ghost refused to delete %s because it is the head of an open pull request into another repository. "+
				"Close or merge that pull request, then try again. (%s)", state.FullName.ValueString(), err))
		return
	}

	resp.Diagnostics.AddError("Error deleting repository", err.Error())
}

func (r *RepositoryResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	owner, slug, err := parseRepositoryImportID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("owner"), owner)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("slug"), slug)...)
}

// refresh re-reads owner/slug into m after a write. The configured name and
// description in m are kept, since the API accepted them.
func (r *RepositoryResource) refresh(ctx context.Context, m *repositoryResourceModel, owner, slug string) error {
	repo, err := r.client.GetRepository(ctx, owner, slug)
	if err != nil {
		return err
	}
	if err := m.fromRemote(repo); err != nil {
		return err
	}
	m.Description = repositoryDescriptionFromRemote(m.Description, repo.Description)

	return nil
}

// fromRemote copies the API-derived attributes of repo into m. It leaves
// name, description and the create-only arguments alone, and keeps the
// configured owner when it matches the remote one apart from case.
func (m *repositoryResourceModel) fromRemote(repo *client.Repository) error {
	owner, err := m.repositoryRemoteModel.fromRemote(repo)
	if err != nil {
		return err
	}
	m.keepOwnerCase(owner)

	return nil
}

// apiLocation returns the owner and slug to put in API paths. Ghost matches
// the owner segment case-sensitively, so it prefers the canonical full_name
// over the configured owner spelling, falling back to owner/slug before the
// first read (e.g. straight after import).
func (m repositoryResourceModel) apiLocation() (owner, slug string) {
	if !m.FullName.IsNull() && !m.FullName.IsUnknown() {
		if o, s, err := client.SplitFullName(m.FullName.ValueString()); err == nil {
			return o, s
		}
	}

	return m.Owner.ValueString(), m.Slug.ValueString()
}

// keepOwnerCase sets owner unless m already holds it in another letter case;
// logins are case-insensitive, and keeping the configured spelling avoids a
// diff.
func (m *repositoryResourceModel) keepOwnerCase(owner string) {
	if m.Owner.IsNull() || m.Owner.IsUnknown() || !strings.EqualFold(m.Owner.ValueString(), owner) {
		m.Owner = types.StringValue(owner)
	}
}

const createOnlyDescription = "Changing this forces a new repository, unless it was previously unset (e.g. after import)."

// createOnlyStringReplace forces replacement when a create-only argument
// changes, except from null: after an import these are null in state, and
// adopting the configured value must not recreate the repository.
func createOnlyStringReplace(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
	resp.RequiresReplace = !req.StateValue.IsNull()
}

// createOnlyBoolReplace is createOnlyStringReplace for bool arguments.
func createOnlyBoolReplace(_ context.Context, req planmodifier.BoolRequest, resp *boolplanmodifier.RequiresReplaceIfFuncResponse) {
	resp.RequiresReplace = !req.StateValue.IsNull()
}

// ownerReplace forces replacement when the owner really changes. Logins are
// case-insensitive, so a change of letter case alone is applied in place.
func ownerReplace(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
	resp.RequiresReplace = req.PlanValue.IsUnknown() || req.StateValue.IsNull() ||
		!strings.EqualFold(req.StateValue.ValueString(), req.PlanValue.ValueString())
}

// repositoryNameFromRemote decides the name to keep in state after a read.
// The v3 API reports the slug as the name, so a configured display name such
// as "My Repo" is kept as long as the slug has not changed; a different slug
// means the repository was renamed outside Terraform.
func repositoryNameFromRemote(prior types.String, priorSlug, remote string) types.String {
	if prior.IsNull() || prior.IsUnknown() {
		return types.StringValue(remote)
	}
	if prior.ValueString() == remote || remote == priorSlug {
		return prior
	}

	return types.StringValue(remote)
}

// repositoryDescriptionFromRemote treats a missing and an empty remote
// description as the same thing, keeping whichever of null or "" was in
// state so neither produces a diff.
func repositoryDescriptionFromRemote(prior types.String, remote *string) types.String {
	if remote == nil || *remote == "" {
		if !prior.IsNull() && !prior.IsUnknown() && prior.ValueString() == "" {
			return prior
		}

		return types.StringNull()
	}

	return types.StringValue(*remote)
}

// parseRepositoryImportID parses an "owner/slug" import ID.
func parseRepositoryImportID(id string) (owner, slug string, err error) {
	owner, slug, err = client.SplitFullName(id)
	if err != nil {
		return "", "", fmt.Errorf("import ID must be \"owner/slug\", got %q", id)
	}

	return owner, slug, nil
}
