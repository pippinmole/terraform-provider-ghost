# Every license, the keys ghost_repository's license_template takes.
data "ghost_licenses" "all" {}

# One license and its text. Keys are matched case-insensitively, so an SPDX
# ID works too: license.key is Ghost's key, "apache-2.0".
data "ghost_licenses" "apache" {
  key = "Apache-2.0"
}

resource "ghost_repository" "example" {
  name             = "example"
  auto_init        = true
  license_template = data.ghost_licenses.apache.license.key
}

output "license_names" {
  value = { for l in data.ghost_licenses.all.licenses : l.key => l.name }
}
