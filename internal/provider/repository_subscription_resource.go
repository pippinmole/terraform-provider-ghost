package provider

import (
	"context"
	"fmt"

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
	_ resource.Resource                = &RepositorySubscriptionResource{}
	_ resource.ResourceWithConfigure   = &RepositorySubscriptionResource{}
	_ resource.ResourceWithImportState = &RepositorySubscriptionResource{}
	_ resource.ResourceWithModifyPlan  = &RepositorySubscriptionResource{}
)

// watchLevels are the levels Ghost accepts, in the order the docs list them.
var watchLevels = []string{client.WatchAll, client.WatchIgnore, client.WatchParticipating}

// NewRepositorySubscriptionResource returns the
// ghost_repository_subscription resource.
func NewRepositorySubscriptionResource() resource.Resource {
	return &RepositorySubscriptionResource{}
}

// RepositorySubscriptionResource manages how the token's user watches a
// repository.
type RepositorySubscriptionResource struct {
	client *client.Client
}

// repositorySubscriptionResourceModel maps the
// ghost_repository_subscription schema.
type repositorySubscriptionResourceModel struct {
	ID           types.String `tfsdk:"id"`
	Repository   types.String `tfsdk:"repository"`
	RepositoryID types.Int64  `tfsdk:"repository_id"`
	Level        types.String `tfsdk:"level"`
}

func (r *RepositorySubscriptionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_repository_subscription"
}

func (r *RepositorySubscriptionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Watches a repository, ignores it, or sets it back to the default, for the user the provider's " +
			"token belongs to. It sets which notifications that user gets about the repository; it can't manage anyone " +
			"else's. Any repository the user can see can be watched, including other people's public repositories. " +
			"Every repository has a level, `participating` until the user chooses another, so creating this resource " +
			"adopts the current level, and destroying it sets the level back to `participating`, which stops watching " +
			"or ignoring the repository. The level follows the repository when it's renamed.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The repository's full name, `owner/slug`, as configured.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"repository": schema.StringAttribute{
				MarkdownDescription: "The repository's full name, `owner/slug`, e.g. `ghost_repository.example.full_name`, matched case-insensitively and kept as configured. Renaming the repository updates this in place; pointing it at a different repository forces a new resource.",
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
			"level": schema.StringAttribute{
				MarkdownDescription: "Which notifications the user gets about the repository: `all` for every issue, pull request and discussion " +
					"thread (watching), `participating` only for threads they take part in or are mentioned in (the default, not watching), " +
					"or `ignore` for nothing, mentions included. Changing it updates the resource in place.",
				Required:   true,
				Validators: []validator.String{stringvalidator.OneOf(watchLevels...)},
			},
		},
	}
}

func (r *RepositorySubscriptionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
// repository under a new name, and forces a new resource otherwise.
func (r *RepositorySubscriptionResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}

	var plan, state repositorySubscriptionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || plan.Repository.Equal(state.Repository) {
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

func (r *RepositorySubscriptionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan repositorySubscriptionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	repository, err := lookupRepository(ctx, r.client, plan.Repository.ValueString())
	if client.IsNotFound(err) {
		resp.Diagnostics.AddAttributeError(path.Root("repository"), "Repository not found",
			fmt.Sprintf("Repository %s not found, or the token's user can't see it.", plan.Repository.ValueString()))
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading repository", err.Error())
		return
	}
	plan.RepositoryID = types.Int64Value(repository.ID)

	resp.Diagnostics.Append(r.set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *RepositorySubscriptionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state repositorySubscriptionResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	owner, repo, err := client.SplitFullName(state.Repository.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("repository"), "Invalid repository", err.Error())
		return
	}

	subscription, err := r.client.GetRepositorySubscription(ctx, owner, repo)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading the repository subscription", err.Error())
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

	state.fromRemote(subscription)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *RepositorySubscriptionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state repositorySubscriptionResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.RepositoryID = state.RepositoryID

	if !plan.Repository.Equal(state.Repository) {
		repository, err := lookupRepository(ctx, r.client, plan.Repository.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Error reading repository", err.Error())
			return
		}
		if !sameRepository(state.RepositoryID, repository) {
			resp.Diagnostics.AddAttributeError(path.Root("repository"), "A subscription can't be moved",
				fmt.Sprintf("%s is a different repository from the one this resource was created for. "+
					"Replace it with terraform apply -replace=<address>.", plan.Repository.ValueString()))
			return
		}
		plan.RepositoryID = types.Int64Value(repository.ID)
	}

	resp.Diagnostics.Append(r.set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *RepositorySubscriptionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state repositorySubscriptionResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	owner, repo, err := client.SplitFullName(state.Repository.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("repository"), "Invalid repository", err.Error())
		return
	}

	// participating is the level of a repository nobody chose one for.
	// Ghost returns 404 when the repository is gone.
	_, err = r.client.SetRepositorySubscription(ctx, owner, repo, client.WatchParticipating)
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error resetting the repository subscription", err.Error())
	}
}

// ImportState takes the repository's full name, "owner/slug".
func (r *RepositorySubscriptionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !fullNamePattern.MatchString(req.ID) {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("import ID must be the repository's full name, \"owner/slug\", e.g. \"acme/my-repo\", got %q", req.ID))
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("repository"), req.ID)...)
}

// set sends m's level for m's repository, and copies the result into m.
func (r *RepositorySubscriptionResource) set(ctx context.Context, m *repositorySubscriptionResourceModel) diag.Diagnostics {
	var diags diag.Diagnostics

	owner, repo, err := client.SplitFullName(m.Repository.ValueString())
	if err != nil {
		diags.AddAttributeError(path.Root("repository"), "Invalid repository", err.Error())
		return diags
	}

	subscription, err := r.client.SetRepositorySubscription(ctx, owner, repo, m.Level.ValueString())
	switch {
	case err == nil:
		m.fromRemote(subscription)
	case client.IsNotFound(err):
		diags.AddAttributeError(path.Root("repository"), "Repository not found",
			fmt.Sprintf("Repository %s not found, or the token's user can't see it: %s", m.Repository.ValueString(), err))
	default:
		diags.AddError("Error setting the repository subscription",
			fmt.Sprintf("Setting the watch level of %s to %q: %s", m.Repository.ValueString(), m.Level.ValueString(), err))
	}

	return diags
}

// fromRemote copies subscription into m. The repository is kept as
// configured.
func (m *repositorySubscriptionResourceModel) fromRemote(subscription *client.RepositorySubscription) {
	m.ID = types.StringValue(m.Repository.ValueString())
	m.Level = types.StringValue(subscription.Level)
}
