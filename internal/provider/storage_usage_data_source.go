package provider

import (
	"context"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

var (
	_ datasource.DataSource              = &StorageUsageDataSource{}
	_ datasource.DataSourceWithConfigure = &StorageUsageDataSource{}
)

// NewStorageUsageDataSource returns the ghost_storage_usage data source.
func NewStorageUsageDataSource() datasource.DataSource {
	return &StorageUsageDataSource{}
}

// StorageUsageDataSource reads the storage an account uses against its
// quotas.
type StorageUsageDataSource struct {
	client *client.Client
}

// storageUsageDataSourceModel maps the ghost_storage_usage data source
// schema.
type storageUsageDataSourceModel struct {
	ID            types.String       `tfsdk:"id"`
	Owner         types.String       `tfsdk:"owner"`
	UsedBytes     types.Int64        `tfsdk:"used_bytes"`
	QuotaBytes    types.Int64        `tfsdk:"quota_bytes"`
	MaxAssetBytes types.Int64        `tfsdk:"max_asset_bytes"`
	Fork          *storageLimitModel `tfsdk:"fork"`
	LFS           *storageLimitModel `tfsdk:"lfs"`
	Asset         *storageLimitModel `tfsdk:"asset"`
}

// storageLimitModel is the usage against one quota: `fork`, `lfs` or
// `asset`.
type storageLimitModel struct {
	UsedBytes  types.Int64 `tfsdk:"used_bytes"`
	QuotaBytes types.Int64 `tfsdk:"quota_bytes"`
}

func newStorageLimitModel(u client.StorageLimitUsage) *storageLimitModel {
	return &storageLimitModel{UsedBytes: types.Int64Value(u.UsedBytes), QuotaBytes: types.Int64PointerValue(u.QuotaBytes)}
}

func (d *StorageUsageDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_storage_usage"
}

func (d *StorageUsageDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	limit := func(description string) schema.SingleNestedAttribute {
		return schema.SingleNestedAttribute{
			MarkdownDescription: description,
			Computed:            true,
			Attributes: map[string]schema.Attribute{
				"used_bytes": schema.Int64Attribute{
					MarkdownDescription: "Bytes counted against this quota.",
					Computed:            true,
				},
				"quota_bytes": schema.Int64Attribute{
					MarkdownDescription: "The quota in bytes, null when none applies.",
					Computed:            true,
				},
			},
		}
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads the storage a Ghost account uses against each of its quotas, in bytes: git data in its repositories, " +
			"forks, Git LFS objects and release assets. Use it in a `check` block or a `precondition` to warn before a push or an upload " +
			"would go over a quota.\n\n" +
			"Ghost shows storage usage only to the account itself, so `owner` must be the user that owns the provider's token or an " +
			"organization that user belongs to. The figures are current: a push counts as soon as it lands, and a deleted repository or release asset stops counting at once. " +
			"Deleted attachments stop counting a day later, and deleting a branch frees nothing.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The same as `owner`.",
				Computed:            true,
			},
			"owner": schema.StringAttribute{
				MarkdownDescription: "The user or organization login, matched case-insensitively. Omit it to read the user that owns the provider's token.",
				Optional:            true,
				Computed:            true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					stringvalidator.RegexMatches(regexp.MustCompile(`^[^/\s]+$`), "must be a single login, without slashes or spaces"),
				},
			},
			"used_bytes": schema.Int64Attribute{
				MarkdownDescription: "Bytes of pushed git data and merged pull requests' heads in the account's repositories that are not forks. " +
					"Git LFS objects, release assets and attachments are counted separately.",
				Computed: true,
			},
			"quota_bytes": schema.Int64Attribute{
				MarkdownDescription: "The quota for `used_bytes`, null when none applies.",
				Computed:            true,
			},
			"max_asset_bytes": schema.Int64Attribute{
				MarkdownDescription: "The largest release asset Ghost accepts, in bytes.",
				Computed:            true,
			},
			"fork": limit("Everything the account's forks hold, Git LFS objects and release assets included, under one quota."),
			"lfs":  limit("Git LFS objects in the account's repositories that are not forks."),
			"asset": limit("Release assets in the account's repositories that are not forks and, for a user, files they attached to " +
				"issues, pull requests and comments anywhere, uploads in progress included."),
		},
	}
}

func (d *StorageUsageDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *StorageUsageDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data storageUsageDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	owner := data.Owner.ValueString()
	if data.Owner.IsNull() || data.Owner.IsUnknown() {
		user, err := d.client.GetAuthenticatedUser(ctx)
		if err != nil {
			resp.Diagnostics.AddError("Error reading the authenticated user", err.Error())
			return
		}
		owner = user.Login
	}

	usage, err := d.client.GetStorageUsage(ctx, owner)
	if client.IsNotFound(err) {
		// Ghost answers the same 404 for an unknown name and for an account
		// it won't show, so look the name up to say which.
		detail := fmt.Sprintf("No user or organization named %q exists (%s).", owner, err)
		if _, userErr := d.client.GetUser(ctx, owner); userErr == nil {
			detail = fmt.Sprintf("Ghost shows storage usage only to the account itself: %q must be the user that owns the provider's token "+
				"or an organization that user belongs to (%s).", owner, err)
		}
		resp.Diagnostics.AddAttributeError(path.Root("owner"), "Storage usage not found", detail)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading storage usage", err.Error())
		return
	}

	data.ID = types.StringValue(owner)
	data.Owner = types.StringValue(owner)
	data.UsedBytes = types.Int64Value(usage.UsedBytes)
	data.QuotaBytes = types.Int64PointerValue(usage.QuotaBytes)
	data.MaxAssetBytes = types.Int64Value(usage.MaxAssetBytes)
	data.Fork = newStorageLimitModel(usage.Fork)
	data.LFS = newStorageLimitModel(usage.LFS)
	data.Asset = newStorageLimitModel(usage.Asset)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
