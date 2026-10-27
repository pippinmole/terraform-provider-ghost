package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

// bypassActorsValidator checks what Ghost would otherwise silently change:
// role and team_id must be set exactly when the actor type uses them, and no
// two actors may be the same actor, since Ghost keeps only the first.
// Elements with unknown values skip the checks that need them.
func bypassActorsValidator() validator.Set {
	return bypassActorsSetValidator{}
}

type bypassActorsSetValidator struct{}

func (v bypassActorsSetValidator) Description(_ context.Context) string {
	return "role is set only for RepositoryRole actors, team_id only for Team actors, and no actor appears twice"
}

func (v bypassActorsSetValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v bypassActorsSetValidator) ValidateSet(ctx context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	seen := map[string]bool{}
	for _, elem := range req.ConfigValue.Elements() {
		obj, ok := elem.(types.Object)
		if !ok || obj.IsUnknown() || obj.IsNull() {
			continue
		}
		var a rulesetBypassActorModel
		resp.Diagnostics.Append(obj.As(ctx, &a, basetypes.ObjectAsOptions{})...)
		if resp.Diagnostics.HasError() {
			return
		}
		if a.ActorType.IsUnknown() || a.ActorType.IsNull() {
			continue
		}
		actorType := a.ActorType.ValueString()

		checkField := func(name string, value types.String, usedBy string) bool {
			switch {
			case actorType == usedBy && value.IsNull():
				resp.Diagnostics.AddAttributeError(req.Path, "Missing bypass actor "+name,
					fmt.Sprintf("%s is required for %s bypass actors.", name, usedBy))
				return false
			case actorType != usedBy && !value.IsNull() && !value.IsUnknown():
				resp.Diagnostics.AddAttributeError(req.Path, "Unexpected bypass actor "+name,
					fmt.Sprintf("%s is not allowed for %s bypass actors; it applies only to %s actors.", name, actorType, usedBy))
				return false
			}
			return true
		}
		roleOK := checkField("role", a.Role, "RepositoryRole")
		teamOK := checkField("team_id", a.TeamID, "Team")
		if !roleOK || !teamOK || a.Role.IsUnknown() || a.TeamID.IsUnknown() {
			continue
		}

		key := bypassActorKey(client.BypassActor{
			ActorType: actorType, Role: a.Role.ValueString(), TeamID: a.TeamID.ValueString(),
		})
		if seen[key] {
			resp.Diagnostics.AddAttributeError(req.Path, "Duplicate bypass actor",
				fmt.Sprintf("Duplicate bypass actor %s: Ghost keeps only the first. Each actor may appear once, "+
					"whatever its bypass_mode.", key))
			continue
		}
		seen[key] = true
	}
}
