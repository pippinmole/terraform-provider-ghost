package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

var (
	_ resource.Resource                = &RepositoryRulesetResource{}
	_ resource.ResourceWithConfigure   = &RepositoryRulesetResource{}
	_ resource.ResourceWithImportState = &RepositoryRulesetResource{}
	_ resource.ResourceWithModifyPlan  = &RepositoryRulesetResource{}
)

// Ghost's defaults for merge methods, used as the schema default so the plan
// matches what Ghost stores.
var defaultMergeMethods = []string{"merge", "squash", "rebase"}

var (
	refNameAttrTypes = map[string]attr.Type{
		"include": types.ListType{ElemType: types.StringType},
		"exclude": types.ListType{ElemType: types.StringType},
	}
	conditionsAttrTypes = map[string]attr.Type{
		"ref_name": types.ObjectType{AttrTypes: refNameAttrTypes},
	}
	bypassActorAttrTypes = map[string]attr.Type{
		"actor_type":  types.StringType,
		"role":        types.StringType,
		"team_id":     types.StringType,
		"bypass_mode": types.StringType,
	}
	requiredCheckAttrTypes = map[string]attr.Type{
		"context": types.StringType,
	}
	pullRequestAttrTypes = map[string]attr.Type{
		"required_approving_review_count":   types.Int64Type,
		"dismiss_stale_reviews_on_push":     types.BoolType,
		"require_code_owner_review":         types.BoolType,
		"require_last_push_approval":        types.BoolType,
		"required_review_thread_resolution": types.BoolType,
		"allowed_merge_methods":             types.SetType{ElemType: types.StringType},
	}
	statusChecksAttrTypes = map[string]attr.Type{
		"required_check":                       types.ListType{ElemType: types.ObjectType{AttrTypes: requiredCheckAttrTypes}},
		"strict_required_status_checks_policy": types.BoolType,
		"do_not_enforce_on_create":             types.BoolType,
	}
	rulesAttrTypes = map[string]attr.Type{
		"creation":                types.BoolType,
		"update":                  types.BoolType,
		"deletion":                types.BoolType,
		"non_fast_forward":        types.BoolType,
		"required_linear_history": types.BoolType,
		"required_signatures":     types.BoolType,
		"pull_request":            types.ObjectType{AttrTypes: pullRequestAttrTypes},
		"required_status_checks":  types.ObjectType{AttrTypes: statusChecksAttrTypes},
	}
)

// NewRepositoryRulesetResource returns the ghost_repository_ruleset resource.
func NewRepositoryRulesetResource() resource.Resource {
	return &RepositoryRulesetResource{}
}

// RepositoryRulesetResource manages a ruleset on a Ghost repository.
type RepositoryRulesetResource struct {
	client *client.Client
}

// repositoryRulesetResourceModel maps the ghost_repository_ruleset schema.
type repositoryRulesetResourceModel struct {
	ID           types.String `tfsdk:"id"`
	Repository   types.String `tfsdk:"repository"`
	RepositoryID types.Int64  `tfsdk:"repository_id"`
	RulesetID    types.String `tfsdk:"ruleset_id"`
	Name         types.String `tfsdk:"name"`
	Target       types.String `tfsdk:"target"`
	Enforcement  types.String `tfsdk:"enforcement"`
	Conditions   types.Object `tfsdk:"conditions"`
	BypassActors types.Set    `tfsdk:"bypass_actors"`
	Rules        types.Object `tfsdk:"rules"`
}

type rulesetConditionsModel struct {
	RefName rulesetRefNameModel `tfsdk:"ref_name"`
}

type rulesetRefNameModel struct {
	Include types.List `tfsdk:"include"`
	Exclude types.List `tfsdk:"exclude"`
}

type rulesetBypassActorModel struct {
	ActorType  types.String `tfsdk:"actor_type"`
	Role       types.String `tfsdk:"role"`
	TeamID     types.String `tfsdk:"team_id"`
	BypassMode types.String `tfsdk:"bypass_mode"`
}

type rulesetRulesModel struct {
	Creation              types.Bool   `tfsdk:"creation"`
	Update                types.Bool   `tfsdk:"update"`
	Deletion              types.Bool   `tfsdk:"deletion"`
	NonFastForward        types.Bool   `tfsdk:"non_fast_forward"`
	RequiredLinearHistory types.Bool   `tfsdk:"required_linear_history"`
	RequiredSignatures    types.Bool   `tfsdk:"required_signatures"`
	PullRequest           types.Object `tfsdk:"pull_request"`
	RequiredStatusChecks  types.Object `tfsdk:"required_status_checks"`
}

type rulesetPullRequestModel struct {
	RequiredApprovingReviewCount   types.Int64 `tfsdk:"required_approving_review_count"`
	DismissStaleReviewsOnPush      types.Bool  `tfsdk:"dismiss_stale_reviews_on_push"`
	RequireCodeOwnerReview         types.Bool  `tfsdk:"require_code_owner_review"`
	RequireLastPushApproval        types.Bool  `tfsdk:"require_last_push_approval"`
	RequiredReviewThreadResolution types.Bool  `tfsdk:"required_review_thread_resolution"`
	AllowedMergeMethods            types.Set   `tfsdk:"allowed_merge_methods"`
}

type rulesetStatusChecksModel struct {
	RequiredCheck                    []rulesetRequiredCheckModel `tfsdk:"required_check"`
	StrictRequiredStatusChecksPolicy types.Bool                  `tfsdk:"strict_required_status_checks_policy"`
	DoNotEnforceOnCreate             types.Bool                  `tfsdk:"do_not_enforce_on_create"`
}

type rulesetRequiredCheckModel struct {
	Context types.String `tfsdk:"context"`
}

func (r *RepositoryRulesetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_repository_ruleset"
}

// optionalBool is an optional boolean that defaults to false, Ghost's own
// default.
func optionalBool(description string) schema.BoolAttribute {
	return schema.BoolAttribute{
		MarkdownDescription: description + " Defaults to `false`.",
		Optional:            true,
		Computed:            true,
		Default:             booldefault.StaticBool(false),
	}
}

// refPatternList is an include or exclude list of ref name patterns.
func refPatternList(description string) schema.ListAttribute {
	return schema.ListAttribute{
		MarkdownDescription: description,
		ElementType:         types.StringType,
		Optional:            true,
		Computed:            true,
		Default:             listdefault.StaticValue(types.ListValueMust(types.StringType, []attr.Value{})),
		Validators: []validator.List{
			listvalidator.UniqueValues(),
			listvalidator.ValueStringsAre(stringvalidator.UTF8LengthBetween(1, 255)),
		},
	}
}

func (r *RepositoryRulesetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	stableString := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}

	mergeMethods := make([]attr.Value, len(defaultMergeMethods))
	for i, m := range defaultMergeMethods {
		mergeMethods[i] = types.StringValue(m)
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a ruleset on a Ghost repository: branch or tag protection rules, the refs they apply to, and who may bypass them. " +
			"The shape follows the GitHub provider's `github_repository_ruleset`, using nested attributes rather than blocks.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The repository's full name and the ruleset ID, `owner/slug:ruleset_id`.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
			"repository": schema.StringAttribute{
				MarkdownDescription: "The repository's full name, `owner/slug`, e.g. `ghost_repository.example.full_name`. Renaming the repository updates this in place; pointing it at a different repository forces a new ruleset. Replacing the repository deletes its rulesets, so add `lifecycle { replace_triggered_by = [ghost_repository.example.repo_id] }` to recreate the ruleset in the same apply.",
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
			"ruleset_id": schema.StringAttribute{
				MarkdownDescription: "Ghost's ID for the ruleset, e.g. `ruleset_tcql3h2wcrh4uqqxfndwsspt`.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The ruleset name, 1 to 100 characters, unique within the repository.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.UTF8LengthBetween(1, 100),
					stringvalidator.RegexMatches(untrimmedPattern, "must not start or end with whitespace"),
				},
			},
			"target": schema.StringAttribute{
				MarkdownDescription: "What the ruleset protects: `branch` or `tag`. Defaults to `branch`. Changing it updates the ruleset in place.",
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("branch"),
				Validators:          []validator.String{stringvalidator.OneOf("branch", "tag")},
			},
			"enforcement": schema.StringAttribute{
				MarkdownDescription: "`active` enforces the rules, `evaluate` only reports what would be blocked, and `disabled` turns the ruleset off.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.OneOf("active", "evaluate", "disabled")},
			},
			"conditions": schema.SingleNestedAttribute{
				MarkdownDescription: "The refs the ruleset applies to. Leaving it out means no refs.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"ref_name": schema.SingleNestedAttribute{
						MarkdownDescription: "Ref name patterns to include and exclude.",
						Required:            true,
						Attributes: map[string]schema.Attribute{
							"include": refPatternList("Patterns the ruleset applies to, e.g. `main`, `release/*` or `~DEFAULT_BRANCH` (`~ALL` matches every ref). " +
								"`*` matches within one path segment and `**` across segments. Ghost stores short names in full form " +
								"(`main` becomes `refs/heads/main` on a branch ruleset, `refs/tags/main` on a tag ruleset); the configured spelling is kept in state. Defaults to `[]`."),
							"exclude": refPatternList("Patterns excluded from the ruleset, in the same form as `include`. Defaults to `[]`."),
						},
					},
				},
			},
			"bypass_actors": schema.SetNestedAttribute{
				MarkdownDescription: "Who may bypass the ruleset. Personal repositories allow only `RepositoryRole` actors. Defaults to none.",
				Optional:            true,
				Computed:            true,
				Default:             setdefault.StaticValue(types.SetValueMust(types.ObjectType{AttrTypes: bypassActorAttrTypes}, []attr.Value{})),
				Validators:          []validator.Set{bypassActorsValidator()},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"actor_type": schema.StringAttribute{
							MarkdownDescription: "`RepositoryRole`, `Team` or `OrganizationAdmin`.",
							Required:            true,
							Validators:          []validator.String{stringvalidator.OneOf("RepositoryRole", "Team", "OrganizationAdmin")},
						},
						"role": schema.StringAttribute{
							MarkdownDescription: "For `RepositoryRole` actors, the role that may bypass: `write`, `maintain` or `admin`. Required for `RepositoryRole`, not allowed otherwise.",
							Optional:            true,
							Validators:          []validator.String{stringvalidator.OneOf("write", "maintain", "admin")},
						},
						"team_id": schema.StringAttribute{
							MarkdownDescription: "For `Team` actors, the ID of a team in the repository's organization. Required for `Team`, not allowed otherwise.",
							Optional:            true,
							Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
						},
						"bypass_mode": schema.StringAttribute{
							MarkdownDescription: "`always`, or `pull_request` to allow bypassing only through a pull request. Defaults to `always`.",
							Optional:            true,
							Computed:            true,
							Default:             stringdefault.StaticString("always"),
							Validators:          []validator.String{stringvalidator.OneOf("always", "pull_request")},
						},
					},
				},
			},
			"rules": schema.SingleNestedAttribute{
				MarkdownDescription: "The rules to enforce. Leaving it out means no rules.",
				Optional:            true,
				Attributes: map[string]schema.Attribute{
					"creation":                optionalBool("Only allow users with bypass permission to create matching refs."),
					"update":                  optionalBool("Only allow users with bypass permission to update matching refs."),
					"deletion":                optionalBool("Only allow users with bypass permission to delete matching refs."),
					"non_fast_forward":        optionalBool("Prevent force pushes to matching refs."),
					"required_linear_history": optionalBool("Prevent merge commits from being pushed to matching refs."),
					"required_signatures":     optionalBool("Require commits pushed to matching refs to be signed."),
					"pull_request": schema.SingleNestedAttribute{
						MarkdownDescription: "Require changes to go through a pull request. Setting this turns the rule on.",
						Optional:            true,
						Attributes: map[string]schema.Attribute{
							"required_approving_review_count": schema.Int64Attribute{
								MarkdownDescription: "Approving reviews needed before merging, 0 to 10. Defaults to `0`.",
								Optional:            true,
								Computed:            true,
								Default:             int64default.StaticInt64(0),
								Validators:          []validator.Int64{int64validator.Between(0, 10)},
							},
							"dismiss_stale_reviews_on_push":     optionalBool("Dismiss approvals when new commits are pushed."),
							"require_code_owner_review":         optionalBool("Require a review from a code owner."),
							"require_last_push_approval":        optionalBool("Require the most recent push to be approved by someone other than its author."),
							"required_review_thread_resolution": optionalBool("Require all review threads to be resolved before merging."),
							"allowed_merge_methods": schema.SetAttribute{
								MarkdownDescription: "Merge methods allowed: any of `merge`, `squash` and `rebase`. Defaults to all three.",
								ElementType:         types.StringType,
								Optional:            true,
								Computed:            true,
								Default:             setdefault.StaticValue(types.SetValueMust(types.StringType, mergeMethods)),
								Validators: []validator.Set{
									setvalidator.SizeAtLeast(1),
									setvalidator.ValueStringsAre(stringvalidator.OneOf(defaultMergeMethods...)),
								},
							},
						},
					},
					"required_status_checks": schema.SingleNestedAttribute{
						MarkdownDescription: "Require status checks to pass before merging. Setting this turns the rule on.",
						Optional:            true,
						Attributes: map[string]schema.Attribute{
							"required_check": schema.ListNestedAttribute{
								MarkdownDescription: "The status checks that must pass. May be empty.",
								Required:            true,
								NestedObject: schema.NestedAttributeObject{
									Attributes: map[string]schema.Attribute{
										"context": schema.StringAttribute{
											MarkdownDescription: "The status check context, e.g. `ci/build`.",
											Required:            true,
											Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
										},
									},
								},
							},
							"strict_required_status_checks_policy": optionalBool("Require branches to be up to date with the base before merging."),
							"do_not_enforce_on_create":             optionalBool("Allow creating a matching ref even if its checks would fail."),
						},
					},
				},
			},
		},
	}
}

func (r *RepositoryRulesetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
// repository under a new name, and forces a new ruleset otherwise. The id is
// marked unknown when the repository changes, since UseStateForUnknown would
// otherwise keep the stale value.
func (r *RepositoryRulesetResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}

	var plan, state repositoryRulesetResourceModel
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

func (r *RepositoryRulesetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan repositoryRulesetResourceModel
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

	body, diags := plan.toRequest(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	rs, err := r.client.CreateRuleset(ctx, owner, repo, body)
	if client.IsConflict(err) {
		resp.Diagnostics.AddError("Error creating ruleset", r.conflictMessage(ctx, plan.Repository.ValueString(), body.Name, "", err))
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error creating ruleset", err.Error())
		return
	}

	resp.Diagnostics.Append(plan.fromRemote(ctx, rs)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *RepositoryRulesetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state repositoryRulesetResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	owner, repo, err := client.SplitFullName(state.Repository.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("repository"), "Invalid repository", err.Error())
		return
	}

	rs, err := r.client.GetRuleset(ctx, owner, repo, state.RulesetID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading ruleset", err.Error())
		return
	}

	// An imported ruleset has no repository_id yet.
	if state.RepositoryID.IsNull() {
		repository, err := r.client.GetRepository(ctx, owner, repo)
		if err != nil {
			resp.Diagnostics.AddError("Error reading repository", err.Error())
			return
		}
		state.RepositoryID = types.Int64Value(repository.ID)
	}

	resp.Diagnostics.Append(state.fromRemote(ctx, rs)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *RepositoryRulesetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state repositoryRulesetResourceModel
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
			resp.Diagnostics.AddAttributeError(path.Root("repository"), "Ruleset can't be moved",
				fmt.Sprintf("%s is a different repository from the one this ruleset was created in. "+
					"Rulesets can't be moved; replace it with terraform apply -replace=<address>.", plan.Repository.ValueString()))
			return
		}
		plan.RepositoryID = types.Int64Value(repository.ID)
	}

	body, diags := plan.toRequest(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	rs, err := r.client.ReplaceRuleset(ctx, owner, repo, state.RulesetID.ValueString(), body)
	if client.IsConflict(err) {
		resp.Diagnostics.AddError("Error updating ruleset", r.conflictMessage(ctx, plan.Repository.ValueString(), body.Name, state.RulesetID.ValueString(), err))
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error updating ruleset", err.Error())
		return
	}

	resp.Diagnostics.Append(plan.fromRemote(ctx, rs)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *RepositoryRulesetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state repositoryRulesetResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	owner, repo, err := client.SplitFullName(state.Repository.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("repository"), "Invalid repository", err.Error())
		return
	}

	if err := r.client.DeleteRuleset(ctx, owner, repo, state.RulesetID.ValueString()); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error deleting ruleset", err.Error())
	}
}

func (r *RepositoryRulesetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	repository, id, err := parseRulesetID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("repository"), repository)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("ruleset_id"), id)...)
}

// conflictMessage explains a 409 from Ghost: another ruleset already has the
// name. self is the ID of the ruleset being updated, or "" on create. On
// create the message gives the import command for the existing ruleset; on
// update importing can't help, so it asks for another name.
func (r *RepositoryRulesetResource) conflictMessage(ctx context.Context, repository, name, self string, cause error) string {
	var existing string
	if owner, repo, err := client.SplitFullName(repository); err == nil {
		if list, err := r.client.ListRulesets(ctx, owner, repo); err == nil {
			for _, rs := range list {
				if rs.Name == name && rs.ID != self {
					existing = rs.ID
					break
				}
			}
		}
	}

	if self != "" {
		if existing != "" {
			return fmt.Sprintf("another ruleset (%s) on %s already uses the name %q; choose a different name, or remove that ruleset first",
				existing, repository, name)
		}
		return fmt.Sprintf("another ruleset on %s already uses the name %q; choose a different name, or remove that ruleset first (%s)",
			repository, name, cause)
	}
	if existing != "" {
		return fmt.Sprintf("a ruleset named %q already exists on %s; import it with: terraform import <address> %s",
			name, repository, rulesetID(repository, existing))
	}

	return fmt.Sprintf("a ruleset named %q already exists on %s; find its ID with GET /api/repositories/%s/rulesets "+
		"and import it with: terraform import <address> %s:<ruleset_id> (%s)", name, repository, repository, repository, cause)
}

// toRequest builds the full request body from the model. Null conditions,
// rules and bypass actors are sent as empty lists.
func (m *repositoryRulesetResourceModel) toRequest(ctx context.Context) (client.RulesetRequest, diag.Diagnostics) {
	var diags diag.Diagnostics
	req := client.RulesetRequest{
		Name:         m.Name.ValueString(),
		Target:       m.Target.ValueString(),
		Enforcement:  m.Enforcement.ValueString(),
		Include:      []string{},
		Exclude:      []string{},
		BypassActors: []client.BypassActor{},
		Rules:        []client.Rule{},
	}

	if !m.Conditions.IsNull() && !m.Conditions.IsUnknown() {
		var c rulesetConditionsModel
		diags.Append(m.Conditions.As(ctx, &c, basetypes.ObjectAsOptions{})...)
		diags.Append(c.RefName.Include.ElementsAs(ctx, &req.Include, false)...)
		diags.Append(c.RefName.Exclude.ElementsAs(ctx, &req.Exclude, false)...)
	}

	if !m.BypassActors.IsNull() && !m.BypassActors.IsUnknown() {
		var actors []rulesetBypassActorModel
		diags.Append(m.BypassActors.ElementsAs(ctx, &actors, false)...)
		for _, a := range actors {
			req.BypassActors = append(req.BypassActors, client.BypassActor{
				ActorType:  a.ActorType.ValueString(),
				Role:       a.Role.ValueString(),
				TeamID:     a.TeamID.ValueString(),
				BypassMode: a.BypassMode.ValueString(),
			})
		}
	}

	if !m.Rules.IsNull() && !m.Rules.IsUnknown() {
		var rm rulesetRulesModel
		diags.Append(m.Rules.As(ctx, &rm, basetypes.ObjectAsOptions{})...)
		rules := rulesetRules{
			Creation:              rm.Creation.ValueBool(),
			Update:                rm.Update.ValueBool(),
			Deletion:              rm.Deletion.ValueBool(),
			NonFastForward:        rm.NonFastForward.ValueBool(),
			RequiredLinearHistory: rm.RequiredLinearHistory.ValueBool(),
			RequiredSignatures:    rm.RequiredSignatures.ValueBool(),
		}
		if !rm.PullRequest.IsNull() {
			var pr rulesetPullRequestModel
			diags.Append(rm.PullRequest.As(ctx, &pr, basetypes.ObjectAsOptions{})...)
			p := client.PullRequestParameters{
				RequiredApprovingReviewCount:   pr.RequiredApprovingReviewCount.ValueInt64(),
				DismissStaleReviewsOnPush:      pr.DismissStaleReviewsOnPush.ValueBool(),
				RequireCodeOwnerReview:         pr.RequireCodeOwnerReview.ValueBool(),
				RequireLastPushApproval:        pr.RequireLastPushApproval.ValueBool(),
				RequiredReviewThreadResolution: pr.RequiredReviewThreadResolution.ValueBool(),
			}
			diags.Append(pr.AllowedMergeMethods.ElementsAs(ctx, &p.AllowedMergeMethods, false)...)
			rules.PullRequest = &p
		}
		if !rm.RequiredStatusChecks.IsNull() {
			var sc rulesetStatusChecksModel
			diags.Append(rm.RequiredStatusChecks.As(ctx, &sc, basetypes.ObjectAsOptions{})...)
			p := client.RequiredStatusChecksParameters{
				RequiredStatusChecks:             make([]client.StatusCheck, 0, len(sc.RequiredCheck)),
				StrictRequiredStatusChecksPolicy: sc.StrictRequiredStatusChecksPolicy.ValueBool(),
				DoNotEnforceOnCreate:             sc.DoNotEnforceOnCreate.ValueBool(),
			}
			for _, c := range sc.RequiredCheck {
				p.RequiredStatusChecks = append(p.RequiredStatusChecks, client.StatusCheck{Context: c.Context.ValueString()})
			}
			rules.RequiredStatusChecks = &p
		}
		req.Rules = rulesToAPI(rules)
	}

	return req, diags
}

// fromRemote copies rs into m. The repository is kept as configured, the
// configured spelling of each pattern is kept when it means the same ref, and
// "nothing" stays null or empty, whichever it was before.
func (m *repositoryRulesetResourceModel) fromRemote(ctx context.Context, rs *client.Ruleset) diag.Diagnostics {
	var diags diag.Diagnostics

	m.RulesetID = types.StringValue(rs.ID)
	m.ID = types.StringValue(rulesetID(m.Repository.ValueString(), rs.ID))
	m.Name = types.StringValue(rs.Name)
	m.Target = types.StringValue(rs.Target)
	m.Enforcement = types.StringValue(rs.Enforcement)

	var priorInclude, priorExclude []string
	if !m.Conditions.IsNull() && !m.Conditions.IsUnknown() {
		var c rulesetConditionsModel
		diags.Append(m.Conditions.As(ctx, &c, basetypes.ObjectAsOptions{})...)
		if !c.RefName.Include.IsUnknown() {
			diags.Append(c.RefName.Include.ElementsAs(ctx, &priorInclude, false)...)
		}
		if !c.RefName.Exclude.IsUnknown() {
			diags.Append(c.RefName.Exclude.ElementsAs(ctx, &priorExclude, false)...)
		}
	}
	include := patternsFromRemote(rs.Target, priorInclude, rs.Include)
	exclude := patternsFromRemote(rs.Target, priorExclude, rs.Exclude)
	if len(include) > 0 || len(exclude) > 0 || !m.Conditions.IsNull() {
		includeList, d := types.ListValueFrom(ctx, types.StringType, include)
		diags.Append(d...)
		excludeList, d := types.ListValueFrom(ctx, types.StringType, exclude)
		diags.Append(d...)
		m.Conditions, d = types.ObjectValueFrom(ctx, conditionsAttrTypes, rulesetConditionsModel{
			RefName: rulesetRefNameModel{Include: includeList, Exclude: excludeList},
		})
		diags.Append(d...)
	}

	if rs.BypassActors != nil {
		actors := make([]rulesetBypassActorModel, len(*rs.BypassActors))
		for i, a := range *rs.BypassActors {
			actors[i] = rulesetBypassActorModel{
				ActorType:  types.StringValue(a.ActorType),
				Role:       stringOrNull(a.Role),
				TeamID:     stringOrNull(a.TeamID),
				BypassMode: types.StringValue(a.BypassMode),
			}
		}
		var d diag.Diagnostics
		m.BypassActors, d = types.SetValueFrom(ctx, types.ObjectType{AttrTypes: bypassActorAttrTypes}, actors)
		diags.Append(d...)
	}

	rules, err := rulesFromAPI(rs.Rules)
	if err != nil {
		diags.AddError("Unsupported ruleset", err.Error())
		return diags
	}
	if !rules.empty() || !m.Rules.IsNull() {
		var d diag.Diagnostics
		m.Rules, d = rulesObject(ctx, rules)
		diags.Append(d...)
	}

	return diags
}

// rulesObject builds the `rules` attribute value from r.
func rulesObject(ctx context.Context, r rulesetRules) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics
	rm := rulesetRulesModel{
		Creation:              types.BoolValue(r.Creation),
		Update:                types.BoolValue(r.Update),
		Deletion:              types.BoolValue(r.Deletion),
		NonFastForward:        types.BoolValue(r.NonFastForward),
		RequiredLinearHistory: types.BoolValue(r.RequiredLinearHistory),
		RequiredSignatures:    types.BoolValue(r.RequiredSignatures),
		PullRequest:           types.ObjectNull(pullRequestAttrTypes),
		RequiredStatusChecks:  types.ObjectNull(statusChecksAttrTypes),
	}

	if p := r.PullRequest; p != nil {
		methods, d := types.SetValueFrom(ctx, types.StringType, p.AllowedMergeMethods)
		diags.Append(d...)
		rm.PullRequest, d = types.ObjectValueFrom(ctx, pullRequestAttrTypes, rulesetPullRequestModel{
			RequiredApprovingReviewCount:   types.Int64Value(p.RequiredApprovingReviewCount),
			DismissStaleReviewsOnPush:      types.BoolValue(p.DismissStaleReviewsOnPush),
			RequireCodeOwnerReview:         types.BoolValue(p.RequireCodeOwnerReview),
			RequireLastPushApproval:        types.BoolValue(p.RequireLastPushApproval),
			RequiredReviewThreadResolution: types.BoolValue(p.RequiredReviewThreadResolution),
			AllowedMergeMethods:            methods,
		})
		diags.Append(d...)
	}

	if p := r.RequiredStatusChecks; p != nil {
		checks := make([]rulesetRequiredCheckModel, len(p.RequiredStatusChecks))
		for i, c := range p.RequiredStatusChecks {
			checks[i] = rulesetRequiredCheckModel{Context: types.StringValue(c.Context)}
		}
		var d diag.Diagnostics
		rm.RequiredStatusChecks, d = types.ObjectValueFrom(ctx, statusChecksAttrTypes, rulesetStatusChecksModel{
			RequiredCheck:                    checks,
			StrictRequiredStatusChecksPolicy: types.BoolValue(p.StrictRequiredStatusChecksPolicy),
			DoNotEnforceOnCreate:             types.BoolValue(p.DoNotEnforceOnCreate),
		})
		diags.Append(d...)
	}

	obj, d := types.ObjectValueFrom(ctx, rulesAttrTypes, rm)
	diags.Append(d...)

	return obj, diags
}

// stringOrNull returns s as a string value, or null when it is empty.
func stringOrNull(s string) types.String {
	if s == "" {
		return types.StringNull()
	}

	return types.StringValue(s)
}
