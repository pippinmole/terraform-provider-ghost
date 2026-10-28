package provider

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

func TestReleaseDataSourceConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config string
		expect string
	}{
		{"no repository", `retrieve_by = "latest"`, `(?s)"repository" is required`},
		{"bad repository", `repository = "nope"` + "\n" + `retrieve_by = "latest"`, `must be "owner/slug"`},
		{"no retrieve_by", `repository = "alice/repo"`, `(?s)"retrieve_by" is required`},
		{"bad retrieve_by", `repository = "alice/repo"` + "\n" + `retrieve_by = "newest"`, `(?s)value must be one of`},
		{"retrieve_by is case-sensitive", `repository = "alice/repo"` + "\n" + `retrieve_by = "ID"`, `(?s)value must be one of`},
		{"id without release_id", `repository = "alice/repo"` + "\n" + `retrieve_by = "id"`, `(?s)Missing release_id.*required when retrieve_by is "id"`},
		{"tag without release_tag", `repository = "alice/repo"` + "\n" + `retrieve_by = "tag"`, `(?s)Missing release_tag.*required when retrieve_by is "tag"`},
		{"latest with release_id", `repository = "alice/repo"` + "\n" + `retrieve_by = "latest"` + "\n" + `release_id = 1`,
			`(?s)Unexpected release_id.*only used when retrieve_by is "id"`},
		{"latest with release_tag", `repository = "alice/repo"` + "\n" + `retrieve_by = "latest"` + "\n" + `release_tag = "v1"`,
			`(?s)Unexpected release_tag.*only used when retrieve_by is "tag"`},
		{"id with release_tag", `repository = "alice/repo"` + "\n" + `retrieve_by = "id"` + "\n" + `release_id = 1` + "\n" + `release_tag = "v1"`,
			`(?s)Unexpected release_tag`},
		{"tag with release_id", `repository = "alice/repo"` + "\n" + `retrieve_by = "tag"` + "\n" + `release_tag = "v1"` + "\n" + `release_id = 1`,
			`(?s)Unexpected release_id`},
		{"zero release_id", `repository = "alice/repo"` + "\n" + `retrieve_by = "id"` + "\n" + `release_id = 0`, `(?s)at least 1`},
		{"empty release_tag", `repository = "alice/repo"` + "\n" + `retrieve_by = "tag"` + "\n" + `release_tag = ""`, `(?i)length`},
		{"long release_tag", `repository = "alice/repo"` + "\n" + `retrieve_by = "tag"` + "\n" + `release_tag = "` + strings.Repeat("a", 251) + `"`, `(?i)length`},
		{"full ref release_tag", `repository = "alice/repo"` + "\n" + `retrieve_by = "tag"` + "\n" + `release_tag = "refs/tags/v1"`, `(?s)must not start with "refs/"`},
		{"computed name", `repository = "alice/repo"` + "\n" + `retrieve_by = "latest"` + "\n" + `name = "x"`, `(?s)Invalid Configuration for Read-Only Attribute`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      "data \"ghost_release\" \"test\" {\n" + tt.config + "\n}\n",
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tt.expect),
				}},
			})
		})
	}
}

// Valid forms pass validation, including a release ID and tag that are only
// known after apply. The read then fails, with no Ghost server to talk to.
func TestReleaseDataSourceValidForms(t *testing.T) {
	for _, attrs := range []string{
		`retrieve_by = "latest"`,
		`retrieve_by = "id"` + "\n" + `release_id = 42`,
		`retrieve_by = "tag"` + "\n" + `release_tag = "release/` + strings.Repeat("a", 242) + `"`,
	} {
		t.Run(attrs, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config: `
provider "ghost" {
  token    = "x"
  base_url = "http://127.0.0.1:1"
}

data "ghost_release" "test" {
  repository = "alice/repo"
  ` + attrs + `
}
`,
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(`Error reading repository`),
				}},
			})
		})
	}

	// Values from resources that don't exist yet are unknown at validation,
	// and the read waits for apply, so the plan succeeds.
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
provider "ghost" {
  token    = "x"
  base_url = "http://127.0.0.1:1"
}

resource "ghost_release" "test" {
  repository = "alice/repo"
  tag_name   = "v1"
}

data "ghost_release" "by_id" {
  repository  = "alice/repo"
  retrieve_by = "id"
  release_id  = ghost_release.test.release_id
}

data "ghost_release" "by_tag" {
  repository  = "alice/repo"
  retrieve_by = "tag"
  release_tag = ghost_release.test.node_id
}
`,
			PlanOnly:           true,
			ExpectNonEmptyPlan: true,
		}},
	})
}

func TestReleaseDataSourceFromRemote(t *testing.T) {
	str := func(s string) *string { return &s }

	m := releaseDataSourceModel{Repository: types.StringValue("ALICE/old")}
	release := &client.Release{
		ID: 7, NodeID: "RE_x", TagName: "v1", TargetCommitish: testAccSHA,
		Name: str("One"), Body: str(" notes \n"), Prerelease: true,
		URL: "u", HTMLURL: "h", AssetsURL: "a", UploadURL: "up", TarballURL: "t", ZipballURL: "z",
		CreatedAt: "c", PublishedAt: str("p"), Author: &client.ReleaseAuthor{Login: "alice"},
		Assets: []client.ReleaseAsset{
			{ID: 1, NodeID: "RA_1", Name: "a.zip", Label: str("A"), ContentType: "application/zip", Size: 3, DownloadCount: 2,
				URL: "au", BrowserDownloadURL: "ab", CreatedAt: "ac", UpdatedAt: "ad"},
			{ID: 2, Name: "b.txt"},
		},
	}
	if diags := m.fromRemote("alice/new", release); diags.HasError() {
		t.Fatal(diags)
	}

	for name, got := range map[string]string{
		"id": m.ID.ValueString(), "repository": m.Repository.ValueString(), "release_tag": m.ReleaseTag.ValueString(),
		"name": m.Name.ValueString(), "body": m.Body.ValueString(), "commit_sha": m.CommitSHA.ValueString(),
		"author_login": m.AuthorLogin.ValueString(), "assets_url": m.AssetsURL.ValueString(), "published_at": m.PublishedAt.ValueString(),
	} {
		want := map[string]string{
			"id": "alice/new:7", "repository": "ALICE/old", "release_tag": "v1", "name": "One", "body": " notes \n",
			"commit_sha": testAccSHA, "author_login": "alice", "assets_url": "a", "published_at": "p",
		}[name]
		if got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if m.ReleaseID.ValueInt64() != 7 || !m.Prerelease.ValueBool() || m.Draft.ValueBool() {
		t.Errorf("release_id, prerelease, draft = %v, %v, %v", m.ReleaseID, m.Prerelease, m.Draft)
	}
	if len(m.Assets.Elements()) != 2 {
		t.Fatalf("assets = %v, want 2 elements", m.Assets)
	}
	first := m.Assets.Elements()[0].String()
	for _, want := range []string{`"name":"a.zip"`, `"label":"A"`, `"size":3`, `"download_count":2`, `"browser_download_url":"ab"`} {
		if !strings.Contains(first, want) {
			t.Errorf("assets[0] = %s, missing %s", first, want)
		}
	}
	if second := m.Assets.Elements()[1].String(); !strings.Contains(second, `"label":<null>`) {
		t.Errorf("assets[1] = %s, want a null label", second)
	}

	// A never-published draft without a name, body or author, whose tag
	// has been deleted.
	m = releaseDataSourceModel{}
	if diags := m.fromRemote("alice/repo", &client.Release{ID: 8, TagName: "v2", TargetCommitish: "main", Draft: true}); diags.HasError() {
		t.Fatal(diags)
	}
	for name, v := range map[string]types.String{
		"name": m.Name, "body": m.Body, "commit_sha": m.CommitSHA, "author_login": m.AuthorLogin, "published_at": m.PublishedAt,
	} {
		if !v.IsNull() {
			t.Errorf("%s = %v, want null", name, v)
		}
	}
	if m.Assets.IsNull() || len(m.Assets.Elements()) != 0 {
		t.Errorf("assets = %v, want an empty list", m.Assets)
	}
}

func TestAccReleaseDataSource_basic(t *testing.T) {
	testAccRepositoriesPreCheck(t)
	repoName := acctest.RandomWithPrefix("tf-acc-release-ds")
	owner := testAccTokenLogin(t)
	oldFullName := owner + "/" + repoName

	// Releases are created one after another, so the latest is predictable.
	releases := testAccReleaseBlock("plain", `tag_name = "release/0.9"`) +
		testAccReleaseBlock("stable", "tag_name = \"v1.0.0\"\n  name = \"Stable\"\n  body = \"# Notes\\n\\n**bold**\"\n  depends_on = [ghost_release.plain]") +
		testAccReleaseBlock("pre", "tag_name = \"v1.1.0-rc.1\"\n  prerelease = true\n  depends_on = [ghost_release.stable]") +
		testAccReleaseBlock("draft", "tag_name = \"v2.0.0\"\n  name = \"Draft\"\n  draft = true\n  depends_on = [ghost_release.pre]")
	newer := testAccReleaseBlock("newer", "tag_name = \"v1.2.0\"\n  depends_on = [ghost_release.draft]")
	data := func(name, attrs string) string {
		return fmt.Sprintf(`
data "ghost_release" %q {
  repository = ghost_repository.test.full_name
  %s
  depends_on = [ghost_release.plain, ghost_release.stable, ghost_release.pre, ghost_release.draft]
}
`, name, attrs)
	}
	repo := func(name string) string {
		return testAccRepositoryConfig(name, "auto_init = true\n  visibility = \"public\"")
	}
	latest := data("latest", `retrieve_by = "latest"`)
	byTag := data("by_tag", "retrieve_by = \"tag\"\n  release_tag = \"v1.0.0\"")
	step1 := repo(repoName) + releases + latest + byTag +
		data("by_id", "retrieve_by = \"id\"\n  release_id = ghost_release.pre.release_id") +
		data("plain", "retrieve_by = \"tag\"\n  release_tag = \"release/0.9\"") +
		data("draft_by_tag", "retrieve_by = \"tag\"\n  release_tag = \"v2.0.0\"") +
		data("draft_by_id", "retrieve_by = \"id\"\n  release_id = ghost_release.draft.release_id")

	const (
		dLatest = "data.ghost_release.latest"
		dByTag  = "data.ghost_release.by_tag"
	)
	pairs := func(d, r string, attrs ...string) resource.TestCheckFunc {
		checks := make([]resource.TestCheckFunc, 0, len(attrs))
		for _, a := range attrs {
			ra := a
			if a == "release_tag" {
				ra = "tag_name"
			}
			checks = append(checks, resource.TestCheckResourceAttrPair(d, a, r, ra))
		}
		return resource.ComposeAggregateTestCheckFunc(checks...)
	}
	allPairs := []string{"id", "release_id", "release_tag", "name", "body", "draft", "prerelease", "commit_sha", "node_id",
		"url", "html_url", "upload_url", "tarball_url", "zipball_url", "created_at", "published_at", "repository_id"}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckReleaseDestroy,
		Steps: []resource.TestStep{
			{
				Config: step1,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					// The prerelease and the draft were created later, but
					// are never the latest.
					pairs(dLatest, "ghost_release.stable", "id", "release_id", "release_tag"),
					resource.TestCheckResourceAttr(dLatest, "draft", "false"),
					resource.TestCheckResourceAttr(dLatest, "prerelease", "false"),

					pairs(dByTag, "ghost_release.stable", allPairs...),
					resource.TestCheckResourceAttr(dByTag, "name", "Stable"),
					resource.TestCheckResourceAttr(dByTag, "author_login", owner),
					resource.TestCheckResourceAttr(dByTag, "assets.#", "0"),
					resource.TestCheckResourceAttrPair(dByTag, "repository_id", "ghost_repository.test", "repo_id"),
					resource.TestCheckResourceAttrWith(dByTag, "assets_url", func(v string) error {
						if !strings.HasSuffix(v, "/assets") {
							return fmt.Errorf("assets_url %q doesn't end in /assets", v)
						}
						return nil
					}),

					pairs("data.ghost_release.by_id", "ghost_release.pre", "id", "release_tag", "prerelease", "published_at"),
					resource.TestCheckResourceAttr("data.ghost_release.by_id", "prerelease", "true"),

					// No name or body, and a tag with a slash.
					pairs("data.ghost_release.plain", "ghost_release.plain", "id", "commit_sha", "html_url"),
					resource.TestCheckNoResourceAttr("data.ghost_release.plain", "name"),
					resource.TestCheckNoResourceAttr("data.ghost_release.plain", "body"),

					// The tag route never returns drafts: found by listing.
					pairs("data.ghost_release.draft_by_tag", "ghost_release.draft", "id", "name", "commit_sha"),
					resource.TestCheckResourceAttr("data.ghost_release.draft_by_tag", "draft", "true"),
					resource.TestCheckNoResourceAttr("data.ghost_release.draft_by_tag", "published_at"),
					pairs("data.ghost_release.draft_by_id", "ghost_release.draft", "id", "release_tag", "draft"),
				),
			},
			{
				// A newer published release becomes the latest, and an
				// uploaded asset is listed.
				PreConfig: func() { testAccUploadReleaseAsset(t, repoName, "v1.0.0", "notes.txt", "hello asset\n") },
				Config: repo(repoName) + releases + newer + byTag +
					strings.Replace(latest, "ghost_release.draft]", "ghost_release.draft, ghost_release.newer]", 1),
				Check: resource.ComposeAggregateTestCheckFunc(
					pairs(dLatest, "ghost_release.newer", "id", "release_tag"),
					resource.TestCheckResourceAttr(dByTag, "assets.#", "1"),
					resource.TestCheckResourceAttr(dByTag, "assets.0.name", "notes.txt"),
					resource.TestCheckResourceAttr(dByTag, "assets.0.size", "12"),
					resource.TestCheckResourceAttr(dByTag, "assets.0.content_type", "text/plain"),
					resource.TestCheckResourceAttr(dByTag, "assets.0.download_count", "0"),
					resource.TestCheckNoResourceAttr(dByTag, "assets.0.label"),
					resource.TestCheckResourceAttrWith(dByTag, "assets.0.browser_download_url", func(v string) error {
						if !strings.HasSuffix(v, "/releases/download/v1.0.0/notes.txt") {
							return fmt.Errorf("browser_download_url %q doesn't name the tag and file", v)
						}
						return nil
					}),
					resource.TestCheckResourceAttrSet(dByTag, "assets.0.id"),
					resource.TestCheckResourceAttrSet(dByTag, "assets.0.node_id"),
					resource.TestCheckResourceAttrSet(dByTag, "assets.0.url"),
					resource.TestCheckResourceAttrSet(dByTag, "assets.0.created_at"),
					resource.TestCheckResourceAttrSet(dByTag, "assets.0.updated_at"),
				),
			},
			{
				// A release whose tag was deleted is still found by tag,
				// without a commit.
				PreConfig: func() { testAccDeleteTag(t, repoName, "v1.0.0") },
				Config:    repo(repoName) + releases + newer + byTag,
				Check: resource.ComposeAggregateTestCheckFunc(
					pairs(dByTag, "ghost_release.stable", "id", "release_tag"),
					resource.TestCheckNoResourceAttr(dByTag, "commit_sha"),
				),
			},
			{
				// The old name, in another case, still finds the release
				// after a rename; id and html_url use the new name.
				Config: repo(repoName+"-renamed") + releases + newer + fmt.Sprintf(`
data "ghost_release" "old_name" {
  repository  = %q
  retrieve_by = "tag"
  release_tag = "v1.2.0"
  depends_on  = [ghost_repository.test, ghost_release.newer]
}
`, strings.ToUpper(oldFullName)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.ghost_release.old_name", "repository", strings.ToUpper(oldFullName)),
					resource.TestMatchResourceAttr("data.ghost_release.old_name", "id",
						regexp.MustCompile("^"+regexp.QuoteMeta(oldFullName+"-renamed")+":[0-9]+$")),
					resource.TestCheckResourceAttrPair("data.ghost_release.old_name", "release_id", "ghost_release.newer", "release_id"),
					resource.TestMatchResourceAttr("data.ghost_release.old_name", "html_url",
						regexp.MustCompile("/"+regexp.QuoteMeta(repoName+"-renamed")+"/releases/tag/v1.2.0$")),
				),
			},
		},
	})
}

func TestAccReleaseDataSource_notFound(t *testing.T) {
	testAccRepositoriesPreCheck(t)
	repoName := acctest.RandomWithPrefix("tf-acc-release-ds")
	owner := testAccTokenLogin(t)

	// Repository test has only a draft and a prerelease; other has none.
	base := testAccRepositoryConfig(repoName, "auto_init = true") +
		strings.Replace(testAccRepositoryConfig(repoName+"-other", "auto_init = true"), `"test"`, `"other"`, 1) +
		testAccReleaseBlock("draft", "tag_name = \"v1\"\n  draft = true") +
		testAccReleaseBlock("pre", "tag_name = \"v2\"\n  prerelease = true\n  depends_on = [ghost_release.draft]")
	with := func(repository, attrs string) string {
		return base + fmt.Sprintf(`
data "ghost_release" "test" {
  repository = %s
  %s
  depends_on = [ghost_release.pre]
}
`, repository, attrs)
	}
	const repo, other = "ghost_repository.test.full_name", "ghost_repository.other.full_name"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckReleaseDestroy,
		Steps: []resource.TestStep{
			{Config: base},
			{
				Config:      with(repo, `retrieve_by = "latest"`),
				ExpectError: regexp.MustCompile(`(?s)No latest release.*Drafts\s+and\s+prereleases\s+are\s+never\s+the\s+latest`),
			},
			{
				Config:      with(other, `retrieve_by = "latest"`),
				ExpectError: regexp.MustCompile(`(?s)No latest release.*` + regexp.QuoteMeta(repoName+"-other")),
			},
			{
				Config:      with(repo, "retrieve_by = \"id\"\n  release_id = 999999999"),
				ExpectError: regexp.MustCompile(`(?s)Release not found.*no\s+release\s+with\s+ID\s+999999999`),
			},
			{
				// Another repository's release.
				Config:      with(other, "retrieve_by = \"id\"\n  release_id = ghost_release.draft.release_id"),
				ExpectError: regexp.MustCompile(`(?s)Release not found.*no\s+release\s+with\s+ID`),
			},
			{
				Config:      with(repo, "retrieve_by = \"tag\"\n  release_tag = \"v9\""),
				ExpectError: regexp.MustCompile(`(?s)Release not found.*no\s+release\s+for\s+tag\s+"v9".*case-sensitive`),
			},
			{
				Config:      with(repo, "retrieve_by = \"tag\"\n  release_tag = \"V1\""),
				ExpectError: regexp.MustCompile(`(?s)Release not found.*"V1"`),
			},
			{
				Config:      with(fmt.Sprintf("%q", owner+"/"+repoName+"-missing"), `retrieve_by = "latest"`),
				ExpectError: regexp.MustCompile(`(?s)Repository not found.*` + regexp.QuoteMeta(repoName+"-missing")),
			},
			{
				Config:      with(`"tf-acc-no-such-owner-x9/tf-acc-missing"`, `retrieve_by = "latest"`),
				ExpectError: regexp.MustCompile(`(?s)Repository not found.*tf-acc-no-such-owner-x9/tf-acc-missing`),
			},
			{
				// The failed reads left nothing behind.
				Config: base,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: testAccCheckReleaseCount(repoName, 2),
			},
		},
	})
}

// testAccUploadReleaseAsset uploads content as an asset named name to the
// release for tag, outside Terraform, through Ghost's native upload route.
// The token is sent in a header and never logged.
func testAccUploadReleaseAsset(t *testing.T, repoName, tag, name, content string) {
	t.Helper()
	c, repo := testAccRepository(t, repoName)
	owner, slug, _ := client.SplitFullName(repo.FullName)
	release, err := c.FindReleaseByTag(context.Background(), owner, slug, tag)
	if err != nil || release == nil {
		t.Fatalf("finding the release for tag %s: %v", tag, err)
	}

	baseURL := os.Getenv(envBaseURL)
	if baseURL == "" {
		baseURL = client.DefaultBaseURL
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	u = u.JoinPath("api", "uploads", "repos", owner, slug, "releases", fmt.Sprint(release.ID), "assets")
	u.RawQuery = url.Values{"name": {name}}.Encode()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, u.String(), bytes.NewBufferString(content))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv(envToken))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("uploading asset %s: %v", name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("uploading asset %s: %s: %s", name, resp.Status, body)
	}
}
