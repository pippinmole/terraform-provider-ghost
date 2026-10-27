package client

import (
	"context"
	"encoding/json"
	"net/http"
)

// Ruleset is a repository ruleset as returned by the native API.
type Ruleset struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Target      string   `json:"target"`
	Enforcement string   `json:"enforcement"`
	Include     []string `json:"include"`
	Exclude     []string `json:"exclude"`
	// BypassActors is nil when Ghost leaves the field out, which it does for
	// callers who aren't repository admins.
	BypassActors *[]BypassActor `json:"bypassActors"`
	Rules        []Rule         `json:"rules"`
}

// RulesetRequest is the body for creating or replacing a ruleset. Every field
// is always sent, because a PUT replaces the whole ruleset.
type RulesetRequest struct {
	Name         string        `json:"name"`
	Target       string        `json:"target"`
	Enforcement  string        `json:"enforcement"`
	Include      []string      `json:"include"`
	Exclude      []string      `json:"exclude"`
	BypassActors []BypassActor `json:"bypassActors"`
	Rules        []Rule        `json:"rules"`
}

// BypassActor is an actor allowed to bypass a ruleset. Role applies to
// RepositoryRole actors and TeamID to Team actors.
type BypassActor struct {
	ActorType  string `json:"actorType"`
	Role       string `json:"role,omitempty"`
	TeamID     string `json:"teamId,omitempty"`
	BypassMode string `json:"bypassMode"`
}

// Rule is one rule in a ruleset. Parameters are decoded per type, into
// PullRequestParameters or RequiredStatusChecksParameters.
type Rule struct {
	Type       string          `json:"type"`
	Parameters json.RawMessage `json:"parameters,omitempty"`
}

// PullRequestParameters are the parameters of a pull_request rule.
type PullRequestParameters struct {
	RequiredApprovingReviewCount   int64    `json:"required_approving_review_count"`
	DismissStaleReviewsOnPush      bool     `json:"dismiss_stale_reviews_on_push"`
	RequireCodeOwnerReview         bool     `json:"require_code_owner_review"`
	RequireLastPushApproval        bool     `json:"require_last_push_approval"`
	RequiredReviewThreadResolution bool     `json:"required_review_thread_resolution"`
	AllowedMergeMethods            []string `json:"allowed_merge_methods"`
}

// RequiredStatusChecksParameters are the parameters of a
// required_status_checks rule.
type RequiredStatusChecksParameters struct {
	RequiredStatusChecks             []StatusCheck `json:"required_status_checks"`
	StrictRequiredStatusChecksPolicy bool          `json:"strict_required_status_checks_policy"`
	DoNotEnforceOnCreate             bool          `json:"do_not_enforce_on_create"`
}

// StatusCheck is a status check context a required_status_checks rule waits
// for.
type StatusCheck struct {
	Context string `json:"context"`
}

// Team is a team in an organization.
type Team struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Slug            string `json:"slug"`
	MemberCount     int64  `json:"memberCount"`
	RepositoryCount int64  `json:"repositoryCount"`
}

func rulesetPath(owner, repo string, id ...string) string {
	return NativePath(append([]string{"repositories", owner, repo, "rulesets"}, id...)...)
}

// withEmptySlices returns req with nil slices replaced by empty ones, so they
// are sent as [] rather than null.
func (req RulesetRequest) withEmptySlices() RulesetRequest {
	if req.Include == nil {
		req.Include = []string{}
	}
	if req.Exclude == nil {
		req.Exclude = []string{}
	}
	if req.BypassActors == nil {
		req.BypassActors = []BypassActor{}
	}
	if req.Rules == nil {
		req.Rules = []Rule{}
	}

	return req
}

// CreateRuleset creates a ruleset on the repository owner/repo.
func (c *Client) CreateRuleset(ctx context.Context, owner, repo string, req RulesetRequest) (*Ruleset, error) {
	var rs Ruleset
	if err := c.Do(ctx, http.MethodPost, rulesetPath(owner, repo), req.withEmptySlices(), &rs); err != nil {
		return nil, err
	}

	return &rs, nil
}

// GetRuleset returns the ruleset with the given id.
func (c *Client) GetRuleset(ctx context.Context, owner, repo, id string) (*Ruleset, error) {
	var rs Ruleset
	if err := c.Do(ctx, http.MethodGet, rulesetPath(owner, repo, id), nil, &rs); err != nil {
		return nil, err
	}

	return &rs, nil
}

// ListRulesets returns every ruleset on the repository owner/repo.
func (c *Client) ListRulesets(ctx context.Context, owner, repo string) ([]Ruleset, error) {
	var out struct {
		Rulesets []Ruleset `json:"rulesets"`
	}
	if err := c.Do(ctx, http.MethodGet, rulesetPath(owner, repo), nil, &out); err != nil {
		return nil, err
	}

	return out.Rulesets, nil
}

// ReplaceRuleset replaces the ruleset with the given id.
func (c *Client) ReplaceRuleset(ctx context.Context, owner, repo, id string, req RulesetRequest) (*Ruleset, error) {
	var rs Ruleset
	if err := c.Do(ctx, http.MethodPut, rulesetPath(owner, repo, id), req.withEmptySlices(), &rs); err != nil {
		return nil, err
	}

	return &rs, nil
}

// DeleteRuleset deletes the ruleset with the given id.
func (c *Client) DeleteRuleset(ctx context.Context, owner, repo, id string) error {
	return c.Do(ctx, http.MethodDelete, rulesetPath(owner, repo, id), nil, nil)
}

// ListOrganizationTeams returns the teams in the organization org.
func (c *Client) ListOrganizationTeams(ctx context.Context, org string) ([]Team, error) {
	var out struct {
		Teams []Team `json:"teams"`
	}
	if err := c.Do(ctx, http.MethodGet, NativePath("organizations", org, "teams"), nil, &out); err != nil {
		return nil, err
	}

	return out.Teams, nil
}
