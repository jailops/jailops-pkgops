package build

import (
	"bytes"
	"context"
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const helloMain = `package main

import "fmt"

var version = "dev"

func main() { fmt.Println("hello", version) }
`

func writeModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/hello\n\ngo 1.21\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "cmd", "hello"), 0o755)
	os.WriteFile(filepath.Join(dir, "cmd", "hello", "main.go"), []byte(helloMain), 0o644)
	os.WriteFile(filepath.Join(dir, "pkgops.yaml"), []byte(`
name: hello
version: 1.2.3
comment: hello
maintainer: a@b.c
www: https://example.com
abi: FreeBSD:15:amd64
build:
  type: go
  package: ./cmd/hello
  ldflags: -s -w -X main.version=${VERSION}
files:
  - source: build/${ARCH}/hello
    destination: bin/hello
`), 0o644)
	return dir
}

// Cross-compiles for FreeBSD, checks the ELF is a FreeBSD binary of the right
// architecture, and checks that two full builds are byte-identical.
func TestGoBuildCrossCompileReproducible(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not in PATH")
	}
	if testing.Short() {
		t.Skip("-short")
	}
	for _, c := range []struct {
		abi     string
		machine elf.Machine
	}{{"FreeBSD:15:amd64", elf.EM_X86_64}, {"FreeBSD:15:aarch64", elf.EM_AARCH64}} {
		dir := writeModule(t)
		spec, err := LoadSpec(filepath.Join(dir, "pkgops.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var pkgs [2][]byte
		for i := range pkgs {
			os.RemoveAll(filepath.Join(dir, "build"))
			b, err := BuildPackage(context.Background(), spec, BuildOptions{
				BaseDir: dir, ABI: c.abi, OutDir: filepath.Join(dir, "dist"), Epoch: time.Unix(1700000000, 0)})
			if err != nil {
				t.Fatal(err)
			}
			pkgs[i], _ = os.ReadFile(b.Path)
		}
		if !bytes.Equal(pkgs[0], pkgs[1]) {
			t.Errorf("%s: two builds differ", c.abi)
		}
		abi, _ := ParseABI(c.abi)
		bin := filepath.Join(dir, "build", abi.Arch, "hello")
		f, err := elf.Open(bin)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if f.OSABI != elf.ELFOSABI_FREEBSD || f.Machine != c.machine {
			t.Errorf("%s: ELF osabi=%v machine=%v", c.abi, f.OSABI, f.Machine)
		}
		if libs, _ := f.ImportedLibraries(); len(libs) != 0 {
			t.Errorf("%s: binary is not static: %v", c.abi, libs)
		}
	}
}

func TestGoBuildFailureIsReported(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not in PATH")
	}
	dir := writeModule(t)
	os.WriteFile(filepath.Join(dir, "cmd", "hello", "main.go"), []byte("package main\nfunc main() { undefined() }\n"), 0o644)
	spec, _ := LoadSpec(filepath.Join(dir, "pkgops.yaml"))
	_, err := BuildPackage(context.Background(), spec, BuildOptions{BaseDir: dir, OutDir: filepath.Join(dir, "dist"), Epoch: time.Unix(1, 0)})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("undefined")) {
		t.Fatalf("want build error mentioning undefined, got %v", err)
	}
}

func TestUnknownVariable(t *testing.T) {
	if _, err := expandVars("${NOPE}", map[string]string{}); err == nil {
		t.Fatal("unknown variable accepted")
	}
}
