# A private repository owned by the authenticated user.
resource "ghost_repository" "example" {
  name        = "example"
  description = "Managed by Terraform"
}

# A public repository in an organization, initialised with a commit so the
# default branch exists.
resource "ghost_repository" "org" {
  name               = "org-example"
  owner              = "my-org"
  visibility         = "public"
  auto_init          = true
  gitignore_template = "Go"
  license_template   = "mit"
}
