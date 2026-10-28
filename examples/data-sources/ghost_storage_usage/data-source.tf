# The storage the token's own user uses against its quotas.
data "ghost_storage_usage" "me" {}

# An organization the token's user belongs to.
data "ghost_storage_usage" "org" {
  owner = "my-org"
}

# Warn when the organization's repositories are close to their quota.
check "storage" {
  assert {
    condition = (
      data.ghost_storage_usage.org.quota_bytes == null ||
      data.ghost_storage_usage.org.used_bytes < 0.9 * data.ghost_storage_usage.org.quota_bytes
    )
    error_message = "my-org uses ${data.ghost_storage_usage.org.used_bytes} of its ${data.ghost_storage_usage.org.quota_bytes} bytes of repository storage."
  }
}

output "lfs_headroom_bytes" {
  value = data.ghost_storage_usage.me.lfs.quota_bytes - data.ghost_storage_usage.me.lfs.used_bytes
}
