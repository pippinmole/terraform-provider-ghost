package client

import (
	"context"
	"net/http"
	"testing"
)

const labelJSON = `{"id":13,"node_id":"LA_1","url":"https://ghost.test/api/v3/repos/octo/r/labels/bug",` +
	`"name":"bug","color":"ff0000","description":"d","default":false,"archived_at":null,"archived_by":null}`

func TestCreateLabel(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v3/repos/octo/r/labels" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		body := readBody(t, r)
		if len(body) != 2 || body["name"] != "bug" || body["color"] != "ff0000" {
			t.Errorf("unexpected body: %v", body)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(labelJSON))
	})

	l, err := c.CreateLabel(context.Background(), "octo", "r", CreateLabelRequest{Name: "bug", Color: "ff0000"})
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	if l.ID != 13 || l.NodeID != "LA_1" || l.Name != "bug" || l.Color != "ff0000" ||
		l.Description == nil || *l.Description != "d" || l.Default || l.URL == "" {
		t.Errorf("unexpected label: %+v", l)
	}
}

func TestUpdateLabelSendsNullDescription(t *testing.T) {
	var body map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/api/v3/repos/octo/r/labels/bug" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		body = readBody(t, r)
		_, _ = w.Write([]byte(labelJSON))
	})

	if _, err := c.UpdateLabel(context.Background(), "octo", "r", "bug", UpdateLabelRequest{Color: "00ff00"}); err != nil {
		t.Fatalf("UpdateLabel: %v", err)
	}
	desc, ok := body["description"]
	if !ok || desc != nil {
		t.Errorf("description should be sent as null: %v", body)
	}
	if _, ok := body["new_name"]; ok || body["color"] != "00ff00" {
		t.Errorf("unexpected body: %v", body)
	}

	newName := "defect"
	if _, err := c.UpdateLabel(context.Background(), "octo", "r", "bug", UpdateLabelRequest{NewName: &newName, Color: "00ff00"}); err != nil {
		t.Fatalf("UpdateLabel: %v", err)
	}
	if body["new_name"] != "defect" {
		t.Errorf("new_name not sent: %v", body)
	}
}

func TestLabelPathEscaping(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.EscapedPath(), "/api/v3/repos/octo/r/labels/a%2Fb:%20c%3F%23"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		if r.URL.RawQuery != "" {
			t.Errorf("query = %q, want none", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(labelJSON))
	})

	if _, err := c.GetLabel(context.Background(), "octo", "r", "a/b: c?#"); err != nil {
		t.Fatalf("GetLabel: %v", err)
	}
}

func TestDeleteLabel(t *testing.T) {
	status := http.StatusNoContent
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v3/repos/octo/r/labels/bug" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(status)
	})

	if err := c.DeleteLabel(context.Background(), "octo", "r", "bug"); err != nil {
		t.Fatalf("DeleteLabel: %v", err)
	}
	status = http.StatusNotFound
	if err := c.DeleteLabel(context.Background(), "octo", "r", "bug"); !IsNotFound(err) {
		t.Fatalf("expected not found, got %v", err)
	}
}
