package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"jailops.net/pkgops/internal/archive"
	"jailops.net/pkgops/internal/fetch"
	"jailops.net/pkgops/internal/index"
	"jailops.net/pkgops/internal/prune"
)

func splitABIs(s string) []string {
	var out []string
	for _, a := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		out = append(out, a)
	}
	return out
}

// cmdFetch rebuilds a baseline from the published site or the release archive.
func cmdFetch(ctx context.Context, args []string) error {
	fs := newFlags("fetch")
	from := fs.String("from", "", "published site URL (https://pkg.example.org), or github-releases:OWNER/REPO for the archive")
	into := fs.String("into", "repo", "directory to write the baseline to (a repository root)")
	abis := fs.String("abi", "", "limit to these ABIs (comma separated)")
	pruneFile := fs.String("prune", "", "prune manifest: listed files are not fetched")
	source := fs.String("source", "auto", "pages, releases or auto (pages, then the archive when the site has no baseline)")
	gh := addGHFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *from == "" {
		return errors.New("--from is required")
	}
	m := prune.New()
	if *pruneFile != "" {
		var err error
		if m, err = prune.Load(*pruneFile); err != nil {
			return err
		}
	}
	o := fetch.Options{Into: *into, ABIs: splitABIs(*abis), Prune: m, Log: os.Stderr}
	archiveRepo, isArchive := strings.CutPrefix(*from, "github-releases:")
	siteURL := *from
	if isArchive {
		*source = "releases"
	}
	switch *source {
	case "pages", "releases", "auto":
	default:
		return errors.New("--source must be pages, releases or auto")
	}
	var res *fetch.Result
	var err error
	if *source != "releases" {
		o.From = siteURL
		res, err = fetch.Pages(ctx, o)
		if err != nil && *source == "auto" && errors.Is(err, fetch.ErrNoBaseline) {
			fmt.Fprintln(os.Stderr, "the site has no baseline; trying the archive:", err)
			err = nil
			res = nil
			if repo := os.Getenv("PKGOPS_ARCHIVE_REPO"); repo != "" {
				archiveRepo, isArchive = repo, true
			}
			if !isArchive {
				return errors.New("no baseline on the site and no archive given: use --from github-releases:OWNER/REPO (or $PKGOPS_ARCHIVE_REPO)")
			}
		}
		if err != nil {
			return err
		}
	}
	if res == nil {
		if !isArchive {
			return errors.New("--source releases needs --from github-releases:OWNER/REPO")
		}
		c, err := gh.repoClient(archiveRepo)
		if err != nil {
			return err
		}
		res, err = (&archive.Archive{GH: c, Log: os.Stderr}).Fetch(ctx, o)
		if err != nil {
			return err
		}
	}
	fmt.Printf("baseline from %s: %d downloaded, %d already present, %d pruned (skipped), %d bytes -> %s\n",
		res.Source, res.Downloaded, res.Reused, res.Pruned, res.Bytes, *into)
	return nil
}

var reDays = regexp.MustCompile(`^([0-9]+)d$`)

func parseAge(s string) (time.Duration, error) {
	if m := reDays.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

func loadIndex(ctx context.Context, from string) (*index.Index, error) {
	if strings.HasPrefix(from, "http://") || strings.HasPrefix(from, "https://") {
		ix, _, err := fetch.FetchIndex(ctx, &fetch.Options{From: from})
		return ix, err
	}
	ix, err := index.Read(from)
	if err == nil && ix == nil {
		err = fmt.Errorf("%s has no %s", from, index.FileName)
	}
	return ix, err
}

// cmdPrune only plans. It writes the prune manifest and never deletes a file.
func cmdPrune(ctx context.Context, args []string) error {
	fs := newFlags("prune")
	plan := fs.Bool("plan", false, "required: pkgops never deletes; this writes the plan into the prune manifest")
	from := fs.String("from", "repo", "the published site URL, or a repository root directory, to plan against")
	manifest := fs.String("manifest", prune.FileName, "prune manifest to create or update")
	abi := fs.String("abi", "", "limit to one ABI")
	name := fs.String("name", "", "limit to one package")
	version := fs.String("version", "", "prune this version (needs --name)")
	keep := fs.Int("keep", 0, "prune everything but the newest N versions of each package")
	older := fs.String("older-than", "", "prune versions first published longer ago than this (90d, 36h)")
	reason := fs.String("reason", "", "one-line reason recorded with each entry")
	force := fs.Bool("force", false, "allow pruning the newest version of a package")
	dry := fs.Bool("n", false, "print the plan, do not write the manifest")
	who := fs.String("by", os.Getenv("USER"), "who requests it (recorded in the manifest)")
	if err := fs.Parse(reorder(args)); err != nil {
		return err
	}
	if !*plan {
		return errors.New("pkgops never deletes published files by itself: use `pkgops prune --plan ...` to write the plan, review and commit prune.yaml, and let the publish workflow apply it")
	}
	ix, err := loadIndex(ctx, *from)
	if err != nil {
		return err
	}
	m, err := prune.Load(*manifest)
	if err != nil {
		return err
	}
	sel := prune.Selector{ABI: *abi, Name: *name, Version: *version, Keep: *keep, Reason: *reason, Force: *force, RequestedBy: *who}
	if *older != "" {
		if sel.OlderThan, err = parseAge(*older); err != nil {
			return fmt.Errorf("--older-than: %w", err)
		}
	}
	add, err := prune.Plan(ix, m, sel)
	if err != nil {
		return err
	}
	if len(add) == 0 {
		fmt.Println("nothing to prune (everything selected is already in the manifest, or nothing matches)")
		return nil
	}
	var freed int64
	for _, e := range add {
		if x, ok := ix.Find(e.ABI, e.Name, e.Version); ok {
			freed += x.Size
		}
		fmt.Printf("prune  %s %s-%s\n", e.ABI, e.Name, e.Version)
	}
	m.Merge(add)
	if err := m.Check(ix); err != nil {
		return err
	}
	if *dry {
		fmt.Printf("%d file(s), %d bytes would leave the next deploy; %s not written\n", len(add), freed, *manifest)
		return nil
	}
	if err := m.Save(*manifest); err != nil {
		return err
	}
	fmt.Printf("%d file(s), %d bytes will leave the next deploy once %s is committed and the publish workflow has run.\nNothing was deleted, and the archive is not touched.\n", len(add), freed, filepath.Base(*manifest))
	return nil
}
