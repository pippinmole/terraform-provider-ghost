package provider

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
	"golang.org/x/crypto/ssh"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

const testAccSSHSigningKeyAddress = "ghost_user_ssh_signing_key.test"

// Test keys generated with ssh-keygen; the fingerprints are what
// ssh-keygen -lf prints for them.
const (
	testEd25519Key     = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKDvL6hsOgtbepvkxOnAUmujWpjGaIYBYMNqUkc9qQ2R"
	testEd25519FP      = "SHA256:y0miGnNZ4e6Ut2rSFI9yNf7EYnBZfIPcWiGK2DhtSVU"
	testECDSAKey       = "ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBGsAbh5tYUrNiFTocyt+6IoqAvjEQ5snLC6CwoX5WClPwmuoCXq4JsIJ8CGhrHolsBXLfolEkAC6Baz8DbDlUME="
	testECDSAFP        = "SHA256:Dd380uSFD78sgc8tbdy1RxxnG3vrfPX2ztKJ8GivnkA"
	testPrivateKeyLine = "-----BEGIN OPENSSH PRIVATE KEY-----"
)

func TestNormalizeSSHPublicKey(t *testing.T) {
	ok := []struct{ in, want string }{
		{testEd25519Key, testEd25519Key},
		{testEd25519Key + " me@host", testEd25519Key},
		{"  " + testEd25519Key + "\tme@host  \n", testEd25519Key},
		{testEd25519Key + " comment with spaces\r\n", testEd25519Key},
		{testECDSAKey + " x", testECDSAKey},
	}
	for _, tt := range ok {
		got, err := normalizeSSHPublicKey(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("normalizeSSHPublicKey(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}

	for _, in := range []string{"", "ssh-ed25519", "ssh-ed25519 notakey", "garbage", testPrivateKeyLine,
		testEd25519Key + "\n" + testECDSAKey} {
		if got, err := normalizeSSHPublicKey(in); err == nil {
			t.Errorf("normalizeSSHPublicKey(%q) = %q, want an error", in, got)
		}
	}
}

func TestSSHKeyFingerprint(t *testing.T) {
	for _, tt := range []struct{ key, want string }{
		{testEd25519Key, testEd25519FP},
		{"  " + testEd25519Key + " other comment\n", testEd25519FP},
		{testECDSAKey, testECDSAFP},
	} {
		if got, err := sshKeyFingerprint(tt.key); err != nil || got != tt.want {
			t.Errorf("sshKeyFingerprint(%q) = %q, %v; want %q", tt.key, got, err, tt.want)
		}
	}
}

func TestSSHKeyChanged(t *testing.T) {
	for _, tt := range []struct {
		name         string
		state, plan  string
		wantReplaced bool
	}{
		{"same", testEd25519Key, testEd25519Key, false},
		{"comment added", testEd25519Key, testEd25519Key + " me@host", false},
		{"whitespace", testEd25519Key + " a", "\n" + testEd25519Key + " b\n", false},
		{"different key", testEd25519Key, testECDSAKey, true},
		{"unparseable state", "junk", testEd25519Key, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := planmodifier.StringRequest{StateValue: types.StringValue(tt.state), PlanValue: types.StringValue(tt.plan)}
			var resp stringplanmodifier.RequiresReplaceIfFuncResponse
			sshKeyChanged(context.Background(), req, &resp)
			if resp.RequiresReplace != tt.wantReplaced {
				t.Errorf("RequiresReplace = %v, want %v", resp.RequiresReplace, tt.wantReplaced)
			}
		})
	}
}

func TestUserSSHSigningKeyFromRemote(t *testing.T) {
	remote := &client.SSHSigningKey{ID: 42, Key: testEd25519Key, Title: "laptop", CreatedAt: "2026-10-28T00:28:51.723Z"}

	// The configured spelling of the same key is kept.
	m := userSSHSigningKeyResourceModel{Key: types.StringValue(testEd25519Key + " me@host\n")}
	m.fromRemote(remote)
	if m.ID.ValueString() != "42" || m.Title.ValueString() != "laptop" || m.Key.ValueString() != testEd25519Key+" me@host\n" ||
		m.Fingerprint.ValueString() != testEd25519FP || m.CreatedAt.ValueString() != remote.CreatedAt {
		t.Errorf("unexpected model: %+v", m)
	}

	// A different key in state is drift and takes the remote value.
	m = userSSHSigningKeyResourceModel{Key: types.StringValue(testECDSAKey)}
	m.fromRemote(remote)
	if m.Key.ValueString() != testEd25519Key {
		t.Errorf("key = %q, want the remote key", m.Key.ValueString())
	}

	// An import has no key in state.
	m = userSSHSigningKeyResourceModel{Key: types.StringNull()}
	m.fromRemote(remote)
	if m.Key.ValueString() != testEd25519Key {
		t.Errorf("key = %q, want the remote key", m.Key.ValueString())
	}
}

func TestParseSSHSigningKeyID(t *testing.T) {
	if id, err := parseSSHSigningKeyID("123"); err != nil || id != 123 {
		t.Errorf("parseSSHSigningKeyID(123) = %d, %v", id, err)
	}
	for _, in := range []string{"", "abc", "-1", "0", "1.5", " 1"} {
		if _, err := parseSSHSigningKeyID(in); err == nil || !strings.Contains(err.Error(), "numeric ID") {
			t.Errorf("parseSSHSigningKeyID(%q) error = %v", in, err)
		}
	}
}

func TestUserSSHSigningKeyConfigValidation(t *testing.T) {
	tests := []struct {
		name, title, key, expect string
	}{
		{"empty title", `""`, `"` + testEd25519Key + `"`, `must be between 1 and 100`},
		{"long title", `"` + strings.Repeat("a", 101) + `"`, `"` + testEd25519Key + `"`, `must be between 1 and 100`},
		{"leading space", `" a"`, `"` + testEd25519Key + `"`, `must not start or end with whitespace`},
		{"trailing space", `"a "`, `"` + testEd25519Key + `"`, `must not start or end with whitespace`},
		{"empty key", `"a"`, `""`, `Invalid SSH public key`},
		{"malformed key", `"a"`, `"ssh-ed25519 notakey"`, `Invalid SSH public key`},
		{"private key", `"a"`, `"` + testPrivateKeyLine + `"`, `not a private key`},
		{"two keys", `"a"`, `"` + testEd25519Key + `\n` + testECDSAKey + `"`, `only one key`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      testAccSSHSigningKeyConfig("test", tt.title, tt.key),
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tt.expect),
				}},
			})
		})
	}

}

func TestAccUserSSHSigningKeyResource(t *testing.T) {
	prefix := testAccSSHSigningKeyFixture(t)
	dir := t.TempDir()
	ed := testAccGenerateSSHKey(t, dir, "ed25519")
	rsaKey := testAccGenerateSSHKey(t, dir, "rsa")
	title := prefix + "-a"
	retitled := prefix + "-b"
	sameID := statecheck.CompareValue(compare.ValuesSame())
	newID := statecheck.CompareValue(compare.ValuesDiffer())

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckSSHSigningKeyDestroy(prefix),
		Steps: []resource.TestStep{
			{
				Config: testAccSSHSigningKeyConfig("test", strconv.Quote(title), strconv.Quote(ed.public+" tf-acc@host")),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccSSHSigningKeyAddress, plancheck.ResourceActionCreate)},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(testAccSSHSigningKeyAddress, tfjsonpath.New("id"), knownvalue.StringRegexp(regexp.MustCompile(`^\d+$`))),
					statecheck.ExpectKnownValue(testAccSSHSigningKeyAddress, tfjsonpath.New("key"), knownvalue.StringExact(ed.public+" tf-acc@host")),
					statecheck.ExpectKnownValue(testAccSSHSigningKeyAddress, tfjsonpath.New("fingerprint"), knownvalue.StringExact(ed.fingerprint)),
					statecheck.ExpectKnownValue(testAccSSHSigningKeyAddress, tfjsonpath.New("created_at"), knownvalue.NotNull()),
					sameID.AddStateValue(testAccSSHSigningKeyAddress, tfjsonpath.New("id")),
					newID.AddStateValue(testAccSSHSigningKeyAddress, tfjsonpath.New("id")),
				},
				Check: testAccCheckSSHSigningKeyRemote(title, ed.public),
			},
			{
				ResourceName:            testAccSSHSigningKeyAddress,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"key"},
			},
			{
				// The import holds the key as Ghost stores it.
				ResourceName: testAccSSHSigningKeyAddress,
				ImportState:  true,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 || states[0].Attributes["key"] != ed.public || states[0].Attributes["title"] != title {
						return fmt.Errorf("unexpected imported state: %v", states)
					}
					return nil
				},
			},
			{
				ResourceName:  testAccSSHSigningKeyAddress,
				ImportState:   true,
				ImportStateId: "abc",
				ExpectError:   regexp.MustCompile(`numeric ID`),
			},
			{
				ResourceName:  testAccSSHSigningKeyAddress,
				ImportState:   true,
				ImportStateId: "999999999",
				ExpectError:   regexp.MustCompile(`Cannot import non-existent remote object`),
			},
			{
				// A new comment and whitespace only update the state.
				Config: testAccSSHSigningKeyConfig("test", strconv.Quote(title), strconv.Quote("\n"+ed.public+"  other comment\n")),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccSSHSigningKeyAddress, plancheck.ResourceActionUpdate)},
				},
				ConfigStateChecks: []statecheck.StateCheck{sameID.AddStateValue(testAccSSHSigningKeyAddress, tfjsonpath.New("id"))},
				Check:             testAccCheckSSHSigningKeyRemote(title, ed.public),
			},
			{
				Config: testAccSSHSigningKeyConfig("test", strconv.Quote(retitled), strconv.Quote(ed.public)),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccSSHSigningKeyAddress, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				ConfigStateChecks: []statecheck.StateCheck{newID.AddStateValue(testAccSSHSigningKeyAddress, tfjsonpath.New("id"))},
				Check:             testAccCheckSSHSigningKeyRemote(retitled, ed.public),
			},
			{
				Config: testAccSSHSigningKeyConfig("test", strconv.Quote(retitled), strconv.Quote(rsaKey.public)),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccSSHSigningKeyAddress, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(testAccSSHSigningKeyAddress, tfjsonpath.New("fingerprint"), knownvalue.StringExact(rsaKey.fingerprint)),
				},
				Check: testAccCheckSSHSigningKeyRemote(retitled, rsaKey.public),
			},
		},
	})
}

func TestAccUserSSHSigningKeyResource_drift(t *testing.T) {
	prefix := testAccSSHSigningKeyFixture(t)
	key := testAccGenerateSSHKey(t, t.TempDir(), "ecdsa")
	config := testAccSSHSigningKeyConfig("test", strconv.Quote(prefix), strconv.Quote(key.public))
	var id string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckSSHSigningKeyDestroy(prefix),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  resource.TestCheckResourceAttrWith(testAccSSHSigningKeyAddress, "id", func(v string) error { id = v; return nil }),
			},
			{
				// Deleted outside Terraform: removed from state and recreated.
				PreConfig: func() { testAccDeleteSSHSigningKey(t, id) },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccSSHSigningKeyAddress, plancheck.ResourceActionCreate)},
				},
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttrWith(testAccSSHSigningKeyAddress, "id", func(v string) error {
						if v == id {
							return fmt.Errorf("id %s was not replaced", v)
						}
						return nil
					}),
					testAccCheckSSHSigningKeyRemote(prefix, key.public),
				),
			},
		},
	})
}

func TestAccUserSSHSigningKeyResource_errors(t *testing.T) {
	prefix := testAccSSHSigningKeyFixture(t)
	key := testAccGenerateSSHKey(t, t.TempDir(), "ed25519")
	first := testAccSSHSigningKeyConfig("test", strconv.Quote(prefix+"-a"), strconv.Quote(key.public))
	duplicate := first + testAccSSHSigningKeyConfig("dup", strconv.Quote(prefix+"-b"), strconv.Quote(key.public+" another comment"))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckSSHSigningKeyDestroy(prefix),
		Steps: []resource.TestStep{
			{Config: first},
			{
				// The same key under another title can't be added twice.
				Config:      duplicate,
				ExpectError: testAccWrappedRegexp("This key is already a signing key on your Ghost account, with ID"),
			},
			{
				Config: first,
				Check:  testAccCheckSSHSigningKeyRemote(prefix+"-a", key.public),
			},
		},
	})
}

func TestAccUserSSHSigningKeyResource_signing(t *testing.T) {
	prefix := testAccSSHSigningKeyFixture(t)
	repoName := acctest.RandomWithPrefix("tf-acc-ssh-signing")
	key := testAccGenerateSSHKey(t, t.TempDir(), "ed25519")
	repo := fmt.Sprintf(`
resource "ghost_repository" "test" {
  name       = %q
  visibility = "public"
  auto_init  = true
}
`, repoName)
	withKey := repo + testAccSSHSigningKeyConfig("test", strconv.Quote(prefix), strconv.Quote(key.public))
	var sha string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: resource.ComposeTestCheckFunc(
			testAccCheckRepositoryDestroy,
			testAccCheckSSHSigningKeyDestroy(prefix),
		),
		Steps: []resource.TestStep{
			{
				// Ghost recognises a commit signed with the key as this
				// account's. It isn't verified, since the author address
				// isn't one of the account's.
				Config: withKey,
				Check: func(*terraform.State) error {
					sha = testAccPushSignedCommit(t, repoName, "main", "", key.private)
					return testAccCheckCommitVerification(t, repoName, sha, key.fingerprint,
						"The signing key is not linked to tf-acc@example.invalid")
				},
			},
			{
				// Without the key, Ghost no longer knows who signed it.
				Config: repo,
				Check: func(*terraform.State) error {
					return testAccCheckCommitVerification(t, repoName, sha, key.fingerprint,
						"Signed with a key no Ghost account has uploaded")
				},
			},
		},
	})
}

// testAccSSHKey is a key pair generated for a test.
type testAccSSHKey struct {
	public      string // "<type> <base64>", without a comment
	private     string // path to the OpenSSH private key file
	fingerprint string
}

// testAccGenerateSSHKey writes a new private key of the given kind (ed25519,
// rsa or ecdsa) to dir.
func testAccGenerateSSHKey(t *testing.T, dir, kind string) testAccSSHKey {
	t.Helper()

	var priv any
	var err error
	switch kind {
	case "ed25519":
		_, priv, err = ed25519.GenerateKey(rand.Reader)
	case "rsa":
		priv, err = rsa.GenerateKey(rand.Reader, 3072)
	case "ecdsa":
		priv, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	default:
		t.Fatalf("unknown key kind %q", kind)
	}
	if err != nil {
		t.Fatalf("generating %s key: %v", kind, err)
	}

	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "tf-acc")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "tf-acc-"+kind)
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}

	public := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	return testAccSSHKey{public: public, private: path, fingerprint: ssh.FingerprintSHA256(signer.PublicKey())}
}

// testAccSSHSigningKeyFixture returns a unique tf-acc title prefix, and
// deletes any key whose title starts with it when the test ends. Only those
// titles are ever logged: other keys' titles can hold personal data.
func testAccSSHSigningKeyFixture(t *testing.T) string {
	t.Helper()
	prefix := acctest.RandomWithPrefix("tf-acc-signing")

	t.Cleanup(func() {
		if os.Getenv(envToken) == "" {
			return
		}
		c, err := testAccClient()
		if err != nil {
			t.Error(err)
			return
		}
		for _, k := range testAccMarkedSSHSigningKeys(t, c, prefix) {
			if err := c.DeleteSSHSigningKey(context.Background(), k.ID); err != nil && !client.IsNotFound(err) {
				t.Errorf("deleting leftover signing key %d (%s): %v", k.ID, k.Title, err)
			}
		}
	})

	return prefix
}

// testAccMarkedSSHSigningKeys lists the signing keys whose title starts with
// prefix.
func testAccMarkedSSHSigningKeys(t *testing.T, c *client.Client, prefix string) []client.SSHSigningKey {
	t.Helper()
	keys, err := c.ListSSHSigningKeys(context.Background())
	if err != nil {
		t.Errorf("listing signing keys: %v", err)
		return nil
	}

	var marked []client.SSHSigningKey
	for _, k := range keys {
		if strings.HasPrefix(k.Title, prefix) {
			marked = append(marked, k)
		}
	}
	return marked
}

func testAccDeleteSSHSigningKey(t *testing.T, id string) {
	t.Helper()
	c, err := testAccClient()
	if err != nil {
		t.Fatal(err)
	}
	n, err := parseSSHSigningKeyID(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteSSHSigningKey(context.Background(), n); err != nil {
		t.Fatalf("deleting signing key %s: %v", id, err)
	}
}

// testAccCheckSSHSigningKeyRemote verifies, straight from the API, that the
// key in state has the given title and stored key.
func testAccCheckSSHSigningKeyRemote(title, key string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[testAccSSHSigningKeyAddress]
		if !ok {
			return fmt.Errorf("%s not found in state", testAccSSHSigningKeyAddress)
		}
		id, err := parseSSHSigningKeyID(rs.Primary.ID)
		if err != nil {
			return err
		}
		c, err := testAccClient()
		if err != nil {
			return err
		}
		k, err := c.GetSSHSigningKey(context.Background(), id)
		if err != nil {
			return err
		}
		if k.Title != title || k.Key != key {
			return fmt.Errorf("signing key %d: got title %q and key %q, want %q and %q", id, k.Title, k.Key, title, key)
		}
		return nil
	}
}

// testAccCheckSSHSigningKeyDestroy checks that every signing key in state is
// gone, and that no key titled with prefix is left.
func testAccCheckSSHSigningKeyDestroy(prefix string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		c, err := testAccClient()
		if err != nil {
			return err
		}
		for _, rs := range s.RootModule().Resources {
			if rs.Type != "ghost_user_ssh_signing_key" {
				continue
			}
			id, err := parseSSHSigningKeyID(rs.Primary.ID)
			if err != nil {
				return err
			}
			if _, err := c.GetSSHSigningKey(context.Background(), id); !client.IsNotFound(err) {
				return fmt.Errorf("signing key %d still exists (err: %v)", id, err)
			}
		}

		keys, err := c.ListSSHSigningKeys(context.Background())
		if err != nil {
			return err
		}
		for _, k := range keys {
			if strings.HasPrefix(k.Title, prefix) {
				return fmt.Errorf("signing key %d (%s) was left behind", k.ID, k.Title)
			}
		}
		return nil
	}
}

// testAccCheckCommitVerification checks a commit's signature verification as
// Ghost reports it on the native commit endpoint. v3 has no single-commit
// route.
func testAccCheckCommitVerification(t *testing.T, repoName, sha, keyID, reason string) error {
	t.Helper()
	c, repo := testAccRepository(t, repoName)
	owner, slug, _ := client.SplitFullName(repo.FullName)

	var commit struct {
		Verification *struct {
			Verified bool   `json:"verified"`
			Reason   string `json:"reason"`
			KeyID    string `json:"keyId"`
		} `json:"verification"`
	}
	if err := c.Do(context.Background(), "GET", client.NativePath("repositories", owner, slug, "commits", sha), nil, &commit); err != nil {
		return err
	}
	v := commit.Verification
	if v == nil {
		return fmt.Errorf("commit %s has no verification", sha)
	}
	if v.Verified || v.KeyID != keyID || v.Reason != reason {
		return fmt.Errorf("commit %s verification: got verified=%v keyId=%q reason=%q, want verified=false keyId=%q reason=%q",
			sha, v.Verified, v.KeyID, v.Reason, keyID, reason)
	}
	return nil
}

func testAccSSHSigningKeyConfig(name, title, key string) string {
	return fmt.Sprintf(`
resource "ghost_user_ssh_signing_key" %q {
  title = %s
  key   = %s
}
`, name, title, key)
}
