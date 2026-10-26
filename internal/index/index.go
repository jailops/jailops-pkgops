// Package index is the published-file index of a PkgOps repository,
// pkgops-index.json in the repository root.
//
// The pkg catalog (packagesite.pkg, data.pkg) advertises only the newest
// version of each package, which is all pkg(8) needs; the index lists every
// published .pkg file of every ABI with its sha256, so a later build can
// rebuild the whole site from it (pkgops fetch), and the web UI can show the
// history. Files that were pruned are not listed under Packages; they are
// remembered under Pruned.
package index

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"jailops.net/pkgops/internal/fsutil"
	"jailops.net/pkgops/internal/pkgerr"
	"jailops.net/pkgops/internal/pkgver"
)

// FileName is the index's name in the repository root.
const FileName = "pkgops-index.json"

// SchemaVersion is the current value of Index.Schema. Readers refuse a newer
// schema instead of guessing.
const SchemaVersion = 1

// Entry is one published .pkg file.
type Entry struct {
	ABI     string `json:"abi"`
	Name    string `json:"name"`
	Version string `json:"version"`
	// File is the path of the .pkg relative to the repository root, with
	// forward slashes: FreeBSD:15:amd64/latest/All/hello-0.1.0.pkg.
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	// Built is when the file was first indexed (UTC).
	Built        time.Time `json:"built"`
	MinRelease   string    `json:"min_release,omitempty"`
	MinOSVersion int       `json:"min_osversion,omitempty"`
	Comment      string    `json:"comment,omitempty"`
}

// Key is "abi name-version", unique within an index.
func (e Entry) Key() string { return e.ABI + " " + e.Name + "-" + e.Version }

// Pruned remembers a file that was removed from the published site. The file
// itself is gone; a permanent archive (GitHub Releases) may still hold it.
type Pruned struct {
	ABI      string    `json:"abi"`
	Name     string    `json:"name"`
	Version  string    `json:"version"`
	SHA256   string    `json:"sha256,omitempty"`
	Size     int64     `json:"size,omitempty"`
	Built    time.Time `json:"built,omitzero"`
	Reason   string    `json:"reason,omitempty"`
	PrunedAt time.Time `json:"pruned_at"`
}

// Key is "abi name-version".
func (p Pruned) Key() string { return p.ABI + " " + p.Name + "-" + p.Version }

// Index is the document.
type Index struct {
	Schema    int       `json:"schema"`
	Generator string    `json:"generator,omitempty"`
	Generated time.Time `json:"generated"`
	Packages  []Entry   `json:"packages"`
	Pruned    []Pruned  `json:"pruned"`
}

// New returns an empty index of the current schema.
func New() *Index { return &Index{Schema: SchemaVersion, Packages: []Entry{}, Pruned: []Pruned{}} }

// Parse decodes and validates an index document.
func Parse(data []byte) (*Index, error) {
	var ix Index
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&ix); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", pkgerr.ErrInvalidPackage, FileName, err)
	}
	if ix.Schema < 1 {
		return nil, fmt.Errorf("%w: %s has no schema version", pkgerr.ErrInvalidPackage, FileName)
	}
	if ix.Schema > SchemaVersion {
		return nil, fmt.Errorf("%w: %s uses schema %d, this pkgops understands up to %d; upgrade pkgops", pkgerr.ErrInvalidPackage, FileName, ix.Schema, SchemaVersion)
	}
	seen := map[string]bool{}
	for _, e := range ix.Packages {
		if e.ABI == "" || e.Name == "" || e.Version == "" || e.File == "" || len(e.SHA256) != 64 {
			return nil, fmt.Errorf("%w: %s: incomplete entry %q", pkgerr.ErrInvalidPackage, FileName, e.Key())
		}
		if err := SafeFile(e.File); err != nil {
			return nil, fmt.Errorf("%w: %s: %q: %v", pkgerr.ErrInvalidPackage, FileName, e.File, err)
		}
		if seen[e.Key()] {
			return nil, fmt.Errorf("%w: %s lists %s twice", pkgerr.ErrInvalidPackage, FileName, e.Key())
		}
		seen[e.Key()] = true
	}
	if ix.Packages == nil {
		ix.Packages = []Entry{}
	}
	if ix.Pruned == nil {
		ix.Pruned = []Pruned{}
	}
	return &ix, nil
}

// SafeFile checks that an index file path stays inside the repository root.
func SafeFile(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || strings.Contains(p, "\x00") {
		return errors.New("not a relative path")
	}
	for _, el := range strings.Split(p, "/") {
		if el == ".." || el == "" || el == "." {
			return errors.New("not a clean relative path")
		}
	}
	return nil
}

// Read loads the index from a repository root. A missing file returns (nil, nil).
func Read(root string) (*Index, error) {
	data, err := os.ReadFile(filepath.Join(root, FileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return Parse(data)
}

// Sort orders packages by ABI, name and version (oldest first) and pruned
// entries by key, so the document is stable.
func (ix *Index) Sort() {
	sort.SliceStable(ix.Packages, func(i, j int) bool {
		a, b := ix.Packages[i], ix.Packages[j]
		if a.ABI != b.ABI {
			return a.ABI < b.ABI
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return pkgver.Compare(a.Version, b.Version) < 0
	})
	sort.SliceStable(ix.Pruned, func(i, j int) bool {
		a, b := ix.Pruned[i], ix.Pruned[j]
		if a.ABI != b.ABI {
			return a.ABI < b.ABI
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if c := pkgver.Compare(a.Version, b.Version); c != 0 {
			return c < 0
		}
		return a.PrunedAt.Before(b.PrunedAt)
	})
}

// Marshal returns the sorted, indented document.
func (ix *Index) Marshal() ([]byte, error) {
	ix.Sort()
	ix.Schema = SchemaVersion
	b, err := json.MarshalIndent(ix, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Write stores the index atomically in root.
func (ix *Index) Write(root string) error {
	b, err := ix.Marshal()
	if err != nil {
		return err
	}
	return fsutil.AtomicWrite(filepath.Join(root, FileName), 0o644, func(w io.Writer) error {
		_, err := w.Write(b)
		return err
	})
}

// Find returns the live entry for abi name-version.
func (ix *Index) Find(abi, name, version string) (Entry, bool) {
	for _, e := range ix.Packages {
		if e.ABI == abi && e.Name == name && e.Version == version {
			return e, true
		}
	}
	return Entry{}, false
}

// FindPruned reports whether abi name-version was pruned.
func (ix *Index) FindPruned(abi, name, version string) (Pruned, bool) {
	for _, p := range ix.Pruned {
		if p.ABI == abi && p.Name == name && p.Version == version {
			return p, true
		}
	}
	return Pruned{}, false
}

// Newest returns the newest live version of name for abi.
func (ix *Index) Newest(abi, name string) (Entry, bool) {
	var best Entry
	found := false
	for _, e := range ix.Packages {
		if e.ABI == abi && e.Name == name && (!found || pkgver.Compare(e.Version, best.Version) > 0) {
			best, found = e, true
		}
	}
	return best, found
}

// ABIs lists the ABIs that have live packages, sorted.
func (ix *Index) ABIs() []string {
	set := map[string]bool{}
	for _, e := range ix.Packages {
		set[e.ABI] = true
	}
	var out []string
	for a := range set {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// TotalSize sums the live package sizes, optionally for one ABI ("" = all).
func (ix *Index) TotalSize(abi string) int64 {
	var n int64
	for _, e := range ix.Packages {
		if abi == "" || e.ABI == abi {
			n += e.Size
		}
	}
	return n
}
