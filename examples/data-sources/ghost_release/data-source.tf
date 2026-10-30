# The latest release: the most recently published release that is neither a
# draft nor a prerelease.
data "ghost_release" "latest" {
  repository  = "my-org/my-repo"
  retrieve_by = "latest"
}

# A release by its numeric ID.
data "ghost_release" "by_id" {
  repository  = "my-org/my-repo"
  retrieve_by = "id"
  release_id  = 42
}

# A release by its tag, drafts included.
data "ghost_release" "by_tag" {
  repository  = "my-org/my-repo"
  retrieve_by = "tag"
  release_tag = "v1.0.0"
}

output "latest_version" {
  value = data.ghost_release.latest.release_tag
}

output "download_urls" {
  value = [for a in data.ghost_release.by_tag.assets : a.browser_download_url]
}
