# Every argument is optional: the ones you leave out stay as they are.
# Destroying the resource leaves the settings unchanged.
resource "ghost_organization_settings" "example" {
  organization = "my-org"

  # Members can read every repository; "none" gives them nothing beyond
  # their teams and collaborations.
  base_permission                         = "read"
  members_can_create_public_repositories  = false
  members_can_create_private_repositories = true
  allow_private_forks                     = false
  oauth_app_restrictions                  = true

  # New repositories start on this branch.
  default_branch = "main"

  # The public profile. "" clears a field.
  description = "Rockets and other things that go up"
  website     = "https://my-org.example"
  location    = "Earth"
  email       = ""
}
