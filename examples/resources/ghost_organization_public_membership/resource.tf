# Make the token user's membership of my-org public. Ghost only lets a user
# change their own membership, so there's no username. Destroy makes it
# private again.
resource "ghost_organization_public_membership" "example" {
  organization = "my-org"
}

# Or keep it private, reverting it if someone makes it public:
#
#   public = false
