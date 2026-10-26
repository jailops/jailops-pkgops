// Package prune is the prune manifest: a committed list of published package
// files that the next build must leave out of the site. Nothing here deletes
// anything. `pkgops prune --plan` writes the list; `pkgops repo` and
// `pkgops fetch` honour it; a person reviews and commits it.
package prune

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"jailops.net/pkgops/internal/fsutil"
	"jailops.net/pkgops/internal/index"
	"jailops.net/pkgops/internal/pkgerr"
	"jailops.net/pkgops/internal/pkgfile"
	"jailops.net/pkgops/internal/pkgver"
)

// FileName is the conventional name of the manifest (in the packages repo).
const FileName = "prune.yaml"

// SchemaVersion is the manifest schema this pkgops understands.
const SchemaVersion = 1

// ErrNewest is returned when a prune would remove the newest version of a
// package and the entry is not forced.
var ErrNewest = errors.New("refusing to prune the newest version")

// Entry names one file to leave out of the next published site.
type Entry struct {
	ABI     string `yaml:"abi" json:"abi"`
	Name    string `yaml:"name" json:"name"`
	Version string `yaml:"version" json:"version"`
	Reason  string `yaml:"reason,omitempty" json:"reason,omitempty"`
	// Force allows pruning the newest version of the package.
	Force bool `yaml:"force,omitempty" json:"force,omitempty"`
	// RequestedBy and Requested record who asked and when (informational;
	// the commit history is the audit trail).
	RequestedBy string `yaml:"requested_by,omitempty" json:"requested_by,omitempty"`
	Requested   string `yaml:"requested,omitempty" json:"requested,omitempty"`
}

// Key is "abi name-version", the same as index.Entry.Key.
func (e Entry) Key() string { return e.ABI + " " + e.Name + "-" + e.Version }

// Manifest is the document.
type Manifest struct {
	Schema  int     `yaml:"schema" json:"schema"`
	Entries []Entry `yaml:"entries" json:"entries"`
}

// New returns an empty manifest.
func New() *Manifest { return &Manifest{Schema: SchemaVersion} }

// Parse decodes and validates manifest YAML. An empty document is an empty manifest.
func Parse(data []byte) (*Manifest, error) {
	m := New()
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(m); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: %s: %v", pkgerr.ErrInvalidSpec, FileName, err)
	}
	if m.Schema == 0 {
		m.Schema = SchemaVersion
	}
	if m.Schema != SchemaVersion {
		return nil, fmt.Errorf("%w: %s: schema %d is not supported (want %d)", pkgerr.ErrInvalidSpec, FileName, m.Schema, SchemaVersion)
	}
	seen := map[string]bool{}
	for i, e := range m.Entries {
		if err := e.validate(); err != nil {
			return nil, fmt.Errorf("%w: %s: entries[%d]: %v", pkgerr.ErrInvalidSpec, FileName, i, err)
		}
		if seen[e.Key()] {
			return nil, fmt.Errorf("%w: %s: %s listed twice", pkgerr.ErrInvalidSpec, FileName, e.Key())
		}
		seen[e.Key()] = true
	}
	return m, nil
}

func (e Entry) validate() error {
	a, err := pkgfile.ParseABI(e.ABI)
	if err != nil || a.String() != e.ABI {
		return fmt.Errorf("abi %q is not a canonical ABI", e.ABI)
	}
	if !pkgfile.ValidName(e.Name) {
		return fmt.Errorf("invalid package name %q", e.Name)
	}
	if e.Version == "" || strings.ContainsAny(e.Version, "/\\ \t\n") {
		return fmt.Errorf("invalid version %q", e.Version)
	}
	if strings.ContainsAny(e.Reason, "\n\r") {
		return errors.New("reason must be one line")
	}
	return nil
}

// Validate checks one entry's syntax (used for requests before they are merged).
func (e Entry) Validate() error {
	if err := e.validate(); err != nil {
		return fmt.Errorf("%w: %v", pkgerr.ErrInvalidSpec, err)
	}
	return nil
}

// Load reads a manifest file; a missing file is an empty manifest.
func Load(file string) (*Manifest, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return New(), nil
		}
		return nil, err
	}
	m, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return m, nil
}

const header = "# Prune manifest: published package files the next build leaves out of the site.\n" +
	"# Written by `pkgops prune --plan` or the admin UI; review it, commit it, the publish workflow applies it.\n" +
	"# Nothing is deleted from the archive (GitHub Releases) by this file.\n"

// Marshal renders the manifest sorted, with a header comment.
func (m *Manifest) Marshal() ([]byte, error) {
	m.Schema = SchemaVersion
	sort.SliceStable(m.Entries, func(i, j int) bool {
		a, b := m.Entries[i], m.Entries[j]
		if a.ABI != b.ABI {
			return a.ABI < b.ABI
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return pkgver.Compare(a.Version, b.Version) < 0
	})
	if m.Entries == nil {
		m.Entries = []Entry{}
	}
	b, err := yaml.Marshal(m)
	if err != nil {
		return nil, err
	}
	return append([]byte(header), b...), nil
}

// Save writes the manifest atomically.
func (m *Manifest) Save(file string) error {
	b, err := m.Marshal()
	if err != nil {
		return err
	}
	return fsutil.AtomicWrite(file, 0o644, func(w io.Writer) error { _, err := w.Write(b); return err })
}

// Has reports whether abi name-version is listed.
func (m *Manifest) Has(abi, name, version string) (Entry, bool) {
	if m == nil {
		return Entry{}, false
	}
	for _, e := range m.Entries {
		if e.ABI == abi && e.Name == name && e.Version == version {
			return e, true
		}
	}
	return Entry{}, false
}

// Merge adds entries that are not listed yet and returns the ones it added.
func (m *Manifest) Merge(add []Entry) []Entry {
	var added []Entry
	for _, e := range add {
		if _, ok := m.Has(e.ABI, e.Name, e.Version); ok {
			continue
		}
		m.Entries = append(m.Entries, e)
		added = append(added, e)
	}
	return added
}

// Remove drops entries (un-requests a prune that has not been applied).
func (m *Manifest) Remove(abi, name, version string) bool {
	for i, e := range m.Entries {
		if e.ABI == abi && e.Name == name && e.Version == version {
			m.Entries = append(m.Entries[:i], m.Entries[i+1:]...)
			return true
		}
	}
	return false
}

// Check validates the manifest against an index: an entry that names the
// newest live version of its package and is not forced is an error. Entries
// that name files that are already gone (or never existed) are fine: the
// manifest is applied idempotently. A nil index checks nothing.
func (m *Manifest) Check(ix *index.Index) error {
	if m == nil || ix == nil {
		return nil
	}
	var bad []string
	for _, e := range m.Entries {
		if e.Force {
			continue
		}
		if _, live := ix.Find(e.ABI, e.Name, e.Version); !live {
			continue
		}
		if newest, ok := ix.Newest(e.ABI, e.Name); ok && newest.Version == e.Version {
			bad = append(bad, e.Key())
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("%w: %s (removing the newest version silently downgrades what pkg clients are offered; add force: true to the entry to allow it)", ErrNewest, strings.Join(bad, ", "))
	}
	return nil
}

// Selector chooses what Plan proposes.
type Selector struct {
	ABI  string // limit to one ABI ("" = all)
	Name string // limit to one package ("" = all)
	// Version selects one version (needs Name).
	Version string
	// Keep > 0 selects, per ABI and package, every version except the newest Keep.
	Keep int
	// OlderThan > 0 selects versions first indexed longer ago than this.
	OlderThan time.Duration
	Now       time.Time
	Reason    string
	// Force lets the plan include the newest version of a package.
	Force       bool
	RequestedBy string
}

// Plan computes the entries a selector would add to the manifest. It does not
// touch any file. Versions already listed in existing are not repeated.
func Plan(ix *index.Index, existing *Manifest, s Selector) ([]Entry, error) {
	if ix == nil {
		return nil, fmt.Errorf("%w: no index to plan against", pkgerr.ErrNotFound)
	}
	if s.Version != "" && s.Name == "" {
		return nil, fmt.Errorf("%w: a version needs a package name", pkgerr.ErrInvalidSpec)
	}
	if s.Keep < 0 || s.OlderThan < 0 {
		return nil, fmt.Errorf("%w: keep and older-than must not be negative", pkgerr.ErrInvalidSpec)
	}
	if s.Keep == 0 && s.OlderThan == 0 && s.Version == "" {
		return nil, fmt.Errorf("%w: choose what to prune: --keep N, --older-than D or --name N --version V", pkgerr.ErrInvalidSpec)
	}
	now := s.Now
	if now.IsZero() {
		now = time.Now()
	}
	type key struct{ abi, name string }
	groups := map[key][]index.Entry{}
	for _, e := range ix.Packages {
		if s.ABI != "" && e.ABI != s.ABI {
			continue
		}
		if s.Name != "" && e.Name != s.Name {
			continue
		}
		groups[key{e.ABI, e.Name}] = append(groups[key{e.ABI, e.Name}], e)
	}
	var out []Entry
	var refused []string
	for k, vs := range groups {
		sort.Slice(vs, func(i, j int) bool { return pkgver.Compare(vs[i].Version, vs[j].Version) < 0 })
		newest := vs[len(vs)-1].Version
		for i, e := range vs {
			pick := false
			if s.Version != "" && e.Version == s.Version {
				pick = true
			}
			if s.Keep > 0 && i < len(vs)-s.Keep {
				pick = true
			}
			if s.OlderThan > 0 && s.Version == "" && s.Keep == 0 && now.Sub(e.Built) > s.OlderThan {
				pick = true
			}
			if !pick {
				continue
			}
			if e.Version == newest && !s.Force {
				if s.Version != "" { // explicitly asked for: say so
					refused = append(refused, e.Key())
				}
				continue
			}
			if _, ok := existing.Has(k.abi, k.name, e.Version); ok {
				continue
			}
			reason := s.Reason
			out = append(out, Entry{ABI: e.ABI, Name: e.Name, Version: e.Version, Reason: reason, Force: e.Version == newest && s.Force,
				RequestedBy: s.RequestedBy, Requested: now.UTC().Format(time.RFC3339)})
		}
	}
	if len(refused) > 0 {
		sort.Strings(refused)
		return nil, fmt.Errorf("%w: %s (use force to allow it)", ErrNewest, strings.Join(refused, ", "))
	}
	if s.Version != "" && len(out) == 0 {
		if _, live := ix.Find(s.ABI, s.Name, s.Version); !live && s.ABI != "" {
			return nil, fmt.Errorf("%w: %s %s-%s is not in the index", pkgerr.ErrNotFound, s.ABI, s.Name, s.Version)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ABI != out[j].ABI {
			return out[i].ABI < out[j].ABI
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return pkgver.Compare(out[i].Version, out[j].Version) < 0
	})
	return out, nil
}
