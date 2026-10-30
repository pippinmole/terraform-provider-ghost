package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
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
	_ datasource.DataSource                   = &ReleaseDataSource{}
	_ datasource.DataSourceWithConfigure      = &ReleaseDataSource{}
	_ datasource.DataSourceWithValidateConfig = &ReleaseDataSource{}
)

// The values of retrieve_by.
const (
	retrieveByLatest = "latest"
	retrieveByID     = "id"
	retrieveByTag    = "tag"
)

// NewReleaseDataSource returns the ghost_release data source.
func NewReleaseDataSource() datasource.DataSource {
	return &ReleaseDataSource{}
}

// ReleaseDataSource looks up one release of a repository.
type ReleaseDataSource struct {
	client *client.Client
}

// releaseDataSourceModel maps the ghost_release data source schema.
type releaseDataSourceModel struct {
	ID           types.String `tfsdk:"id"`
	Repository   types.String `tfsdk:"repository"`
	RepositoryID types.Int64  `tfsdk:"repository_id"`
	RetrieveBy   types.String `tfsdk:"retrieve_by"`
	ReleaseID    types.Int64  `tfsdk:"release_id"`
	ReleaseTag   types.String `tfsdk:"release_tag"`
	Name         types.String `tfsdk:"name"`
	Body         types.String `tfsdk:"body"`
	Draft        types.Bool   `tfsdk:"draft"`
	Prerelease   types.Bool   `tfsdk:"prerelease"`
	CommitSHA    types.String `tfsdk:"commit_sha"`
	AuthorLogin  types.String `tfsdk:"author_login"`
	NodeID       types.String `tfsdk:"node_id"`
	URL          types.String `tfsdk:"url"`
	HTMLURL      types.String `tfsdk:"html_url"`
	AssetsURL    types.String `tfsdk:"assets_url"`
	UploadURL    types.String `tfsdk:"upload_url"`
	TarballURL   types.String `tfsdk:"tarball_url"`
	ZipballURL   types.String `tfsdk:"zipball_url"`
	CreatedAt    types.String `tfsdk:"created_at"`
	PublishedAt  types.String `tfsdk:"published_at"`
	Assets       types.List   `tfsdk:"assets"`
}

// releaseAssetAttrTypes is the object type of one element of assets.
var releaseAssetAttrTypes = map[string]attr.Type{
	"id":                   types.Int64Type,
	"node_id":              types.StringType,
	"name":                 types.StringType,
	"label":                types.StringType,
	"content_type":         types.StringType,
	"size":                 types.Int64Type,
	"download_count":       types.Int64Type,
	"url":                  types.StringType,
	"browser_download_url": types.StringType,
	"created_at":           types.StringType,
	"updated_at":           types.StringType,
}

func (d *ReleaseDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_release"
}

func (d *ReleaseDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up one release of a Ghost repository: the latest release, or a release by its ID or tag.\n\n" +
			"Drafts are only visible to people who can write to the repository, and are never the latest release.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The repository's current full name and the release ID, `owner/slug:release_id`, the same as a `ghost_release` resource's import ID. " +
					"The repository part differs from `repository` when that names a repository that has since been renamed.",
				Computed: true,
			},
			"repository": schema.StringAttribute{
				MarkdownDescription: "The repository's full name, `owner/slug`, e.g. `ghost_repository.example.full_name`. A repository's old name still works after a rename.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(fullNamePattern, `must be "owner/slug", e.g. "alice/my-repo"`),
				},
			},
			"repository_id": schema.Int64Attribute{
				MarkdownDescription: "The numeric ID of `repository`.",
				Computed:            true,
			},
			"retrieve_by": schema.StringAttribute{
				MarkdownDescription: "How to find the release:\n" +
					"  - `latest`: the most recently published release that is neither a draft nor a prerelease. Ghost orders by publication " +
					"time, so a draft published today becomes the latest even if it was created before the current one.\n" +
					"  - `id`: the release with ID `release_id`.\n" +
					"  - `tag`: the release for tag `release_tag`, drafts included.",
				Required:   true,
				Validators: []validator.String{stringvalidator.OneOf(retrieveByLatest, retrieveByID, retrieveByTag)},
			},
			"release_id": schema.Int64Attribute{
				MarkdownDescription: "The numeric release ID, e.g. `ghost_release.example.release_id`. Required when `retrieve_by` is `id`, and not allowed otherwise; " +
					"set to the found release's ID.",
				Optional:   true,
				Computed:   true,
				Validators: []validator.Int64{int64validator.AtLeast(1)},
			},
			"release_tag": schema.StringAttribute{
				MarkdownDescription: "The tag, without the `refs/tags/` prefix, e.g. `v1.0.0`. Tag names are case-sensitive. Required when `retrieve_by` is `tag`, " +
					"and not allowed otherwise; set to the found release's tag.",
				Optional: true,
				Computed: true,
				Validators: []validator.String{
					stringvalidator.UTF8LengthBetween(1, 250),
					notPrefixValidator{prefix: "refs/", hint: `give the tag name only, e.g. "v1.0.0" rather than "refs/tags/v1.0.0"`},
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The release title; null when it has none (the web UI then shows the tag name).",
				Computed:            true,
			},
			"body": schema.StringAttribute{
				MarkdownDescription: "The release notes, in Markdown; null when there are none.",
				Computed:            true,
			},
			"draft": schema.BoolAttribute{
				MarkdownDescription: "Whether the release is an unpublished draft.",
				Computed:            true,
			},
			"prerelease": schema.BoolAttribute{
				MarkdownDescription: "Whether the release is marked as a prerelease.",
				Computed:            true,
			},
			"commit_sha": schema.StringAttribute{
				MarkdownDescription: "The commit the release's tag points at; null when the tag has been deleted. Ghost doesn't keep the branch a release was created from.",
				Computed:            true,
			},
			"author_login": schema.StringAttribute{
				MarkdownDescription: "The login of the user who created the release; null when their account has been deleted.",
				Computed:            true,
			},
			"node_id": schema.StringAttribute{
				MarkdownDescription: "The GraphQL node ID.",
				Computed:            true,
			},
			"url": schema.StringAttribute{
				MarkdownDescription: "The release's API URL.",
				Computed:            true,
			},
			"html_url": schema.StringAttribute{
				MarkdownDescription: "The release's page in the web UI.",
				Computed:            true,
			},
			"assets_url": schema.StringAttribute{
				MarkdownDescription: "The API URL that lists the release's assets.",
				Computed:            true,
			},
			"upload_url": schema.StringAttribute{
				MarkdownDescription: "The URI template for uploading assets, taking `name` and `label`.",
				Computed:            true,
			},
			"tarball_url": schema.StringAttribute{
				MarkdownDescription: "The API URL of the tag's source tarball.",
				Computed:            true,
			},
			"zipball_url": schema.StringAttribute{
				MarkdownDescription: "The API URL of the tag's source zip archive.",
				Computed:            true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "When the release was created.",
				Computed:            true,
			},
			"published_at": schema.StringAttribute{
				MarkdownDescription: "When the release was first published; null for a draft that has never been published.",
				Computed:            true,
			},
			"assets": schema.ListNestedAttribute{
				MarkdownDescription: "The files attached to the release, as Ghost lists them.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.Int64Attribute{
							MarkdownDescription: "The numeric asset ID.",
							Computed:            true,
						},
						"node_id": schema.StringAttribute{
							MarkdownDescription: "The GraphQL node ID.",
							Computed:            true,
						},
						"name": schema.StringAttribute{
							MarkdownDescription: "The file name.",
							Computed:            true,
						},
						"label": schema.StringAttribute{
							MarkdownDescription: "The label shown instead of the file name; null when it has none.",
							Computed:            true,
						},
						"content_type": schema.StringAttribute{
							MarkdownDescription: "The file's media type, e.g. `application/zip`.",
							Computed:            true,
						},
						"size": schema.Int64Attribute{
							MarkdownDescription: "The file size in bytes.",
							Computed:            true,
						},
						"download_count": schema.Int64Attribute{
							MarkdownDescription: "How many times the file has been downloaded.",
							Computed:            true,
						},
						"url": schema.StringAttribute{
							MarkdownDescription: "The asset's API URL.",
							Computed:            true,
						},
						"browser_download_url": schema.StringAttribute{
							MarkdownDescription: "The URL that downloads the file in a browser.",
							Computed:            true,
						},
						"created_at": schema.StringAttribute{
							MarkdownDescription: "When the asset was uploaded.",
							Computed:            true,
						},
						"updated_at": schema.StringAttribute{
							MarkdownDescription: "When the asset was last changed.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

// ValidateConfig checks that release_id and release_tag are set exactly
// when retrieve_by needs them.
func (d *ReleaseDataSource) ValidateConfig(ctx context.Context, req datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse) {
	var data releaseDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() || data.RetrieveBy.IsUnknown() || data.RetrieveBy.IsNull() {
		return
	}

	by := data.RetrieveBy.ValueString()
	check := func(p path.Path, value attr.Value, wantedBy string) {
		switch {
		case by == wantedBy && value.IsNull():
			resp.Diagnostics.AddAttributeError(p, "Missing "+p.String(),
				fmt.Sprintf("%s is required when retrieve_by is %q.", p, wantedBy))
		case by != wantedBy && !value.IsNull():
			resp.Diagnostics.AddAttributeError(p, "Unexpected "+p.String(),
				fmt.Sprintf("%s is only used when retrieve_by is %q, but retrieve_by is %q. Remove it, or set retrieve_by = %q.",
					p, wantedBy, by, wantedBy))
		}
	}
	check(path.Root("release_id"), data.ReleaseID, retrieveByID)
	check(path.Root("release_tag"), data.ReleaseTag, retrieveByTag)
}

func (d *ReleaseDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *ReleaseDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data releaseDataSourceModel
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

	// Use the current name, so id and the URLs don't depend on an old one.
	owner, repo, err := client.SplitFullName(repository.FullName)
	if err != nil {
		resp.Diagnostics.AddError("Error reading repository", err.Error())
		return
	}

	var release *client.Release
	switch data.RetrieveBy.ValueString() {
	case retrieveByLatest:
		release, err = d.client.GetLatestRelease(ctx, owner, repo)
		if client.IsNotFound(err) {
			resp.Diagnostics.AddAttributeError(path.Root("retrieve_by"), "No latest release",
				fmt.Sprintf("%s has no published release that isn't a prerelease. Drafts and prereleases are never the latest "+
					"release: look one up with retrieve_by = \"tag\" or \"id\" instead.", repository.FullName))
			return
		}
	case retrieveByID:
		id := data.ReleaseID.ValueInt64()
		release, err = d.client.GetRelease(ctx, owner, repo, id)
		if client.IsNotFound(err) {
			resp.Diagnostics.AddAttributeError(path.Root("release_id"), "Release not found",
				fmt.Sprintf("%s has no release with ID %d. It may have been deleted or belong to another repository, "+
					"or be a draft, which only people who can write to the repository can see.", repository.FullName, id))
			return
		}
	case retrieveByTag:
		tag := data.ReleaseTag.ValueString()
		release, err = d.client.GetReleaseByTag(ctx, owner, repo, tag)
		if client.IsNotFound(err) {
			// The tag route never returns drafts, but the list does.
			release, err = d.client.FindReleaseByTag(ctx, owner, repo, tag)
			if err == nil && release == nil {
				resp.Diagnostics.AddAttributeError(path.Root("release_tag"), "Release not found",
					fmt.Sprintf("%s has no release for tag %q. Tag names are case-sensitive, a tag can exist without a release, "+
						"and drafts are only visible to people who can write to the repository.", repository.FullName, tag))
				return
			}
		}
	default:
		// ValidateConfig only allows the cases above.
		resp.Diagnostics.AddAttributeError(path.Root("retrieve_by"), "Invalid retrieve_by",
			fmt.Sprintf("Unexpected retrieve_by %q.", data.RetrieveBy.ValueString()))
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading release", err.Error())
		return
	}

	data.RepositoryID = types.Int64Value(repository.ID)
	resp.Diagnostics.Append(data.fromRemote(repository.FullName, release)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// fromRemote copies release, found in the repository named fullName, into m.
func (m *releaseDataSourceModel) fromRemote(fullName string, release *client.Release) diag.Diagnostics {
	m.ID = types.StringValue(releaseID(fullName, release.ID))
	m.ReleaseID = types.Int64Value(release.ID)
	m.ReleaseTag = types.StringValue(release.TagName)
	m.Name = types.StringPointerValue(release.Name)
	m.Body = types.StringPointerValue(release.Body)
	m.Draft = types.BoolValue(release.Draft)
	m.Prerelease = types.BoolValue(release.Prerelease)
	m.CommitSHA = types.StringNull()
	if sha := release.CommitSHA(); sha != "" {
		m.CommitSHA = types.StringValue(sha)
	}
	m.AuthorLogin = types.StringNull()
	if release.Author != nil {
		m.AuthorLogin = types.StringValue(release.Author.Login)
	}
	m.NodeID = types.StringValue(release.NodeID)
	m.URL = types.StringValue(release.URL)
	m.HTMLURL = types.StringValue(release.HTMLURL)
	m.AssetsURL = types.StringValue(release.AssetsURL)
	m.UploadURL = types.StringValue(release.UploadURL)
	m.TarballURL = types.StringValue(release.TarballURL)
	m.ZipballURL = types.StringValue(release.ZipballURL)
	m.CreatedAt = types.StringValue(release.CreatedAt)
	m.PublishedAt = types.StringPointerValue(release.PublishedAt)

	assets := make([]attr.Value, 0, len(release.Assets))
	for _, a := range release.Assets {
		assets = append(assets, types.ObjectValueMust(releaseAssetAttrTypes, map[string]attr.Value{
			"id":                   types.Int64Value(a.ID),
			"node_id":              types.StringValue(a.NodeID),
			"name":                 types.StringValue(a.Name),
			"label":                types.StringPointerValue(a.Label),
			"content_type":         types.StringValue(a.ContentType),
			"size":                 types.Int64Value(a.Size),
			"download_count":       types.Int64Value(a.DownloadCount),
			"url":                  types.StringValue(a.URL),
			"browser_download_url": types.StringValue(a.BrowserDownloadURL),
			"created_at":           types.StringValue(a.CreatedAt),
			"updated_at":           types.StringValue(a.UpdatedAt),
		}))
	}

	var diags diag.Diagnostics
	m.Assets, diags = types.ListValue(types.ObjectType{AttrTypes: releaseAssetAttrTypes}, assets)

	return diags
}
