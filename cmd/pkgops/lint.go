package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"jailops.net/pkgops"
)

// exitCode is returned by a command that decides its own exit status; main
// exits with it without printing anything more.
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// cmdLint is `pkgops lint`: the checks a port gets from stage-qa, for recipes.
// Exit status: 0 nothing at or above --fail-on, 1 findings, 2 usage.
func cmdLint(args []string, out io.Writer) error {
	fs := newFlags("lint")
	var recipes multiFlag
	fs.Var(&recipes, "recipe", "directory of a recipe (holds pkgops.yaml); repeat it for the recipes that package one source (default: .)")
	src := fs.String("src", "", "the fetched source tree that `src/` means in the recipes (default: <first recipe>/src if it exists)")
	failOn := fs.String("fail-on", "error", "exit 1 on findings of this severity or worse: error or warning")
	format := fs.String("o", "text", "output format: text or json")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(out, "pkgops lint:", err)
		return exitCode(2)
	}
	if fs.NArg() != 0 || (*failOn != "error" && *failOn != "warning") || (*format != "text" && *format != "json") {
		fmt.Fprintln(out, "usage: pkgops lint [--recipe DIR]... [--src DIR] [--fail-on error|warning] [-o text|json]")
		return exitCode(2)
	}
	if len(recipes) == 0 {
		recipes = multiFlag{"."}
	}
	var loaded []pkgops.LintRecipe
	var findings []pkgops.LintFinding
	for _, dir := range recipes {
		spec, err := pkgops.LoadSpec(filepath.Join(dir, "pkgops.yaml"))
		if err != nil {
			// a manifest that does not load is a finding of its own, not a usage error
			findings = append(findings, pkgops.LintFinding{Package: filepath.Base(filepath.Clean(dir)), Rule: "manifest", Severity: "error", Message: err.Error()})
			continue
		}
		loaded = append(loaded, pkgops.LintRecipe{Dir: dir, Spec: spec})
	}
	srcDir := *src
	if srcDir == "" && len(recipes) > 0 {
		if def := filepath.Join(recipes[0], "src"); dirExists(def) {
			srcDir = def
		}
	}
	findings = append(findings, pkgops.Lint(loaded, srcDir)...)
	errs, warns := 0, 0
	for _, f := range findings {
		if f.Severity == "error" {
			errs++
		} else {
			warns++
		}
	}
	if *format == "json" {
		b, _ := json.MarshalIndent(struct {
			Findings []pkgops.LintFinding `json:"findings"`
			Errors   int                  `json:"errors"`
			Warnings int                  `json:"warnings"`
		}{append([]pkgops.LintFinding{}, findings...), errs, warns}, "", "  ")
		fmt.Fprintln(out, string(b))
	} else {
		for _, f := range findings {
			who := f.Package
			if who == "" {
				who = "source"
			}
			fmt.Fprintf(out, "%s: %s: [%s] %s\n", who, f.Severity, f.Rule, f.Message)
		}
		if len(findings) == 0 {
			fmt.Fprintln(out, "lint: no findings")
		} else {
			fmt.Fprintf(out, "lint: %d error(s), %d warning(s)\n", errs, warns)
		}
	}
	if errs > 0 || (*failOn == "warning" && warns > 0) {
		return exitCode(1)
	}
	return nil
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
