package client

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
)

// Release is a release as returned by the v3 API.
type Release struct {
	ID         int64  `json:"id"`
	NodeID     string `json:"node_id"`
	URL        string `json:"url"`
	HTMLURL    string `json:"html_url"`
	AssetsURL  string `json:"assets_url"`
	UploadURL  string `json:"upload_url"`
	TarballURL string `json:"tarball_url"`
	ZipballURL string `json:"zipball_url"`
	TagName    string `json:"tag_name"`
	// TargetCommitish is the commit the tag points at, or the default
	// branch's name once the tag has been deleted. Ghost doesn't keep the
	// branch a release was created from. Use CommitSHA.
	TargetCommitish string `json:"target_commitish"`
	// Name and Body are nil when unset: Ghost stores "" as null, and the web
	// UI shows the tag name for a release without a name.
	Name       *string `json:"name"`
	Body       *string `json:"body"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	CreatedAt  string  `json:"created_at"`
	// PublishedAt is nil for a draft that has never been published. It is
	// stamped the first time a release is published and kept afterwards.
	PublishedAt *string        `json:"published_at"`
	UpdatedAt   string         `json:"updated_at"`
	Author      *ReleaseAuthor `json:"author"`
	Assets      []ReleaseAsset `json:"assets"`
}

// ReleaseAuthor is the user who created a release. It is nil once their
// account has been deleted.
type ReleaseAuthor struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
}

// ReleaseAsset is a file attached to a release.
type ReleaseAsset struct {
	ID                 int64   `json:"id"`
	NodeID             string  `json:"node_id"`
	URL                string  `json:"url"`
	BrowserDownloadURL string  `json:"browser_download_url"`
	Name               string  `json:"name"`
	Label              *string `json:"label"`
	ContentType        string  `json:"content_type"`
	Size               int64   `json:"size"`
	DownloadCount      int64   `json:"download_count"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`
}

var commitSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// CommitSHA returns the commit the release's tag points at, or "" when the
// tag has been deleted (Ghost then reports the default branch's name in
// target_commitish instead of a SHA).
func (r *Release) CommitSHA() string {
	if commitSHAPattern.MatchString(r.TargetCommitish) {
		return r.TargetCommitish
	}

	return ""
}

// CreateReleaseRequest is the body for creating a release. Ghost creates the
// tag straight away, even for a draft, at TargetCommitish (a branch, tag or
// commit SHA; the default branch when empty). TargetCommitish is ignored
// when the tag already exists.
type CreateReleaseRequest struct {
	TagName         string `json:"tag_name"`
	TargetCommitish string `json:"target_commitish,omitempty"`
	Name            string `json:"name,omitempty"`
	Body            string `json:"body,omitempty"`
	Draft           bool   `json:"draft"`
	Prerelease      bool   `json:"prerelease"`
}

// UpdateReleaseRequest is the body for editing a release. It replaces every
// field: a nil Name or Body is sent as null and clears it. Ghost can't move
// a release to another tag, so the tag isn't part of it.
type UpdateReleaseRequest struct {
	Name       *string `json:"name"`
	Body       *string `json:"body"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
}

// ReleasePageSize is the page size ListReleases asks for. Tests lower it to
// exercise paging.
var ReleasePageSize = 100

func releasePath(owner, repo string, segments ...string) string {
	return V3Path(append([]string{"repos", owner, repo, "releases"}, segments...)...)
}

// CreateRelease creates a release in the repository owner/repo. Ghost
// answers 422 when the tag already has a release (tag_name already_exists),
// the tag name isn't valid (tag_name invalid), or the target doesn't exist or
// the repository is empty (target_commitish invalid).
func (c *Client) CreateRelease(ctx context.Context, owner, repo string, req CreateReleaseRequest) (*Release, error) {
	return c.doRelease(ctx, http.MethodPost, releasePath(owner, repo), req)
}

// GetRelease returns a release by its numeric ID. Drafts are returned only to
// people who can write to the repository.
func (c *Client) GetRelease(ctx context.Context, owner, repo string, id int64) (*Release, error) {
	return c.doRelease(ctx, http.MethodGet, releasePath(owner, repo, strconv.FormatInt(id, 10)), nil)
}

// GetReleaseByTag returns the published release for a tag. Drafts are never
// returned (404); use FindReleaseByTag for those.
func (c *Client) GetReleaseByTag(ctx context.Context, owner, repo, tag string) (*Release, error) {
	return c.doRelease(ctx, http.MethodGet, releasePath(owner, repo, "tags", tag), nil)
}

// GetLatestRelease returns the most recently published release that is
// neither a draft nor a prerelease.
func (c *Client) GetLatestRelease(ctx context.Context, owner, repo string) (*Release, error) {
	return c.doRelease(ctx, http.MethodGet, releasePath(owner, repo, "latest"), nil)
}

// UpdateRelease edits a release.
func (c *Client) UpdateRelease(ctx context.Context, owner, repo string, id int64, req UpdateReleaseRequest) (*Release, error) {
	return c.doRelease(ctx, http.MethodPatch, releasePath(owner, repo, strconv.FormatInt(id, 10)), req)
}

// DeleteRelease deletes a release and its assets. Ghost keeps the tag.
func (c *Client) DeleteRelease(ctx context.Context, owner, repo string, id int64) error {
	return c.Do(ctx, http.MethodDelete, releasePath(owner, repo, strconv.FormatInt(id, 10)), nil, nil)
}

// ListReleases returns every release of the repository owner/repo, newest
// first, following the pages until an empty one. Drafts are included for
// people who can write to the repository.
func (c *Client) ListReleases(ctx context.Context, owner, repo string) ([]Release, error) {
	var all []Release
	seen := map[int64]bool{}
	for page := 1; ; page++ {
		params := url.Values{}
		params.Set("per_page", strconv.Itoa(ReleasePageSize))
		params.Set("page", strconv.Itoa(page))

		var releases []Release
		if err := c.Do(ctx, http.MethodGet, releasePath(owner, repo)+"?"+params.Encode(), nil, &releases); err != nil {
			return nil, err
		}
		// Stop at an empty page, or one with nothing new: a release
		// created mid-listing shifts later pages, and a server that
		// ignored page would otherwise loop forever.
		added := false
		for _, r := range releases {
			if !seen[r.ID] {
				seen[r.ID] = true
				all = append(all, r)
				added = true
			}
		}
		if !added {
			return all, nil
		}
	}
}

// FindReleaseByTag returns the release for a tag, drafts included, by
// listing the repository's releases. It returns nil when no release has the
// tag. Tag names are case-sensitive.
func (c *Client) FindReleaseByTag(ctx context.Context, owner, repo, tag string) (*Release, error) {
	releases, err := c.ListReleases(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	for i := range releases {
		if releases[i].TagName == tag {
			return &releases[i], nil
		}
	}

	return nil, nil
}

func (c *Client) doRelease(ctx context.Context, method, path string, body any) (*Release, error) {
	var r Release
	if err := c.Do(ctx, method, path, body, &r); err != nil {
		return nil, err
	}

	return &r, nil
}
