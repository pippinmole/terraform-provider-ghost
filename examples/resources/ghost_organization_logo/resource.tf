# The logo from a local PNG or JPEG file (up to 2 MiB). The file is hashed at
# plan time, so editing it uploads the new image. Destroy removes the logo.
resource "ghost_organization_logo" "example" {
  organization = "my-org"
  file         = "${path.module}/logo.png"
}

# Or base64-encoded content:
#
#   content_base64 = filebase64("${path.module}/logo.jpg")
