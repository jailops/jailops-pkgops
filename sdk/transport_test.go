package sdk

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestTokenIsNotSentOverPlainHTTPToANonLoopbackHost(t *testing.T) {
	var hit atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit.Store(true) }))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	c := &Client{BaseURL: "http://registry.example.org:" + strconv.Itoa(port), Token: "secret"}
	if err := c.Health(context.Background()); !errors.Is(err, ErrInsecureTransport) {
		t.Fatalf("want ErrInsecureTransport, got %v", err)
	}
	if hit.Load() {
		t.Fatal("a request was sent")
	}
	if err := (&Client{BaseURL: srv.URL, Token: "secret"}).Health(context.Background()); err != nil {
		t.Fatalf("loopback with a token: %v", err)
	}
	if err := (&Client{BaseURL: srv.URL}).Health(context.Background()); err != nil {
		t.Fatalf("loopback without a token: %v", err)
	}
	for _, tc := range []struct {
		c    Client
		want bool
	}{
		{Client{BaseURL: "http://registry.example.org", Token: "s", AllowPlainHTTP: true}, true},
		{Client{BaseURL: "https://registry.example.org", Token: "s"}, true},
		{Client{BaseURL: "http://registry.example.org"}, true}, // no token, nothing to protect
		{Client{BaseURL: "http://localhost:1", Token: "s"}, true},
		{Client{BaseURL: "http://[::1]:1", Token: "s"}, true},
		{Client{BaseURL: "http://127.0.0.2:1", Token: "s"}, true},
		{Client{BaseURL: "http://192.0.2.1", Token: "s"}, false},
		{Client{BaseURL: "http://registry.example.org", Token: "s"}, false},
	} {
		u, _ := url.Parse(tc.c.BaseURL)
		if got := tc.c.checkTransport(u) == nil; got != tc.want {
			t.Errorf("%s token=%q plain=%v: allowed=%v, want %v", tc.c.BaseURL, tc.c.Token, tc.c.AllowPlainHTTP, got, tc.want)
		}
	}
}

func TestTokenStaysWithItsOriginOnRedirects(t *testing.T) {
	var other atomic.Value
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		other.Store(r.Header.Get("Authorization"))
	}))
	defer elsewhere.Close()
	home := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/api/v1/health", http.StatusTemporaryRedirect)
	}))
	defer home.Close()
	// two loopback servers differ in port: another origin
	c := &Client{BaseURL: home.URL, Token: "secret"}
	if err := c.Health(context.Background()); err == nil {
		t.Fatal("the redirect to another origin must not be followed")
	}
	if v := other.Load(); v != nil {
		t.Fatalf("the other origin was reached, with Authorization %q", v)
	}
	// the caller's own client is left as it was
	hc := &http.Client{}
	_ = (&Client{BaseURL: home.URL, Token: "secret", HTTP: hc}).Health(context.Background())
	if hc.CheckRedirect != nil {
		t.Error("the caller's client was modified")
	}
}
