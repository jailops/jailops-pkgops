package publish

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"jailops.net/pkgops/internal/fsutil"
	"jailops.net/pkgops/internal/pkgerr"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Options configures [Publish].
type Options struct {
	// Delete removes files from the destination that are not in the source
	// (done last, after the catalogs are in place).
	Delete bool
	// SSHCommand overrides the ssh command rsync uses for remote destinations,
	// e.g. "ssh -i key -o BatchMode=yes".
	SSHCommand string
	// Log receives one line per transferred file (local copies) or rsync output.
	Log io.Writer
}

// catalogFiles are the repository files clients fetch first. They are
// published last so a client never sees a catalog that points at a package
// that is not uploaded yet.
var catalogFiles = []string{"files.pkg", "packagesite.pkg", "data.pkg", "meta", "meta.conf"}

func catIndex(rel string) int {
	b := filepath.Base(rel)
	for i, c := range catalogFiles {
		if c == b {
			return i
		}
	}
	return len(catalogFiles)
}

func isCatalog(name string) bool { return catIndex(name) < len(catalogFiles) }

// IsRemote reports whether dest looks like an rsync-over-ssh target
// ("user@host:/path" or "host:/path") rather than a local directory.
func IsRemote(dest string) bool {
	if strings.HasPrefix(dest, "/") || strings.HasPrefix(dest, ".") {
		return false
	}
	i := strings.Index(dest, ":")
	slash := strings.Index(dest, "/")
	return i > 0 && (slash == -1 || i < slash)
}

// Publish synchronises a repository tree (the Root of a [Repository]) to a
// static-site directory or, with an rsync target, to a remote host. Packages
// go first, catalogs last; unchanged files are skipped.
func Publish(ctx context.Context, src, dest string, opt Options) error {
	if st, err := os.Stat(src); err != nil || !st.IsDir() {
		return fmt.Errorf("%w: source %q is not a directory", pkgerr.ErrNotFound, src)
	}
	if IsRemote(dest) {
		return publishRsync(ctx, src, dest, opt)
	}
	return publishLocal(ctx, src, dest, opt)
}

func publishLocal(ctx context.Context, src, dest string, opt Options) error {
	var data, cats []string
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		rel, _ := filepath.Rel(src, p)
		if isCatalog(d.Name()) {
			cats = append(cats, rel)
		} else {
			data = append(data, rel)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(data)
	sort.Slice(cats, func(i, j int) bool {
		di, dj := filepath.Dir(cats[i]), filepath.Dir(cats[j])
		if di != dj {
			return di < dj
		}
		return catIndex(cats[i]) < catIndex(cats[j])
	})
	copyOne := func(rel string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		from, to := filepath.Join(src, rel), filepath.Join(dest, rel)
		same, err := sameContent(from, to)
		if err != nil {
			return err
		}
		if same {
			return nil
		}
		f, err := os.Open(from)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := fsutil.AtomicWrite(to, 0o644, func(w io.Writer) error { _, err := io.Copy(w, f); return err }); err != nil {
			return err
		}
		if opt.Log != nil {
			fmt.Fprintln(opt.Log, rel)
		}
		return nil
	}
	for _, rel := range append(data, cats...) {
		if err := copyOne(rel); err != nil {
			return err
		}
	}
	if opt.Delete {
		keep := map[string]bool{}
		for _, r := range append(data, cats...) {
			keep[r] = true
		}
		return filepath.WalkDir(dest, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(dest, p)
			if !keep[rel] && !strings.HasPrefix(d.Name(), ".") {
				if opt.Log != nil {
					fmt.Fprintln(opt.Log, "delete", rel)
				}
				return os.Remove(p)
			}
			return nil
		})
	}
	return nil
}

func sameContent(a, b string) (bool, error) {
	sa, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	sb, err := os.Stat(b)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if sa.Size() != sb.Size() {
		return false, nil
	}
	ha, err := fileSum(a)
	if err != nil {
		return false, err
	}
	hb, err := fileSum(b)
	if err != nil {
		return false, err
	}
	return bytes.Equal(ha, hb), nil
}

func fileSum(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

func publishRsync(ctx context.Context, src, dest string, opt Options) error {
	if _, err := exec.LookPath("rsync"); err != nil {
		return fmt.Errorf("rsync not found in PATH: %w", err)
	}
	srcArg := strings.TrimRight(src, "/") + "/"
	base := []string{"-rlt", "--chmod=D755,F644"}
	if opt.SSHCommand != "" {
		base = append(base, "-e", opt.SSHCommand)
	}
	var excl, incl []string
	for _, c := range catalogFiles {
		excl = append(excl, "--exclude", c)
		incl = append(incl, "--include", c)
	}
	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "rsync", args...)
		cmd.Stdout, cmd.Stderr = opt.Log, opt.Log
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("rsync: %w", err)
		}
		return nil
	}
	// 1: packages (no catalogs), 2: catalogs only, 3: optional delete pass.
	if err := run(append(append(append([]string{}, base...), excl...), srcArg, dest)...); err != nil {
		return err
	}
	pass2 := append(append([]string{}, base...), "--include", "*/")
	pass2 = append(pass2, incl...)
	pass2 = append(pass2, "--exclude", "*", srcArg, dest)
	if err := run(pass2...); err != nil {
		return err
	}
	if opt.Delete {
		return run(append(append(append([]string{}, base...), "--delete"), append(excl, srcArg, dest)...)...)
	}
	return nil
}
