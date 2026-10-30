package provider

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

// testAccReleaseAssetConfig returns a public auto_init repository with a
// published release ghost_release.test (v1), plus extra.
func testAccReleaseAssetConfig(repoName, extra string) string {
	return testAccRepositoryConfig(repoName, `auto_init = true
  visibility = "public"`) + testAccReleaseBlock("test", `tag_name = "v1"`) + extra
}

// testAccReleaseAssetBlock returns a ghost_release_asset.<name> in
// ghost_release.test, with attrs as its other arguments. attrs may override
// repository and release_id.
func testAccReleaseAssetBlock(name, attrs string) string {
	defaults := ""
	if !strings.Contains(attrs, "repository ") && !strings.Contains(attrs, "repository=") {
		defaults += "  repository = ghost_repository.test.full_name\n"
	}
	if !strings.Contains(attrs, "release_id") {
		defaults += "  release_id = ghost_release.test.release_id\n"
	}

	return fmt.Sprintf(`
resource "ghost_release_asset" %q {
%s  %s
}
`, name, defaults, attrs)
}

// testAccWriteFile writes b to name in dir and returns its path.
func testAccWriteFile(t *testing.T, dir, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}

	return p
}

func testAccRandomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	b[0], b[1] = 0, 0xff // a NUL and invalid UTF-8

	return b
}

// testAccGetReleaseAsset reads the asset at address from Ghost.
func testAccGetReleaseAsset(s *terraform.State, address string) (*client.Client, *client.ReleaseAsset, error) {
	rs, ok := s.RootModule().Resources[address]
	if !ok {
		return nil, nil, fmt.Errorf("%s not found in state", address)
	}
	owner, repo, err := client.SplitFullName(rs.Primary.Attributes["repository"])
	if err != nil {
		return nil, nil, err
	}
	id, err := strconv.ParseInt(rs.Primary.Attributes["asset_id"], 10, 64)
	if err != nil {
		return nil, nil, err
	}
	c, err := testAccClient()
	if err != nil {
		return nil, nil, err
	}
	asset, err := c.GetReleaseAsset(context.Background(), owner, repo, id)

	return c, asset, err
}

// testAccCheckReleaseAsset checks the asset at address matches its state, and
// its content is want.
func testAccCheckReleaseAsset(address string, want []byte) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		c, asset, err := testAccGetReleaseAsset(s, address)
		if err != nil {
			return err
		}
		a := s.RootModule().Resources[address].Primary.Attributes
		label := ""
		if asset.Label != nil {
			label = *asset.Label
		}
		if asset.Name != a["name"] || label != a["label"] || asset.ContentType != a["content_type"] ||
			strconv.FormatInt(asset.Size, 10) != a["size"] || asset.NodeID != a["node_id"] ||
			asset.BrowserDownloadURL != a["browser_download_url"] {
			return fmt.Errorf("remote asset %+v doesn't match state %v", asset, a)
		}
		if want == nil {
			return nil
		}
		owner, repo, _ := client.SplitFullName(a["repository"])
		var buf bytes.Buffer
		if err := c.DownloadReleaseAsset(context.Background(), owner, repo, asset.ID, &buf); err != nil {
			return err
		}
		sum := sha256.Sum256(want)
		if !bytes.Equal(buf.Bytes(), want) || a["content_sha256"] != hex.EncodeToString(sum[:]) || a["size"] != strconv.Itoa(len(want)) {
			return fmt.Errorf("%s: downloaded %d bytes, want %d; content_sha256 %s", address, buf.Len(), len(want), a["content_sha256"])
		}

		return nil
	}
}

// testAccCaptureAssetID stores the asset_id of address in id.
func testAccCaptureAssetID(address string, id *int64) resource.TestCheckFunc {
	return resource.TestCheckResourceAttrWith(address, "asset_id", func(v string) error {
		n, err := strconv.ParseInt(v, 10, 64)
		*id = n
		return err
	})
}

// testAccCheckReleaseAssetGone checks the asset id is gone from repoName.
func testAccCheckReleaseAssetGone(t *testing.T, repoName string, id *int64) resource.TestCheckFunc {
	return func(*terraform.State) error {
		c, repo := testAccRepository(t, repoName)
		owner, slug, _ := client.SplitFullName(repo.FullName)
		if _, err := c.GetReleaseAsset(context.Background(), owner, slug, *id); !client.IsNotFound(err) {
			return fmt.Errorf("asset %d still exists (err %v)", *id, err)
		}
		return nil
	}
}

func testAccReleaseAssetImportID(address string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[address]
		if !ok {
			return "", fmt.Errorf("%s not found in state", address)
		}
		return rs.Primary.Attributes["repository"] + ":" + rs.Primary.Attributes["asset_id"], nil
	}
}

// testAccCheckReleaseAssetDestroy checks every asset in state is gone.
func testAccCheckReleaseAssetDestroy(s *terraform.State) error {
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "ghost_release_asset" {
			continue
		}
		owner, repo, err := client.SplitFullName(rs.Primary.Attributes["repository"])
		if err != nil {
			return err
		}
		id, _ := strconv.ParseInt(rs.Primary.Attributes["asset_id"], 10, 64)
		c, err := testAccClient()
		if err != nil {
			return err
		}
		if _, err := c.GetReleaseAsset(context.Background(), owner, repo, id); !client.IsNotFound(err) {
			return fmt.Errorf("release asset %s still exists (err %v)", rs.Primary.ID, err)
		}
	}

	return nil
}

// All three sources, every argument, import, and in-place changes.
func TestAccReleaseAssetResource(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc-asset")
	dir := t.TempDir()
	bin := testAccRandomBytes(t, 1024)
	binPath := testAccWriteFile(t, dir, "tool.bin", bin)
	data := testAccRandomBytes(t, 300)
	dataB64 := base64.StdEncoding.EncodeToString(data)
	text := "héllo\n"
	textPath := testAccWriteFile(t, dir, "notes.txt", []byte(text))
	dataPath := testAccWriteFile(t, dir, "data.bin", data)

	const binAddr, textAddr, b64Addr = "ghost_release_asset.bin", "ghost_release_asset.text", "ghost_release_asset.b64"
	sameBin := statecheck.CompareValue(compare.ValuesSame())
	sameText := statecheck.CompareValue(compare.ValuesSame())
	sameB64 := statecheck.CompareValue(compare.ValuesSame())
	pinned := func(withB64 bool) []statecheck.StateCheck {
		c := []statecheck.StateCheck{
			sameBin.AddStateValue(binAddr, tfjsonpath.New("asset_id")),
			sameText.AddStateValue(textAddr, tfjsonpath.New("asset_id")),
		}
		if withB64 {
			c = append(c, sameB64.AddStateValue(b64Addr, tfjsonpath.New("asset_id")))
		}
		return c
	}

	step1 := testAccReleaseAssetConfig(repoName,
		testAccReleaseAssetBlock("bin", fmt.Sprintf(`file = %q
  name = "tool linux.bin"
  label = "Linux build"
  content_type = "application/x-executable"`, binPath))+
			testAccReleaseAssetBlock("text", fmt.Sprintf(`content = %q
  name = "notes.txt"
  content_type = "text/plain"`, text))+
			testAccReleaseAssetBlock("b64", fmt.Sprintf(`content_base64 = %q
  name = "data.bin"`, dataB64)))

	step3 := testAccReleaseAssetConfig(repoName,
		testAccReleaseAssetBlock("bin", fmt.Sprintf(`file = %q
  name = "tool-linux.bin"
  label = "Linux (x86_64)"
  content_type = "application/x-executable"`, binPath))+
			testAccReleaseAssetBlock("text", fmt.Sprintf(`content = %q
  name = "notes.txt"
  label = ""
  content_type = "text/plain"`, text))+
			testAccReleaseAssetBlock("b64", fmt.Sprintf(`content_base64 = %q
  name = "data.bin"`, dataB64)))

	step4 := testAccReleaseAssetConfig(repoName,
		testAccReleaseAssetBlock("bin", fmt.Sprintf(`file = %q
  name = "tool-linux.bin"
  content_type = "application/x-executable"`, binPath))+
			testAccReleaseAssetBlock("text", fmt.Sprintf(`file = %q
  name = "notes.txt"
  content_type = "text/plain"`, textPath))+
			testAccReleaseAssetBlock("b64", fmt.Sprintf(`file = %q
  name = "data.bin"`, dataPath)))

	step5 := testAccReleaseAssetConfig(repoName,
		testAccReleaseAssetBlock("bin", fmt.Sprintf(`file = %q
  name = "tool-linux.bin"
  content_type = "application/x-executable"`, binPath))+
			testAccReleaseAssetBlock("text", fmt.Sprintf(`file = %q
  name = "notes.txt"
  content_type = "text/plain"`, textPath)))

	var b64ID int64
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckReleaseAssetDestroy,
		Steps: []resource.TestStep{
			{
				Config: step1,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: append(pinned(true),
					statecheck.CompareValuePairs(binAddr, tfjsonpath.New("repository_id"), "ghost_repository.test", tfjsonpath.New("repo_id"), compare.ValuesSame()),
					statecheck.CompareValuePairs(binAddr, tfjsonpath.New("release_id"), "ghost_release.test", tfjsonpath.New("release_id"), compare.ValuesSame()),
				),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckReleaseAsset(binAddr, bin),
					testAccCheckReleaseAsset(textAddr, []byte(text)),
					testAccCheckReleaseAsset(b64Addr, data),
					testAccCaptureAssetID(b64Addr, &b64ID),
					resource.TestMatchResourceAttr(binAddr, "id", regexp.MustCompile(`^[^/]+/`+regexp.QuoteMeta(repoName)+`:[0-9]+$`)),
					resource.TestMatchResourceAttr(binAddr, "node_id", regexp.MustCompile(`^RA_`)),
					resource.TestMatchResourceAttr(binAddr, "browser_download_url", regexp.MustCompile(`/`+regexp.QuoteMeta(repoName)+`/releases/download/v1/tool%20linux\.bin$`)),
					resource.TestCheckResourceAttr(binAddr, "label", "Linux build"),
					resource.TestCheckResourceAttr(b64Addr, "content_type", "application/octet-stream"),
					resource.TestCheckNoResourceAttr(b64Addr, "label"),
					resource.TestCheckResourceAttr(b64Addr, "download_count", "0"),
					resource.TestCheckResourceAttrSet(binAddr, "url"),
					resource.TestCheckResourceAttrSet(binAddr, "created_at"),
					resource.TestCheckResourceAttrSet(binAddr, "updated_at"),
				),
			},
			{
				ResourceName:            binAddr,
				ImportState:             true,
				ImportStateIdFunc:       testAccReleaseAssetImportID(binAddr),
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"file", "download_count", "updated_at"},
			},
			{
				ResourceName:            b64Addr,
				ImportState:             true,
				ImportStateIdFunc:       testAccReleaseAssetImportID(b64Addr),
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"content_base64", "download_count", "updated_at"},
			},
			{
				Config: step3,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(binAddr, plancheck.ResourceActionUpdate),
						plancheck.ExpectResourceAction(textAddr, plancheck.ResourceActionUpdate),
						plancheck.ExpectResourceAction(b64Addr, plancheck.ResourceActionNoop),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: pinned(true),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckReleaseAsset(binAddr, nil),
					testAccCheckReleaseAsset(textAddr, nil),
					resource.TestMatchResourceAttr(binAddr, "browser_download_url", regexp.MustCompile(`/tool-linux\.bin$`)),
					resource.TestCheckResourceAttr(binAddr, "label", "Linux (x86_64)"),
					resource.TestCheckResourceAttr(textAddr, "label", ""),
				),
			},
			{
				// Clearing the label, and moving content to files with the
				// same bytes, are in place.
				Config: step4,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(binAddr, plancheck.ResourceActionUpdate),
						plancheck.ExpectResourceAction(textAddr, plancheck.ResourceActionUpdate),
						plancheck.ExpectResourceAction(b64Addr, plancheck.ResourceActionUpdate),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: pinned(true),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckReleaseAsset(binAddr, nil),
					resource.TestCheckNoResourceAttr(binAddr, "label"),
				),
			},
			{
				Config: step5,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(b64Addr, plancheck.ResourceActionDestroy)},
				},
				ConfigStateChecks: pinned(false),
				Check:             testAccCheckReleaseAssetGone(t, repoName, &b64ID),
			},
		},
	})
}

// Changing the content, content_type or release replaces the asset; a file
// generated during apply is read then.
func TestAccReleaseAssetResource_replace(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc-asset")
	dir := t.TempDir()
	p := testAccWriteFile(t, dir, "x.txt", []byte("one"))
	generated := filepath.Join(dir, "generated.txt")
	const addr = "ghost_release_asset.test"

	draft := testAccReleaseBlock("draft", `tag_name = "v2"
  draft = true`)
	config := func(attrs string) string {
		return testAccReleaseAssetConfig(repoName, draft+testAccReleaseAssetBlock("test", attrs))
	}
	fileAttrs := fmt.Sprintf(`file = %q
  name = "x.txt"`, p)

	var first, previous int64
	replaced := resource.ConfigPlanChecks{
		PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
		PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
	}
	differs := statecheck.CompareValue(compare.ValuesDiffer())
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckReleaseAssetDestroy,
		Steps: []resource.TestStep{
			{
				Config:            config(fileAttrs + "\n  content_type = \"text/plain\""),
				ConfigStateChecks: []statecheck.StateCheck{differs.AddStateValue(addr, tfjsonpath.New("asset_id"))},
				Check:             resource.ComposeAggregateTestCheckFunc(testAccCaptureAssetID(addr, &first), testAccCheckReleaseAsset(addr, []byte("one"))),
			},
			{
				PreConfig: func() {
					previous = first
					testAccWriteFile(t, dir, "x.txt", []byte("two"))
				},
				Config:            config(fileAttrs + "\n  content_type = \"text/plain\""),
				ConfigPlanChecks:  replaced,
				ConfigStateChecks: []statecheck.StateCheck{differs.AddStateValue(addr, tfjsonpath.New("asset_id"))},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckReleaseAsset(addr, []byte("two")),
					testAccCheckReleaseAssetGone(t, repoName, &previous),
					testAccCaptureAssetID(addr, &first),
				),
			},
			{
				Config:            config(fileAttrs + "\n  content_type = \"text/markdown\""),
				ConfigPlanChecks:  replaced,
				ConfigStateChecks: []statecheck.StateCheck{differs.AddStateValue(addr, tfjsonpath.New("asset_id"))},
				Check:             resource.ComposeAggregateTestCheckFunc(testAccCheckReleaseAsset(addr, nil), resource.TestCheckResourceAttr(addr, "content_type", "text/markdown")),
			},
			{
				Config:            config(fileAttrs + "\n  content_type = \"text/markdown\"\n  release_id = ghost_release.draft.release_id"),
				ConfigPlanChecks:  replaced,
				ConfigStateChecks: []statecheck.StateCheck{differs.AddStateValue(addr, tfjsonpath.New("asset_id"))},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckReleaseAsset(addr, []byte("two")),
					resource.TestCheckResourceAttrPair(addr, "release_id", "ghost_release.draft", "release_id"),
				),
			},
			{
				// The file doesn't exist at plan time.
				Config: testAccReleaseAssetConfig(repoName, draft+fmt.Sprintf(`
resource "terraform_data" "gen" {
  provisioner "local-exec" {
    command = "printf three > %s"
  }
}
`, generated)+testAccReleaseAssetBlock("test", fmt.Sprintf(`file = %q
  name = "x.txt"
  depends_on = [terraform_data.gen]`, generated))),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate),
						plancheck.ExpectUnknownValue(addr, tfjsonpath.New("content_sha256")),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: testAccCheckReleaseAsset(addr, []byte("three")),
			},
			{
				// A missing file with an unchanged configuration keeps the
				// asset.
				PreConfig: func() { _ = os.Remove(generated) },
				Config: testAccReleaseAssetConfig(repoName, draft+`
resource "terraform_data" "gen" {}
`+testAccReleaseAssetBlock("test", fmt.Sprintf(`file = %q
  name = "x.txt"`, generated))),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionNoop)},
				},
			},
		},
	})
}

// Changes made outside Terraform are put back.
func TestAccReleaseAssetResource_drift(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc-asset")
	const addr = "ghost_release_asset.test"
	config := testAccReleaseAssetConfig(repoName, testAccReleaseAssetBlock("test", `content = "a"
  name = "d.txt"`))

	var assetID, releaseID int64
	ghost := func(f func(c *client.Client, owner, repo string) error) func() {
		return func() {
			c, r := testAccRepository(t, repoName)
			owner, slug, _ := client.SplitFullName(r.FullName)
			if err := f(c, owner, slug); err != nil {
				t.Fatal(err)
			}
		}
	}
	same := statecheck.CompareValue(compare.ValuesSame())
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckReleaseAssetDestroy,
		Steps: []resource.TestStep{
			{
				Config:            config,
				ConfigStateChecks: []statecheck.StateCheck{same.AddStateValue(addr, tfjsonpath.New("asset_id"))},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCaptureAssetID(addr, &assetID),
					resource.TestCheckResourceAttrWith(addr, "release_id", func(v string) error {
						var err error
						releaseID, err = strconv.ParseInt(v, 10, 64)
						return err
					}),
				),
			},
			{
				PreConfig: ghost(func(c *client.Client, owner, repo string) error {
					name, label := "renamed.txt", "drifted"
					_, err := c.UpdateReleaseAsset(context.Background(), owner, repo, assetID, client.UpdateReleaseAssetRequest{Name: &name, Label: &label})
					return err
				}),
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{same.AddStateValue(addr, tfjsonpath.New("asset_id"))},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckReleaseAsset(addr, nil),
					resource.TestCheckResourceAttr(addr, "name", "d.txt"),
					resource.TestCheckNoResourceAttr(addr, "label"),
				),
			},
			{
				PreConfig: ghost(func(c *client.Client, owner, repo string) error {
					return c.DeleteReleaseAsset(context.Background(), owner, repo, assetID)
				}),
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionCreate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(testAccCheckReleaseAsset(addr, []byte("a")), testAccCaptureAssetID(addr, &assetID)),
			},
			{
				// Deleting the release deletes its assets.
				PreConfig: func() { testAccDeleteRelease(t, repoName, releaseID) },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("ghost_release.test", plancheck.ResourceActionCreate),
						plancheck.ExpectResourceAction(addr, plancheck.ResourceActionCreate),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: testAccCheckReleaseAsset(addr, []byte("a")),
			},
		},
	})
}

// Renaming the repository is in place; moving to another repository replaces.
func TestAccReleaseAssetResource_repository(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc-asset")
	otherName := repoName + "-other"
	const addr = "ghost_release_asset.test"
	config := func(name, assetAttrs string) string {
		return testAccRepositoryConfig(name, `auto_init = true`) +
			testAccReleaseBlock("test", `tag_name = "v1"`) + fmt.Sprintf(`
resource "ghost_repository" "other" {
  name      = %q
  auto_init = true
}

resource "ghost_release" "other" {
  repository = ghost_repository.other.full_name
  tag_name   = "v1"
}
`, otherName) + testAccReleaseAssetBlock("test", `content = "r"
  name = "r.txt"
  `+assetAttrs)
	}

	same := statecheck.CompareValue(compare.ValuesSame())
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckReleaseAssetDestroy,
		Steps: []resource.TestStep{
			{
				Config:            config(repoName, ""),
				ConfigStateChecks: []statecheck.StateCheck{same.AddStateValue(addr, tfjsonpath.New("asset_id"))},
			},
			{
				Config: config(repoName+"-renamed", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					same.AddStateValue(addr, tfjsonpath.New("asset_id")),
					statecheck.CompareValuePairs(addr, tfjsonpath.New("repository"), "ghost_repository.test", tfjsonpath.New("full_name"), compare.ValuesSame()),
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckReleaseAsset(addr, []byte("r")),
					resource.TestMatchResourceAttr(addr, "id", regexp.MustCompile(`/`+regexp.QuoteMeta(repoName)+`-renamed:[0-9]+$`)),
					resource.TestMatchResourceAttr(addr, "browser_download_url", regexp.MustCompile(`/`+regexp.QuoteMeta(url.PathEscape(repoName))+`-renamed/releases/download/v1/r\.txt$`)),
				),
			},
			{
				Config: config(repoName+"-renamed", `repository = ghost_repository.other.full_name
  release_id = ghost_release.other.release_id`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckReleaseAsset(addr, []byte("r")),
					resource.TestCheckResourceAttrPair(addr, "repository_id", "ghost_repository.other", "repo_id"),
				),
			},
			{
				ResourceName:            addr,
				ImportState:             true,
				ImportStateIdFunc:       testAccReleaseAssetImportID(addr),
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"content", "download_count", "updated_at"},
			},
		},
	})
}

func TestAccReleaseAssetResource_organization(t *testing.T) {
	org := os.Getenv("GHOST_TEST_ORGANIZATION")
	if org == "" {
		t.Skip("set GHOST_TEST_ORGANIZATION to test assets of organization repositories")
	}
	repoName := acctest.RandomWithPrefix("tf-acc-asset")
	const addr = "ghost_release_asset.test"
	data := []byte{0, 1, 2, 0xff}
	config := func(name string) string {
		return fmt.Sprintf(`
resource "ghost_repository" "test" {
  owner      = %q
  name       = %q
  auto_init  = true
  visibility = "public"
}
`, org, repoName) + testAccReleaseBlock("test", `tag_name = "v1"`) + testAccReleaseAssetBlock("test", fmt.Sprintf(`content_base64 = %q
  name  = %q
  label = "Org build"`, base64.StdEncoding.EncodeToString(data), name))
	}

	same := statecheck.CompareValue(compare.ValuesSame())
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckReleaseAssetDestroy,
		Steps: []resource.TestStep{
			{
				Config:            config("org.bin"),
				ConfigStateChecks: []statecheck.StateCheck{same.AddStateValue(addr, tfjsonpath.New("asset_id"))},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckReleaseAsset(addr, data),
					resource.TestMatchResourceAttr(addr, "browser_download_url", regexp.MustCompile(`/`+regexp.QuoteMeta(org)+`/`+regexp.QuoteMeta(repoName)+`/releases/download/v1/org\.bin$`)),
				),
			},
			{
				ResourceName:            addr,
				ImportState:             true,
				ImportStateIdFunc:       testAccReleaseAssetImportID(addr),
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"content_base64", "download_count", "updated_at"},
			},
			{
				Config: config("org-v1.bin"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{same.AddStateValue(addr, tfjsonpath.New("asset_id"))},
				Check:             testAccCheckReleaseAsset(addr, nil),
			},
		},
	})
}

// Errors from Ghost, and from files, are explained.
func TestAccReleaseAssetResource_errors(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc-asset")
	dir := t.TempDir()
	empty := testAccWriteFile(t, dir, "empty.bin", nil)
	base := testAccReleaseAssetBlock("a", `content = "x"
  name = "dup.txt"`)
	config := func(extra string) string {
		return testAccReleaseAssetConfig(repoName, base+extra)
	}
	b := func(attrs string) string {
		return testAccReleaseAssetBlock("b", attrs+"\n  depends_on = [ghost_release_asset.a]")
	}

	var aID int64
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckReleaseAssetDestroy,
		Steps: []resource.TestStep{
			{
				Config: config(""),
				Check:  testAccCaptureAssetID("ghost_release_asset.a", &aID),
			},
			{
				Config:      config(b(`content = "y"` + "\n  name = \"dup.txt\"")),
				ExpectError: regexp.MustCompile(`(?s)already\s+has\s+an\s+asset\s+named\s+"dup\.txt".*terraform\s+import`),
			},
			{
				// Names are case-sensitive.
				Config: config(b(`content = "y"` + "\n  name = \"DUP.txt\"")),
				Check:  testAccCheckReleaseAsset("ghost_release_asset.b", []byte("y")),
			},
			{
				Config:      config(b(`content = "y"` + "\n  name = \"dup.txt\"")),
				ExpectError: regexp.MustCompile(`(?s)already\s+has\s+an\s+asset\s+named\s+"dup\.txt"`),
			},
			{
				Config:      config(testAccReleaseAssetBlock("c", fmt.Sprintf("file = %q\n  name = \"empty.bin\"", empty))),
				ExpectError: regexp.MustCompile(`(?s)Empty\s+asset`),
			},
			{
				Config:      config(testAccReleaseAssetBlock("c", "content = \"z\"\n  name = \"z.txt\"\n  release_id = 9007199254740000")),
				ExpectError: regexp.MustCompile(`(?s)Release\s+not\s+found.*9007199254740000`),
			},
			{
				Config:      config(testAccReleaseAssetBlock("c", "content = \"z\"\n  name = \"z.txt\"\n  repository = \"${ghost_repository.test.full_name}-missing\"")),
				ExpectError: regexp.MustCompile(`(?s)Repository\s+not\s+found`),
			},
		},
	})
}
