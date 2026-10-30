# Adds a key to the token owner's account for verifying SSH-signed commits.
# Sign with: git -c gpg.format=ssh -c user.signingkey=~/.ssh/id_ed25519 commit -S
resource "ghost_user_ssh_signing_key" "laptop" {
  title = "Laptop"
  key   = file("~/.ssh/id_ed25519.pub")
}
