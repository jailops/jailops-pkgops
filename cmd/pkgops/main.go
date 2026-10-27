// Command pkgops builds native FreeBSD packages and static pkg repositories
// on any OS. It is a thin wrapper around the jailops.net/pkgops SDK.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"jailops.net/pkgops"
	"jailops.net/pkgops/internal/index"
	"jailops.net/pkgops/internal/policy"
	"jailops.net/pkgops/internal/prune"
)

// Stamped at release time with -ldflags "-X main.version=... -X main.commit=... -X main.date=..."
// (Justfile, .goreleaser.yaml). A plain `go build` or `go install ...@tag` leaves them unset and
// versionString falls back to the module build info.
var (
	version = "dev"
	commit  = ""
	date    = ""
)

func versionString() string {
	v, c, d := version, commit, date
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			v = strings.TrimPrefix(bi.Main.Version, "v")
		}
		for _, kv := range bi.Settings {
			switch {
			case kv.Key == "vcs.revision" && c == "":
				c = kv.Value
			case kv.Key == "vcs.time" && d == "":
				d = kv.Value
			}
		}
	}
	out := "pkgops " + v
	if c != "" {
		if len(c) > 12 {
			c = c[:12]
		}
		out += " (commit " + c
		if d != "" {
			out += ", built " + d
		}
		out += ")"
	}
	return out
}

const usage = `pkgops - build native FreeBSD packages and pkg repositories (no FreeBSD host needed)

usage:
  pkgops build   [-f pkgops.yaml] [--abi FreeBSD:15:amd64] [-o dist] [--no-go-build] [--epoch N]
  pkgops inspect <file.pkg>                 verify a package and print its manifest
  pkgops key     generate [--out repo.key] [--bits 4096]
  pkgops key     public --key repo.key              print the public key (PEM)
  pkgops key     fingerprint <repo.pub>             print the file for a client fingerprints dir
  pkgops repo    [--repo DIR] [--key repo.key] [--sign pubkey|fingerprints] [--files-list]
                 [--policy pkgops-repo.yaml] [--prune prune.yaml] [dist-dir | file.pkg ...]
  pkgops fetch   --from URL|github-releases:OWNER/REPO --into DIR [--abi a,b] [--prune prune.yaml] [--source pages|releases|auto]
  pkgops prune   --plan [--from URL|DIR] [--manifest prune.yaml] [--keep N | --older-than 90d | --name N --version V] [-n]
  pkgops archive push --repo OWNER/REPO [--from DIR]      keep every published .pkg in GitHub Releases
  pkgops archive prune --repo OWNER/REPO --abi A --name N --version V --reason R --confirm "delete from archive"
  pkgops verify-archive --repo OWNER/REPO --from URL|DIR [--prune prune.yaml]
  pkgops verify  <repo-dir> [--pubkey repo.pub]
  pkgops publish --from DIR --to DEST [--delete] [--ssh "ssh -i key"]
  pkgops conf    --name NAME --url URL [--pubkey PATH | --fingerprints DIR] [--client-cert PATH --client-key PATH [--ca-cert PATH]]
  pkgops pki     init|issue|revoke|list|crl|show   private CA for a private registry (mTLS); see 'pkgops pki'
  pkgops push    --url URL --token T file.pkg ...   upload to a running 'pkgops serve'
  pkgops serve   token --name NAME [--role viewer|uploader|admin]
  pkgops registry status|list|show|upload|delete --url URL [--token T]   (client of a running registry)
  pkgops serve   --repo DIR [--listen :39112|unix:/path.sock] [--token-file FILE] [--key repo.key]
                 [--tls-cert F --tls-key F [--client-ca F [--crl F]]]
  pkgops serve   --source URL [--prune-via github:OWNER/REPO[@branch]:prune.yaml] [--access-team T --access-aud A --access-allow FILE]
  pkgops version
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "build":
		err = cmdBuild(ctx, args)
	case "inspect":
		err = cmdInspect(args)
	case "key":
		err = cmdKey(args)
	case "repo":
		err = cmdRepo(ctx, args)
	case "verify":
		err = cmdVerify(args)
	case "fetch":
		err = cmdFetch(ctx, args)
	case "prune":
		err = cmdPrune(ctx, args)
	case "archive":
		err = cmdArchive(ctx, args)
	case "verify-archive":
		err = cmdVerifyArchive(ctx, args)
	case "publish":
		err = cmdPublish(ctx, args)
	case "registry":
		err = cmdRegistry(ctx, args)
	case "push":
		err = cmdPush(ctx, args)
	case "conf":
		err = cmdConf(args)
	case "pki":
		err = cmdPKI(args)
	case "serve":
		err = cmdServe(ctx, args)
	case "version", "--version":
		fmt.Println(versionString())
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "pkgops: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		msg := err.Error()
		if !strings.HasPrefix(msg, "pkgops: ") {
			msg = "pkgops: " + msg
		}
		fmt.Fprintln(os.Stderr, msg)
		os.Exit(1)
	}
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("pkgops "+name, flag.ContinueOnError)
	return fs
}

func cmdBuild(ctx context.Context, args []string) error {
	fs := newFlags("build")
	file := fs.String("f", "pkgops.yaml", "manifest file")
	abi := fs.String("abi", "", "ABIs to build: a,b or all (default: every ABI in the manifest)")
	out := fs.String("o", "dist", "output directory")
	noGo := fs.Bool("no-go-build", false, "skip the go build step")
	epoch := fs.Int64("epoch", -1, "timestamp for package content (default SOURCE_DATE_EPOCH)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	spec, err := pkgops.LoadSpec(*file)
	if err != nil {
		return err
	}
	opt := pkgops.BuildOptions{BaseDir: filepath.Dir(*file), ABI: *abi, OutDir: *out, SkipGoBuild: *noGo, Log: os.Stderr}
	if *epoch >= 0 {
		opt.Epoch = time.Unix(*epoch, 0)
	}
	built, err := pkgops.BuildAll(ctx, spec, opt)
	for _, b := range built {
		fmt.Println(b.Path)
	}
	return err
}

func cmdInspect(args []string) error {
	fs := newFlags("inspect")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: pkgops inspect <file.pkg>")
	}
	info, err := pkgops.Verify(fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Printf("%s\n", info.CompactManifest())
	fmt.Fprintf(os.Stderr, "ok: %s-%s %s, %d bytes, sha256 %s\n", info.Name, info.Version, info.ABI, info.Size, info.SHA256)
	return nil
}

func cmdKey(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: pkgops key generate|fingerprint")
	}
	switch args[0] {
	case "generate":
		fs := newFlags("key generate")
		out := fs.String("out", "repo.key", "private key path (public key is written next to it as .pub)")
		bits := fs.Int("bits", 4096, "RSA key size")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		k, err := pkgops.GenerateRSAKey(*bits)
		if err != nil {
			return err
		}
		priv, err := pkgops.PrivateKeyPEM(k)
		if err != nil {
			return err
		}
		pub, err := pkgops.PublicKeyPEM(&k.PublicKey)
		if err != nil {
			return err
		}
		pubPath := strings.TrimSuffix(*out, filepath.Ext(*out)) + ".pub"
		if _, err := os.Stat(*out); err == nil {
			return fmt.Errorf("%s already exists; refusing to overwrite", *out)
		}
		if err := os.WriteFile(*out, priv, 0o400); err != nil {
			return err
		}
		if err := os.WriteFile(pubPath, pub, 0o644); err != nil {
			return err
		}
		fmt.Printf("private key: %s\npublic key:  %s\nfingerprint: %s\n", *out, pubPath, pkgops.Fingerprint(pub))
		return nil
	case "public":
		fs := newFlags("key public")
		key := fs.String("key", os.Getenv("PKGOPS_SIGNING_KEY"), "private key (PEM)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *key == "" {
			return errors.New("--key is required")
		}
		s, err := pkgops.LoadRSASigner(*key, pkgops.SignPubkey)
		if err != nil {
			return err
		}
		pub, err := pkgops.PublicKeyPEM(&s.Key.PublicKey)
		if err != nil {
			return err
		}
		fmt.Print(string(pub))
		return nil
	case "fingerprint":
		if len(args) != 2 {
			return errors.New("usage: pkgops key fingerprint <repo.pub>")
		}
		pub, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		fmt.Print(string(pkgops.FingerprintFile(pub)))
		return nil
	}
	return fmt.Errorf("unknown key command %q", args[0])
}

func signerFrom(key, mode string) (pkgops.Signer, error) {
	if key == "" {
		return nil, nil
	}
	m := pkgops.SignPubkey
	switch mode {
	case "pubkey", "":
	case "fingerprints":
		m = pkgops.SignFingerprints
	default:
		return nil, fmt.Errorf("--sign must be pubkey or fingerprints")
	}
	return pkgops.LoadRSASigner(key, m)
}

func cmdRepo(ctx context.Context, args []string) error {
	fs := newFlags("repo")
	dir := fs.String("repo", "repo", "repository root directory")
	key := fs.String("key", os.Getenv("PKGOPS_SIGNING_KEY"), "RSA private key (PEM); also $PKGOPS_SIGNING_KEY; omit for an unsigned repository")
	mode := fs.String("sign", "pubkey", "signature style: pubkey or fingerprints")
	files := fs.Bool("files-list", false, "also write files.pkg")
	channel := fs.String("channel", pkgops.DefaultChannel, "channel directory name")
	replace := fs.Bool("replace", false, "overwrite an already published name-version with different bytes")
	retain := fs.Int("keep", 0, "DELETES older versions: keep only the newest N per package (0 = keep all); prefer a reviewed prune manifest")
	policyFile := fs.String("policy", "", "repository policy (default: pkgops-repo.yaml in the repository root, if present)")
	pruneFile := fs.String("prune", "", "prune manifest: listed files are removed from the repository, the catalog and the index")
	if err := fs.Parse(args); err != nil {
		return err
	}
	signer, err := signerFrom(*key, *mode)
	if err != nil {
		return err
	}
	r := &pkgops.Repository{Root: *dir, Channel: *channel, Signer: signer, FileList: *files, Replace: *replace}
	if r.Policy, err = loadPolicy(*policyFile, *dir); err != nil {
		return err
	}
	if *pruneFile != "" {
		if r.PruneManifest, err = prune.Load(*pruneFile); err != nil {
			return err
		}
	}
	pkgs, err := expandPkgs(fs.Args())
	if err != nil {
		return err
	}
	infos, err := r.Update(ctx, pkgs...)
	if err != nil {
		return err
	}
	for _, i := range infos {
		fmt.Printf("added %s-%s (%s)\n", i.Name, i.Version, i.ABI)
	}
	abis, _ := r.ABIs()
	for _, a := range abis {
		if *retain > 0 {
			rm, err := r.Prune(ctx, a, *retain)
			if err != nil {
				return err
			}
			for _, f := range rm {
				fmt.Println("pruned", f)
			}
		}
		fmt.Printf("repository: %s\n", r.Dir(a))
	}
	if r.Policy != nil && *policyFile != "" {
		// keep a copy next to the index so the site states its own rules
		if err := copyFile(*policyFile, filepath.Join(*dir, policy.FileName)); err != nil {
			return err
		}
	}
	if signer == nil {
		fmt.Fprintln(os.Stderr, "warning: repository is NOT signed")
	}
	return nil
}

// loadPolicy reads --policy, or the policy file in the repository root.
func loadPolicy(file, root string) (*policy.Policy, error) {
	if file != "" {
		return policy.Load(file)
	}
	return policy.LoadRoot(root)
}

func copyFile(src, dst string) error {
	if a, _ := filepath.Abs(src); a == mustAbs(dst) {
		return nil
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

func mustAbs(p string) string {
	a, _ := filepath.Abs(p)
	return a
}

func cmdVerify(args []string) error {
	fs := newFlags("verify")
	pub := fs.String("pubkey", "", "public key PEM to verify catalog signatures against")
	if err := fs.Parse(reorder(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: pkgops verify <repo-dir> [--pubkey repo.pub]")
	}
	var pem []byte
	if *pub != "" {
		var err error
		if pem, err = os.ReadFile(*pub); err != nil {
			return err
		}
	}
	res, err := pkgops.VerifyRepository(fs.Arg(0), pem)
	if err != nil {
		return err
	}
	fmt.Printf("ok: %d package(s), signature: %q (verified against key: %v)\n", res.Packages, res.Signature, pem != nil)
	return nil
}

// reorder lets flags follow positional arguments (flag stops at the first
// non-flag otherwise).
func reorder(args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				flags = append(flags, args[i+1])
				i++
			}
		} else {
			pos = append(pos, a)
		}
	}
	return append(flags, pos...)
}

func cmdPublish(ctx context.Context, args []string) error {
	fs := newFlags("publish")
	from := fs.String("from", "repo", "repository root to publish")
	to := fs.String("to", "", "destination: local directory or [user@]host:/path (rsync over ssh)")
	del := fs.Bool("delete", false, "delete destination files not present in the source")
	ssh := fs.String("ssh", "", "ssh command for rsync, e.g. \"ssh -i key\"")
	policyFile := fs.String("policy", "", "repository policy to enforce (default: pkgops-repo.yaml in --from, if present)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *to == "" {
		return errors.New("--to is required")
	}
	if err := checkPolicy(*from, *policyFile); err != nil {
		return err
	}
	return pkgops.Publish(ctx, *from, *to, pkgops.PublishOptions{Delete: *del, SSHCommand: *ssh, Log: os.Stderr})
}

// checkPolicy refuses to publish a repository that holds a package the policy
// does not allow (unsupported ABI, minimum release too low).
func checkPolicy(root, file string) error {
	p, err := loadPolicy(file, root)
	if err != nil || p == nil {
		return err
	}
	ix, err := index.Read(root)
	if err != nil {
		return err
	}
	if ix == nil {
		return fmt.Errorf("%s has no %s; run pkgops repo first so the policy can be checked", root, index.FileName)
	}
	for _, e := range ix.Packages {
		if err := p.Check(e.ABI, e.MinOSVersion, e.Name, e.Version); err != nil {
			return fmt.Errorf("refusing to publish: %w", err)
		}
	}
	return nil
}

func cmdConf(args []string) error {
	fs := newFlags("conf")
	name := fs.String("name", "", "repository name")
	url := fs.String("url", "", "repository URL, e.g. https://pkg.example.org/${ABI}/latest")
	pub := fs.String("pubkey", "", "client path of the public key (signature_type pubkey)")
	fp := fs.String("fingerprints", "", "client fingerprints directory (signature_type fingerprints)")
	cert := fs.String("client-cert", "", "client path of the client certificate (private registry, mTLS)")
	ckey := fs.String("client-key", "", "client path of the client private key")
	ca := fs.String("ca-cert", "", "client path of the CA certificate that signed the server certificate")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" || *url == "" {
		return errors.New("--name and --url are required")
	}
	return writeConf(os.Stdout, os.Stderr, *name, *url, *pub, *fp, *cert, *ckey, *ca)
}

// writeConf prints a pkg repos/NAME.conf entry. The env block sets the
// environment of the fetcher for this repository (libfetch reads
// SSL_CLIENT_CERT_FILE, SSL_CLIENT_KEY_FILE and SSL_CA_CERT_FILE).
func writeConf(out, errw io.Writer, name, url, pub, fp, cert, ckey, ca string) error {
	if (cert == "") != (ckey == "") {
		return errors.New("--client-cert and --client-key must be given together")
	}
	fmt.Fprintf(out, "%s: {\n  url: %q,\n", name, url)
	switch {
	case pub != "":
		fmt.Fprintf(out, "  signature_type: \"pubkey\",\n  pubkey: %q,\n", pub)
	case fp != "":
		fmt.Fprintf(out, "  signature_type: \"fingerprints\",\n  fingerprints: %q,\n", fp)
	default:
		fmt.Fprintln(errw, "warning: no signature configured")
	}
	if cert != "" || ca != "" {
		fmt.Fprint(out, "  env: {\n")
		if cert != "" {
			fmt.Fprintf(out, "    SSL_CLIENT_CERT_FILE: %q,\n    SSL_CLIENT_KEY_FILE: %q,\n", cert, ckey)
		}
		if ca != "" {
			fmt.Fprintf(out, "    SSL_CA_CERT_FILE: %q,\n", ca)
		}
		fmt.Fprint(out, "  },\n")
	}
	fmt.Fprint(out, "  enabled: yes\n}\n")
	return nil
}

// expandPkgs turns directories (such as dist/, holding <ABI>/*.pkg) into the
// .pkg files below them, so one command updates every ABI's repository.
func expandPkgs(args []string) ([]string, error) {
	var out []string
	for _, a := range args {
		st, err := os.Stat(a)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			out = append(out, a)
			continue
		}
		n := len(out)
		err = filepath.WalkDir(a, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".pkg") && !strings.HasPrefix(d.Name(), ".") {
				out = append(out, p)
			}
			return err
		})
		if err != nil {
			return nil, err
		}
		if len(out) == n {
			return nil, fmt.Errorf("no .pkg files under %s", a)
		}
	}
	return out, nil
}
