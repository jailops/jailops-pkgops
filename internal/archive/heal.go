package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"jailops.net/pkgops/internal/index"
	"jailops.net/pkgops/internal/pkgerr"
	"jailops.net/pkgops/internal/pkgfile"
)

// ErrNotHealable means the report holds a problem that is not exactly "archived,
// not on Pages and not pruned": nothing is healed.
var ErrNotHealable = errors.New("the archive can only heal files that are archived but missing from the baseline")

// HealResult describes what Heal did.
type HealResult struct {
	// Healed lists the keys ("abi name-version") taken from the archive.
	Healed []string
	// Present counts files already in the baseline with the right bytes.
	Present int
	// Bytes downloaded.
	Bytes int64
}

// Heal brings the files that Verify found archived but missing from Pages
// ("the archive is ahead": a failed or skipped deploy, a stale cache, a
// cancelled run) back into the baseline in into, so that the next publish puts
// them on the site again. It is deliberately narrow:
//
//   - rep must hold exactly that one class of problem. A sha256 that differs, a
//     live file that is not archived or a pruned file that is not archived
//     (gone for good) is a different failure that a copy from the archive must
//     not hide: nothing is healed.
//   - every file is checked against the archive manifest (sha256 and size), and
//     against the index when ix knows the file.
//   - an existing file with other bytes is never overwritten; nothing is ever
//     deleted.
//   - all or nothing: the files are downloaded as .part files and verified
//     first; only then are they renamed into place and the index of into
//     written, so a failure leaves the baseline as it was.
//
// into must hold a baseline (an index); ix is the index the report was made
// from (it may be the same one).
func (a *Archive) Heal(ctx context.Context, into string, ix *index.Index, recs []Record, rep *Report) (*HealResult, error) {
	res := &HealResult{}
	if len(rep.ArchivedNotOnPages) == 0 {
		return res, nil
	}
	if len(rep.OnPagesNotArchived) > 0 || len(rep.Mismatched) > 0 || len(rep.PrunedNotArchived) > 0 {
		return nil, fmt.Errorf("%w: the report also lists %d live file(s) that are not archived, %d sha256 difference(s) and %d pruned file(s) that are gone for good; fix those first",
			ErrNotHealable, len(rep.OnPagesNotArchived), len(rep.Mismatched), len(rep.PrunedNotArchived))
	}
	base, err := index.Read(into)
	if err != nil {
		return nil, err
	}
	if base == nil {
		return nil, fmt.Errorf("%w: %s holds no %s: fetch the baseline first (pkgops fetch)", pkgerr.ErrInvalidSpec, into, index.FileName)
	}
	arch := map[string]Record{}
	for _, r := range recs {
		arch[r.Key()] = r
	}
	keys := append([]string(nil), rep.ArchivedNotOnPages...)
	sort.Strings(keys)

	type job struct {
		rec  Record
		ent  index.Entry
		dst  string
		part string
		done bool
	}
	var jobs []*job
	for _, k := range keys {
		r, ok := arch[k]
		if !ok {
			return nil, fmt.Errorf("%w: %s is in the report but not in the archive records", pkgerr.ErrNotFound, k)
		}
		if err := validRef(r.Name, r.Version); err != nil {
			return nil, err
		}
		if len(r.SHA256) != 64 || r.Size <= 0 || r.Asset == "" {
			return nil, fmt.Errorf("%w: the archive manifest of %s has no usable sha256, size and asset (sha256 %q, size %d, asset %q)", pkgerr.ErrInvalidPackage, k, r.SHA256, r.Size, r.Asset)
		}
		if e, ok := ix.Find(r.ABI, r.Name, r.Version); ok && (e.SHA256 != r.SHA256 || e.Size != r.Size) {
			return nil, fmt.Errorf("%w: %s: the index says sha256 %s, %d bytes; the archive manifest says %s, %d bytes", pkgerr.ErrInvalidPackage, k, e.SHA256, e.Size, r.SHA256, r.Size)
		}
		abi, err := pkgfile.ParseABI(r.ABI)
		if err != nil {
			return nil, err
		}
		file := abi.String() + "/latest/All/" + r.Name + "-" + r.Version + ".pkg"
		if err := index.SafeFile(file); err != nil {
			return nil, err
		}
		j := &job{rec: r, dst: filepath.Join(into, filepath.FromSlash(file))}
		j.part = j.dst + ".part"
		j.ent = index.Entry{ABI: r.ABI, Name: r.Name, Version: r.Version, File: file, SHA256: r.SHA256, Size: r.Size, Built: r.Built,
			MinOSVersion: r.MinOSVersion, MinRelease: pkgfile.OSVersionRelease(r.MinOSVersion), Comment: r.Comment}
		if have, ok := base.Find(r.ABI, r.Name, r.Version); ok && have.SHA256 != r.SHA256 {
			return nil, fmt.Errorf("%w: %s is in the baseline index with another sha256 than the archive", pkgerr.ErrInvalidPackage, k)
		}
		if st, err := os.Stat(j.dst); err == nil {
			if st.Mode().IsRegular() && fileHasSum(j.dst, r.SHA256, r.Size) {
				res.Present++
				j.done = true // right bytes already here: only the index entry is missing
			} else {
				return nil, fmt.Errorf("%w: %s exists in the baseline with other bytes than the archive: not overwriting it", pkgerr.ErrInvalidPackage, file)
			}
		}
		jobs = append(jobs, j)
	}

	var parts []string
	cleanup := func() {
		for _, p := range parts {
			os.Remove(p)
		}
	}
	for _, j := range jobs {
		if j.done {
			continue
		}
		if err := ctx.Err(); err != nil {
			cleanup()
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(j.dst), 0o755); err != nil {
			cleanup()
			return nil, err
		}
		parts = append(parts, j.part)
		n, err := a.download(ctx, j.rec, j.part)
		res.Bytes += n
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("healing %s: %w", j.rec.Key(), err)
		}
		if !fileHasSum(j.part, j.rec.SHA256, j.rec.Size) {
			cleanup()
			return nil, fmt.Errorf("%w: %s from the archive does not match its manifest (sha256 %s, %d bytes): the archive is damaged", pkgerr.ErrInvalidPackage, j.rec.Key(), j.rec.SHA256, j.rec.Size)
		}
	}
	for _, j := range jobs {
		if j.done {
			continue
		}
		if err := os.Rename(j.part, j.dst); err != nil {
			return nil, err
		}
	}
	for _, j := range jobs {
		if _, ok := base.Find(j.ent.ABI, j.ent.Name, j.ent.Version); !ok {
			base.Packages = append(base.Packages, j.ent)
		}
		res.Healed = append(res.Healed, j.rec.Key())
	}
	base.Sort()
	if err := base.Write(into); err != nil {
		return nil, err
	}
	return res, nil
}

func fileHasSum(file, sum string, size int64) bool {
	f, err := os.Open(file)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return err == nil && n == size && hex.EncodeToString(h.Sum(nil)) == sum
}
