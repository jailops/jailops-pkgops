package build

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/pkgops/internal/pkgerr"
)

const svcHead = "name: demo\nversion: 1.0.0\ncomment: demo\nmaintainer: a@b.c\nwww: https://example.com\nabi: FreeBSD:15:amd64\n"

func svcSpec(t *testing.T, service string) *Spec {
	t.Helper()
	s, err := ParseSpec([]byte(svcHead + service))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestServiceMinimal(t *testing.T) {
	s := svcSpec(t, "service:\n  name: myservice\n  command: /usr/local/bin/myservice\n  user: myservice\n  restart: true\n")
	got := s.rcScript()
	for _, want := range []string{
		"# PROVIDE: myservice\n", "# REQUIRE: DAEMON\n", "# KEYWORD: shutdown\n",
		"rcvar=myservice_enable\n", ": ${myservice_enable:=NO}\n", ": ${myservice_runas:=myservice}\n",
		"pidfile=/var/run/myservice.pid\n", "procname=/usr/sbin/daemon\n", "command=/usr/sbin/daemon\n",
		`command_args="-f -r -P /var/run/myservice.pid -p /var/run/myservice.child.pid -u ${myservice_runas} /usr/local/bin/myservice ${myservice_args} ${myservice_flags}"`,
		"start_precmd=myservice_prestart\n", "run_rc_command \"$1\"\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("script lacks %q:\n%s", want, got)
		}
	}
	syntaxCheck(t, got)

	// no restart: no -r
	s = svcSpec(t, "service: {command: bin/x, name: x}\n")
	if sc := s.rcScript(); strings.Contains(sc, " -r ") || strings.Contains(sc, " -R ") || !strings.Contains(sc, "-u ${x_runas} /usr/local/bin/x ") {
		t.Errorf("unexpected supervision or command:\n%s", sc)
	}
	// delay
	s = svcSpec(t, "service: {command: bin/x, name: x, restart: true, restart_delay: 5}\n")
	if !strings.Contains(s.rcScript(), "-f -R 5 -P") {
		t.Errorf("no -R 5:\n%s", s.rcScript())
	}
}

func TestServiceFull(t *testing.T) {
	s := svcSpec(t, `service:
  name: full
  command: /usr/local/sbin/full
  args: [-listen, ":8080", "it's a $(touch /tmp/pwned) ; `+"`id`"+` \"x\"", ""]
  env: {B: "two words", A: "q'uote $HOME"}
  require: [NETWORKING, mysql]
  before: [LOGIN]
  log: {file: /var/log/full.log, syslog: true, rotate: 3}
`)
	got := s.rcScript()
	syntaxCheck(t, got)
	for _, want := range []string{
		"# REQUIRE: NETWORKING mysql\n", "# BEFORE: LOGIN\n",
		"-H -M 640 -o /var/log/full.log -S -T full",
		"\texport A='q'\\''uote $HOME'\n\texport B='two words'\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("script lacks %q:\n%s", want, got)
		}
	}
	if !strings.Contains(s.newsyslogFragment(), "/var/log/full.log\troot:wheel\t0640\t3\t*\t$D0\tCZ\t/var/run/full.pid\t1") {
		t.Errorf("fragment: %q", s.newsyslogFragment())
	}
	// the script evaluates the args back to the exact words
	if sh, err := exec.LookPath("sh"); err == nil {
		line := ""
		for _, l := range strings.Split(got, "\n") {
			if strings.HasPrefix(l, ": ${full_args=") {
				line = l
			}
		}
		prog := "full_unset=; " + line + "\neval set -- $full_args\nfor a; do printf '[%s]\\n' \"$a\"; done\n"
		out, err := exec.Command(sh, "-c", prog).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		want := "[-listen]\n[:8080]\n[it's a $(touch /tmp/pwned) ; `id` \"x\"]\n[]\n"
		if string(out) != want {
			t.Errorf("args round trip: %q, want %q", out, want)
		}
	}
}

func syntaxCheck(t *testing.T, script string) {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		return
	}
	f := filepath.Join(t.TempDir(), "rc")
	if err := os.WriteFile(f, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(sh, "-n", f).CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v: %s\n%s", err, out, script)
	}
}

func TestServicePackage(t *testing.T) {
	s := svcSpec(t, "service: {name: myservice, command: /usr/local/bin/myservice, user: myservice, log: {file: /var/log/m.log, rotate: 2}}\nusers: [{name: other}]\n")
	p, err := s.Package(BuildOptions{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]File{}
	for _, f := range p.Files {
		files[f.Path] = f
	}
	rc := files["/usr/local/etc/rc.d/myservice"]
	if rc.Mode != 0o555 || !strings.Contains(string(rc.Data), "# PROVIDE: myservice") {
		t.Errorf("rc.d file: %+v", rc)
	}
	if f := files["/usr/local/etc/newsyslog.conf.d/myservice.conf"]; !f.Config || f.Mode != 0o644 {
		t.Errorf("newsyslog fragment: %+v", f)
	}
	if strings.Join(p.Users, ",") != "myservice,other" || !strings.Contains(p.Scripts["pre-install"], "${PW} useradd myservice -g myservice -d /nonexistent -s /usr/sbin/nologin") {
		t.Errorf("users %v, script %q", p.Users, p.Scripts["pre-install"])
	}
	if pd := p.Scripts["pre-deinstall"]; !strings.Contains(pd, "/usr/local/etc/rc.d/myservice onestop") {
		t.Errorf("pre-deinstall: %q", pd)
	}
	if len(s.Users) != 1 || len(s.Files) != 0 {
		t.Error("Package modified the spec")
	}

	// a declared user is not created twice; root is never created
	s = svcSpec(t, "service: {name: a, command: /bin/a, user: svc}\nusers: [{name: svc, home: /var/db/svc}]\n")
	p, err = s.Package(BuildOptions{BaseDir: t.TempDir()})
	if err != nil || strings.Count(p.Scripts["pre-install"], "useradd") != 1 || !strings.Contains(p.Scripts["pre-install"], "-d /var/db/svc") {
		t.Errorf("declared user: %v %q", err, p.Scripts["pre-install"])
	}
	s = svcSpec(t, "service: {name: a, command: /bin/a, user: root}\n")
	if p, err = s.Package(BuildOptions{BaseDir: t.TempDir()}); err != nil || len(p.Users) != 0 {
		t.Errorf("root: %v %v", err, p.Users)
	}
}

func TestServiceInvalid(t *testing.T) {
	for name, y := range map[string]string{
		"hyphen name":      "service: {name: my-svc, command: /bin/x}\n",
		"upper name":       "service: {name: MySvc, command: /bin/x}\n",
		"no command":       "service: {name: x}\n",
		"command metachar": "service: {name: x, command: \"/bin/x; rm -rf /\"}\n",
		"command dotdot":   "service: {name: x, command: /bin/../x}\n",
		"newline arg":      "service: {name: x, command: /bin/x, args: [\"a\\nb\"]}\n",
		"nul env":          "service: {name: x, command: /bin/x, env: {A: \"a\\u0000b\"}}\n",
		"env name":         "service: {name: x, command: /bin/x, env: {\"A B\": c}}\n",
		"env name inject":  "service: {name: x, command: /bin/x, env: {\"A=1;id\": c}}\n",
		"bad user":         "service: {name: x, command: /bin/x, user: \"a b\"}\n",
		"delay w/o r":      "service: {name: x, command: /bin/x, restart_delay: 3}\n",
		"neg delay":        "service: {name: x, command: /bin/x, restart: true, restart_delay: -1}\n",
		"require inject":   "service: {name: x, command: /bin/x, require: [\"A; id\"]}\n",
		"before inject":    "service: {name: x, command: /bin/x, before: [\"A\\nB\"]}\n",
		"log path":         "service: {name: x, command: /bin/x, log: {file: \"/var/log/x log\"}}\n",
		"log relative":     "service: {name: x, command: /bin/x, log: {file: x.log}}\n",
		"rotate no file":   "service: {name: x, command: /bin/x, log: {rotate: 3}}\n",
		"unknown key":      "service: {name: x, command: /bin/x, restrat: true}\n",
		"unknown log key":  "service: {name: x, command: /bin/x, log: {fil: /a}}\n",
		"clash services":   "service: {name: x, command: /bin/x}\nservices: [{name: x, content: \"#!/bin/sh\"}]\n",
		"clash files":      "service: {name: x, command: /bin/x}\nfiles: [{content: y, destination: etc/rc.d/x}]\n",
	} {
		if _, err := ParseSpec([]byte(svcHead + y)); !errors.Is(err, pkgerr.ErrInvalidSpec) {
			t.Errorf("%s: want ErrInvalidSpec, got %v", name, err)
		}
	}
	// coexistence with a hand-written service is fine
	svcSpec(t, "service: {name: x, command: /bin/x}\nservices: [{name: y, content: \"#!/bin/sh\"}]\n")
}
