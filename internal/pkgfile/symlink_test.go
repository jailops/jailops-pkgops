package pkgfile

import "testing"

// Vectors taken from FreeBSD-runtime-15.1p6, a package built by FreeBSD itself
// (pkgbase, "1$" sums), so the rule is checked against pkg's own output.
func TestSymlinkSumMatchesFreeBSDPackages(t *testing.T) {
	for target, want := range map[string]string{
		"mail/aliases":            "1$6fb4748e847c31b9a0078accbb76821788981391a5bd7a99f4c14a0a8d4e3a82",
		"../var/run/os-release":   "1$e23e5d27a373d32da24d9abc171da71fd858d117231f03c790b94ecf1ed4a82f",
		"../../bin/pgrep":         "1$7b7c90ffd8b7b6de2694f7a3841fd42737b60de005fe2069f05ae8d5849e75c4",
		"/usr/share/misc/termcap": "1$c482548cd5a2075ebe27321480932ec8470b0a7581fdd1ac8771f3e61f8fe9e0", // absolute: no leading slash, NUL included
	} {
		if got := symlinkSum(target); got != want {
			t.Errorf("symlinkSum(%q) = %s, want %s", target, got, want)
		}
	}
}
