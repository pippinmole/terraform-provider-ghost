package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

var (
	_ resource.Resource                = &OrganizationWebhookResource{}
	_ resource.ResourceWithConfigure   = &OrganizationWebhookResource{}
	_ resource.ResourceWithImportState = &OrganizationWebhookResource{}
	_ resource.ResourceWithModifyPlan  = &OrganizationWebhookResource{}
)

// NewOrganizationWebhookResource returns the ghost_organization_webhook
// resource.
func NewOrganizationWebhookResource() resource.Resource {
	return &OrganizationWebhookResource{}
}

// OrganizationWebhookResource manages a webhook on a Ghost organization,
// which receives the events of every repository in it.
type OrganizationWebhookResource struct {
	client *client.Client
}

// organizationWebhookResourceModel maps the ghost_organization_webhook
// schema.
type organizationWebhookResourceModel struct {
	ID           types.String `tfsdk:"id"`
	Organization types.String `tfsdk:"organization"`
	webhookModel
}

func (r *OrganizationWebhookResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization_webhook"
}

func (r *OrganizationWebhookResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := webhookSchemaAttributes("organization")
	attrs["id"] = schema.StringAttribute{
		MarkdownDescription: "The organization's slug in lower case and the webhook ID, `organization:webhook_id`.",
		Computed:            true,
		PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
	attrs["organization"] = schema.StringAttribute{
		MarkdownDescription: "The organization's slug, matched case-insensitively and kept as configured. Changing it to another organization forces a new webhook; changing only its case doesn't.",
		Required:            true,
		Validators: []validator.String{
			stringvalidator.LengthAtLeast(1),
			stringvalidator.RegexMatches(usernamePattern, `must not contain ":" or "/"`),
		},
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a webhook on a Ghost organization. It receives the events of every repository the organization owns, " +
			"including repositories created later. Ghost sends the URL a `ping` as soon as the webhook is created, " +
			"and signs every delivery with `secret`. You must be an admin of the organization.",
		Attributes: attrs,
	}
}

func (r *OrganizationWebhookResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ModifyPlan forces a new webhook for another organization, but not for a
// change of case. It also marks the attributes an update will change as
// unknown.
func (r *OrganizationWebhookResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}

	var plan, state organizationWebhookResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !plan.Organization.Equal(state.Organization) &&
		(plan.Organization.IsUnknown() || !strings.EqualFold(plan.Organization.ValueString(), state.Organization.ValueString())) {
		resp.RequiresReplace.Append(path.Root("organization"))
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("id"), types.StringUnknown())...)
		return
	}

	modifyWebhookPlan(ctx, &plan.webhookModel, &state.webhookModel, resp)
}

func (r *OrganizationWebhookResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan organizationWebhookResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := plan.Organization.ValueString()
	hooks := r.client.OrganizationWebhooks(org)

	// Listing first turns an unknown organization into a helpful error,
	// rather than Ghost's bare "Organization not found".
	if _, err := hooks.List(ctx); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("organization"), "Error reading organization webhooks", organizationWebhookError(org, err))
		return
	}

	resp.Diagnostics.Append(createWebhook(ctx, hooks, &plan.webhookModel)...)
	if plan.WebhookID.IsNull() || plan.WebhookID.IsUnknown() {
		return
	}
	plan.ID = types.StringValue(organizationWebhookID(org, plan.WebhookID.ValueString()))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *OrganizationWebhookResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state organizationWebhookResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Ghost answers 404 once the webhook or the organization is gone, or
	// the token's user has left the organization.
	org := state.Organization.ValueString()
	hook, err := r.client.OrganizationWebhooks(org).Get(ctx, state.WebhookID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading webhook", err.Error())
		return
	}

	resp.Diagnostics.Append(state.fromRemote(ctx, hook)...)
	state.ID = types.StringValue(organizationWebhookID(org, hook.ID))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *OrganizationWebhookResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state organizationWebhookResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := plan.Organization.ValueString()
	resp.Diagnostics.Append(updateWebhook(ctx, r.client.OrganizationWebhooks(org), &plan.webhookModel, &state.webhookModel)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = types.StringValue(organizationWebhookID(org, plan.WebhookID.ValueString()))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *OrganizationWebhookResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state organizationWebhookResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.OrganizationWebhooks(state.Organization.ValueString()).Delete(ctx, state.WebhookID.ValueString())
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error deleting webhook", err.Error())
	}
}

// ImportState takes "organization:webhook_id".
func (r *OrganizationWebhookResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	org, webhookID, err := parseOrganizationWebhookID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), organizationWebhookID(org, webhookID))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization"), org)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("webhook_id"), webhookID)...)
}

// organizationWebhookID builds the "organization:webhook_id" ID of a webhook.
// The slug is lower-cased, since Ghost matches it case-insensitively.
func organizationWebhookID(org, webhookID string) string {
	return strings.ToLower(org) + ":" + webhookID
}

// parseOrganizationWebhookID parses an "organization:webhook_id" ID.
func parseOrganizationWebhookID(id string) (org, webhookID string, err error) {
	org, webhookID, ok := strings.Cut(id, ":")
	if !ok || org == "" || webhookID == "" || strings.Contains(org, "/") || strings.ContainsAny(webhookID, ":/") {
		return "", "", fmt.Errorf("import ID must be \"organization:webhook_id\", e.g. \"my-org:whk_abc123\", got %q", id)
	}

	return org, webhookID, nil
}

// organizationWebhookError explains a failed request for an organization's
// webhooks.
func organizationWebhookError(org string, err error) string {
	if client.IsNotFound(err) {
		return fmt.Sprintf("Ghost has no organization %q that you're a member of (%s). Check the slug: a user's login isn't an organization, "+
			"and Ghost answers as if organizations you aren't in didn't exist. Only an organization's admins can manage its webhooks.", org, err)
	}

	return err.Error()
}
