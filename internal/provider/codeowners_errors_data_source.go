package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

var (
	_ datasource.DataSource              = &CodeownersErrorsDataSource{}
	_ datasource.DataSourceWithConfigure = &CodeownersErrorsDataSource{}
)

// NewCodeownersErrorsDataSource returns the ghost_codeowners_errors data
// source.
func NewCodeownersErrorsDataSource() datasource.DataSource {
	return &CodeownersErrorsDataSource{}
}

// CodeownersErrorsDataSource checks a repository's CODEOWNERS file.
type CodeownersErrorsDataSource struct {
	client *client.Client
}

// codeownersErrorsDataSourceModel maps the ghost_codeowners_errors data
// source schema.
type codeownersErrorsDataSourceModel struct {
	ID           types.String           `tfsdk:"id"`
	Repository   types.String           `tfsdk:"repository"`
	Ref          types.String           `tfsdk:"ref"`
	RepositoryID types.Int64            `tfsdk:"repository_id"`
	Path         types.String           `tfsdk:"path"`
	Errors       []codeownersErrorModel `tfsdk:"errors"`
}

// codeownersErrorModel is one element of `errors`.
type codeownersErrorModel struct {
	Line       types.Int64  `tfsdk:"line"`
	Column     types.Int64  `tfsdk:"column"`
	Kind       types.String `tfsdk:"kind"`
	Source     types.String `tfsdk:"source"`
	Suggestion types.String `tfsdk:"suggestion"`
	Message    types.String `tfsdk:"message"`
	Path       types.String `tfsdk:"path"`
}

func (d *CodeownersErrorsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_codeowners_errors"
}

func (d *CodeownersErrorsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	computedString := func(description string) schema.StringAttribute {
		return schema.StringAttribute{MarkdownDescription: description, Computed: true}
	}
	computedInt := func(description string) schema.Int64Attribute {
		return schema.Int64Attribute{MarkdownDescription: description, Computed: true}
	}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Checks the CODEOWNERS file of a Ghost repository at a branch, tag or commit, and lists every error in it: " +
			"bad syntax, and owners that don't exist or lack write access to the repository. Ghost ignores the lines with errors " +
			"when it assigns code owners, so a `check` block or a `postcondition` on `length(errors) == 0` catches a CODEOWNERS file " +
			"that names a team before `ghost_team_repository` grants it write access.\n\n" +
			"Ghost uses the first file it finds of `.github/CODEOWNERS`, `CODEOWNERS` and `docs/CODEOWNERS`, and checks only that one. " +
			"The name is case-sensitive.",
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
			"ref": schema.StringAttribute{
				MarkdownDescription: "The branch, tag or commit SHA (full or short) to read the CODEOWNERS file from. Defaults to the default branch. " +
					"Names are case-sensitive, and a full ref is accepted only for branches (`refs/heads/…`), so name a tag without `refs/tags/`.",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"repository_id": schema.Int64Attribute{
				MarkdownDescription: "The numeric ID of `repository`.",
				Computed:            true,
			},
			"path": schema.StringAttribute{
				MarkdownDescription: "The CODEOWNERS file in effect: `.github/CODEOWNERS`, `CODEOWNERS` or `docs/CODEOWNERS`. " +
					"Null when `ref` has none (including an empty repository) or the file is over 3 MB.",
				Computed: true,
			},
			"errors": schema.ListNestedAttribute{
				MarkdownDescription: "The errors in the file, in line order. Empty when the file is valid or there is no file.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"line":   computedInt("The 1-based line number."),
						"column": computedInt("The 1-based column where the error starts, counted in characters: `1` for a bad pattern, the owner's column for a bad owner."),
						"kind": computedString("What is wrong: `Invalid pattern`, `Invalid owner` (not `@user`, `@org/team` or an email address), " +
							"`Unknown owner`, `Owner without write access`, `Unknown team` or `Team without write access`. " +
							"A pending collaborator invitation doesn't give write access yet, and a team is unknown on a repository owned by a user."),
						"source":     computedString("The whole line as written."),
						"suggestion": computedString("A suggested fix, when Ghost has one. Usually null."),
						"message":    computedString("A readable description, e.g. `Unknown owner on line 3: make sure @alice exists and has write access to the repository`."),
						"path":       computedString("The file the error is in, the same as the top-level `path`."),
					},
				},
			},
		},
	}
}

func (d *CodeownersErrorsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *CodeownersErrorsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data codeownersErrorsDataSourceModel
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

	// The native route, not v3 /codeowners/errors: v3 answers a bare 404
	// both when the ref has no CODEOWNERS file and when the ref doesn't
	// exist, and it doesn't say which file it checked.
	ref := data.Ref.ValueString()
	codeowners, err := d.client.GetCodeowners(ctx, owner, repo, ref)
	if client.IsNotFound(err) && ref != "" {
		resp.Diagnostics.AddAttributeError(path.Root("ref"), "Ref not found", refNotFoundDetail(repository.FullName, ref, err))
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error checking the CODEOWNERS file", err.Error())
		return
	}

	data.ID = types.StringValue(repository.FullName)
	data.RepositoryID = types.Int64Value(repository.ID)
	data.Path = types.StringPointerValue(codeowners.Path)
	data.Errors = make([]codeownersErrorModel, len(codeowners.Errors))
	for i, e := range codeowners.Errors {
		data.Errors[i] = codeownersErrorModel{
			Line:       types.Int64Value(e.Line),
			Column:     types.Int64Value(e.Column),
			Kind:       types.StringValue(e.Kind),
			Source:     types.StringValue(e.Source),
			Suggestion: types.StringPointerValue(e.Suggestion),
			Message:    types.StringValue(e.Message),
			Path:       types.StringValue(e.Path),
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// refNotFoundDetail explains a 404 for ref in the repository fullName, with
// a hint for the ref forms Ghost doesn't accept.
func refNotFoundDetail(fullName, ref string, err error) string {
	detail := fmt.Sprintf("%s has no branch, tag or commit named %q, or it is an empty repository (%s).", fullName, ref, err)
	switch {
	case strings.HasPrefix(ref, "refs/") && !strings.HasPrefix(ref, "refs/heads/"):
		detail += " Ghost accepts a full ref only for branches (refs/heads/…): name a tag without refs/tags/."
	case strings.ToLower(ref) != ref:
		detail += " Branch and tag names are case-sensitive."
	}

	return detail
}
