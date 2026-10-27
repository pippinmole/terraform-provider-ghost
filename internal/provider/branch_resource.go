package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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
	_ resource.Resource                = &BranchResource{}
	_ resource.ResourceWithConfigure   = &BranchResource{}
	_ resource.ResourceWithImportState = &BranchResource{}
	_ resource.ResourceWithModifyPlan  = &BranchResource{}
)

// noColonPattern rejects ":", which git doesn't allow in branch names and
// which separates the repository from the branch in the ID.
var noColonPattern = regexp.MustCompile(`^[^:]*$`)

// NewBranchResource returns the ghost_branch resource.
func NewBranchResource() resource.Resource {
	return &BranchResource{}
}

// BranchResource manages a branch in a Ghost repository.
type BranchResource struct {
	client *client.Client
}

// branchResourceModel maps the ghost_branch schema.
type branchResourceModel struct {
	ID           types.String `tfsdk:"id"`
	Repository   types.String `tfsdk:"repository"`
	RepositoryID types.Int64  `tfsdk:"repository_id"`
	Branch       types.String `tfsdk:"branch"`
	Source       types.String `tfsdk:"source"`
	SHA          types.String `tfsdk:"sha"`
}

func (r *BranchResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_branch"
}

func (r *BranchResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	const sourceReplaceDescription = "Changing this forces a new branch, unless it was previously unset (e.g. after import)."

	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a branch in a Ghost repository. Ghost can't rename branches, so changing `branch` creates a new one and deletes the old one.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The repository's full name and the branch name, `owner/slug:branch`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"repository": schema.StringAttribute{
				MarkdownDescription: "The repository's full name, `owner/slug`, e.g. `ghost_repository.example.full_name`. The repository must have at least one commit, e.g. from `auto_init`. Renaming the repository updates this in place; pointing it at a different repository forces a new branch. Replacing the repository deletes its branches, so add `lifecycle { replace_triggered_by = [ghost_repository.example.repo_id] }` to recreate the branch in the same apply.",
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
				MarkdownDescription: "The branch name, without the `refs/heads/` prefix, e.g. `feat/login`. Branch names are case-sensitive. Changing it forces a new branch.",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					stringvalidator.RegexMatches(noColonPattern, `must not contain ":"`),
					branchNameValidator{},
				},
			},
			"source": schema.StringAttribute{
				MarkdownDescription: "The branch, tag or commit SHA to start the branch at. Defaults to the repository's default branch. " + sourceReplaceDescription + " A source taken from another branch's `sha` changes whenever that branch moves, which replaces this branch; use the other branch's name to start from it once.",
				Optional:            true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIf(createOnlyStringReplace,
					sourceReplaceDescription, sourceReplaceDescription)},
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"sha": schema.StringAttribute{
				MarkdownDescription: "The SHA of the commit the branch points at. It follows pushes to the branch.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *BranchResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
// repository under a new name, and forces a new branch otherwise.
func (r *BranchResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}

	var plan, state branchResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if plan.Repository.Equal(state.Repository) {
		return
	}

	if !plan.Repository.IsUnknown() {
		// A known new value can be checked now. An unknown one is usually a
		// ghost_repository being renamed, and Update checks it once known.
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

func (r *BranchResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan branchResourceModel
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

	branch, err := r.client.CreateBranch(ctx, owner, repo, client.CreateBranchRequest{
		Name: plan.Branch.ValueString(),
		From: plan.Source.ValueString(),
	})
	if client.IsConflict(err) {
		id := branchID(plan.Repository.ValueString(), plan.Branch.ValueString())
		resp.Diagnostics.AddError("Branch already exists",
			fmt.Sprintf("Branch %q already exists in %s. Import it with: terraform import <resource address> %q",
				plan.Branch.ValueString(), plan.Repository.ValueString(), id))
		return
	}
	if client.IsNotFound(err) {
		source := "the default branch"
		if !plan.Source.IsNull() {
			source = fmt.Sprintf("%q", plan.Source.ValueString())
		}
		resp.Diagnostics.AddError("Error creating branch",
			fmt.Sprintf("Can't start the branch at %s: it doesn't exist in %s. A repository with no commits has no "+
				"branches to start from; create it with auto_init = true. (%s)", source, plan.Repository.ValueString(), err))
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error creating branch", err.Error())
		return
	}

	plan.ID = types.StringValue(branchID(plan.Repository.ValueString(), branch.Name))
	plan.SHA = types.StringValue(branch.SHA)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *BranchResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state branchResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	owner, repo, err := client.SplitFullName(state.Repository.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("repository"), "Invalid repository", err.Error())
		return
	}

	// There's no single-branch endpoint, so check the list.
	branches, err := r.client.ListBranches(ctx, owner, repo)
	if client.IsNotFound(err) || (err == nil && !branches.Has(state.Branch.ValueString())) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading branches", err.Error())
		return
	}

	// The full ref, so a tag of the same name can't shadow the branch.
	status, err := r.client.GetCommitStatus(ctx, owner, repo, "refs/heads/"+state.Branch.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading branch commit", err.Error())
		return
	}

	// An imported branch has no repository_id yet.
	if state.RepositoryID.IsNull() {
		repository, err := r.client.GetRepository(ctx, owner, repo)
		if err != nil {
			resp.Diagnostics.AddError("Error reading repository", err.Error())
			return
		}
		state.RepositoryID = types.Int64Value(repository.ID)
	}

	state.ID = types.StringValue(branchID(state.Repository.ValueString(), state.Branch.ValueString()))
	state.SHA = types.StringValue(status.SHA)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update handles the only in-place changes: the repository being renamed, and
// source being set after an import. Neither touches the branch itself.
func (r *BranchResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state branchResourceModel
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
			resp.Diagnostics.AddAttributeError(path.Root("repository"), "Branch can't be moved",
				fmt.Sprintf("%s is a different repository from the one this branch was created in. "+
					"Branches can't be moved; replace it with terraform apply -replace=<address>.", plan.Repository.ValueString()))
			return
		}
		plan.RepositoryID = types.Int64Value(repository.ID)
	}

	plan.ID = types.StringValue(branchID(plan.Repository.ValueString(), plan.Branch.ValueString()))
	plan.SHA = state.SHA
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *BranchResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state branchResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	owner, repo, err := client.SplitFullName(state.Repository.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("repository"), "Invalid repository", err.Error())
		return
	}

	err = r.client.DeleteBranch(ctx, owner, repo, state.Branch.ValueString())
	if client.IsConflict(err) {
		resp.Diagnostics.AddError("Error deleting branch",
			fmt.Sprintf("Ghost won't delete branch %q while it is the repository's default branch or an open pull "+
				"request compares it. Change the default branch or close the pull request first. (%s)", state.Branch.ValueString(), err))
		return
	}
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error deleting branch", err.Error())
	}
}

func (r *BranchResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	repository, branch, err := parseBranchID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("repository"), repository)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("branch"), branch)...)
}

// branchNameValidator rejects a branch given as a full ref. Ghost would take
// it literally and create refs/heads/refs/heads/….
type branchNameValidator struct{}

func (v branchNameValidator) Description(_ context.Context) string {
	return `must not start with "refs/"`
}

func (v branchNameValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v branchNameValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	name := req.ConfigValue.ValueString()
	switch {
	case strings.HasPrefix(name, "refs/heads/"):
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid branch name",
			fmt.Sprintf("Give the branch name without the refs/heads/ prefix, e.g. %q instead of %q.",
				strings.TrimPrefix(name, "refs/heads/"), name))
	case strings.HasPrefix(name, "refs/"):
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid branch name",
			fmt.Sprintf("%q is a full ref, not a branch name: the branch name must not start with \"refs/\".", name))
	}
}

// branchID builds the "owner/slug:branch" ID of a branch.
func branchID(repository, branch string) string {
	return repository + ":" + branch
}

// parseBranchID parses an "owner/slug:branch" ID. Git doesn't allow ":" in
// branch names, and repository full names can't contain one either.
func parseBranchID(id string) (repository, branch string, err error) {
	repository, branch, ok := strings.Cut(id, ":")
	if _, _, splitErr := client.SplitFullName(repository); !ok || branch == "" || strings.Contains(branch, ":") || splitErr != nil {
		return "", "", fmt.Errorf("import ID must be \"owner/repo:branch\", got %q", id)
	}

	return repository, branch, nil
}
