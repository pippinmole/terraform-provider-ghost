data "ghost_organization" "example" {
  slug = "my-org"
}

output "organization" {
  value = {
    name        = data.ghost_organization.example.name
    description = data.ghost_organization.example.description
    members     = [for m in data.ghost_organization.example.members : m.username]
    pinned      = [for r in data.ghost_organization.example.pinned_repositories : r.full_name]
    my_role     = data.ghost_organization.example.viewer_role
  }
}
