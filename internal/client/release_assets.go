package client

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// UploadReleaseAssetRequest describes a file to attach to a release.
type UploadReleaseAssetRequest struct {
	// Name is the file name the asset is saved and downloaded under.
	Name string
	// Label is shown instead of the name in the web UI; "" sends none.
	Label string
	// ContentType is the media type the asset is served with;
	// application/octet-stream when empty.
	ContentType string
	// Content is read from the start on each attempt; Size is its length.
	Content io.ReaderAt
	Size    int64
}

func releaseAssetPath(owner, repo string, id int64) string {
	return releasePath(owner, repo, "assets", strconv.FormatInt(id, 10))
}

// UploadReleaseAsset attaches a file to the release releaseID, through the
// GitHub-compatible upload route (POST /api/uploads/repos/{o}/{r}/releases/
// {id}/assets), which takes the raw bytes and serves them afterwards with
// the request's Content-Type.
func (c *Client) UploadReleaseAsset(ctx context.Context, owner, repo string, releaseID int64, req UploadReleaseAssetRequest) (*ReleaseAsset, error) {
	params := url.Values{}
	params.Set("name", req.Name)
	if req.Label != "" {
		params.Set("label", req.Label)
	}
	path := NativePath("uploads", "repos", owner, repo, "releases", strconv.FormatInt(releaseID, 10), "assets") + "?" + params.Encode()

	content := req.Content
	if content == nil {
		content = bytes.NewReader(nil)
	}
	contentType := req.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	var asset ReleaseAsset
	if err := c.Upload(ctx, http.MethodPost, path, contentType, content, req.Size, &asset); err != nil {
		return nil, err
	}

	return &asset, nil
}

// GetReleaseAsset returns a release asset by its numeric ID.
func (c *Client) GetReleaseAsset(ctx context.Context, owner, repo string, id int64) (*ReleaseAsset, error) {
	var asset ReleaseAsset
	if err := c.Do(ctx, http.MethodGet, releaseAssetPath(owner, repo, id), nil, &asset); err != nil {
		return nil, err
	}

	return &asset, nil
}

// DownloadReleaseAsset copies a release asset's content to w.
func (c *Client) DownloadReleaseAsset(ctx context.Context, owner, repo string, id int64, w io.Writer) error {
	return c.Download(ctx, releaseAssetPath(owner, repo, id), w)
}

// UpdateReleaseAssetRequest renames an asset or sets its label. A nil field
// is left as it is; an empty Label clears it.
type UpdateReleaseAssetRequest struct {
	Name  *string `json:"name,omitempty"`
	Label *string `json:"label,omitempty"`
}

// UpdateReleaseAsset renames a release asset or sets its label.
func (c *Client) UpdateReleaseAsset(ctx context.Context, owner, repo string, id int64, req UpdateReleaseAssetRequest) (*ReleaseAsset, error) {
	var asset ReleaseAsset
	if err := c.Do(ctx, http.MethodPatch, releaseAssetPath(owner, repo, id), req, &asset); err != nil {
		return nil, err
	}

	return &asset, nil
}

// DeleteReleaseAsset deletes a release asset.
func (c *Client) DeleteReleaseAsset(ctx context.Context, owner, repo string, id int64) error {
	return c.Do(ctx, http.MethodDelete, releaseAssetPath(owner, repo, id), nil, nil)
}

// ListReleaseAssets returns every asset of the release releaseID, following
// the pages until one has nothing new.
func (c *Client) ListReleaseAssets(ctx context.Context, owner, repo string, releaseID int64) ([]ReleaseAsset, error) {
	var all []ReleaseAsset
	seen := map[int64]bool{}
	for page := 1; ; page++ {
		params := url.Values{}
		params.Set("per_page", "100")
		params.Set("page", strconv.Itoa(page))

		var assets []ReleaseAsset
		if err := c.Do(ctx, http.MethodGet, releasePath(owner, repo, strconv.FormatInt(releaseID, 10), "assets")+"?"+params.Encode(), nil, &assets); err != nil {
			return nil, err
		}
		added := false
		for _, a := range assets {
			if !seen[a.ID] {
				seen[a.ID] = true
				all = append(all, a)
				added = true
			}
		}
		if !added {
			return all, nil
		}
	}
}
