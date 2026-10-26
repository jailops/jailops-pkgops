package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strconv"

	"jailops.net/pkgops/internal/ghapp"
)

// ghFlags are the credentials flags shared by serve and archive.
type ghFlags struct {
	appID, installID *int64
	keyFile          *string
}

func addGHFlags(fs *flag.FlagSet) *ghFlags {
	envInt := func(k string) int64 {
		n, _ := strconv.ParseInt(os.Getenv(k), 10, 64)
		return n
	}
	return &ghFlags{
		appID:     fs.Int64("github-app-id", envInt("PKGOPS_GITHUB_APP_ID"), "GitHub App id (or $PKGOPS_GITHUB_APP_ID)"),
		installID: fs.Int64("github-installation-id", envInt("PKGOPS_GITHUB_INSTALLATION_ID"), "GitHub App installation id (or $PKGOPS_GITHUB_INSTALLATION_ID)"),
		keyFile:   fs.String("github-key-file", os.Getenv("PKGOPS_GITHUB_KEY_FILE"), "GitHub App private key file, mode 0600 (or $PKGOPS_GITHUB_KEY_FILE); the key is never taken from a flag value"),
	}
}

// auth builds the GitHub credentials: the App (preferred), else a plain token
// from $PKGOPS_GITHUB_TOKEN or $GITHUB_TOKEN (the Actions job token; a
// personal token is the less preferred fallback for dev and tests).
func (g *ghFlags) auth() (ghapp.Auth, string, error) {
	if *g.appID != 0 || *g.installID != 0 || *g.keyFile != "" {
		a, err := ghapp.NewAppAuth(ghapp.AppConfig{AppID: *g.appID, InstallationID: *g.installID, KeyFile: *g.keyFile})
		return a, "github-app", err
	}
	for _, k := range []string{"PKGOPS_GITHUB_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
		if v := os.Getenv(k); v != "" {
			return ghapp.StaticToken(v), "token", nil
		}
	}
	return nil, "", errors.New("no GitHub credentials: set --github-app-id, --github-installation-id and --github-key-file (preferred), or $PKGOPS_GITHUB_TOKEN")
}

var reRepo = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9-]*)/([A-Za-z0-9._-]+)$`)

// repoClient returns a GitHub client for OWNER/REPO.
func (g *ghFlags) repoClient(repo string) (*ghapp.Client, error) {
	m := reRepo.FindStringSubmatch(repo)
	if m == nil {
		return nil, fmt.Errorf("repository %q is not OWNER/REPO", repo)
	}
	auth, _, err := g.auth()
	if err != nil {
		return nil, err
	}
	return &ghapp.Client{Owner: m[1], Repo: m[2], Auth: auth}, nil
}

var rePruneVia = regexp.MustCompile(`^github:([A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9._-]+)(?:@([^:@]+))?:(.+)$`)

// parsePruneVia parses github:OWNER/REPO[@branch]:prune.yaml.
func parsePruneVia(v string) (repo, branch, file string, err error) {
	m := rePruneVia.FindStringSubmatch(v)
	if m == nil {
		return "", "", "", fmt.Errorf("--prune-via %q: want github:OWNER/REPO[@branch]:path, for example github:jailops/jailops-packages@master:prune.yaml", v)
	}
	branch = m[2]
	if branch == "" {
		branch = "master"
	}
	return m[1], branch, m[3], nil
}
