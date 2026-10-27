resource "ghost_repository" "example" {
  name      = "example"
  auto_init = true
}

# Protect the default branch: changes go through a reviewed pull request with
# passing checks, and the branch can't be deleted or force-pushed.
resource "ghost_repository_ruleset" "main" {
  repository  = ghost_repository.example.full_name
  name        = "protect-main"
  enforcement = "active"

  conditions = {
    ref_name = {
      include = ["~DEFAULT_BRANCH"]
    }
  }

  # Repository admins may push directly.
  bypass_actors = [
    { actor_type = "RepositoryRole", role = "admin" },
  ]

  rules = {
    deletion         = true
    non_fast_forward = true

    pull_request = {
      required_approving_review_count = 1
      dismiss_stale_reviews_on_push   = true
      allowed_merge_methods           = ["squash"]
    }

    required_status_checks = {
      required_check = [{ context = "ci/build" }]
    }
  }

  # Replacing the repository deletes its rulesets; this recreates the ruleset
  # in the same apply. repo_id doesn't change when the repository is renamed.
  lifecycle {
    replace_triggered_by = [ghost_repository.example.repo_id]
  }
}

# Only maintainers and admins may create or delete release tags.
resource "ghost_repository_ruleset" "tags" {
  repository  = ghost_repository.example.full_name
  name        = "release-tags"
  target      = "tag"
  enforcement = "active"

  conditions = {
    ref_name = {
      include = ["v*"]
    }
  }

  bypass_actors = [
    { actor_type = "RepositoryRole", role = "maintain" },
    { actor_type = "RepositoryRole", role = "admin" },
  ]

  rules = {
    creation = true
    deletion = true
  }
}
