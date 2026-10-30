# Every repository of a user or organization that the token's user can read.
data "ghost_repositories" "all" {
  owner = "my-org"
}

# Only the private repositories whose name or description contains "api".
data "ghost_repositories" "private_api" {
  owner      = "my-org"
  query      = "api"
  visibility = "private"
}

# Add a label to each of them.
resource "ghost_issue_label" "triage" {
  count = length(data.ghost_repositories.private_api.full_names)

  repository = data.ghost_repositories.private_api.full_names[count.index]
  name       = "triage"
  color      = "fbca04"
}

output "repositories" {
  value = data.ghost_repositories.all.full_names
}
