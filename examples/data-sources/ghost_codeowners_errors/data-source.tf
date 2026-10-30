# The errors in the CODEOWNERS file on the default branch.
data "ghost_codeowners_errors" "app" {
  repository = "my-org/my-repo"
}

# A branch, tag or commit SHA instead.
data "ghost_codeowners_errors" "release" {
  repository = "my-org/my-repo"
  ref        = "release/1.x"
}

# Fail the plan when the CODEOWNERS file names an owner Ghost ignores, e.g. a
# team that hasn't been given write access yet.
check "codeowners" {
  assert {
    condition = length(data.ghost_codeowners_errors.app.errors) == 0
    error_message = join("\n", [
      for e in data.ghost_codeowners_errors.app.errors : "${e.path}:${e.line}:${e.column}: ${e.message}"
    ])
  }
}

output "codeowners_file" {
  value = data.ghost_codeowners_errors.app.path
}
