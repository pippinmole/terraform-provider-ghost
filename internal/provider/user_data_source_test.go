package provider

import (
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

func TestUserDataSourceFromRemote(t *testing.T) {
	u := &client.User{
		Login: "alice", ID: 7, NodeID: "U7", Name: "", Type: "User", SiteAdmin: true,
		AvatarURL: "a", HTMLURL: "h", CreatedAt: "t1", UpdatedAt: "t2",
	}

	var m userDataSourceModel
	m.fromRemote(u)
	if m.ID.ValueString() != "alice" || m.Login.ValueString() != "alice" || m.UserID.ValueInt64() != 7 {
		t.Errorf("unexpected identity: %s %s %s", m.ID, m.Login, m.UserID)
	}
	if m.NodeID.ValueString() != "U7" || m.Type.ValueString() != "User" || !m.SiteAdmin.ValueBool() ||
		m.AvatarURL.ValueString() != "a" || m.HTMLURL.ValueString() != "h" ||
		m.CreatedAt.ValueString() != "t1" || m.UpdatedAt.ValueString() != "t2" {
		t.Errorf("unexpected attributes: %+v", m)
	}
	if !m.Name.IsNull() {
		t.Errorf("empty name should be null, got %s", m.Name)
	}

	u.Name = "Alice"
	m.fromRemote(u)
	if m.Name.ValueString() != "Alice" {
		t.Errorf("name = %s", m.Name)
	}
}

func TestAccUserDataSource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
data "ghost_user" "me" {}

data "ghost_user" "by_login" {
  login = data.ghost_user.me.login
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.ghost_user.me", "login"),
					resource.TestCheckResourceAttrPair("data.ghost_user.me", "id", "data.ghost_user.me", "login"),
					resource.TestCheckResourceAttrPair("data.ghost_user.by_login", "user_id", "data.ghost_user.me", "user_id"),
					resource.TestCheckResourceAttrPair("data.ghost_user.by_login", "node_id", "data.ghost_user.me", "node_id"),
					resource.TestCheckResourceAttrPair("data.ghost_user.by_login", "html_url", "data.ghost_user.me", "html_url"),
					resource.TestCheckResourceAttr("data.ghost_user.me", "type", "User"),
					resource.TestCheckResourceAttrSet("data.ghost_user.me", "created_at"),
				),
			},
			{
				Config: `
data "ghost_user" "missing" {
  login = "` + acctest.RandomWithPrefix("tf-acc-missing") + `"
}
`,
				ExpectError: regexp.MustCompile(`(?s)User not found.*case-sensitive`),
			},
		},
	})
}
