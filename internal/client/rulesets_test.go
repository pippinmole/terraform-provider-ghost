package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

const rulesetJSON = `{"id":"ruleset_abc","name":"protect","target":"branch","enforcement":"active",` +
	`"include":["refs/heads/main"],"exclude":[],` +
	`"bypassActors":[{"actorType":"Team","teamId":"T1","bypassMode":"always"}],` +
	`"rules":[{"type":"deletion"},{"type":"pull_request","parameters":{"required_approving_review_count":2,` +
	`"dismiss_stale_reviews_on_push":true,"require_code_owner_review":false,"require_last_push_approval":false,` +
	`"required_review_thread_resolution":false,"allowed_merge_methods":["squash"]}}]}`

var rulesetKeys = []string{"name", "target", "enforcement", "include", "exclude", "bypassActors", "rules"}

func assertFullRulesetBody(t *testing.T, body map[string]any) {
	t.Helper()

	if len(body) != len(rulesetKeys) {
		t.Errorf("body has %d keys, want %d: %v", len(body), len(rulesetKeys), body)
	}
	for _, k := range rulesetKeys {
		if _, ok := body[k]; !ok {
			t.Errorf("body missing %q: %v", k, body)
		}
	}
	for _, k := range []string{"include", "exclude", "bypassActors", "rules"} {
		if _, ok := body[k].([]any); !ok {
			t.Errorf("%s = %#v, want a JSON array", k, body[k])
		}
	}
}

func TestCreateRuleset(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/repositories/octo/r/rulesets" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		body := readBody(t, r)
		assertFullRulesetBody(t, body)
		if body["name"] != "protect" {
			t.Errorf("unexpected body: %v", body)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(rulesetJSON))
	})

	rs, err := c.CreateRuleset(context.Background(), "octo", "r", RulesetRequest{
		Name: "protect", Target: "branch", Enforcement: "active",
	})
	if err != nil {
		t.Fatalf("CreateRuleset: %v", err)
	}
	if rs.ID != "ruleset_abc" || rs.Name != "protect" || len(rs.Include) != 1 || len(rs.Rules) != 2 {
		t.Errorf("unexpected ruleset: %+v", rs)
	}
	if rs.BypassActors == nil || len(*rs.BypassActors) != 1 || (*rs.BypassActors)[0].TeamID != "T1" {
		t.Errorf("unexpected bypass actors: %+v", rs.BypassActors)
	}
	var p PullRequestParameters
	if err := json.Unmarshal(rs.Rules[1].Parameters, &p); err != nil || p.RequiredApprovingReviewCount != 2 {
		t.Errorf("pull_request parameters = %+v (%v)", p, err)
	}
}

func TestGetRulesetWithoutBypassActors(t *testing.T) {
	resp := `{"id":"ruleset_abc","name":"n","target":"branch","enforcement":"active","include":[],"exclude":[],"rules":[]}`
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/repositories/octo/r/rulesets/ruleset_abc" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(resp))
	})

	rs, err := c.GetRuleset(context.Background(), "octo", "r", "ruleset_abc")
	if err != nil {
		t.Fatalf("GetRuleset: %v", err)
	}
	if rs.BypassActors != nil {
		t.Errorf("BypassActors = %v, want nil when omitted", *rs.BypassActors)
	}

	resp = `{"id":"ruleset_abc","name":"n","bypassActors":[]}`
	rs, err = c.GetRuleset(context.Background(), "octo", "r", "ruleset_abc")
	if err != nil {
		t.Fatalf("GetRuleset: %v", err)
	}
	if rs.BypassActors == nil || len(*rs.BypassActors) != 0 {
		t.Errorf("BypassActors = %v, want non-nil empty", rs.BypassActors)
	}
}

func TestListRulesets(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/repositories/octo/r/rulesets" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"rulesets":[{"id":"ruleset_a","name":"a"},{"id":"ruleset_b","name":"b"}]}`))
	})

	list, err := c.ListRulesets(context.Background(), "octo", "r")
	if err != nil {
		t.Fatalf("ListRulesets: %v", err)
	}
	if len(list) != 2 || list[0].ID != "ruleset_a" || list[1].Name != "b" {
		t.Errorf("unexpected list: %+v", list)
	}
}

func TestListRulesetSummaries(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v3/repos/octo/r/rulesets" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"id":75,"name":"a","target":"tag","enforcement":"evaluate","node_id":"RRS_x",` +
			`"_links":{"self":{"href":"https://api.example/api/v3/repos/octo/r/rulesets/75"},` +
			`"html":{"href":"https://web.example/octo/r/rules/75"}}}]`))
	})

	list, err := c.ListRulesetSummaries(context.Background(), "octo", "r")
	if err != nil {
		t.Fatalf("ListRulesetSummaries: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d summaries, want 1", len(list))
	}
	got := list[0]
	if got.ID != 75 || got.Name != "a" || got.NodeID != "RRS_x" || got.Target != "tag" || got.Enforcement != "evaluate" ||
		got.Links.HTML.Href != "https://web.example/octo/r/rules/75" {
		t.Errorf("unexpected summary: %+v", got)
	}
}

func TestReplaceRuleset(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/repositories/octo/r/rulesets/ruleset_abc" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		body := readBody(t, r)
		assertFullRulesetBody(t, body)
		rules, _ := body["rules"].([]any)
		if len(rules) != 1 {
			t.Fatalf("unexpected rules: %v", rules)
		}
		if rule, _ := rules[0].(map[string]any); rule["type"] != "deletion" {
			t.Errorf("unexpected rules: %v", rules)
		}
		_, _ = w.Write([]byte(rulesetJSON))
	})

	_, err := c.ReplaceRuleset(context.Background(), "octo", "r", "ruleset_abc", RulesetRequest{
		Name: "protect", Target: "branch", Enforcement: "active",
		Rules: []Rule{{Type: "deletion"}},
	})
	if err != nil {
		t.Fatalf("ReplaceRuleset: %v", err)
	}
}

func TestDeleteRuleset(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/repositories/octo/r/rulesets/ruleset_abc" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	if err := c.DeleteRuleset(context.Background(), "octo", "r", "ruleset_abc"); err != nil {
		t.Fatalf("DeleteRuleset: %v", err)
	}
}

func TestIsConflict(t *testing.T) {
	if !IsConflict(&APIError{StatusCode: http.StatusConflict}) {
		t.Error("409 should be a conflict")
	}
	if IsConflict(&APIError{StatusCode: http.StatusNotFound}) {
		t.Error("404 should not be a conflict")
	}
	if IsConflict(errors.New("boom")) {
		t.Error("plain error should not be a conflict")
	}
}

func TestPullRequestParametersMarshal(t *testing.T) {
	b, err := json.Marshal(PullRequestParameters{})
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{
		"required_approving_review_count", "dismiss_stale_reviews_on_push", "require_code_owner_review",
		"require_last_push_approval", "required_review_thread_resolution", "allowed_merge_methods",
	} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %q in %s", k, b)
		}
	}
}
