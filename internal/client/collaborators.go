package client

import (
	"context"
	"net/http"
)

// Collaborator statuses. Only an accepted collaborator has access; an expired
// invitation is sent again by inviting the user again.
const (
	CollaboratorAccepted = "accepted"
	CollaboratorPending  = "pending"
	CollaboratorExpired  = "expired"
)

// Collaborator is a user invited to a repository, or collaborating on it.
type Collaborator struct {
	Username string  `json:"username"`
	Name     string  `json:"name"`
	Image    *string `json:"image"`
	Role     string  `json:"role"`
	// Status is CollaboratorAccepted, CollaboratorPending or
	// CollaboratorExpired.
	Status    string `json:"status"`
	InvitedAt string `json:"invitedAt"`
	// ExpiresAt is when the invitation lapses, or lapsed. It is nil once the
	// invitation is accepted.
	ExpiresAt *string `json:"expiresAt"`
}

// ListRepositoryCollaborators returns the collaborators of owner/repo,
// including pending and expired invitations. Only repository admins may list
// them.
func (c *Client) ListRepositoryCollaborators(ctx context.Context, owner, repo string) ([]Collaborator, error) {
	var out struct {
		Collaborators []Collaborator `json:"collaborators"`
	}
	if err := c.Do(ctx, http.MethodGet, NativePath("repositories", owner, repo, "collaborators"), nil, &out); err != nil {
		return nil, err
	}

	return out.Collaborators, nil
}

// SetRepositoryCollaborator invites the user to owner/repo with role, or
// changes the role of someone already invited or collaborating. Ghost matches
// the username case-insensitively, keeps the invitation's dates when it only
// changes the role, and sends an expired invitation again.
func (c *Client) SetRepositoryCollaborator(ctx context.Context, owner, repo, username, role string) (*Collaborator, error) {
	body := struct {
		Role string `json:"role"`
	}{Role: role}

	var out Collaborator
	if err := c.Do(ctx, http.MethodPut, NativePath("repositories", owner, repo, "collaborators", username), body, &out); err != nil {
		return nil, err
	}

	return &out, nil
}

// RemoveRepositoryCollaborator removes the user from owner/repo, or withdraws
// their invitation. Ghost returns 404 when the user isn't a collaborator or
// doesn't exist.
func (c *Client) RemoveRepositoryCollaborator(ctx context.Context, owner, repo, username string) error {
	return c.Do(ctx, http.MethodDelete, NativePath("repositories", owner, repo, "collaborators", username), nil, nil)
}
