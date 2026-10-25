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
	// labelNamePattern rejects leading and trailing whitespace, which Ghost
	// trims silently and would otherwise show as a perpetual diff.
	labelNamePattern = regexp.MustCompile(`(?s)^\S(.*\S)?$`)
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
	ID          types.String `tfsdk:"id"`
	Repository  types.String `tfsdk:"repository"`
	Name        types.String `tfsdk:"name"`
	Color       types.String `tfsdk:"color"`
	Description types.String `tfsdk:"description"`
	LabelID     types.Int64  `tfsdk:"label_id"`
	NodeID      types.String `tfsdk:"node_id"`
	URL         types.String `tfsdk:"url"`
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
				MarkdownDescription: "The repository's full name, `owner/slug`, e.g. `ghost_repository.example.full_name`. Changing it forces a new label.",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					stringvalidator.RegexMatches(fullNamePattern, `must be "owner/slug", e.g. "alice/my-repo"`),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The label name, up to 50 characters. Ghost matches label names case-sensitively, so `bug` and `Bug` are different labels. Renaming updates the label in place.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 50),
					stringvalidator.NoneOf(".", ".."),
					stringvalidator.RegexMatches(labelNamePattern, "must not start or end with whitespace"),
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

// ModifyPlan marks the attributes derived from the name as unknown when it
// changes, since UseStateForUnknown would otherwise keep their stale values.
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

	if !plan.Name.Equal(state.Name) {
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
