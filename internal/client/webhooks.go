package client

import (
	"context"
	"net/http"
)

// WebhookEvents lists the events a webhook can subscribe to, in the order
// Ghost documents them.
var WebhookEvents = []string{
	"push",
	"issue.opened",
	"issue.edited",
	"issue.closed",
	"issue.reopened",
	"issue.assigned",
	"issue.unassigned",
	"issue.labeled",
	"issue.unlabeled",
	"issue.commented",
	"issue.comment_edited",
	"issue.comment_deleted",
	"pull_request.ready_for_review",
	"pull_request.converted_to_draft",
	"pull_request.synchronized",
	"pull_request.merged",
	"pull_request.reviewed",
	"pull_request.review_dismissed",
	"pull_request.review_commented",
	"pull_request.review_requested",
	"pull_request.review_request_removed",
	"label.created",
	"label.edited",
	"label.deleted",
	"release.created",
	"release.published",
	"release.edited",
	"release.deleted",
	"commit_status.created",
	"star.created",
	"star.deleted",
	"watch.started",
	"fork.created",
	"repository.edited",
	"repository.transferred",
	"member.added",
	"member.removed",
}

// Webhook is a webhook as returned by the native API, for a repository or an
// organization.
type Webhook struct {
	ID     string   `json:"id"`
	URL    string   `json:"url"`
	Events []string `json:"events"`
	Active bool     `json:"active"`
	// DisabledReason says why Ghost turned the webhook off, e.g. after the
	// endpoint answered 410 Gone. Nil while it is on.
	DisabledReason *string `json:"disabledReason"`
	CreatedAt      string  `json:"createdAt"`
	// Secret signs every delivery. Ghost returns it only when the webhook is
	// created.
	Secret string `json:"secret,omitempty"`
}

// CreateWebhookRequest is the body for creating a webhook. Ghost ignores an
// active flag here: a new webhook is always on.
type CreateWebhookRequest struct {
	URL    string   `json:"url"`
	Events []string `json:"events"`
}

// UpdateWebhookRequest is the body for updating a webhook. Nil fields are
// left unchanged. Ghost answers an empty update with a 500, so send at least
// one field. Turning a webhook on clears its DisabledReason.
type UpdateWebhookRequest struct {
	URL    *string  `json:"url,omitempty"`
	Events []string `json:"events,omitempty"`
	Active *bool    `json:"active,omitempty"`
}

// IsEmpty reports whether the update changes nothing.
func (r UpdateWebhookRequest) IsEmpty() bool {
	return r.URL == nil && r.Events == nil && r.Active == nil
}

// WebhookDelivery is one delivery of an event to a webhook.
type WebhookDelivery struct {
	ID     string `json:"id"`
	Event  string `json:"event"`
	Status string `json:"status"`
	// Body is the exact JSON payload that was signed and sent.
	Body     string                   `json:"body"`
	Attempts []WebhookDeliveryAttempt `json:"attempts"`
}

// WebhookDeliveryAttempt is one attempt to deliver a WebhookDelivery.
type WebhookDeliveryAttempt struct {
	StatusCode     *int              `json:"statusCode"`
	Error          *string           `json:"error"`
	RequestHeaders map[string]string `json:"requestHeaders"`
}

// Webhooks manages the webhooks of one repository or organization. Both
// share the same native API shape under different paths.
type Webhooks struct {
	c    *Client
	base []string
}

// RepositoryWebhooks returns the webhooks of the repository owner/repo.
func (c *Client) RepositoryWebhooks(owner, repo string) *Webhooks {
	return &Webhooks{c: c, base: []string{"repositories", owner, repo, "webhooks"}}
}

// OrganizationWebhooks returns the webhooks of the organization with the
// given slug.
func (c *Client) OrganizationWebhooks(org string) *Webhooks {
	return &Webhooks{c: c, base: []string{"organizations", org, "webhooks"}}
}

func (w *Webhooks) path(segments ...string) string {
	return NativePath(append(append([]string{}, w.base...), segments...)...)
}

// List returns all webhooks. Ghost has no endpoint to get a single one.
func (w *Webhooks) List(ctx context.Context) ([]Webhook, error) {
	var out struct {
		Webhooks []Webhook `json:"webhooks"`
	}
	if err := w.c.Do(ctx, http.MethodGet, w.path(), nil, &out); err != nil {
		return nil, err
	}

	return out.Webhooks, nil
}

// Get returns the webhook with the given ID from the list, or a 404 APIError
// when there is none.
func (w *Webhooks) Get(ctx context.Context, id string) (*Webhook, error) {
	hooks, err := w.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range hooks {
		if hooks[i].ID == id {
			return &hooks[i], nil
		}
	}

	return nil, &APIError{StatusCode: http.StatusNotFound, Method: http.MethodGet, Path: w.path(id), Message: "Webhook not found"}
}

// Create creates a webhook. Ghost pings the URL straight away, and the
// response is the only time it returns the secret.
func (w *Webhooks) Create(ctx context.Context, req CreateWebhookRequest) (*Webhook, error) {
	var hook Webhook
	if err := w.c.Do(ctx, http.MethodPost, w.path(), req, &hook); err != nil {
		return nil, err
	}

	return &hook, nil
}

// Update changes the given fields of a webhook.
func (w *Webhooks) Update(ctx context.Context, id string, req UpdateWebhookRequest) (*Webhook, error) {
	var hook Webhook
	if err := w.c.Do(ctx, http.MethodPatch, w.path(id), req, &hook); err != nil {
		return nil, err
	}

	return &hook, nil
}

// Delete deletes a webhook, dropping its pending deliveries.
func (w *Webhooks) Delete(ctx context.Context, id string) error {
	return w.c.Do(ctx, http.MethodDelete, w.path(id), nil, nil)
}

// RotateSecret replaces a webhook's signing secret and returns the new one.
// The old secret stops working at once.
func (w *Webhooks) RotateSecret(ctx context.Context, id string) (string, error) {
	var out struct {
		Secret string `json:"secret"`
	}
	if err := w.c.Do(ctx, http.MethodPost, w.path(id, "secret"), nil, &out); err != nil {
		return "", err
	}

	return out.Secret, nil
}

// ListDeliveries returns a webhook's 50 most recent deliveries.
func (w *Webhooks) ListDeliveries(ctx context.Context, id string) ([]WebhookDelivery, error) {
	var out struct {
		Deliveries []WebhookDelivery `json:"deliveries"`
	}
	if err := w.c.Do(ctx, http.MethodGet, w.path(id, "deliveries"), nil, &out); err != nil {
		return nil, err
	}

	return out.Deliveries, nil
}
