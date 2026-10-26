package ghapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// MaxAssetSize is GitHub's limit for a single release asset (2 GiB).
const MaxAssetSize = 2 << 30

// Asset is a release asset.
type Asset struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	State       string `json:"state"`
	ContentType string `json:"content_type"`
	// URL is the API URL of the asset (not the browser download URL).
	URL string `json:"url"`
	// Digest is "sha256:..." when GitHub reports it.
	Digest string `json:"digest"`
}

// Release is a GitHub release.
type Release struct {
	ID         int64   `json:"id"`
	TagName    string  `json:"tag_name"`
	Name       string  `json:"name"`
	Body       string  `json:"body"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	HTMLURL    string  `json:"html_url"`
	Assets     []Asset `json:"assets"`
	UploadURL  string  `json:"upload_url"`
}

// GetReleaseByTag returns the release for tag, or ErrNotFound.
func (c *Client) GetReleaseByTag(ctx context.Context, tag string) (*Release, error) {
	var r Release
	u := c.api() + c.repoPath("releases", "tags", escapePath(tag))
	if err := c.doJSON(ctx, http.MethodGet, u, nil, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// CreateRelease creates a release for tag, targeting the default branch;
// GitHub creates the tag if it does not exist.
func (c *Client) CreateRelease(ctx context.Context, tag, name, body string, prerelease bool) (*Release, error) {
	in := map[string]any{"tag_name": tag, "name": name, "body": body, "prerelease": prerelease}
	var r Release
	if err := c.doJSON(ctx, http.MethodPost, c.api()+c.repoPath("releases"), in, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// UpdateReleaseBody replaces the description of release id.
func (c *Client) UpdateReleaseBody(ctx context.Context, id int64, body string) error {
	u := c.api() + c.repoPath("releases", strconv.FormatInt(id, 10))
	return c.doJSON(ctx, http.MethodPatch, u, map[string]any{"body": body}, nil)
}

// ListReleases returns every release, following Link-header pagination
// (100 per page). Next links pointing at another host are refused so the
// token cannot leak.
func (c *Client) ListReleases(ctx context.Context) ([]Release, error) {
	var all []Release
	next := c.api() + c.repoPath("releases") + "?per_page=100"
	for page := 0; next != ""; page++ {
		if page >= 1000 {
			return nil, errors.New("github: too many release pages")
		}
		if err := c.sameHost(next); err != nil {
			return nil, err
		}
		resp, err := c.send(ctx, reqSpec{method: http.MethodGet, url: next})
		if err != nil {
			return nil, err
		}
		var batch []Release
		err = decodeJSON(resp.Body, &batch)
		link := resp.Header.Get("Link")
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("github: decoding releases: %w", err)
		}
		all = append(all, batch...)
		next = nextLink(link)
	}
	return all, nil
}

func nextLink(h string) string {
	for _, part := range strings.Split(h, ",") {
		part = strings.TrimSpace(part)
		if !strings.Contains(part, `rel="next"`) {
			continue
		}
		if i, j := strings.Index(part, "<"), strings.Index(part, ">"); i == 0 && j > 0 {
			return part[1:j]
		}
	}
	return ""
}

// sameHost refuses URLs whose host differs from the API host.
func (c *Client) sameHost(u string) error {
	a, err1 := url.Parse(c.api())
	b, err2 := url.Parse(u)
	if err1 != nil || err2 != nil || a.Host != b.Host {
		return errors.New("github: refusing to send credentials to a different host")
	}
	return nil
}

// UploadAsset uploads size bytes from r as an asset of rel. Sizes above
// 2 GiB are rejected before anything is sent. The body is streamed and not
// retried.
func (c *Client) UploadAsset(ctx context.Context, rel *Release, name string, r io.Reader, size int64, contentType string) (*Asset, error) {
	if rel == nil {
		return nil, errors.New("github: nil release")
	}
	if name == "" {
		return nil, errors.New("github: asset name is required")
	}
	if size < 0 {
		return nil, errors.New("github: negative asset size")
	}
	if size > MaxAssetSize {
		return nil, fmt.Errorf("github: asset %s is %d bytes, over the 2 GiB release asset limit", name, size)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	u := c.uploadAPI() + c.repoPath("releases", strconv.FormatInt(rel.ID, 10), "assets") + "?name=" + url.QueryEscape(name)
	resp, err := c.send(ctx, reqSpec{method: http.MethodPost, url: u, upload: r, size: size, ctype: contentType, stream: true})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var a Asset
	if err := decodeJSON(resp.Body, &a); err != nil {
		return nil, fmt.Errorf("github: decoding asset: %w", err)
	}
	return &a, nil
}

// DownloadAsset streams an asset. The caller must close the result. GitHub
// redirects to object storage; the Authorization header is never forwarded to
// a host other than the API host.
func (c *Client) DownloadAsset(ctx context.Context, a Asset) (io.ReadCloser, error) {
	if err := c.sameHost(a.URL); err != nil {
		return nil, err
	}
	resp, err := c.send(ctx, reqSpec{method: http.MethodGet, url: a.URL, accept: "application/octet-stream", stream: true})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (c *Client) del(ctx context.Context, u string) error {
	resp, err := c.send(ctx, reqSpec{method: http.MethodDelete, url: u})
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// DeleteAsset deletes a release asset.
func (c *Client) DeleteAsset(ctx context.Context, id int64) error {
	return c.del(ctx, c.api()+c.repoPath("releases", "assets", strconv.FormatInt(id, 10)))
}

// DeleteRelease deletes a release (its tag is kept; see DeleteTag).
func (c *Client) DeleteRelease(ctx context.Context, id int64) error {
	return c.del(ctx, c.api()+c.repoPath("releases", strconv.FormatInt(id, 10)))
}

// DeleteTag deletes the git tag ref.
func (c *Client) DeleteTag(ctx context.Context, tag string) error {
	return c.del(ctx, c.api()+c.repoPath("git", "refs", "tags", escapePath(tag)))
}
