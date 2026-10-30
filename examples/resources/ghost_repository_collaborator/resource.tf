resource "ghost_repository" "example" {
  name = "example"
}

# Invite a user to the repository. The invitation stays pending until they
# accept it; destroying the resource withdraws it, or removes the
# collaborator once they have accepted.
resource "ghost_repository_collaborator" "octocat" {
  repository = ghost_repository.example.full_name
  username   = "octocat"
  role       = "write"

  # Invite them again if the repository is replaced, which drops its
  # collaborators.
  lifecycle {
    replace_triggered_by = [ghost_repository.example.repo_id]
  }
}

output "octocat_invitation" {
  value = ghost_repository_collaborator.octocat.status # "pending" or "accepted"
}
