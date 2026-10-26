package build

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ServiceDef is the `service:` section of pkgops.yaml: a foreground program
// from which pkgops generates a conventional FreeBSD rc.d script (rc.subr
// based, supervised by daemon(8)), plus the service user and an optional
// newsyslog(8) fragment. See docs/format.md.
//
// The section is a single mapping today. It carries its own name so a later
// list form (several services per package) is a compatible extension.
type ServiceDef struct {
	Name    string   `yaml:"name"`    // rc name, default = package name
	Command string   `yaml:"command"` // the foreground program (absolute, or relative to prefix)
	Args    []string `yaml:"args"`    // default arguments (overridable with <name>_args)
	// User the program runs as (daemon -u). Default: the service name. It is
	// created by the package unless it is "root" or declared in users:.
	User string `yaml:"user"`
	// Restart supervises the program with daemon(8) and restarts it when it
	// exits (daemon -r); RestartDelay is the pause in seconds (daemon -R).
	Restart      bool              `yaml:"restart"`
	RestartDelay int               `yaml:"restart_delay"`
	Env          map[string]string `yaml:"env"` // exported before start
	// rc.d ordering keywords. Require defaults to DAEMON.
	Require []string    `yaml:"require"`
	Before  []string    `yaml:"before"`
	Log     *ServiceLog `yaml:"log"`
}

// ServiceLog routes the program's stdout and stderr (daemon -o, -S).
type ServiceLog struct {
	File   string `yaml:"file"`   // append output here (absolute), mode 0640
	Syslog bool   `yaml:"syslog"` // send output to syslog, tag = service name
	// Rotate writes a newsyslog(8) fragment for File: rotate daily, keep this
	// many compressed archives.
	Rotate int `yaml:"rotate"`
}

var (
	reSvcName  = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
	reRCWord   = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	reEnvName  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	reSafePath = regexp.MustCompile(`^/[A-Za-z0-9._+@%,=:/-]*$`)
	reSafeWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)
)

// shq quotes s for a POSIX shell so it is a single literal word. Words made
// of harmless characters stay unquoted to keep the script readable.
func shq(s string) string {
	if reSafeWord.MatchString(s) {
		return s
	}
	return shqAlways(s)
}

// shqAlways quotes even harmless words.
func shqAlways(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// checkText rejects what cannot be written into a script line: control
// characters (newline and NUL above all) and invalid UTF-8. Everything else is
// quoted, never interpreted.
func checkText(what, s string) error {
	if !utf8.ValidString(s) || len(s) > 4096 {
		return specErr("service: %s is not valid text", what)
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return specErr("service: %s contains a control character", what)
		}
	}
	return nil
}

func checkAbsPath(what, p string) error {
	if !reSafePath.MatchString(p) || strings.Contains(p, "//") {
		return specErr("service: %s %q must be an absolute path of letters, digits and ._+@%%,=:/-", what, p)
	}
	for _, el := range strings.Split(p, "/") {
		if el == ".." {
			return specErr("service: %s %q must not contain ..", what, p)
		}
	}
	return nil
}

func (s *Spec) svcName() string {
	if s.Service.Name != "" {
		return s.Service.Name
	}
	return s.Name
}

func (s *Spec) svcUser() string {
	if s.Service.User != "" {
		return s.Service.User
	}
	return s.svcName()
}

func (s *Spec) svcCommand() string { return s.dest(s.Service.Command) }

func (s *Spec) svcPidfile() string      { return "/var/run/" + s.svcName() + ".pid" }
func (s *Spec) svcChildPidfile() string { return "/var/run/" + s.svcName() + ".child.pid" }

// validateService checks the service section.
func (s *Spec) validateService() error {
	d := s.Service
	if d == nil {
		return nil
	}
	name := s.svcName()
	if !reSvcName.MatchString(name) {
		return specErr("service: name %q must be lowercase letters, digits and underscores (rc.subr derives shell variables such as <name>_enable from it, so no hyphens)", name)
	}
	if d.Command == "" {
		return specErr("service: command is required")
	}
	if err := checkDest(d.Command); err != nil {
		return specErr("service: %v", err)
	}
	if err := checkAbsPath("command", s.svcCommand()); err != nil {
		return err
	}
	if strings.HasSuffix(s.svcCommand(), "/") {
		return specErr("service: command %q must name a file", d.Command)
	}
	for i, a := range d.Args {
		if err := checkText(fmt.Sprintf("args[%d]", i), a); err != nil {
			return err
		}
	}
	if d.User != "" && !reIdent.MatchString(d.User) {
		return specErr("service: invalid user %q", d.User)
	}
	for k, v := range d.Env {
		if !reEnvName.MatchString(k) {
			return specErr("service: env name %q is invalid", k)
		}
		if err := checkText("env "+k, v); err != nil {
			return err
		}
	}
	if d.RestartDelay < 0 || d.RestartDelay > 31536000 {
		return specErr("service: restart_delay must be 1..31536000 seconds")
	}
	if d.RestartDelay != 0 && !d.Restart {
		return specErr("service: restart_delay needs restart: true")
	}
	for _, l := range []struct {
		what string
		v    []string
	}{{"require", d.Require}, {"before", d.Before}} {
		for _, w := range l.v {
			if !reRCWord.MatchString(w) {
				return specErr("service: %s %q must be letters, digits and underscores", l.what, w)
			}
		}
	}
	if l := d.Log; l != nil {
		if l.File != "" {
			if err := checkAbsPath("log.file", l.File); err != nil {
				return err
			}
		}
		if l.Rotate < 0 || l.Rotate > 1000 {
			return specErr("service: log.rotate must be 1..1000 archives")
		}
		if l.Rotate > 0 && l.File == "" {
			return specErr("service: log.rotate needs log.file")
		}
	}
	// no clash with hand-written services or files
	dst := s.dest("etc/rc.d/" + name)
	for _, sv := range s.Services {
		p := sv.Destination
		if p == "" {
			p = "etc/rc.d/" + sv.Name
		}
		if s.dest(p) == dst {
			return specErr("service: services[] (%s) already installs %s", sv.Name, dst)
		}
	}
	for _, f := range s.Files {
		if s.dest(f.Destination) == dst {
			return specErr("service: files[] already installs %s", dst)
		}
	}
	return nil
}

// withService returns the manifest with the generated rc.d script, newsyslog
// fragment and service user folded in as ordinary files: and users: entries,
// so the rest of the build needs no knowledge of the service section. The
// receiver is not modified. Without a service section it returns s itself.
func (s *Spec) withService() *Spec {
	if s.Service == nil {
		return s
	}
	mode := func(m Mode) *Mode { return &m }
	c := *s
	c.Files = append(append([]FileSpec(nil), s.Files...), FileSpec{
		Destination: "etc/rc.d/" + s.svcName(),
		Content:     s.rcScript(),
		Mode:        mode(0o555),
	})
	if frag := s.newsyslogFragment(); frag != "" {
		c.Files = append(c.Files, FileSpec{
			Destination: "etc/newsyslog.conf.d/" + s.svcName() + ".conf", Content: frag, Mode: mode(0o644), Config: true,
		})
	}
	// stop the service before pkg removes its files (a running supervisor
	// would otherwise outlive the package)
	c.Scripts = make(map[string]ScriptSpec, len(s.Scripts)+1)
	for k, v := range s.Scripts {
		c.Scripts[k] = v
	}
	pd := c.Scripts["pre-deinstall"]
	pd.Gen = fmt.Sprintf("# generated by pkgops: stop the %[1]s service\n"+
		"if [ -z \"${PKG_ROOTDIR}\" ] || [ \"${PKG_ROOTDIR}\" = \"/\" ]; then\n"+
		"  if [ -x %[2]s ]; then\n    %[2]s onestop >/dev/null 2>&1 || true\n  fi\nfi\n", s.svcName(), s.dest("etc/rc.d/"+s.svcName()))
	c.Scripts["pre-deinstall"] = pd
	c.Users = append([]UserSpec(nil), s.Users...)
	if u := s.svcUser(); u != "root" {
		found := false
		for _, x := range s.Users {
			found = found || x.Name == u
		}
		if !found {
			c.Users = append(c.Users, UserSpec{Name: u, Comment: s.svcName() + " service"})
		}
	}
	return &c
}

// newsyslogFragment returns the content of the newsyslog.conf.d file, or ""
// when log rotation is not configured.
func (s *Spec) newsyslogFragment() string {
	l := s.Service.Log
	if l == nil || l.Rotate == 0 || l.File == "" {
		return ""
	}
	// daemon -H reopens the file on SIGHUP, and the supervisor (the pid in
	// the pid file) holds it.
	return fmt.Sprintf("# generated by pkgops for the %s service\n"+
		"# logfilename\towner:group\tmode\tcount\tsize\twhen\tflags\tpid_file\tsig\n"+
		"%s\troot:wheel\t0640\t%d\t*\t$D0\tCZ\t%s\t1\n",
		s.svcName(), l.File, l.Rotate, s.svcPidfile())
}

// rcScript renders the rc.d script. Every value that comes from the manifest
// is either validated against a strict pattern (names, paths, keywords) or
// passed through shq, so no manifest string is ever interpreted by the shell.
func (s *Spec) rcScript() string {
	d := s.Service
	n := s.svcName()
	prog := s.svcCommand()
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	require := d.Require
	if require == nil {
		require = []string{"DAEMON"}
	}
	desc := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s.Comment)

	w("#!/bin/sh\n#\n# Generated by pkgops from the service: section of pkgops.yaml (package %s %s).\n", s.Name, s.Version)
	w("# Edit the manifest, not this file. Settings belong in /etc/rc.conf (sysrc).\n\n")
	w("# PROVIDE: %s\n", n)
	if len(require) > 0 {
		w("# REQUIRE: %s\n", strings.Join(require, " "))
	}
	if len(d.Before) > 0 {
		w("# BEFORE: %s\n", strings.Join(d.Before, " "))
	}
	w("# KEYWORD: shutdown\n#\n")
	w("# rc.conf variables:\n")
	w("#   %s_enable=\"YES\"  start at boot (default NO)\n", n)
	w("#   %s_runas         user the program runs as (default %s)\n", n, s.svcUser())
	w("#   %s_args          arguments of the program (replaces the packaged default)\n", n)
	w("#   %s_flags         extra arguments, appended after %s_args\n", n, n)
	w("#   %s_env, %s_chdir  as in rc.subr(8)\n", n, n)
	w("# (not %s_user: rc.subr would run daemon(8) itself as that user)\n\n", n)
	w(". /etc/rc.subr\n\n")
	w("name=%s\n", n)
	w("rcvar=%s_enable\n", n)
	w("desc=%s\n\n", shqAlways(desc))
	w("load_rc_config $name\n\n")
	w(": ${%s_enable:=NO}\n", n)
	w(": ${%s_runas:=%s}\n", n, s.svcUser())
	args := make([]string, len(d.Args))
	for i, a := range d.Args {
		args[i] = shq(a)
	}
	w(": ${%s_args=%s}\n\n", n, shqAlways(strings.Join(args, " ")))

	// daemon(8): -f closes the standard descriptors, -P is the supervisor's
	// pid file (the one rc.subr uses: signalling the program itself would
	// only make -r restart it), -p the program's.
	opts := []string{"-f"}
	switch {
	case d.Restart && d.RestartDelay > 0:
		opts = append(opts, "-R", strconv.Itoa(d.RestartDelay))
	case d.Restart:
		opts = append(opts, "-r")
	}
	opts = append(opts, "-P", s.svcPidfile(), "-p", s.svcChildPidfile(), "-u", "${"+n+"_runas}")
	if l := d.Log; l != nil {
		if l.File != "" {
			opts = append(opts, "-H", "-M", "640", "-o", l.File)
		}
		if l.Syslog {
			opts = append(opts, "-S", "-T", n)
		}
	}
	w("pidfile=%s\n", s.svcPidfile())
	w("procname=/usr/sbin/daemon\n")
	w("command=/usr/sbin/daemon\n")
	w("command_args=\"%s %s ${%s_args} ${%s_flags}\"\n", strings.Join(opts, " "), prog, n, n)
	w("start_precmd=%s_prestart\n", n)
	w("\n%s_prestart()\n{\n", n)
	w("\t# %s_flags are program arguments (see command_args), not daemon(8) options\n\trc_flags=\n", n)
	w("\tif [ ! -x %s ]; then\n\t\twarn \"cannot run %s\"\n\t\treturn 1\n\tfi\n", prog, prog)
	envKeys := make([]string, 0, len(d.Env))
	for k := range d.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		w("\texport %s=%s\n", k, shqAlways(d.Env[k]))
	}
	w("\treturn 0\n}\n")
	w("\nrun_rc_command \"$1\"\n")
	return b.String()
}
