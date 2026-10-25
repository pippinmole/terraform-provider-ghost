resource "ghost_repository" "example" {
  name = "example"
}

# A single label. color accepts six hex digits, with or without a "#".
resource "ghost_issue_label" "bug" {
  repository  = ghost_repository.example.full_name
  name        = "bug"
  color       = "#D73A4A"
  description = "Something isn't working"
}

# A standard set of labels. Label names are case-sensitive.
locals {
  labels = {
    "enhancement" = { color = "a2eeef", description = "New feature or request" }
    "question"    = { color = "d876e3", description = "Further information is requested" }
    "wontfix"     = { color = "ffffff", description = "This will not be worked on" }
  }
}

resource "ghost_issue_label" "standard" {
  for_each = local.labels

  repository  = ghost_repository.example.full_name
  name        = each.key
  color       = each.value.color
  description = each.value.description
}
