# Every branch of a repository.
data "ghost_branches" "all" {
  repository = "my-org/my-repo"
}

# Only the branches an active ruleset protects, with the commit each points
# at. include_sha costs one API request per branch.
data "ghost_branches" "protected" {
  repository              = "my-org/my-repo"
  only_protected_branches = true
  include_sha             = true
}

output "default_branch" {
  value = data.ghost_branches.all.default_branch
}

output "release_branches" {
  value = [for name in data.ghost_branches.all.names : name if startswith(name, "release/")]
}

output "protected_shas" {
  value = { for b in data.ghost_branches.protected.branches : b.name => b.sha }
}
