# Every .gitignore template name, the values ghost_repository's
# gitignore_template takes.
data "ghost_gitignore_templates" "all" {}

# One template and its content. Names are matched case-insensitively:
# template.name is Ghost's spelling, "Go".
data "ghost_gitignore_templates" "go" {
  name = "go"
}

resource "ghost_repository" "example" {
  name               = "example"
  auto_init          = true
  gitignore_template = data.ghost_gitignore_templates.go.template.name
}

output "go_gitignore" {
  value = data.ghost_gitignore_templates.go.template.source
}
