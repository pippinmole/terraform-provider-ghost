package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

func TestRepositoryDataSourceFromRemote(t *testing.T) {
	desc := "hello"
	repo := &client.Repository{
		ID: 7, NodeID: "R7", Name: "my-repo", FullName: "alice/my-repo", Private: true, Visibility: "private",
		HTMLURL: "h", Description: &desc, CloneURL: "c", SSHURL: "s", DefaultBranch: "main",
		CreatedAt: "t1", UpdatedAt: "t2", PushedAt: "",
	}

	var m repositoryDataSourceModel
	if err := m.fromRemote(repo); err != nil {
		t.Fatal(err)
	}
	if m.ID.ValueString() != "alice/my-repo" || m.Owner.ValueString() != "alice" || m.Slug.ValueString() != "my-repo" {
		t.Errorf("unexpected identity: %s %s %s", m.ID, m.Owner, m.Slug)
	}
	if m.Description.ValueString() != "hello" {
		t.Errorf("description = %s", m.Description)
	}
	if !m.PushedAt.IsNull() {
		t.Errorf("empty pushed_at should be null, got %s", m.PushedAt)
	}

	repo.Description = nil
	repo.PushedAt = "t3"
	if err := m.fromRemote(repo); err != nil {
		t.Fatal(err)
	}
	if !m.Description.IsNull() || m.PushedAt.ValueString() != "t3" {
		t.Errorf("description = %s, pushed_at = %s", m.Description, m.PushedAt)
	}

	repo.FullName = "bad"
	if err := m.fromRemote(repo); err == nil {
		t.Error("expected error for invalid full name")
	}
}

// TestRepositoryDataSourceConfigValidation runs offline: the config
// validators reject the lookup before the provider is ever configured.
func TestRepositoryDataSourceConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config string
		expect string
	}{
		{"both forms", `full_name = "alice/repo"
  name      = "repo"`, `(?s)full_name.*name`},
		{"full_name with owner", `full_name = "alice/repo"
  owner     = "alice"`, `(?s)full_name.*owner`},
		{"neither form", `owner = "alice"`, `Exactly one of these attributes`},
		{"nothing", ``, `Exactly one of these attributes`},
		{"full_name without slash", `full_name = "alice"`, `must be "owner/slug"`},
		{"full_name with extra segment", `full_name = "alice/repo/x"`, `must be "owner/slug"`},
		{"full_name missing owner", `full_name = "/repo"`, `must be "owner/slug"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      "data \"ghost_repository\" \"test\" {\n  " + tt.config + "\n}\n",
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tt.expect),
				}},
			})
		})
	}
}

func TestAccRepositoryDataSource(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc")

	// Attributes that must match the managed resource exactly.
	paired := []string{
		"id", "full_name", "owner", "name", "slug", "repo_id", "node_id", "description", "visibility", "private",
		"fork", "html_url", "clone_url", "ssh_url", "default_branch",
	}
	checkDS := func(ds string) resource.TestCheckFunc {
		var checks []resource.TestCheckFunc
		for _, attr := range paired {
			checks = append(checks, resource.TestCheckResourceAttrPair(ds, attr, "ghost_repository.test", attr))
		}
		for _, attr := range []string{"created_at", "updated_at"} {
			checks = append(checks, resource.TestCheckResourceAttrSet(ds, attr))
		}

		return resource.ComposeAggregateTestCheckFunc(checks...)
	}

	config := testAccRepositoryConfig(name, `description = "created by terraform acceptance tests"`) + `
data "ghost_repository" "by_full_name" {
  full_name = ghost_repository.test.full_name
}

data "ghost_repository" "by_name" {
  name = ghost_repository.test.slug
}

data "ghost_repository" "by_owner" {
  owner = ghost_repository.test.owner
  name  = ghost_repository.test.slug
}
`

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					checkDS("data.ghost_repository.by_full_name"),
					checkDS("data.ghost_repository.by_name"),
					checkDS("data.ghost_repository.by_owner"),
				),
			},
			{
				Config: testAccRepositoryConfig(name, "") + fmt.Sprintf(`
data "ghost_repository" "missing" {
  full_name = "%s/does-not-exist-%s"
}
`, "${ghost_repository.test.owner}", name),
				ExpectError: regexp.MustCompile(`Repository not found`),
			},
		},
	})
}
