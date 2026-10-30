resource "ghost_repository" "example" {
  name      = "example"
  auto_init = true
}

resource "ghost_release" "v1" {
  repository = ghost_repository.example.full_name
  tag_name   = "v1.0.0"
}

# A build output uploaded from a local file. The file is hashed at plan time,
# so a new build replaces the asset.
resource "ghost_release_asset" "linux" {
  repository   = ghost_repository.example.full_name
  release_id   = ghost_release.v1.release_id
  name         = "example-linux-amd64.tar.gz"
  label        = "Linux (x86_64)"
  content_type = "application/gzip"
  file         = "${path.module}/dist/example-linux-amd64.tar.gz"

  # Replacing the repository deletes its releases and their assets.
  lifecycle {
    replace_triggered_by = [ghost_repository.example.repo_id]
  }
}

# Small text content inline.
resource "ghost_release_asset" "checksums" {
  repository   = ghost_repository.example.full_name
  release_id   = ghost_release.v1.release_id
  name         = "SHA256SUMS"
  content_type = "text/plain"
  content      = "${ghost_release_asset.linux.content_sha256}  example-linux-amd64.tar.gz\n"
}

# Binary content, base64-encoded.
resource "ghost_release_asset" "icon" {
  repository     = ghost_repository.example.full_name
  release_id     = ghost_release.v1.release_id
  name           = "icon.png"
  content_type   = "image/png"
  content_base64 = filebase64("${path.module}/icon.png")
}
