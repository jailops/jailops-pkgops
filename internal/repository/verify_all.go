package repository

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"jailops.net/pkgops/internal/index"
	"jailops.net/pkgops/internal/pkgfile"
)

// Report is what VerifyAll found: one entry per ABI and channel, the findings
// of the index, and a flat list of every problem.
type Report struct {
	OK       bool        `json:"ok"`
	Root     string      `json:"root"`
	Verified bool        `json:"verified"` // catalog signatures were checked against a key
	Repos    []RepoEntry `json:"repos"`
	Index    IndexEntry  `json:"index"`
	Problems []Finding   `json:"problems"`
}

// RepoEntry is one ABI and channel directory.
type RepoEntry struct {
	ABI       string `json:"abi"`
	Channel   string `json:"channel"`
	Packages  int    `json:"catalog_packages"` // advertised by the catalog
	Files     int    `json:"files"`            // .pkg files in All/
	Signature string `json:"signature,omitempty"`
}

// IndexEntry summarises pkgops-index.json.
type IndexEntry struct {
	Present  bool `json:"present"`
	Packages int  `json:"packages"`
	Pruned   int  `json:"pruned"`
}

// Finding is one finding: where, and what is wrong.
type Finding struct {
	ABI     string `json:"abi,omitempty"`
	Channel string `json:"channel,omitempty"`
	File    string `json:"file,omitempty"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// Problem kinds.
const (
	ProblemCatalog        = "catalog"         // a catalog does not parse, verify or match its packages
	ProblemMissingFile    = "missing-file"    // a catalog or the index names a file that is not there
	ProblemChecksum       = "checksum"        // a file differs from what a catalog or the index says
	ProblemNotInIndex     = "not-in-index"    // a file or catalog entry the index does not know
	ProblemNotInCatalog   = "not-in-catalog"  // the newest version of a package the catalog does not advertise
	ProblemPrunedPresent  = "pruned-present"  // a file the index records as pruned is still there
	ProblemUnreadableFile = "unreadable-file" // a .pkg file that cannot be read
	ProblemIndex          = "index"           // the index itself is missing or unreadable
	ProblemNoRepositories = "no-repositories" // the root holds no ABI directory
)

// VerifyAll checks a whole repository root, every ABI and channel at once,
// without changing anything: each catalog parses and (with pubPEM) verifies,
// every package a catalog advertises is there with the advertised size and
// checksum, every file the index lists is there with its bytes, the index and
// the catalogs agree, and nothing the index records as pruned is still on the
// site. The returned error is non-nil only when the check itself could not run;
// findings are in the Report (OK is false then).
func VerifyAll(root string, pubPEM []byte) (*Report, error) {
	rep := &Report{Root: root, Verified: pubPEM != nil, Repos: []RepoEntry{}, Problems: []Finding{}}
	if pubPEM != nil {
		if _, err := ParseRSAPublicKey(pubPEM); err != nil {
			return nil, err
		}
	}
	add := func(abi, channel, file, kind string, f string, a ...any) {
		rep.Problems = append(rep.Problems, Finding{ABI: abi, Channel: channel, File: file, Kind: kind, Message: fmt.Sprintf(f, a...)})
	}
	ix, err := index.Read(root)
	switch {
	case err != nil:
		add("", "", index.FileName, ProblemIndex, "%v", err)
		ix = nil
	case ix == nil:
		add("", "", index.FileName, ProblemIndex, "the index is missing")
	default:
		rep.Index = IndexEntry{Present: true, Packages: len(ix.Packages), Pruned: len(ix.Pruned)}
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	type repoDir struct{ abi, channel, dir string }
	var dirs []repoDir
	for _, e := range ents {
		if !e.IsDir() || !abiCanonical(e.Name()) {
			continue
		}
		chans, err := os.ReadDir(filepath.Join(root, e.Name()))
		if err != nil {
			return nil, err
		}
		for _, c := range chans {
			if !c.IsDir() || strings.HasPrefix(c.Name(), ".") {
				continue
			}
			d := filepath.Join(root, e.Name(), c.Name())
			if st, err := os.Stat(filepath.Join(d, "All")); err == nil && st.IsDir() {
				dirs = append(dirs, repoDir{e.Name(), c.Name(), d})
			}
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].dir < dirs[j].dir })
	if len(dirs) == 0 {
		add("", "", "", ProblemNoRepositories, "%s holds no ABI directory with an All/ directory", root)
	}
	// what each catalog advertises, by ABI: "name-version" -> sum
	indexed := map[string]index.Entry{}
	if ix != nil {
		for _, e := range ix.Packages {
			indexed[e.ABI+" "+e.Name+"-"+e.Version] = e
		}
	}
	seenInIndex := map[string]bool{}
	for _, d := range dirs {
		entry := RepoEntry{ABI: d.abi, Channel: d.channel}
		files, bad, _ := (&Repository{Root: root, Channel: d.channel}).scan(d.abi)
		entry.Files = len(files)
		for _, b := range bad {
			add(d.abi, d.channel, "All/"+b.File, ProblemUnreadableFile, "%v", b.Err)
		}
		res, err := VerifyRepository(d.dir, pubPEM)
		if err != nil {
			kind := ProblemCatalog
			switch {
			case strings.Contains(err.Error(), "does not match"):
				kind = ProblemChecksum
			case os.IsNotExist(unwrapAll(err)) || strings.Contains(err.Error(), "no such file"):
				kind = ProblemMissingFile
			}
			add(d.abi, d.channel, "", kind, "%v", err)
		} else {
			entry.Packages, entry.Signature = res.Packages, res.Signature
		}
		// the catalog against the index: the newest version of every package is advertised, and it is a file the index lists
		newest := map[string]*Info{}
		for _, i := range files {
			if cur, ok := newest[i.Name]; !ok || CompareVersions(i.Version, cur.Version) > 0 {
				newest[i.Name] = i
			}
		}
		cat := catalogNames(d.dir)
		for _, n := range sortedKeys(newest) {
			i := newest[n]
			if cat != nil && !cat[i.Name+"-"+i.Version] {
				add(d.abi, d.channel, "All/"+i.Filename(), ProblemNotInCatalog, "%s is the newest %s here and the catalog does not advertise it", i.Filename(), i.Name)
			}
		}
		for _, i := range files {
			key := d.abi + " " + i.Name + "-" + i.Version
			e, ok := indexed[key]
			if !ok {
				if ix != nil {
					add(d.abi, d.channel, "All/"+i.Filename(), ProblemNotInIndex, "%s is on the site and the index does not list it", i.Filename())
				}
				continue
			}
			seenInIndex[key] = true
			if e.SHA256 != i.SHA256 || e.Size != i.Size {
				add(d.abi, d.channel, "All/"+i.Filename(), ProblemChecksum, "%s differs from the index (index sha256 %s, file %s): published files are immutable", i.Filename(), short12(e.SHA256), short12(i.SHA256))
			}
			if p, ok := ix.FindPruned(d.abi, i.Name, i.Version); ok {
				add(d.abi, d.channel, "All/"+i.Filename(), ProblemPrunedPresent, "%s was pruned on %s and is still on the site", i.Filename(), p.PrunedAt.Format("2006-01-02"))
			}
		}
		rep.Repos = append(rep.Repos, entry)
	}
	if ix != nil {
		for _, e := range ix.Packages {
			key := e.ABI + " " + e.Name + "-" + e.Version
			if seenInIndex[key] {
				continue
			}
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(e.File))); err != nil {
				add(e.ABI, "", e.File, ProblemMissingFile, "the index lists %s and the file is not there", e.File)
			}
		}
	}
	sort.SliceStable(rep.Problems, func(i, j int) bool {
		a, b := rep.Problems[i], rep.Problems[j]
		if a.ABI != b.ABI {
			return a.ABI < b.ABI
		}
		if a.Channel != b.Channel {
			return a.Channel < b.Channel
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Kind < b.Kind
	})
	rep.OK = len(rep.Problems) == 0
	return rep, nil
}

func abiCanonical(s string) bool {
	a, err := pkgfile.ParseABI(s)
	return err == nil && a.String() == s
}

func short12(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func unwrapAll(err error) error {
	for {
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return err
		}
		next := u.Unwrap()
		if next == nil {
			return err
		}
		err = next
	}
}

// catalogNames returns the name-version pairs packagesite.pkg advertises in
// one repository directory, or nil when it cannot be read (VerifyRepository
// reports that).
func catalogNames(dir string) map[string]bool {
	d, err1 := os.Open(filepath.Join(dir, "data.pkg"))
	s, err2 := os.Open(filepath.Join(dir, "packagesite.pkg"))
	if err1 != nil || err2 != nil {
		if err1 == nil {
			d.Close()
		}
		if err2 == nil {
			s.Close()
		}
		return nil
	}
	defer d.Close()
	defer s.Close()
	cat, err := ReadCatalog(d, s, nil)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, p := range cat.Packages {
		out[p.Name+"-"+p.Version] = true
	}
	for _, p := range cat.Site {
		out[p.Name+"-"+p.Version] = true
	}
	return out
}
