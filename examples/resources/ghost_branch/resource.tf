# A branch needs something to start from, so the repository must have at
# least one commit.
resource "ghost_repository" "example" {
  name      = "example"
  auto_init = true
}

# A branch started at the default branch.
resource "ghost_branch" "development" {
  repository = ghost_repository.example.full_name
  branch     = "development"

  # Replacing the repository deletes its branches; this recreates the branch
  # in the same apply. repo_id doesn't change when the repository is renamed.
  lifecycle {
    replace_triggered_by = [ghost_repository.example.repo_id]
  }
}

# A branch started at another branch. source also accepts a tag or a commit
# SHA.
resource "ghost_branch" "release" {
  repository = ghost_repository.example.full_name
  branch     = "release/1.x"
  source     = ghost_branch.development.branch
}
