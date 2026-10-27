# The default branch is imported by the repository's full name, "owner/slug".
# An imported resource has no previous_branch, so destroying it only removes it
# from state.
terraform import ghost_branch_default.example my-user/example
