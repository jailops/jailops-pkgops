package policy

import (
	"strings"
	"testing"
)

func TestParseAndCheck(t *testing.T) {
	p, err := Parse([]byte("abis: [FreeBSD:15:amd64, FreeBSD:15:aarch64]\nmin_release: \"15.1\"\nretention: {keep: 5}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p.MinOSVersion() != 1501000 || p.Retention.Keep != 5 {
		t.Errorf("%+v", p)
	}
	for _, c := range []struct {
		abi  string
		min  int
		want string
	}{
		{"FreeBSD:15:amd64", 1501000, ""},
		{"FreeBSD:15:aarch64", 1502000, ""},
		{"FreeBSD:14:amd64", 1401000, "supports only FreeBSD:15:amd64, FreeBSD:15:aarch64"},
		{"FreeBSD:15:amd64", 0, "declares no minimum release"},
		{"FreeBSD:15:amd64", 1500000, "below the repository minimum 15.1"},
	} {
		err := p.Check(c.abi, c.min, "x", "1")
		if (err == nil) != (c.want == "") || (err != nil && !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%v: got %v want %q", c, err, c.want)
		}
	}
	var none *Policy
	if err := none.Check("anything", 0, "x", "1"); err != nil {
		t.Errorf("nil policy: %v", err)
	}
	for _, bad := range []string{"abis: [FreeBSD:15:x86]", "abis: [freebsd:15:amd64]", "min_release: 15", "min_release: \"16.0\"\nabis: [FreeBSD:15:amd64]", "mystery: 1", "schema: 3"} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if p, err := Parse([]byte("")); err != nil || p.MinOSVersion() != 0 {
		t.Errorf("empty policy: %v %v", p, err)
	}
}
