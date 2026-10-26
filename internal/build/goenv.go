package build

import (
	"context"
	"os/exec"
	"strings"
)

// newGoCmd creates the go command; tests replace it to see the environment
// and arguments without running a toolchain.
var newGoCmd = func(ctx context.Context, dir string, env []string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	cmd.Env = env
	return cmd
}

// goEnv is the environment of a cross build for one ABI. GOTOOLCHAIN defaults
// to auto, so the go and toolchain lines of the module's go.mod pick (and
// download) the Go that builds it, whatever old Go a distribution installed;
// a GOTOOLCHAIN set by the caller or CI is respected. The Go version is part of
// the build identity: the same inputs built by the same Go give the same bytes.
func goEnv(base []string, abi ABI) []string {
	env := append([]string(nil), base...)
	env = append(env, "GOOS=freebsd", "GOARCH="+abi.GOARCH(), "CGO_ENABLED=0")
	if _, set := lookup(base, "GOTOOLCHAIN"); !set {
		env = append(env, "GOTOOLCHAIN=auto")
	}
	if epoch, ok := lookup(base, "SOURCE_DATE_EPOCH"); ok {
		env = append(env, "SOURCE_DATE_EPOCH="+epoch)
	}
	return env
}

func lookup(env []string, key string) (string, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], key+"="); ok {
			return v, true
		}
	}
	return "", false
}

// goVersion returns the version of the toolchain that will build the module in
// dir ("go1.25.3"), after toolchain selection, or "" when it cannot be told.
func goVersion(ctx context.Context, dir string, env []string) string {
	cmd := newGoCmd(ctx, dir, env, "env", "GOVERSION")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
