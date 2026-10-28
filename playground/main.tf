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
  description = "ID of a team in var.organization, to add as a ruleset bypass actor, look up with ghost_team and give access with ghost_team_repository. Leave null to skip them."
  type        = string
  default     = null
}

variable "collaborator" {
  description = "Username of another Ghost user to invite to repositories with ghost_repository_collaborator. They get an email invitation, which they can ignore: it stays pending, and destroy withdraws it. Leave null to skip the collaborator examples."
  type        = string
  default     = null
}

variable "team_maintainer" {
  description = "Username of a member of var.organization to make a maintainer of the var.organization_team_id team with ghost_team_membership. Only organization members can join its teams, and they can't be invited with a token. Destroy removes them from the team. Leave null to skip it."
  type        = string
  default     = null
}

variable "team_member" {
  description = "Username of another member of var.organization to add to the same team as an ordinary member, named by its slug. Must differ from var.team_maintainer. Leave null to skip it."
  type        = string
  default     = null
}

variable "manage_organization_settings" {
  description = "Set to true to manage var.organization's settings with ghost_organization_settings. Applying overwrites its base permission, repository policies, default branch and profile, and destroy leaves them as they are: it doesn't restore the old values. You must be an admin of the organization."
  type        = bool
  default     = false
}

variable "organization_email" {
  description = "Public email address for var.organization's profile, used when var.manage_organization_settings is true. The default, \"\", clears it."
  type        = string
  default     = ""
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
# ghost_team (data source), in each lookup form. Read only when
# var.organization and var.organization_team_id are set: teams can't be
# created with a token.
# ---------------------------------------------------------------------------

# By ID, which survives a rename of the team.
data "ghost_team" "by_id" {
  count = var.organization == null || var.organization_team_id == null ? 0 : 1

  organization = var.organization
  id           = var.organization_team_id
}

# By slug, which Ghost matches case-insensitively and changes on rename.
data "ghost_team" "by_slug" {
  count = length(data.ghost_team.by_id)

  organization = var.organization
  slug         = data.ghost_team.by_id[0].slug
}

# ---------------------------------------------------------------------------
# ghost_organization_teams (data source). Read only when var.organization is
# set.
# ---------------------------------------------------------------------------

data "ghost_organization_teams" "all" {
  count = var.organization == null ? 0 : 1

  organization = var.organization
}

# ---------------------------------------------------------------------------
# ghost_organization (data source). Read only when var.organization is set.
# Any organization's profile can be read, but only members see private
# members and viewer_role.
# ---------------------------------------------------------------------------

data "ghost_organization" "this" {
  count = var.organization == null ? 0 : 1

  slug = var.organization
}

# ---------------------------------------------------------------------------
# ghost_organization_settings (resource). Created only when var.organization
# is set and var.manage_organization_settings is true. Every argument is
# optional and unset ones are left alone; destroy changes nothing.
# ---------------------------------------------------------------------------

resource "ghost_organization_settings" "this" {
  count = var.organization != null && var.manage_organization_settings ? 1 : 0

  organization                            = var.organization
  base_permission                         = "read" # or none, triage, write, maintain, admin
  members_can_create_public_repositories  = true
  members_can_create_private_repositories = true
  allow_private_forks                     = false
  oauth_app_restrictions                  = false
  default_branch                          = "main"
  description                             = "Managed by the Ghost Terraform playground"
  website                                 = "https://web.ghost.procrastinator.fyi"
  location                                = "" # "" clears a field
  email                                   = var.organization_email
}

# ---------------------------------------------------------------------------
# ghost_organization_webhook (resource). Created only when var.organization is
# set. An organization webhook receives the events of every repository in the
# organization. Import it with "<organization>:<webhook_id>"; the secret is
# null after import.
# ---------------------------------------------------------------------------

# The minimal form: active, with a secret Ghost generates. Ghost pings the URL
# as soon as the webhook is created; Ghost's own web host answers 200.
resource "ghost_organization_webhook" "minimal" {
  count = var.organization != null ? 1 : 0

  organization = var.organization
  url          = "https://web.ghost.procrastinator.fyi/?playground=org-minimal"
  events       = ["push"]
}

# Every argument. Change secret_rotation to replace the secret in place, e.g.
# with time_rotating.
resource "ghost_organization_webhook" "full" {
  count = var.organization != null ? 1 : 0

  organization    = var.organization
  url             = "https://example.com/ghost-org-webhook"
  events          = ["repository.edited", "label.created", "member.added", "member.removed"]
  active          = false
  secret_rotation = "2026-10"
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
# ghost_branch_default (resource)
# ---------------------------------------------------------------------------

# ghost_repository.full sets default_branch itself, and the two must not
# manage the same repository, so this uses a repository of its own that
# leaves default_branch unset.
resource "ghost_repository" "default_branch" {
  name      = "${var.name_prefix}-default-branch"
  auto_init = true
}

resource "ghost_branch" "trunk" {
  repository = ghost_repository.default_branch.full_name
  branch     = "trunk"
}

# Every argument. Referencing the ghost_branch creates it first; destroying
# this sets the default back to main (previous_branch) before the branch is
# deleted. Import it with "<owner>/<slug>".
resource "ghost_branch_default" "full" {
  repository = ghost_repository.default_branch.full_name
  branch     = ghost_branch.trunk.branch
}

# ---------------------------------------------------------------------------
# ghost_repository_webhook (resource)
# ---------------------------------------------------------------------------

# The minimal form: active, with a secret Ghost generates. Ghost pings the URL
# as soon as the webhook is created. Ghost's own web host answers 200, so the
# ping shows as delivered.
resource "ghost_repository_webhook" "minimal" {
  repository = ghost_repository.minimal.full_name
  url        = "https://web.ghost.procrastinator.fyi/"
  events     = ["push"]
}

# Every argument. Change secret_rotation to replace the secret in place, e.g.
# with time_rotating. replace_triggered_by recreates the webhook when the
# repository is replaced, which deletes its webhooks. Import it with
# "<owner>/<slug>:<webhook_id>"; the secret is null after import.
resource "ghost_repository_webhook" "full" {
  repository      = ghost_repository.full.full_name
  url             = "https://example.com/ghost-webhook"
  events          = ["issue.opened", "issue.closed", "label.created", "release.published"]
  active          = false
  secret_rotation = "2026-10"

  lifecycle {
    replace_triggered_by = [ghost_repository.full.repo_id]
  }
}

# ---------------------------------------------------------------------------
# ghost_team_repository, in each team form. Created only when var.organization
# and var.organization_team_id are set: only an organization's repositories
# can be shared with its teams, and teams can't be created with a token. A team
# has one role per repository, so the slug form uses a second repository.
# ---------------------------------------------------------------------------

resource "ghost_repository" "organization_team" {
  count = length(data.ghost_team.by_id)

  name  = "${var.name_prefix}-org-team"
  owner = var.organization
}

# By team ID, which survives a rename of the team.
resource "ghost_team_repository" "by_id" {
  count = length(ghost_repository.organization_team)

  repository = ghost_repository.organization[0].full_name
  team_id    = data.ghost_team.by_id[0].id
  role       = "write"

  # Replacing the repository drops its team access; give it back.
  lifecycle {
    replace_triggered_by = [ghost_repository.organization[0].repo_id]
  }
}

# By team slug, matched case-insensitively.
resource "ghost_team_repository" "by_slug" {
  count = length(ghost_repository.organization_team)

  repository = ghost_repository.organization_team[0].full_name
  team_slug  = data.ghost_team.by_id[0].slug
  role       = "maintain"

  lifecycle {
    replace_triggered_by = [ghost_repository.organization_team[0].repo_id]
  }
}

# ---------------------------------------------------------------------------
# ghost_repository_collaborator. Created only when var.collaborator is set.
# The invitation stays pending until that user accepts it; destroy withdraws
# it (or removes the collaborator after they accept). Import with
# "<owner>/<slug>:<username>".
# ---------------------------------------------------------------------------

resource "ghost_repository_collaborator" "full" {
  count = var.collaborator == null ? 0 : 1

  repository = ghost_repository.full.full_name
  username   = var.collaborator
  role       = "triage"

  # Replacing the repository drops its collaborators; invite them again.
  lifecycle {
    replace_triggered_by = [ghost_repository.full.repo_id]
  }
}

# On the organization repository too: a user outside the organization becomes
# an outside collaborator once they accept.
resource "ghost_repository_collaborator" "organization" {
  count = var.collaborator == null ? 0 : length(ghost_repository.organization)

  repository = ghost_repository.organization[0].full_name
  username   = var.collaborator
  role       = "read"

  lifecycle {
    replace_triggered_by = [ghost_repository.organization[0].repo_id]
  }
}

# ---------------------------------------------------------------------------
# ghost_team_membership, in each team form. Created only when
# var.organization_team_id and the usernames are set. The users must already
# be members of var.organization. Destroy takes them out of the team (not the
# organization). Import with "<organization>:<team id or slug>:<username>".
# ---------------------------------------------------------------------------

# By team ID, as a maintainer.
resource "ghost_team_membership" "maintainer" {
  count = var.team_maintainer == null ? 0 : length(data.ghost_team.by_id)

  organization = var.organization
  team_id      = data.ghost_team.by_id[0].id
  username     = var.team_maintainer
  role         = "maintainer"
}

# By team slug, matched case-insensitively; role defaults to "member".
resource "ghost_team_membership" "member" {
  count = var.team_member == null ? 0 : length(data.ghost_team.by_id)

  organization = var.organization
  team_slug    = data.ghost_team.by_id[0].slug
  username     = var.team_member
}

# ---------------------------------------------------------------------------
# Outputs
# ---------------------------------------------------------------------------

output "repositories" {
  description = "The repositories the playground manages."
  value = {
    for key, repo in merge(
      { minimal = ghost_repository.minimal, full = ghost_repository.full, default_branch = ghost_repository.default_branch },
      { for r in ghost_repository.organization : "organization" => r },
      { for r in ghost_repository.organization_team : "organization_team" => r },
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
      trunk   = ghost_branch.trunk
    } : key => { id = b.id, sha = b.sha }
  }
}

output "default_branch" {
  description = "The default branch ghost_branch_default set, and the one destroying it restores."
  value = {
    branch          = ghost_branch_default.full.branch
    previous_branch = ghost_branch_default.full.previous_branch
  }
}

output "repository_webhooks" {
  description = "The repository webhooks the playground manages. The secrets are sensitive: read them with terraform output -json."
  value = {
    for key, hook in {
      minimal = ghost_repository_webhook.minimal
      full    = ghost_repository_webhook.full
    } : key => { id = hook.id, active = hook.active, disabled_reason = hook.disabled_reason }
  }
}

output "repository_webhook_secrets" {
  description = "The secrets Ghost signs deliveries with (X-Hub-Signature-256)."
  sensitive   = true
  value       = { minimal = ghost_repository_webhook.minimal.secret, full = ghost_repository_webhook.full.secret }
}

output "organization_webhooks" {
  description = "The organization webhooks the playground manages, when var.organization is set. The secrets are sensitive: read them with terraform output -json."
  value = {
    for key, hook in merge(
      { for h in ghost_organization_webhook.minimal : "minimal" => h },
      { for h in ghost_organization_webhook.full : "full" => h },
    ) : key => { id = hook.id, active = hook.active, disabled_reason = hook.disabled_reason }
  }
}

output "organization_webhook_secrets" {
  description = "The secrets Ghost signs organization webhook deliveries with (X-Hub-Signature-256)."
  sensitive   = true
  value       = { for key, hooks in { minimal = ghost_organization_webhook.minimal, full = ghost_organization_webhook.full } : key => one(hooks[*].secret) }
}

output "team_repositories" {
  description = "The team access the playground manages, when var.organization_team_id is set."
  value = {
    for key, tr in merge(
      { for r in ghost_team_repository.by_id : "by_id" => r },
      { for r in ghost_team_repository.by_slug : "by_slug" => r },
    ) : key => { id = tr.id, team_slug = tr.team_slug, role = tr.role }
  }
}

output "repository_collaborators" {
  description = "The collaborators the playground invites, when var.collaborator is set."
  value = {
    for key, c in merge(
      { for r in ghost_repository_collaborator.full : "full" => r },
      { for r in ghost_repository_collaborator.organization : "organization" => r },
    ) : key => { id = c.id, role = c.role, status = c.status, expires_at = c.expires_at }
  }
}

output "team_memberships" {
  description = "The team memberships the playground manages, when var.team_maintainer or var.team_member is set."
  value = {
    for key, m in merge(
      { for r in ghost_team_membership.maintainer : "maintainer" => r },
      { for r in ghost_team_membership.member : "member" => r },
    ) : key => { id = m.id, team_slug = m.team_slug, role = m.role }
  }
}

output "data_sources_agree" {
  description = "True when each data source lookup matches the resource it reads."
  value = alltrue([
    data.ghost_repository.by_full_name.repo_id == ghost_repository.full.repo_id,
    data.ghost_repository.by_name.repo_id == ghost_repository.minimal.repo_id,
    data.ghost_repository.by_owner_and_name.repo_id == ghost_repository.full.repo_id,
    data.ghost_user.by_login.user_id == data.ghost_user.me.user_id,
    alltrue([for t in data.ghost_team.by_slug : t.id == data.ghost_team.by_id[0].id]),
    alltrue([
      for t in data.ghost_team.by_id : contains([for o in data.ghost_organization_teams.all[0].teams : o.id], t.id)
    ]),
    alltrue([for o in data.ghost_organization.this : o.id == lower(var.organization)]),
  ])
}

output "organization_teams" {
  description = "The teams in var.organization, keyed by slug, as listed by ghost_organization_teams."
  value = one([
    for o in data.ghost_organization_teams.all : {
      for t in o.teams : t.slug => {
        id               = t.id
        name             = t.name
        member_count     = t.member_count
        repository_count = t.repository_count
      }
    }
  ])
}

output "organization" {
  description = "The profile of var.organization, as read by ghost_organization."
  value = one([
    for o in data.ghost_organization.this : {
      id                      = o.id
      name                    = o.name
      logo                    = o.logo
      created_at              = o.created_at
      description             = o.description
      website                 = o.website
      location                = o.location
      email                   = o.email
      public_repository_count = o.public_repository_count
      members                 = { for m in o.members : m.username => { name = m.name, image = m.image, public = m.public } }
      pinned_repositories     = [for r in o.pinned_repositories : { full_name = r.full_name, name = r.name, description = r.description, visibility = r.visibility, slug = r.slug }]
      viewer_role             = o.viewer_role
    }
  ])
}

output "organization_settings" {
  description = "var.organization's settings, when var.manage_organization_settings is true."
  value = one([for s in ghost_organization_settings.this : {
    base_permission = s.base_permission
    default_branch  = s.default_branch
    description     = s.description
  }])
}

output "team" {
  description = "The team looked up by ghost_team, when var.organization_team_id is set."
  value = one([
    for t in data.ghost_team.by_slug : {
      id           = t.id
      slug         = t.slug
      name         = t.name
      members      = [for m in t.members : m.username]
      maintainers  = t.maintainers
      repositories = { for r in t.repositories : r.full_name => r.role }
    }
  ])
}
