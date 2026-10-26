// Package policy reads the repository policy file, pkgops-repo.yaml in the
// repository root: which ABIs the repository serves, the minimum FreeBSD
// release, and retention hints.
//
// FreeBSD's ABI string (FreeBSD:15:amd64) names the major version only, so the
// minimum release is a separate number. pkg(8) enforces a package's
// FreeBSD_version annotation on the client; this policy keeps packages that
// do not carry a high enough one out of the repository in the first place.
package policy

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"jailops.net/pkgops/internal/pkgerr"
	"jailops.net/pkgops/internal/pkgfile"
)

// FileName is the policy file's name in the repository root.
const FileName = "pkgops-repo.yaml"

// SchemaVersion is the policy schema this pkgops understands.
const SchemaVersion = 1

// Retention holds hints, never automatic deletion: `pkgops prune --plan`
// uses Keep as its default and the UI offers it.
type Retention struct {
	Keep int `yaml:"keep,omitempty" json:"keep,omitempty"`
}

// Policy is the document.
type Policy struct {
	Schema int `yaml:"schema" json:"schema"`
	// ABIs the repository accepts, e.g. FreeBSD:15:amd64. Empty = any.
	ABIs []string `yaml:"abis" json:"abis"`
	// MinRelease is the lowest release packages must support ("15.1"): every
	// package must carry a FreeBSD_version annotation of at least that
	// release (min_release / min_osversion in pkgops.yaml). Empty = no rule.
	MinRelease string `yaml:"min_release" json:"min_release"`
	// Retention hints.
	Retention Retention `yaml:"retention" json:"retention"`

	minOS int
}

// Parse decodes and validates policy YAML (unknown keys are errors).
func Parse(data []byte) (*Policy, error) {
	var p Policy
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil && err != io.EOF {
		return nil, fmt.Errorf("%w: %s: %v", pkgerr.ErrInvalidSpec, FileName, err)
	}
	if p.Schema == 0 {
		p.Schema = SchemaVersion
	}
	if p.Schema != SchemaVersion {
		return nil, fmt.Errorf("%w: %s: schema %d is not supported (want %d)", pkgerr.ErrInvalidSpec, FileName, p.Schema, SchemaVersion)
	}
	seen := map[string]bool{}
	for i, a := range p.ABIs {
		abi, err := pkgfile.ParseABI(a)
		if err != nil || abi.String() != a {
			return nil, fmt.Errorf("%w: %s: abis[%d] %q is not a canonical ABI like FreeBSD:15:amd64", pkgerr.ErrInvalidSpec, FileName, i, a)
		}
		if seen[a] {
			return nil, fmt.Errorf("%w: %s: abi %s listed twice", pkgerr.ErrInvalidSpec, FileName, a)
		}
		seen[a] = true
	}
	if p.MinRelease != "" {
		v, err := pkgfile.ReleaseOSVersion(p.MinRelease)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", FileName, err)
		}
		p.minOS = v
		for _, a := range p.ABIs {
			abi, _ := pkgfile.ParseABI(a)
			if abi.Major < pkgfile.OSVersionMajor(v) {
				return nil, fmt.Errorf("%w: %s: min_release %s is newer than %s", pkgerr.ErrInvalidSpec, FileName, p.MinRelease, a)
			}
		}
	}
	if p.Retention.Keep < 0 {
		return nil, fmt.Errorf("%w: %s: retention.keep must be >= 0", pkgerr.ErrInvalidSpec, FileName)
	}
	return &p, nil
}

// Load reads a policy file.
func Load(file string) (*Policy, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	p, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return p, nil
}

// LoadRoot reads the policy of a repository root; a missing file returns (nil, nil).
func LoadRoot(root string) (*Policy, error) {
	p, err := Load(filepath.Join(root, FileName))
	if err != nil && os.IsNotExist(err) {
		return nil, nil
	}
	return p, err
}

// MinOSVersion is MinRelease as a __FreeBSD_version, 0 when there is no rule.
func (p *Policy) MinOSVersion() int {
	if p == nil {
		return 0
	}
	return p.minOS
}

// Check refuses a package built for an unsupported ABI, or one that does not
// declare a minimum release at least as high as the policy's. A nil policy
// accepts everything.
func (p *Policy) Check(abi string, minOSVersion int, name, version string) error {
	if p == nil {
		return nil
	}
	who := name + "-" + version
	if len(p.ABIs) > 0 {
		ok := false
		for _, a := range p.ABIs {
			if a == abi {
				ok = true
			}
		}
		if !ok {
			return fmt.Errorf("%w: %s is built for %s, but this repository supports only %s (%s)", pkgerr.ErrUnsupportedABI, who, abi, strings.Join(p.ABIs, ", "), FileName)
		}
	}
	if p.minOS > 0 {
		switch {
		case minOSVersion == 0:
			return fmt.Errorf("%w: %s declares no minimum release; this repository requires at least FreeBSD %s (set min_release: %q in pkgops.yaml)", pkgerr.ErrInvalidPackage, who, p.MinRelease, p.MinRelease)
		case minOSVersion < p.minOS:
			return fmt.Errorf("%w: %s requires FreeBSD %s (FreeBSD_version %d), below the repository minimum %s (%d); rebuild it with min_release: %q", pkgerr.ErrInvalidPackage, who, pkgfile.OSVersionRelease(minOSVersion), minOSVersion, p.MinRelease, p.minOS, p.MinRelease)
		}
	}
	return nil
}
