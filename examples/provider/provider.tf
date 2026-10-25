provider "ghost" {
  # Or set the GHOST_TOKEN environment variable.
  token = var.ghost_token

  # Optional. Defaults to https://api.ghost.procrastinator.fyi
  # base_url = "https://api.ghost.procrastinator.fyi"
}

variable "ghost_token" {
  type      = string
  sensitive = true
}
