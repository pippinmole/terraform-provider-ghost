package provider

import (
	"cmp"
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

var (
	_ datasource.DataSource              = &RepositoriesDataSource{}
	_ datasource.DataSourceWithConfigure = &RepositoriesDataSource{}
)

// NewRepositoriesDataSource returns the ghost_repositories data source.
func NewRepositoriesDataSource() datasource.DataSource {
	return &RepositoriesDataSource{}
}

// RepositoriesDataSource lists the repositories of a user or organization.
type RepositoriesDataSource struct {
	client *client.Client
}

// repositoriesDataSourceModel maps the ghost_repositories data source schema.
type repositoriesDataSourceModel struct {
	ID           types.String `tfsdk:"id"`
	Owner        types.String `tfsdk:"owner"`
	Query        types.String `tfsdk:"query"`
	Visibility   types.String `tfsdk:"visibility"`
	Repositories types.List   `tfsdk:"repositories"`
	FullNames    types.List   `tfsdk:"full_names"`
	Slugs        types.List   `tfsdk:"slugs"`
}

// listedRepositoryAttrTypes is the object type of one element of
// repositories.
var listedRepositoryAttrTypes = map[string]attr.Type{
	"id":          types.StringType,
	"numeric_id":  types.Int64Type,
	"name":        types.StringType,
	"slug":        types.StringType,
	"full_name":   types.StringType,
	"description": types.StringType,
	"visibility":  types.StringType,
	"private":     types.BoolType,
	"fork":        types.BoolType,
	"created_at":  types.StringType,
	"updated_at":  types.StringType,
	"pushed_at":   types.StringType,
}

func (d *RepositoriesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_repositories"
}

func (d *RepositoriesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	computedString := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{MarkdownDescription: desc, Computed: true}
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the repositories of a user or organization that the token's user can read: " +
			"every public repository, and the private ones the user has access to. " +
			"Narrow the list with `query` and `visibility`. Use `ghost_repository` for one repository's full details, such as its default branch and URLs.",
		Attributes: map[string]schema.Attribute{
			"id": computedString("The owner as Ghost spells it (lower case)."),
			"owner": schema.StringAttribute{
				MarkdownDescription: "The login of the user, or the slug of the organization, whose repositories to list. Ghost matches it case-insensitively.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					stringvalidator.RegexMatches(regexp.MustCompile(`^[^/]+$`), "must be a user login or organization slug, without a slash"),
				},
			},
			"query": schema.StringAttribute{
				MarkdownDescription: "Keeps only the repositories whose name or description contains this text, ignoring case. Ghost does the matching.",
				Optional:            true,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"visibility": schema.StringAttribute{
				MarkdownDescription: "Keeps only `public` or `private` repositories. Leave it out to list both.",
				Optional:            true,
				Validators:          []validator.String{stringvalidator.OneOf("public", "private")},
			},
			"repositories": schema.ListNestedAttribute{
				MarkdownDescription: "The matching repositories, sorted by slug.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": computedString("Ghost's id for the repository, e.g. `repo_…`."),
						"numeric_id": schema.Int64Attribute{
							MarkdownDescription: "The repository's numeric id, the `id` the GitHub-compatible API uses.",
							Computed:            true,
						},
						"name":        computedString("The repository's display name."),
						"slug":        computedString("The repository's slug, which appears in its URL."),
						"full_name":   computedString("The repository's full name, `owner/slug`. Pass it to the `repository` argument of other resources."),
						"description": computedString("The repository's description, null when none is set."),
						"visibility":  computedString("Either `public` or `private`."),
						"private": schema.BoolAttribute{
							MarkdownDescription: "Whether the repository is private.",
							Computed:            true,
						},
						"fork": schema.BoolAttribute{
							MarkdownDescription: "Whether the repository is a fork.",
							Computed:            true,
						},
						"created_at": computedString("When the repository was created, as an RFC 3339 timestamp."),
						"updated_at": computedString("When the repository was last updated, as an RFC 3339 timestamp."),
						"pushed_at":  computedString("When the repository was last pushed to, as an RFC 3339 timestamp. Null if it never was."),
					},
				},
			},
			"full_names": schema.ListAttribute{
				MarkdownDescription: "The `full_name` of each repository, in the same order as `repositories`.",
				ElementType:         types.StringType,
				Computed:            true,
			},
			"slugs": schema.ListAttribute{
				MarkdownDescription: "The `slug` of each repository, in the same order as `repositories`.",
				ElementType:         types.StringType,
				Computed:            true,
			},
		},
	}
}

func (d *RepositoriesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *RepositoriesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data repositoriesDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	owner := data.Owner.ValueString()
	repos, err := d.client.ListOwnerRepositories(ctx, owner, data.Query.ValueString())
	if client.IsNotFound(err) {
		resp.Diagnostics.AddError("Owner not found",
			fmt.Sprintf("Ghost has no user or organization %q (%s). Check `owner`: it's a user's login or an organization's slug.", owner, err))
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error listing repositories", err.Error())
		return
	}

	resp.Diagnostics.Append(data.fromRemote(repos)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// fromRemote sets every computed attribute of m from repos, keeping those
// that match m.Visibility, sorted by slug. The list doesn't name the owner,
// so full names use m.Owner in lower case, which is how Ghost spells logins
// and organization slugs.
func (m *repositoriesDataSourceModel) fromRemote(repos []client.OwnerRepository) diag.Diagnostics {
	var diags, d diag.Diagnostics
	owner := strings.ToLower(m.Owner.ValueString())
	m.ID = types.StringValue(owner)

	repos = slices.DeleteFunc(slices.Clone(repos), func(r client.OwnerRepository) bool {
		return !m.Visibility.IsNull() && r.Visibility != m.Visibility.ValueString()
	})
	slices.SortFunc(repos, func(a, b client.OwnerRepository) int { return cmp.Compare(a.Slug, b.Slug) })

	elems := make([]attr.Value, 0, len(repos))
	fullNames := make([]attr.Value, 0, len(repos))
	slugs := make([]attr.Value, 0, len(repos))
	for _, r := range repos {
		fullName := owner + "/" + r.Slug
		elems = append(elems, types.ObjectValueMust(listedRepositoryAttrTypes, map[string]attr.Value{
			"id":          types.StringValue(r.ID),
			"numeric_id":  types.Int64Value(r.NumericID),
			"name":        types.StringValue(r.Name),
			"slug":        types.StringValue(r.Slug),
			"full_name":   types.StringValue(fullName),
			"description": types.StringPointerValue(r.Description),
			"visibility":  types.StringValue(r.Visibility),
			"private":     types.BoolValue(r.Visibility == "private"),
			"fork":        types.BoolValue(r.ParentRepositoryID != nil),
			"created_at":  types.StringValue(r.CreatedAt),
			"updated_at":  types.StringValue(r.UpdatedAt),
			"pushed_at":   types.StringPointerValue(r.LastPushedAt),
		}))
		fullNames = append(fullNames, types.StringValue(fullName))
		slugs = append(slugs, types.StringValue(r.Slug))
	}

	m.Repositories, d = types.ListValue(types.ObjectType{AttrTypes: listedRepositoryAttrTypes}, elems)
	diags.Append(d...)
	m.FullNames, d = types.ListValue(types.StringType, fullNames)
	diags.Append(d...)
	m.Slugs, d = types.ListValue(types.StringType, slugs)
	diags.Append(d...)

	return diags
}
