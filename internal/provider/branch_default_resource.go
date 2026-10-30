package provider

import (
	"context"
	"fmt"
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
	_ resource.Resource                = &BranchDefaultResource{}
	_ resource.ResourceWithConfigure   = &BranchDefaultResource{}
	_ resource.ResourceWithImportState = &BranchDefaultResource{}
	_ resource.ResourceWithModifyPlan  = &BranchDefaultResource{}
)

// NewBranchDefaultResource returns the ghost_branch_default resource.
func NewBranchDefaultResource() resource.Resource {
	return &BranchDefaultResource{}
}

// BranchDefaultResource manages the default branch of a Ghost repository.
type BranchDefaultResource struct {
	client *client.Client
}

// branchDefaultResourceModel maps the ghost_branch_default schema.
type branchDefaultResourceModel struct {
	ID             types.String `tfsdk:"id"`
	Repository     types.String `tfsdk:"repository"`
	RepositoryID   types.Int64  `tfsdk:"repository_id"`
	Branch         types.String `tfsdk:"branch"`
	PreviousBranch types.String `tfsdk:"previous_branch"`
}

func (r *BranchDefaultResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_branch_default"
}

func (r *BranchDefaultResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets the default branch of a Ghost repository. " +
			"`ghost_repository.default_branch` manages the same setting: use one or the other, not both, or they " +
			"will keep undoing each other's change. Use this resource when the default branch is one Terraform " +
			"creates with `ghost_branch`, since a new repository has only the branch `auto_init` made. " +
			"Destroying it sets the default branch back to `previous_branch`, so the branch can then be deleted. " +
			"Ghost won't delete the default branch, so to move the default off a `ghost_branch` and remove that branch, " +
			"change `branch` in one apply and remove the old `ghost_branch` in the next: in a single apply, Terraform " +
			"deletes the branch before it updates this resource.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The repository's full name, `owner/slug`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"repository": schema.StringAttribute{
				MarkdownDescription: "The repository's full name, `owner/slug`, e.g. `ghost_repository.example.full_name`. Renaming the repository updates this in place; pointing it at a different repository forces a new resource.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(fullNamePattern, `must be "owner/slug", e.g. "alice/my-repo"`),
				},
			},
			"repository_id": schema.Int64Attribute{
				MarkdownDescription: "The numeric ID of `repository`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"branch": schema.StringAttribute{
				MarkdownDescription: "The branch to make the default, without the `refs/heads/` prefix. It must already exist; for a branch Terraform creates, use `ghost_branch.example.branch` so it is created first and, on destroy, deleted only after the default has moved off it. Branch names are case-sensitive.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					stringvalidator.RegexMatches(noColonPattern, `must not contain ":"`),
					branchNameValidator{},
				},
			},
			"previous_branch": schema.StringAttribute{
				MarkdownDescription: "The default branch before this resource was created. Destroying the resource sets the default branch back to it if it still exists, and otherwise leaves the default branch as it is. Null after import, in which case destroying the resource only removes it from state.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *BranchDefaultResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
func (r *BranchDefaultResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}

	var plan, state branchDefaultResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if plan.Repository.Equal(state.Repository) {
		return
	}

	if !plan.Repository.IsUnknown() {
		repo, err := lookupRepository(ctx, r.client, plan.Repository.ValueString())
		if client.IsNotFound(err) || (err == nil && !sameRepository(state.RepositoryID, repo)) {
			resp.RequiresReplace.Append(path.Root("repository"))
			return
		}
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("repository"), "Error reading repository", err.Error())
			return
		}
	}

	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("id"), types.StringUnknown())...)
}

func (r *BranchDefaultResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan branchDefaultResourceModel
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

	// The native branch list reports the real default branch; v3 says
	// "main" even for a repository with no commits.
	branches, err := r.client.ListBranches(ctx, owner, repo)
	if err != nil {
		resp.Diagnostics.AddError("Error reading branches", err.Error())
		return
	}
	if branches.DefaultBranch == nil {
		resp.Diagnostics.AddAttributeError(path.Root("repository"), "Repository has no branches",
			fmt.Sprintf("Repository %s has no commits yet, so it has no branch to make the default. "+
				"Create it with auto_init = true, or push a commit first.", plan.Repository.ValueString()))
		return
	}
	plan.PreviousBranch = types.StringValue(*branches.DefaultBranch)

	if *branches.DefaultBranch != plan.Branch.ValueString() {
		resp.Diagnostics.Append(r.setDefault(ctx, owner, repo, plan.Repository.ValueString(), plan.Branch.ValueString(), branches)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	plan.ID = types.StringValue(plan.Repository.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *BranchDefaultResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state branchDefaultResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	owner, repo, err := client.SplitFullName(state.Repository.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("repository"), "Invalid repository", err.Error())
		return
	}

	// A repository with no commits has no default branch to manage.
	branches, err := r.client.ListBranches(ctx, owner, repo)
	if client.IsNotFound(err) || (err == nil && branches.DefaultBranch == nil) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading branches", err.Error())
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

	state.ID = types.StringValue(state.Repository.ValueString())
	state.Branch = types.StringValue(*branches.DefaultBranch)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *BranchDefaultResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state branchDefaultResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !plan.Repository.Equal(state.Repository) {
		repository, err := lookupRepository(ctx, r.client, plan.Repository.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Error reading repository", err.Error())
			return
		}
		if !sameRepository(state.RepositoryID, repository) {
			resp.Diagnostics.AddAttributeError(path.Root("repository"), "Default branch can't be moved",
				fmt.Sprintf("%s is a different repository from the one this resource was created for. "+
					"Replace it with terraform apply -replace=<address>.", plan.Repository.ValueString()))
			return
		}
		plan.RepositoryID = types.Int64Value(repository.ID)
	}

	if !plan.Branch.Equal(state.Branch) {
		owner, repo, err := client.SplitFullName(plan.Repository.ValueString())
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("repository"), "Invalid repository", err.Error())
			return
		}
		resp.Diagnostics.Append(r.setDefault(ctx, owner, repo, plan.Repository.ValueString(), plan.Branch.ValueString(), nil)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	plan.ID = types.StringValue(plan.Repository.ValueString())
	plan.PreviousBranch = state.PreviousBranch
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete sets the default branch back to the one it replaced, so that a
// ghost_branch it pointed at can be deleted. Ghost has no way to unset it.
func (r *BranchDefaultResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state branchDefaultResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	previous := state.PreviousBranch.ValueString()
	if previous == "" || previous == state.Branch.ValueString() {
		return
	}

	owner, repo, err := client.SplitFullName(state.Repository.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("repository"), "Invalid repository", err.Error())
		return
	}

	branches, err := r.client.ListBranches(ctx, owner, repo)
	if client.IsNotFound(err) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading branches", err.Error())
		return
	}
	if !branches.Has(previous) {
		resp.Diagnostics.AddWarning("Default branch not restored",
			fmt.Sprintf("The previous default branch %q no longer exists in %s, so the default branch was left as %q.",
				previous, state.Repository.ValueString(), stringOrEmpty(branches.DefaultBranch)))
		return
	}
	if branches.DefaultBranch != nil && *branches.DefaultBranch == previous {
		return
	}

	_, err = r.client.UpdateRepository(ctx, owner, repo, client.UpdateRepositoryRequest{DefaultBranch: &previous})
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error restoring the default branch",
			fmt.Sprintf("Setting the default branch of %s back to %q failed: %s", state.Repository.ValueString(), previous, err))
	}
}

func (r *BranchDefaultResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, _, err := client.SplitFullName(req.ID); err != nil || strings.Contains(req.ID, ":") {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("import ID must be \"owner/repo\", got %q", req.ID))
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("repository"), req.ID)...)
}

// setDefault makes branch the default branch of owner/repo. branches, when
// given, is the current branch list, used to give a clear error for a branch
// that doesn't exist.
func (r *BranchDefaultResource) setDefault(ctx context.Context, owner, repo, fullName, branch string, branches *client.Branches) diag.Diagnostics {
	var diags diag.Diagnostics
	missing := func() {
		diags.AddAttributeError(path.Root("branch"), "Branch not found",
			fmt.Sprintf("Branch %q doesn't exist in %s, so it can't be the default branch. Branch names are case-sensitive. "+
				"To make a branch Terraform creates the default, set branch = ghost_branch.<name>.branch.", branch, fullName))
	}

	if branches != nil && !branches.Has(branch) {
		missing()
		return diags
	}

	_, err := r.client.UpdateRepository(ctx, owner, repo, client.UpdateRepositoryRequest{DefaultBranch: &branch})
	if client.IsNotFound(err) {
		missing()
		return diags
	}
	if err != nil {
		diags.AddError("Error setting the default branch", err.Error())
	}

	return diags
}

func stringOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
