# The user that owns the provider's token.
data "ghost_user" "me" {}

# Any user or organization, by login. Logins are case-sensitive.
data "ghost_user" "octocat" {
  login = "octocat"
}
