package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

var (
	_ resource.Resource                     = &ReleaseAssetResource{}
	_ resource.ResourceWithConfigure        = &ReleaseAssetResource{}
	_ resource.ResourceWithImportState      = &ReleaseAssetResource{}
	_ resource.ResourceWithModifyPlan       = &ReleaseAssetResource{}
	_ resource.ResourceWithConfigValidators = &ReleaseAssetResource{}
)

// defaultAssetContentType is the content type of an asset that doesn't set
// one.
const defaultAssetContentType = "application/octet-stream"

// NewReleaseAssetResource returns the ghost_release_asset resource.
func NewReleaseAssetResource() resource.Resource {
	return &ReleaseAssetResource{}
}

// ReleaseAssetResource manages a file attached to a release.
type ReleaseAssetResource struct {
	client *client.Client
}

// releaseAssetResourceModel maps the ghost_release_asset schema.
type releaseAssetResourceModel struct {
	ID                 types.String `tfsdk:"id"`
	Repository         types.String `tfsdk:"repository"`
	RepositoryID       types.Int64  `tfsdk:"repository_id"`
	ReleaseID          types.Int64  `tfsdk:"release_id"`
	Name               types.String `tfsdk:"name"`
	Label              types.String `tfsdk:"label"`
	ContentType        types.String `tfsdk:"content_type"`
	File               types.String `tfsdk:"file"`
	Content            types.String `tfsdk:"content"`
	ContentBase64      types.String `tfsdk:"content_base64"`
	ContentSHA256      types.String `tfsdk:"content_sha256"`
	Size               types.Int64  `tfsdk:"size"`
	AssetID            types.Int64  `tfsdk:"asset_id"`
	NodeID             types.String `tfsdk:"node_id"`
	URL                types.String `tfsdk:"url"`
	BrowserDownloadURL types.String `tfsdk:"browser_download_url"`
	DownloadCount      types.Int64  `tfsdk:"download_count"`
	CreatedAt          types.String `tfsdk:"created_at"`
	UpdatedAt          types.String `tfsdk:"updated_at"`
}

var (
	// assetContentTypePattern is a media type without parameters (RFC 6838
	// restricted names). Ghost drops parameters, so allowing them would
	// cause a permanent diff.
	assetContentTypePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]{0,126}/[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]{0,126}$`)
	// trimmedPattern matches "" or a string with no leading or trailing
	// whitespace. Ghost trims labels.
	trimmedPattern = regexp.MustCompile(`^(\S(.*\S)?)?$`)
)

func (r *ReleaseAssetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_release_asset"
}

func (r *ReleaseAssetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	stableString := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	stableInt := []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Uploads a file to a release in a Ghost repository, as a release asset.\n\n" +
			"The content comes from exactly one of `file`, `content` or `content_base64`. The provider hashes it at plan time " +
			"(`content_sha256`), so changing the bytes replaces the asset (Ghost can't change an asset's content in place). " +
			"Renaming the asset or changing its label is done in place.\n\n" +
			"Ghost rejects empty files, accepts files up to 2 GiB on the public instance, and counts assets against the " +
			"repository owner's storage quota. Assets of a draft release are visible only to people who can write to the " +
			"repository. Deleting the release deletes its assets.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The repository's full name and the numeric asset ID, `owner/slug:asset_id`.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
			"repository": schema.StringAttribute{
				MarkdownDescription: "The repository's full name, `owner/slug`, e.g. `ghost_repository.example.full_name`. Renaming the repository updates this in place; pointing it at a different repository forces a new asset.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(fullNamePattern, `must be "owner/slug", e.g. "alice/my-repo"`),
				},
			},
			"repository_id": schema.Int64Attribute{
				MarkdownDescription: "The numeric ID of `repository`.",
				Computed:            true,
				PlanModifiers:       stableInt,
			},
			"release_id": schema.Int64Attribute{
				MarkdownDescription: "The numeric ID of the release, e.g. `ghost_release.example.release_id`. Changing it forces a new asset.",
				Required:            true,
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.RequiresReplace()},
				Validators:          []validator.Int64{int64validator.AtLeast(1)},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The file name the asset is downloaded under, up to 255 characters, unique within the release. Names are case-sensitive, and can't contain `/`, `\\` or control characters, start or end with whitespace, or be `.` or `..`. Changing it renames the asset in place, which changes `browser_download_url`.",
				Required:            true,
				Validators: []validator.String{
					assetNameValidator{},
				},
			},
			"label": schema.StringAttribute{
				MarkdownDescription: "A short description the web UI shows instead of the file name, up to 255 characters. Changed in place; `\"\"` and null both mean no label.",
				Optional:            true,
				Validators: []validator.String{
					stringvalidator.UTF8LengthAtMost(255),
					stringvalidator.RegexMatches(trimmedPattern, "must not start or end with whitespace (Ghost trims labels)"),
				},
			},
			"content_type": schema.StringAttribute{
				MarkdownDescription: "The media type the asset is served with, e.g. `application/gzip`, without parameters such as `charset` (Ghost drops them). Defaults to `" + defaultAssetContentType + "`. Changing it forces a new asset.",
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString(defaultAssetContentType),
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					stringvalidator.RegexMatches(assetContentTypePattern, `must be a media type "type/subtype" without parameters, e.g. "application/gzip" or "text/plain"`),
				},
			},
			"file": schema.StringAttribute{
				MarkdownDescription: "The path of a local file to upload. It is read at plan time to work out `content_sha256`, so a changed file replaces the asset. A file that doesn't exist yet at plan time (one created during the apply) is read at apply time. Conflicts with `content` and `content_base64`.",
				Optional:            true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"content": schema.StringAttribute{
				MarkdownDescription: "The asset's content as a UTF-8 string, which is stored in state. Prefer `file` for anything large. Conflicts with `file` and `content_base64`.",
				Optional:            true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"content_base64": schema.StringAttribute{
				MarkdownDescription: "The asset's content, base64-encoded, for binary data, e.g. `filebase64(\"dist/app.zip\")`. It is stored in state; prefer `file` for anything large. Conflicts with `file` and `content`.",
				Optional:            true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					base64Validator{},
				},
			},
			"content_sha256": schema.StringAttribute{
				MarkdownDescription: "The hex SHA-256 of the content. A change replaces the asset. After import it is computed by downloading the asset.",
				Computed:            true,
			},
			"size": schema.Int64Attribute{
				MarkdownDescription: "The size of the asset in bytes.",
				Computed:            true,
			},
			"asset_id": schema.Int64Attribute{
				MarkdownDescription: "The numeric asset ID.",
				Computed:            true,
				PlanModifiers:       stableInt,
			},
			"node_id": schema.StringAttribute{
				MarkdownDescription: "The GraphQL node ID.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
			"url": schema.StringAttribute{
				MarkdownDescription: "The asset's API URL. Ask it for `application/octet-stream` to download the content.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
			"browser_download_url": schema.StringAttribute{
				MarkdownDescription: "The download link in the web UI, `…/releases/download/<tag>/<name>`. Anyone who can read the repository can use it once the release is published.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
			"download_count": schema.Int64Attribute{
				MarkdownDescription: "How many times the asset has been downloaded, as of the last refresh.",
				Computed:            true,
				PlanModifiers:       stableInt,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "When the asset was uploaded.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
			"updated_at": schema.StringAttribute{
				MarkdownDescription: "When the asset was last changed. Ghost also bumps it when the asset is downloaded, along with `download_count`.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
		},
	}
}

func (r *ReleaseAssetResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.ExactlyOneOf(path.MatchRoot("file"), path.MatchRoot("content"), path.MatchRoot("content_base64")),
	}
}

func (r *ReleaseAssetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ModifyPlan hashes the content, replaces the asset when the hash changes,
// resolves a repository change (the same repository renamed is in place,
// another repository forces a new asset), and marks the computed attributes
// an update can change as unknown.
func (r *ReleaseAssetResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}

	var plan releaseAssetResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state *releaseAssetResourceModel
	if !req.State.Raw.IsNull() {
		state = &releaseAssetResourceModel{}
		resp.Diagnostics.Append(req.State.Get(ctx, state)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	sum, size, known, diags := planContentHash(plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	switch {
	case known:
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("content_sha256"), sum)...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("size"), size)...)
		if state != nil && !state.ContentSHA256.IsNull() && state.ContentSHA256.ValueString() != sum {
			resp.RequiresReplace.Append(path.Root("content_sha256"))
		}
	case state != nil && sameContentSource(plan, *state):
		// The file isn't there to hash (e.g. a build directory was
		// cleaned), but the configuration hasn't changed: keep the asset.
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("content_sha256"), state.ContentSHA256)...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("size"), state.Size)...)
	case state != nil:
		// The content comes from somewhere else and can't be hashed yet.
		resp.RequiresReplace.Append(path.Root("content_sha256"))
	}

	if state == nil || resp.RequiresReplace.Contains(path.Root("content_sha256")) {
		return
	}

	if !plan.Repository.Equal(state.Repository) && !plan.Repository.IsUnknown() {
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

	if !plan.ReleaseID.Equal(state.ReleaseID) || !plan.ContentType.Equal(state.ContentType) {
		// RequiresReplace handles it.
		return
	}

	renamed := !plan.Name.Equal(state.Name)
	relabelled := normalizedLabel(plan.Label) != normalizedLabel(state.Label)
	moved := !plan.Repository.Equal(state.Repository)
	if renamed || relabelled || moved {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("updated_at"), types.StringUnknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("download_count"), types.Int64Unknown())...)
	}
	if renamed || moved {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("browser_download_url"), types.StringUnknown())...)
	}
	if moved {
		for _, attr := range []string{"id", "url"} {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(attr), types.StringUnknown())...)
		}
	}
}

func (r *ReleaseAssetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan releaseAssetResourceModel
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
			fmt.Sprintf("Repository %s not found, or the token can't read it.", plan.Repository.ValueString()))
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading repository", err.Error())
		return
	}
	plan.RepositoryID = types.Int64Value(repository.ID)

	content, diags := openContent(plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	defer func() { _ = content.Close() }()

	if !plan.ContentSHA256.IsUnknown() && !plan.ContentSHA256.IsNull() && plan.ContentSHA256.ValueString() != content.sha256 {
		resp.Diagnostics.AddAttributeError(path.Root("file"), "File changed since the plan",
			fmt.Sprintf("%s has changed since Terraform planned this apply (SHA-256 %s, planned %s). Run terraform apply again.",
				plan.File.ValueString(), content.sha256, plan.ContentSHA256.ValueString()))
		return
	}
	if content.size == 0 {
		resp.Diagnostics.AddAttributeError(content.attr, "Empty asset", "Ghost doesn't accept empty files: the asset's content must be at least 1 byte.")
		return
	}

	name := plan.Name.ValueString()
	releaseID := plan.ReleaseID.ValueInt64()
	asset, err := r.client.UploadReleaseAsset(ctx, owner, repo, releaseID, client.UploadReleaseAssetRequest{
		Name:        name,
		Label:       plan.Label.ValueString(),
		ContentType: plan.ContentType.ValueString(),
		Content:     content.reader,
		Size:        content.size,
	})
	if err != nil {
		resp.Diagnostics.Append(r.uploadError(ctx, plan, err)...)
		return
	}

	plan.ContentSHA256 = types.StringValue(content.sha256)
	plan.fromRemote(asset)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// uploadError explains why an upload failed.
func (r *ReleaseAssetResource) uploadError(ctx context.Context, plan releaseAssetResourceModel, err error) diag.Diagnostics {
	var diags diag.Diagnostics
	repository := plan.Repository.ValueString()
	name := plan.Name.ValueString()
	releaseID := plan.ReleaseID.ValueInt64()

	switch {
	case client.IsFieldError(err, "name", "already_exists"):
		detail := fmt.Sprintf("Release %d in %s already has an asset named %q, and names are unique within a release.", releaseID, repository, name)
		owner, repo, _ := client.SplitFullName(repository)
		if assets, listErr := r.client.ListReleaseAssets(ctx, owner, repo, releaseID); listErr == nil {
			for _, a := range assets {
				if a.Name == name {
					detail += fmt.Sprintf(" To manage it, import it with: terraform import <resource address> %q", releaseAssetID(repository, a.ID))
					break
				}
			}
		}
		diags.AddAttributeError(path.Root("name"), "Release asset already exists", fmt.Sprintf("%s (%s)", detail, err))
	case client.IsFieldError(err, "name", "invalid"):
		diags.AddAttributeError(path.Root("name"), "Invalid asset name",
			fmt.Sprintf("Ghost rejected the file name %q: it must be a plain file name, without \"/\", \"\\\" or control characters. (%s)", name, err))
	case client.IsFieldError(err, "content_type", "invalid"):
		diags.AddAttributeError(path.Root("content_type"), "Invalid content type",
			fmt.Sprintf("Ghost rejected the content type %q. (%s)", plan.ContentType.ValueString(), err))
	case client.IsNotFound(err):
		diags.AddAttributeError(path.Root("release_id"), "Release not found",
			fmt.Sprintf("Release %d not found in %s, or the token can't write to it. (%s)", releaseID, repository, err))
	case client.HasStatus(err, http.StatusRequestEntityTooLarge):
		diags.AddError("Release asset too large",
			fmt.Sprintf("Ghost refused the upload: the file is over the per-file limit, or would take the repository's owner over "+
				"their release asset storage quota. GET /api/storage/<owner> reports both (maxAssetBytes and asset.quotaBytes). (%s)", err))
	case client.HasStatus(err, http.StatusLengthRequired):
		diags.AddError("Empty asset", fmt.Sprintf("Ghost doesn't accept empty files. (%s)", err))
	case client.IsForbidden(err):
		diags.AddError("Not allowed to upload",
			fmt.Sprintf("The token can't upload assets to %s: it needs write access to the repository. (%s)", repository, err))
	default:
		diags.AddError("Error uploading release asset", err.Error())
	}

	return diags
}

func (r *ReleaseAssetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state releaseAssetResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	owner, repo, err := client.SplitFullName(state.Repository.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("repository"), "Invalid repository", err.Error())
		return
	}

	asset, err := r.client.GetReleaseAsset(ctx, owner, repo, state.AssetID.ValueInt64())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading release asset", err.Error())
		return
	}

	// An imported asset has only its repository and ID.
	if state.RepositoryID.IsNull() {
		repository, err := r.client.GetRepository(ctx, owner, repo)
		if err != nil {
			resp.Diagnostics.AddError("Error reading repository", err.Error())
			return
		}
		state.RepositoryID = types.Int64Value(repository.ID)
	}
	if state.ReleaseID.IsNull() {
		releaseID, err := r.findAssetRelease(ctx, owner, repo, asset.ID)
		if err != nil {
			resp.Diagnostics.AddError("Error finding the asset's release", err.Error())
			return
		}
		state.ReleaseID = types.Int64Value(releaseID)
	}
	if state.ContentSHA256.IsNull() {
		h := sha256.New()
		if err := r.client.DownloadReleaseAsset(ctx, owner, repo, asset.ID, h); err != nil {
			resp.Diagnostics.AddError("Error downloading release asset", err.Error())
			return
		}
		state.ContentSHA256 = types.StringValue(hex.EncodeToString(h.Sum(nil)))
	}

	state.fromRemote(asset)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// findAssetRelease returns the ID of the release an asset belongs to, by
// listing the repository's releases, drafts included.
func (r *ReleaseAssetResource) findAssetRelease(ctx context.Context, owner, repo string, assetID int64) (int64, error) {
	releases, err := r.client.ListReleases(ctx, owner, repo)
	if err != nil {
		return 0, err
	}
	for _, release := range releases {
		for _, a := range release.Assets {
			if a.ID == assetID {
				return release.ID, nil
			}
		}
	}

	return 0, fmt.Errorf("no release of %s/%s lists asset %d", owner, repo, assetID)
}

func (r *ReleaseAssetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state releaseAssetResourceModel
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

	moved := !plan.Repository.Equal(state.Repository)
	if moved {
		repository, err := r.client.GetRepository(ctx, owner, repo)
		if err != nil {
			resp.Diagnostics.AddError("Error reading repository", err.Error())
			return
		}
		if !sameRepository(state.RepositoryID, repository) {
			resp.Diagnostics.AddAttributeError(path.Root("repository"), "Release asset can't be moved",
				fmt.Sprintf("%s is a different repository from the one this asset was uploaded to. "+
					"Assets can't be moved; replace it with terraform apply -replace=<address>.", plan.Repository.ValueString()))
			return
		}
		plan.RepositoryID = types.Int64Value(repository.ID)
	}

	id := state.AssetID.ValueInt64()
	var update client.UpdateReleaseAssetRequest
	if !plan.Name.Equal(state.Name) {
		update.Name = plan.Name.ValueStringPointer()
	}
	if label := normalizedLabel(plan.Label); label != normalizedLabel(state.Label) {
		// "" clears the label; null makes Ghost fail.
		update.Label = &label
	}

	var asset *client.ReleaseAsset
	switch {
	case update.Name != nil || update.Label != nil:
		asset, err = r.client.UpdateReleaseAsset(ctx, owner, repo, id, update)
		if client.IsFieldError(err, "name", "already_exists") {
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Release asset already exists",
				fmt.Sprintf("Release %d in %s already has an asset named %q. (%s)", state.ReleaseID.ValueInt64(), plan.Repository.ValueString(), plan.Name.ValueString(), err))
			return
		}
	case moved:
		asset, err = r.client.GetReleaseAsset(ctx, owner, repo, id)
	}
	if err != nil {
		resp.Diagnostics.AddError("Error updating release asset", err.Error())
		return
	}

	plan.AssetID = state.AssetID
	if plan.ContentSHA256.IsUnknown() {
		plan.ContentSHA256 = state.ContentSHA256
	}
	if asset != nil {
		plan.fromRemote(asset)
	} else {
		// Only where the content comes from changed: nothing to send.
		plan.copyComputed(state)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ReleaseAssetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state releaseAssetResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	owner, repo, err := client.SplitFullName(state.Repository.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("repository"), "Invalid repository", err.Error())
		return
	}

	err = r.client.DeleteReleaseAsset(ctx, owner, repo, state.AssetID.ValueInt64())
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error deleting release asset", err.Error())
	}
}

func (r *ReleaseAssetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	repository, id, err := parseReleaseAssetID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("repository"), repository)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("asset_id"), id)...)
}

// fromRemote copies asset into m. The repository and content attributes are
// kept as configured, and a configured "" label is kept for Ghost's null.
func (m *releaseAssetResourceModel) fromRemote(asset *client.ReleaseAsset) {
	m.ID = types.StringValue(releaseAssetID(m.Repository.ValueString(), asset.ID))
	m.AssetID = types.Int64Value(asset.ID)
	m.NodeID = types.StringValue(asset.NodeID)
	m.Name = types.StringValue(asset.Name)
	m.Label = descriptionFromRemote(m.Label, asset.Label)
	m.ContentType = types.StringValue(asset.ContentType)
	m.Size = types.Int64Value(asset.Size)
	m.URL = types.StringValue(asset.URL)
	m.BrowserDownloadURL = types.StringValue(asset.BrowserDownloadURL)
	m.DownloadCount = types.Int64Value(asset.DownloadCount)
	m.CreatedAt = types.StringValue(asset.CreatedAt)
	m.UpdatedAt = types.StringValue(asset.UpdatedAt)
}

// copyComputed copies the computed attributes from state into m.
func (m *releaseAssetResourceModel) copyComputed(state releaseAssetResourceModel) {
	m.ID = state.ID
	m.RepositoryID = state.RepositoryID
	m.ContentSHA256 = state.ContentSHA256
	m.Size = state.Size
	m.NodeID = state.NodeID
	m.URL = state.URL
	m.BrowserDownloadURL = state.BrowserDownloadURL
	m.DownloadCount = state.DownloadCount
	m.CreatedAt = state.CreatedAt
	m.UpdatedAt = state.UpdatedAt
}

// normalizedLabel returns a label as Ghost stores it, "" for none.
func normalizedLabel(s types.String) string {
	if s.IsNull() || s.IsUnknown() {
		return ""
	}

	return s.ValueString()
}

// sameContentSource reports whether plan and state take their content from
// the same file, string or base64 string.
func sameContentSource(plan, state releaseAssetResourceModel) bool {
	return plan.File.Equal(state.File) && plan.Content.Equal(state.Content) && plan.ContentBase64.Equal(state.ContentBase64)
}

// planContentHash hashes the planned content. known is false when the
// content isn't known yet: a value that is unknown until apply, or a file
// that doesn't exist yet.
func planContentHash(m releaseAssetResourceModel) (sum string, size int64, known bool, diags diag.Diagnostics) {
	switch {
	case m.File.IsUnknown() || m.Content.IsUnknown() || m.ContentBase64.IsUnknown():
		return "", 0, false, nil
	case !m.Content.IsNull():
		sum, size = hashBytes([]byte(m.Content.ValueString()))
	case !m.ContentBase64.IsNull():
		b, err := base64.StdEncoding.DecodeString(m.ContentBase64.ValueString())
		if err != nil {
			diags.AddAttributeError(path.Root("content_base64"), "Invalid base64", err.Error())
			return "", 0, false, diags
		}
		sum, size = hashBytes(b)
	case !m.File.IsNull():
		var err error
		sum, size, err = hashFile(m.File.ValueString())
		if errors.Is(err, fs.ErrNotExist) {
			return "", 0, false, nil
		}
		if err != nil {
			diags.AddAttributeError(path.Root("file"), "Can't read file", err.Error())
			return "", 0, false, diags
		}
	default:
		// ExactlyOneOf reports it.
		return "", 0, false, nil
	}

	if size == 0 {
		diags.AddAttributeError(contentAttr(m), "Empty asset", "Ghost doesn't accept empty files: the asset's content must be at least 1 byte.")
		return "", 0, false, diags
	}

	return sum, size, true, nil
}

// contentAttr returns the attribute the content comes from.
func contentAttr(m releaseAssetResourceModel) path.Path {
	switch {
	case !m.Content.IsNull():
		return path.Root("content")
	case !m.ContentBase64.IsNull():
		return path.Root("content_base64")
	default:
		return path.Root("file")
	}
}

func hashBytes(b []byte) (string, int64) {
	h := sha256.Sum256(b)

	return hex.EncodeToString(h[:]), int64(len(b))
}

func hashFile(name string) (string, int64, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, fmt.Errorf("reading %s: %w", name, err)
	}

	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// assetContent is the content to upload.
type assetContent struct {
	reader io.ReaderAt
	size   int64
	sha256 string
	attr   path.Path
	close  func() error
}

func (c *assetContent) Close() error {
	if c.close == nil {
		return nil
	}

	return c.close()
}

// openContent opens the configured content and hashes it.
func openContent(m releaseAssetResourceModel) (*assetContent, diag.Diagnostics) {
	var diags diag.Diagnostics
	attr := contentAttr(m)

	switch {
	case !m.Content.IsNull():
		b := []byte(m.Content.ValueString())
		sum, size := hashBytes(b)
		return &assetContent{reader: bytes.NewReader(b), size: size, sha256: sum, attr: attr}, nil
	case !m.ContentBase64.IsNull():
		b, err := base64.StdEncoding.DecodeString(m.ContentBase64.ValueString())
		if err != nil {
			diags.AddAttributeError(attr, "Invalid base64", err.Error())
			return nil, diags
		}
		sum, size := hashBytes(b)
		return &assetContent{reader: bytes.NewReader(b), size: size, sha256: sum, attr: attr}, nil
	}

	name := m.File.ValueString()
	sum, size, err := hashFile(name)
	if err != nil {
		diags.AddAttributeError(attr, "Can't read file", err.Error())
		return nil, diags
	}
	f, err := os.Open(name)
	if err != nil {
		diags.AddAttributeError(attr, "Can't read file", err.Error())
		return nil, diags
	}

	return &assetContent{reader: f, size: size, sha256: sum, attr: attr, close: f.Close}, nil
}

// releaseAssetID builds the "owner/slug:asset_id" ID of a release asset.
func releaseAssetID(repository string, id int64) string {
	return repository + ":" + strconv.FormatInt(id, 10)
}

// parseReleaseAssetID parses an "owner/slug:asset_id" ID.
func parseReleaseAssetID(id string) (repository string, assetID int64, err error) {
	repository, rawID, ok := strings.Cut(id, ":")
	n, parseErr := strconv.ParseInt(rawID, 10, 64)
	if _, _, splitErr := client.SplitFullName(repository); !ok || splitErr != nil || parseErr != nil || n <= 0 {
		return "", 0, fmt.Errorf("import ID must be \"owner/repo:asset_id\" with a numeric asset ID, e.g. \"alice/my-repo:42\", got %q", id)
	}

	return repository, n, nil
}

// assetNameValidator checks a release asset's file name the way Ghost does.
type assetNameValidator struct{}

func (v assetNameValidator) Description(_ context.Context) string {
	return `must be a file name of 1 to 255 characters, without "/", "\" or control characters, not starting or ending with whitespace, and not "." or ".."`
}

func (v assetNameValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v assetNameValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if problem := assetNameProblem(req.ConfigValue.ValueString()); problem != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid asset name",
			fmt.Sprintf("%s %s: %s, got %q", req.Path, v.Description(ctx), problem, req.ConfigValue.ValueString()))
	}
}

// assetNameProblem says what is wrong with a release asset name, or returns
// "" when Ghost accepts it unchanged.
func assetNameProblem(name string) string {
	switch {
	case name == "":
		return "it is empty"
	case len([]rune(name)) > 255:
		return fmt.Sprintf("it is %d characters long", len([]rune(name)))
	case name == "." || name == "..":
		return "it isn't a file name"
	case strings.ContainsAny(name, `/\`):
		return "it contains a path separator"
	case strings.IndexFunc(name, unicode.IsControl) >= 0:
		return "it contains a control character"
	case strings.TrimSpace(name) != name:
		return "Ghost trims whitespace from the ends"
	}

	return ""
}

// base64Validator checks that a string is standard, padded base64.
type base64Validator struct{}

func (v base64Validator) Description(_ context.Context) string {
	return "must be standard base64, as filebase64() and base64encode() produce"
}

func (v base64Validator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v base64Validator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if _, err := base64.StdEncoding.DecodeString(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid attribute value",
			fmt.Sprintf("%s %s: %s", req.Path, v.Description(ctx), err))
	}
}
