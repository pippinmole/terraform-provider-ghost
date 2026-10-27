resource "ghost_repository" "example" {
  name      = "example"
  auto_init = true

  # Don't set default_branch here as well: ghost_branch_default manages the
  # same setting, and the two would keep undoing each other's change.
}

resource "ghost_branch" "development" {
  repository = ghost_repository.example.full_name
  branch     = "development"
}

# Referencing the ghost_branch creates the branch first. Destroying this
# resource sets the default branch back to main, so the branch can then be
# deleted.
resource "ghost_branch_default" "example" {
  repository = ghost_repository.example.full_name
  branch     = ghost_branch.development.branch
}
