// Package fetch rebuilds a repository baseline on disk from the published
// site: it downloads pkgops-index.json and every package file the index lists,
// verifying sha256 and size, so the next build can add packages to the full
// history instead of replacing it.
//
// The index is not signed. With Options.TrustedKey, Pages first verifies the
// signed catalogs of the published site against that key and refuses an index
// that disagrees with them (see verify.go), so a baseline that is about to be
// signed again never takes its newest packages from unsigned data.
package fetch

import (
	"bytes"
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
	"strconv"
	"strings"
	"sync/atomic"
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
	// HTTP is the client (default: 60 s header timeout). Bodies are bounded
	// by IdleTimeout and the size-scaled deadline below, whatever the client.
	HTTP *http.Client
	// TrustedKey is the PEM public key the published catalogs must be signed
	// with. It must come from a trusted place (a checkout, a pinned
	// fingerprint), never from the site itself. Set, Pages verifies the
	// previous signed state before it writes anything (verify.go).
	TrustedKey []byte
	// IdleTimeout aborts a download that receives nothing for this long
	// (default 60 s).
	IdleTimeout time.Duration
	// MinDeadline and MinRate bound one download as a whole: MinDeadline
	// plus the expected size at MinRate bytes per second (defaults 2 min and
	// 64 KiB/s).
	MinDeadline time.Duration
	MinRate     int64
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
	// Signed counts the files verified against a signed catalog (TrustedKey),
	// Unsigned the older versions that no signature covers and that were
	// checked against the index only.
	Signed   int
	Unsigned int
}

func (o *Options) logf(f string, a ...any) {
	if o.Log != nil {
		fmt.Fprintf(o.Log, f+"\n", a...)
	}
}

func (o *Options) idle() time.Duration {
	if o.IdleTimeout > 0 {
		return o.IdleTimeout
	}
	return 60 * time.Second
}

// deadline is the time one transfer of n bytes may take as a whole.
func (o *Options) deadline(n int64) time.Duration {
	base, rate := o.MinDeadline, o.MinRate
	if base <= 0 {
		base = 2 * time.Minute
	}
	if rate <= 0 {
		rate = 64 << 10
	}
	return base + time.Duration(n/rate)*time.Second
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
	data, code, err := getAll(ctx, o, u, 64<<20)
	switch {
	case err != nil:
		return nil, nil, fmt.Errorf("fetching %s: %w", u, err)
	case code == http.StatusNotFound:
		return nil, nil, fmt.Errorf("%w (%s: 404)", ErrNoBaseline, u)
	case code != http.StatusOK:
		return nil, nil, fmt.Errorf("fetching %s: HTTP %d", u, code)
	}
	ix, err := index.Parse(data)
	if err != nil {
		return nil, nil, err
	}
	return ix, data, nil
}

// getAll reads a small document (an index, a catalog) of at most limit bytes,
// with the same idle timeout and deadline as a package download. The body is
// only read for 200 answers.
func getAll(ctx context.Context, o *Options, u string, limit int64) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, o.deadline(limit))
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := o.client().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, nil
	}
	if resp.ContentLength > limit {
		return nil, resp.StatusCode, fmt.Errorf("%s announces %d bytes, more than the %d allowed", u, resp.ContentLength, limit)
	}
	var b bytes.Buffer
	if _, err := copyBody(ctx, &b, resp.Body, limit, false, o.idle(), cancel); err != nil {
		return nil, resp.StatusCode, err
	}
	return b.Bytes(), resp.StatusCode, nil
}

// Pages downloads the baseline from the published site.
func Pages(ctx context.Context, o Options) (*Result, error) {
	ix, _, err := FetchIndex(ctx, &o)
	if err != nil {
		return nil, err
	}
	res := &Result{Source: "pages", Index: ix}
	if o.TrustedKey != nil {
		if err := verifyPrevious(ctx, &o, ix, res); err != nil {
			return nil, err
		}
	}
	get := func(ctx context.Context, e index.Entry, dst string) error {
		u, err := join(o.From, e.File)
		if err != nil {
			return err
		}
		return download(ctx, &o, u, dst, e.Size, &res.Bytes)
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
// server honours Range. It never reads more than the expected size: a longer
// body, a Content-Length or Content-Range that does not fit, a body that stalls
// for IdleTimeout or a transfer slower than the size-scaled deadline is an
// error. The caller verifies the content.
func download(ctx context.Context, o *Options, u, dst string, size int64, total *int64) error {
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
	ctx, cancel := context.WithTimeout(ctx, o.deadline(size-have))
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
	}
	resp, err := o.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	flags := os.O_CREATE | os.O_WRONLY
	switch resp.StatusCode {
	case http.StatusPartialContent:
		if have == 0 {
			return fmt.Errorf("%s answered 206 to a request without Range", u)
		}
		if err := checkContentRange(resp.Header.Get("Content-Range"), have, size); err != nil {
			return fmt.Errorf("resuming %s: %w", u, err)
		}
		flags |= os.O_APPEND
	case http.StatusOK:
		flags |= os.O_TRUNC // no resume: start over
		have = 0
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s is listed in the index but the site answers 404", pkgerr.ErrNotFound, u)
	default:
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, u)
	}
	want := size - have
	if resp.ContentLength >= 0 && resp.ContentLength != want {
		return fmt.Errorf("%s announces %d bytes, the index expects %d", u, resp.ContentLength, want)
	}
	f, err := os.OpenFile(dst, flags, 0o644)
	if err != nil {
		return err
	}
	n, err := copyBody(ctx, f, resp.Body, want, true, o.idle(), cancel)
	*total += n
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if errors.Is(err, errTooLong) {
		os.Remove(dst) // never keep bytes from a body that broke its size
	}
	return err
}

var errTooLong = errors.New("the body is longer than expected")

// copyBody copies at most max bytes from body to w. exact also requires
// exactly max bytes. A byte beyond max fails at once, without reading on; no
// progress for idle cancels the transfer through cancel.
func copyBody(ctx context.Context, w io.Writer, body io.Reader, max int64, exact bool, idle time.Duration, cancel context.CancelFunc) (int64, error) {
	var stalled atomic.Bool
	t := time.AfterFunc(idle, func() { stalled.Store(true); cancel() })
	defer t.Stop()
	buf := make([]byte, 32<<10)
	lr := io.LimitReader(body, max+1)
	var n int64
	for {
		m, rerr := lr.Read(buf)
		if m > 0 {
			t.Reset(idle)
			if n+int64(m) > max {
				return n, fmt.Errorf("%w: more than %d bytes", errTooLong, max)
			}
			if _, err := w.Write(buf[:m]); err != nil {
				return n, err
			}
			n += int64(m)
		}
		switch {
		case rerr == io.EOF:
			if exact && n != max {
				return n, fmt.Errorf("the body ended after %d of %d bytes", n, max)
			}
			return n, nil
		case rerr != nil:
			if stalled.Load() {
				return n, fmt.Errorf("no data for %s after %d bytes: the transfer stalled", idle, n)
			}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return n, fmt.Errorf("the transfer was too slow: %d of %d bytes before the deadline", n, max)
			}
			return n, rerr
		}
	}
}

// checkContentRange accepts only "bytes have-(size-1)/size" (or "/*").
func checkContentRange(cr string, have, size int64) error {
	var start, end int64
	var tot string
	if _, err := fmt.Sscanf(cr, "bytes %d-%d/%s", &start, &end, &tot); err != nil {
		return fmt.Errorf("unreadable Content-Range %q", cr)
	}
	if start != have || end != size-1 || (tot != "*" && tot != strconv.FormatInt(size, 10)) {
		return fmt.Errorf("Content-Range %q does not continue the %d bytes already here (file of %d bytes)", cr, have, size)
	}
	return nil
}
