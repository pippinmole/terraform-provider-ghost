resource "ghost_repository" "example" {
  name      = "example"
  auto_init = true
}

# A published release. Ghost creates the v1.0.0 tag at the default branch
# unless it already exists.
resource "ghost_release" "v1" {
  repository = ghost_repository.example.full_name
  tag_name   = "v1.0.0"
  name       = "v1.0.0"
  body       = <<-EOT
    ## Changes

    * First stable release.
  EOT

  # Replacing the repository deletes its releases; this recreates the release
  # in the same apply. repo_id doesn't change when the repository is renamed.
  lifecycle {
    replace_triggered_by = [ghost_repository.example.repo_id]
  }
}

# A draft prerelease, tagged at a branch. Ghost creates the tag straight away,
# even for a draft, so the tag is visible before the release is published.
resource "ghost_release" "next" {
  repository       = ghost_repository.example.full_name
  tag_name         = "v1.1.0-rc.1"
  target_commitish = "main"
  draft            = true
  prerelease       = true
}
