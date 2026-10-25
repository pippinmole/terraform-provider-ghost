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
	ID                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	Owner             types.String `tfsdk:"owner"`
	Description       types.String `tfsdk:"description"`
	Visibility        types.String `tfsdk:"visibility"`
	DefaultBranch     types.String `tfsdk:"default_branch"`
	AutoInit          types.Bool   `tfsdk:"auto_init"`
	GitignoreTemplate types.String `tfsdk:"gitignore_template"`
	LicenseTemplate   types.String `tfsdk:"license_template"`
	Slug              types.String `tfsdk:"slug"`
	FullName          types.String `tfsdk:"full_name"`
	RepoID            types.Int64  `tfsdk:"repo_id"`
	NodeID            types.String `tfsdk:"node_id"`
	HTMLURL           types.String `tfsdk:"html_url"`
	CloneURL          types.String `tfsdk:"clone_url"`
	SSHURL            types.String `tfsdk:"ssh_url"`
	Private           types.Bool   `tfsdk:"private"`
	Fork              types.Bool   `tfsdk:"fork"`
}

func (r *RepositoryResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_repository"
}

func (r *RepositoryResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	stableString := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	createOnlyString := []planmodifier.String{stringplanmodifier.RequiresReplace()}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a Ghost repository.\n\n" +
			"Repositories are created through the GitHub-compatible API and updated or deleted through the native API.",
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
				MarkdownDescription: "The user or organization that owns the repository. Defaults to the authenticated user. Changing it forces a new repository.",
				Optional:            true,
				Computed:            true,
				// UseStateForUnknown must run first: when owner is omitted the
				// framework marks it unknown on any change, and RequiresReplace
				// would treat that as a new owner.
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
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
				MarkdownDescription: "Create an initial commit when the repository is created. Changing it forces a new repository.",
				Optional:            true,
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"gitignore_template": schema.StringAttribute{
				MarkdownDescription: "A `.gitignore` template to apply at creation, e.g. `Go`. Changing it forces a new repository.",
				Optional:            true,
				PlanModifiers:       createOnlyString,
			},
			"license_template": schema.StringAttribute{
				MarkdownDescription: "A license template to apply at creation, e.g. `mit`. Changing it forces a new repository.",
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

	org := ""
	if owner := plan.Owner.ValueString(); owner != "" {
		user, err := r.client.GetAuthenticatedUser(ctx)
		if err != nil {
			resp.Diagnostics.AddError("Error reading authenticated user",
				"Could not determine whether owner "+owner+" is the authenticated user: "+err.Error())
			return
		}
		if !strings.EqualFold(owner, user.Login) {
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
		resp.Diagnostics.AddError("Error reading created repository", err.Error())
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
		owner, slug := plan.Owner.ValueString(), plan.Slug.ValueString()
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

func (r *RepositoryResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state repositoryResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	repo, err := r.client.GetRepository(ctx, state.Owner.ValueString(), state.Slug.ValueString())
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

	owner, slug := state.Owner.ValueString(), state.Slug.ValueString()

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

	err := r.client.DeleteRepository(ctx, state.Owner.ValueString(), state.Slug.ValueString())
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
	owner, slug, err := client.SplitFullName(repo.FullName)
	if err != nil {
		return err
	}

	m.ID = types.StringValue(repo.FullName)
	m.FullName = types.StringValue(repo.FullName)
	if m.Owner.IsNull() || m.Owner.IsUnknown() || !strings.EqualFold(m.Owner.ValueString(), owner) {
		m.Owner = types.StringValue(owner)
	}
	m.Slug = types.StringValue(slug)
	m.RepoID = types.Int64Value(repo.ID)
	m.NodeID = types.StringValue(repo.NodeID)
	m.HTMLURL = types.StringValue(repo.HTMLURL)
	m.CloneURL = types.StringValue(repo.CloneURL)
	m.SSHURL = types.StringValue(repo.SSHURL)
	m.Private = types.BoolValue(repo.Private)
	m.Fork = types.BoolValue(repo.Fork)
	m.Visibility = types.StringValue(repo.Visibility)
	m.DefaultBranch = types.StringValue(repo.DefaultBranch)

	return nil
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
