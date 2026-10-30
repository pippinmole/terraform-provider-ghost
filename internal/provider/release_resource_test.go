package provider

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

const testAccSHA = "d90fe1d27c8299025bf3425daeb03a16ee65f7a4"

func TestParseReleaseID(t *testing.T) {
	tests := []struct {
		id         string
		repository string
		releaseID  int64
	}{
		{"alice/repo:12", "alice/repo", 12},
		{"alice/repo:9007199254740993", "alice/repo", 9007199254740993},
	}
	for _, tt := range tests {
		repository, id, err := parseReleaseID(tt.id)
		if err != nil || repository != tt.repository || id != tt.releaseID {
			t.Errorf("parseReleaseID(%q) = %q, %d, %v", tt.id, repository, id, err)
		}
	}

	for _, id := range []string{"", "alice/repo", "alice/repo:", "alice/repo:abc", "alice/repo:-1", "alice/repo:0",
		"alice/repo:1.5", "alice:1", "a/b/c:1", ":1", "alice/repo:1:2", "alice/repo:v1.0.0"} {
		_, _, err := parseReleaseID(id)
		if err == nil {
			t.Errorf("parseReleaseID(%q): expected error", id)
		} else if !strings.Contains(err.Error(), "owner/repo:release_id") {
			t.Errorf("parseReleaseID(%q): error doesn't name the expected form: %v", id, err)
		}
	}
}

func TestReleaseFromRemote(t *testing.T) {
	str := func(s string) *string { return &s }

	tests := []struct {
		name   string
		prior  types.String
		remote *string
		want   types.String
	}{
		{"configured empty kept", types.StringValue(""), nil, types.StringValue("")},
		{"null", types.StringNull(), nil, types.StringNull()},
		{"import", types.StringUnknown(), nil, types.StringNull()},
		{"cleared outside terraform", types.StringValue("x"), nil, types.StringNull()},
		{"set outside terraform", types.StringValue(""), str("y"), types.StringValue("y")},
		{"same", types.StringValue("x"), str("x"), types.StringValue("x")},
		{"whitespace kept", types.StringValue(" x \n"), str(" x \n"), types.StringValue(" x \n")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, attr := range []string{"name", "body"} {
				m := releaseResourceModel{Repository: types.StringValue("alice/repo"), Name: tt.prior, Body: tt.prior,
					TargetCommitish: types.StringValue("feat/x")}
				release := &client.Release{ID: 7, TagName: "v1", TargetCommitish: testAccSHA}
				if attr == "name" {
					release.Name = tt.remote
				} else {
					release.Body = tt.remote
				}
				m.fromRemote(release)

				got := m.Name
				if attr == "body" {
					got = m.Body
				}
				if !got.Equal(tt.want) {
					t.Errorf("%s: got %s, want %s", attr, got, tt.want)
				}
				if m.ID.ValueString() != "alice/repo:7" || m.ReleaseID.ValueInt64() != 7 {
					t.Errorf("id = %s, release_id = %s", m.ID, m.ReleaseID)
				}
				// target_commitish is kept as configured; Ghost returns the SHA.
				if m.TargetCommitish.ValueString() != "feat/x" || m.CommitSHA.ValueString() != testAccSHA {
					t.Errorf("target_commitish = %s, commit_sha = %s", m.TargetCommitish, m.CommitSHA)
				}
			}
		})
	}
}

func TestReleaseCommitSHAFromRemote(t *testing.T) {
	for commitish, want := range map[string]types.String{
		testAccSHA:  types.StringValue(testAccSHA),
		"main":      types.StringNull(), // the tag was deleted
		"release/1": types.StringNull(),
	} {
		m := releaseResourceModel{Repository: types.StringValue("alice/repo"), TargetCommitish: types.StringNull()}
		m.fromRemote(&client.Release{ID: 1, TargetCommitish: commitish})
		if !m.CommitSHA.Equal(want) {
			t.Errorf("%q: commit_sha = %s, want %s", commitish, m.CommitSHA, want)
		}
		if !m.TargetCommitish.IsNull() {
			t.Errorf("%q: imported target_commitish should stay null, got %s", commitish, m.TargetCommitish)
		}
	}
}

func TestNonEmptyStringPointer(t *testing.T) {
	if nonEmptyStringPointer(types.StringNull()) != nil || nonEmptyStringPointer(types.StringValue("")) != nil ||
		nonEmptyStringPointer(types.StringUnknown()) != nil {
		t.Error("null, unknown and empty should be nil")
	}
	if p := nonEmptyStringPointer(types.StringValue(" x ")); p == nil || *p != " x " {
		t.Errorf("got %v", p)
	}
}

// TestReleaseTargetCommitishWarning runs ModifyPlan directly, because
// acceptance tests can't see warnings.
func TestReleaseTargetCommitishWarning(t *testing.T) {
	tests := []struct {
		name        string
		state, plan types.String
		wantWarning bool
	}{
		{"changed", types.StringValue("main"), types.StringValue("dev"), true},
		{"removed", types.StringValue("main"), types.StringNull(), true},
		{"unchanged", types.StringValue("main"), types.StringValue("main"), false},
		{"set after import", types.StringNull(), types.StringValue("main"), false},
		{"unknown", types.StringValue("main"), types.StringUnknown(), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diags := modifyReleasePlan(t, tt.state, tt.plan)
			if diags.HasError() {
				t.Fatalf("unexpected error: %v", diags)
			}
			warnings := diags.Warnings()
			if got := len(warnings) == 1; got != tt.wantWarning || len(warnings) > 1 {
				t.Fatalf("got %d warnings, want warning %v: %v", len(warnings), tt.wantWarning, warnings)
			}
			if tt.wantWarning && !strings.Contains(warnings[0].Summary(), "doesn't move the tag") {
				t.Errorf("unexpected warning: %s", warnings[0].Summary())
			}
		})
	}

	t.Run("create and destroy", func(t *testing.T) {
		r := &ReleaseResource{}
		var schemaResp fwresource.SchemaResponse
		r.Schema(context.Background(), fwresource.SchemaRequest{}, &schemaResp)
		null := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(context.Background()), nil)
		for _, req := range []fwresource.ModifyPlanRequest{
			{State: tfsdk.State{Schema: schemaResp.Schema, Raw: null}, Plan: releasePlan(t, schemaResp, types.StringValue("dev"))},
			{State: tfsdk.State(releasePlan(t, schemaResp, types.StringValue("main"))), Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: null}},
		} {
			resp := &fwresource.ModifyPlanResponse{Plan: req.Plan}
			r.ModifyPlan(context.Background(), req, resp)
			if len(resp.Diagnostics) != 0 {
				t.Errorf("unexpected diagnostics: %v", resp.Diagnostics)
			}
		}
	})
}

// modifyReleasePlan runs ModifyPlan on an update of a release that changes
// only target_commitish.
func modifyReleasePlan(t *testing.T, state, plan types.String) diag.Diagnostics {
	t.Helper()
	r := &ReleaseResource{}
	var schemaResp fwresource.SchemaResponse
	r.Schema(context.Background(), fwresource.SchemaRequest{}, &schemaResp)

	req := fwresource.ModifyPlanRequest{
		State: tfsdk.State(releasePlan(t, schemaResp, state)),
		Plan:  releasePlan(t, schemaResp, plan),
	}
	resp := &fwresource.ModifyPlanResponse{Plan: req.Plan}
	r.ModifyPlan(context.Background(), req, resp)

	return resp.Diagnostics
}

// releasePlan returns a plan for a published release in alice/repo with the
// given target_commitish.
func releasePlan(t *testing.T, schemaResp fwresource.SchemaResponse, target types.String) tfsdk.Plan {
	t.Helper()
	ctx := context.Background()
	plan := tfsdk.Plan{Schema: schemaResp.Schema, Raw: tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil)}
	m := releaseResourceModel{
		ID: types.StringValue("alice/repo:1"), Repository: types.StringValue("alice/repo"), RepositoryID: types.Int64Value(2),
		TagName: types.StringValue("v1"), TargetCommitish: target, Name: types.StringNull(), Body: types.StringNull(),
		Draft: types.BoolValue(false), Prerelease: types.BoolValue(false), ReleaseID: types.Int64Value(1),
		NodeID: types.StringValue("n"), CommitSHA: types.StringValue(testAccSHA), URL: types.StringValue("u"),
		HTMLURL: types.StringValue("h"), UploadURL: types.StringValue("u"), TarballURL: types.StringValue("t"),
		ZipballURL: types.StringValue("z"), CreatedAt: types.StringValue("c"), PublishedAt: types.StringValue("p"),
	}
	if diags := plan.Set(ctx, &m); diags.HasError() {
		t.Fatal(diags)
	}

	return plan
}

// TestReleaseConfigValidation runs offline: the attribute validators reject
// the config before the provider is ever configured.
func TestReleaseConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config string
		expect string
	}{
		{"missing tag", `tag_name = null`, `(?i)required`},
		{"empty tag", `tag_name = ""`, `(?i)length`},
		{"long tag", `tag_name = "` + strings.Repeat("a", 251) + `"`, `(?i)length`},
		{"refs tags prefix", `tag_name = "refs/tags/v1"`, `refs/`},
		{"refs heads prefix", `tag_name = "refs/heads/x"`, `refs/`},
		{"long name", `name = "` + strings.Repeat("n", 201) + `"`, `(?i)length`},
		{"long body", `body = "` + strings.Repeat("b", 100001) + `"`, `(?i)length`},
		{"empty target", `target_commitish = ""`, `(?i)length`},
		{"bad repository", `repository = "nope"`, `must be "owner/slug"`},
		{"nested repository", `repository = "a/b/c"`, `must be "owner/slug"`},
		{"empty repository", `repository = ""`, `must be "owner/slug"`},
		{"missing repository", `repository = null`, `(?i)required`},
		{"draft type", `draft = "yes"`, `(?i)bool`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      testAccReleaseUnitConfig(tt.config),
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tt.expect),
				}},
			})
		})
	}
}

// TestReleaseConfigValidationLimits checks that values at the limits pass
// validation: planning then fails only because the provider has no token.
func TestReleaseConfigValidationLimits(t *testing.T) {
	t.Setenv(envToken, "")
	config := testAccReleaseUnitConfig(`tag_name = "` + strings.Repeat("a", 250) + `"
  name = "` + strings.Repeat("n", 200) + `"
  body = "` + strings.Repeat("b", 100000) + `"`)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      config,
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`(?i)token`),
		}},
	})
}

// testAccReleaseUnitConfig returns a ghost_release with valid arguments, with
// override replacing one of them or adding more.
func testAccReleaseUnitConfig(override string) string {
	attrs := map[string]string{"repository": `"alice/repo"`, "tag_name": `"v1"`}
	for _, line := range strings.Split(override, "\n") {
		key, _, _ := strings.Cut(strings.TrimSpace(line), " ")
		delete(attrs, key)
	}
	config := "resource \"ghost_release\" \"test\" {\n  " + override + "\n"
	for k, v := range attrs {
		config += "  " + k + " = " + v + "\n"
	}

	return config + "}\n"
}

func TestAccReleaseResource(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")
	const addr = "ghost_release.test"

	sameReleaseID := statecheck.CompareValue(compare.ValuesSame())
	samePublishedAt := statecheck.CompareValue(compare.ValuesSame())
	sameCommitSHA := statecheck.CompareValue(compare.ValuesSame())
	checks := func(published bool) []statecheck.StateCheck {
		c := []statecheck.StateCheck{
			sameReleaseID.AddStateValue(addr, tfjsonpath.New("release_id")),
			sameCommitSHA.AddStateValue(addr, tfjsonpath.New("commit_sha")),
		}
		if published {
			c = append(c, samePublishedAt.AddStateValue(addr, tfjsonpath.New("published_at")))
		}
		return c
	}
	update := resource.ConfigPlanChecks{
		PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
		PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
	}
	importStep := resource.TestStep{
		ResourceName:            addr,
		ImportState:             true,
		ImportStateIdFunc:       testAccReleaseImportID(addr),
		ImportStateVerify:       true,
		ImportStateVerifyIgnore: []string{"target_commitish"},
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckReleaseDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccReleaseConfig(repoName, `
  tag_name   = "v0.1.0"
  name       = "First"
  body       = "# Notes\n\n**bold**"
  draft      = true
  prerelease = false`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionCreate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: append(checks(false),
					statecheck.CompareValuePairs(addr, tfjsonpath.New("repository"), "ghost_repository.test", tfjsonpath.New("full_name"), compare.ValuesSame()),
					statecheck.CompareValuePairs(addr, tfjsonpath.New("repository_id"), "ghost_repository.test", tfjsonpath.New("repo_id"), compare.ValuesSame()),
				),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckReleaseExists(addr),
					resource.TestCheckResourceAttr(addr, "tag_name", "v0.1.0"),
					resource.TestCheckResourceAttr(addr, "name", "First"),
					resource.TestCheckResourceAttr(addr, "body", "# Notes\n\n**bold**"),
					resource.TestCheckResourceAttr(addr, "draft", "true"),
					resource.TestCheckResourceAttr(addr, "prerelease", "false"),
					resource.TestCheckNoResourceAttr(addr, "target_commitish"),
					resource.TestCheckNoResourceAttr(addr, "published_at"),
					resource.TestCheckResourceAttrWith(addr, "id", func(v string) error {
						if !regexp.MustCompile(`^[^/]+/` + regexp.QuoteMeta(repoName) + `:[0-9]+$`).MatchString(v) {
							return fmt.Errorf("unexpected id %q", v)
						}
						return nil
					}),
					resource.TestMatchResourceAttr(addr, "commit_sha", regexp.MustCompile(`^[0-9a-f]{40}$`)),
					resource.TestCheckResourceAttrSet(addr, "node_id"),
					resource.TestCheckResourceAttrSet(addr, "created_at"),
					resource.TestCheckResourceAttrSet(addr, "url"),
					resource.TestCheckResourceAttrSet(addr, "html_url"),
					resource.TestCheckResourceAttrSet(addr, "upload_url"),
					resource.TestCheckResourceAttrSet(addr, "tarball_url"),
					resource.TestCheckResourceAttrSet(addr, "zipball_url"),
					// Ghost creates the tag straight away, even for a draft.
					testAccCheckRemoteTag(t, repoName, "v0.1.0", addr),
				),
			},
			importStep,
			{
				Config: testAccReleaseConfig(repoName, `
  tag_name = "v0.1.0"
  name     = "First"
  body     = "# Notes\n\n**bold**"`),
				ConfigPlanChecks:  update,
				ConfigStateChecks: checks(true),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckReleaseExists(addr),
					resource.TestCheckResourceAttr(addr, "draft", "false"),
					resource.TestCheckResourceAttrSet(addr, "published_at"),
					resource.TestCheckResourceAttrWith(addr, "html_url", func(v string) error {
						if !strings.HasSuffix(v, "/"+repoName+"/releases/tag/v0.1.0") {
							return fmt.Errorf("unexpected html_url %q", v)
						}
						return nil
					}),
				),
			},
			{
				// Unpublishing keeps published_at.
				Config: testAccReleaseConfig(repoName, `
  tag_name = "v0.1.0"
  name     = "First"
  body     = "# Notes\n\n**bold**"
  draft    = true`),
				ConfigPlanChecks:  update,
				ConfigStateChecks: checks(true),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckReleaseExists(addr),
					resource.TestCheckResourceAttr(addr, "draft", "true"),
				),
			},
			{
				Config: testAccReleaseConfig(repoName, `
  tag_name   = "v0.1.0"
  name       = "Second"
  body       = "  changed\n"
  prerelease = true`),
				ConfigPlanChecks:  update,
				ConfigStateChecks: checks(true),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckReleaseExists(addr),
					resource.TestCheckResourceAttr(addr, "draft", "false"),
					resource.TestCheckResourceAttr(addr, "prerelease", "true"),
					resource.TestCheckResourceAttr(addr, "name", "Second"),
					resource.TestCheckResourceAttr(addr, "body", "  changed\n"),
				),
			},
			{
				// An empty name is kept in state for Ghost's null, and a
				// removed body is cleared.
				Config: testAccReleaseConfig(repoName, `
  tag_name = "v0.1.0"
  name     = ""`),
				ConfigPlanChecks:  update,
				ConfigStateChecks: checks(true),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckReleaseExists(addr),
					resource.TestCheckResourceAttr(addr, "name", ""),
					resource.TestCheckNoResourceAttr(addr, "body"),
					resource.TestCheckResourceAttr(addr, "prerelease", "false"),
				),
			},
			{
				// Setting target_commitish on an existing tag only updates
				// state: the tag doesn't move.
				Config: testAccReleaseConfig(repoName, `
  tag_name         = "v0.1.0"
  target_commitish = "main"`),
				ConfigPlanChecks:  update,
				ConfigStateChecks: checks(true),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckReleaseExists(addr),
					resource.TestCheckResourceAttr(addr, "target_commitish", "main"),
					resource.TestCheckNoResourceAttr(addr, "name"),
					testAccCheckRemoteTag(t, repoName, "v0.1.0", addr),
				),
			},
			importStep,
		},
	})
}

func TestAccReleaseResource_targets(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")
	var shaFeat, shaTag, shaMain string

	branch := `
resource "ghost_branch" "feat" {
  repository = ghost_repository.test.full_name
  branch     = "feat/x.y"
}
`
	releases := func(branchTarget string) string {
		return testAccRepositoryConfig(repoName, "auto_init = true") + branch + fmt.Sprintf(`
resource "ghost_release" "default" {
  repository = ghost_repository.test.full_name
  tag_name   = "v1"
}

resource "ghost_release" "branch" {
  repository       = ghost_repository.test.full_name
  tag_name         = "v2"
  target_commitish = %q
}

resource "ghost_release" "sha" {
  repository       = ghost_repository.test.full_name
  tag_name         = "release/1.0"
  target_commitish = ghost_branch.feat.sha
}

# The tag already exists, so the target is ignored.
resource "ghost_release" "existing" {
  repository       = ghost_repository.test.full_name
  tag_name         = "pushed"
  target_commitish = "feat/x.y"
}
`, branchTarget)
	}
	sha := func(want *string) resource.CheckResourceAttrWithFunc {
		return func(v string) error {
			if v != *want {
				return fmt.Errorf("commit_sha is %s, want %s", v, *want)
			}
			return nil
		}
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckReleaseDestroy,
		Steps: []resource.TestStep{
			{Config: testAccRepositoryConfig(repoName, "auto_init = true") + branch},
			{
				PreConfig: func() {
					shaFeat = testAccPushCommit(t, repoName, "feat/x.y", "")
					shaTag = testAccPushCommit(t, repoName, "main", "pushed")
					shaMain = testAccPushCommit(t, repoName, "main", "")
				},
				Config: releases("feat/x.y"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrWith("ghost_release.default", "commit_sha", sha(&shaMain)),
					resource.TestCheckResourceAttrWith("ghost_release.branch", "commit_sha", sha(&shaFeat)),
					resource.TestCheckResourceAttr("ghost_release.branch", "target_commitish", "feat/x.y"),
					resource.TestCheckResourceAttrPair("ghost_release.sha", "commit_sha", "ghost_branch.feat", "sha"),
					testAccCheckRemoteTag(t, repoName, "release/1.0", "ghost_release.sha"),
					resource.TestCheckResourceAttrWith("ghost_release.existing", "commit_sha", sha(&shaTag)),
					resource.TestCheckResourceAttr("ghost_release.existing", "target_commitish", "feat/x.y"),
					testAccCheckReleaseExists("ghost_release.existing"),
				),
			},
			{
				// Changing the target updates state only.
				Config: releases("main"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("ghost_release.branch", plancheck.ResourceActionUpdate),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ghost_release.branch", "target_commitish", "main"),
					resource.TestCheckResourceAttrWith("ghost_release.branch", "commit_sha", sha(&shaFeat)),
					testAccCheckRemoteTag(t, repoName, "v2", "ghost_release.branch"),
				),
			},
			{
				ResourceName:            "ghost_release.sha",
				ImportState:             true,
				ImportStateIdFunc:       testAccReleaseImportID("ghost_release.sha"),
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"target_commitish"},
			},
		},
	})
}

func TestAccReleaseResource_drift(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")
	const addr = "ghost_release.test"
	config := testAccReleaseConfig(repoName, `tag_name = "v1"`)

	var releaseID int64
	var pushed, moved string
	captureID := func(s *terraform.State) error {
		id, err := strconv.ParseInt(s.RootModule().Resources[addr].Primary.Attributes["release_id"], 10, 64)
		releaseID = id
		return err
	}
	differentID := statecheck.CompareValue(compare.ValuesDiffer())
	sameSHA := statecheck.CompareValue(compare.ValuesSame())

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckReleaseDestroy,
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					differentID.AddStateValue(addr, tfjsonpath.New("release_id")),
					sameSHA.AddStateValue(addr, tfjsonpath.New("commit_sha")),
				},
				Check: captureID,
			},
			{
				// A release deleted outside Terraform is recreated on its
				// tag, which Ghost kept.
				PreConfig: func() { testAccDeleteRelease(t, repoName, releaseID) },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionCreate)},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					differentID.AddStateValue(addr, tfjsonpath.New("release_id")),
					sameSHA.AddStateValue(addr, tfjsonpath.New("commit_sha")),
				},
			},
			{
				// The release survives its tag being deleted.
				PreConfig: func() { testAccDeleteTag(t, repoName, "v1") },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckReleaseExists(addr),
					resource.TestCheckNoResourceAttr(addr, "commit_sha"),
				),
			},
			{
				PreConfig: func() { pushed = testAccPushCommit(t, repoName, "main", "v1") },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.TestCheckResourceAttrWith(addr, "commit_sha", func(v string) error {
					if v != pushed {
						return fmt.Errorf("commit_sha is %s, want the pushed tag %s", v, pushed)
					}
					return nil
				}),
			},
			{
				PreConfig: func() { moved = testAccMoveTag(t, repoName, "v1", "main") },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.TestCheckResourceAttrWith(addr, "commit_sha", func(v string) error {
					if v != moved {
						return fmt.Errorf("commit_sha is %s, want the moved tag %s", v, moved)
					}
					return nil
				}),
			},
		},
	})
}

func TestAccReleaseResource_tagChangeAndDelete(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")
	const addr = "ghost_release.test"

	var firstID, secondID int64
	capture := func(id *int64) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			var err error
			*id, err = strconv.ParseInt(s.RootModule().Resources[addr].Primary.Attributes["release_id"], 10, 64)
			return err
		}
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckReleaseDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccReleaseConfig(repoName, `tag_name = "v1"`),
				Check:  capture(&firstID),
			},
			{
				// Ghost can't move a release to another tag, so it's
				// replaced, and the old tag is kept.
				Config: testAccReleaseConfig(repoName, `tag_name = "v2"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionReplace)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					capture(&secondID),
					testAccCheckReleaseGone(repoName, &firstID),
					testAccCheckRemoteTag(t, repoName, "v1", ""),
					testAccCheckRemoteTag(t, repoName, "v2", addr),
				),
			},
			{
				// Deleting a release keeps its tag.
				Config: testAccRepositoryConfig(repoName, "auto_init = true"),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckReleaseGone(repoName, &secondID),
					testAccCheckRemoteTag(t, repoName, "v2", ""),
				),
			},
			{
				// A release on the kept tag reuses it, ignoring the target.
				Config: testAccReleaseConfig(repoName, `
  tag_name         = "v2"
  target_commitish = "no-such-branch"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckReleaseExists(addr),
					testAccCheckRemoteTag(t, repoName, "v2", addr),
				),
			},
		},
	})
}

func TestAccReleaseResource_repositoryRename(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")
	renamed := repoName + "-renamed"
	const addr = "ghost_release.test"

	sameReleaseID := statecheck.CompareValue(compare.ValuesSame())
	stateChecks := func() []statecheck.StateCheck {
		return []statecheck.StateCheck{
			sameReleaseID.AddStateValue(addr, tfjsonpath.New("release_id")),
			statecheck.CompareValuePairs(addr, tfjsonpath.New("repository"), "ghost_repository.test", tfjsonpath.New("full_name"), compare.ValuesSame()),
		}
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckReleaseDestroy,
		Steps: []resource.TestStep{
			{
				Config:            testAccReleaseConfig(repoName, testAccReleaseReplaceTriggeredBy),
				ConfigStateChecks: stateChecks(),
			},
			{
				// Renaming the repository keeps the release, even with the
				// recommended replace_triggered_by.
				Config: testAccReleaseConfig(renamed, testAccReleaseReplaceTriggeredBy),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: stateChecks(),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckReleaseExists(addr),
					resource.TestCheckResourceAttrWith(addr, "id", func(v string) error {
						if !regexp.MustCompile("/" + regexp.QuoteMeta(renamed) + ":[0-9]+$").MatchString(v) {
							return fmt.Errorf("id %q was not updated after the repository rename", v)
						}
						return nil
					}),
					resource.TestCheckResourceAttrWith(addr, "html_url", func(v string) error {
						if !strings.Contains(v, "/"+renamed+"/releases/") {
							return fmt.Errorf("html_url %q was not updated after the repository rename", v)
						}
						return nil
					}),
				),
			},
		},
	})
}

func TestAccReleaseResource_repositoryReplaced(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")
	const addr = "ghost_release.test"
	config := func(license string) string {
		return testAccRepositoryConfig(repoName, fmt.Sprintf("auto_init = true\n  license_template = %q", license)) +
			testAccReleaseBlock("test", testAccReleaseReplaceTriggeredBy)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckReleaseDestroy,
		Steps: []resource.TestStep{
			{Config: config("mit")},
			{
				// Replacing the repository deletes its releases;
				// replace_triggered_by recreates them.
				Config: config("apache-2.0"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("ghost_repository.test", plancheck.ResourceActionDestroyBeforeCreate),
						plancheck.ExpectResourceAction(addr, plancheck.ResourceActionReplace),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.CompareValuePairs(addr, tfjsonpath.New("repository_id"), "ghost_repository.test", tfjsonpath.New("repo_id"), compare.ValuesSame()),
				},
				Check: testAccCheckReleaseExists(addr),
			},
		},
	})
}

func TestAccReleaseResource_errors(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")
	emptyRepo := strings.Replace(testAccRepositoryConfig(repoName+"-empty", ""), `"test"`, `"empty"`, 1)
	base := testAccRepositoryConfig(repoName, "auto_init = true") + emptyRepo +
		testAccReleaseBlock("a", "tag_name = \"v1\"\n  draft = true")
	with := func(name, attrs string) string { return base + testAccReleaseBlock(name, attrs) }

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckReleaseDestroy,
		Steps: []resource.TestStep{
			{Config: base},
			{
				// The existing release is a draft, so it's found by listing.
				Config:      with("b", "tag_name = \"v1\"\n  depends_on = [ghost_release.a]"),
				ExpectError: regexp.MustCompile(`(?s)already has a release.*terraform import.*/` + regexp.QuoteMeta(repoName) + `:[0-9]+`),
			},
			{
				Config:      with("b", "tag_name = \"v9\"\n  target_commitish = \"no-such-branch\""),
				ExpectError: regexp.MustCompile(`(?s)Can't create the tag.*no-such-branch`),
			},
			{
				Config: base + `
resource "ghost_release" "b" {
  repository = ghost_repository.empty.full_name
  tag_name   = "v1"
}
`,
				ExpectError: regexp.MustCompile(`(?s)Can't create the tag.*empty`),
			},
			{
				Config:      with("b", `tag_name = "bad..name"`),
				ExpectError: regexp.MustCompile(`Invalid tag name`),
			},
			{
				Config: base + fmt.Sprintf(`
resource "ghost_release" "b" {
  repository = "${split("/", ghost_repository.test.full_name)[0]}/%s-missing"
  tag_name   = "v1"
}
`, repoName),
				ExpectError: regexp.MustCompile(`Repository not found`),
			},
			{
				// Nothing was left behind by the failed creates.
				Config: base,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: testAccCheckReleaseCount(repoName, 1),
			},
		},
	})
}

// testAccReleaseReplaceTriggeredBy is a release configured as the docs
// recommend, so it is recreated along with its repository.
const testAccReleaseReplaceTriggeredBy = `tag_name = "v1"

  lifecycle {
    replace_triggered_by = [ghost_repository.test.repo_id]
  }`

// testAccReleaseConfig returns an initialised repository and a
// ghost_release.test in it, with attrs as the release's other arguments.
func testAccReleaseConfig(repoName, attrs string) string {
	return testAccRepositoryConfig(repoName, "auto_init = true") + testAccReleaseBlock("test", attrs)
}

// testAccReleaseBlock returns a ghost_release.<name> in
// ghost_repository.test, with attrs as its other arguments.
func testAccReleaseBlock(name, attrs string) string {
	return fmt.Sprintf(`
resource "ghost_release" %q {
  repository = ghost_repository.test.full_name
  %s
}
`, name, attrs)
}

// testAccReleaseImportID returns the import ID of the release at address.
func testAccReleaseImportID(address string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[address]
		if !ok {
			return "", fmt.Errorf("%s not in state", address)
		}
		return rs.Primary.Attributes["repository"] + ":" + rs.Primary.Attributes["release_id"], nil
	}
}

// testAccCheckReleaseExists checks that the release in state matches the
// remote one.
func testAccCheckReleaseExists(address string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[address]
		if !ok {
			return fmt.Errorf("%s not in state", address)
		}
		release, err := testAccGetRelease(rs.Primary.Attributes["repository"], rs.Primary.Attributes["release_id"])
		if err != nil {
			return fmt.Errorf("reading %s: %w", rs.Primary.ID, err)
		}
		a := rs.Primary.Attributes
		deref := func(p *string) string {
			if p == nil {
				return ""
			}
			return *p
		}
		got := map[string]string{
			"tag_name": release.TagName, "draft": strconv.FormatBool(release.Draft),
			"prerelease": strconv.FormatBool(release.Prerelease), "name": deref(release.Name), "body": deref(release.Body),
		}
		for k, v := range got {
			if a[k] != v {
				return fmt.Errorf("%s: %s is %q in state, %q remotely", address, k, a[k], v)
			}
		}
		return nil
	}
}

// testAccGetRelease reads a release by its repository and ID.
func testAccGetRelease(repository, id string) (*client.Release, error) {
	c, err := testAccClient()
	if err != nil {
		return nil, err
	}
	owner, repo, err := client.SplitFullName(repository)
	if err != nil {
		return nil, err
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return nil, err
	}
	return c.GetRelease(context.Background(), owner, repo, n)
}

// testAccCheckReleaseGone checks that the release *id in repoName has been
// deleted.
func testAccCheckReleaseGone(repoName string, id *int64) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		repository := s.RootModule().Resources["ghost_repository.test"].Primary.Attributes["full_name"]
		_, err := testAccGetRelease(repository, strconv.FormatInt(*id, 10))
		if err == nil {
			return fmt.Errorf("release %d still exists in %s", *id, repoName)
		}
		if !client.IsNotFound(err) {
			return err
		}
		return nil
	}
}

// testAccCheckReleaseCount checks how many releases, drafts included, the
// repository has.
func testAccCheckReleaseCount(repoName string, want int) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		c, err := testAccClient()
		if err != nil {
			return err
		}
		owner, repo, err := client.SplitFullName(s.RootModule().Resources["ghost_repository.test"].Primary.Attributes["full_name"])
		if err != nil {
			return err
		}
		releases, err := c.ListReleases(context.Background(), owner, repo)
		if err != nil {
			return err
		}
		if len(releases) != want {
			return fmt.Errorf("%s has %d releases, want %d", repoName, len(releases), want)
		}
		return nil
	}
}

// testAccCheckRemoteTag checks over git that tag exists in repoName and, when
// address isn't "", that it points at that release's commit_sha.
func testAccCheckRemoteTag(t *testing.T, repoName, tag, address string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		sha, ok := testAccRemoteTag(t, repoName, tag)
		if !ok {
			return fmt.Errorf("tag %s doesn't exist in %s", tag, repoName)
		}
		if address == "" {
			return nil
		}
		if want := s.RootModule().Resources[address].Primary.Attributes["commit_sha"]; sha != want {
			return fmt.Errorf("tag %s points at %s, but %s.commit_sha is %s", tag, sha, address, want)
		}
		return nil
	}
}

// testAccRemoteTag returns the commit tag points at in repoName, peeling an
// annotated tag, read with git ls-remote so it costs no API request.
func testAccRemoteTag(t *testing.T, repoName, tag string) (string, bool) {
	t.Helper()
	_, repo := testAccRepository(t, repoName)
	git := testAccGit(t, repo)

	refs := map[string]string{}
	for _, line := range strings.Split(git("ls-remote", "--tags", repo.CloneURL, "refs/tags/"+tag, "refs/tags/"+tag+"^{}"), "\n") {
		if sha, ref, ok := strings.Cut(line, "\t"); ok {
			refs[ref] = sha
		}
	}
	if sha, ok := refs["refs/tags/"+tag+"^{}"]; ok {
		return sha, true
	}
	sha, ok := refs["refs/tags/"+tag]
	return sha, ok
}

// testAccMoveTag pushes an empty commit to branch and force-moves tag to it
// as an annotated tag, returning the commit's SHA.
func testAccMoveTag(t *testing.T, repoName, tag, branch string) string {
	t.Helper()
	_, repo := testAccRepository(t, repoName)
	git := testAccGit(t, repo)

	git("clone", "-q", "--branch", branch, repo.CloneURL, ".")
	git("commit", "-q", "--allow-empty", "-m", "tf-acc move tag")
	git("tag", "-f", "-a", tag, "-m", "moved")
	git("push", "-q", "-f", "origin", "HEAD:refs/heads/"+branch, "refs/tags/"+tag)
	return git("rev-parse", "HEAD")
}

// testAccDeleteTag deletes a tag outside Terraform.
func testAccDeleteTag(t *testing.T, repoName, tag string) {
	t.Helper()
	_, repo := testAccRepository(t, repoName)
	git := testAccGit(t, repo)

	git("init", "-q")
	git("push", "-q", repo.CloneURL, "--delete", "refs/tags/"+tag)
}

// testAccDeleteRelease deletes a release outside Terraform.
func testAccDeleteRelease(t *testing.T, repoName string, id int64) {
	t.Helper()
	c, repo := testAccRepository(t, repoName)
	owner, slug, _ := client.SplitFullName(repo.FullName)
	if err := c.DeleteRelease(context.Background(), owner, slug, id); err != nil {
		t.Fatalf("deleting release %d: %v", id, err)
	}
}

// testAccCheckReleaseDestroy checks that every release in state is gone. It
// can't check that the tags were kept, because the repository is gone too:
// TestAccReleaseResource_tagChangeAndDelete checks that.
func testAccCheckReleaseDestroy(s *terraform.State) error {
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "ghost_release" {
			continue
		}
		_, err := testAccGetRelease(rs.Primary.Attributes["repository"], rs.Primary.Attributes["release_id"])
		if err == nil {
			return fmt.Errorf("release %s still exists", rs.Primary.ID)
		}
		if !client.IsNotFound(err) {
			return fmt.Errorf("checking release %s: %w", rs.Primary.ID, err)
		}
	}

	return testAccCheckRepositoryDestroy(s)
}
