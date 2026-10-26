// Package fetch rebuilds a repository baseline on disk from the published
// site: it downloads pkgops-index.json and every package file the index lists,
// verifying sha256 and size, so the next build can add packages to the full
// history instead of replacing it.
package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"jailops.net/pkgops/internal/index"
	"jailops.net/pkgops/internal/pkgerr"
	"jailops.net/pkgops/internal/prune"
)

// ErrNoBaseline means the source has no index (a first build, or a damaged site).
var ErrNoBaseline = errors.New("no baseline: the source has no " + index.FileName)

// Options configures a fetch.
type Options struct {
	// From is the base URL of the published site (the directory that holds
	// pkgops-index.json).
	From string
	// Into is the local repository root the baseline is written to.
	Into string
	// ABIs limits the fetch to these ABIs (empty = all).
	ABIs []string
	// Prune lists files that must not be fetched; they are recorded as
	// pruned in the written index.
	Prune *prune.Manifest
	// HTTP is the client (default: 60 s header timeout, no body timeout).
	HTTP *http.Client
	// Log receives one line per file.
	Log io.Writer
	// Now stamps pruned entries (default time.Now).
	Now func() time.Time
}

// Result describes what a fetch did.
type Result struct {
	Source     string
	Index      *index.Index
	Downloaded int
	Reused     int
	Bytes      int64
	Pruned     int
}

func (o *Options) logf(f string, a ...any) {
	if o.Log != nil {
		fmt.Fprintf(o.Log, f+"\n", a...)
	}
}

func (o *Options) client() *http.Client {
	if o.HTTP != nil {
		return o.HTTP
	}
	return &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 60 * time.Second, Proxy: http.ProxyFromEnvironment}}
}

func join(base, rel string) (string, error) {
	u, err := url.Parse(strings.TrimRight(base, "/") + "/")
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("%w: --from must be an http(s) URL, got %q", pkgerr.ErrInvalidSpec, base)
	}
	segs := strings.Split(rel, "/")
	return u.JoinPath(segs...).String(), nil
}

// FetchIndex downloads and parses the published index.
func FetchIndex(ctx context.Context, o *Options) (*index.Index, []byte, error) {
	u, err := join(o.From, index.FileName)
	if err != nil {
		return nil, nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := o.client().Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching %s: %w", u, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, nil, fmt.Errorf("%w (%s: 404)", ErrNoBaseline, u)
	case resp.StatusCode != http.StatusOK:
		return nil, nil, fmt.Errorf("fetching %s: HTTP %d", u, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, nil, err
	}
	ix, err := index.Parse(data)
	if err != nil {
		return nil, nil, err
	}
	return ix, data, nil
}

// Pages downloads the baseline from the published site.
func Pages(ctx context.Context, o Options) (*Result, error) {
	ix, _, err := FetchIndex(ctx, &o)
	if err != nil {
		return nil, err
	}
	res := &Result{Source: "pages", Index: ix}
	get := func(ctx context.Context, e index.Entry, dst string) error {
		u, err := join(o.From, e.File)
		if err != nil {
			return err
		}
		return download(ctx, o.client(), u, dst, e.SHA256, e.Size, &res.Bytes)
	}
	if err := Materialize(ctx, &o, ix, res, get); err != nil {
		return nil, err
	}
	return res, nil
}

// Getter fetches one indexed file to dst (a .part path that may already hold
// a partial download) and must verify nothing: Materialize verifies.
type Getter func(ctx context.Context, e index.Entry, dst string) error

// Materialize writes the baseline described by ix into o.Into, using get for
// every file that is missing or wrong, then writes the index. Files are
// verified against the index (size and sha256); any mismatch or failure is an
// error and the index is not written, so a half baseline is never mistaken
// for a whole one.
func Materialize(ctx context.Context, o *Options, ix *index.Index, res *Result, get Getter) error {
	if o.Into == "" {
		return fmt.Errorf("%w: Into is required", pkgerr.ErrInvalidSpec)
	}
	if err := o.Prune.Check(ix); err != nil {
		return err
	}
	want := map[string]bool{}
	for _, a := range o.ABIs {
		want[a] = true
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	out := index.New()
	out.Generator = ix.Generator
	out.Generated = ix.Generated
	out.Pruned = append(out.Pruned, ix.Pruned...)
	var todo []index.Entry
	for _, e := range ix.Packages {
		if len(want) > 0 && !want[e.ABI] {
			continue
		}
		if m, ok := o.Prune.Has(e.ABI, e.Name, e.Version); ok {
			why := m.Reason
			if why == "" {
				why = "prune manifest"
			}
			if _, done := out.FindPruned(e.ABI, e.Name, e.Version); !done {
				out.Pruned = append(out.Pruned, index.Pruned{ABI: e.ABI, Name: e.Name, Version: e.Version, SHA256: e.SHA256,
					Size: e.Size, Built: e.Built, Reason: why, PrunedAt: now().UTC()})
			}
			res.Pruned++
			o.logf("skip   %s (pruned: %s)", e.File, why)
			continue
		}
		out.Packages = append(out.Packages, e)
		todo = append(todo, e)
	}
	if len(want) > 0 { // keep other ABIs' history out of a partial baseline
		var keep []index.Pruned
		for _, p := range out.Pruned {
			if want[p.ABI] {
				keep = append(keep, p)
			}
		}
		out.Pruned = keep
	}
	sort.Slice(todo, func(i, j int) bool { return todo[i].File < todo[j].File })
	for _, e := range todo {
		if err := ctx.Err(); err != nil {
			return err
		}
		dst := filepath.Join(o.Into, filepath.FromSlash(e.File))
		if ok, _ := fileMatches(dst, e.SHA256, e.Size); ok {
			res.Reused++
			o.logf("have   %s", e.File)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		part := dst + ".part"
		if err := get(ctx, e, part); err != nil {
			return fmt.Errorf("fetching %s: %w (the partial file stays at %s; run the same command again to resume)", e.File, err, part)
		}
		if ok, err := fileMatches(part, e.SHA256, e.Size); !ok {
			os.Remove(part)
			return fmt.Errorf("%w: %s does not match the index (%v); the source is damaged or tampered with", pkgerr.ErrInvalidPackage, e.File, err)
		}
		if err := os.Rename(part, dst); err != nil {
			return err
		}
		res.Downloaded++
		o.logf("fetch  %s (%d bytes)", e.File, e.Size)
	}
	out.Sort()
	res.Index = out
	return out.Write(o.Into)
}

func fileMatches(file, sum string, size int64) (bool, error) {
	f, err := os.Open(file)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return false, err
	}
	if n != size {
		return false, fmt.Errorf("size %d, index says %d", n, size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sum {
		return false, fmt.Errorf("sha256 %s, index says %s", got[:12], sum[:12])
	}
	return true, nil
}

// download fetches u into dst, resuming from the bytes already in dst when the
// server honours Range. The caller verifies the result.
func download(ctx context.Context, c *http.Client, u, dst, sum string, size int64, total *int64) error {
	var have int64
	if st, err := os.Stat(dst); err == nil {
		have = st.Size()
		if have > size {
			os.Remove(dst)
			have = 0
		}
	}
	if have == size && have > 0 {
		return nil // complete partial; verification decides
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	flags := os.O_CREATE | os.O_WRONLY
	switch resp.StatusCode {
	case http.StatusPartialContent:
		flags |= os.O_APPEND
	case http.StatusOK:
		flags |= os.O_TRUNC // no resume: start over
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s is listed in the index but the site answers 404", pkgerr.ErrNotFound, u)
	default:
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, u)
	}
	f, err := os.OpenFile(dst, flags, 0o644)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, resp.Body)
	*total += n
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
