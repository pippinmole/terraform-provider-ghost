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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

var (
	_ resource.Resource                = &OrganizationSettingsResource{}
	_ resource.ResourceWithConfigure   = &OrganizationSettingsResource{}
	_ resource.ResourceWithImportState = &OrganizationSettingsResource{}
	_ resource.ResourceWithModifyPlan  = &OrganizationSettingsResource{}
)

// basePermissionNone stands for Ghost's null base permission: members get
// nothing beyond their teams and collaborations.
const basePermissionNone = "none"

// Ghost's limits on the settings' strings.
const (
	maxOrganizationDescription   = 350
	maxOrganizationLocation      = 100
	maxOrganizationDefaultBranch = 255
)

// NewOrganizationSettingsResource returns the ghost_organization_settings
// resource.
func NewOrganizationSettingsResource() resource.Resource {
	return &OrganizationSettingsResource{}
}

// OrganizationSettingsResource manages an organization's settings. There is
// one per organization, and destroying it leaves the settings as they are.
type OrganizationSettingsResource struct {
	client *client.Client
}

// organizationSettingsResourceModel maps the ghost_organization_settings
// schema.
type organizationSettingsResourceModel struct {
	ID                                  types.String `tfsdk:"id"`
	Organization                        types.String `tfsdk:"organization"`
	BasePermission                      types.String `tfsdk:"base_permission"`
	MembersCanCreatePublicRepositories  types.Bool   `tfsdk:"members_can_create_public_repositories"`
	MembersCanCreatePrivateRepositories types.Bool   `tfsdk:"members_can_create_private_repositories"`
	AllowPrivateForks                   types.Bool   `tfsdk:"allow_private_forks"`
	OAuthAppRestrictions                types.Bool   `tfsdk:"oauth_app_restrictions"`
	DefaultBranch                       types.String `tfsdk:"default_branch"`
	Description                         types.String `tfsdk:"description"`
	Website                             types.String `tfsdk:"website"`
	Location                            types.String `tfsdk:"location"`
	Email                               types.String `tfsdk:"email"`
}

func (r *OrganizationSettingsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization_settings"
}

func (r *OrganizationSettingsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keepString := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	keepBool := []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}
	unmanaged := " Leave it out to leave the setting unmanaged."
	clearable := " Set it to `\"\"` to clear it." + unmanaged

	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an organization's settings: its base permission, repository policies and public " +
			"profile. There is one per organization, and you must be an admin of it. Every argument is optional: an " +
			"argument you leave out is left as it is in Ghost and isn't managed, and taking an argument out of the " +
			"configuration stops managing it without changing it. **Destroying the resource doesn't change any " +
			"settings**: it only removes them from the Terraform state. The organization's display name and logo " +
			"can't be changed here.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The organization's slug, in lower case.",
				Computed:            true,
				PlanModifiers:       keepString,
			},
			"organization": schema.StringAttribute{
				MarkdownDescription: "The organization's slug, matched case-insensitively and kept as configured. Changing it to another organization forces a new resource (which leaves the old organization's settings as they are); changing only its case doesn't.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					stringvalidator.RegexMatches(usernamePattern, `must not contain ":" or "/"`),
				},
			},
			"base_permission": schema.StringAttribute{
				MarkdownDescription: "Every member's role on every repository: `read`, `triage`, `write`, `maintain` or `admin`, or `none` to give members nothing beyond their teams and collaborations." + unmanaged,
				Optional:            true,
				Computed:            true,
				PlanModifiers:       keepString,
				Validators:          []validator.String{stringvalidator.OneOf(basePermissionNone, "read", "triage", "write", "maintain", "admin")},
			},
			"members_can_create_public_repositories": schema.BoolAttribute{
				MarkdownDescription: "Whether members can create public repositories." + unmanaged,
				Optional:            true,
				Computed:            true,
				PlanModifiers:       keepBool,
			},
			"members_can_create_private_repositories": schema.BoolAttribute{
				MarkdownDescription: "Whether members can create private repositories." + unmanaged,
				Optional:            true,
				Computed:            true,
				PlanModifiers:       keepBool,
			},
			"allow_private_forks": schema.BoolAttribute{
				MarkdownDescription: "Whether the organization's private repositories can be forked." + unmanaged,
				Optional:            true,
				Computed:            true,
				PlanModifiers:       keepBool,
			},
			"oauth_app_restrictions": schema.BoolAttribute{
				MarkdownDescription: "Whether OAuth apps reach the organization's repositories only once an admin approves them. Apps built into Ghost, such as `gh`, and the organization's own apps are always allowed." + unmanaged,
				Optional:            true,
				Computed:            true,
				PlanModifiers:       keepBool,
			},
			"default_branch": schema.StringAttribute{
				MarkdownDescription: fmt.Sprintf("The default branch of each new repository, at most %d characters. Ghost doesn't check that it's a valid branch name, and existing repositories keep theirs.", maxOrganizationDefaultBranch) + clearable,
				Optional:            true,
				Computed:            true,
				PlanModifiers:       keepString,
				Validators:          []validator.String{stringvalidator.UTF8LengthAtMost(maxOrganizationDefaultBranch)},
			},
			"description": schema.StringAttribute{
				MarkdownDescription: fmt.Sprintf("The description on the organization's profile, at most %d characters.", maxOrganizationDescription) + clearable,
				Optional:            true,
				Computed:            true,
				PlanModifiers:       keepString,
				Validators:          []validator.String{stringvalidator.UTF8LengthAtMost(maxOrganizationDescription)},
			},
			"website": schema.StringAttribute{
				MarkdownDescription: "The website on the organization's profile. Ghost requires a URL with a scheme, such as `https://example.com`." + clearable,
				Optional:            true,
				Computed:            true,
				PlanModifiers:       keepString,
			},
			"location": schema.StringAttribute{
				MarkdownDescription: fmt.Sprintf("The location on the organization's profile, at most %d characters.", maxOrganizationLocation) + clearable,
				Optional:            true,
				Computed:            true,
				PlanModifiers:       keepString,
				Validators:          []validator.String{stringvalidator.UTF8LengthAtMost(maxOrganizationLocation)},
			},
			"email": schema.StringAttribute{
				MarkdownDescription: "The public email address on the organization's profile." + clearable,
				Optional:            true,
				Computed:            true,
				PlanModifiers:       keepString,
			},
		},
	}
}

func (r *OrganizationSettingsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
func (r *OrganizationSettingsResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}

	var plan, state organizationSettingsResourceModel
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

func (r *OrganizationSettingsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan organizationSettingsResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Without a prior state, the arguments left out are unknown.
	resp.Diagnostics.Append(r.apply(ctx, &plan, settingsUpdate(&plan, nil))...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *OrganizationSettingsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state organizationSettingsResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Ghost answers 404 once the organization is gone, or the token's user
	// has left it.
	s, err := r.client.GetOrganizationSettings(ctx, state.Organization.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading organization settings", err.Error())
		return
	}

	state.fromRemote(s)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *OrganizationSettingsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state organizationSettingsResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(r.apply(ctx, &plan, settingsUpdate(&plan, &state))...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete leaves the settings as they are: an organization always has
// settings, and Ghost's defaults would undo whatever the organization had
// before Terraform managed it.
func (r *OrganizationSettingsResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
}

// ImportState takes the organization's slug.
func (r *OrganizationSettingsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	org := req.ID
	if org == "" || !usernamePattern.MatchString(org) {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Expected an organization slug, got %q.", req.ID))
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), strings.ToLower(org))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization"), org)...)
}

// apply sends update and stores the settings Ghost returns in m.
func (r *OrganizationSettingsResource) apply(ctx context.Context, m *organizationSettingsResourceModel, update client.UpdateOrganizationSettingsRequest) diag.Diagnostics {
	var diags diag.Diagnostics
	org := m.Organization.ValueString()
	s, err := r.client.UpdateOrganizationSettings(ctx, org, update)
	if err != nil {
		summary := "Error changing organization settings"
		if update.IsEmpty() {
			summary = "Error reading organization settings"
		}
		diags.AddError(summary, organizationSettingsError(org, err))
		return diags
	}

	m.fromRemote(s)
	return diags
}

// settingsUpdate returns the changes from state to plan. With no state, it
// returns every argument that is set.
func settingsUpdate(plan, state *organizationSettingsResourceModel) client.UpdateOrganizationSettingsRequest {
	var old organizationSettingsResourceModel
	if state != nil {
		old = *state
	}

	return client.UpdateOrganizationSettingsRequest{
		BasePermission:                      basePermissionChange(plan.BasePermission, old.BasePermission),
		MembersCanCreatePublicRepositories:  boolChange(plan.MembersCanCreatePublicRepositories, old.MembersCanCreatePublicRepositories),
		MembersCanCreatePrivateRepositories: boolChange(plan.MembersCanCreatePrivateRepositories, old.MembersCanCreatePrivateRepositories),
		AllowPrivateForks:                   boolChange(plan.AllowPrivateForks, old.AllowPrivateForks),
		OAuthAppRestrictions:                boolChange(plan.OAuthAppRestrictions, old.OAuthAppRestrictions),
		DefaultBranch:                       clearableChange(plan.DefaultBranch, old.DefaultBranch),
		Description:                         clearableChange(plan.Description, old.Description),
		Website:                             clearableChange(plan.Website, old.Website),
		Location:                            clearableChange(plan.Location, old.Location),
		Email:                               clearableChange(plan.Email, old.Email),
	}
}

// boolChange sets the field when the planned value is known and differs
// from the old one.
func boolChange(plan, old types.Bool) client.Nullable[bool] {
	if plan.IsNull() || plan.IsUnknown() || plan.Equal(old) {
		return client.Nullable[bool]{}
	}

	return client.Value(plan.ValueBool())
}

// clearableChange sets the field when the planned value is known and
// differs from the old one, sending "" as null, which clears it.
func clearableChange(plan, old types.String) client.Nullable[string] {
	if plan.IsNull() || plan.IsUnknown() || plan.Equal(old) {
		return client.Nullable[string]{}
	}
	if plan.ValueString() == "" {
		return client.Null[string]()
	}

	return client.Value(plan.ValueString())
}

// basePermissionChange is like clearableChange, but "none" clears the
// field.
func basePermissionChange(plan, old types.String) client.Nullable[string] {
	if plan.IsNull() || plan.IsUnknown() || plan.Equal(old) {
		return client.Nullable[string]{}
	}
	if plan.ValueString() == basePermissionNone {
		return client.Null[string]()
	}

	return client.Value(plan.ValueString())
}

// fromRemote copies Ghost's settings into m. A null base permission is
// "none", and a null string is "".
func (m *organizationSettingsResourceModel) fromRemote(s *client.OrganizationSettings) {
	m.ID = types.StringValue(strings.ToLower(m.Organization.ValueString()))
	m.BasePermission = types.StringValue(basePermissionNone)
	if s.BasePermission != nil {
		m.BasePermission = types.StringValue(*s.BasePermission)
	}
	m.MembersCanCreatePublicRepositories = types.BoolValue(s.MembersCanCreatePublicRepositories)
	m.MembersCanCreatePrivateRepositories = types.BoolValue(s.MembersCanCreatePrivateRepositories)
	m.AllowPrivateForks = types.BoolValue(s.AllowPrivateForks)
	m.OAuthAppRestrictions = types.BoolValue(s.OAuthAppRestrictions)
	m.DefaultBranch = types.StringValue(derefString(s.DefaultBranch))
	m.Description = types.StringValue(derefString(s.Description))
	m.Website = types.StringValue(derefString(s.Website))
	m.Location = types.StringValue(derefString(s.Location))
	m.Email = types.StringValue(derefString(s.Email))
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}

// organizationSettingsError explains a failed settings request.
func organizationSettingsError(org string, err error) string {
	if client.IsNotFound(err) {
		return fmt.Sprintf("Ghost has no organization %q that you're an admin of (%s). Check the slug: only an organization's admins can see its settings, and a user's login isn't an organization.", org, err)
	}

	return err.Error()
}
