package fetch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jailops.net/pkgops/internal/index"
)

const oneRel = abi + "/latest/All/x-1.pkg"

// oneFile serves an index that lists one file of size bytes and answers the
// file itself with h.
func oneFile(t *testing.T, size int64, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	ix := index.New()
	ix.Packages = append(ix.Packages, index.Entry{ABI: abi, Name: "x", Version: "1", File: oneRel, SHA256: fmt.Sprintf("%064x", 1), Size: size})
	data, _ := json.Marshal(ix)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/"+index.FileName {
			w.Write(data)
			return
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// counting reports how many body bytes the client read.
type countingBody struct {
	io.ReadCloser
	n *atomic.Int64
}

func (c countingBody) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.n.Add(int64(n))
	return n, err
}

type countingRT struct{ n *atomic.Int64 }

func (c countingRT) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err == nil && strings.HasSuffix(r.URL.Path, ".pkg") {
		resp.Body = countingBody{resp.Body, c.n}
	}
	return resp, err
}

func noFinalFile(t *testing.T, into string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(into, filepath.FromSlash(oneRel))); !os.IsNotExist(err) {
		t.Error("a file was installed")
	}
	if _, err := os.Stat(filepath.Join(into, index.FileName)); !os.IsNotExist(err) {
		t.Error("the index was written")
	}
}

func TestDownloadStopsAtTheExpectedSize(t *testing.T) {
	srv := oneFile(t, 1000, func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 64<<10)
		for i := 0; i < 4096; i++ { // up to 256 MiB, chunked (no Content-Length)
			if _, err := w.Write(buf); err != nil {
				return
			}
		}
	})
	var n atomic.Int64
	into := t.TempDir()
	_, err := Pages(context.Background(), Options{From: srv.URL, Into: into, HTTP: &http.Client{Transport: countingRT{&n}}})
	if err == nil || !strings.Contains(err.Error(), "longer than expected") {
		t.Fatalf("err %v", err)
	}
	if got := n.Load(); got > 1001 {
		t.Errorf("read %d body bytes for a 1000 byte file", got)
	}
	noFinalFile(t, into)
	if _, err := os.Stat(filepath.Join(into, filepath.FromSlash(oneRel)) + ".part"); !os.IsNotExist(err) {
		t.Error("the bytes of an oversized body must not stay behind")
	}
}

func TestDownloadRefusesAWrongContentLength(t *testing.T) {
	srv := oneFile(t, 1000, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "5000")
		w.Write(make([]byte, 5000))
	})
	var n atomic.Int64
	into := t.TempDir()
	_, err := Pages(context.Background(), Options{From: srv.URL, Into: into, HTTP: &http.Client{Transport: countingRT{&n}}})
	if err == nil || !strings.Contains(err.Error(), "announces 5000 bytes") {
		t.Fatalf("err %v", err)
	}
	if n.Load() != 0 {
		t.Errorf("read %d bytes of a body with the wrong length", n.Load())
	}
	noFinalFile(t, into)
}

func TestDownloadTimesOutAStalledBody(t *testing.T) {
	block := make(chan struct{})
	srv := oneFile(t, 1000, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.Write(make([]byte, 10))
		w.(http.Flusher).Flush()
		select {
		case <-block:
		case <-r.Context().Done():
		}
	})
	defer close(block)
	into := t.TempDir()
	start := time.Now()
	_, err := Pages(context.Background(), Options{From: srv.URL, Into: into, IdleTimeout: 200 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("err %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %s", d)
	}
	noFinalFile(t, into)
}

func TestDownloadHasAnOverallDeadline(t *testing.T) {
	srv := oneFile(t, 1000, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		for i := 0; i < 1000; i++ { // one byte every 20 ms: never idle, far too slow
			if _, err := w.Write([]byte{0}); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-time.After(20 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
	})
	into := t.TempDir()
	_, err := Pages(context.Background(), Options{From: srv.URL, Into: into, IdleTimeout: 5 * time.Second,
		MinDeadline: 300 * time.Millisecond, MinRate: 1 << 30})
	if err == nil || !strings.Contains(err.Error(), "too slow") {
		t.Fatalf("err %v", err)
	}
	noFinalFile(t, into)
}

func TestDownloadTruncatedBodyFails(t *testing.T) {
	srv := oneFile(t, 1000, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.Write(make([]byte, 400))
		w.(http.Flusher).Flush()
		c, _, _ := w.(http.Hijacker).Hijack()
		c.Close()
	})
	into := t.TempDir()
	if _, err := Pages(context.Background(), Options{From: srv.URL, Into: into}); err == nil {
		t.Fatal("a truncated body was accepted")
	}
	noFinalFile(t, into)
	// what stays is the .part file of the resume feature, a prefix that is
	// only ever installed after the full size and sha256 match
	if st, err := os.Stat(filepath.Join(into, filepath.FromSlash(oneRel)) + ".part"); err != nil || st.Size() > 400 {
		t.Errorf("part: %v %v", st, err)
	}
}

func TestDownloadChecksTheResumedRange(t *testing.T) {
	part := func(t *testing.T, into string) string {
		p := filepath.Join(into, filepath.FromSlash(oneRel)) + ".part"
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, make([]byte, 300), 0o644)
		return p
	}
	for _, cr := range []string{"bytes 0-999/1000", "bytes 300-999/2000", "bytes 300-500/1000", "garbage"} {
		srv := oneFile(t, 1000, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Range", cr)
			w.WriteHeader(http.StatusPartialContent)
			w.Write(make([]byte, 700))
		})
		into := t.TempDir()
		p := part(t, into)
		_, err := Pages(context.Background(), Options{From: srv.URL, Into: into})
		if err == nil || !strings.Contains(err.Error(), "Content-Range") {
			t.Errorf("%q: %v", cr, err)
		}
		if st, _ := os.Stat(p); st == nil || st.Size() != 300 {
			t.Errorf("%q: the partial file changed", cr)
		}
		noFinalFile(t, into)
	}
}
