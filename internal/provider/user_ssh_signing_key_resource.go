package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"golang.org/x/crypto/ssh"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

var (
	_ resource.Resource                = &UserSSHSigningKeyResource{}
	_ resource.ResourceWithConfigure   = &UserSSHSigningKeyResource{}
	_ resource.ResourceWithImportState = &UserSSHSigningKeyResource{}
)

// NewUserSSHSigningKeyResource returns the ghost_user_ssh_signing_key resource.
func NewUserSSHSigningKeyResource() resource.Resource {
	return &UserSSHSigningKeyResource{}
}

// UserSSHSigningKeyResource manages an SSH key the authenticated user signs
// commits with.
type UserSSHSigningKeyResource struct {
	client *client.Client
}

// userSSHSigningKeyResourceModel maps the ghost_user_ssh_signing_key schema.
type userSSHSigningKeyResourceModel struct {
	ID          types.String `tfsdk:"id"`
	Title       types.String `tfsdk:"title"`
	Key         types.String `tfsdk:"key"`
	Fingerprint types.String `tfsdk:"fingerprint"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

func (r *UserSSHSigningKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user_ssh_signing_key"
}

func (r *UserSSHSigningKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	stableString := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an SSH signing key of the authenticated user (the owner of the provider's token). " +
			"Ghost uses signing keys to verify SSH-signed commits; they don't grant git access. " +
			"Ghost can't edit a signing key, so changing the title or the key replaces it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The numeric ID of the key.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
			"title": schema.StringAttribute{
				MarkdownDescription: "A name for the key, up to 100 characters. Changing it forces a new key.",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					stringvalidator.UTF8LengthBetween(1, 100),
					stringvalidator.RegexMatches(untrimmedPattern, "must not start or end with whitespace"),
				},
			},
			"key": schema.StringAttribute{
				MarkdownDescription: "The public key in OpenSSH format, e.g. `file(\"~/.ssh/id_ed25519.pub\")`. " +
					"Ghost stores only `<type> <base64>`, so the comment and surrounding whitespace are ignored: " +
					"changing them updates the state without touching the key. Any other change forces a new key.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplaceIf(sshKeyChanged,
						"Changing the key itself (not just its comment) forces a new key.",
						"Changing the key itself (not just its comment) forces a new key."),
				},
				Validators: []validator.String{sshPublicKeyValidator{}},
			},
			"fingerprint": schema.StringAttribute{
				MarkdownDescription: "The key's SHA256 fingerprint, as `ssh-keygen -lf` prints it, e.g. `SHA256:y0mi…`. Ghost reports it as the `keyId` of a signed commit's verification.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "When the key was added, in ISO 8601.",
				Computed:            true,
				PlanModifiers:       stableString,
			},
		},
	}
}

func (r *UserSSHSigningKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data",
			fmt.Sprintf("Expected *client.Client, got %T. Please report this issue to the provider developers.", req.ProviderData))
		return
	}

	r.client = c
}

func (r *UserSSHSigningKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan userSSHSigningKeyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	key, err := r.client.CreateSSHSigningKey(ctx, client.CreateSSHSigningKeyRequest{
		Title: plan.Title.ValueString(),
		Key:   plan.Key.ValueString(),
	})
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnprocessableEntity {
		resp.Diagnostics.AddAttributeError(path.Root("key"), "Error adding SSH signing key",
			r.duplicateHint(ctx, plan.Key.ValueString(), err))
		return
	}
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("key"), "Error adding SSH signing key", err.Error())
		return
	}

	plan.fromRemote(key)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// duplicateHint explains a 422 from Ghost. When the key is already one of the
// user's signing keys, it names the key's ID so it can be imported.
func (r *UserSSHSigningKeyResource) duplicateHint(ctx context.Context, configured string, err error) string {
	want, normErr := normalizeSSHPublicKey(configured)
	if normErr != nil {
		return err.Error()
	}

	keys, listErr := r.client.ListSSHSigningKeys(ctx)
	if listErr != nil {
		return err.Error()
	}
	for _, k := range keys {
		if got, _ := normalizeSSHPublicKey(k.Key); got == want {
			return fmt.Sprintf("This key is already a signing key on your Ghost account, with ID %d. "+
				"Import it with: terraform import <resource address> %d (%s)", k.ID, k.ID, err)
		}
	}

	return err.Error()
}

func (r *UserSSHSigningKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state userSSHSigningKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id, err := parseSSHSigningKeyID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("id"), "Invalid ID", err.Error())
		return
	}

	key, err := r.client.GetSSHSigningKey(ctx, id)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error reading SSH signing key", err.Error())
		return
	}

	state.fromRemote(key)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update only runs when the key's comment or surrounding whitespace changed,
// since every other change forces a new key. Ghost doesn't store either, so
// there's nothing to send.
func (r *UserSSHSigningKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state userSSHSigningKeyResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	state.Key = plan.Key
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *UserSSHSigningKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state userSSHSigningKeyResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id, err := parseSSHSigningKeyID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("id"), "Invalid ID", err.Error())
		return
	}

	if err := r.client.DeleteSSHSigningKey(ctx, id); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Error deleting SSH signing key", err.Error())
	}
}

func (r *UserSSHSigningKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := parseSSHSigningKeyID(req.ID); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// fromRemote copies key into m. The configured key is kept when it is the same
// key with a comment or extra whitespace.
func (m *userSSHSigningKeyResourceModel) fromRemote(key *client.SSHSigningKey) {
	m.ID = types.StringValue(strconv.FormatInt(key.ID, 10))
	m.Title = types.StringValue(key.Title)
	m.CreatedAt = types.StringValue(key.CreatedAt)

	remote, err := normalizeSSHPublicKey(key.Key)
	if err != nil {
		remote = key.Key
	}
	if prior, err := normalizeSSHPublicKey(m.Key.ValueString()); m.Key.IsNull() || m.Key.IsUnknown() || err != nil || prior != remote {
		m.Key = types.StringValue(key.Key)
	}

	if fp, err := sshKeyFingerprint(key.Key); err == nil {
		m.Fingerprint = types.StringValue(fp)
	} else {
		m.Fingerprint = types.StringNull()
	}
}

// parseSSHSigningKeyID parses a key's numeric ID.
func parseSSHSigningKeyID(id string) (int64, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("the ID must be the key's numeric ID, e.g. \"42\", got %q", id)
	}

	return n, nil
}

// parseSSHPublicKey parses an OpenSSH public key, as found in a .pub file or
// an authorized_keys line.
func parseSSHPublicKey(key string) (ssh.PublicKey, error) {
	pub, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(key))
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(rest)) > 0 {
		return nil, errors.New("only one key is allowed")
	}

	return pub, nil
}

// normalizeSSHPublicKey returns key the way Ghost stores it: "<type> <base64>",
// without the comment.
func normalizeSSHPublicKey(key string) (string, error) {
	pub, err := parseSSHPublicKey(key)
	if err != nil {
		return "", err
	}

	return pub.Type() + " " + base64.StdEncoding.EncodeToString(pub.Marshal()), nil
}

// sshKeyFingerprint returns key's SHA256 fingerprint, as ssh-keygen -lf
// prints it.
func sshKeyFingerprint(key string) (string, error) {
	pub, err := parseSSHPublicKey(key)
	if err != nil {
		return "", err
	}

	return ssh.FingerprintSHA256(pub), nil
}

// sshKeyChanged forces a new key unless only the comment or whitespace
// changed.
func sshKeyChanged(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
	prior, priorErr := normalizeSSHPublicKey(req.StateValue.ValueString())
	planned, plannedErr := normalizeSSHPublicKey(req.PlanValue.ValueString())
	resp.RequiresReplace = priorErr != nil || plannedErr != nil || prior != planned
}

// sshPublicKeyValidator checks that a key is a single OpenSSH public key.
type sshPublicKeyValidator struct{}

func (v sshPublicKeyValidator) Description(_ context.Context) string {
	return "must be an OpenSSH public key"
}

func (v sshPublicKeyValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v sshPublicKeyValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	if _, err := parseSSHPublicKey(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid SSH public key",
			fmt.Sprintf("The key must be an OpenSSH public key such as the contents of id_ed25519.pub "+
				"(\"ssh-ed25519 AAAA… comment\"), not a private key (%s).", err))
	}
}
