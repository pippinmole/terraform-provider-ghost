resource "ghost_repository" "example" {
  owner = "my-org"
  name  = "example"
}

data "ghost_team" "core" {
  organization = "my-org"
  slug         = "core"
}

# Give every member of the team write access to the repository.
resource "ghost_team_repository" "core" {
  repository = ghost_repository.example.full_name
  team_id    = data.ghost_team.core.id
  role       = "write"

  # Recreate the access if the repository is replaced, which drops it.
  lifecycle {
    replace_triggered_by = [ghost_repository.example.repo_id]
  }
}

# Or name the team by its slug, without a data source.
resource "ghost_team_repository" "docs" {
  repository = ghost_repository.example.full_name
  team_slug  = "docs"
  role       = "triage"
}
