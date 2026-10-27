package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

// Ghost's rule types, in the order Ghost returns them.
const (
	ruleCreation              = "creation"
	ruleUpdate                = "update"
	ruleDeletion              = "deletion"
	ruleNonFastForward        = "non_fast_forward"
	ruleRequiredLinearHistory = "required_linear_history"
	ruleRequiredSignatures    = "required_signatures"
	rulePullRequest           = "pull_request"
	ruleRequiredStatusChecks  = "required_status_checks"
)

// rulesetRules is the domain form of the `rules` attribute. A nil
// PullRequest or RequiredStatusChecks means that rule is off.
type rulesetRules struct {
	Creation              bool
	Update                bool
	Deletion              bool
	NonFastForward        bool
	RequiredLinearHistory bool
	RequiredSignatures    bool
	PullRequest           *client.PullRequestParameters
	RequiredStatusChecks  *client.RequiredStatusChecksParameters
}

// empty reports whether no rule is on.
func (r rulesetRules) empty() bool {
	return !r.Creation && !r.Update && !r.Deletion && !r.NonFastForward && !r.RequiredLinearHistory &&
		!r.RequiredSignatures && r.PullRequest == nil && r.RequiredStatusChecks == nil
}

// rulesToAPI returns the rules that are on, in Ghost's order. It never
// returns nil.
func rulesToAPI(r rulesetRules) []client.Rule {
	out := []client.Rule{}
	for _, flag := range []struct {
		on  bool
		typ string
	}{
		{r.Creation, ruleCreation},
		{r.Update, ruleUpdate},
		{r.Deletion, ruleDeletion},
		{r.NonFastForward, ruleNonFastForward},
		{r.RequiredLinearHistory, ruleRequiredLinearHistory},
		{r.RequiredSignatures, ruleRequiredSignatures},
	} {
		if flag.on {
			out = append(out, client.Rule{Type: flag.typ})
		}
	}
	if r.PullRequest != nil {
		p := *r.PullRequest
		if p.AllowedMergeMethods == nil {
			p.AllowedMergeMethods = []string{}
		}
		out = append(out, client.Rule{Type: rulePullRequest, Parameters: mustMarshal(p)})
	}
	if r.RequiredStatusChecks != nil {
		p := *r.RequiredStatusChecks
		if p.RequiredStatusChecks == nil {
			p.RequiredStatusChecks = []client.StatusCheck{}
		}
		out = append(out, client.Rule{Type: ruleRequiredStatusChecks, Parameters: mustMarshal(p)})
	}

	return out
}

// mustMarshal marshals v, which must be one of the plain parameter structs.
func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("marshalling %T: %v", v, err))
	}

	return b
}

// rulesFromAPI maps Ghost's rule list onto rulesetRules. It fails on a rule
// type this provider doesn't know, rather than silently dropping it.
func rulesFromAPI(rules []client.Rule) (rulesetRules, error) {
	var r rulesetRules
	for _, rule := range rules {
		switch rule.Type {
		case ruleCreation:
			r.Creation = true
		case ruleUpdate:
			r.Update = true
		case ruleDeletion:
			r.Deletion = true
		case ruleNonFastForward:
			r.NonFastForward = true
		case ruleRequiredLinearHistory:
			r.RequiredLinearHistory = true
		case ruleRequiredSignatures:
			r.RequiredSignatures = true
		case rulePullRequest:
			var p client.PullRequestParameters
			if err := json.Unmarshal(rule.Parameters, &p); err != nil {
				return r, fmt.Errorf("decoding pull_request parameters: %w", err)
			}
			if p.AllowedMergeMethods == nil {
				p.AllowedMergeMethods = []string{}
			}
			r.PullRequest = &p
		case ruleRequiredStatusChecks:
			var p client.RequiredStatusChecksParameters
			if err := json.Unmarshal(rule.Parameters, &p); err != nil {
				return r, fmt.Errorf("decoding required_status_checks parameters: %w", err)
			}
			if p.RequiredStatusChecks == nil {
				p.RequiredStatusChecks = []client.StatusCheck{}
			}
			r.RequiredStatusChecks = &p
		default:
			return r, fmt.Errorf("ruleset has a rule of type %q, which this provider does not support", rule.Type)
		}
	}

	return r, nil
}

// normalizeRefPattern returns pattern the way Ghost stores it: prefixed with
// refs/heads/ (branch) or refs/tags/ (tag), unless it is a ~ token or
// already starts with refs/.
func normalizeRefPattern(target, pattern string) string {
	if strings.HasPrefix(pattern, "~") || strings.HasPrefix(pattern, "refs/") {
		return pattern
	}
	if target == "tag" {
		return "refs/tags/" + pattern
	}

	return "refs/heads/" + pattern
}

// patternsFromRemote keeps the prior spelling of each remote pattern when it
// normalises to the same ref (e.g. "main" for "refs/heads/main"), so Ghost's
// rewriting doesn't produce a diff. Each prior pattern is used at most once.
func patternsFromRemote(target string, prior, remote []string) []string {
	out := make([]string, 0, len(remote))
	used := make([]bool, len(prior))
	for _, r := range remote {
		kept := r
		for i, p := range prior {
			if !used[i] && normalizeRefPattern(target, p) == r {
				used[i] = true
				kept = p

				break
			}
		}
		out = append(out, kept)
	}

	return out
}

// bypassActorKey identifies a bypass actor the way Ghost deduplicates them:
// by type and role or team, ignoring the bypass mode.
func bypassActorKey(a client.BypassActor) string {
	switch a.ActorType {
	case "RepositoryRole":
		return a.ActorType + ":" + a.Role
	case "Team":
		return a.ActorType + ":" + a.TeamID
	default:
		return a.ActorType
	}
}

// rulesetID returns the resource id for a ruleset: "owner/slug:ruleset_id".
func rulesetID(repository, rulesetID string) string {
	return repository + ":" + rulesetID
}

// parseRulesetID splits an "owner/slug:ruleset_id" id at the first colon.
func parseRulesetID(id string) (repository, rulesetID string, err error) {
	repository, rulesetID, ok := strings.Cut(id, ":")
	if _, _, splitErr := client.SplitFullName(repository); !ok || rulesetID == "" || splitErr != nil {
		return "", "", fmt.Errorf("import ID must be \"owner/repo:ruleset_id\", got %q", id)
	}

	return repository, rulesetID, nil
}
