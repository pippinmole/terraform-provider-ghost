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
}

# A name containing "/" and ":", which are escaped in the API path. Import
# it with "<owner>/<slug>:area/api: v3".
resource "ghost_issue_label" "punctuated" {
  repository = ghost_repository.full.full_name
  name       = "area/api: v3"
  color      = "0e8a16"
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

output "data_sources_agree" {
  description = "True when each data source lookup matches the resource it reads."
  value = alltrue([
    data.ghost_repository.by_full_name.repo_id == ghost_repository.full.repo_id,
    data.ghost_repository.by_name.repo_id == ghost_repository.minimal.repo_id,
    data.ghost_repository.by_owner_and_name.repo_id == ghost_repository.full.repo_id,
    data.ghost_user.by_login.user_id == data.ghost_user.me.user_id,
  ])
}
