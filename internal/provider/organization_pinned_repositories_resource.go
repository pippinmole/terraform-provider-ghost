package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
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
	_ resource.Resource                = &OrganizationPinnedRepositoriesResource{}
	_ resource.ResourceWithConfigure   = &OrganizationPinnedRepositoriesResource{}
	_ resource.ResourceWithImportState = &OrganizationPinnedRepositoriesResource{}
	_ resource.ResourceWithModifyPlan  = &OrganizationPinnedRepositoriesResource{}
)

// pinnedSlugPattern matches a repository slug as Ghost's pins endpoint takes
// it: Ghost matches slugs exactly, and they are always lower case, so an
// upper-case letter or an owner prefix can never match.
var pinnedSlugPattern = regexp.MustCompile(`^[^A-Z/]+$`)

// NewOrganizationPinnedRepositoriesResource returns the
// ghost_organization_pinned_repositories resource.
func NewOrganizationPinnedRepositoriesResource() resource.Resource {
	return &OrganizationPinnedRepositoriesResource{}
}

// OrganizationPinnedRepositoriesResource manages the ordered list of
// repositories pinned to an organization's profile. There is one per
// organization, and destroying it unpins every repository.
type OrganizationPinnedRepositoriesResource struct {
	client *client.Client
}

// organizationPinnedRepositoriesResourceModel maps the
// ghost_organization_pinned_repositories schema.
type organizationPinnedRepositoriesResourceModel struct {
	ID           types.String `tfsdk:"id"`
	Organization types.String `tfsdk:"organization"`
	Repositories types.List   `tfsdk:"repositories"`
}

func (r *OrganizationPinnedRepositoriesResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization_pinned_repositories"
}

func (r *OrganizationPinnedRepositoriesResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: fmt.Sprintf("Manages the repositories pinned to an organization's profile page, in the order "+
			"they're shown. The resource owns the whole list: there is one per organization, any repository pinned "+
			"outside Terraform is unpinned on the next apply, and you must be an admin of the organization. Up to %d "+
			"of the organization's own repositories can be pinned, private ones included (only people who can read a "+
			"private repository see it pinned). **Destroying the resource unpins every repository.** Ghost unpins a "+
			"repository when it's deleted. Pinned repositories of a user's profile can't be managed: Ghost's API has "+
			"no route for them.", client.MaxPinnedRepositories),
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The organization's slug, in lower case.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"organization": schema.StringAttribute{
				MarkdownDescription: "The organization's slug, matched case-insensitively and kept as configured. Changing it to another organization forces a new resource (which unpins the old organization's repositories); changing only its case doesn't.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					stringvalidator.RegexMatches(usernamePattern, `must not contain ":" or "/"`),
				},
			},
			"repositories": schema.ListAttribute{
				MarkdownDescription: fmt.Sprintf("The slugs of the organization's repositories to pin, in display order, at most %d. "+
					"Ghost matches slugs exactly, and a slug is the repository's name in lower case with spaces turned into "+
					"hyphens, so refer to `ghost_repository.<name>.slug` rather than `name`. A renamed repository stays pinned "+
					"under its new slug. Leave the list empty to unpin everything.", client.MaxPinnedRepositories),
				Required:    true,
				ElementType: types.StringType,
				Validators: []validator.List{
					listvalidator.SizeAtMost(client.MaxPinnedRepositories),
					listvalidator.UniqueValues(),
					listvalidator.ValueStringsAre(
						stringvalidator.LengthAtLeast(1),
						stringvalidator.RegexMatches(pinnedSlugPattern, "must be a repository slug, which is in lower case and has no owner (use ghost_repository.<name>.slug)"),
					),
				},
			},
		},
	}
}

func (r *OrganizationPinnedRepositoriesResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ModifyPlan forces a new resource for another organization, but not for a
// change of case.
func (r *OrganizationPinnedRepositoriesResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}

	var plan, state organizationPinnedRepositoriesResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !plan.Organization.Equal(state.Organization) &&
		(plan.Organization.IsUnknown() || !strings.EqualFold(plan.Organization.ValueString(), state.Organization.ValueString())) {
		resp.RequiresReplace.Append(path.Root("organization"))
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("id"), types.StringUnknown())...)
	}
}

func (r *OrganizationPinnedRepositoriesResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan organizationPinnedRepositoriesResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(r.pin(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *OrganizationPinnedRepositoriesResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state organizationPinnedRepositoriesResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The profile is public, so Ghost answers 404 only once the
	// organization is gone.
	o, err := r.client.GetOrganization(ctx, state.Organization.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading pinned repositories", err.Error())
		return
	}

	resp.Diagnostics.Append(state.fromRemote(o)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *OrganizationPinnedRepositoriesResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan organizationPinnedRepositoriesResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(r.pin(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete unpins every repository: the resource owns the organization's whole
// list of pins.
func (r *OrganizationPinnedRepositoriesResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state organizationPinnedRepositoriesResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := state.Organization.ValueString()
	err := r.client.SetOrganizationPins(ctx, org, nil)
	if isOrganizationNotFound(err) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error unpinning repositories", pinsError(org, err))
	}
}

// ImportState takes the organization's slug.
func (r *OrganizationPinnedRepositoriesResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	org := req.ID
	if org == "" || !usernamePattern.MatchString(org) {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Expected an organization slug, got %q.", req.ID))
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), strings.ToLower(org))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization"), org)...)
}

// pin replaces the organization's pins with m's repositories, and sets m's id.
func (r *OrganizationPinnedRepositoriesResource) pin(ctx context.Context, m *organizationPinnedRepositoriesResourceModel) diag.Diagnostics {
	var slugs []string
	diags := m.Repositories.ElementsAs(ctx, &slugs, false)
	if diags.HasError() {
		return diags
	}

	org := m.Organization.ValueString()
	if err := r.client.SetOrganizationPins(ctx, org, slugs); err != nil {
		diags.AddError("Error pinning repositories", pinsError(org, err))
		return diags
	}
	m.ID = types.StringValue(strings.ToLower(org))

	return diags
}

// fromRemote copies the organization's pinned repositories into m, in order.
func (m *organizationPinnedRepositoriesResourceModel) fromRemote(o *client.Organization) diag.Diagnostics {
	m.ID = types.StringValue(strings.ToLower(m.Organization.ValueString()))
	slugs := make([]string, 0, len(o.Pinned))
	for _, p := range o.Pinned {
		slugs = append(slugs, p.Slug)
	}

	var diags diag.Diagnostics
	m.Repositories, diags = types.ListValueFrom(context.Background(), types.StringType, slugs)

	return diags
}

// isOrganizationNotFound reports whether err is Ghost's 404 for an unknown
// organization, or one the token's user isn't in.
func isOrganizationNotFound(err error) bool {
	var apiErr *client.APIError
	return client.IsNotFound(err) && errors.As(err, &apiErr) && strings.HasPrefix(apiErr.Message, "Organization not found")
}

// pinsError explains a failed pins request.
func pinsError(org string, err error) string {
	switch {
	case isOrganizationNotFound(err):
		return fmt.Sprintf("Ghost has no organization %q that you're an admin of (%s). Check the slug: only an organization's admins can pin its repositories, and a user's login isn't an organization (Ghost can't pin repositories to a user's profile).", org, err)
	case client.IsNotFound(err):
		return fmt.Sprintf("At least one of the repositories isn't a repository of %q (%s), so nothing was pinned. Ghost matches repository slugs exactly: use the slug (ghost_repository.<name>.slug), not the name, and only the organization's own repositories can be pinned.", org, err)
	default:
		return err.Error()
	}
}
