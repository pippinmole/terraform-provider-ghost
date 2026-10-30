data "ghost_team" "core" {
  organization = "my-org"
  slug         = "core"
}

# Make an existing member of the organization a maintainer of the team.
# Organization invitations can't be sent with an API token, so invite the
# user in the Ghost web UI first.
resource "ghost_team_membership" "alice" {
  organization = "my-org"
  team_id      = data.ghost_team.core.id
  username     = "alice"
  role         = "maintainer"
}

# Or name the team by its slug. role defaults to "member".
resource "ghost_team_membership" "bob" {
  organization = "my-org"
  team_slug    = "core"
  username     = "bob"
}
