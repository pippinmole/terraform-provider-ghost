# A webhook is imported by the organization's slug and the webhook ID,
# "organization:webhook_id". Ghost never returns the secret again, so it is
# null after import until secret_rotation is set.
terraform import ghost_organization_webhook.example my-org:whk_abc123
