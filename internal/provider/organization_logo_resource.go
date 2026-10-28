package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

var (
	_ resource.Resource                     = &OrganizationLogoResource{}
	_ resource.ResourceWithConfigure        = &OrganizationLogoResource{}
	_ resource.ResourceWithImportState      = &OrganizationLogoResource{}
	_ resource.ResourceWithModifyPlan       = &OrganizationLogoResource{}
	_ resource.ResourceWithConfigValidators = &OrganizationLogoResource{}
)

// NewOrganizationLogoResource returns the ghost_organization_logo resource.
func NewOrganizationLogoResource() resource.Resource {
	return &OrganizationLogoResource{}
}

// OrganizationLogoResource manages an organization's logo.
type OrganizationLogoResource struct {
	client *client.Client
}

// organizationLogoResourceModel maps the ghost_organization_logo schema.
type organizationLogoResourceModel struct {
	ID            types.String `tfsdk:"id"`
	Organization  types.String `tfsdk:"organization"`
	File          types.String `tfsdk:"file"`
	ContentBase64 types.String `tfsdk:"content_base64"`
	ContentType   types.String `tfsdk:"content_type"`
	ContentSHA256 types.String `tfsdk:"content_sha256"`
	Size          types.Int64  `tfsdk:"size"`
	URL           types.String `tfsdk:"url"`
}

func (m organizationLogoResourceModel) source() contentSource {
	return contentSource{File: m.File, Content: types.StringNull(), ContentBase64: m.ContentBase64, noun: "logo"}
}

func (r *OrganizationLogoResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_organization_logo"
}

func (r *OrganizationLogoResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Sets an organization's logo, the image shown on its profile and next to its repositories. " +
			"Only the organization's admins can set it.\n\n" +
			"The image is a PNG or JPEG of up to 2 MiB, from exactly one of `file` or `content_base64`. The provider hashes it at plan time " +
			"(`content_sha256`), so changing the bytes uploads the new image in place. Creating the resource replaces any " +
			"logo the organization already has, and destroying it removes the logo. A logo changed or removed outside " +
			"Terraform shows up as drift.\n\n" +
			"`ghost_organization_settings` doesn't touch the logo, so the two can manage the same organization; the " +
			"`ghost_organization` data source reads it back as `logo`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The organization's slug, in lower case.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"organization": schema.StringAttribute{
				MarkdownDescription: "The organization's slug, matched case-insensitively and kept as configured. Changing it to another organization forces a new resource (removing the old organization's logo); changing only its case doesn't.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					stringvalidator.RegexMatches(usernamePattern, `must not contain ":" or "/"`),
				},
			},
			"file": schema.StringAttribute{
				MarkdownDescription: "The path of a local PNG or JPEG file. It is read at plan time to work out `content_sha256`, so a changed file uploads the new image. A file that doesn't exist yet at plan time (one created during the apply) is read at apply time. Conflicts with `content_base64`.",
				Optional:            true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"content_base64": schema.StringAttribute{
				MarkdownDescription: "The PNG or JPEG image, base64-encoded, e.g. `filebase64(\"logo.png\")`. It is stored in state. Conflicts with `file`.",
				Optional:            true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					base64Validator{},
				},
			},
			"content_type": schema.StringAttribute{
				MarkdownDescription: "The image's type, `image/png` or `image/jpeg`, detected from its first bytes.",
				Computed:            true,
			},
			"content_sha256": schema.StringAttribute{
				MarkdownDescription: "The hex SHA-256 of the image. A change uploads the new image. After an import, or when the logo was changed outside Terraform, it is computed by downloading the logo.",
				Computed:            true,
			},
			"size": schema.Int64Attribute{
				MarkdownDescription: "The size of the image in bytes.",
				Computed:            true,
			},
			"url": schema.StringAttribute{
				MarkdownDescription: "The URL Ghost serves the logo from, the organization's `logo`. Each upload gets a new URL.",
				Computed:            true,
			},
		},
	}
}

func (r *OrganizationLogoResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.ExactlyOneOf(path.MatchRoot("file"), path.MatchRoot("content_base64")),
	}
}

func (r *OrganizationLogoResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ModifyPlan hashes the image and checks that it is a PNG or JPEG, uploads
// a changed image in place, keeps the URL when the image is unchanged, and
// replaces the resource when it moves to another organization.
func (r *OrganizationLogoResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}

	var plan organizationLogoResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state *organizationLogoResourceModel
	if !req.State.Raw.IsNull() {
		state = &organizationLogoResourceModel{}
		resp.Diagnostics.Append(req.State.Get(ctx, state)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	src := plan.source()
	info, known, diags := src.inspect()
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	sum := types.StringUnknown()
	switch {
	case known:
		contentType, err := checkLogo(info)
		if err != nil {
			resp.Diagnostics.AddAttributeError(src.attr(), "Unsupported logo image", err.Error())
			return
		}
		sum = types.StringValue(info.sha256)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("content_sha256"), sum)...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("size"), info.size)...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("content_type"), contentType)...)
	case state != nil && src.same(state.source()):
		// The file isn't there to hash (e.g. a build directory was
		// cleaned), but the configuration hasn't changed: keep the logo.
		sum = state.ContentSHA256
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("content_sha256"), state.ContentSHA256)...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("size"), state.Size)...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("content_type"), state.ContentType)...)
	default:
		for _, attr := range []string{"content_sha256", "content_type"} {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(attr), types.StringUnknown())...)
		}
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("size"), types.Int64Unknown())...)
	}

	if state == nil {
		return
	}

	if !plan.Organization.Equal(state.Organization) &&
		(plan.Organization.IsUnknown() || !strings.EqualFold(plan.Organization.ValueString(), state.Organization.ValueString())) {
		resp.RequiresReplace.Append(path.Root("organization"))
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("id"), types.StringUnknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("url"), types.StringUnknown())...)
		return
	}

	if sum.Equal(state.ContentSHA256) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("url"), state.URL)...)
	} else {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("url"), types.StringUnknown())...)
	}
}

func (r *OrganizationLogoResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan organizationLogoResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(r.upload(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = types.StringValue(strings.ToLower(plan.Organization.ValueString()))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// upload sends the configured image as the organization's logo and records
// it in m.
func (r *OrganizationLogoResource) upload(ctx context.Context, m *organizationLogoResourceModel) diag.Diagnostics {
	var diags diag.Diagnostics
	org := m.Organization.ValueString()

	content, d := m.source().open()
	diags.Append(d...)
	if diags.HasError() {
		return diags
	}
	defer func() { _ = content.Close() }()

	if !m.ContentSHA256.IsUnknown() && !m.ContentSHA256.IsNull() && m.ContentSHA256.ValueString() != content.sha256 {
		diags.AddAttributeError(path.Root("file"), "File changed since the plan",
			fmt.Sprintf("%s has changed since Terraform planned this apply (SHA-256 %s, planned %s). Run terraform apply again.",
				m.File.ValueString(), content.sha256, m.ContentSHA256.ValueString()))
		return diags
	}
	if content.size == 0 {
		diags.Append(m.source().emptyError())
		return diags
	}
	contentType, err := checkLogo(content.contentInfo)
	if err != nil {
		diags.AddAttributeError(content.attr, "Unsupported logo image", err.Error())
		return diags
	}

	u, err := r.client.SetOrganizationLogo(ctx, org, contentType, content.reader, content.size)
	if err != nil {
		diags.AddError("Error setting organization logo", organizationLogoError(org, err))
		return diags
	}

	m.ContentType = types.StringValue(contentType)
	m.ContentSHA256 = types.StringValue(content.sha256)
	m.Size = types.Int64Value(content.size)
	m.URL = types.StringValue(u)

	return diags
}

func (r *OrganizationLogoResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state organizationLogoResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org, err := r.client.GetOrganization(ctx, state.Organization.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading organization", err.Error())
		return
	}
	if org.Logo == nil || *org.Logo == "" {
		// Removed outside Terraform.
		resp.State.RemoveResource(ctx)
		return
	}

	if *org.Logo != state.URL.ValueString() || state.ContentSHA256.IsNull() {
		// Imported, or replaced outside Terraform: hash what Ghost serves.
		resp.Diagnostics.Append(state.fromDownload(ctx, r.client, *org.Logo)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	state.ID = types.StringValue(strings.ToLower(org.Slug))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *OrganizationLogoResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state organizationLogoResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if plan.ContentSHA256.IsUnknown() || !plan.ContentSHA256.Equal(state.ContentSHA256) {
		resp.Diagnostics.Append(r.upload(ctx, &plan)...)
		if resp.Diagnostics.HasError() {
			return
		}
	} else {
		// Only where the image comes from, or the slug's case, changed.
		plan.ContentType = state.ContentType
		plan.Size = state.Size
		plan.URL = state.URL
	}
	plan.ID = state.ID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *OrganizationLogoResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state organizationLogoResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	org := state.Organization.ValueString()
	err := r.client.RemoveOrganizationLogo(ctx, org)
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error removing organization logo", organizationLogoError(org, err))
	}
}

// ImportState takes the organization's slug.
func (r *OrganizationLogoResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	org := req.ID
	if org == "" || !usernamePattern.MatchString(org) {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Expected an organization slug, got %q.", req.ID))
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), strings.ToLower(org))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("organization"), org)...)
}

// fromDownload downloads the logo Ghost serves at logoURL and records its
// hash, size and type in m.
func (m *organizationLogoResourceModel) fromDownload(ctx context.Context, c *client.Client, logoURL string) diag.Diagnostics {
	var diags diag.Diagnostics

	h := sha256.New()
	head := &limitedBuffer{limit: contentHeadSize}
	n := &countingWriter{}
	if err := c.DownloadLogo(ctx, logoURL, io.MultiWriter(h, head, n)); err != nil {
		diags.AddError("Error downloading organization logo", err.Error())
		return diags
	}

	m.ContentSHA256 = types.StringValue(hex.EncodeToString(h.Sum(nil)))
	m.Size = types.Int64Value(n.n)
	m.URL = types.StringValue(logoURL)
	if contentType, err := logoContentType(head.Bytes()); err == nil {
		m.ContentType = types.StringValue(contentType)
	} else {
		m.ContentType = types.StringNull()
	}

	return diags
}

// countingWriter counts the bytes written to it.
type countingWriter struct{ n int64 }

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))

	return len(p), nil
}

// maxLogoBytes is the largest logo Ghost accepts: 2 MiB. A larger one is a
// 413 with no explanation.
const maxLogoBytes = 2 << 20

var (
	pngSignature  = []byte("\x89PNG\r\n\x1a\n")
	jpegSignature = []byte("\xff\xd8\xff")
)

// checkLogo checks that Ghost will take info's image as a logo, and returns
// its media type.
func checkLogo(info contentInfo) (string, error) {
	contentType, err := logoContentType(info.head)
	if err != nil {
		return "", err
	}
	if info.size > maxLogoBytes {
		return "", fmt.Errorf("the image is %d bytes, but Ghost accepts logos of up to 2 MiB (%d bytes)", info.size, maxLogoBytes)
	}

	return contentType, nil
}

// logoContentType returns the media type of an image from its first bytes:
// image/png or image/jpeg, the only types Ghost accepts for a logo.
func logoContentType(head []byte) (string, error) {
	switch {
	case bytes.HasPrefix(head, pngSignature):
		return "image/png", nil
	case bytes.HasPrefix(head, jpegSignature):
		return "image/jpeg", nil
	}

	kind := http.DetectContentType(head)
	if i := strings.IndexByte(kind, ';'); i >= 0 {
		kind = kind[:i]
	}

	return "", fmt.Errorf("the image must be a PNG or JPEG, the only logo formats Ghost accepts, but it looks like %s", kind)
}

// organizationLogoError explains a failed logo request.
func organizationLogoError(org string, err error) string {
	switch {
	case client.IsNotFound(err):
		return fmt.Sprintf("Ghost has no organization %q that you're an admin of (%s). Check the slug: only an organization's admins can change its logo, and a user's login isn't an organization.", org, err)
	case client.IsForbidden(err):
		return fmt.Sprintf("The token can't change the logo of %q: only the organization's admins can. (%s)", org, err)
	case client.HasStatus(err, http.StatusRequestEntityTooLarge):
		return fmt.Sprintf("Ghost refused the image as too large: logos can be up to 2 MiB. (%s)", err)
	case client.HasStatus(err, http.StatusUnsupportedMediaType), client.HasStatus(err, http.StatusBadRequest):
		return fmt.Sprintf("Ghost couldn't use the image as a logo: it must be a valid PNG or JPEG. (%s)", err)
	}

	return err.Error()
}
