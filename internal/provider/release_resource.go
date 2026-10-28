package provider

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

var (
	_ resource.Resource                = &ReleaseResource{}
	_ resource.ResourceWithConfigure   = &ReleaseResource{}
	_ resource.ResourceWithImportState = &ReleaseResource{}
	_ resource.ResourceWithModifyPlan  = &ReleaseResource{}
)

// NewReleaseResource returns the ghost_release resource.
func NewReleaseResource() resource.Resource {
	return &ReleaseResource{}
}

// ReleaseResource manages a release in a Ghost repository.
type ReleaseResource struct {
	client *client.Client
}

// releaseResourceModel maps the ghost_release schema.
type releaseResourceModel struct {
	ID              types.String `tfsdk:"id"`
	Repository      types.String `tfsdk:"repository"`
	RepositoryID    types.Int64  `tfsdk:"repository_id"`
	TagName         types.String `tfsdk:"tag_name"`
	TargetCommitish types.String `tfsdk:"target_commitish"`
	Name            types.String `tfsdk:"name"`
	Body            types.String `tfsdk:"body"`
	Draft           types.Bool   `tfsdk:"draft"`
	Prerelease      types.Bool   `tfsdk:"prerelease"`
	ReleaseID       types.Int64  `tfsdk:"release_id"`
	NodeID          types.String `tfsdk:"node_id"`
	CommitSHA       types.String `tfsdk:"commit_sha"`
	URL             types.String `tfsdk:"url"`
	HTMLURL         types.String `tfsdk:"html_url"`
	UploadURL       types.String `tfsdk:"upload_url"`
	TarballURL      types.String `tfsdk:"tarball_url"`
	ZipballURL      types.String `tfsdk:"zipball_url"`
	CreatedAt       types.String `tfsdk:"created_at"`
	PublishedAt     types.String `tfsdk:"published_at"`
}

func (r *ReleaseResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_release"
}

func (r *ReleaseResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	stableString := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a release in a Ghost repository, and creates its tag when the tag doesn't exist yet.\n\n" +
			"Ghost creates the tag as soon as the release is created, **even for a draft**, so a draft's tag (though not the " +
			"draft itself) is visible to anyone who can read the repository. Deleting the release keeps the tag. Ghost has no " +
			"API for deleting tags: delete one with `git push origin --delete <tag>`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The repository's full name and the numeric release ID, `owner/slug:release_id`.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
			"repository": schema.StringAttribute{
				MarkdownDescription: "The repository's full name, `owner/slug`, e.g. `ghost_repository.example.full_name`. Renaming the repository updates this in place; pointing it at a different repository forces a new release. Replacing the repository deletes its releases, so add `lifecycle { replace_triggered_by = [ghost_repository.example.repo_id] }` to recreate the release in the same apply.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(fullNamePattern, `must be "owner/slug", e.g. "alice/my-repo"`),
				},
			},
			"repository_id": schema.Int64Attribute{
				MarkdownDescription: "The numeric ID of `repository`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"tag_name": schema.StringAttribute{
				MarkdownDescription: "The tag, without the `refs/tags/` prefix, up to 250 characters. Tag names are case-sensitive. An existing tag is used as it is; otherwise Ghost creates it at `target_commitish`. Ghost can't move a release to another tag, so changing this forces a new release (the old tag is kept).",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					stringvalidator.UTF8LengthBetween(1, 250),
					notPrefixValidator{prefix: "refs/", hint: `give the tag name only, e.g. "v1.0.0" rather than "refs/tags/v1.0.0"`},
				},
			},
			"target_commitish": schema.StringAttribute{
				MarkdownDescription: "The branch, tag or commit SHA to create the tag at when it doesn't exist yet. Defaults to the default branch. " +
					"It is only used when the release is created: Ghost ignores it for an existing tag, and doesn't store it, so it is kept as configured, " +
					"and changing it later only updates state (the tag doesn't move). It is null after import. `commit_sha` is the commit the tag points at.",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "The release title, up to 200 characters. Without one, Ghost shows the tag name.",
				Optional:            true,
				Validators: []validator.String{
					stringvalidator.UTF8LengthAtMost(200),
				},
			},
			"body": schema.StringAttribute{
				MarkdownDescription: "The release notes, in Markdown, up to 100,000 characters. Ghost can't generate release notes.",
				Optional:            true,
				Validators: []validator.String{
					stringvalidator.UTF8LengthAtMost(100000),
				},
			},
			"draft": schema.BoolAttribute{
				MarkdownDescription: "Whether the release is an unpublished draft, visible only to people who can write to the repository. Defaults to `false`. Publishing a draft sets `published_at`; turning a release back into a draft keeps it.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
			},
			"prerelease": schema.BoolAttribute{
				MarkdownDescription: "Whether the release is marked as a prerelease. Defaults to `false`. The latest release is the newest published release that isn't a prerelease.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
			},
			"release_id": schema.Int64Attribute{
				MarkdownDescription: "The numeric release ID.",
				Computed:            true,
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"node_id": schema.StringAttribute{
				MarkdownDescription: "The GraphQL node ID.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
			"commit_sha": schema.StringAttribute{
				MarkdownDescription: "The commit the tag points at. It follows the tag if it's moved, and is null once the tag has been deleted.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
			"url": schema.StringAttribute{
				MarkdownDescription: "The release's API URL.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
			"html_url": schema.StringAttribute{
				MarkdownDescription: "The release's page in the web UI.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
			"upload_url": schema.StringAttribute{
				MarkdownDescription: "The URI template for uploading assets, taking `name` and `label`.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
			"tarball_url": schema.StringAttribute{
				MarkdownDescription: "The API URL of the tag's source tarball.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
			"zipball_url": schema.StringAttribute{
				MarkdownDescription: "The API URL of the tag's source zip archive.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "When the release was created.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
			"published_at": schema.StringAttribute{
				MarkdownDescription: "When the release was first published; null for a draft that has never been published.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
		},
	}
}

func (r *ReleaseResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ModifyPlan resolves a repository change (the same repository renamed is in
// place, another repository forces a new release), warns that a changed
// target_commitish doesn't move the tag, and marks the computed attributes
// an update can change as unknown.
func (r *ReleaseResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}

	var plan, state releaseResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
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

	if !plan.TagName.Equal(state.TagName) {
		// RequiresReplace handles it, and the replacement's attributes are
		// all unknown.
		return
	}

	if !state.TargetCommitish.IsNull() && !plan.TargetCommitish.IsUnknown() && !plan.TargetCommitish.Equal(state.TargetCommitish) {
		resp.Diagnostics.AddAttributeWarning(path.Root("target_commitish"), "Changing target_commitish doesn't move the tag",
			fmt.Sprintf("target_commitish is only used when Ghost creates the tag, and tag %q already exists. "+
				"The new value is only saved in state. To move the tag, push it with git, or delete it and replace "+
				"this release with terraform apply -replace=<address>.", state.TagName.ValueString()))
	}

	if !plan.Repository.Equal(state.Repository) {
		for _, attr := range []string{"id", "url", "html_url", "upload_url", "tarball_url", "zipball_url"} {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(attr), types.StringUnknown())...)
		}
	}
	if !plan.Draft.Equal(state.Draft) && state.PublishedAt.IsNull() {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("published_at"), types.StringUnknown())...)
	}
}

func (r *ReleaseResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan releaseResourceModel
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

	tag := plan.TagName.ValueString()
	release, err := r.client.CreateRelease(ctx, owner, repo, client.CreateReleaseRequest{
		TagName:         tag,
		TargetCommitish: plan.TargetCommitish.ValueString(),
		Name:            plan.Name.ValueString(),
		Body:            plan.Body.ValueString(),
		Draft:           plan.Draft.ValueBool(),
		Prerelease:      plan.Prerelease.ValueBool(),
	})
	switch {
	case client.IsFieldError(err, "tag_name", "already_exists"):
		detail := fmt.Sprintf("Tag %q in %s already has a release, and a tag can have only one.", tag, plan.Repository.ValueString())
		if existing, findErr := r.client.FindReleaseByTag(ctx, owner, repo, tag); findErr == nil && existing != nil {
			detail += fmt.Sprintf(" To manage it, import it with: terraform import <resource address> %q",
				releaseID(plan.Repository.ValueString(), existing.ID))
		}
		resp.Diagnostics.AddAttributeError(path.Root("tag_name"), "Release already exists", fmt.Sprintf("%s (%s)", detail, err))
		return
	case client.IsFieldError(err, "tag_name", "invalid"):
		resp.Diagnostics.AddAttributeError(path.Root("tag_name"), "Invalid tag name",
			fmt.Sprintf("Ghost rejected the tag name %q: it must be a valid git tag name (no spaces, \"..\", \"~\", \"^\", \":\", "+
				"\"?\", \"*\", \"[\" or \"\\\", and no leading or trailing \"/\" or \".\"). (%s)", tag, err))
		return
	case client.IsFieldError(err, "target_commitish", "invalid"):
		target := "the default branch"
		if !plan.TargetCommitish.IsNull() {
			target = fmt.Sprintf("target_commitish %q", plan.TargetCommitish.ValueString())
		}
		resp.Diagnostics.AddAttributeError(path.Root("target_commitish"), "Can't create the tag",
			fmt.Sprintf("Tag %q doesn't exist, and Ghost couldn't create it at %s: it must be an existing branch, tag or "+
				"commit SHA in %s, and the repository must not be empty (push a commit or create it with auto_init = true). (%s)",
				tag, target, plan.Repository.ValueString(), err))
		return
	case err != nil:
		resp.Diagnostics.AddError("Error creating release", err.Error())
		return
	}

	plan.fromRemote(release)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ReleaseResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state releaseResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	owner, repo, err := client.SplitFullName(state.Repository.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("repository"), "Invalid repository", err.Error())
		return
	}

	release, err := r.client.GetRelease(ctx, owner, repo, state.ReleaseID.ValueInt64())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading release", err.Error())
		return
	}

	// An imported release has no repository_id yet.
	if state.RepositoryID.IsNull() {
		repository, err := r.client.GetRepository(ctx, owner, repo)
		if err != nil {
			resp.Diagnostics.AddError("Error reading repository", err.Error())
			return
		}
		state.RepositoryID = types.Int64Value(repository.ID)
	}

	state.fromRemote(release)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ReleaseResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state releaseResourceModel
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

	if !plan.Repository.Equal(state.Repository) {
		repository, err := r.client.GetRepository(ctx, owner, repo)
		if err != nil {
			resp.Diagnostics.AddError("Error reading repository", err.Error())
			return
		}
		if !sameRepository(state.RepositoryID, repository) {
			resp.Diagnostics.AddAttributeError(path.Root("repository"), "Release can't be moved",
				fmt.Sprintf("%s is a different repository from the one this release was created in. "+
					"Releases can't be moved; replace it with terraform apply -replace=<address>.", plan.Repository.ValueString()))
			return
		}
		plan.RepositoryID = types.Int64Value(repository.ID)
	}

	release, err := r.client.UpdateRelease(ctx, owner, repo, state.ReleaseID.ValueInt64(), client.UpdateReleaseRequest{
		Name:       nonEmptyStringPointer(plan.Name),
		Body:       nonEmptyStringPointer(plan.Body),
		Draft:      plan.Draft.ValueBool(),
		Prerelease: plan.Prerelease.ValueBool(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Error updating release", err.Error())
		return
	}

	plan.ReleaseID = state.ReleaseID
	plan.fromRemote(release)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *ReleaseResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state releaseResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	owner, repo, err := client.SplitFullName(state.Repository.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("repository"), "Invalid repository", err.Error())
		return
	}

	err = r.client.DeleteRelease(ctx, owner, repo, state.ReleaseID.ValueInt64())
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error deleting release", err.Error())
	}
}

func (r *ReleaseResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	repository, id, err := parseReleaseID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("repository"), repository)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("release_id"), id)...)
}

// fromRemote copies release into m. The repository and target_commitish are
// kept as configured, and a configured "" name or body is kept for Ghost's
// null.
func (m *releaseResourceModel) fromRemote(release *client.Release) {
	m.ID = types.StringValue(releaseID(m.Repository.ValueString(), release.ID))
	m.ReleaseID = types.Int64Value(release.ID)
	m.TagName = types.StringValue(release.TagName)
	m.Name = descriptionFromRemote(m.Name, release.Name)
	m.Body = descriptionFromRemote(m.Body, release.Body)
	m.Draft = types.BoolValue(release.Draft)
	m.Prerelease = types.BoolValue(release.Prerelease)
	m.NodeID = types.StringValue(release.NodeID)
	m.CommitSHA = types.StringNull()
	if sha := release.CommitSHA(); sha != "" {
		m.CommitSHA = types.StringValue(sha)
	}
	m.URL = types.StringValue(release.URL)
	m.HTMLURL = types.StringValue(release.HTMLURL)
	m.UploadURL = types.StringValue(release.UploadURL)
	m.TarballURL = types.StringValue(release.TarballURL)
	m.ZipballURL = types.StringValue(release.ZipballURL)
	m.CreatedAt = types.StringValue(release.CreatedAt)
	m.PublishedAt = types.StringPointerValue(release.PublishedAt)
}

// nonEmptyStringPointer returns nil for a null or empty string, which Ghost
// stores as null.
func nonEmptyStringPointer(s types.String) *string {
	if s.IsNull() || s.IsUnknown() || s.ValueString() == "" {
		return nil
	}

	return s.ValueStringPointer()
}

// releaseID builds the "owner/slug:release_id" ID of a release.
func releaseID(repository string, id int64) string {
	return repository + ":" + strconv.FormatInt(id, 10)
}

// parseReleaseID parses an "owner/slug:release_id" ID.
func parseReleaseID(id string) (repository string, releaseID int64, err error) {
	repository, rawID, ok := strings.Cut(id, ":")
	n, parseErr := strconv.ParseInt(rawID, 10, 64)
	if _, _, splitErr := client.SplitFullName(repository); !ok || splitErr != nil || parseErr != nil || n <= 0 {
		return "", 0, fmt.Errorf("import ID must be \"owner/repo:release_id\" with a numeric release ID, e.g. \"alice/my-repo:42\", got %q", id)
	}

	return repository, n, nil
}

// notPrefixValidator rejects strings that start with prefix.
type notPrefixValidator struct {
	prefix string
	hint   string
}

func (v notPrefixValidator) Description(_ context.Context) string {
	return fmt.Sprintf("must not start with %q: %s", v.prefix, v.hint)
}

func (v notPrefixValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v notPrefixValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if strings.HasPrefix(req.ConfigValue.ValueString(), v.prefix) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid attribute value",
			fmt.Sprintf("%s %s, got %q", req.Path, v.Description(ctx), req.ConfigValue.ValueString()))
	}
}
