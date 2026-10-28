package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

const logoAddr = "ghost_organization_logo.test"

// testLogoImage returns an n×n image filled with c, or with noise from seed
// when noise is set (noise doesn't compress, so it makes large files).
func testLogoImage(n int, c color.RGBA, noise bool, seed int64) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	r := rand.New(rand.NewSource(seed)) //nolint:gosec // test pixels
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if noise {
				c = color.RGBA{uint8(r.Intn(256)), uint8(r.Intn(256)), uint8(r.Intn(256)), 255}
			}
			img.Set(x, y, c)
		}
	}

	return img
}

// testLogoPNG returns a small PNG filled with c.
func testLogoPNG(t *testing.T, c color.RGBA) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, testLogoImage(16, c, false, 0)); err != nil {
		t.Fatal(err)
	}

	return b.Bytes()
}

// testLogoJPEG returns a small JPEG filled with c.
func testLogoJPEG(t *testing.T, c color.RGBA) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, testLogoImage(16, c, false, 0), nil); err != nil {
		t.Fatal(err)
	}

	return b.Bytes()
}

// testAccOrganizationLogoFixture skips the test unless GHOST_TEST_ORGANIZATION
// and TF_ACC are set. The organization must have no logo, which the test
// leaves it with.
func testAccOrganizationLogoFixture(t *testing.T) string {
	t.Helper()

	org := os.Getenv("GHOST_TEST_ORGANIZATION")
	if org == "" || os.Getenv(resource.EnvTfAcc) == "" {
		t.Skip("set GHOST_TEST_ORGANIZATION and TF_ACC to test ghost_organization_logo")
	}
	testAccPreCheck(t)

	if logo := testAccOrganizationLogoURL(t, org); logo != "" {
		t.Fatalf("organization %s already has a logo (%s); these tests remove it, so remove it first", org, logo)
	}
	t.Cleanup(func() {
		c, err := testAccClient()
		if err != nil {
			t.Error(err)
			return
		}
		if o, err := c.GetOrganization(context.Background(), org); err == nil && o.Logo != nil {
			if err := c.RemoveOrganizationLogo(context.Background(), org); err != nil {
				t.Errorf("removing the logo of %s: %v", org, err)
			}
		}
	})

	return org
}

// testAccOrganizationLogoURL returns the organization's logo URL, "" for
// none.
func testAccOrganizationLogoURL(t *testing.T, org string) string {
	t.Helper()
	c, err := testAccClient()
	if err != nil {
		t.Fatal(err)
	}
	o, err := c.GetOrganization(context.Background(), org)
	if err != nil {
		t.Fatal(err)
	}
	if o.Logo == nil {
		return ""
	}

	return *o.Logo
}

func testAccCheckOrganizationLogoDestroyed(org string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		c, err := testAccClient()
		if err != nil {
			return err
		}
		o, err := c.GetOrganization(context.Background(), org)
		if err != nil {
			return err
		}
		if o.Logo != nil {
			return fmt.Errorf("organization %s still has logo %s", org, *o.Logo)
		}

		return nil
	}
}

// testAccCheckOrganizationLogo checks that the organization's logo is the
// resource's url, in both APIs, and that Ghost serves exactly want there.
func testAccCheckOrganizationLogo(org string, want []byte) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[logoAddr]
		if !ok {
			return fmt.Errorf("%s not in state", logoAddr)
		}
		u := rs.Primary.Attributes["url"]

		c, err := testAccClient()
		if err != nil {
			return err
		}
		ctx := context.Background()
		o, err := c.GetOrganization(ctx, org)
		if err != nil {
			return err
		}
		if o.Logo == nil || *o.Logo != u {
			return fmt.Errorf("organization logo = %v, want %s", o.Logo, u)
		}
		user, err := c.GetUser(ctx, strings.ToLower(org))
		if err != nil {
			return err
		}
		if user.AvatarURL != u {
			return fmt.Errorf("v3 avatar_url = %q, want %s", user.AvatarURL, u)
		}
		var buf bytes.Buffer
		if err := c.DownloadLogo(ctx, u, &buf); err != nil {
			return err
		}
		if !bytes.Equal(buf.Bytes(), want) {
			return fmt.Errorf("Ghost serves %d bytes at %s, want the %d uploaded", buf.Len(), u, len(want))
		}

		return nil
	}
}

// testAccSaveAttr copies a resource attribute into *dst.
func testAccSaveAttr(addr, attr string, dst *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[addr]
		if !ok {
			return fmt.Errorf("%s not in state", addr)
		}
		*dst = rs.Primary.Attributes[attr]

		return nil
	}
}

func testAccCheckAttrEquals(addr, attr string, want *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		return resource.TestCheckResourceAttr(addr, attr, *want)(s)
	}
}

func testAccOrganizationLogoConfig(org, source string) string {
	return fmt.Sprintf(`
resource "ghost_organization_logo" "test" {
  organization = %q
  %s
}
`, org, source)
}

func TestAccOrganizationLogoResource_lifecycle(t *testing.T) {
	org := testAccOrganizationLogoFixture(t)
	dir := t.TempDir()
	pngA := testLogoPNG(t, color.RGBA{200, 30, 30, 255})
	jpegB := testLogoJPEG(t, color.RGBA{30, 30, 200, 255})
	file := testAccWriteFile(t, dir, "logo.png", pngA)
	var firstURL string

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckOrganizationLogoDestroyed(org),
		Steps: []resource.TestStep{
			{
				Config: testAccOrganizationLogoConfig(org, fmt.Sprintf("file = %q", file)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(logoAddr, "id", strings.ToLower(org)),
					resource.TestCheckResourceAttr(logoAddr, "content_type", "image/png"),
					resource.TestCheckResourceAttr(logoAddr, "content_sha256", sha256Hex(string(pngA))),
					resource.TestCheckResourceAttr(logoAddr, "size", fmt.Sprint(len(pngA))),
					resource.TestMatchResourceAttr(logoAddr, "url", regexp.MustCompile(`^https://.+\.png$`)),
					testAccCheckOrganizationLogo(org, pngA),
					testAccSaveAttr(logoAddr, "url", &firstURL),
				),
			},
			{
				// The same bytes from content_base64: nothing is uploaded.
				Config: testAccOrganizationLogoConfig(org, fmt.Sprintf("content_base64 = %q", base64.StdEncoding.EncodeToString(pngA))),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(logoAddr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckAttrEquals(logoAddr, "url", &firstURL),
					testAccCheckOrganizationLogo(org, pngA),
				),
			},
			{
				// A new image is uploaded in place.
				Config: testAccOrganizationLogoConfig(org, fmt.Sprintf("content_base64 = %q", base64.StdEncoding.EncodeToString(jpegB))),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(logoAddr, plancheck.ResourceActionUpdate),
					plancheck.ExpectUnknownValue(logoAddr, tfjsonpath.New("url")),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(logoAddr, "content_type", "image/jpeg"),
					resource.TestCheckResourceAttr(logoAddr, "content_sha256", sha256Hex(string(jpegB))),
					resource.TestCheckResourceAttr(logoAddr, "size", fmt.Sprint(len(jpegB))),
					testAccCheckOrganizationLogo(org, jpegB),
				),
			},
			{
				// Only the slug's case changes: in place, no upload.
				Config: testAccOrganizationLogoConfig(strings.ToUpper(org), fmt.Sprintf("content_base64 = %q", base64.StdEncoding.EncodeToString(jpegB))),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(logoAddr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(logoAddr, "id", strings.ToLower(org)),
					testAccCheckOrganizationLogo(org, jpegB),
				),
			},
			{
				ResourceName:            logoAddr,
				ImportState:             true,
				ImportStateId:           strings.ToUpper(org),
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"content_base64"},
			},
		},
	})
}

func TestAccOrganizationLogoResource_drift(t *testing.T) {
	org := testAccOrganizationLogoFixture(t)
	pngA := testLogoPNG(t, color.RGBA{20, 160, 20, 255})
	pngB := testLogoPNG(t, color.RGBA{240, 200, 0, 255})
	config := testAccOrganizationLogoConfig(org, fmt.Sprintf("content_base64 = %q", base64.StdEncoding.EncodeToString(pngA)))
	setOutside := func(b []byte) func() {
		return func() {
			c, err := testAccClient()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.SetOrganizationLogo(context.Background(), org, "image/png", bytes.NewReader(b), int64(len(b))); err != nil {
				t.Fatal(err)
			}
		}
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckOrganizationLogoDestroyed(org),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  testAccCheckOrganizationLogo(org, pngA),
			},
			{
				// Removed outside Terraform: uploaded again.
				PreConfig: func() {
					c, err := testAccClient()
					if err != nil {
						t.Fatal(err)
					}
					if err := c.RemoveOrganizationLogo(context.Background(), org); err != nil {
						t.Fatal(err)
					}
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(logoAddr, plancheck.ResourceActionCreate),
				}},
				Check: testAccCheckOrganizationLogo(org, pngA),
			},
			{
				// Replaced outside Terraform: put back in place.
				PreConfig: setOutside(pngB),
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(logoAddr, plancheck.ResourceActionUpdate),
				}},
				Check: testAccCheckOrganizationLogo(org, pngA),
			},
			{
				// The same image uploaded again outside Terraform gets a new
				// URL but isn't a change.
				PreConfig: setOutside(pngA),
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectEmptyPlan(),
				}},
				Check: testAccCheckOrganizationLogo(org, pngA),
			},
		},
	})
}

func TestAccOrganizationLogoResource_errors(t *testing.T) {
	org := testAccOrganizationLogoFixture(t)
	dir := t.TempDir()
	small := testLogoPNG(t, color.RGBA{1, 2, 3, 255})

	// Ghost doesn't decode the image (a PNG signature followed by anything
	// is accepted), so padding makes exact sizes.
	sized := func(name string, size int) string {
		b := make([]byte, size)
		copy(b, small)
		return testAccWriteFile(t, dir, name, b)
	}
	limit := sized("limit.png", maxLogoBytes)
	over := sized("over.png", maxLogoBytes+1)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckOrganizationLogoDestroyed(org),
		Steps: []resource.TestStep{
			{
				Config:      testAccOrganizationLogoConfig("tf-acc-no-such-org", fmt.Sprintf("content_base64 = %q", base64.StdEncoding.EncodeToString(small))),
				ExpectError: regexp.MustCompile(`(?s)Ghost\s+has\s+no\s+organization\s+"tf-acc-no-such-org"`),
			},
			{
				Config:      testAccOrganizationLogoConfig(org, fmt.Sprintf("file = %q", over)),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)2097153\s+bytes,\s+but\s+Ghost\s+accepts\s+logos\s+of\s+up\s+to\s+2\s+MiB`),
			},
			{
				// Exactly 2 MiB is accepted.
				Config: testAccOrganizationLogoConfig(org, fmt.Sprintf("file = %q", limit)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(logoAddr, "size", fmt.Sprint(maxLogoBytes)),
					testAccCheckOrganizationLogo(org, mustReadFile(t, limit)),
				),
			},
		},
	})
}

func mustReadFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}

	return b
}
