resource "ghost_repository" "example" {
  name = "example"
}

resource "ghost_repository_webhook" "example" {
  repository = ghost_repository.example.full_name
  url        = "https://ci.example.com/hooks/ghost"
  events     = ["issue.opened", "issue.closed", "release.published"]

  # Recreate the webhook if the repository is replaced, which deletes its
  # webhooks.
  lifecycle {
    replace_triggered_by = [ghost_repository.example.repo_id]
  }
}

# Hand the signing secret to the receiver, e.g. as a secret in another
# provider. Verify deliveries by comparing the X-Hub-Signature-256 header with
# "sha256=" + hex(HMAC-SHA256(secret, body)).
output "webhook_secret" {
  value     = ghost_repository_webhook.example.secret
  sensitive = true
}
