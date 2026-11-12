package build

import "sort"

// systemDirs are the directories below the prefix (/usr/local) that belong to
// the system layout, not to a package: the former BSD.local.dist tree that the
// ports framework never claims, plus the directories that shell and manual
// packages share (shell completion, manual pages). A package that installs a
// file below one of them owns the file, never the directory: pkg removes a
// package's directories when the last file in them goes, and a directory that
// other packages or the system rely on must stay.
//
// BSD.local.dist is no longer shipped in the base system, so this table is
// written out here instead of being read at build time. It is derived from the
// layout of that file and from the shell and manual directories packages share;
// it has not been compared with a current ports tree.
var systemDirs = func() map[string]bool {
	list := []string{
		"", "bin", "etc", "include", "lib", "libdata", "libexec", "man", "sbin", "share", "www",
		"etc/bash_completion.d", "etc/devd", "etc/man.d", "etc/newsyslog.conf.d", "etc/periodic",
		"etc/periodic/daily", "etc/periodic/monthly", "etc/periodic/security", "etc/periodic/weekly",
		"etc/pkg", "etc/pkg/repos", "etc/rc.d", "etc/sysctl.conf.d", "etc/syslog.d",
		"libdata/ldconfig", "libdata/ldconfig32", "libdata/pkgconfig",
		"share/bash-completion", "share/bash-completion/completions", "share/doc", "share/examples",
		"share/fish", "share/fish/vendor_completions.d", "share/fish/vendor_conf.d", "share/fish/vendor_functions.d",
		"share/info", "share/licenses", "share/locale", "share/man", "share/zsh", "share/zsh/site-functions",
	}
	for _, base := range []string{"man", "share/man"} {
		list = append(list, base+"/mann")
		for _, n := range "123456789" {
			list = append(list, base+"/man"+string(n), base+"/cat"+string(n))
		}
	}
	m := make(map[string]bool, len(list))
	for _, d := range list {
		m[d] = true
	}
	return m
}()

// IsSystemDir reports whether rel, a directory path relative to the prefix,
// is one of the system directories a package must not claim.
func IsSystemDir(rel string) bool { return systemDirs[rel] }

// SystemDirs lists the system directories (relative to the prefix), sorted.
func SystemDirs() []string {
	out := make([]string, 0, len(systemDirs))
	for d := range systemDirs {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}
