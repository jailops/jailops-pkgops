package build

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestGoEnvDefaultsToolchainAutoAndRespectsTheCaller(t *testing.T) {
	abi, _ := ParseABI("FreeBSD:15:aarch64")
	env := goEnv([]string{"PATH=/bin"}, abi)
	for _, want := range []string{"GOOS=freebsd", "GOARCH=arm64", "CGO_ENABLED=0", "GOTOOLCHAIN=auto"} {
		if !slices.Contains(env, want) {
			t.Errorf("%s missing from %v", want, env)
		}
	}
	for _, set := range []string{"GOTOOLCHAIN=local", "GOTOOLCHAIN=go1.25.3", "GOTOOLCHAIN="} {
		env = goEnv([]string{"PATH=/bin", set}, abi)
		n := 0
		for _, e := range env {
			if strings.HasPrefix(e, "GOTOOLCHAIN=") {
				n++
				if e != set {
					t.Errorf("caller's %q replaced by %q", set, e)
				}
			}
		}
		if n != 1 {
			t.Errorf("%q: %d GOTOOLCHAIN entries", set, n)
		}
	}
	if env := goEnv([]string{"SOURCE_DATE_EPOCH=5"}, abi); !slices.Contains(env, "SOURCE_DATE_EPOCH=5") {
		t.Error("SOURCE_DATE_EPOCH must reach the build")
	}
}

// The build runs go through one seam: it sees the environment and the working
// directory, and the toolchain version is asked for in the module directory
// with the same environment (so toolchain selection applies) and logged once.
func TestGoBuildLogsTheToolchainAndPassesTheEnvironment(t *testing.T) {
	old := newGoCmd
	defer func() { newGoCmd = old }()
	type call struct {
		dir  string
		env  []string
		args []string
	}
	var calls []call
	newGoCmd = func(ctx context.Context, dir string, env []string, args ...string) *exec.Cmd {
		calls = append(calls, call{dir, env, args})
		if args[0] == "env" {
			return exec.Command("echo", "go1.25.9")
		}
		return exec.Command("true")
	}
	t.Setenv("GOTOOLCHAIN", "")
	// unset it for real: LookupEnv must report absence
	if err := os.Unsetenv("GOTOOLCHAIN"); err != nil {
		t.Fatal(err)
	}
	s, err := ParseSpec([]byte(specHead + "abis: [FreeBSD:15:amd64, FreeBSD:15:aarch64]\nbuild:\n  type: go\n  packages:\n    - package: ./a\n    - package: ./b\n"))
	if err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	dir := t.TempDir()
	if err := s.GoBuild(context.Background(), BuildOptions{BaseDir: dir, ABI: "FreeBSD:15:aarch64", Log: &log}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(log.String(), "building with go1.25.9") != 1 {
		t.Errorf("log:\n%s", log.String())
	}
	if len(calls) != 3 || calls[0].args[0] != "env" || calls[1].args[0] != "build" || calls[2].args[0] != "build" {
		t.Fatalf("calls: %+v", calls)
	}
	for _, c := range calls {
		if c.dir != dir && !strings.HasPrefix(c.dir, dir) {
			t.Errorf("dir %q", c.dir)
		}
		if !slices.Contains(c.env, "GOTOOLCHAIN=auto") || !slices.Contains(c.env, "GOARCH=arm64") {
			t.Errorf("env: %v", c.env)
		}
	}
}
