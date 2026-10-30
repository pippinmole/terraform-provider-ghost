# Receives the events of every repository in the organization, including
# repositories created later.
resource "ghost_organization_webhook" "example" {
  organization = "my-org"
  url          = "https://ci.example.com/hooks/ghost"
  events       = ["push", "issue.opened", "release.published"]
}

# Hand the signing secret to the receiver, e.g. as a secret in another
# provider. Verify deliveries by comparing the X-Hub-Signature-256 header with
# "sha256=" + hex(HMAC-SHA256(secret, body)).
output "webhook_secret" {
  value     = ghost_organization_webhook.example.secret
  sensitive = true
}
