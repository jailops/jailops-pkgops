package ghapp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAPI       = "https://api.github.com"
	defaultUploadAPI = "https://uploads.github.com"
	apiVersion       = "2022-11-28"
	maxJSON          = 16 << 20
	maxErrBody       = 64 << 10
)

// retryDelay is the backoff before the single retry. Variable for tests.
var retryDelay = 300 * time.Millisecond

var (
	// ErrNotFound is matched (errors.Is) by any 404 response.
	ErrNotFound = errors.New("github: not found")
	// ErrConflict is matched by PutFile when the supplied blob sha is stale
	// or missing (HTTP 409 or 422).
	ErrConflict = errors.New("github: conflict")
)

// APIError is returned for non-2xx responses. It never contains credentials.
type APIError struct {
	Status  int
	Message string
}

// Error implements error.
func (e *APIError) Error() string {
	return fmt.Sprintf("github: HTTP %d: %s", e.Status, e.Message)
}

// Is makes 404 responses match ErrNotFound.
func (e *APIError) Is(target error) bool {
	return target == ErrNotFound && e.Status == http.StatusNotFound
}

// Client talks to the REST API of one repository. Zero optional fields take
// the GitHub.com defaults. A Client is safe for concurrent use.
type Client struct {
	Owner, Repo string
	Auth        Auth
	// API is the API base URL; default https://api.github.com.
	API string
	// UploadAPI is the asset upload base URL; default https://uploads.github.com.
	UploadAPI string
	// HTTP is the HTTP client; default has a 60s timeout. Uploads and
	// downloads ignore any client-wide timeout and rely on the context.
	HTTP *http.Client
}

func setCommonHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", "pkgops")
}

func (c *Client) api() string {
	if c.API == "" {
		return defaultAPI
	}
	return strings.TrimRight(c.API, "/")
}

func (c *Client) uploadAPI() string {
	if c.UploadAPI == "" {
		return defaultUploadAPI
	}
	return strings.TrimRight(c.UploadAPI, "/")
}

func (c *Client) repoPath(parts ...string) string {
	return "/repos/" + url.PathEscape(c.Owner) + "/" + url.PathEscape(c.Repo) + "/" + strings.Join(parts, "/")
}

// escapePath escapes each segment of a slash-separated path.
func escapePath(p string) string {
	segs := strings.Split(strings.Trim(p, "/"), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// httpClient returns a client that never forwards Authorization to another
// host on redirects (Go alone ignores the port, which is not enough).
func (c *Client) httpClient(stream bool) *http.Client {
	var cp http.Client
	if c.HTTP != nil {
		cp = *c.HTTP
	} else {
		cp.Timeout = 60 * time.Second
	}
	if stream {
		cp.Timeout = 0
	}
	orig := cp.CheckRedirect
	cp.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Host != via[0].URL.Host {
			req.Header.Del("Authorization")
		}
		if orig != nil {
			return orig(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &cp
}

type reqSpec struct {
	method, url string
	body        []byte    // replayable body
	upload      io.Reader // streamed body, never retried
	size        int64
	ctype       string
	accept      string
	stream      bool
}

// send performs the request with authentication, returning the response only
// for 2xx statuses. It retries once on 502/503/504 for idempotent methods
// and on 403/429 with a short Retry-After for GET.
func (c *Client) send(ctx context.Context, s reqSpec) (*http.Response, error) {
	hc := c.httpClient(s.stream)
	for attempt := 0; ; attempt++ {
		if c.Auth == nil {
			return nil, errors.New("github: no Auth configured")
		}
		tok, err := c.Auth.Token(ctx)
		if err != nil {
			return nil, fmt.Errorf("github: auth: %w", err)
		}
		var body io.Reader
		switch {
		case s.upload != nil && s.size > 0:
			body = s.upload
		case s.upload != nil:
			body = http.NoBody
		case s.body != nil:
			body = bytes.NewReader(s.body)
		}
		req, err := http.NewRequestWithContext(ctx, s.method, s.url, body)
		if err != nil {
			return nil, fmt.Errorf("github: building request: %w", err)
		}
		if s.upload != nil {
			req.ContentLength = s.size
		}
		setCommonHeaders(req)
		if s.accept != "" {
			req.Header.Set("Accept", s.accept)
		}
		if s.ctype != "" {
			req.Header.Set("Content-Type", s.ctype)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := hc.Do(req)
		if err != nil {
			return nil, fmt.Errorf("github: %s: %w", s.method, err)
		}
		if resp.StatusCode/100 == 2 {
			return resp, nil
		}
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
		aerr := errorFromResponse(resp)
		idem := s.upload == nil && (s.method == http.MethodGet || s.method == http.MethodDelete || s.method == http.MethodHead)
		retry := false
		switch resp.StatusCode {
		case 502, 503, 504:
			retry = idem
		case 403, 429:
			retry = s.method == http.MethodGet && retryAfter > 0 && retryAfter <= 5*time.Second
		}
		if !retry || attempt > 0 {
			return nil, aerr
		}
		d := retryDelay
		if retryAfter > 0 {
			d = retryAfter
		}
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

func parseRetryAfter(v string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}

// errorFromResponse consumes and closes resp and builds an *APIError.
// Exhausted rate limits (403/429 with X-RateLimit-Remaining: 0) say when the
// limit resets.
func errorFromResponse(resp *http.Response) error {
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
	var m struct {
		Message string `json:"message"`
	}
	msg := ""
	if json.Unmarshal(raw, &m) == nil {
		msg = m.Message
	}
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	if (resp.StatusCode == 403 || resp.StatusCode == 429) && resp.Header.Get("X-RateLimit-Remaining") == "0" {
		if reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			msg = fmt.Sprintf("rate limit exceeded; resets at %s", time.Unix(reset, 0).UTC().Format(time.RFC3339))
		} else {
			msg = "rate limit exceeded"
		}
	}
	return &APIError{Status: resp.StatusCode, Message: msg}
}

func decodeJSON(r io.Reader, out any) error {
	lr := &io.LimitedReader{R: r, N: maxJSON + 1}
	data, err := io.ReadAll(lr)
	if err != nil {
		return err
	}
	if len(data) > maxJSON {
		return errors.New("response exceeds 16 MiB limit")
	}
	return json.Unmarshal(data, out)
}

// doJSON sends in (if non-nil) as JSON and decodes the reply into out (if
// non-nil).
func (c *Client) doJSON(ctx context.Context, method, u string, in, out any) error {
	s := reqSpec{method: method, url: u}
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		s.body, s.ctype = b, "application/json"
	}
	resp, err := c.send(ctx, s)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxJSON))
		return nil
	}
	if err := decodeJSON(resp.Body, out); err != nil {
		return fmt.Errorf("github: decoding response: %w", err)
	}
	return nil
}

// GetFile returns the content and blob sha of path at ref (a branch, tag or
// commit; empty means the default branch). It returns ErrNotFound if the file
// does not exist.
func (c *Client) GetFile(ctx context.Context, path, ref string) (content []byte, sha string, err error) {
	u := c.api() + c.repoPath("contents", escapePath(path))
	if ref != "" {
		u += "?ref=" + url.QueryEscape(ref)
	}
	var out struct {
		Type     string `json:"type"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
		SHA      string `json:"sha"`
	}
	if err := c.doJSON(ctx, http.MethodGet, u, nil, &out); err != nil {
		return nil, "", err
	}
	if out.Type != "file" || out.Encoding != "base64" {
		return nil, "", fmt.Errorf("github: %s is not a regular file", path)
	}
	b, err := base64.StdEncoding.DecodeString(strings.NewReplacer("\n", "", "\r", "").Replace(out.Content))
	if err != nil {
		return nil, "", fmt.Errorf("github: decoding %s: %w", path, err)
	}
	return b, out.SHA, nil
}

// PutFile creates (sha == "") or updates (sha = current blob sha) path on
// branch and returns the new commit sha. A stale or missing sha yields an
// error satisfying errors.Is(err, ErrConflict).
func (c *Client) PutFile(ctx context.Context, path, branch, message string, content []byte, sha string) (commitSHA string, err error) {
	in := map[string]any{
		"message": message,
		"content": base64.StdEncoding.EncodeToString(content),
	}
	if branch != "" {
		in["branch"] = branch
	}
	if sha != "" {
		in["sha"] = sha
	}
	var out struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	u := c.api() + c.repoPath("contents", escapePath(path))
	if err := c.doJSON(ctx, http.MethodPut, u, in, &out); err != nil {
		var ae *APIError
		if errors.As(err, &ae) && (ae.Status == 409 || ae.Status == 422) {
			return "", fmt.Errorf("%w: %w", ErrConflict, err)
		}
		return "", err
	}
	return out.Commit.SHA, nil
}

// DispatchWorkflow triggers a workflow_dispatch event for workflow (file name
// or ID) on ref.
func (c *Client) DispatchWorkflow(ctx context.Context, workflow, ref string, inputs map[string]string) error {
	in := map[string]any{"ref": ref}
	if len(inputs) > 0 {
		in["inputs"] = inputs
	}
	u := c.api() + c.repoPath("actions", "workflows", url.PathEscape(workflow), "dispatches")
	resp, err := c.send(ctx, reqSpec{method: http.MethodPost, url: u, ctype: "application/json", body: mustJSON(in)})
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("github: dispatch: unexpected HTTP %d", resp.StatusCode)
	}
	return nil
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// Run is a workflow run.
type Run struct {
	ID         int64     `json:"id"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	URL        string    `json:"html_url"`
	Event      string    `json:"event"`
	HeadBranch string    `json:"head_branch"`
	HeadSHA    string    `json:"head_sha"`
	CreatedAt  time.Time `json:"created_at"`
}

// ListRuns lists the most recent runs of workflow, optionally filtered by
// branch, newest first. limit <= 0 uses GitHub's default page size.
func (c *Client) ListRuns(ctx context.Context, workflow, branch string, limit int) ([]Run, error) {
	q := url.Values{}
	if branch != "" {
		q.Set("branch", branch)
	}
	if limit > 0 {
		q.Set("per_page", strconv.Itoa(limit))
	}
	u := c.api() + c.repoPath("actions", "workflows", url.PathEscape(workflow), "runs")
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var out struct {
		Runs []Run `json:"workflow_runs"`
	}
	if err := c.doJSON(ctx, http.MethodGet, u, nil, &out); err != nil {
		return nil, err
	}
	return out.Runs, nil
}
