package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

var (
	_ datasource.DataSource              = &RepositoryRulesetsDataSource{}
	_ datasource.DataSourceWithConfigure = &RepositoryRulesetsDataSource{}
)

// rulesetItemAttrTypes are the attribute types of one element of the
// ghost_repository_rulesets data source's `rulesets` list.
var rulesetItemAttrTypes = map[string]attr.Type{
	"ruleset_id":    types.StringType,
	"numeric_id":    types.Int64Type,
	"node_id":       types.StringType,
	"html_url":      types.StringType,
	"name":          types.StringType,
	"target":        types.StringType,
	"enforcement":   types.StringType,
	"conditions":    types.ObjectType{AttrTypes: conditionsAttrTypes},
	"bypass_actors": types.ListType{ElemType: types.ObjectType{AttrTypes: bypassActorAttrTypes}},
	"rules":         types.ObjectType{AttrTypes: rulesAttrTypes},
}

// NewRepositoryRulesetsDataSource returns the ghost_repository_rulesets data
// source.
func NewRepositoryRulesetsDataSource() datasource.DataSource {
	return &RepositoryRulesetsDataSource{}
}

// RepositoryRulesetsDataSource lists the rulesets of a repository.
type RepositoryRulesetsDataSource struct {
	client *client.Client
}

// repositoryRulesetsDataSourceModel maps the ghost_repository_rulesets data
// source schema.
type repositoryRulesetsDataSourceModel struct {
	ID           types.String `tfsdk:"id"`
	Repository   types.String `tfsdk:"repository"`
	RepositoryID types.Int64  `tfsdk:"repository_id"`
	Rulesets     types.List   `tfsdk:"rulesets"`
}

// rulesetItemModel is one element of `rulesets`.
type rulesetItemModel struct {
	RulesetID    types.String `tfsdk:"ruleset_id"`
	NumericID    types.Int64  `tfsdk:"numeric_id"`
	NodeID       types.String `tfsdk:"node_id"`
	HTMLURL      types.String `tfsdk:"html_url"`
	Name         types.String `tfsdk:"name"`
	Target       types.String `tfsdk:"target"`
	Enforcement  types.String `tfsdk:"enforcement"`
	Conditions   types.Object `tfsdk:"conditions"`
	BypassActors types.List   `tfsdk:"bypass_actors"`
	Rules        types.Object `tfsdk:"rules"`
}

func (d *RepositoryRulesetsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_repository_rulesets"
}

func computedBool(description string) schema.BoolAttribute {
	return schema.BoolAttribute{MarkdownDescription: description, Computed: true}
}

func (d *RepositoryRulesetsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists every ruleset on a Ghost repository, whatever its enforcement, with its conditions, bypass actors and rules " +
			"in the same shape as `ghost_repository_ruleset`. Rulesets are Ghost's only branch and tag protection. " +
			"To see which existing branches an active ruleset protects, use `ghost_branches`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The repository's current full name, `owner/slug`. It differs from `repository` when that names a repository that has since been renamed.",
				Computed:            true,
			},
			"repository": schema.StringAttribute{
				MarkdownDescription: "The repository's full name, `owner/slug`, e.g. `ghost_repository.example.full_name`.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(fullNamePattern, `must be "owner/slug", e.g. "alice/my-repo"`),
				},
			},
			"repository_id": schema.Int64Attribute{
				MarkdownDescription: "The numeric ID of `repository`.",
				Computed:            true,
			},
			"rulesets": schema.ListNestedAttribute{
				MarkdownDescription: "The repository's rulesets, oldest first. Empty when it has none.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"ruleset_id": schema.StringAttribute{
							MarkdownDescription: "Ghost's ID for the ruleset, e.g. `ruleset_tcql3h2wcrh4uqqxfndwsspt`, the same as `ghost_repository_ruleset`'s `ruleset_id`. " +
								"Import a ruleset with `<id>:<ruleset_id>`.",
							Computed: true,
						},
						"numeric_id": schema.Int64Attribute{
							MarkdownDescription: "The ruleset's numeric ID in the GitHub-compatible API and in web UI links.",
							Computed:            true,
						},
						"node_id": schema.StringAttribute{
							MarkdownDescription: "The ruleset's GitHub-style node ID.",
							Computed:            true,
						},
						"html_url": schema.StringAttribute{
							MarkdownDescription: "The ruleset's page in the web UI. Only repository admins can open it; other users get a 404, even on a public repository.",
							Computed:            true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "The ruleset name, unique within the repository.",
							Computed:            true,
						},
						"target": schema.StringAttribute{
							MarkdownDescription: "What the ruleset protects: `branch` or `tag`.",
							Computed:            true,
						},
						"enforcement": schema.StringAttribute{
							MarkdownDescription: "`active` enforces the rules, `evaluate` only reports what would be blocked, and `disabled` turns the ruleset off.",
							Computed:            true,
						},
						"conditions": schema.SingleNestedAttribute{
							MarkdownDescription: "The refs the ruleset applies to.",
							Computed:            true,
							Attributes: map[string]schema.Attribute{
								"ref_name": schema.SingleNestedAttribute{
									MarkdownDescription: "Ref name patterns to include and exclude.",
									Computed:            true,
									Attributes: map[string]schema.Attribute{
										"include": schema.ListAttribute{
											MarkdownDescription: "Patterns the ruleset applies to, as Ghost stores them: `refs/heads/…` or `refs/tags/…`, or `~DEFAULT_BRANCH` or `~ALL`.",
											ElementType:         types.StringType,
											Computed:            true,
										},
										"exclude": schema.ListAttribute{
											MarkdownDescription: "Patterns excluded from `include`, in the same form.",
											ElementType:         types.StringType,
											Computed:            true,
										},
									},
								},
							},
						},
						"bypass_actors": schema.ListNestedAttribute{
							MarkdownDescription: "Who may bypass the ruleset. Ghost shows bypass actors only to repository admins, so this is null when the token isn't one.",
							Computed:            true,
							NestedObject: schema.NestedAttributeObject{
								Attributes: map[string]schema.Attribute{
									"actor_type": schema.StringAttribute{
										MarkdownDescription: "`RepositoryRole`, `Team` or `OrganizationAdmin`.",
										Computed:            true,
									},
									"role": schema.StringAttribute{
										MarkdownDescription: "For `RepositoryRole` actors, the role that may bypass: `write`, `maintain` or `admin`. Null otherwise.",
										Computed:            true,
									},
									"team_id": schema.StringAttribute{
										MarkdownDescription: "For `Team` actors, the team's ID. Null otherwise.",
										Computed:            true,
									},
									"bypass_mode": schema.StringAttribute{
										MarkdownDescription: "`always`, or `pull_request` when bypassing is allowed only through a pull request.",
										Computed:            true,
									},
								},
							},
						},
						"rules": schema.SingleNestedAttribute{
							MarkdownDescription: "The rules the ruleset enforces. Each flag is `false` and each object null when that rule is off.",
							Computed:            true,
							Attributes: map[string]schema.Attribute{
								"creation":                computedBool("Only bypass actors may create matching refs."),
								"update":                  computedBool("Only bypass actors may push to matching refs."),
								"deletion":                computedBool("Only bypass actors may delete matching refs."),
								"non_fast_forward":        computedBool("Force pushes are blocked."),
								"required_linear_history": computedBool("Merge commits are blocked."),
								"required_signatures":     computedBool("Commits must have verified signatures."),
								"pull_request": schema.SingleNestedAttribute{
									MarkdownDescription: "Changes must go through a pull request. Null when this rule is off.",
									Computed:            true,
									Attributes: map[string]schema.Attribute{
										"required_approving_review_count": schema.Int64Attribute{
											MarkdownDescription: "Approving reviews needed before merging.",
											Computed:            true,
										},
										"dismiss_stale_reviews_on_push":     computedBool("New commits dismiss earlier approvals."),
										"require_code_owner_review":         computedBool("A code owner must approve changes to files they own."),
										"require_last_push_approval":        computedBool("Someone other than the last pusher must approve."),
										"required_review_thread_resolution": computedBool("Every review thread must be resolved before merging."),
										"allowed_merge_methods": schema.SetAttribute{
											MarkdownDescription: "Merge methods allowed: any of `merge`, `squash` and `rebase`.",
											ElementType:         types.StringType,
											Computed:            true,
										},
									},
								},
								"required_status_checks": schema.SingleNestedAttribute{
									MarkdownDescription: "Status checks must pass before merging. Null when this rule is off.",
									Computed:            true,
									Attributes: map[string]schema.Attribute{
										"required_check": schema.ListNestedAttribute{
											MarkdownDescription: "The status checks that must pass.",
											Computed:            true,
											NestedObject: schema.NestedAttributeObject{
												Attributes: map[string]schema.Attribute{
													"context": schema.StringAttribute{
														MarkdownDescription: "The status check context, e.g. `ci/build`.",
														Computed:            true,
													},
												},
											},
										},
										"strict_required_status_checks_policy": computedBool("Branches must be up to date with the base branch before merging."),
										"do_not_enforce_on_create":             computedBool("The checks aren't required when a matching ref is created."),
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func (d *RepositoryRulesetsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data",
			fmt.Sprintf("Expected *client.Client, got %T. Please report this issue to the provider developers.", req.ProviderData))
		return
	}

	d.client = c
}

func (d *RepositoryRulesetsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data repositoryRulesetsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	repository, err := lookupRepository(ctx, d.client, data.Repository.ValueString())
	if client.IsNotFound(err) {
		resp.Diagnostics.AddAttributeError(path.Root("repository"), "Repository not found",
			fmt.Sprintf("No repository named %s exists, or it is not visible to this token.", data.Repository.ValueString()))
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading repository", err.Error())
		return
	}

	// Use the current name: an old name redirects on v3 but not on every
	// native route.
	owner, repo, err := client.SplitFullName(repository.FullName)
	if err != nil {
		resp.Diagnostics.AddError("Error reading repository", err.Error())
		return
	}

	// The native list has every detail, in the shape the resource uses. The
	// v3 list adds the numeric ID, node ID and web link.
	rulesets, err := d.client.ListRulesets(ctx, owner, repo)
	if err != nil {
		resp.Diagnostics.AddError("Error listing rulesets", err.Error())
		return
	}
	summaries, err := d.client.ListRulesetSummaries(ctx, owner, repo)
	if err != nil {
		resp.Diagnostics.AddError("Error listing rulesets", err.Error())
		return
	}

	list, diags := rulesetItems(ctx, rulesets, summaries)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	data.ID = types.StringValue(repository.FullName)
	data.RepositoryID = types.Int64Value(repository.ID)
	data.Rulesets = list
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// rulesetItems builds the `rulesets` list from the native rulesets, in their
// order, adding the v3 fields from the summary with the same name. Names are
// unique within a repository; a ruleset with no summary (e.g. one renamed
// between the two requests) keeps those fields null.
func rulesetItems(ctx context.Context, rulesets []client.Ruleset, summaries []client.RulesetSummary) (types.List, diag.Diagnostics) {
	var diags diag.Diagnostics
	itemType := types.ObjectType{AttrTypes: rulesetItemAttrTypes}

	byName := make(map[string]client.RulesetSummary, len(summaries))
	for _, s := range summaries {
		byName[s.Name] = s
	}

	items := make([]rulesetItemModel, 0, len(rulesets))
	for _, rs := range rulesets {
		item := rulesetItemModel{
			RulesetID:    types.StringValue(rs.ID),
			NumericID:    types.Int64Null(),
			NodeID:       types.StringNull(),
			HTMLURL:      types.StringNull(),
			Name:         types.StringValue(rs.Name),
			Target:       types.StringValue(rs.Target),
			Enforcement:  types.StringValue(rs.Enforcement),
			BypassActors: types.ListNull(types.ObjectType{AttrTypes: bypassActorAttrTypes}),
		}
		if s, ok := byName[rs.Name]; ok {
			item.NumericID = types.Int64Value(s.ID)
			item.NodeID = stringOrNull(s.NodeID)
			item.HTMLURL = stringOrNull(s.Links.HTML.Href)
		}

		include, d := types.ListValueFrom(ctx, types.StringType, nonNil(rs.Include))
		diags.Append(d...)
		exclude, d := types.ListValueFrom(ctx, types.StringType, nonNil(rs.Exclude))
		diags.Append(d...)
		item.Conditions, d = types.ObjectValueFrom(ctx, conditionsAttrTypes, rulesetConditionsModel{
			RefName: rulesetRefNameModel{Include: include, Exclude: exclude},
		})
		diags.Append(d...)

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
			item.BypassActors, d = types.ListValueFrom(ctx, types.ObjectType{AttrTypes: bypassActorAttrTypes}, actors)
			diags.Append(d...)
		}

		rules, err := rulesFromAPI(rs.Rules)
		if err != nil {
			diags.AddError("Unsupported ruleset", fmt.Sprintf("Ruleset %q: %s", rs.Name, err))
			return types.ListNull(itemType), diags
		}
		item.Rules, d = rulesObject(ctx, rules)
		diags.Append(d...)

		items = append(items, item)
	}

	list, d := types.ListValueFrom(ctx, itemType, items)
	diags.Append(d...)

	return list, diags
}

// nonNil returns s, or an empty slice when s is nil.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}

	return s
}
