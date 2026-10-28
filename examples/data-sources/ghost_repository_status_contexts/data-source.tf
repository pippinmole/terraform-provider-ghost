# The status check contexts CI has reported on a repository, most recent
# first (at most 50). Listing them needs write access to the repository.
data "ghost_repository_status_contexts" "app" {
  repository = "my-org/my-repo"
}

# Require every CI check the repository has seen, except the deploy previews.
resource "ghost_repository_ruleset" "main" {
  repository  = "my-org/my-repo"
  name        = "protect-main"
  enforcement = "active"

  conditions = {
    ref_name = { include = ["~DEFAULT_BRANCH"] }
  }

  rules = {
    required_status_checks = {
      required_check = [
        for c in sort(data.ghost_repository_status_contexts.app.contexts) : { context = c }
        if startswith(c, "ci/")
      ]
    }
  }
}

output "status_contexts" {
  value = data.ghost_repository_status_contexts.app.contexts
}
