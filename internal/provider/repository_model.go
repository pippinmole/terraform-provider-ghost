package provider

import (
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

// repositoryRemoteModel holds the attributes that the ghost_repository
// resource and data source both copy straight from the API. It is embedded in
// both models.
type repositoryRemoteModel struct {
	ID            types.String `tfsdk:"id"`
	FullName      types.String `tfsdk:"full_name"`
	Slug          types.String `tfsdk:"slug"`
	RepoID        types.Int64  `tfsdk:"repo_id"`
	NodeID        types.String `tfsdk:"node_id"`
	Visibility    types.String `tfsdk:"visibility"`
	Private       types.Bool   `tfsdk:"private"`
	Fork          types.Bool   `tfsdk:"fork"`
	HTMLURL       types.String `tfsdk:"html_url"`
	CloneURL      types.String `tfsdk:"clone_url"`
	SSHURL        types.String `tfsdk:"ssh_url"`
	DefaultBranch types.String `tfsdk:"default_branch"`
}

// fromRemote sets m from repo and returns the owner. Owner and slug are
// derived from the response's full name, since a renamed repository's old
// name redirects to the new one.
func (m *repositoryRemoteModel) fromRemote(repo *client.Repository) (owner string, err error) {
	owner, slug, err := client.SplitFullName(repo.FullName)
	if err != nil {
		return "", err
	}
	m.set(repo, owner, slug)

	return owner, nil
}

// set copies repo into m, using owner and slug for the identity attributes.
func (m *repositoryRemoteModel) set(repo *client.Repository, owner, slug string) {
	m.ID = types.StringValue(owner + "/" + slug)
	m.FullName = m.ID
	m.Slug = types.StringValue(slug)
	m.RepoID = types.Int64Value(repo.ID)
	m.NodeID = types.StringValue(repo.NodeID)
	m.Visibility = types.StringValue(repo.Visibility)
	m.Private = types.BoolValue(repo.Private)
	m.Fork = types.BoolValue(repo.Fork)
	m.HTMLURL = types.StringValue(repo.HTMLURL)
	m.CloneURL = types.StringValue(repo.CloneURL)
	m.SSHURL = types.StringValue(repo.SSHURL)
	m.DefaultBranch = types.StringValue(repo.DefaultBranch)
}
