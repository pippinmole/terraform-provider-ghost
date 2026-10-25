# A private repository owned by the authenticated user.
resource "ghost_repository" "example" {
  name        = "example"
  description = "Managed by Terraform"
}

# A public repository in an organization, initialised with a commit. A
# default_branch can only be set to a branch that exists, which on a new
# repository needs auto_init.
resource "ghost_repository" "org" {
  name               = "org-example"
  owner              = "my-org"
  visibility         = "public"
  auto_init          = true
  default_branch     = "main"
  gitignore_template = "Go"
  license_template   = "mit"
}
