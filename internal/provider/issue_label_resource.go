package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

var (
	_ resource.Resource                = &IssueLabelResource{}
	_ resource.ResourceWithConfigure   = &IssueLabelResource{}
	_ resource.ResourceWithImportState = &IssueLabelResource{}
	_ resource.ResourceWithModifyPlan  = &IssueLabelResource{}
)

var (
	// untrimmedPattern rejects leading and trailing whitespace, which Ghost
	// trims silently from label and ruleset names, so what it stores would not
	// match the config. The class is the set JavaScript's
	// String.prototype.trim removes.
	untrimmedPattern = regexp.MustCompile(`(?s)^[^\s\p{Z}\x{0B}\x{FEFF}](.*[^\s\p{Z}\x{0B}\x{FEFF}])?$`)
	// labelColorPattern matches the six hex digits Ghost accepts, with an
	// optional leading "#" that Ghost strips.
	labelColorPattern = regexp.MustCompile(`^#?[0-9a-fA-F]{6}$`)
)

// NewIssueLabelResource returns the ghost_issue_label resource.
func NewIssueLabelResource() resource.Resource {
	return &IssueLabelResource{}
}

// IssueLabelResource manages an issue label in a Ghost repository.
type IssueLabelResource struct {
	client *client.Client
}

// issueLabelResourceModel maps the ghost_issue_label schema.
type issueLabelResourceModel struct {
	ID           types.String `tfsdk:"id"`
	Repository   types.String `tfsdk:"repository"`
	RepositoryID types.Int64  `tfsdk:"repository_id"`
	Name         types.String `tfsdk:"name"`
	Color        types.String `tfsdk:"color"`
	Description  types.String `tfsdk:"description"`
	LabelID      types.Int64  `tfsdk:"label_id"`
	NodeID       types.String `tfsdk:"node_id"`
	URL          types.String `tfsdk:"url"`
}

func (r *IssueLabelResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_issue_label"
}

func (r *IssueLabelResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	stableString := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an issue label in a Ghost repository.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The repository's full name and the label name, `owner/slug:name`.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
			"repository": schema.StringAttribute{
				MarkdownDescription: "The repository's full name, `owner/slug`, e.g. `ghost_repository.example.full_name`. Renaming the repository updates this in place; pointing it at a different repository forces a new label. Replacing the repository deletes its labels, so add `lifecycle { replace_triggered_by = [ghost_repository.example.repo_id] }` to recreate the label in the same apply.",
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
			"name": schema.StringAttribute{
				MarkdownDescription: "The label name, up to 50 characters. Ghost matches label names case-sensitively, so `bug` and `Bug` are different labels. Renaming updates the label in place.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.UTF8LengthBetween(1, 50),
					stringvalidator.NoneOf(".", ".."),
					stringvalidator.RegexMatches(untrimmedPattern, "must not start or end with whitespace"),
				},
			},
			"color": schema.StringAttribute{
				MarkdownDescription: "Six hex digits, with or without a leading `#`. Defaults to `ededed`.",
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("ededed"),
				Validators: []validator.String{
					stringvalidator.RegexMatches(labelColorPattern, `must be six hex digits, optionally prefixed with "#", e.g. "d73a4a"`),
				},
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "A short description of the label.",
				Optional:            true,
			},
			"label_id": schema.Int64Attribute{
				MarkdownDescription: "The numeric label ID.",
				Computed:            true,
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"node_id": schema.StringAttribute{
				MarkdownDescription: "The GraphQL node ID.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
			"url": schema.StringAttribute{
				MarkdownDescription: "The label's API URL.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
		},
	}
}

func (r *IssueLabelResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ModifyPlan marks the attributes derived from the name and repository as
// unknown when either changes, since UseStateForUnknown would otherwise keep
// their stale values. A repository change is applied in place when it is the
// same repository under a new name, and forces a new label otherwise.
func (r *IssueLabelResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}

	var plan, state issueLabelResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !plan.Repository.Equal(state.Repository) && !plan.Repository.IsUnknown() {
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

	if !plan.Name.Equal(state.Name) || !plan.Repository.Equal(state.Repository) {
		for _, attr := range []string{"id", "url"} {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(attr), types.StringUnknown())...)
		}
	}
}

func (r *IssueLabelResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan issueLabelResourceModel
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

	label, err := r.client.CreateLabel(ctx, owner, repo, client.CreateLabelRequest{
		Name:        plan.Name.ValueString(),
		Color:       normalizeLabelColor(plan.Color.ValueString()),
		Description: plan.Description.ValueStringPointer(),
	})
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnprocessableEntity {
		id := issueLabelID(plan.Repository.ValueString(), plan.Name.ValueString())
		resp.Diagnostics.AddError("Error creating label",
			fmt.Sprintf("Ghost rejected the label. If a label named %q already exists in %s, import it with: "+
				"terraform import <resource address> %q (%s)", plan.Name.ValueString(), plan.Repository.ValueString(), id, err))
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error creating label", err.Error())
		return
	}

	plan.fromRemote(label)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *IssueLabelResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state issueLabelResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	owner, repo, err := client.SplitFullName(state.Repository.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("repository"), "Invalid repository", err.Error())
		return
	}

	label, err := r.client.GetLabel(ctx, owner, repo, state.Name.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading label", err.Error())
		return
	}

	// An imported label has no repository_id yet.
	if state.RepositoryID.IsNull() {
		repository, err := r.client.GetRepository(ctx, owner, repo)
		if err != nil {
			resp.Diagnostics.AddError("Error reading repository", err.Error())
			return
		}
		state.RepositoryID = types.Int64Value(repository.ID)
	}

	state.fromRemote(label)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *IssueLabelResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state issueLabelResourceModel
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
			resp.Diagnostics.AddAttributeError(path.Root("repository"), "Label can't be moved",
				fmt.Sprintf("%s is a different repository from the one this label was created in. "+
					"Labels can't be moved; replace it with terraform apply -replace=<address>.", plan.Repository.ValueString()))
			return
		}
		plan.RepositoryID = types.Int64Value(repository.ID)
	}

	update := client.UpdateLabelRequest{
		Color:       normalizeLabelColor(plan.Color.ValueString()),
		Description: plan.Description.ValueStringPointer(),
	}
	if !plan.Name.Equal(state.Name) {
		update.NewName = plan.Name.ValueStringPointer()
	}

	label, err := r.client.UpdateLabel(ctx, owner, repo, state.Name.ValueString(), update)
	if err != nil {
		resp.Diagnostics.AddError("Error updating label", err.Error())
		return
	}

	plan.fromRemote(label)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *IssueLabelResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state issueLabelResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	owner, repo, err := client.SplitFullName(state.Repository.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("repository"), "Invalid repository", err.Error())
		return
	}

	if err := r.client.DeleteLabel(ctx, owner, repo, state.Name.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error deleting label", err.Error())
	}
}

func (r *IssueLabelResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	repository, name, err := parseIssueLabelID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("repository"), repository)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
}

// lookupRepository looks up the repository with the given "owner/slug" name.
func lookupRepository(ctx context.Context, c *client.Client, fullName string) (*client.Repository, error) {
	owner, repo, err := client.SplitFullName(fullName)
	if err != nil {
		return nil, err
	}

	return c.GetRepository(ctx, owner, repo)
}

// sameRepository reports whether repo is the repository a label or ruleset
// was created in, given the repository_id in state. A missing ID can't rule it out.
func sameRepository(stateRepoID types.Int64, repo *client.Repository) bool {
	return stateRepoID.IsNull() || stateRepoID.IsUnknown() || stateRepoID.ValueInt64() == repo.ID
}

// fromRemote copies label into m. The repository is kept as configured, and
// the configured color spelling is kept when it means the same color.
func (m *issueLabelResourceModel) fromRemote(label *client.Label) {
	m.Name = types.StringValue(label.Name)
	m.ID = types.StringValue(issueLabelID(m.Repository.ValueString(), label.Name))
	m.Color = labelColorFromRemote(m.Color, label.Color)
	m.Description = descriptionFromRemote(m.Description, label.Description)
	m.LabelID = types.Int64Value(label.ID)
	m.NodeID = types.StringValue(label.NodeID)
	m.URL = types.StringValue(label.URL)
}

// issueLabelID builds the "owner/slug:name" ID of a label.
func issueLabelID(repository, name string) string {
	return repository + ":" + name
}

// parseIssueLabelID parses an "owner/slug:name" ID. It splits at the first
// colon, since repository full names cannot contain one but label names can.
func parseIssueLabelID(id string) (repository, name string, err error) {
	repository, name, ok := strings.Cut(id, ":")
	if _, _, splitErr := client.SplitFullName(repository); !ok || name == "" || splitErr != nil {
		return "", "", fmt.Errorf("import ID must be \"owner/repo:name\", got %q", id)
	}

	return repository, name, nil
}

// normalizeLabelColor returns color the way Ghost stores it: lower case,
// without a leading "#".
func normalizeLabelColor(color string) string {
	return strings.ToLower(strings.TrimPrefix(color, "#"))
}

// labelColorFromRemote keeps the prior color when it is the same color as
// remote spelled differently (e.g. "#FF00AA" for "ff00aa"), so it does not
// produce a diff.
func labelColorFromRemote(prior types.String, remote string) types.String {
	if !prior.IsNull() && !prior.IsUnknown() && normalizeLabelColor(prior.ValueString()) == remote {
		return prior
	}

	return types.StringValue(remote)
}
