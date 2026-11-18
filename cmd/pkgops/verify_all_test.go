package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/pkgops/internal/repository"
	"jailops.net/pkgops/internal/testutil"
)

func captureStdout(t *testing.T, f func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = pw
	done := make(chan string)
	go func() { b, _ := io.ReadAll(pr); done <- string(b) }()
	ferr := f()
	pw.Close()
	os.Stdout = old
	return <-done, ferr
}

func TestVerifyAllJSONIsTheGateAPublishWorkflowReads(t *testing.T) {
	root := filepath.Join(t.TempDir(), "site")
	r := &repository.Repository{Root: root, Time: testutil.FixedTime}
	if _, err := r.Update(context.Background(), testutil.MustPkg(t, t.TempDir(), testutil.Fixture("0.1.0"))); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error { return cmdVerify([]string{"--all", "--json", root}) })
	var rep struct {
		OK       bool `json:"ok"`
		Problems []struct{ Kind, Message string }
	}
	if err != nil || json.Unmarshal([]byte(out), &rep) != nil || !rep.OK {
		t.Fatalf("clean tree: %v %s", err, out)
	}
	// a damaged tree: the report says what, and the exit status fails
	os.Remove(filepath.Join(r.Dir("FreeBSD:15:aarch64"), "All", "hello-0.1.0.pkg"))
	out, err = captureStdout(t, func() error { return cmdVerify([]string{"--all", "--json", root}) })
	if err == nil || !strings.Contains(err.Error(), "problem") || json.Unmarshal([]byte(out), &rep) != nil || rep.OK || len(rep.Problems) == 0 {
		t.Fatalf("damaged tree: %v %s", err, out)
	}
	// the plain text form names the problem too
	out, err = captureStdout(t, func() error { return cmdVerify([]string{"--all", root}) })
	if err == nil || !strings.Contains(out, "PROBLEM") {
		t.Fatalf("text: %v %s", err, out)
	}
	if err := cmdVerify([]string{"--json", root}); err == nil {
		t.Fatal("--json without --all must be refused")
	}
}
