package provider

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

func TestRepositoriesDataSourceFromRemote(t *testing.T) {
	desc, pushed, parent := "Rockets", "2026-10-28T04:31:59.000Z", "repo_p"
	repos := []client.OwnerRepository{
		{ID: "repo_z", NumericID: 3, Name: "Zed", Slug: "zed", Visibility: "public", CreatedAt: "c3", UpdatedAt: "u3"},
		{ID: "repo_a", NumericID: 1, Name: "Alpha", Slug: "alpha", Description: &desc, Visibility: "private",
			ParentRepositoryID: &parent, LastPushedAt: &pushed, CreatedAt: "c1", UpdatedAt: "u1"},
		{ID: "repo_m", NumericID: 2, Name: "Mid", Slug: "mid", Visibility: "private", CreatedAt: "c2", UpdatedAt: "u2"},
	}
	input := slices.Clone(repos)

	m := repositoriesDataSourceModel{Owner: types.StringValue("PippinMole"), Visibility: types.StringNull()}
	if diags := m.fromRemote(repos); diags.HasError() {
		t.Fatalf("fromRemote: %v", diags)
	}
	if !slices.Equal(repos, input) {
		t.Errorf("fromRemote changed its input: %+v", repos)
	}
	if m.ID.ValueString() != "pippinmole" {
		t.Errorf("id = %s, want the owner in lower case", m.ID)
	}

	want := []string{
		`{"created_at":"c1","description":"Rockets","fork":true,"full_name":"pippinmole/alpha","id":"repo_a","name":"Alpha","numeric_id":1,` +
			`"private":true,"pushed_at":"2026-10-28T04:31:59.000Z","slug":"alpha","updated_at":"u1","visibility":"private"}`,
		`{"created_at":"c2","description":<null>,"fork":false,"full_name":"pippinmole/mid","id":"repo_m","name":"Mid","numeric_id":2,` +
			`"private":true,"pushed_at":<null>,"slug":"mid","updated_at":"u2","visibility":"private"}`,
		`{"created_at":"c3","description":<null>,"fork":false,"full_name":"pippinmole/zed","id":"repo_z","name":"Zed","numeric_id":3,` +
			`"private":false,"pushed_at":<null>,"slug":"zed","updated_at":"u3","visibility":"public"}`,
	}
	elems := m.Repositories.Elements()
	if len(elems) != len(want) {
		t.Fatalf("repositories = %s", m.Repositories)
	}
	for i, e := range elems {
		if got := e.String(); got != want[i] {
			t.Errorf("repositories[%d] = %s\nwant %s", i, got, want[i])
		}
	}
	if got := m.FullNames.String(); got != `["pippinmole/alpha","pippinmole/mid","pippinmole/zed"]` {
		t.Errorf("full_names = %s", got)
	}
	if got := m.Slugs.String(); got != `["alpha","mid","zed"]` {
		t.Errorf("slugs = %s", got)
	}

	for vis, wantSlugs := range map[string]string{"private": `["alpha","mid"]`, "public": `["zed"]`} {
		m.Visibility = types.StringValue(vis)
		if diags := m.fromRemote(repos); diags.HasError() {
			t.Fatalf("fromRemote: %v", diags)
		}
		if got := m.Slugs.String(); got != wantSlugs {
			t.Errorf("visibility %s: slugs = %s, want %s", vis, got, wantSlugs)
		}
		if len(m.Repositories.Elements()) != len(m.FullNames.Elements()) {
			t.Errorf("visibility %s: repositories and full_names differ in length", vis)
		}
	}

	// No repositories gives empty lists, not null.
	m.Visibility = types.StringNull()
	for _, in := range [][]client.OwnerRepository{nil, {}} {
		if diags := m.fromRemote(in); diags.HasError() {
			t.Fatalf("fromRemote: %v", diags)
		}
		for name, l := range map[string]types.List{"repositories": m.Repositories, "full_names": m.FullNames, "slugs": m.Slugs} {
			if l.IsNull() || len(l.Elements()) != 0 {
				t.Errorf("%s = %s, want an empty list", name, l)
			}
		}
	}
}

func TestRepositoriesDataSourceConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config string
		expect string
	}{
		{"no owner", ``, `(?s)"owner" is required`},
		{"empty owner", `owner = ""`, `(?s)owner.*at least 1`},
		{"owner with slash", `owner = "alice/repo"`, `(?s)without a slash`},
		{"empty query", `owner = "alice"` + "\n" + `query = ""`, `(?s)query.*at least 1`},
		{"bad visibility", `owner = "alice"` + "\n" + `visibility = "internal"`, `(?s)visibility.*value must be one of`},
		{"computed id", `owner = "alice"` + "\n" + `id = "x"`, `(?s)Invalid Configuration for Read-Only Attribute`},
		{"computed repositories", `owner = "alice"` + "\n" + `repositories = []`, `(?s)Invalid Configuration for Read-Only Attribute`},
		{"computed full_names", `owner = "alice"` + "\n" + `full_names = []`, `(?s)Invalid Configuration for Read-Only Attribute`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      "data \"ghost_repositories\" \"test\" {\n" + tt.config + "\n}\n",
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tt.expect),
				}},
			})
		})
	}
}

// testAccRepositoriesPreCheck skips the test unless TF_ACC is set, before
// it makes any API calls of its own.
func testAccRepositoriesPreCheck(t *testing.T) {
	t.Helper()

	if os.Getenv(resource.EnvTfAcc) == "" {
		t.Skip("set TF_ACC to run acceptance tests")
	}
	testAccPreCheck(t)
}

// testAccSmallRepositoryPages makes the provider list repositories two at
// a time until the test ends, so a handful of repositories spans several
// pages. Tests that call it must not run in parallel.
func testAccSmallRepositoryPages(t *testing.T) {
	t.Helper()

	old := client.OwnerRepositoryPageSize
	client.OwnerRepositoryPageSize = 2
	t.Cleanup(func() { client.OwnerRepositoryPageSize = old })
}

// testAccRepositoriesConfig declares one repository per name, as
// ghost_repository.r[i], owned by owner (the token's user when empty) and
// private where private[i] is true. The repository at index 2 gets a
// description containing "Needle In Source". Then come the data sources in
// extra, which should depend on ghost_repository.r.
func testAccRepositoriesConfig(owner string, names []string, private []bool, extra string) string {
	quoted := make([]string, len(names))
	vis := make([]string, len(names))
	for i, n := range names {
		quoted[i] = strconv.Quote(n)
		vis[i] = `"public"`
		if private[i] {
			vis[i] = `"private"`
		}
	}
	ownerArg := ""
	if owner != "" {
		ownerArg = fmt.Sprintf("owner = %q", owner)
	}

	return fmt.Sprintf(`
locals {
  names      = [%s]
  visibility = [%s]
}

resource "ghost_repository" "r" {
  count       = length(local.names)
  name        = local.names[count.index]
  visibility  = local.visibility[count.index]
  description = count.index == 2 ? "A Needle In Source ${local.names[count.index]}" : null
  %s
}
%s`, strings.Join(quoted, ", "), strings.Join(vis, ", "), ownerArg, extra)
}

// testAccRepositoriesDataBlock is a ghost_repositories data source named
// name with the given arguments, read after the repositories exist.
func testAccRepositoriesDataBlock(name, args string) string {
	return fmt.Sprintf(`
data "ghost_repositories" %q {
  %s
  depends_on = [ghost_repository.r]
}
`, name, args)
}

// testAccCheckListedRepositories checks that the repositories the data
// source at address lists with slugs starting with prefix are exactly want
// (slug → visibility), each once, in slug order, with full names under
// owner and matching full_names and slugs entries. Other repositories the
// owner has are ignored.
func testAccCheckListedRepositories(address, owner, prefix string, want map[string]string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[address]
		if !ok {
			return fmt.Errorf("%s not found in state", address)
		}
		a := rs.Primary.Attributes
		n, err := strconv.Atoi(a["repositories.#"])
		if err != nil {
			return fmt.Errorf("%s: repositories.# = %q", address, a["repositories.#"])
		}
		if a["full_names.#"] != a["repositories.#"] || a["slugs.#"] != a["repositories.#"] {
			return fmt.Errorf("%s: %s repositories, %s full_names, %s slugs", address, a["repositories.#"], a["full_names.#"], a["slugs.#"])
		}

		var got []string
		for i := range n {
			p := fmt.Sprintf("repositories.%d.", i)
			slug := a[p+"slug"]
			if !strings.HasPrefix(slug, prefix) {
				continue
			}
			got = append(got, slug)
			vis, ok := want[slug]
			if !ok {
				return fmt.Errorf("%s: unexpected repository %s", address, slug)
			}
			fullName := strings.ToLower(owner) + "/" + slug
			checks := map[string]string{
				p + "visibility":                vis,
				p + "private":                   strconv.FormatBool(vis == "private"),
				p + "full_name":                 fullName,
				p + "name":                      slug,
				p + "fork":                      "false",
				fmt.Sprintf("full_names.%d", i): fullName,
				fmt.Sprintf("slugs.%d", i):      slug,
			}
			for k, v := range checks {
				if a[k] != v {
					return fmt.Errorf("%s: %s = %q, want %q", address, k, a[k], v)
				}
			}
			if !strings.HasPrefix(a[p+"id"], "repo_") || a[p+"numeric_id"] == "" || a[p+"numeric_id"] == "0" ||
				a[p+"created_at"] == "" || a[p+"updated_at"] == "" {
				return fmt.Errorf("%s: incomplete repository %s: %v", address, slug, a)
			}
		}

		wantSlugs := make([]string, 0, len(want))
		for slug := range want {
			wantSlugs = append(wantSlugs, slug)
		}
		slices.Sort(wantSlugs)
		if !slices.Equal(got, wantSlugs) {
			return fmt.Errorf("%s: listed %v, want %v (in that order)", address, got, wantSlugs)
		}

		return nil
	}
}

// testAccCheckRepositoriesMatchAPI checks that the repositories the data
// source at address lists are the ones Ghost lists for owner, so private
// repositories are compared with the raw API too.
func testAccCheckRepositoriesMatchAPI(address, owner string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		c, err := testAccClient()
		if err != nil {
			return err
		}
		repos, err := c.ListOwnerRepositories(context.Background(), owner, "")
		if err != nil {
			return err
		}
		var want []string
		for _, r := range repos {
			want = append(want, r.ID+" "+r.Visibility)
		}
		slices.Sort(want)

		a := s.RootModule().Resources[address].Primary.Attributes
		n, _ := strconv.Atoi(a["repositories.#"])
		var got []string
		for i := range n {
			got = append(got, a[fmt.Sprintf("repositories.%d.id", i)]+" "+a[fmt.Sprintf("repositories.%d.visibility", i)])
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			return fmt.Errorf("%s lists %v, the API %v", address, got, want)
		}

		return nil
	}
}

func TestAccRepositoriesDataSource(t *testing.T) {
	testAccRepositoriesPreCheck(t)
	testAccSmallRepositoryPages(t)
	me := testAccTokenLogin(t)
	prefix := acctest.RandomWithPrefix("tf-acc-repos")
	names := []string{prefix + "-a", prefix + "-b", prefix + "-c", prefix + "-d", prefix + "-e"}
	a, b, c, d, e := names[0], names[1], names[2], names[3], names[4]

	data := testAccRepositoriesDataBlock("all", fmt.Sprintf("owner = %q", me)) +
		testAccRepositoriesDataBlock("upper", fmt.Sprintf("owner = %q", strings.ToUpper(me))) +
		testAccRepositoriesDataBlock("pub", fmt.Sprintf("owner = %q\n  visibility = \"public\"", me)) +
		testAccRepositoriesDataBlock("priv", fmt.Sprintf("owner = %q\n  visibility = \"private\"", me)) +
		testAccRepositoriesDataBlock("qname", fmt.Sprintf("owner = %q\n  query = %q", me, strings.ToUpper(a))) +
		testAccRepositoriesDataBlock("qdesc", fmt.Sprintf("owner = %q\n  query = \"needle in source %s\"", me, prefix)) +
		testAccRepositoriesDataBlock("qpriv", fmt.Sprintf("owner = %q\n  query = %q\n  visibility = \"private\"", me, prefix)) +
		testAccRepositoriesDataBlock("none", fmt.Sprintf("owner = %q\n  query = %q", me, acctest.RandomWithPrefix("tf-acc-nomatch")))

	step1 := testAccRepositoriesConfig("", names, []bool{false, true, false, true, false}, data)
	step2 := testAccRepositoriesConfig("", names, []bool{false, false, false, true, false}, data)

	all := map[string]string{a: "public", b: "private", c: "public", d: "private", e: "public"}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{
				Config: step1,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.ghost_repositories.all", "id", strings.ToLower(me)),
					resource.TestCheckResourceAttr("data.ghost_repositories.upper", "id", strings.ToLower(me)),
					// Five repositories at two per page: the list spans pages.
					testAccCheckListedRepositories("data.ghost_repositories.all", me, prefix, all),
					testAccCheckListedRepositories("data.ghost_repositories.upper", me, prefix, all),
					testAccCheckRepositoriesMatchAPI("data.ghost_repositories.all", me),
					testAccCheckListedRepositories("data.ghost_repositories.pub", me, prefix, map[string]string{a: "public", c: "public", e: "public"}),
					testAccCheckListedRepositories("data.ghost_repositories.priv", me, prefix, map[string]string{b: "private", d: "private"}),
					// The upper-case name of -a matches only -a: no other name contains it.
					testAccCheckListedRepositories("data.ghost_repositories.qname", me, prefix, map[string]string{a: "public"}),
					resource.TestCheckResourceAttr("data.ghost_repositories.qname", "repositories.#", "1"),
					resource.TestCheckResourceAttrPair("data.ghost_repositories.qname", "full_names.0", "ghost_repository.r.0", "full_name"),
					resource.TestCheckResourceAttrPair("data.ghost_repositories.qname", "repositories.0.numeric_id", "ghost_repository.r.0", "repo_id"),
					resource.TestCheckNoResourceAttr("data.ghost_repositories.qname", "repositories.0.description"),
					// Matched on the description, ignoring case.
					testAccCheckListedRepositories("data.ghost_repositories.qdesc", me, prefix, map[string]string{c: "public"}),
					resource.TestCheckResourceAttr("data.ghost_repositories.qdesc", "repositories.#", "1"),
					resource.TestCheckResourceAttr("data.ghost_repositories.qdesc", "repositories.0.description", "A Needle In Source "+c),
					testAccCheckListedRepositories("data.ghost_repositories.qpriv", me, prefix, map[string]string{b: "private", d: "private"}),
					resource.TestCheckResourceAttr("data.ghost_repositories.qpriv", "repositories.#", "2"),
					resource.TestCheckResourceAttr("data.ghost_repositories.none", "repositories.#", "0"),
					resource.TestCheckResourceAttr("data.ghost_repositories.none", "full_names.#", "0"),
					resource.TestCheckResourceAttr("data.ghost_repositories.none", "slugs.#", "0"),
				),
			},
			{
				// Make -b public: the data sources are read again.
				Config: step2,
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckListedRepositories("data.ghost_repositories.pub", me, prefix, map[string]string{a: "public", b: "public", c: "public", e: "public"}),
					testAccCheckListedRepositories("data.ghost_repositories.priv", me, prefix, map[string]string{d: "private"}),
					testAccCheckListedRepositories("data.ghost_repositories.qpriv", me, prefix, map[string]string{d: "private"}),
					testAccCheckRepositoriesMatchAPI("data.ghost_repositories.all", me),
				),
			},
			{
				Config:   step2,
				PlanOnly: true,
			},
		},
	})
}

func TestAccRepositoriesDataSource_organization(t *testing.T) {
	org, _ := testAccOrganizationFixture(t)
	testAccSmallRepositoryPages(t)
	prefix := acctest.RandomWithPrefix("tf-acc-repos")
	names := []string{prefix + "-a", prefix + "-b", prefix + "-c"}

	data := testAccRepositoriesDataBlock("all", fmt.Sprintf("owner = %q", org)) +
		testAccRepositoriesDataBlock("upper", fmt.Sprintf("owner = %q", strings.ToUpper(org))) +
		testAccRepositoriesDataBlock("priv", fmt.Sprintf("owner = %q\n  visibility = \"private\"", org))

	all := map[string]string{names[0]: "public", names[1]: "private", names[2]: "public"}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccRepositoriesConfig(org, names, []bool{false, true, false}, data),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.ghost_repositories.all", "id", strings.ToLower(org)),
					testAccCheckListedRepositories("data.ghost_repositories.all", org, prefix, all),
					testAccCheckListedRepositories("data.ghost_repositories.upper", org, prefix, all),
					testAccCheckRepositoriesMatchAPI("data.ghost_repositories.all", org),
					testAccCheckListedRepositories("data.ghost_repositories.priv", org, prefix, map[string]string{names[1]: "private"}),
				),
			},
		},
	})
}

func TestAccRepositoriesDataSource_errors(t *testing.T) {
	testAccRepositoriesPreCheck(t)
	missing := acctest.RandomWithPrefix("tf-acc-missing")
	config := func(owner string) string {
		return fmt.Sprintf("data \"ghost_repositories\" \"test\" {\n  owner = %q\n}\n", owner)
	}
	expect := func(owner string) *regexp.Regexp {
		return regexp.MustCompile(`(?s)Owner not found.*"` + regexp.QuoteMeta(owner) + `"`)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config(missing), ExpectError: expect(missing)},
			{Config: config(strings.ToUpper(missing)), ExpectError: expect(strings.ToUpper(missing))},
		},
	})
}
