package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

var (
	_ datasource.DataSource                     = &RepositoryDataSource{}
	_ datasource.DataSourceWithConfigure        = &RepositoryDataSource{}
	_ datasource.DataSourceWithConfigValidators = &RepositoryDataSource{}
)

// fullNamePattern matches an "owner/slug" repository full name.
var fullNamePattern = regexp.MustCompile(`^[^/]+/[^/]+$`)

// NewRepositoryDataSource returns the ghost_repository data source.
func NewRepositoryDataSource() datasource.DataSource {
	return &RepositoryDataSource{}
}

// RepositoryDataSource looks up an existing Ghost repository.
type RepositoryDataSource struct {
	client *client.Client
}

// repositoryDataSourceModel maps the ghost_repository data source schema.
type repositoryDataSourceModel struct {
	repositoryRemoteModel

	Owner       types.String `tfsdk:"owner"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	CreatedAt   types.String `tfsdk:"created_at"`
	UpdatedAt   types.String `tfsdk:"updated_at"`
	PushedAt    types.String `tfsdk:"pushed_at"`
}

func (d *RepositoryDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_repository"
}

func (d *RepositoryDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	nonEmpty := []validator.String{stringvalidator.LengthAtLeast(1)}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a Ghost repository, either by `full_name` or by `name` (with an optional `owner`).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The repository's full name, `owner/slug`.",
				Computed:            true,
			},
			"full_name": schema.StringAttribute{
				MarkdownDescription: "The repository's full name, `owner/slug`. Set this, or `name` (and optionally `owner`), to choose the repository to look up.",
				Optional:            true,
				Computed:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(fullNamePattern, `must be "owner/slug", e.g. "alice/my-repo"`),
				},
			},
			"owner": schema.StringAttribute{
				MarkdownDescription: "The user or organization that owns the repository. When looking up by `name`, defaults to the authenticated user. Cannot be combined with `full_name`.",
				Optional:            true,
				Computed:            true,
				Validators:          nonEmpty,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The repository's slug, as reported by the Ghost API. When looking up by `name`, this is the slug rather than a display name. Cannot be combined with `full_name`.",
				Optional:            true,
				Computed:            true,
				Validators:          nonEmpty,
			},
			"slug": schema.StringAttribute{
				MarkdownDescription: "The repository's URL-safe name.",
				Computed:            true,
			},
			"repo_id": schema.Int64Attribute{
				MarkdownDescription: "The numeric repository ID.",
				Computed:            true,
			},
			"node_id": schema.StringAttribute{
				MarkdownDescription: "The GraphQL node ID.",
				Computed:            true,
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "The repository's description, null when it has none.",
				Computed:            true,
			},
			"visibility": schema.StringAttribute{
				MarkdownDescription: "Either `public` or `private`.",
				Computed:            true,
			},
			"private": schema.BoolAttribute{
				MarkdownDescription: "Whether the repository is private.",
				Computed:            true,
			},
			"fork": schema.BoolAttribute{
				MarkdownDescription: "Whether the repository is a fork.",
				Computed:            true,
			},
			"html_url": schema.StringAttribute{
				MarkdownDescription: "The repository's web URL.",
				Computed:            true,
			},
			"clone_url": schema.StringAttribute{
				MarkdownDescription: "The HTTPS clone URL.",
				Computed:            true,
			},
			"ssh_url": schema.StringAttribute{
				MarkdownDescription: "The SSH clone URL, empty when SSH is unavailable.",
				Computed:            true,
			},
			"default_branch": schema.StringAttribute{
				MarkdownDescription: "The default branch.",
				Computed:            true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "When the repository was created, as an RFC 3339 timestamp.",
				Computed:            true,
			},
			"updated_at": schema.StringAttribute{
				MarkdownDescription: "When the repository was last updated, as an RFC 3339 timestamp.",
				Computed:            true,
			},
			"pushed_at": schema.StringAttribute{
				MarkdownDescription: "When the repository was last pushed to, as an RFC 3339 timestamp. Ghost reports the creation time for a repository that has never been pushed to.",
				Computed:            true,
			},
		},
	}
}

// ConfigValidators requires exactly one lookup form: full_name, or name with
// an optional owner.
func (d *RepositoryDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("full_name"), path.MatchRoot("name")),
		datasourcevalidator.Conflicting(path.MatchRoot("full_name"), path.MatchRoot("owner")),
	}
}

func (d *RepositoryDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *RepositoryDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data repositoryDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var owner, slug string
	if !data.FullName.IsNull() {
		var err error
		owner, slug, err = client.SplitFullName(data.FullName.ValueString())
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("full_name"), "Invalid full_name", err.Error())
			return
		}
	} else {
		slug = data.Name.ValueString()
		owner = data.Owner.ValueString()
		if owner == "" {
			user, err := d.client.GetAuthenticatedUser(ctx)
			if err != nil {
				resp.Diagnostics.AddError("Error reading authenticated user",
					"owner was not set, so the authenticated user is needed to look up the repository: "+err.Error())
				return
			}
			owner = user.Login
		}
	}

	repo, err := d.client.GetRepository(ctx, owner, slug)
	if client.IsNotFound(err) {
		resp.Diagnostics.AddError("Repository not found",
			fmt.Sprintf("No repository named %s/%s exists, or it is not visible to this token.", owner, slug))
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading repository", err.Error())
		return
	}

	if err := data.fromRemote(repo); err != nil {
		resp.Diagnostics.AddError("Error reading repository", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// fromRemote sets every attribute of m from repo. Owner and slug are derived
// from the response's full name, since a renamed repository's old name
// redirects to the new one.
func (m *repositoryDataSourceModel) fromRemote(repo *client.Repository) error {
	owner, err := m.repositoryRemoteModel.fromRemote(repo)
	if err != nil {
		return err
	}

	m.Owner = types.StringValue(owner)
	m.Name = types.StringValue(repo.Name)
	m.Description = repositoryDescriptionFromRemote(types.StringNull(), repo.Description)
	m.CreatedAt = types.StringValue(repo.CreatedAt)
	m.UpdatedAt = types.StringValue(repo.UpdatedAt)
	m.PushedAt = types.StringNull()
	if strings.TrimSpace(repo.PushedAt) != "" {
		m.PushedAt = types.StringValue(repo.PushedAt)
	}

	return nil
}
