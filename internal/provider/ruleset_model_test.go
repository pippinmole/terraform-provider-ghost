package provider

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

func TestNormalizeRefPattern(t *testing.T) {
	cases := []struct{ target, in, want string }{
		{"branch", "main", "refs/heads/main"},
		{"tag", "v*", "refs/tags/v*"},
		{"branch", "~DEFAULT_BRANCH", "~DEFAULT_BRANCH"},
		{"tag", "~ALL", "~ALL"},
		{"branch", "refs/tags/v1", "refs/tags/v1"},
	}
	for _, c := range cases {
		if got := normalizeRefPattern(c.target, c.in); got != c.want {
			t.Errorf("normalizeRefPattern(%q, %q) = %q, want %q", c.target, c.in, got, c.want)
		}
	}
}

func TestPatternsFromRemote(t *testing.T) {
	cases := []struct {
		name          string
		target        string
		prior, remote []string
		want          []string
	}{
		{"keeps short spelling", "branch", []string{"main", "~ALL"}, []string{"refs/heads/main", "~ALL"}, []string{"main", "~ALL"}},
		{"import", "branch", nil, []string{"refs/heads/main"}, []string{"refs/heads/main"}},
		{"target change", "tag", []string{"main"}, []string{"refs/tags/main"}, []string{"main"}},
		{"no match", "branch", []string{"a"}, []string{"refs/heads/b"}, []string{"refs/heads/b"}},
		{"duplicates use each prior once", "branch", []string{"x", "refs/heads/x"}, []string{"refs/heads/x", "refs/heads/x"}, []string{"x", "refs/heads/x"}},
		{"nil remote", "branch", []string{"a"}, nil, []string{}},
	}
	for _, c := range cases {
		got := patternsFromRemote(c.target, c.prior, c.remote)
		if got == nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %#v, want %#v", c.name, got, c.want)
		}
	}
}

func TestRulesRoundTrip(t *testing.T) {
	all := rulesetRules{
		Creation: true, Update: true, Deletion: true, NonFastForward: true,
		RequiredLinearHistory: true, RequiredSignatures: true,
		PullRequest: &client.PullRequestParameters{
			RequiredApprovingReviewCount: 3, DismissStaleReviewsOnPush: true, RequireCodeOwnerReview: true,
			RequireLastPushApproval: true, RequiredReviewThreadResolution: true,
			AllowedMergeMethods: []string{"squash", "rebase"},
		},
		RequiredStatusChecks: &client.RequiredStatusChecksParameters{
			RequiredStatusChecks:             []client.StatusCheck{{Context: "ci/build"}, {Context: "ci/test"}},
			StrictRequiredStatusChecksPolicy: true, DoNotEnforceOnCreate: true,
		},
	}
	api := rulesToAPI(all)
	if len(api) != 8 {
		t.Fatalf("rulesToAPI emitted %d rules, want 8", len(api))
	}
	wantOrder := []string{
		"creation", "update", "deletion", "non_fast_forward", "required_linear_history",
		"required_signatures", "pull_request", "required_status_checks",
	}
	for i, r := range api {
		if r.Type != wantOrder[i] {
			t.Errorf("rule %d = %q, want %q", i, r.Type, wantOrder[i])
		}
	}
	back, err := rulesFromAPI(api)
	if err != nil {
		t.Fatalf("rulesFromAPI: %v", err)
	}
	if !reflect.DeepEqual(back, all) {
		t.Errorf("round trip:\n got %+v\nwant %+v", back, all)
	}

	if none := rulesToAPI(rulesetRules{}); none == nil || len(none) != 0 {
		t.Errorf("rulesToAPI(empty) = %#v, want empty non-nil", none)
	}

	emptyChecks := rulesetRules{RequiredStatusChecks: &client.RequiredStatusChecksParameters{RequiredStatusChecks: []client.StatusCheck{}}}
	back, err = rulesFromAPI(rulesToAPI(emptyChecks))
	if err != nil {
		t.Fatalf("rulesFromAPI: %v", err)
	}
	if back.RequiredStatusChecks == nil || back.RequiredStatusChecks.RequiredStatusChecks == nil ||
		len(back.RequiredStatusChecks.RequiredStatusChecks) != 0 {
		t.Errorf("empty required checks round trip = %+v", back.RequiredStatusChecks)
	}
	if back.empty() {
		t.Error("a required_status_checks rule with no checks is not empty")
	}
	if !(rulesetRules{}).empty() {
		t.Error("zero rules should be empty")
	}
}

func TestRulesFromAPIUnknownType(t *testing.T) {
	_, err := rulesFromAPI([]client.Rule{{Type: "merge_queue"}})
	if err == nil || !strings.Contains(err.Error(), "merge_queue") {
		t.Errorf("err = %v, want one naming merge_queue", err)
	}
}

func TestRulesFromAPIIgnoresParamsOnFlags(t *testing.T) {
	r, err := rulesFromAPI([]client.Rule{{Type: "deletion", Parameters: []byte(`{}`)}})
	if err != nil || !r.Deletion {
		t.Errorf("got %+v, %v", r, err)
	}
}

func TestBypassActorKey(t *testing.T) {
	cases := []struct {
		a    client.BypassActor
		want string
	}{
		{client.BypassActor{ActorType: "RepositoryRole", Role: "maintain", BypassMode: "always"}, "RepositoryRole:maintain"},
		{client.BypassActor{ActorType: "Team", TeamID: "T1", BypassMode: "always"}, "Team:T1"},
		{client.BypassActor{ActorType: "OrganizationAdmin", BypassMode: "always"}, "OrganizationAdmin"},
	}
	for _, c := range cases {
		if got := bypassActorKey(c.a); got != c.want {
			t.Errorf("bypassActorKey(%+v) = %q, want %q", c.a, got, c.want)
		}
	}
	a := client.BypassActor{ActorType: "OrganizationAdmin", BypassMode: "always"}
	b := client.BypassActor{ActorType: "OrganizationAdmin", BypassMode: "pull_request"}
	if bypassActorKey(a) != bypassActorKey(b) {
		t.Error("actors differing only in bypass mode should share a key")
	}
}

func TestParseRulesetID(t *testing.T) {
	repo, id, err := parseRulesetID("alice/repo:ruleset_x")
	if err != nil || repo != "alice/repo" || id != "ruleset_x" {
		t.Errorf("got %q %q %v", repo, id, err)
	}
	_, id, err = parseRulesetID("alice/repo:a:b")
	if err != nil || id != "a:b" {
		t.Errorf("got %q %v", id, err)
	}
	for _, bad := range []string{"alice/repo", ":x", "alice/repo:"} {
		if _, _, err := parseRulesetID(bad); err == nil {
			t.Errorf("parseRulesetID(%q) should fail", bad)
		}
	}
	if got := rulesetID("alice/repo", "ruleset_x"); got != "alice/repo:ruleset_x" {
		t.Errorf("rulesetID = %q", got)
	}
}
