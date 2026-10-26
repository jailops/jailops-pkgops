package pkgops_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"jailops.net/pkgops"
)

// Build a package entirely from Go values: no manifest file, no FreeBSD host.
func ExamplePackage_Write() {
	p := &pkgops.Package{
		Name: "hello", Version: "1.0.0", Comment: "Example", Maintainer: "a@b.c", WWW: "https://example.com", ABI: "FreeBSD:15:amd64",
		Files: []pkgops.File{
			{Path: "/usr/local/bin/hello", Data: []byte("#!/bin/sh\necho hi\n"), Mode: 0o755},
			{Path: "/usr/local/etc/hello.conf", Data: []byte("x=1\n"), Mode: 0o644, Config: true},
		},
		Time: time.Unix(1700000000, 0), // fixed time: reproducible output
	}
	var buf bytes.Buffer
	info, err := p.Write(&buf, pkgops.WriteOptions{})
	if err != nil {
		panic(err)
	}
	fmt.Println(info.Name, info.Version, info.Arch, info.Flatsize)
	// Output: hello 1.0.0 freebsd:15:amd64:64 22
}

// Load a pkgops.yaml, resolve it for an ABI and build the .pkg.
func ExampleBuildPackage() {
	dir, _ := os.MkdirTemp("", "pkgops-example")
	defer os.RemoveAll(dir)
	spec, err := pkgops.ParseSpec([]byte(`
name: hello
version: 1.0.0
comment: Example
maintainer: a@b.c
www: https://example.com
abi: FreeBSD:15:aarch64
files:
  - content: "hello\n"
    destination: share/hello/greeting
`))
	if err != nil {
		panic(err)
	}
	built, err := pkgops.BuildPackage(context.Background(), spec, pkgops.BuildOptions{OutDir: dir, Epoch: time.Unix(1700000000, 0)})
	if err != nil {
		panic(err)
	}
	rel, _ := filepath.Rel(dir, built.Path)
	fmt.Println(rel)
	// Output: FreeBSD:15:aarch64/hello-1.0.0.pkg
}

// Create a signed repository and verify it the way a client would.
func ExampleRepository_Update() {
	dir, _ := os.MkdirTemp("", "pkgops-example")
	defer os.RemoveAll(dir)
	spec, _ := pkgops.ParseSpec([]byte("name: hello\nversion: 1.0.0\ncomment: Example\nmaintainer: a@b.c\nwww: https://example.com\nabi: FreeBSD:15:amd64\n"))
	built, err := pkgops.BuildPackage(context.Background(), spec, pkgops.BuildOptions{OutDir: dir, Epoch: time.Unix(1700000000, 0)})
	if err != nil {
		panic(err)
	}

	key, _ := pkgops.GenerateRSAKey(2048)
	pub, _ := pkgops.PublicKeyPEM(&key.PublicKey)
	repo := &pkgops.Repository{
		Root:   filepath.Join(dir, "site"),
		Signer: &pkgops.RSASigner{Key: key}, // clients: signature_type "pubkey"
	}
	if _, err := repo.Update(context.Background(), built.Path); err != nil {
		panic(err)
	}
	res, err := pkgops.VerifyRepository(repo.Dir("FreeBSD:15:amd64"), pub)
	if err != nil {
		panic(err)
	}
	fmt.Println(res.Packages, res.Signature)
	// Output: 1 pubkey
}

func ExampleCompareVersions() {
	fmt.Println(pkgops.CompareVersions("1.0.9", "1.0.10"), pkgops.CompareVersions("2.0_1", "2.0"))
	// Output: -1 1
}
