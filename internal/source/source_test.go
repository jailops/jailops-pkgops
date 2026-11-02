package source

import (
	"context"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/pkgops/internal/repository"
	"jailops.net/pkgops/internal/testutil"
)

const abi = "FreeBSD:15:aarch64"

var siteKey, otherKey = mustKey(), mustKey()

func mustKey() *rsa.PrivateKey {
	k, err := repository.GenerateRSAKey(2048)
	if err != nil {
		panic(err)
	}
	return k
}

func pemOf(t *testing.T, k *rsa.PrivateKey) []byte {
	t.Helper()
	b, err := repository.PublicKeyPEM(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// site publishes one package (signed with siteKey unless unsigned) and
// serves the tree; files maps extra site files (a public key) to content.
func site(t *testing.T, unsigned bool, files map[string][]byte) (string, *httptest.Server) {
	t.Helper()
	root := t.TempDir()
	r := &repository.Repository{Root: root, Time: testutil.FixedTime}
	if !unsigned {
		r.Signer = &repository.RSASigner{Key: siteKey}
	}
	if _, err := r.Update(context.Background(), testutil.MustPkg(t, t.TempDir(), testutil.Fixture("1.0.0"))); err != nil {
		t.Fatal(err)
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(root, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(http.FileServer(http.Dir(root)))
	t.Cleanup(srv.Close)
	return root, srv
}

func status(t *testing.T, s *Source) (healthy bool, verification, keySource, fp string, problems, notes []string) {
	t.Helper()
	st, err := s.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.ABIs) != 1 || st.ABIs[0].Verification != st.Verification {
		t.Errorf("abis: %+v", st.ABIs)
	}
	return st.Healthy, st.Verification, st.KeySource, st.Fingerprint, st.Problems, st.Notes
}

// The site of the review: the key published as jailops.pub. It used to show
// healthy and signed with no fingerprint, although nothing was verified.
func TestSourceVerifiesWithTheKeyPublishedAsJailopsPub(t *testing.T) {
	_, srv := site(t, false, map[string][]byte{"jailops.pub": pemOf(t, siteKey)})
	s := &Source{Base: srv.URL}
	ok, v, src, fp, probs, notes := status(t, s)
	if !ok || v != StateVerified || src != "site" || fp != repository.Fingerprint(pemOf(t, siteKey)) || len(probs) != 0 {
		t.Errorf("healthy=%v verification=%s source=%s fp=%q problems=%v", ok, v, src, fp, probs)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "same site") {
		t.Errorf("a site key needs the weaker-trust note: %v", notes)
	}
	if got := s.KeyURL(); got != srv.URL+"/jailops.pub" {
		t.Errorf("key url %q", got)
	}
}

func TestSourceWithoutAKeyIsUnverified(t *testing.T) {
	_, srv := site(t, false, nil)
	ok, v, _, fp, probs, notes := status(t, &Source{Base: srv.URL})
	if !ok || v != StateUnverified || fp != "" || len(probs) != 0 || len(notes) != 1 {
		t.Errorf("healthy=%v verification=%s fp=%q problems=%v notes=%v", ok, v, fp, probs, notes)
	}
	ok, v, _, _, probs, _ = status(t, &Source{Base: srv.URL, RequireVerified: true})
	if ok || v != StateUnverified || len(probs) != 1 {
		t.Errorf("required: healthy=%v verification=%s problems=%v", ok, v, probs)
	}
}

func TestSourceInvalidKeyOrSignature(t *testing.T) {
	cases := map[string]map[string][]byte{
		"bad PEM":   {"pkgops.pub": []byte("-----BEGIN PUBLIC KEY-----\nnot base64\n-----END PUBLIC KEY-----\n")},
		"other key": {"pkgops.pub": pemOf(t, otherKey)},
	}
	for name, files := range cases {
		_, srv := site(t, false, files)
		ok, v, _, _, probs, _ := status(t, &Source{Base: srv.URL})
		if ok || v != StateInvalid || len(probs) != 1 {
			t.Errorf("%s: healthy=%v verification=%s problems=%v", name, ok, v, probs)
		}
	}
	// a damaged data.pkg next to a good packagesite.pkg
	root, srv := site(t, false, map[string][]byte{"pkgops.pub": pemOf(t, siteKey)})
	f := filepath.Join(root, abi, "latest", "data.pkg")
	b, _ := os.ReadFile(f)
	b[len(b)-5] ^= 0xff
	os.WriteFile(f, b, 0o644)
	ok, v, _, _, probs, _ := status(t, &Source{Base: srv.URL})
	if ok || v == StateVerified || len(probs) != 1 {
		t.Errorf("data.pkg: healthy=%v verification=%s problems=%v", ok, v, probs)
	}
}

func TestSourceConfiguredKeyAndFingerprint(t *testing.T) {
	good, bad := pemOf(t, siteKey), pemOf(t, otherKey)
	_, srv := site(t, false, map[string][]byte{"repo.pub": good})
	ok, v, src, _, _, notes := status(t, &Source{Base: srv.URL, PubKey: good})
	if !ok || v != StateVerified || src != "configured" || len(notes) != 0 {
		t.Errorf("configured: %v %s %s %v", ok, v, src, notes)
	}
	if ok, v, _, _, _, _ := status(t, &Source{Base: srv.URL, PubKey: bad}); ok || v != StateInvalid {
		t.Errorf("configured other key: %v %s", ok, v)
	}
	ok, v, src, _, _, notes = status(t, &Source{Base: srv.URL, KeyFingerprint: repository.Fingerprint(good)})
	if !ok || v != StateVerified || src != "configured" || len(notes) != 0 {
		t.Errorf("pinned: %v %s %s %v", ok, v, src, notes)
	}
	ok, v, _, _, probs, _ := status(t, &Source{Base: srv.URL, KeyFingerprint: repository.Fingerprint(bad)})
	if ok || v != StateInvalid || !strings.Contains(strings.Join(probs, " "), "not the configured") {
		t.Errorf("wrong pin: %v %s %v", ok, v, probs)
	}
	// a configured key path
	_, srv2 := site(t, false, map[string][]byte{"keys.pem": good})
	if ok, v, _, _, _, _ := status(t, &Source{Base: srv2.URL, KeyPaths: []string{"keys.pem"}}); !ok || v != StateVerified {
		t.Errorf("key path: %v %s", ok, v)
	}
	// a pinned key the site does not have
	_, srv3 := site(t, false, nil)
	if ok, v, _, _, _, _ := status(t, &Source{Base: srv3.URL, KeyFingerprint: repository.Fingerprint(good)}); ok || v != StateInvalid {
		t.Errorf("pinned, no key: %v %s", ok, v)
	}
}

func TestSourceUnsignedSite(t *testing.T) {
	_, srv := site(t, true, nil)
	if ok, v, _, _, _, _ := status(t, &Source{Base: srv.URL}); !ok || v != StateUnsigned {
		t.Errorf("unsigned: %v %s", ok, v)
	}
	if ok, _, _, _, probs, _ := status(t, &Source{Base: srv.URL, RequireVerified: true}); ok || len(probs) != 1 {
		t.Errorf("unsigned, required: %v %v", ok, probs)
	}
}
