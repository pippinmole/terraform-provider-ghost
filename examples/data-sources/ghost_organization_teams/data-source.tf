data "ghost_organization_teams" "all" {
  organization = "my-org"
}

# Look up every team in full, keyed by slug.
data "ghost_team" "each" {
  for_each = { for t in data.ghost_organization_teams.all.teams : t.slug => t.id }

  organization = "my-org"
  id           = each.value
}

output "team_ids" {
  value = { for t in data.ghost_organization_teams.all.teams : t.slug => t.id }
}

output "team_members" {
  value = { for slug, t in data.ghost_team.each : slug => [for m in t.members : m.username] }
}
