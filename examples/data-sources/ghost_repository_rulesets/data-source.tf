# Every ruleset on a repository, whatever its enforcement.
data "ghost_repository_rulesets" "app" {
  repository = "my-org/my-repo"
}

locals {
  rulesets = data.ghost_repository_rulesets.app.rulesets
}

# Rulesets that are enforced, by name.
output "active_rulesets" {
  value = [for rs in local.rulesets : rs.name if rs.enforcement == "active"]
}

# Rulesets that require a pull request, and how many approvals they need.
output "required_approvals" {
  value = {
    for rs in local.rulesets : rs.name => rs.rules.pull_request.required_approving_review_count
    if rs.rules.pull_request != null
  }
}

# The ID to import a ruleset Terraform didn't create, e.g.
# terraform import ghost_repository_ruleset.main "my-org/my-repo:ruleset_…".
output "import_ids" {
  value = { for rs in local.rulesets : rs.name => "${data.ghost_repository_rulesets.app.id}:${rs.ruleset_id}" }
}
