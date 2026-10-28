package client

import (
	"context"
	"net/http"
)

// StorageLimitUsage is the bytes an account keeps against one quota.
type StorageLimitUsage struct {
	UsedBytes int64 `json:"usedBytes"`
	// QuotaBytes is nil when no quota applies.
	QuotaBytes *int64 `json:"quotaBytes"`
}

// StorageUsage is the bytes an account keeps against each of its quotas.
type StorageUsage struct {
	// UsedBytes is pushed git data and merged pull requests' heads in
	// repositories that are not forks.
	UsedBytes int64 `json:"usedBytes"`
	// QuotaBytes is nil when no quota applies.
	QuotaBytes *int64 `json:"quotaBytes"`
	// MaxAssetBytes is the largest single release asset.
	MaxAssetBytes int64 `json:"maxAssetBytes"`
	// Fork is everything forks hold, LFS objects and release assets included.
	Fork StorageLimitUsage `json:"fork"`
	// LFS is Git LFS objects in repositories that are not forks.
	LFS StorageLimitUsage `json:"lfs"`
	// Asset is release assets in repositories that are not forks and, for a
	// user, files they attached anywhere, uploads in progress included.
	Asset StorageLimitUsage `json:"asset"`
}

// GetStorageUsage returns the storage usage of owner, which must be the
// token's own user or an organization it belongs to, through the native API
// (v3 has no equivalent). Anyone else, like an unknown name, is a 404
// "User <owner> not found". Ghost matches owner case-insensitively.
func (c *Client) GetStorageUsage(ctx context.Context, owner string) (*StorageUsage, error) {
	var u StorageUsage
	if err := c.Do(ctx, http.MethodGet, NativePath("storage", owner), nil, &u); err != nil {
		return nil, err
	}

	return &u, nil
}
