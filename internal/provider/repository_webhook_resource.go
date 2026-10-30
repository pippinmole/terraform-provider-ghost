package provider

import (
	"context"
	"fmt"
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
	_ resource.Resource                = &RepositoryWebhookResource{}
	_ resource.ResourceWithConfigure   = &RepositoryWebhookResource{}
	_ resource.ResourceWithImportState = &RepositoryWebhookResource{}
	_ resource.ResourceWithModifyPlan  = &RepositoryWebhookResource{}
)

// NewRepositoryWebhookResource returns the ghost_repository_webhook resource.
func NewRepositoryWebhookResource() resource.Resource {
	return &RepositoryWebhookResource{}
}

// RepositoryWebhookResource manages a webhook on a Ghost repository.
type RepositoryWebhookResource struct {
	client *client.Client
}

// repositoryWebhookResourceModel maps the ghost_repository_webhook schema.
type repositoryWebhookResourceModel struct {
	ID           types.String `tfsdk:"id"`
	Repository   types.String `tfsdk:"repository"`
	RepositoryID types.Int64  `tfsdk:"repository_id"`
	webhookModel
}

func (r *RepositoryWebhookResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_repository_webhook"
}

func (r *RepositoryWebhookResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := webhookSchemaAttributes("repository")
	attrs["id"] = schema.StringAttribute{
		MarkdownDescription: "The repository's full name and the webhook ID, `owner/slug:webhook_id`.",
		Computed:            true,
		PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
	attrs["repository"] = schema.StringAttribute{
		MarkdownDescription: "The repository's full name, `owner/slug`, e.g. `ghost_repository.example.full_name`. Renaming the repository updates this in place; pointing it at a different repository forces a new webhook. Replacing the repository deletes its webhooks, so add `lifecycle { replace_triggered_by = [ghost_repository.example.repo_id] }` to recreate the webhook in the same apply.",
		Required:            true,
		Validators: []validator.String{
			stringvalidator.RegexMatches(fullNamePattern, `must be "owner/slug", e.g. "alice/my-repo"`),
		},
	}
	attrs["repository_id"] = schema.Int64Attribute{
		MarkdownDescription: "The numeric ID of `repository`.",
		Computed:            true,
		PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a webhook on a Ghost repository. Ghost sends the URL a `ping` as soon as the webhook is created, " +
			"and signs every delivery with `secret`. You must be an admin of the repository.",
		Attributes: attrs,
	}
}

func (r *RepositoryWebhookResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
// repository under a new name, and forces a new webhook otherwise. It also
// marks the attributes an update will change as unknown.
func (r *RepositoryWebhookResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}

	var plan, state repositoryWebhookResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !plan.Repository.Equal(state.Repository) {
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

	modifyWebhookPlan(ctx, &plan.webhookModel, &state.webhookModel, resp)
}

func (r *RepositoryWebhookResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan repositoryWebhookResourceModel
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

	resp.Diagnostics.Append(createWebhook(ctx, r.client.RepositoryWebhooks(owner, repo), &plan.webhookModel)...)
	if plan.WebhookID.IsNull() || plan.WebhookID.IsUnknown() {
		return
	}
	plan.ID = types.StringValue(repositoryWebhookID(plan.Repository.ValueString(), plan.WebhookID.ValueString()))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *RepositoryWebhookResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state repositoryWebhookResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	owner, repo, err := client.SplitFullName(state.Repository.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("repository"), "Invalid repository", err.Error())
		return
	}

	hook, err := r.client.RepositoryWebhooks(owner, repo).Get(ctx, state.WebhookID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading webhook", err.Error())
		return
	}

	// An imported webhook has no repository_id yet.
	if state.RepositoryID.IsNull() {
		repository, err := r.client.GetRepository(ctx, owner, repo)
		if err != nil {
			resp.Diagnostics.AddError("Error reading repository", err.Error())
			return
		}
		state.RepositoryID = types.Int64Value(repository.ID)
	}

	resp.Diagnostics.Append(state.fromRemote(ctx, hook)...)
	state.ID = types.StringValue(repositoryWebhookID(state.Repository.ValueString(), hook.ID))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *RepositoryWebhookResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state repositoryWebhookResourceModel
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
			resp.Diagnostics.AddAttributeError(path.Root("repository"), "Webhook can't be moved",
				fmt.Sprintf("%s is a different repository from the one this webhook was created in. "+
					"Webhooks can't be moved; replace it with terraform apply -replace=<address>.", plan.Repository.ValueString()))
			return
		}
		plan.RepositoryID = types.Int64Value(repository.ID)
	}

	resp.Diagnostics.Append(updateWebhook(ctx, r.client.RepositoryWebhooks(owner, repo), &plan.webhookModel, &state.webhookModel)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = types.StringValue(repositoryWebhookID(plan.Repository.ValueString(), plan.WebhookID.ValueString()))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *RepositoryWebhookResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state repositoryWebhookResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	owner, repo, err := client.SplitFullName(state.Repository.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("repository"), "Invalid repository", err.Error())
		return
	}

	err = r.client.RepositoryWebhooks(owner, repo).Delete(ctx, state.WebhookID.ValueString())
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error deleting webhook", err.Error())
	}
}

func (r *RepositoryWebhookResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	repository, webhookID, err := parseRepositoryWebhookID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("repository"), repository)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("webhook_id"), webhookID)...)
}

// repositoryWebhookID builds the "owner/slug:webhook_id" ID of a webhook.
func repositoryWebhookID(repository, webhookID string) string {
	return repository + ":" + webhookID
}

// parseRepositoryWebhookID parses an "owner/slug:webhook_id" ID.
func parseRepositoryWebhookID(id string) (repository, webhookID string, err error) {
	repository, webhookID, ok := strings.Cut(id, ":")
	if _, _, splitErr := client.SplitFullName(repository); !ok || webhookID == "" || strings.ContainsAny(webhookID, ":/") || splitErr != nil {
		return "", "", fmt.Errorf("import ID must be \"owner/repo:webhook_id\", e.g. \"alice/my-repo:whk_abc123\", got %q", id)
	}

	return repository, webhookID, nil
}
