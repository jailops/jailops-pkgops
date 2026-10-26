package pkgfile

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"

	"jailops.net/pkgops/internal/pkgerr"
)

// ManifestFile is one entry of the files or directories map of +MANIFEST.
type ManifestFile struct {
	Path    string
	Mode    string
	Owner   string
	Group   string
	Symlink string
	Config  bool
}

// Manifest is the parsed content of +MANIFEST (without checksums).
type Manifest struct {
	Info        *Info
	Desc        string
	Prefix      string
	Categories  []string
	Deps        []Dep
	Users       []string
	Groups      []string
	Files       []ManifestFile // sorted by path
	Directories []ManifestFile // sorted by path
	Scripts     []string       // phases present, in pkg's order
}

// ReadManifest reads the full +MANIFEST of a .pkg without reading the payload.
func ReadManifest(filename string) (*Manifest, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tr, closeFn, err := OpenTar(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filename, err)
	}
	defer closeFn()
	var raw []byte
	var compact []byte
	for raw == nil {
		h, err := tr.Next()
		if err != nil {
			return nil, fmt.Errorf("%s: %w: no %s entry", filename, pkgerr.ErrInvalidPackage, manifestName)
		}
		switch h.Name {
		case compactName, manifestName:
			b, err := io.ReadAll(io.LimitReader(tr, 64<<20))
			if err != nil {
				return nil, fmt.Errorf("%s: %w: %v", filename, pkgerr.ErrInvalidPackage, err)
			}
			if h.Name == manifestName {
				raw = b
			} else {
				compact = b
			}
		}
	}
	if compact == nil {
		compact = raw
	}
	info, err := parseInfo(compact)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filename, err)
	}
	type fe struct {
		Uname   string `json:"uname"`
		Gname   string `json:"gname"`
		Perm    string `json:"perm"`
		Symlink string `json:"symlink_target"`
	}
	var m struct {
		Desc       string                     `json:"desc"`
		Prefix     string                     `json:"prefix"`
		Categories []string                   `json:"categories"`
		Deps       map[string]Dep             `json:"deps"`
		Users      []string                   `json:"users"`
		Groups     []string                   `json:"groups"`
		Files      map[string]fe              `json:"files"`
		Config     []string                   `json:"config"`
		Dirs       map[string]fe              `json:"directories"`
		Scripts    map[string]json.RawMessage `json:"scripts"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%s: %w: %v", filename, pkgerr.ErrInvalidPackage, err)
	}
	out := &Manifest{Info: info, Desc: m.Desc, Prefix: m.Prefix, Categories: m.Categories, Users: m.Users, Groups: m.Groups}
	for name, d := range m.Deps {
		d.Name = name
		out.Deps = append(out.Deps, d)
	}
	sort.Slice(out.Deps, func(i, j int) bool { return out.Deps[i].Name < out.Deps[j].Name })
	cfg := map[string]bool{}
	for _, c := range m.Config {
		cfg[c] = true
	}
	for p, e := range m.Files {
		out.Files = append(out.Files, ManifestFile{Path: p, Mode: e.Perm, Owner: e.Uname, Group: e.Gname, Symlink: e.Symlink, Config: cfg[p]})
	}
	for p, e := range m.Dirs {
		out.Directories = append(out.Directories, ManifestFile{Path: p, Mode: e.Perm, Owner: e.Uname, Group: e.Gname})
	}
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Path < out.Files[j].Path })
	sort.Slice(out.Directories, func(i, j int) bool { return out.Directories[i].Path < out.Directories[j].Path })
	for _, n := range ScriptNames {
		if _, ok := m.Scripts[n]; ok {
			out.Scripts = append(out.Scripts, n)
		}
	}
	return out, nil
}
