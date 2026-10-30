package provider

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// testAccStoragePushBytes is the size of the incompressible file the push
// tests commit, so git can't pack it into much less.
const testAccStoragePushBytes = 256 << 10

// TestAccStorageUsageDataSource_user reads the token's own account by
// default and by login in another case, before and after a push to a new
// repository. The push is polled for first in case usage ever lags it.
func TestAccStorageUsageDataSource_user(t *testing.T) {
	testAccRepositoriesPreCheck(t)
	login := testAccTokenLogin(t)
	repoName := acctest.RandomWithPrefix("tf-acc-storage")
	repo := testAccRepositoryConfig(repoName, `auto_init = true`)
	config := repo + fmt.Sprintf(`
data "ghost_storage_usage" "test" {
  depends_on = [ghost_repository.test]
}

data "ghost_storage_usage" "upper" {
  owner      = %q
  depends_on = [ghost_repository.test]
}
`, strings.ToUpper(login))
	const d = "data.ghost_storage_usage."

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(d+"test", "id", login),
					resource.TestCheckResourceAttr(d+"test", "owner", login),
					testAccCheckStorageUsage(d+"test", 0),
					testAccCheckStorageUsageMatchesAPI(d+"test", login),
					// Ghost matches the owner case-insensitively; it is kept as written.
					resource.TestCheckResourceAttr(d+"upper", "id", strings.ToUpper(login)),
					resource.TestCheckResourceAttr(d+"upper", "owner", strings.ToUpper(login)),
					testAccCheckStorageUsageMatchesAPI(d+"upper", login),
				),
			},
			{
				PreConfig: func() { testAccPushStorage(t, login+"/"+repoName, login) },
				Config:    config,
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckStorageUsage(d+"test", testAccStoragePushBytes),
					testAccCheckStorageUsage(d+"upper", testAccStoragePushBytes),
				),
			},
		},
	})
}

// TestAccStorageUsageDataSource_organization reads an organization the
// token's user belongs to, before and after a push to one of its
// repositories.
func TestAccStorageUsageDataSource_organization(t *testing.T) {
	org := os.Getenv("GHOST_TEST_ORGANIZATION")
	if org == "" || os.Getenv(resource.EnvTfAcc) == "" {
		t.Skip("set GHOST_TEST_ORGANIZATION and TF_ACC to test ghost_storage_usage for an organization")
	}
	testAccPreCheck(t)
	repoName := acctest.RandomWithPrefix("tf-acc-storage")
	config := testAccRepositoryConfig(repoName, fmt.Sprintf("owner     = %q\n  auto_init = true", org)) + fmt.Sprintf(`
data "ghost_storage_usage" "test" {
  owner      = %q
  depends_on = [ghost_repository.test]
}
`, org)
	const d = "data.ghost_storage_usage.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(d, "id", org),
					resource.TestCheckResourceAttr(d, "owner", org),
					testAccCheckStorageUsage(d, 0),
					testAccCheckStorageUsageMatchesAPI(d, org),
				),
			},
			{
				PreConfig: func() { testAccPushStorage(t, org+"/"+repoName, org) },
				Config:    config,
				Check:     testAccCheckStorageUsage(d, testAccStoragePushBytes),
			},
		},
	})
}

// TestAccStorageUsageDataSource_notFound covers an unknown name, another
// user (GHOST_TEST_COLLABORATOR, when set), and a login with a slash, which
// is rejected at plan time.
func TestAccStorageUsageDataSource_notFound(t *testing.T) {
	testAccRepositoriesPreCheck(t)
	data := func(owner string) string {
		return fmt.Sprintf("data \"ghost_storage_usage\" \"test\" {\n  owner = %q\n}\n", owner)
	}
	steps := []resource.TestStep{
		{
			Config:      data(acctest.RandomWithPrefix("tf-acc-no-such-owner")),
			ExpectError: regexp.MustCompile(`(?s)Storage usage not found.*No\s+user\s+or\s+organization\s+named\s+"tf-acc-no-such-owner-\d+"\s+exists`),
		},
		{
			Config:      data("tf-acc/x"),
			ExpectError: regexp.MustCompile(`must be a single login`),
		},
	}
	if other := os.Getenv("GHOST_TEST_COLLABORATOR"); other != "" {
		steps = append(steps, resource.TestStep{
			Config: data(other),
			ExpectError: regexp.MustCompile(fmt.Sprintf(`(?s)Storage usage not found.*only\s+to\s+the\s+account\s+itself:\s+"%s"\s+must\s+be`,
				regexp.QuoteMeta(other))),
		})
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps:                    steps,
	})
}

// testAccPushStorage pushes an incompressible file of testAccStoragePushBytes
// to the repository fullName, then waits up to two minutes for the storage
// usage of owner to count at least that many bytes, logging how long it took.
func testAccPushStorage(t *testing.T, fullName, owner string) {
	t.Helper()
	content := make([]byte, testAccStoragePushBytes)
	if _, err := rand.Read(content); err != nil {
		t.Fatal(err)
	}
	testAccCodeownersPusher(t, fullName)("main", "main", "", map[string]string{"blob.bin": string(content)})

	c, err := testAccClient()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for {
		u, err := c.GetStorageUsage(context.Background(), owner)
		if err != nil {
			t.Fatalf("reading the storage usage of %s: %v", owner, err)
		}
		if u.UsedBytes >= testAccStoragePushBytes {
			t.Logf("storage usage of %s counted the push after %s: used_bytes = %d", owner, time.Since(start).Round(time.Second), u.UsedBytes)
			return
		}
		if time.Since(start) > 2*time.Minute {
			t.Fatalf("storage usage of %s is still %d bytes two minutes after pushing %d bytes", owner, u.UsedBytes, testAccStoragePushBytes)
		}
		time.Sleep(5 * time.Second)
	}
}

// testAccCheckStorageUsage checks that every figure in the data source at
// address is set, that used_bytes is at least minUsed, and that every quota
// is null or positive. Other tests change usage concurrently, so exact
// figures aren't checked.
func testAccCheckStorageUsage(address string, minUsed int64) resource.TestCheckFunc {
	atLeast := func(want int64) func(string) error {
		return func(v string) error {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return err
			}
			if n < want {
				return fmt.Errorf("got %d, want at least %d", n, want)
			}
			return nil
		}
	}
	checks := []resource.TestCheckFunc{
		resource.TestCheckResourceAttrWith(address, "used_bytes", atLeast(minUsed)),
		resource.TestCheckResourceAttrWith(address, "max_asset_bytes", atLeast(1)),
	}
	for _, prefix := range []string{"", "fork.", "lfs.", "asset."} {
		if prefix != "" {
			checks = append(checks, resource.TestCheckResourceAttrWith(address, prefix+"used_bytes", atLeast(0)))
		}
		checks = append(checks, func(s *terraform.State) error {
			rs, ok := s.RootModule().Resources[address]
			if !ok {
				return fmt.Errorf("%s not found", address)
			}
			if v, ok := rs.Primary.Attributes[prefix+"quota_bytes"]; ok {
				if err := atLeast(1)(v); err != nil {
					return fmt.Errorf("%s %squota_bytes: %w", address, prefix, err)
				}
			}
			return nil
		})
	}

	return resource.ComposeAggregateTestCheckFunc(checks...)
}

// testAccCheckStorageUsageMatchesAPI checks that the quotas in the data
// source at address are those the API reports for owner now.
func testAccCheckStorageUsageMatchesAPI(address, owner string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		c, err := testAccClient()
		if err != nil {
			return err
		}
		u, err := c.GetStorageUsage(context.Background(), owner)
		if err != nil {
			return err
		}
		quota := func(q *int64) string {
			if q == nil {
				return ""
			}
			return strconv.FormatInt(*q, 10)
		}
		rs, ok := s.RootModule().Resources[address]
		if !ok {
			return fmt.Errorf("%s not found", address)
		}
		for attr, want := range map[string]string{
			"quota_bytes":       quota(u.QuotaBytes),
			"max_asset_bytes":   strconv.FormatInt(u.MaxAssetBytes, 10),
			"fork.quota_bytes":  quota(u.Fork.QuotaBytes),
			"lfs.quota_bytes":   quota(u.LFS.QuotaBytes),
			"asset.quota_bytes": quota(u.Asset.QuotaBytes),
		} {
			if got := rs.Primary.Attributes[attr]; got != want {
				return fmt.Errorf("%s %s = %q, the API says %q", address, attr, got, want)
			}
		}
		return nil
	}
}
