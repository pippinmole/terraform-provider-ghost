package provider

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

func strPtr(s string) *string { return &s }

func TestParseRepositoryImportID(t *testing.T) {
	owner, slug, err := parseRepositoryImportID("alice/my-repo")
	if err != nil || owner != "alice" || slug != "my-repo" {
		t.Fatalf("got %q %q %v", owner, slug, err)
	}

	for _, id := range []string{"", "alice", "alice/", "/repo", "a/b/c"} {
		if _, _, err := parseRepositoryImportID(id); err == nil {
			t.Errorf("parseRepositoryImportID(%q): expected error", id)
		}
	}
}

func TestRepositoryNameFromRemote(t *testing.T) {
	tests := []struct {
		name      string
		prior     types.String
		priorSlug string
		remote    string
		want      types.String
	}{
		{"import has no prior name", types.StringNull(), "", "repo", types.StringValue("repo")},
		{"same as remote", types.StringValue("repo"), "repo", "repo", types.StringValue("repo")},
		{"display name kept while slug unchanged", types.StringValue("My Repo"), "my-repo", "my-repo", types.StringValue("My Repo")},
		{"renamed outside terraform", types.StringValue("My Repo"), "my-repo", "other", types.StringValue("other")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := repositoryNameFromRemote(tt.prior, tt.priorSlug, tt.remote); !got.Equal(tt.want) {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestRepositoryDescriptionFromRemote(t *testing.T) {
	tests := []struct {
		name   string
		prior  types.String
		remote *string
		want   types.String
	}{
		{"null remote, null prior", types.StringNull(), nil, types.StringNull()},
		{"empty remote, null prior", types.StringNull(), strPtr(""), types.StringNull()},
		{"null remote, empty prior", types.StringValue(""), nil, types.StringValue("")},
		{"null remote, stale prior", types.StringValue("old"), nil, types.StringNull()},
		{"value remote", types.StringNull(), strPtr("hello"), types.StringValue("hello")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := repositoryDescriptionFromRemote(tt.prior, tt.remote); !got.Equal(tt.want) {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestRepositoryModelFromRemote(t *testing.T) {
	r := &client.Repository{
		ID:            42,
		NodeID:        "R_abc",
		Name:          "my-repo",
		FullName:      "Alice/my-repo",
		Private:       true,
		Visibility:    "private",
		Owner:         client.RepositoryOwner{Login: "Alice"},
		HTMLURL:       "https://web.example/Alice/my-repo",
		Description:   strPtr("desc"),
		Fork:          false,
		CloneURL:      "https://api.example/Alice/my-repo.git",
		SSHURL:        "",
		DefaultBranch: "main",
	}

	m := repositoryResourceModel{Owner: types.StringValue("alice")}
	if err := m.fromRemote(r); err != nil {
		t.Fatalf("fromRemote: %v", err)
	}

	checks := map[string][2]string{
		"id":             {m.ID.ValueString(), "Alice/my-repo"},
		"full_name":      {m.FullName.ValueString(), "Alice/my-repo"},
		"slug":           {m.Slug.ValueString(), "my-repo"},
		"owner":          {m.Owner.ValueString(), "alice"}, // configured case is kept
		"node_id":        {m.NodeID.ValueString(), "R_abc"},
		"html_url":       {m.HTMLURL.ValueString(), "https://web.example/Alice/my-repo"},
		"clone_url":      {m.CloneURL.ValueString(), "https://api.example/Alice/my-repo.git"},
		"ssh_url":        {m.SSHURL.ValueString(), ""},
		"visibility":     {m.Visibility.ValueString(), "private"},
		"default_branch": {m.DefaultBranch.ValueString(), "main"},
	}
	for attr, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s = %q, want %q", attr, c[0], c[1])
		}
	}
	if m.RepoID.ValueInt64() != 42 || !m.Private.ValueBool() || m.Fork.ValueBool() {
		t.Errorf("repo_id/private/fork = %d/%t/%t", m.RepoID.ValueInt64(), m.Private.ValueBool(), m.Fork.ValueBool())
	}

	// An owner that differs from the remote one (other than by case) is replaced.
	m = repositoryResourceModel{Owner: types.StringValue("bob")}
	if err := m.fromRemote(r); err != nil {
		t.Fatalf("fromRemote: %v", err)
	}
	if m.Owner.ValueString() != "Alice" {
		t.Errorf("owner = %q, want Alice", m.Owner.ValueString())
	}

	bad := *r
	bad.FullName = "nope"
	if err := m.fromRemote(&bad); err == nil {
		t.Error("expected error for invalid full_name")
	}
}

func TestAccRepositoryResource(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc")
	renamed := name + "-renamed"

	// The same repository must survive every step: updates are in place.
	sameRepoID := statecheck.CompareValue(compare.ValuesSame())
	// Each call records one more value to compare, so call it once per step.
	repoIDCheck := func() []statecheck.StateCheck {
		return []statecheck.StateCheck{
			sameRepoID.AddStateValue("ghost_repository.test", tfjsonpath.New("repo_id")),
		}
	}
	expectAction := func(action plancheck.ResourceActionType) resource.ConfigPlanChecks {
		return resource.ConfigPlanChecks{
			PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("ghost_repository.test", action)},
		}
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{
				Config:            testAccRepositoryConfig(name, `description = "created by terraform acceptance tests"`),
				ConfigPlanChecks:  expectAction(plancheck.ResourceActionCreate),
				ConfigStateChecks: repoIDCheck(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ghost_repository.test", "name", name),
					resource.TestCheckResourceAttr("ghost_repository.test", "slug", name),
					resource.TestCheckResourceAttr("ghost_repository.test", "visibility", "private"),
					resource.TestCheckResourceAttr("ghost_repository.test", "private", "true"),
					resource.TestCheckResourceAttr("ghost_repository.test", "fork", "false"),
					resource.TestCheckResourceAttr("ghost_repository.test", "description", "created by terraform acceptance tests"),
					resource.TestCheckResourceAttr("ghost_repository.test", "default_branch", "main"),
					resource.TestCheckResourceAttrPair("ghost_repository.test", "id", "ghost_repository.test", "full_name"),
					resource.TestCheckResourceAttrSet("ghost_repository.test", "owner"),
					resource.TestCheckResourceAttrSet("ghost_repository.test", "repo_id"),
					resource.TestCheckResourceAttrSet("ghost_repository.test", "node_id"),
					resource.TestCheckResourceAttrSet("ghost_repository.test", "html_url"),
					resource.TestCheckResourceAttrSet("ghost_repository.test", "clone_url"),
				),
			},
			{
				ResourceName:            "ghost_repository.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"auto_init", "gitignore_template", "license_template"},
			},
			{
				Config: testAccRepositoryConfig(renamed, `
  description = "updated"
  visibility  = "public"`),
				ConfigPlanChecks:  expectAction(plancheck.ResourceActionUpdate),
				ConfigStateChecks: repoIDCheck(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ghost_repository.test", "name", renamed),
					resource.TestCheckResourceAttr("ghost_repository.test", "slug", renamed),
					resource.TestCheckResourceAttr("ghost_repository.test", "description", "updated"),
					resource.TestCheckResourceAttr("ghost_repository.test", "visibility", "public"),
					resource.TestCheckResourceAttr("ghost_repository.test", "private", "false"),
					resource.TestCheckResourceAttrWith("ghost_repository.test", "html_url", func(v string) error {
						if !strings.HasSuffix(v, "/"+renamed) {
							return fmt.Errorf("html_url %q was not re-read after the rename", v)
						}
						return nil
					}),
					testAccCheckRepositoryRemote(renamed, strPtr("updated"), "public"),
				),
			},
			{
				// Removing the description clears it remotely.
				Config:            testAccRepositoryConfig(renamed, `visibility = "public"`),
				ConfigPlanChecks:  expectAction(plancheck.ResourceActionUpdate),
				ConfigStateChecks: repoIDCheck(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("ghost_repository.test", "description"),
					testAccCheckRepositoryRemote(renamed, nil, "public"),
				),
			},
			{
				// Setting a create-only argument that was unset (as after an
				// import) adopts it in place instead of recreating the repo.
				Config: testAccRepositoryConfig(renamed, `
  visibility       = "public"
  license_template = "mit"`),
				ConfigPlanChecks:  expectAction(plancheck.ResourceActionUpdate),
				ConfigStateChecks: repoIDCheck(),
				Check:             resource.TestCheckResourceAttr("ghost_repository.test", "license_template", "mit"),
			},
		},
	})
}

func testAccRepositoryConfig(name, extra string) string {
	return fmt.Sprintf(`
resource "ghost_repository" "test" {
  name = %q
  %s
}
`, name, extra)
}

// testAccClient returns an API client configured from the environment.
func testAccClient() (*client.Client, error) {
	baseURL := os.Getenv(envBaseURL)
	if baseURL == "" {
		baseURL = client.DefaultBaseURL
	}

	return client.New(baseURL, os.Getenv(envToken))
}

// testAccCheckRepositoryRemote verifies, straight from the API, that the
// repository in state has the given slug, description (nil or "" for none)
// and visibility.
func testAccCheckRepositoryRemote(slug string, description *string, visibility string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources["ghost_repository.test"]
		if !ok {
			return fmt.Errorf("ghost_repository.test not in state")
		}
		c, err := testAccClient()
		if err != nil {
			return err
		}
		repo, err := c.GetRepository(context.Background(), rs.Primary.Attributes["owner"], slug)
		if err != nil {
			return fmt.Errorf("reading %s: %w", slug, err)
		}

		gotDesc := ""
		if repo.Description != nil {
			gotDesc = *repo.Description
		}
		wantDesc := ""
		if description != nil {
			wantDesc = *description
		}
		if repo.Name != slug || gotDesc != wantDesc || repo.Visibility != visibility {
			return fmt.Errorf("remote name/description/visibility = %q/%q/%q, want %q/%q/%q",
				repo.Name, gotDesc, repo.Visibility, slug, wantDesc, visibility)
		}

		return nil
	}
}

func testAccCheckRepositoryDestroy(s *terraform.State) error {
	c, err := testAccClient()
	if err != nil {
		return err
	}

	for _, rs := range s.RootModule().Resources {
		if rs.Type != "ghost_repository" {
			continue
		}
		owner, slug, err := client.SplitFullName(rs.Primary.ID)
		if err != nil {
			return err
		}
		_, err = c.GetRepository(context.Background(), owner, slug)
		if err == nil {
			return fmt.Errorf("repository %s still exists", rs.Primary.ID)
		}
		if !client.IsNotFound(err) {
			return fmt.Errorf("checking repository %s: %w", rs.Primary.ID, err)
		}
	}

	return nil
}
