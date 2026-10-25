# Look up a repository by its full name.
data "ghost_repository" "by_full_name" {
  full_name = "my-org/example"
}

# Look up a repository by its slug. The owner defaults to the authenticated user.
data "ghost_repository" "by_name" {
  name = "example"
}

# Look up a repository by owner and slug.
data "ghost_repository" "by_owner" {
  owner = "my-org"
  name  = "example"
}
