data "ghost_organization_members" "all" {
  organization = "my-org"
}

# Admins can also list the people outside the organization who have, or are
# invited to, a role on its repositories.
data "ghost_organization_members" "with_outside_collaborators" {
  organization                  = "my-org"
  include_outside_collaborators = true
}

output "members" {
  value = data.ghost_organization_members.all.usernames
}

output "public_members" {
  value = [for m in data.ghost_organization_members.all.members : m.username if m.public]
}

output "outside_collaborators" {
  value = {
    for c in data.ghost_organization_members.with_outside_collaborators.outside_collaborators :
    c.username => { for r in c.repositories : r.full_name => r.pending ? "${r.role} (pending)" : r.role }
  }
}
