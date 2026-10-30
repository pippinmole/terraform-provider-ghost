package provider

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

// webhookURLMaxLength is the longest URL Ghost accepts.
const webhookURLMaxLength = 2048

// webhookModel holds the attributes repository and organization webhooks
// share. Resources embed it next to their owner attributes.
type webhookModel struct {
	WebhookID      types.String `tfsdk:"webhook_id"`
	URL            types.String `tfsdk:"url"`
	Events         types.Set    `tfsdk:"events"`
	Active         types.Bool   `tfsdk:"active"`
	DisabledReason types.String `tfsdk:"disabled_reason"`
	Secret         types.String `tfsdk:"secret"`
	SecretRotation types.String `tfsdk:"secret_rotation"`
	CreatedAt      types.String `tfsdk:"created_at"`
}

// webhookSchemaAttributes returns the attributes shared by webhook
// resources. owner names what the webhook belongs to, e.g. "repository".
func webhookSchemaAttributes(owner string) map[string]schema.Attribute {
	stable := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	events := make([]string, len(client.WebhookEvents))
	for i, e := range client.WebhookEvents {
		events[i] = "`" + e + "`"
	}

	return map[string]schema.Attribute{
		"webhook_id": schema.StringAttribute{
			MarkdownDescription: "The webhook's ID, e.g. `whk_…`.",
			Computed:            true,
			PlanModifiers:       stable,
		},
		"url": schema.StringAttribute{
			MarkdownDescription: fmt.Sprintf("The URL Ghost sends deliveries to, up to %d characters. It must be `https://` or `http://`, must not contain a username or password, "+
				"and must resolve to a public address: Ghost rejects `localhost`, private and reserved addresses, and hosts that don't resolve. "+
				"Ghost stores it exactly as given. Sensitive, since webhook URLs often carry a token.", webhookURLMaxLength),
			Required:   true,
			Sensitive:  true,
			Validators: []validator.String{webhookURLValidator{}},
		},
		"events": schema.SetAttribute{
			MarkdownDescription: "The events that trigger a delivery, at least one. One of " + strings.Join(events, ", ") +
				". Ghost doesn't deliver `push` events yet.",
			ElementType: types.StringType,
			Required:    true,
			Validators: []validator.Set{
				setvalidator.SizeAtLeast(1),
				setvalidator.ValueStringsAre(stringvalidator.OneOf(client.WebhookEvents...)),
			},
		},
		"active": schema.BoolAttribute{
			MarkdownDescription: "Whether Ghost sends deliveries. Defaults to `true`. Ghost turns a webhook off by itself when the endpoint " +
				"says it is gone (see `disabled_reason`); the next apply then turns it back on, unless this is `false`.",
			Optional: true,
			Computed: true,
			Default:  booldefault.StaticBool(true),
		},
		"disabled_reason": schema.StringAttribute{
			MarkdownDescription: "Why Ghost turned the webhook off by itself, e.g. after the endpoint answered `410 Gone`. Null while it is on, or when it was turned off with `active = false`.",
			Computed:            true,
			PlanModifiers:       stable,
		},
		"secret": schema.StringAttribute{
			MarkdownDescription: "The secret Ghost signs every delivery with: the `X-Hub-Signature-256` header is `sha256=` followed by the hex HMAC-SHA256 of the body. " +
				"Ghost returns it only when the webhook is created or its secret is rotated, so it is null after import until `secret_rotation` is set.",
			Computed:      true,
			Sensitive:     true,
			PlanModifiers: stable,
		},
		"secret_rotation": schema.StringAttribute{
			MarkdownDescription: "Any value. Setting or changing it replaces the secret in place (the old one stops working at once), " +
				"e.g. `time_rotating.webhook.id` to rotate it on a schedule. Removing it does nothing.",
			Optional: true,
		},
		"created_at": schema.StringAttribute{
			MarkdownDescription: fmt.Sprintf("When the webhook was added to the %s.", owner),
			Computed:            true,
			PlanModifiers:       stable,
		},
	}
}

// fromRemote copies hook into m. The secret is kept, since Ghost returns it
// only on create.
func (m *webhookModel) fromRemote(ctx context.Context, hook *client.Webhook) diag.Diagnostics {
	events, diags := types.SetValueFrom(ctx, types.StringType, hook.Events)
	m.WebhookID = types.StringValue(hook.ID)
	m.URL = types.StringValue(hook.URL)
	m.Events = events
	m.Active = types.BoolValue(hook.Active)
	m.DisabledReason = types.StringPointerValue(hook.DisabledReason)
	m.CreatedAt = types.StringValue(hook.CreatedAt)
	if hook.Secret != "" {
		m.Secret = types.StringValue(hook.Secret)
	}

	return diags
}

// eventList returns the configured events in Ghost's documented order, so
// requests are stable.
func (m *webhookModel) eventList(ctx context.Context) ([]string, diag.Diagnostics) {
	var events []string
	diags := m.Events.ElementsAs(ctx, &events, false)
	slices.SortFunc(events, func(a, b string) int {
		return slices.Index(client.WebhookEvents, a) - slices.Index(client.WebhookEvents, b)
	})

	return events, diags
}

// wantsRotation reports whether going from state to plan should replace the
// secret: secret_rotation was set or changed, but not removed.
func (m *webhookModel) wantsRotation(state *webhookModel) bool {
	return !m.SecretRotation.IsNull() && !m.SecretRotation.Equal(state.SecretRotation)
}

// modifyWebhookPlan marks the attributes an update will change as unknown.
func modifyWebhookPlan(ctx context.Context, plan, state *webhookModel, resp *resource.ModifyPlanResponse) {
	if !plan.URL.Equal(state.URL) || !plan.Events.Equal(state.Events) || !plan.Active.Equal(state.Active) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("disabled_reason"), types.StringUnknown())...)
	}
	if plan.wantsRotation(state) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("secret"), types.StringUnknown())...)
	}
}

// createWebhook creates the webhook in plan and fills in its computed
// attributes. Ghost ignores active on create, so turning it off takes a
// second request.
func createWebhook(ctx context.Context, hooks *client.Webhooks, plan *webhookModel) diag.Diagnostics {
	events, diags := plan.eventList(ctx)
	if diags.HasError() {
		return diags
	}

	hook, err := hooks.Create(ctx, client.CreateWebhookRequest{URL: plan.URL.ValueString(), Events: events})
	if err != nil {
		diags.AddError("Error creating webhook", err.Error())
		return diags
	}
	plan.Secret = types.StringValue(hook.Secret)

	if !plan.Active.ValueBool() && hook.Active {
		off := false
		updated, err := hooks.Update(ctx, hook.ID, client.UpdateWebhookRequest{Active: &off})
		if err != nil {
			// Keep what was created in state, so it isn't orphaned.
			diags.Append(plan.fromRemote(ctx, hook)...)
			diags.AddError("Error turning the webhook off",
				fmt.Sprintf("Webhook %s was created but could not be turned off: %s", hook.ID, err))
			return diags
		}
		hook = updated
	}

	diags.Append(plan.fromRemote(ctx, hook)...)
	return diags
}

// updateWebhook applies the changes from state to plan and fills in plan's
// computed attributes.
func updateWebhook(ctx context.Context, hooks *client.Webhooks, plan, state *webhookModel) diag.Diagnostics {
	var diags diag.Diagnostics
	id := state.WebhookID.ValueString()
	plan.WebhookID = state.WebhookID
	plan.Secret = state.Secret

	var update client.UpdateWebhookRequest
	if !plan.URL.Equal(state.URL) {
		update.URL = plan.URL.ValueStringPointer()
	}
	if !plan.Events.Equal(state.Events) {
		events, d := plan.eventList(ctx)
		diags.Append(d...)
		if diags.HasError() {
			return diags
		}
		update.Events = events
	}
	if !plan.Active.Equal(state.Active) {
		update.Active = plan.Active.ValueBoolPointer()
	}

	// Ghost answers an empty PATCH with a 500, so only read when nothing
	// changed (e.g. only secret_rotation or a repository rename).
	var hook *client.Webhook
	var err error
	if update.IsEmpty() {
		hook, err = hooks.Get(ctx, id)
	} else {
		hook, err = hooks.Update(ctx, id, update)
	}
	if err != nil {
		diags.AddError("Error updating webhook", err.Error())
		return diags
	}

	if plan.wantsRotation(state) {
		secret, err := hooks.RotateSecret(ctx, id)
		if err != nil {
			diags.AddError("Error rotating the webhook secret", err.Error())
			return diags
		}
		plan.Secret = types.StringValue(secret)
	}

	diags.Append(plan.fromRemote(ctx, hook)...)
	return diags
}

// webhookURLValidator checks what Ghost checks about a webhook URL without a
// DNS lookup, so mistakes show up at plan time.
type webhookURLValidator struct{}

func (v webhookURLValidator) Description(context.Context) string {
	return fmt.Sprintf("must be an http:// or https:// URL of at most %d characters, without a username or password", webhookURLMaxLength)
}

func (v webhookURLValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v webhookURLValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if err := validateWebhookURL(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid webhook URL", err.Error())
	}
}

// validateWebhookURL returns an error explaining why Ghost would reject raw.
// The URL itself is left out of the message, since it may hold a token.
func validateWebhookURL(raw string) error {
	if strings.TrimSpace(raw) != raw {
		return fmt.Errorf("the URL must not start or end with whitespace")
	}
	if len(raw) > webhookURLMaxLength {
		return fmt.Errorf("the URL is %d characters long; Ghost accepts at most %d", len(raw), webhookURLMaxLength)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("enter a full URL, such as https://example.com/webhook")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("the URL must start with https:// or http://")
	}
	if u.User != nil {
		return fmt.Errorf("the URL must not contain a username or password")
	}

	return nil
}
