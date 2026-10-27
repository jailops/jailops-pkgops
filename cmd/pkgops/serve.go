package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"jailops.net/pkgops"
	"jailops.net/pkgops/internal/admin"
	"jailops.net/pkgops/internal/archive"
	"jailops.net/pkgops/internal/cfaccess"
	"jailops.net/pkgops/internal/config"
	"jailops.net/pkgops/internal/mtls"
	"jailops.net/pkgops/internal/policy"
	"jailops.net/pkgops/internal/source"
	"jailops.net/pkgops/sdk"
	"jailops.net/pkgops/serve"
)

func cmdServe(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "token" {
		return cmdToken(args[1:])
	}
	fs := newFlags("serve")
	dir := fs.String("repo", "repo", "repository root directory")
	listen := fs.String("listen", "127.0.0.1:39112", "listen address, or unix:/path/to.sock (plain HTTP behind a proxy; TLS with --tls-cert)")
	socketMode := fs.String("socket-mode", "0660", "file mode of a unix socket (octal)")
	tlsCert := fs.String("tls-cert", "", "serve HTTPS with this certificate (PEM; reloaded when the file changes)")
	tlsKey := fs.String("tls-key", "", "private key of --tls-cert")
	clientCA := fs.String("client-ca", "", "REQUIRE a client certificate issued by this CA (PEM) for everything: repository, status, UI, API")
	crlFile := fs.String("crl", "", "certificate revocation list (PEM) checked for client certificates; reloaded when the file changes")
	crlGrace := fs.Duration("crl-grace", mtls.DefaultCRLGrace, "deny all clients when the CRL is this long past its nextUpdate")
	tokenFile := fs.String("token-file", "", "token file enabling the upload API (see: pkgops serve token)")
	key := fs.String("key", os.Getenv("PKGOPS_SIGNING_KEY"), "RSA private key used to re-sign catalogs after uploads")
	mode := fs.String("sign", "pubkey", "signature style: pubkey or fingerprints")
	keep := fs.Int("keep", 0, "retain only the newest N versions per package (0 = all)")
	files := fs.Bool("files-list", false, "also write files.pkg")
	max := fs.Int64("max-upload-mb", 512, "maximum upload size in MiB")
	webUI := fs.Bool("ui", false, "serve the web UI at /ui/ (it is in the binary and off by default)")
	policyFile := fs.String("policy", "", "repository policy (default: pkgops-repo.yaml in --repo)")
	src := fs.String("source", "", "read-through mode: the published site URL (read-only; instead of --repo)")
	pruneVia := fs.String("prune-via", "", "how cleanup requests are carried out: github:OWNER/REPO[@branch]:prune.yaml (default: show the manual steps)")
	workflow := fs.String("workflow", "", "publish workflow file in the --prune-via repository (enables Republish and run status)")
	archiveRepo := fs.String("archive-repo", "", "GitHub OWNER/REPO whose releases are the package archive (default: the --prune-via repository when --archive is set)")
	withArchive := fs.Bool("archive", false, "show and manage the archive in the --prune-via repository")
	team := fs.String("access-team", "", "Cloudflare Access team name (enables Access authentication)")
	aud := fs.String("access-aud", "", "Cloudflare Access application audience (AUD tag)")
	allow := fs.String("access-allow", "", "allow-list file: one \"identity role\" per line (role: viewer, uploader, admin)")
	gh := addGHFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *src != "" {
		explicit := false
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "key" || f.Name == "files-list" || f.Name == "keep" {
				explicit = true
			}
		})
		if explicit {
			return errors.New("--source is read-only: --key, --files-list and --keep do not apply")
		}
		*key = ""
	}
	if err := checkTransport(*listen, *tlsCert, *tlsKey, *clientCA, *crlFile); err != nil {
		return err
	}
	signer, err := signerFrom(*key, *mode)
	if err != nil {
		return err
	}
	cfg := serve.Config{Keep: *keep, MaxUpload: *max << 20, UI: *webUI}
	adm := &admin.Service{Workflow: *workflow}
	if *src != "" {
		if !strings.HasPrefix(*src, "https://") && !strings.HasPrefix(*src, "http://") {
			return errors.New("--source must be an http(s) URL")
		}
		cfg.Source = &source.Source{Base: *src}
		*dir = ""
	} else {
		cfg.Repo = &pkgops.Repository{Root: *dir, Signer: signer, FileList: *files}
		if cfg.Repo.Policy, err = loadPolicy(*policyFile, *dir); err != nil {
			return err
		}
	}
	if *policyFile != "" && *src != "" {
		if adm.Policy, err = policy.Load(*policyFile); err != nil {
			return err
		}
	}
	if *pruneVia != "" {
		repo, branch, file, err := parsePruneVia(*pruneVia)
		if err != nil {
			return err
		}
		c, err := gh.repoClient(repo)
		if err != nil {
			return err
		}
		adm.GH, adm.Branch, adm.PruneFile = c, branch, file
		if *withArchive || *archiveRepo != "" {
			ac := c
			if *archiveRepo != "" {
				if ac, err = gh.repoClient(*archiveRepo); err != nil {
					return err
				}
			}
			adm.Archive = &archive.Archive{GH: ac}
		}
	} else if *workflow != "" || *withArchive || *archiveRepo != "" {
		return errors.New("--workflow, --archive and --archive-repo need --prune-via (they use the same GitHub credentials)")
	}
	cfg.Admin = adm
	if *team != "" || *aud != "" || *allow != "" {
		if *team == "" || *aud == "" || *allow == "" {
			return errors.New("Cloudflare Access mode needs --access-team, --access-aud and --access-allow together; refusing to start half configured")
		}
		if cfg.Access, err = cfaccess.New(cfaccess.Config{Team: *team, Audience: *aud}); err != nil {
			return err
		}
		if cfg.AccessAllow, err = loadAllow(*allow); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Cloudflare Access mode: this server must only be reachable through Access (or on loopback behind a tunnel)")
	}
	if *tokenFile != "" {
		if cfg.Tokens, err = serve.LoadTokens(*tokenFile); err != nil {
			return err
		}
	}
	if len(cfg.Tokens) > 0 && signer == nil && cfg.Source == nil {
		fmt.Fprintln(os.Stderr, "warning: uploads enabled but no --key: the repository will be unsigned")
	}
	if *tlsCert == "" && len(cfg.Tokens) > 0 && !isLoopbackListen(*listen) {
		fmt.Fprintln(os.Stderr, "warning: upload tokens over plain HTTP on a non-loopback address: use --tls-cert/--tls-key or put TLS in front")
	}
	srv, err := serve.New(cfg)
	if err != nil {
		return err
	}
	var handler http.Handler = srv.Handler()
	hs := &http.Server{ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute,
		ErrorLog: slog.NewLogLogger(slog.Default().Handler(), slog.LevelInfo)}
	var tm *mtls.Manager
	if *tlsCert != "" {
		if tm, err = mtls.New(mtls.Options{CertFile: *tlsCert, KeyFile: *tlsKey, ClientCAFile: *clientCA, CRLFile: *crlFile, CRLGrace: *crlGrace}); err != nil {
			return err
		}
		hs.TLSConfig = tm.TLSConfig()
		handler = tm.Middleware(handler)
	}
	hs.Handler = handler
	ln, err := listenOn(*listen, *socketMode)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		sc, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		hs.Shutdown(sc)
	}()
	slog.Info("serving", "repo", *dir, "source", *src, "listen", *listen, "uploads", len(cfg.Tokens) > 0 && cfg.Source == nil,
		"cleanup", adm.GH != nil, "access", cfg.Access != nil, "tls", tm != nil, "client_certs_required", tm != nil && tm.Required())
	if tm != nil {
		err = hs.ServeTLS(ln, "", "")
	} else {
		err = hs.Serve(ln)
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// checkTransport validates the TLS flags. A server that requires client
// certificates must never speak plain HTTP, so --client-ca needs TLS, and a
// plain-HTTP server is only allowed on loopback or a unix socket when it sits
// behind a TLS terminator.
func checkTransport(listen, tlsCert, tlsKey, clientCA, crl string) error {
	if (tlsCert == "") != (tlsKey == "") {
		return errors.New("--tls-cert and --tls-key must be given together")
	}
	if clientCA != "" && tlsCert == "" {
		return errors.New("--client-ca requires --tls-cert and --tls-key: refusing to require client certificates over plain HTTP")
	}
	if crl != "" && clientCA == "" {
		return errors.New("--crl needs --client-ca")
	}
	return nil
}

// isLoopbackListen reports whether the listen address is a unix socket or a
// loopback IP/localhost.
func isLoopbackListen(listen string) bool {
	if strings.HasPrefix(listen, "unix:") {
		return true
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// listenOn opens the listener: TCP, or a unix socket with the given mode.
func listenOn(listen, socketMode string) (net.Listener, error) {
	path, ok := strings.CutPrefix(listen, "unix:")
	if !ok {
		return net.Listen("tcp", listen)
	}
	if path == "" {
		return nil, errors.New("--listen unix: needs a path")
	}
	var mode uint64
	if _, err := fmt.Sscanf(socketMode, "%o", &mode); err != nil || mode == 0 || mode > 0o777 {
		return nil, fmt.Errorf("--socket-mode %q: want an octal mode such as 0660", socketMode)
	}
	if st, err := os.Lstat(path); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			c.Close()
			return nil, fmt.Errorf("%s is in use by another server", path)
		}
		os.Remove(path) // stale socket of a previous run
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, os.FileMode(mode)); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// loadAllow reads the Cloudflare Access allow-list: "identity role" per line.
func loadAllow(file string) (map[string]config.Role, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	out := map[string]config.Role{}
	for n, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 {
			return nil, fmt.Errorf("%s:%d: want \"identity role\"", file, n+1)
		}
		switch r := config.Role(f[1]); r {
		case config.RoleViewer, config.RoleUploader, config.RoleAdmin:
			out[strings.ToLower(f[0])] = r
		default:
			return nil, fmt.Errorf("%s:%d: role %q must be viewer, uploader or admin", file, n+1, f[1])
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s lists no identity", file)
	}
	return out, nil
}

func cmdToken(args []string) error {
	fs := newFlags("serve token")
	name := fs.String("name", "", "token name (e.g. ci)")
	scopes := fs.String("scopes", "", "comma-separated scopes: upload,delete (alternative to --role)")
	role := fs.String("role", "uploader", "viewer (read-only sign-in), uploader or admin (upload and delete)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" || strings.ContainsAny(*name, " \t#") {
		return errors.New("--name is required and must not contain spaces")
	}
	if *scopes == "" {
		*scopes = *role
	}
	if _, err := config.ParseScopes(*scopes); err != nil {
		return err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	secret := "pkgops_" + hex.EncodeToString(b)
	fmt.Printf("token (shown once): %s\n", secret)
	fmt.Printf("line for the token file:\n%s %s %s\n", *name, *scopes, serve.HashToken(secret))
	return nil
}

func cmdPush(ctx context.Context, args []string) error {
	fs := newFlags("push")
	url := fs.String("url", "", "registry base URL, e.g. https://pkg.example.org")
	token := fs.String("token", os.Getenv("PKGOPS_TOKEN"), "API token (or $PKGOPS_TOKEN)")
	tc := addTLSClientFlags(fs)
	if err := fs.Parse(reorder(args)); err != nil {
		return err
	}
	if *url == "" || *token == "" || fs.NArg() == 0 {
		return errors.New("usage: pkgops push --url URL --token T [--client-cert C --client-key K [--ca-cert CA]] file.pkg ...")
	}
	hc, err := tc.client()
	if err != nil {
		return err
	}
	c := &sdk.Client{BaseURL: *url, Token: *token, HTTP: hc}
	for _, f := range fs.Args() {
		if _, err := c.Upload(ctx, f); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		fmt.Println("pushed", f)
	}
	return nil
}

func cmdRegistry(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: pkgops registry status|list|show|upload|delete --url URL [--token T]")
	}
	sub := args[0]
	fs := newFlags("registry " + sub)
	url := fs.String("url", os.Getenv("PKGOPS_URL"), "registry base URL (or $PKGOPS_URL)")
	token := fs.String("token", os.Getenv("PKGOPS_TOKEN"), "API token (or $PKGOPS_TOKEN)")
	abi := fs.String("abi", "", "ABI filter / selector")
	yes := fs.Bool("yes", false, "delete without asking")
	tc := addTLSClientFlags(fs)
	if err := fs.Parse(reorder(args[1:])); err != nil {
		return err
	}
	if *url == "" {
		return errors.New("--url is required")
	}
	hc, err := tc.client()
	if err != nil {
		return err
	}
	c := &sdk.Client{BaseURL: *url, Token: *token, HTTP: hc}
	switch sub {
	case "status":
		st, err := c.Status(ctx)
		if err != nil {
			return err
		}
		health := "healthy"
		if !st.Healthy {
			health = "PROBLEMS"
		}
		fmt.Printf("%s: %d packages, %d versions, signature %s", health, st.Packages, st.Versions, st.Signature)
		if st.Fingerprint != "" {
			fmt.Printf(" (fingerprint %s)", st.Fingerprint)
		}
		fmt.Println()
		for _, a := range st.ABIs {
			fmt.Printf("  %-20s %3d packages %3d versions  updated %s  consistent=%v\n", a.ABI, a.Packages, a.Versions, a.Updated.Format("2006-01-02 15:04:05Z"), a.Consistent)
			if a.Problem != "" {
				fmt.Printf("    problem: %s\n", a.Problem)
			}
		}
		if !st.Healthy {
			return errors.New("repository is not healthy")
		}
	case "list":
		pk, err := c.Packages(ctx, *abi)
		if err != nil {
			return err
		}
		for _, p := range pk {
			mark := " "
			if p.Latest {
				mark = "*"
			}
			fmt.Printf("%s %-22s %-14s %-20s %8d  %s\n", mark, p.Name, p.Version, p.ABI, p.Size, p.Comment)
		}
	case "show":
		if fs.NArg() != 2 || *abi == "" {
			return errors.New("usage: pkgops registry show --abi ABI NAME VERSION")
		}
		d, err := c.Package(ctx, *abi, fs.Arg(0), fs.Arg(1))
		if err != nil {
			return err
		}
		fmt.Printf("%s-%s (%s)\n%s\n\n", d.Name, d.Version, d.ABI, d.Desc)
		for _, f := range d.Files {
			t := ""
			if f.Symlink != "" {
				t = " -> " + f.Symlink
			}
			fmt.Printf("  %s %s:%s %s%s\n", f.Mode, f.Owner, f.Group, f.Path, t)
		}
	case "upload":
		if fs.NArg() == 0 {
			return errors.New("usage: pkgops registry upload file.pkg ...")
		}
		for _, f := range fs.Args() {
			u, err := c.Upload(ctx, f)
			if err != nil {
				return fmt.Errorf("%s: %w", f, err)
			}
			fmt.Printf("uploaded %s-%s (%s)\n", u.Package.Name, u.Package.Version, u.Package.ABI)
		}
	case "delete":
		if fs.NArg() != 2 || *abi == "" {
			return errors.New("usage: pkgops registry delete --abi ABI NAME VERSION [--yes]")
		}
		if !*yes {
			fmt.Fprintf(os.Stderr, "delete %s-%s (%s) from %s? [y/N] ", fs.Arg(0), fs.Arg(1), *abi, *url)
			var ans string
			fmt.Scanln(&ans)
			if !strings.EqualFold(ans, "y") && !strings.EqualFold(ans, "yes") {
				return errors.New("aborted")
			}
		}
		if err := c.Delete(ctx, *abi, fs.Arg(0), fs.Arg(1)); err != nil {
			return err
		}
		fmt.Println("deleted")
	default:
		return fmt.Errorf("unknown registry command %q", sub)
	}
	return nil
}

// tlsClientFlags carry the client certificate of a private registry (mTLS)
// for the commands that talk to a running 'pkgops serve'.
type tlsClientFlags struct{ cert, key, ca *string }

func addTLSClientFlags(fs *flag.FlagSet) tlsClientFlags {
	return tlsClientFlags{
		cert: fs.String("client-cert", os.Getenv("PKGOPS_CLIENT_CERT"), "client certificate for a registry that requires mTLS (or $PKGOPS_CLIENT_CERT)"),
		key:  fs.String("client-key", os.Getenv("PKGOPS_CLIENT_KEY"), "private key of --client-cert (or $PKGOPS_CLIENT_KEY)"),
		ca:   fs.String("ca-cert", os.Getenv("PKGOPS_CA_CERT"), "CA certificate that signed the server certificate (or $PKGOPS_CA_CERT)"),
	}
}

// client returns an http.Client for the flags, or nil for the default one.
func (f tlsClientFlags) client() (*http.Client, error) {
	if *f.cert == "" && *f.key == "" && *f.ca == "" {
		return nil, nil
	}
	if (*f.cert == "") != (*f.key == "") {
		return nil, errors.New("--client-cert and --client-key must be given together")
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12}
	if *f.cert != "" {
		c, err := tls.LoadX509KeyPair(*f.cert, *f.key)
		if err != nil {
			return nil, err
		}
		tc.Certificates = []tls.Certificate{c}
	}
	if *f.ca != "" {
		data, err := os.ReadFile(*f.ca)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(data) {
			return nil, fmt.Errorf("%s: no certificate found", *f.ca)
		}
		tc.RootCAs = pool
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: tc}, Timeout: 10 * time.Minute}, nil
}
