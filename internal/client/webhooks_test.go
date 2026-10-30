package client

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

const testWebhookJSON = `{"id":"whk_1","url":"https://example.com/hook","events":["push","issue.opened"],"active":true,"disabledReason":null,"createdAt":"2026-10-27T22:25:51.268Z"}`

func TestWebhookPaths(t *testing.T) {
	c, err := New("https://ghost.test", "")
	if err != nil {
		t.Fatal(err)
	}

	if got, want := c.RepositoryWebhooks("octo", "my repo").path("whk_1", "secret"), "/api/repositories/octo/my%20repo/webhooks/whk_1/secret"; got != want {
		t.Errorf("repository path = %q, want %q", got, want)
	}
	if got, want := c.OrganizationWebhooks("acme").path(), "/api/organizations/acme/webhooks"; got != want {
		t.Errorf("organization path = %q, want %q", got, want)
	}
}

func TestWebhooksList(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/repositories/octo/r/webhooks" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"webhooks":[` + testWebhookJSON + `,{"id":"whk_2","url":"https://x.test","events":["push"],"active":false,"disabledReason":"410 Gone","createdAt":"x"}]}`))
	})

	hooks, err := c.RepositoryWebhooks("octo", "r").List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(hooks) != 2 || hooks[0].ID != "whk_1" || hooks[0].DisabledReason != nil || !hooks[0].Active {
		t.Errorf("unexpected hooks: %+v", hooks)
	}
	if hooks[1].Active || hooks[1].DisabledReason == nil || *hooks[1].DisabledReason != "410 Gone" {
		t.Errorf("disabled hook not decoded: %+v", hooks[1])
	}
}

func TestWebhooksGet(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"webhooks":[` + testWebhookJSON + `]}`))
	})
	hooks := c.RepositoryWebhooks("octo", "r")

	hook, err := hooks.Get(context.Background(), "whk_1")
	if err != nil || hook.URL != "https://example.com/hook" || len(hook.Events) != 2 {
		t.Fatalf("Get = %+v, %v", hook, err)
	}

	if _, err := hooks.Get(context.Background(), "whk_missing"); !IsNotFound(err) {
		t.Errorf("Get of a missing webhook: got %v, want a 404", err)
	}
}

func TestWebhooksCreate(t *testing.T) {
	var body map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/organizations/acme/webhooks" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		body = readBody(t, r)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"whk_1","url":"https://example.com/hook","events":["push"],"active":true,"disabledReason":null,"createdAt":"x","secret":"whsec_abc"}`))
	})

	hook, err := c.OrganizationWebhooks("acme").Create(context.Background(), CreateWebhookRequest{URL: "https://example.com/hook", Events: []string{"push"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if hook.Secret != "whsec_abc" {
		t.Errorf("secret not decoded: %+v", hook)
	}
	if body["url"] != "https://example.com/hook" || len(body) != 2 {
		t.Errorf("unexpected body: %v", body)
	}
}

func TestWebhooksUpdateSendsOnlySetFields(t *testing.T) {
	var raw []byte
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.EscapedPath(), "/api/repositories/octo/r/webhooks/whk_1"; r.Method != http.MethodPatch || got != want {
			t.Errorf("got %s %s, want PATCH %s", r.Method, got, want)
		}
		m := readBody(t, r)
		raw, _ = json.Marshal(m)
		_, _ = w.Write([]byte(testWebhookJSON))
	})
	hooks := c.RepositoryWebhooks("octo", "r")

	off := false
	if _, err := hooks.Update(context.Background(), "whk_1", UpdateWebhookRequest{Active: &off}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if string(raw) != `{"active":false}` {
		t.Errorf("body = %s", raw)
	}

	if _, err := hooks.Update(context.Background(), "whk_1", UpdateWebhookRequest{Events: []string{"push"}}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if string(raw) != `{"events":["push"]}` {
		t.Errorf("body = %s", raw)
	}
}

func TestUpdateWebhookRequestIsEmpty(t *testing.T) {
	u := "https://x.test"
	if !(UpdateWebhookRequest{}).IsEmpty() || (UpdateWebhookRequest{URL: &u}).IsEmpty() || (UpdateWebhookRequest{Events: []string{"push"}}).IsEmpty() {
		t.Error("IsEmpty is wrong")
	}
}

func TestWebhooksDeleteNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/repositories/octo/r/webhooks/whk_1" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"statusCode":404,"message":"Webhook not found"}`))
	})

	err := c.RepositoryWebhooks("octo", "r").Delete(context.Background(), "whk_1")
	if !IsNotFound(err) {
		t.Errorf("got %v, want a 404", err)
	}
}

func TestWebhooksRotateSecret(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/repositories/octo/r/webhooks/whk_1/secret" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"secret":"whsec_new"}`))
	})

	secret, err := c.RepositoryWebhooks("octo", "r").RotateSecret(context.Background(), "whk_1")
	if err != nil || secret != "whsec_new" {
		t.Errorf("RotateSecret = %q, %v", secret, err)
	}
}

func TestWebhooksListDeliveries(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/repositories/octo/r/webhooks/whk_1/deliveries" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"deliveries":[{"id":"d1","event":"ping","status":"succeeded","body":"{}","attempts":[{"statusCode":200,"error":null,"requestHeaders":{"X-Hub-Signature-256":"sha256=ab"}}]}]}`))
	})

	ds, err := c.RepositoryWebhooks("octo", "r").ListDeliveries(context.Background(), "whk_1")
	if err != nil {
		t.Fatalf("ListDeliveries: %v", err)
	}
	if len(ds) != 1 || ds[0].Event != "ping" || len(ds[0].Attempts) != 1 ||
		*ds[0].Attempts[0].StatusCode != 200 || ds[0].Attempts[0].RequestHeaders["X-Hub-Signature-256"] != "sha256=ab" {
		t.Errorf("unexpected deliveries: %+v", ds)
	}
}
