# Local development workspace. Run `make dev-tfrc` once, then:
#   export TF_CLI_CONFIG_FILE=$PWD/../dev.tfrc   (from this directory)
#   make install && terraform plan
# The token lives in terraform.tfvars, which is gitignored.
terraform {
  required_providers {
    ghost = {
      source = "pippinmole/ghost"
    }
  }
}

variable "ghost_token" {
  type      = string
  sensitive = true
}

provider "ghost" {
  token = var.ghost_token
}
