# By slug. Ghost matches it case-insensitively, but it changes when the team
# is renamed.
data "ghost_team" "core" {
  organization = "my-org"
  slug         = "core"
}

# By ID, which never changes.
data "ghost_team" "by_id" {
  organization = "my-org"
  id           = "DG4eRcoHLcE1RfixZDetBcBzEQBBYIbb"
}

# Let the team bypass a ruleset.
resource "ghost_repository_ruleset" "protect_main" {
  repository  = "my-org/my-repo"
  name        = "protect-main"
  enforcement = "active"

  conditions = {
    ref_name = {
      include = ["~DEFAULT_BRANCH"]
    }
  }

  bypass_actors = [
    { actor_type = "Team", team_id = data.ghost_team.core.id, bypass_mode = "pull_request" },
  ]

  rules = {
    deletion = true
  }
}

output "core_team_members" {
  value = [for m in data.ghost_team.core.members : m.username]
}
