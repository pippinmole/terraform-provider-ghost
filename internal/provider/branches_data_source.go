package provider

import (
	"context"
	"fmt"
	"slices"

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
	_ datasource.DataSource                   = &BranchesDataSource{}
	_ datasource.DataSourceWithConfigure      = &BranchesDataSource{}
	_ datasource.DataSourceWithValidateConfig = &BranchesDataSource{}
)

// NewBranchesDataSource returns the ghost_branches data source.
func NewBranchesDataSource() datasource.DataSource {
	return &BranchesDataSource{}
}

// BranchesDataSource lists the branches of a repository.
type BranchesDataSource struct {
	client *client.Client
}

// branchesDataSourceModel maps the ghost_branches data source schema.
type branchesDataSourceModel struct {
	ID                       types.String `tfsdk:"id"`
	Repository               types.String `tfsdk:"repository"`
	RepositoryID             types.Int64  `tfsdk:"repository_id"`
	OnlyProtectedBranches    types.Bool   `tfsdk:"only_protected_branches"`
	OnlyNonProtectedBranches types.Bool   `tfsdk:"only_non_protected_branches"`
	IncludeSHA               types.Bool   `tfsdk:"include_sha"`
	DefaultBranch            types.String `tfsdk:"default_branch"`
	Branches                 types.List   `tfsdk:"branches"`
	Names                    types.List   `tfsdk:"names"`
}

// listedBranchAttrTypes is the object type of one element of branches.
var listedBranchAttrTypes = map[string]attr.Type{
	"name":      types.StringType,
	"default":   types.BoolType,
	"protected": types.BoolType,
	"rules":     types.ListType{ElemType: types.StringType},
	"sha":       types.StringType,
}

// listedBranch is one branch, before it is converted to a Terraform object.
// SHA is empty when it wasn't fetched.
type listedBranch struct {
	Name      string
	Protected *client.ProtectedRef
	SHA       string
}

func (d *BranchesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_branches"
}

func (d *BranchesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the branches of a Ghost repository, with the default branch and which branches an active " +
			"`ghost_repository_ruleset` protects. Branch SHAs cost one API request per branch, so they are only fetched " +
			"with `include_sha = true`.",
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
			"only_protected_branches": schema.BoolAttribute{
				MarkdownDescription: "Keeps only the branches an active ruleset protects. Cannot be `true` together with `only_non_protected_branches`.",
				Optional:            true,
			},
			"only_non_protected_branches": schema.BoolAttribute{
				MarkdownDescription: "Keeps only the branches no active ruleset protects. Cannot be `true` together with `only_protected_branches`.",
				Optional:            true,
			},
			"include_sha": schema.BoolAttribute{
				MarkdownDescription: "Whether to look up the commit each listed branch points at, which takes one API request per branch. Defaults to `false`, which leaves every `sha` null.",
				Optional:            true,
			},
			"default_branch": schema.StringAttribute{
				MarkdownDescription: "The branch the repository opens on (git `HEAD`), null when nothing has been pushed yet. " +
					"This is what the web UI shows, and it can differ from `ghost_repository`'s `default_branch`, which is the stored setting: " +
					"for example when the first push was to a branch other than `main`, or the default branch was deleted over git.",
				Computed: true,
			},
			"branches": schema.ListNestedAttribute{
				MarkdownDescription: "The repository's branches, after the filters, ordered by name as Ghost orders them (byte order, so upper case comes first). Empty for a repository with no commits.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							MarkdownDescription: "The branch name, without the `refs/heads/` prefix, e.g. `feat/login`.",
							Computed:            true,
						},
						"default": schema.BoolAttribute{
							MarkdownDescription: "Whether this is `default_branch`.",
							Computed:            true,
						},
						"protected": schema.BoolAttribute{
							MarkdownDescription: "Whether an active ruleset targets the branch. Rulesets in `evaluate` or `disabled` mode, and branches a ruleset excludes, don't count.",
							Computed:            true,
						},
						"rules": schema.ListAttribute{
							MarkdownDescription: "The rule types the active rulesets apply to the branch, combined and sorted, e.g. `[\"deletion\", \"pull_request\"]`. Empty when it isn't protected.",
							ElementType:         types.StringType,
							Computed:            true,
						},
						"sha": schema.StringAttribute{
							MarkdownDescription: "The SHA of the commit the branch points at. Null unless `include_sha` is `true`.",
							Computed:            true,
						},
					},
				},
			},
			"names": schema.ListAttribute{
				MarkdownDescription: "The `name` of each branch, in the same order as `branches`.",
				ElementType:         types.StringType,
				Computed:            true,
			},
		},
	}
}

// ValidateConfig rejects asking for only protected and only unprotected
// branches at once, which would always list nothing.
func (d *BranchesDataSource) ValidateConfig(ctx context.Context, req datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse) {
	var data branchesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if data.OnlyProtectedBranches.ValueBool() && data.OnlyNonProtectedBranches.ValueBool() {
		resp.Diagnostics.AddAttributeError(path.Root("only_non_protected_branches"), "Conflicting filters",
			"only_protected_branches and only_non_protected_branches can't both be true: no branch would be listed. Set at most one of them.")
	}
}

func (d *BranchesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *BranchesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data branchesDataSourceModel
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

	list, err := d.client.ListBranches(ctx, owner, repo)
	if err != nil {
		resp.Diagnostics.AddError("Error listing branches", err.Error())
		return
	}

	protected, err := d.client.ListProtectedBranches(ctx, owner, repo)
	if err != nil {
		resp.Diagnostics.AddError("Error listing protected branches", err.Error())
		return
	}

	branches := filterBranches(list.Names, protected, data.OnlyProtectedBranches.ValueBool(), data.OnlyNonProtectedBranches.ValueBool())

	if data.IncludeSHA.ValueBool() {
		kept := branches[:0]
		for _, b := range branches {
			// The full ref, so a tag of the same name can't shadow the branch.
			status, err := d.client.GetCommitStatus(ctx, owner, repo, "refs/heads/"+b.Name)
			if client.IsNotFound(err) {
				// Deleted since it was listed.
				continue
			}
			if err != nil {
				resp.Diagnostics.AddError("Error reading branch commit",
					fmt.Sprintf("Can't read the commit of branch %q: %s", b.Name, err))
				return
			}
			b.SHA = status.SHA
			kept = append(kept, b)
		}
		branches = kept
	}

	data.ID = types.StringValue(repository.FullName)
	data.RepositoryID = types.Int64Value(repository.ID)
	data.DefaultBranch = types.StringPointerValue(list.DefaultBranch)
	resp.Diagnostics.Append(data.setBranches(branches)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// filterBranches pairs each branch name with its protection, keeping the
// order of names, and drops the ones the filters exclude.
func filterBranches(names []string, protected map[string]client.ProtectedRef, onlyProtected, onlyNonProtected bool) []listedBranch {
	branches := make([]listedBranch, 0, len(names))
	for _, name := range names {
		var p *client.ProtectedRef
		if ref, ok := protected[name]; ok {
			p = &ref
		}
		if (onlyProtected && p == nil) || (onlyNonProtected && p != nil) {
			continue
		}
		branches = append(branches, listedBranch{Name: name, Protected: p})
	}

	return branches
}

// setBranches sets branches and names from branches. DefaultBranch must be
// set first.
func (m *branchesDataSourceModel) setBranches(branches []listedBranch) diag.Diagnostics {
	var diags, d diag.Diagnostics

	elems := make([]attr.Value, 0, len(branches))
	names := make([]attr.Value, 0, len(branches))
	for _, b := range branches {
		var rules []string
		if b.Protected != nil {
			rules = slices.Clone(b.Protected.Rules)
			slices.Sort(rules)
			rules = slices.Compact(rules)
		}
		ruleValues := make([]attr.Value, len(rules))
		for i, r := range rules {
			ruleValues[i] = types.StringValue(r)
		}

		sha := types.StringNull()
		if b.SHA != "" {
			sha = types.StringValue(b.SHA)
		}

		elems = append(elems, types.ObjectValueMust(listedBranchAttrTypes, map[string]attr.Value{
			"name":      types.StringValue(b.Name),
			"default":   types.BoolValue(!m.DefaultBranch.IsNull() && m.DefaultBranch.ValueString() == b.Name),
			"protected": types.BoolValue(b.Protected != nil),
			"rules":     types.ListValueMust(types.StringType, ruleValues),
			"sha":       sha,
		}))
		names = append(names, types.StringValue(b.Name))
	}

	m.Branches, d = types.ListValue(types.ObjectType{AttrTypes: listedBranchAttrTypes}, elems)
	diags.Append(d...)
	m.Names, d = types.ListValue(types.StringType, names)
	diags.Append(d...)

	return diags
}
