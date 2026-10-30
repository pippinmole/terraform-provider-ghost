package client

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

const sshSigningKeyJSON = `{"id":7,"key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKDvL6hsOgtbepvkxOnAUmujWpjGaIYBYMNqUkc9qQ2R",` +
	`"title":"laptop","created_at":"2026-10-28T00:28:51.723Z"}`

func TestCreateSSHSigningKey(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v3/user/ssh_signing_keys" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		body := readBody(t, r)
		if len(body) != 2 || body["title"] != "laptop" || body["key"] != "ssh-ed25519 AAAA comment" {
			t.Errorf("unexpected body: %v", body)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(sshSigningKeyJSON))
	})

	k, err := c.CreateSSHSigningKey(context.Background(), CreateSSHSigningKeyRequest{Title: "laptop", Key: "ssh-ed25519 AAAA comment"})
	if err != nil {
		t.Fatalf("CreateSSHSigningKey: %v", err)
	}
	if k.ID != 7 || k.Title != "laptop" || !strings.HasPrefix(k.Key, "ssh-ed25519 AAAAC3") || k.CreatedAt != "2026-10-28T00:28:51.723Z" {
		t.Errorf("unexpected key: %+v", k)
	}
}

func TestCreateSSHSigningKeyDuplicate(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"Validation Failed","errors":[{"resource":"PublicKey","field":"key","code":"already_exists"}],"status":"422"}`))
	})

	_, err := c.CreateSSHSigningKey(context.Background(), CreateSSHSigningKeyRequest{Title: "t", Key: "k"})
	if err == nil || !strings.Contains(err.Error(), "422 Validation Failed: key already_exists") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetSSHSigningKey(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v3/user/ssh_signing_keys/7" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(sshSigningKeyJSON))
	})

	k, err := c.GetSSHSigningKey(context.Background(), 7)
	if err != nil {
		t.Fatalf("GetSSHSigningKey: %v", err)
	}
	if k.ID != 7 || k.Title != "laptop" {
		t.Errorf("unexpected key: %+v", k)
	}
}

func TestGetSSHSigningKeyNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found","documentation_url":"https://docs.github.com/rest","status":"404"}`))
	})

	if _, err := c.GetSSHSigningKey(context.Background(), 7); !IsNotFound(err) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestListSSHSigningKeys(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v3/user/ssh_signing_keys" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`[` + sshSigningKeyJSON + `]`))
	})

	keys, err := c.ListSSHSigningKeys(context.Background())
	if err != nil {
		t.Fatalf("ListSSHSigningKeys: %v", err)
	}
	if len(keys) != 1 || keys[0].ID != 7 {
		t.Errorf("unexpected keys: %+v", keys)
	}
}

func TestDeleteSSHSigningKey(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v3/user/ssh_signing_keys/7" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	if err := c.DeleteSSHSigningKey(context.Background(), 7); err != nil {
		t.Fatalf("DeleteSSHSigningKey: %v", err)
	}
}
