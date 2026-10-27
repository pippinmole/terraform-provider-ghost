# Rulesets are imported by the repository's full name and the ruleset ID,
# "owner/slug:ruleset_id". Ghost stores short patterns such as "main" as
# "refs/heads/main", so the first apply after an import updates the ruleset
# in place to the configured spelling; nothing changes in Ghost.
terraform import ghost_repository_ruleset.main my-user/example:ruleset_abc123
