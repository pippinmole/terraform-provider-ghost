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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

var (
	_ resource.Resource                = &OrganizationPublicMembershipResource{}
	_ resource.ResourceWithConfigure   = &OrganizationPublicMembershipResource{}
	_ resource.ResourceWithImportState = &OrganizationPublicMembershipResource{}
)

// NewOrganizationPublicMembershipResource returns the
// ghost_organization_public_membership resource.
func NewOrganizationPublicMembershipResource() resource.Resource {
	return &OrganizationPublicMembershipResource{}
}

// OrganizationPublicMembershipResource manages whether the token user's
// membership of an organization is public.
type OrganizationPublicMembershipResource struct {
	client *client.Client
}

// organizationPublicMembershipResourceModel maps the
// ghost_organization_public_membership schema.
type organizationPublicMembershipResourceModel struct {
	ID           types.String `tfsdk:"id"`
	Organization types.String `tfsdk:"organization"`
	Public       types.Bool   `tfsdk:"public"`
	Username     types.String `tfsdk:"username"`
}

func (r *OrganizationPublicMembershipResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization_public_membership"
}

func (r *OrganizationPublicMembershipResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Makes the membership of an organization public or private for the user the provider's token " +
			"belongs to. A public membership shows that user on the organization's People tab to everyone, and the " +
			"organization on their profile; a private one is seen only by the organization's other members.\n\n" +
			"Ghost only lets a user change their own membership, so this resource has no username argument and can't " +
			"publicize anyone else; the user must already be a member of the organization. " +
			"`public` defaults to `true`, so the resource's presence makes the membership public; set it to `false` to " +
			"keep it private and have Terraform put it back if someone makes it public. **Destroying the resource " +
			"always makes the membership private**, whatever it was before Terraform. A membership flipped outside Terraform shows up " +
			"as drift, and the resource drops out of state if the user leaves the organization.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The organization's slug, in lower case.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"organization": schema.StringAttribute{
				MarkdownDescription: "The organization's slug, matched case-insensitively and kept as configured. Changing it to another organization forces a new resource (making the old membership private); changing only its case doesn't.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					stringvalidator.RegexMatches(usernamePattern, `must not contain ":" or "/"`),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplaceIf(ownerReplace,
						"Changing the organization, other than its letter case, forces a new resource.",
						"Changing the organization, other than its letter case, forces a new resource."),
				},
			},
			"public": schema.BoolAttribute{
				MarkdownDescription: "Whether the membership is public. Defaults to `true`. Changing it updates the resource in place.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
			},
			"username": schema.StringAttribute{
				MarkdownDescription: "The login of the token's user, whose membership this is.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *OrganizationPublicMembershipResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *OrganizationPublicMembershipResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan organizationPublicMembershipResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(r.set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *OrganizationPublicMembershipResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state organizationPublicMembershipResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	me, err := r.client.GetAuthenticatedUser(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error reading the token's user", err.Error())
		return
	}

	org, err := r.client.GetOrganization(ctx, state.Organization.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading organization", err.Error())
		return
	}

	member := findMember(org, me.Login)
	if member == nil {
		// The user left the organization: there is no membership.
		resp.State.RemoveResource(ctx)
		return
	}

	state.ID = types.StringValue(strings.ToLower(org.Slug))
	state.Username = types.StringValue(me.Login)
	state.Public = types.BoolValue(member.Public)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *OrganizationPublicMembershipResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state organizationPublicMembershipResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if plan.Public.Equal(state.Public) {
		// Only the organization's letter case changed.
		plan.ID = state.ID
		plan.Username = state.Username
	} else {
		resp.Diagnostics.Append(r.set(ctx, &plan)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *OrganizationPublicMembershipResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state organizationPublicMembershipResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Ghost answers 404 when the organization is gone or the user has left
	// it, and 204 when the membership is already private.
	org := state.Organization.ValueString()
	err := r.client.SetOrganizationMembershipPublic(ctx, org, false)
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error making the organization membership private",
			fmt.Sprintf("Making %s's membership of %s private: %s", state.Username.ValueString(), org, err))
	}
}

// ImportState takes the organization's slug.
func (r *OrganizationPublicMembershipResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	org := req.ID
	if org == "" || !usernamePattern.MatchString(org) {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Expected an organization slug, got %q.", req.ID))
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), strings.ToLower(org))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization"), org)...)
}

// set makes the membership in m public or private, as m says, and fills in
// m's computed attributes. Ghost answers 404 both for an unknown
// organization and for one the user isn't a member of, so a 404 is
// explained by reading the organization.
func (r *OrganizationPublicMembershipResource) set(ctx context.Context, m *organizationPublicMembershipResourceModel) diag.Diagnostics {
	var diags diag.Diagnostics
	org := m.Organization.ValueString()

	me, err := r.client.GetAuthenticatedUser(ctx)
	if err != nil {
		diags.AddError("Error reading the token's user", err.Error())
		return diags
	}

	err = r.client.SetOrganizationMembershipPublic(ctx, org, m.Public.ValueBool())
	if client.IsNotFound(err) {
		diags.AddAttributeError(path.Root("organization"), "Organization membership not found",
			publicMembershipNotFound(ctx, r.client, org, me.Login, err))
		return diags
	}
	if err != nil {
		diags.AddError("Error setting organization membership visibility",
			fmt.Sprintf("Making %s's membership of %s %s: %s", me.Login, org, visibilityWord(m.Public.ValueBool()), err))
		return diags
	}

	m.ID = types.StringValue(strings.ToLower(org))
	m.Username = types.StringValue(me.Login)

	return diags
}

// publicMembershipNotFound explains a 404 from the public-members route.
func publicMembershipNotFound(ctx context.Context, c *client.Client, org, login string, err error) string {
	o, getErr := c.GetOrganization(ctx, org)
	switch {
	case client.IsNotFound(getErr):
		return fmt.Sprintf("Organization %s not found: %s", org, err)
	case getErr == nil && findMember(o, login) == nil:
		return fmt.Sprintf("%s, the token's user, isn't a member of %s. Ghost only lets members make their own membership public or private: %s", login, org, err)
	default:
		return fmt.Sprintf("Ghost couldn't find %s's membership of %s: %s", login, org, err)
	}
}

// findMember returns login's entry in o's members, matching the login
// case-insensitively, or nil when login isn't a member.
func findMember(o *client.Organization, login string) *client.OrganizationMember {
	for i := range o.Members {
		if strings.EqualFold(o.Members[i].Username, login) {
			return &o.Members[i]
		}
	}

	return nil
}

func visibilityWord(public bool) string {
	if public {
		return "public"
	}

	return "private"
}
