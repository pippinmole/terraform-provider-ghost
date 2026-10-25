package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

var (
	_ datasource.DataSource              = &UserDataSource{}
	_ datasource.DataSourceWithConfigure = &UserDataSource{}
)

// NewUserDataSource returns the ghost_user data source.
func NewUserDataSource() datasource.DataSource {
	return &UserDataSource{}
}

// UserDataSource looks up a Ghost user or organization.
type UserDataSource struct {
	client *client.Client
}

// userDataSourceModel maps the ghost_user data source schema.
type userDataSourceModel struct {
	ID        types.String `tfsdk:"id"`
	Login     types.String `tfsdk:"login"`
	UserID    types.Int64  `tfsdk:"user_id"`
	NodeID    types.String `tfsdk:"node_id"`
	Name      types.String `tfsdk:"name"`
	Type      types.String `tfsdk:"type"`
	SiteAdmin types.Bool   `tfsdk:"site_admin"`
	AvatarURL types.String `tfsdk:"avatar_url"`
	HTMLURL   types.String `tfsdk:"html_url"`
	CreatedAt types.String `tfsdk:"created_at"`
	UpdatedAt types.String `tfsdk:"updated_at"`
}

func (d *UserDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user"
}

func (d *UserDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a Ghost user or organization by `login`, or the authenticated user when `login` is omitted.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The login.",
				Computed:            true,
			},
			"login": schema.StringAttribute{
				MarkdownDescription: "The login to look up. Ghost matches logins case-sensitively. Omit it to look up the user that owns the provider's token.",
				Optional:            true,
				Computed:            true,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"user_id": schema.Int64Attribute{
				MarkdownDescription: "The numeric user ID.",
				Computed:            true,
			},
			"node_id": schema.StringAttribute{
				MarkdownDescription: "The GraphQL node ID.",
				Computed:            true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The display name, null when none is set.",
				Computed:            true,
			},
			"type": schema.StringAttribute{
				MarkdownDescription: "Either `User` or `Organization`.",
				Computed:            true,
			},
			"site_admin": schema.BoolAttribute{
				MarkdownDescription: "Whether the user is a Ghost site administrator.",
				Computed:            true,
			},
			"avatar_url": schema.StringAttribute{
				MarkdownDescription: "The avatar image URL.",
				Computed:            true,
			},
			"html_url": schema.StringAttribute{
				MarkdownDescription: "The profile's web URL.",
				Computed:            true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "When the account was created, as an RFC 3339 timestamp.",
				Computed:            true,
			},
			"updated_at": schema.StringAttribute{
				MarkdownDescription: "When the account was last updated, as an RFC 3339 timestamp.",
				Computed:            true,
			},
		},
	}
}

func (d *UserDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *UserDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data userDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var (
		user *client.User
		err  error
	)
	if data.Login.IsNull() {
		user, err = d.client.GetAuthenticatedUser(ctx)
	} else {
		user, err = d.client.GetUser(ctx, data.Login.ValueString())
	}
	if client.IsNotFound(err) {
		resp.Diagnostics.AddError("User not found",
			fmt.Sprintf("No user or organization with login %q exists. Logins are case-sensitive.", data.Login.ValueString()))
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading user", err.Error())
		return
	}

	data.fromRemote(user)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// fromRemote sets every attribute of m from u.
func (m *userDataSourceModel) fromRemote(u *client.User) {
	m.ID = types.StringValue(u.Login)
	m.Login = m.ID
	m.UserID = types.Int64Value(u.ID)
	m.NodeID = types.StringValue(u.NodeID)
	m.Name = types.StringNull()
	if u.Name != "" {
		m.Name = types.StringValue(u.Name)
	}
	m.Type = types.StringValue(u.Type)
	m.SiteAdmin = types.BoolValue(u.SiteAdmin)
	m.AvatarURL = types.StringValue(u.AvatarURL)
	m.HTMLURL = types.StringValue(u.HTMLURL)
	m.CreatedAt = types.StringValue(u.CreatedAt)
	m.UpdatedAt = types.StringValue(u.UpdatedAt)
}
