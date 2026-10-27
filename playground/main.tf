# Playground: one config that exercises every resource and data source the
# provider implements, for manual end-to-end testing against a real Ghost
# account. Add new resources and data sources here as they are implemented.
#
# Setup (from the repository root):
#   make install dev-tfrc
#   export TF_CLI_CONFIG_FILE="$PWD/dev.tfrc"
#   cp playground/terraform.tfvars.example playground/terraform.tfvars   # add your token
#   terraform -chdir=playground apply     # no `terraform init` needed with dev_overrides
#   terraform -chdir=playground destroy   # clean up when done
#
# Everything is created under your own account. The names start with
# var.name_prefix, so the repositories are easy to spot and delete by hand.

terraform {
  required_providers {
    ghost = {
      source = "pippinmole/ghost"
    }
  }
}

variable "ghost_token" {
  description = "Ghost personal access token."
  type        = string
  sensitive   = true
}

variable "ghost_base_url" {
  description = "Ghost API base URL. Leave null for the provider default."
  type        = string
  default     = null
}

variable "name_prefix" {
  description = "Prefix for every repository the playground creates."
  type        = string
  default     = "playground"
}

variable "organization" {
  description = "Organization to create a repository in. Leave null to skip the organization example."
  type        = string
  default     = null
}

variable "organization_team_id" {
  description = "ID of a team in var.organization, to add as a ruleset bypass actor. Leave null to skip it."
  type        = string
  default     = null
}

provider "ghost" {
  token    = var.ghost_token
  base_url = var.ghost_base_url
}

# ---------------------------------------------------------------------------
# ghost_repository (resource)
# ---------------------------------------------------------------------------

# The minimal form: a private repository owned by the authenticated user.
resource "ghost_repository" "minimal" {
  name = "${var.name_prefix}-minimal"
}

# Every argument. auto_init creates the initial commit, so default_branch can
# point at a branch that exists. auto_init, gitignore_template and
# license_template only apply at creation.
resource "ghost_repository" "full" {
  name               = "${var.name_prefix}-full"
  description        = "Created by the terraform-provider-ghost playground"
  visibility         = "public"
  auto_init          = true
  default_branch     = "main"
  gitignore_template = "Go"
  license_template   = "mit"
}

# A repository in an organization, created only when var.organization is set.
resource "ghost_repository" "organization" {
  count = var.organization == null ? 0 : 1

  name        = "${var.name_prefix}-org"
  owner       = var.organization
  description = "Organization-owned playground repository"
}

# ---------------------------------------------------------------------------
# ghost_repository (data source), in each lookup form
# ---------------------------------------------------------------------------

data "ghost_repository" "by_full_name" {
  full_name = ghost_repository.full.full_name
}

# The owner defaults to the authenticated user.
data "ghost_repository" "by_name" {
  name = ghost_repository.minimal.slug
}

data "ghost_repository" "by_owner_and_name" {
  owner = ghost_repository.full.owner
  name  = ghost_repository.full.slug
}

# ---------------------------------------------------------------------------
# ghost_user (data source), in each lookup form
# ---------------------------------------------------------------------------

# The user that owns the token.
data "ghost_user" "me" {}

data "ghost_user" "by_login" {
  login = data.ghost_user.me.login
}

# ---------------------------------------------------------------------------
# ghost_issue_label (resource)
# ---------------------------------------------------------------------------

# The minimal form: the color defaults to ededed.
resource "ghost_issue_label" "minimal" {
  repository = ghost_repository.full.full_name
  name       = "triage"
}

# Every argument. The "#" and upper case are kept in state as written.
resource "ghost_issue_label" "full" {
  repository  = ghost_repository.full.full_name
  name        = "bug"
  color       = "#D73A4A"
  description = "Something isn't working"

  # Recreate the label if the repository is replaced, not when it's renamed.
  lifecycle {
    replace_triggered_by = [ghost_repository.full.repo_id]
  }
}

# A name containing "/" and ":", which are escaped in the API path. Import
# it with "<owner>/<slug>:area/api: v3".
resource "ghost_issue_label" "punctuated" {
  repository = ghost_repository.full.full_name
  name       = "area/api: v3"
  color      = "0e8a16"
}

# ---------------------------------------------------------------------------
# ghost_repository_ruleset (resource)
# ---------------------------------------------------------------------------

# The minimal form: an active ruleset with no conditions, rules or bypass
# actors. target defaults to "branch".
resource "ghost_repository_ruleset" "minimal" {
  repository  = ghost_repository.full.full_name
  name        = "minimal"
  enforcement = "active"
}

# Every argument. Patterns are kept as written; Ghost stores "main" as
# "refs/heads/main".
resource "ghost_repository_ruleset" "full" {
  repository  = ghost_repository.full.full_name
  name        = "protect-main"
  target      = "branch"
  enforcement = "evaluate"

  conditions = {
    ref_name = {
      include = ["~DEFAULT_BRANCH", "release/*"]
      exclude = ["release/old"]
    }
  }

  bypass_actors = [
    { actor_type = "RepositoryRole", role = "maintain", bypass_mode = "pull_request" },
    { actor_type = "RepositoryRole", role = "admin" },
  ]

  rules = {
    creation                = true
    update                  = true
    deletion                = true
    non_fast_forward        = true
    required_linear_history = true
    required_signatures     = true

    pull_request = {
      required_approving_review_count   = 1
      dismiss_stale_reviews_on_push     = true
      require_code_owner_review         = true
      require_last_push_approval        = true
      required_review_thread_resolution = true
      allowed_merge_methods             = ["squash", "rebase"]
    }

    required_status_checks = {
      required_check                       = [{ context = "ci/build" }, { context = "ci/test" }]
      strict_required_status_checks_policy = true
      do_not_enforce_on_create             = true
    }
  }

  # Recreate the ruleset if the repository is replaced, not when it's renamed.
  lifecycle {
    replace_triggered_by = [ghost_repository.full.repo_id]
  }
}

# A tag ruleset in the organization repository, with every bypass actor type.
# The Team actor is added only when var.organization_team_id is set.
resource "ghost_repository_ruleset" "organization" {
  count = var.organization == null ? 0 : 1

  repository  = ghost_repository.organization[0].full_name
  name        = "protect-tags"
  target      = "tag"
  enforcement = "active"

  conditions = {
    ref_name = {
      include = ["v*"]
    }
  }

  bypass_actors = concat(
    [
      { actor_type = "OrganizationAdmin", role = null, team_id = null, bypass_mode = "always" },
      { actor_type = "RepositoryRole", role = "admin", team_id = null, bypass_mode = "always" },
    ],
    var.organization_team_id == null ? [] : [
      { actor_type = "Team", role = null, team_id = var.organization_team_id, bypass_mode = "pull_request" },
    ],
  )

  rules = {
    update   = true
    deletion = true
  }
}

# ---------------------------------------------------------------------------
# ghost_branch (resource)
# ---------------------------------------------------------------------------

# The minimal form: a branch started at the default branch. The repository
# needs a commit to branch from, which auto_init provides.
resource "ghost_branch" "minimal" {
  repository = ghost_repository.full.full_name
  branch     = "development"
}

# Every argument. source is a branch here, but a tag or commit SHA also
# works. The "/" is escaped in the API path; import it with
# "<owner>/<slug>:release/1.x".
resource "ghost_branch" "full" {
  repository = ghost_repository.full.full_name
  branch     = "release/1.x"
  source     = ghost_branch.minimal.branch

  # Recreate the branch if the repository is replaced, not when it's renamed.
  lifecycle {
    replace_triggered_by = [ghost_repository.full.repo_id]
  }
}

# ---------------------------------------------------------------------------
# Outputs
# ---------------------------------------------------------------------------

output "repositories" {
  description = "The repositories the playground manages."
  value = {
    for key, repo in merge(
      { minimal = ghost_repository.minimal, full = ghost_repository.full },
      { for r in ghost_repository.organization : "organization" => r },
    ) : key => { full_name = repo.full_name, visibility = repo.visibility, html_url = repo.html_url }
  }
}

output "issue_labels" {
  description = "The issue labels the playground manages."
  value = {
    for key, label in {
      minimal    = ghost_issue_label.minimal
      full       = ghost_issue_label.full
      punctuated = ghost_issue_label.punctuated
    } : key => { id = label.id, color = label.color, url = label.url }
  }
}

output "repository_rulesets" {
  description = "The repository rulesets the playground manages."
  value = {
    for key, rs in merge(
      { minimal = ghost_repository_ruleset.minimal, full = ghost_repository_ruleset.full },
      { for r in ghost_repository_ruleset.organization : "organization" => r },
    ) : key => { id = rs.id, ruleset_id = rs.ruleset_id, enforcement = rs.enforcement }
  }
}

output "branches" {
  description = "The branches the playground manages."
  value = {
    for key, b in {
      minimal = ghost_branch.minimal
      full    = ghost_branch.full
    } : key => { id = b.id, sha = b.sha }
  }
}

output "data_sources_agree" {
  description = "True when each data source lookup matches the resource it reads."
  value = alltrue([
    data.ghost_repository.by_full_name.repo_id == ghost_repository.full.repo_id,
    data.ghost_repository.by_name.repo_id == ghost_repository.minimal.repo_id,
    data.ghost_repository.by_owner_and_name.repo_id == ghost_repository.full.repo_id,
    data.ghost_user.by_login.user_id == data.ghost_user.me.user_id,
  ])
}
