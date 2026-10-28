package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

var (
	_ datasource.DataSource              = &RepositoryStatusContextsDataSource{}
	_ datasource.DataSourceWithConfigure = &RepositoryStatusContextsDataSource{}
)

// NewRepositoryStatusContextsDataSource returns the
// ghost_repository_status_contexts data source.
func NewRepositoryStatusContextsDataSource() datasource.DataSource {
	return &RepositoryStatusContextsDataSource{}
}

// RepositoryStatusContextsDataSource lists the commit status contexts a
// repository has seen.
type RepositoryStatusContextsDataSource struct {
	client *client.Client
}

// repositoryStatusContextsDataSourceModel maps the
// ghost_repository_status_contexts data source schema.
type repositoryStatusContextsDataSourceModel struct {
	ID           types.String `tfsdk:"id"`
	Repository   types.String `tfsdk:"repository"`
	RepositoryID types.Int64  `tfsdk:"repository_id"`
	Contexts     types.List   `tfsdk:"contexts"`
}

func (d *RepositoryStatusContextsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_repository_status_contexts"
}

func (d *RepositoryStatusContextsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the commit status check contexts a Ghost repository has seen, such as `ci/build`: the names " +
			"CI systems report statuses under. These are the checks the web UI suggests for a ruleset's required status checks, " +
			"so they can feed `ghost_repository_ruleset`'s `rules.required_status_checks.required_check`. " +
			"Ghost returns only the 50 most recently reported contexts, and listing them needs write access to the repository.",
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
			"contexts": schema.ListAttribute{
				MarkdownDescription: "The distinct contexts of the statuses reported on any of the repository's commits, the most recently reported first, " +
					"at most 50 (older ones drop off). Reporting a status again moves its context to the front. Contexts are case-sensitive, " +
					"and a status reported without one is listed as `default`. Empty until a status has been reported. " +
					"A ruleset's required checks don't have to appear here.",
				ElementType: types.StringType,
				Computed:    true,
			},
		},
	}
}

func (d *RepositoryStatusContextsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *RepositoryStatusContextsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data repositoryStatusContextsDataSourceModel
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

	contexts, err := d.client.ListStatusContexts(ctx, owner, repo)
	if client.IsForbidden(err) {
		resp.Diagnostics.AddAttributeError(path.Root("repository"), "Not allowed to list status contexts",
			fmt.Sprintf("Listing the status contexts of %s needs write access to it, and this token can only read it: %s", repository.FullName, err))
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error listing status contexts", err.Error())
		return
	}

	list, diags := types.ListValueFrom(ctx, types.StringType, contexts)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	data.ID = types.StringValue(repository.FullName)
	data.RepositoryID = types.Int64Value(repository.ID)
	data.Contexts = list
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
