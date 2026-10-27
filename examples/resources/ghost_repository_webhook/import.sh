# A webhook is imported by the repository's full name and the webhook ID,
# "owner/slug:webhook_id". Ghost never returns the secret again, so it is null
# after import until secret_rotation is set.
terraform import ghost_repository_webhook.example my-user/example:whk_abc123
