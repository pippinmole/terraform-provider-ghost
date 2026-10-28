package client

import (
	"context"
	"net/http"
)

// Repository watch levels, from least to most notifications but for ignore.
const (
	// WatchParticipating notifies about threads the viewer takes part in or
	// is mentioned in. It is the level of every repository nobody chose a
	// level for.
	WatchParticipating = "participating"
	// WatchAll notifies about every thread.
	WatchAll = "all"
	// WatchIgnore notifies about nothing, mentions included.
	WatchIgnore = "ignore"
)

// RepositorySubscription is how the token's user watches a repository.
type RepositorySubscription struct {
	// Level is WatchParticipating, WatchAll or WatchIgnore.
	Level string `json:"level"`
}

// GetRepositorySubscription returns how the token's user watches owner/repo.
// A repository they never chose a level for is WatchParticipating; Ghost
// returns 404 only when the repository doesn't exist or they can't see it.
// Only the native API has this.
func (c *Client) GetRepositorySubscription(ctx context.Context, owner, repo string) (*RepositorySubscription, error) {
	var out RepositorySubscription
	if err := c.Do(ctx, http.MethodGet, NativePath("repositories", owner, repo, "subscription"), nil, &out); err != nil {
		return nil, err
	}

	return &out, nil
}

// SetRepositorySubscription sets how the token's user watches owner/repo.
// Setting WatchParticipating stops watching the repository.
func (c *Client) SetRepositorySubscription(ctx context.Context, owner, repo, level string) (*RepositorySubscription, error) {
	var out RepositorySubscription
	if err := c.Do(ctx, http.MethodPut, NativePath("repositories", owner, repo, "subscription"), RepositorySubscription{Level: level}, &out); err != nil {
		return nil, err
	}

	return &out, nil
}
