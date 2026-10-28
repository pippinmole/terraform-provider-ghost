resource "ghost_repository" "example" {
  name = "example"
}

# Get notified about every issue, pull request and discussion thread in the
# repository. Destroying the resource sets the level back to "participating".
resource "ghost_repository_subscription" "example" {
  repository = ghost_repository.example.full_name
  level      = "all"

  # A replaced repository starts at "participating" again; watch it again.
  lifecycle {
    replace_triggered_by = [ghost_repository.example.repo_id]
  }
}

# Other people's public repositories can be watched or ignored too.
resource "ghost_repository_subscription" "noisy" {
  repository = "some-org/noisy-repo"
  level      = "ignore"
}
