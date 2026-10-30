resource "ghost_repository" "api" {
  name  = "api"
  owner = "my-org"
}

resource "ghost_repository" "web" {
  name       = "Web App"
  owner      = "my-org"
  visibility = "public"
}

# The organization's whole list of pins, in display order (at most 6).
# Refer to slug rather than name: Ghost matches slugs exactly, and the slug of
# "Web App" is "web-app". Destroying the resource unpins every repository.
resource "ghost_organization_pinned_repositories" "example" {
  organization = "my-org"
  repositories = [
    ghost_repository.web.slug,
    ghost_repository.api.slug,
  ]
}
