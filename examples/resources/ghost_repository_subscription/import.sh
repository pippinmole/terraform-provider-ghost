# A subscription is imported by the repository's full name, "owner/slug".
# Every repository has a level, so any repository the token's user can see
# can be imported; one they never chose a level for imports as
# "participating".
terraform import ghost_repository_subscription.example my-user/example
