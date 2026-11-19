package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"jailops.net/pkgops/internal/archive"
	"jailops.net/pkgops/internal/index"
	"jailops.net/pkgops/internal/prune"
)

// cmdArchive handles `archive push` and `archive prune`.
func cmdArchive(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: pkgops archive push|prune ...")
	}
	switch args[0] {
	case "push":
		return cmdArchivePush(ctx, args[1:])
	case "prune":
		return cmdArchivePrune(ctx, args[1:])
	}
	return fmt.Errorf("unknown archive command %q (push or prune)", args[0])
}

func cmdArchivePush(ctx context.Context, args []string) error {
	fs := newFlags("archive push")
	repo := fs.String("repo", "", "GitHub OWNER/REPO that holds the archive releases")
	dir := fs.String("from", "repo", "repository root whose index and package files are archived")
	gh := addGHFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := gh.repoClient(*repo)
	if err != nil {
		return err
	}
	ix, err := index.Read(*dir)
	if err != nil {
		return err
	}
	if ix == nil {
		return fmt.Errorf("%s has no %s: build the repository first (pkgops repo)", *dir, index.FileName)
	}
	res, err := (&archive.Archive{GH: c, Log: os.Stderr}).Push(ctx, *dir, ix)
	if err != nil {
		return err
	}
	fmt.Printf("archive: %d uploaded, %d already archived\n", res.Uploaded, res.Skipped)
	return nil
}

// cmdArchivePrune deletes from the permanent archive. It is not a Pages prune
// and never part of one: its own command, typed confirmation and audit entry.
func cmdArchivePrune(ctx context.Context, args []string) error {
	fs := newFlags("archive prune")
	repo := fs.String("repo", "", "GitHub OWNER/REPO that holds the archive releases")
	abi := fs.String("abi", "", "ABI of the archived file")
	name := fs.String("name", "", "package name")
	version := fs.String("version", "", "package version")
	reason := fs.String("reason", "", "why (recorded in the archive audit log)")
	by := fs.String("by", os.Getenv("USER"), "who deletes (recorded in the archive audit log)")
	confirm := fs.String("confirm", "", `type exactly "delete from archive" to confirm`)
	gh := addGHFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *abi == "" || *name == "" || *version == "" || strings.TrimSpace(*reason) == "" {
		return errors.New("--abi, --name, --version and --reason are required")
	}
	if *confirm != "delete from archive" {
		return errors.New(`this PERMANENTLY deletes the archived file (the Pages site is not changed). Repeat with --confirm "delete from archive"`)
	}
	c, err := gh.repoClient(*repo)
	if err != nil {
		return err
	}
	a := &archive.Archive{GH: c, Log: os.Stderr}
	if err := a.Delete(ctx, []archive.Ref{{ABI: *abi, Name: *name, Version: *version}}, *by, *reason); err != nil {
		return err
	}
	fmt.Printf("deleted %s %s-%s from the archive; the audit log release %q has the entry\n", *abi, *name, *version, archive.AuditTag)
	return nil
}

// cmdVerifyArchive compares the Pages site with the archive.
func cmdVerifyArchive(ctx context.Context, args []string) error {
	fs := newFlags("verify-archive")
	repo := fs.String("repo", "", "GitHub OWNER/REPO that holds the archive releases")
	from := fs.String("from", "", "published site URL, or a repository root directory, with the index to compare")
	pruneFile := fs.String("prune", "", "prune manifest (entries listed there count as pruned)")
	healInto := fs.String("heal-into", "", "baseline directory: when the ONLY problem is files archived but missing from the site, copy them from the archive into it (checked against the archive manifest) and exit 0; any other problem still fails and nothing is healed")
	noBust := fs.Bool("no-cache-bust", false, "do not add ?cb=TIME to the request for the index of a URL")
	gh := addGHFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *from == "" {
		return errors.New("--from is required")
	}
	c, err := gh.repoClient(*repo)
	if err != nil {
		return err
	}
	ix, err := loadIndex(ctx, *from, *noBust)
	if err != nil {
		return err
	}
	listed := map[string]bool{}
	if *pruneFile != "" {
		m, err := prune.Load(*pruneFile)
		if err != nil {
			return err
		}
		for _, e := range m.Entries {
			listed[e.Key()] = true
		}
	}
	arch := &archive.Archive{GH: c}
	recs, err := arch.List(ctx)
	if err != nil {
		return err
	}
	rep := archive.Verify(ix, recs, listed)
	show := func(title string, l []string) {
		fmt.Printf("%s: %d\n", title, len(l))
		for _, k := range l {
			fmt.Println("  ", k)
		}
	}
	show("on Pages, not archived (PROBLEM)", rep.OnPagesNotArchived)
	show("archived, not on Pages and not pruned (PROBLEM)", rep.ArchivedNotOnPages)
	show("sha256 differs between Pages and archive (PROBLEM)", rep.Mismatched)
	show("pruned from Pages, still archived (normal)", rep.PrunedStillArchived)
	show("pruned from Pages and not archived (gone for good)", rep.PrunedNotArchived)
	if !rep.OK() {
		if *healInto == "" {
			return errors.New("the Pages index and the archive disagree")
		}
		res, err := arch.Heal(ctx, *healInto, ix, recs, rep)
		if err != nil {
			if errors.Is(err, archive.ErrNotHealable) {
				return fmt.Errorf("the Pages index and the archive disagree; nothing was healed: %w", err)
			}
			return fmt.Errorf("the Pages index and the archive disagree; healing failed, the baseline in %s is unchanged: %w", *healInto, err)
		}
		for _, k := range res.Healed {
			fmt.Println("healed:", k)
		}
		fmt.Printf("notice: the archive was ahead of the site: %d file(s) restored from the archive into %s (%d already there, %d bytes downloaded); the next publish puts them back on the site\n",
			len(res.Healed), *healInto, res.Present, res.Bytes)
		return nil
	}
	fmt.Println("ok: every file on Pages is archived with the same sha256")
	return nil
}
