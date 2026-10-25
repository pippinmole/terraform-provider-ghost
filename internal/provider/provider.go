package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

const (
	envToken   = "GHOST_TOKEN"
	envBaseURL = "GHOST_BASE_URL"
)

var _ provider.Provider = &GhostProvider{}

// GhostProvider is the Terraform provider for Ghost.
type GhostProvider struct {
	// version is "dev" for local builds, "test" in acceptance tests, and the
	// release version when built by GoReleaser.
	version string
}

// GhostProviderModel maps the provider block.
type GhostProviderModel struct {
	Token   types.String `tfsdk:"token"`
	BaseURL types.String `tfsdk:"base_url"`
}

// New returns a constructor for the provider.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &GhostProvider{version: version}
	}
}

func (p *GhostProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "ghost"
	resp.Version = p.version
}

func (p *GhostProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manage resources on [Ghost](https://web.ghost.procrastinator.fyi), a GitHub alternative.",
		Attributes: map[string]schema.Attribute{
			"token": schema.StringAttribute{
				MarkdownDescription: "Ghost personal access token. May also be set with the `" + envToken + "` environment variable.",
				Optional:            true,
				Sensitive:           true,
			},
			"base_url": schema.StringAttribute{
				MarkdownDescription: "Base URL of the Ghost API. May also be set with the `" + envBaseURL + "` environment variable. Defaults to `" + client.DefaultBaseURL + "`.",
				Optional:            true,
			},
		},
	}
}

func (p *GhostProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config GhostProviderModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if config.Token.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("token"), "Unknown Ghost token",
			"The provider cannot create the Ghost API client because the token is not known until apply. "+
				"Set it statically or use the "+envToken+" environment variable.")
	}
	if config.BaseURL.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("base_url"), "Unknown Ghost base URL",
			"The provider cannot create the Ghost API client because the base URL is not known until apply. "+
				"Set it statically or use the "+envBaseURL+" environment variable.")
	}
	if resp.Diagnostics.HasError() {
		return
	}

	token := stringValueOrEnv(config.Token, envToken)
	baseURL := stringValueOrEnv(config.BaseURL, envBaseURL)
	if baseURL == "" {
		baseURL = client.DefaultBaseURL
	}

	if token == "" {
		resp.Diagnostics.AddAttributeError(path.Root("token"), "Missing Ghost token",
			"Set the token argument in the provider block or the "+envToken+" environment variable.")
		return
	}

	c, err := client.New(baseURL, token, client.WithUserAgent("terraform-provider-ghost/"+p.version))
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("base_url"), "Invalid Ghost base URL", err.Error())
		return
	}

	resp.DataSourceData = c
	resp.ResourceData = c
}

func (p *GhostProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{}
}

func (p *GhostProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{}
}

// stringValueOrEnv returns the configured value, falling back to the named
// environment variable when the attribute is null.
func stringValueOrEnv(v types.String, env string) string {
	if !v.IsNull() {
		return v.ValueString()
	}

	return os.Getenv(env)
}
