// Package admin is the logic behind the admin API of `pkgops serve`: the
// overview of every published file, cleanup (prune) requests, republishing and
// the permanent archive. It knows nothing about HTTP; internal/server wraps it.
//
// Roles are enforced by the server before any method here is called: every
// state-changing method takes the identity of the admin who asked and records
// it in the commit or audit message.
package admin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"jailops.net/pkgops/internal/archive"
	"jailops.net/pkgops/internal/ghapp"
	"jailops.net/pkgops/internal/index"
	"jailops.net/pkgops/internal/pkgerr"
	"jailops.net/pkgops/internal/pkgfile"
	"jailops.net/pkgops/internal/pkgver"
	"jailops.net/pkgops/internal/policy"
	"jailops.net/pkgops/internal/prune"
	"jailops.net/pkgops/sdk"
)

// Service is the admin logic over one published repository.
type Service struct {
	// Index returns the published index (a local directory's or the site's).
	Index func(ctx context.Context) (*index.Index, error)
	// Policy is the repository policy, may be nil.
	Policy *policy.Policy

	// GH is the packages repository on GitHub. Nil means manual mode: cleanup
	// requests only show the steps.
	GH *ghapp.Client
	// Branch and PruneFile locate the prune manifest in that repository
	// (defaults master and prune.yaml); Workflow is the publish workflow file.
	Branch    string
	PruneFile string
	Workflow  string
	// Archive is the GitHub Releases archive, may be nil.
	Archive *archive.Archive

	Mode   string // "local" or "source"
	Source string
	Now    func() time.Time

	mu       sync.Mutex
	recs     []archive.Record
	recsAt   time.Time
	recsErr  error
	recsLive bool
}

// archiveTTL bounds how often the archive is listed: listing reads one
// manifest per archived version, which is many GitHub requests.
const archiveTTL = 2 * time.Minute

func (s *Service) archiveRecords(ctx context.Context) ([]archive.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recsLive && s.now().Sub(s.recsAt) < archiveTTL {
		return s.recs, s.recsErr
	}
	s.recs, s.recsErr = s.Archive.List(ctx)
	s.recsAt, s.recsLive = s.now(), true
	return s.recs, s.recsErr
}

func (s *Service) forgetArchive() {
	s.mu.Lock()
	s.recsLive = false
	s.mu.Unlock()
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) branch() string {
	if s.Branch == "" {
		return "master"
	}
	return s.Branch
}

func (s *Service) pruneFile() string {
	if s.PruneFile == "" {
		return prune.FileName
	}
	return s.PruneFile
}

// Effect is the sentence the UI shows with every prune.
const Effect = "Removed from the next deploy. The published site is an artifact rebuilt from the baseline plus the new packages, so no binary lives in git history and nothing there needs rewriting. Until the publish workflow has run, the files are still served. The archive (GitHub Releases) is not touched."

// manifest reads the prune manifest from GitHub (nil, "" in manual mode).
func (s *Service) manifest(ctx context.Context) (*prune.Manifest, string, error) {
	if s.GH == nil {
		return prune.New(), "", nil
	}
	data, sha, err := s.GH.GetFile(ctx, s.pruneFile(), s.branch())
	if errors.Is(err, ghapp.ErrNotFound) {
		return prune.New(), "", nil
	}
	if err != nil {
		return nil, "", err
	}
	m, err := prune.Parse(data)
	return m, sha, err
}

// Overview gathers everything the UI shows. Failures of the optional parts
// (GitHub, the archive) are reported inside the result, not as an error.
func (s *Service) Overview(ctx context.Context) (*sdk.Overview, error) {
	ix, err := s.Index(ctx)
	if ix == nil {
		return nil, err
	}
	o := &sdk.Overview{Mode: s.Mode, Source: s.Source, ABIs: []sdk.ABISummary{}, Versions: []sdk.Version{}}
	if err != nil {
		o.Problems = append(o.Problems, "index: "+err.Error()+" (showing the last good copy)")
	}
	if s.Policy != nil {
		o.Policy = &sdk.Policy{ABIs: s.Policy.ABIs, MinRelease: s.Policy.MinRelease, Keep: s.Policy.Retention.Keep}
	}
	o.Prune = sdk.PruneInfo{Mode: "manual", File: s.pruneFile()}
	var m *prune.Manifest = prune.New()
	if s.GH != nil {
		o.Prune = sdk.PruneInfo{Mode: "github", Repo: s.GH.Owner + "/" + s.GH.Repo, Branch: s.branch(), File: s.pruneFile()}
		if mm, _, err := s.manifest(ctx); err != nil {
			o.Prune.Error = err.Error()
		} else {
			m = mm
		}
		o.Publish = s.publishInfo(ctx)
	}
	phase := "committed"
	for _, r := range o.Publish.Runs {
		if r.Status != "completed" {
			phase = "workflow-running"
		}
		break // newest run first
	}

	var recs []archive.Record
	archived := map[string]archive.Record{}
	if s.Archive != nil {
		o.Archive = sdk.ArchiveInfo{Available: true, Repo: s.Archive.GH.Owner + "/" + s.Archive.GH.Repo}
		var err error
		if recs, err = s.archiveRecords(ctx); err != nil {
			o.Archive.Error = err.Error()
		} else {
			for _, r := range recs {
				archived[r.Key()] = r
			}
			listed := map[string]bool{}
			for _, e := range m.Entries {
				listed[e.ABI+" "+e.Name+"-"+e.Version] = true
			}
			rep := archive.Verify(ix, recs, listed)
			o.Archive.Files = len(recs)
			o.Archive.Report = &sdk.ArchiveReport{OnPagesNotArchived: nz(rep.OnPagesNotArchived), ArchivedNotOnPages: nz(rep.ArchivedNotOnPages),
				Mismatched: nz(rep.Mismatched), PrunedStillArchived: nz(rep.PrunedStillArchived), PrunedNotArchived: nz(rep.PrunedNotArchived), OK: rep.OK()}
		}
	}
	archState := func(key string, live bool) (string, string) {
		if s.Archive == nil || o.Archive.Error != "" {
			return "unknown", ""
		}
		if r, ok := archived[key]; ok {
			return "archived", r.ReleaseURL
		}
		if live {
			return "not-archived", ""
		}
		return "archive-deleted", ""
	}

	sums := map[string]*sdk.ABISummary{}
	names := map[string]map[string]bool{}
	sum := func(abi string) *sdk.ABISummary {
		if sums[abi] == nil {
			sums[abi] = &sdk.ABISummary{ABI: abi}
			names[abi] = map[string]bool{}
		}
		return sums[abi]
	}
	for _, e := range ix.Packages {
		n, _ := ix.Newest(e.ABI, e.Name)
		v := sdk.Version{ABI: e.ABI, Name: e.Name, Version: e.Version, Size: e.Size, SHA256: e.SHA256, Built: e.Built,
			MinRelease: e.MinRelease, Comment: e.Comment, Newest: n.Version == e.Version, State: "active"}
		v.Archive, v.ArchiveURL = archState(e.Key(), true)
		a := sum(e.ABI)
		a.Versions++
		a.Bytes += e.Size
		names[e.ABI][e.Name] = true
		o.Bytes += e.Size
		if pe, ok := m.Has(e.ABI, e.Name, e.Version); ok {
			v.State, v.Phase, v.Reason = "pending-prune", phase, pe.Reason
			a.Pending++
		}
		o.Versions = append(o.Versions, v)
	}
	for _, p := range ix.Pruned {
		v := sdk.Version{ABI: p.ABI, Name: p.Name, Version: p.Version, Size: p.Size, SHA256: p.SHA256, Built: p.Built, State: "pruned",
			Phase: "published", Reason: p.Reason, PrunedAt: p.PrunedAt.UTC().Format(time.RFC3339)}
		v.Archive, v.ArchiveURL = archState(p.Key(), false)
		sum(p.ABI).Pruned++
		o.Versions = append(o.Versions, v)
	}
	for _, r := range recs { // archived, unknown to Pages
		if _, live := ix.Find(r.ABI, r.Name, r.Version); live {
			continue
		}
		if _, p := ix.FindPruned(r.ABI, r.Name, r.Version); p {
			continue
		}
		o.Versions = append(o.Versions, sdk.Version{ABI: r.ABI, Name: r.Name, Version: r.Version, Size: r.Size, SHA256: r.SHA256, Built: r.Built,
			State: "archive-only", Archive: "archived", ArchiveURL: r.ReleaseURL})
	}
	sort.SliceStable(o.Versions, func(i, j int) bool {
		a, b := o.Versions[i], o.Versions[j]
		if a.ABI != b.ABI {
			return a.ABI < b.ABI
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return pkgver.Compare(a.Version, b.Version) > 0
	})
	for abi, a := range sums {
		a.Packages = len(names[abi])
		o.ABIs = append(o.ABIs, *a)
	}
	sort.Slice(o.ABIs, func(i, j int) bool { return o.ABIs[i].ABI < o.ABIs[j].ABI })
	return o, nil
}

func nz(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

func (s *Service) publishInfo(ctx context.Context) sdk.PublishInfo {
	pi := sdk.PublishInfo{Available: s.GH != nil && s.Workflow != "", Workflow: s.Workflow}
	if !pi.Available {
		return pi
	}
	runs, err := s.GH.ListRuns(ctx, s.Workflow, s.branch(), 5)
	if err != nil {
		pi.Error = err.Error()
		return pi
	}
	for _, r := range runs {
		pi.Runs = append(pi.Runs, sdk.Run{ID: r.ID, Status: r.Status, Conclusion: r.Conclusion, URL: r.URL, Created: r.CreatedAt})
	}
	return pi
}

// Plan returns the entries a selection would prune. It changes nothing.
func (s *Service) Plan(ctx context.Context, req sdk.PrunePlanRequest) (*sdk.PruneResult, error) {
	ix, err := s.Index(ctx)
	if ix == nil {
		return nil, err
	}
	m, _, err := s.manifest(ctx)
	if err != nil {
		return nil, err
	}
	entries, err := prune.Plan(ix, m, prune.Selector{ABI: req.ABI, Name: req.Name, Version: req.Version, Keep: req.Keep,
		OlderThan: time.Duration(req.OlderThanDays) * 24 * time.Hour, Force: req.Force, Now: s.now()})
	if err != nil {
		return nil, err
	}
	return s.result(ix, m, entries, nil), nil
}

func (s *Service) result(ix *index.Index, m *prune.Manifest, add, skipped []prune.Entry) *sdk.PruneResult {
	mode := "manual"
	if s.GH != nil {
		mode = "github"
	}
	r := &sdk.PruneResult{Mode: mode, Effect: Effect, Entries: toSDK(add), Skipped: toSDK(skipped)}
	for _, e := range add {
		if x, ok := ix.Find(e.ABI, e.Name, e.Version); ok {
			r.Bytes += x.Size
		}
	}
	next := &prune.Manifest{Schema: m.Schema, Entries: append([]prune.Entry(nil), m.Entries...)}
	next.Merge(add)
	r.Diff = diff(s.pruneFile(), add)
	if mode == "manual" {
		content, _ := next.Marshal()
		r.Manual = &sdk.Manual{File: s.pruneFile(), Diff: r.Diff, Content: string(content), Commands: manualCommands(s.pruneFile(), add)}
	}
	return r
}

func toSDK(es []prune.Entry) []sdk.PruneEntry {
	out := []sdk.PruneEntry{}
	for _, e := range es {
		out = append(out, sdk.PruneEntry{ABI: e.ABI, Name: e.Name, Version: e.Version, Reason: e.Reason, Force: e.Force})
	}
	return out
}

func diff(file string, add []prune.Entry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n@@ entries @@\n", file, file)
	for _, e := range add {
		fmt.Fprintf(&b, "+  - abi: %s\n+    name: %s\n+    version: %q\n", e.ABI, e.Name, e.Version)
		if e.Reason != "" {
			fmt.Fprintf(&b, "+    reason: %q\n", e.Reason)
		}
		if e.Force {
			b.WriteString("+    force: true\n")
		}
	}
	return b.String()
}

func manualCommands(file string, add []prune.Entry) []string {
	var cmds []string
	for _, e := range add {
		c := fmt.Sprintf("pkgops prune --plan --from <site-url> --manifest %s --abi %s --name %s --version %s", file, e.ABI, e.Name, e.Version)
		if e.Force {
			c += " --force"
		}
		cmds = append(cmds, c)
	}
	return append(cmds, "git add "+file+" && git commit -m 'Prune: remove "+fmt.Sprint(len(add))+" package version(s)' && git push", "run the publish workflow (or push triggers it)")
}

// RequestPrune merges the entries into the prune manifest and, in GitHub
// mode, commits it. who is the authenticated admin; it goes into the commit
// message. Nothing is deleted anywhere: the next deploy leaves the files out.
func (s *Service) RequestPrune(ctx context.Context, who string, req sdk.PruneRequest) (*sdk.PruneResult, error) {
	if req.Confirm != "prune" {
		return nil, fmt.Errorf("%w: confirm must be \"prune\"", pkgerr.ErrInvalidSpec)
	}
	if len(req.Entries) == 0 {
		return nil, fmt.Errorf("%w: no entries", pkgerr.ErrInvalidSpec)
	}
	ix, err := s.Index(ctx)
	if ix == nil {
		return nil, err
	}
	var want []prune.Entry
	for _, e := range req.Entries {
		pe := prune.Entry{ABI: e.ABI, Name: e.Name, Version: e.Version, Reason: firstLine(firstNonEmpty(e.Reason, req.Reason)),
			Force: e.Force, RequestedBy: who, Requested: s.now().UTC().Format(time.RFC3339)}
		if err := pe.Validate(); err != nil {
			return nil, err
		}
		if _, live := ix.Find(pe.ABI, pe.Name, pe.Version); !live {
			if _, was := ix.FindPruned(pe.ABI, pe.Name, pe.Version); !was {
				return nil, fmt.Errorf("%w: %s %s-%s is not published", pkgerr.ErrNotFound, pe.ABI, pe.Name, pe.Version)
			}
		}
		want = append(want, pe)
	}
	for attempt := 0; ; attempt++ {
		m, sha, err := s.manifest(ctx)
		if err != nil {
			return nil, err
		}
		var add, skipped []prune.Entry
		for _, e := range want {
			if _, ok := m.Has(e.ABI, e.Name, e.Version); ok {
				skipped = append(skipped, e)
			} else if _, was := ix.FindPruned(e.ABI, e.Name, e.Version); was {
				skipped = append(skipped, e)
			} else {
				add = append(add, e)
			}
		}
		next := &prune.Manifest{Schema: m.Schema, Entries: append([]prune.Entry(nil), m.Entries...)}
		next.Merge(add)
		if err := next.Check(ix); err != nil {
			return nil, err
		}
		res := s.result(ix, m, add, skipped)
		res.DryRun = req.DryRun
		res.Message = commitMessage(who, add, req.Reason)
		if req.DryRun || s.GH == nil || len(add) == 0 {
			return res, nil
		}
		content, err := next.Marshal()
		if err != nil {
			return nil, err
		}
		commit, err := s.GH.PutFile(ctx, s.pruneFile(), s.branch(), res.Message, content, sha)
		if errors.Is(err, ghapp.ErrConflict) && attempt < 2 {
			continue // somebody committed in between: re-read and merge again
		}
		if err != nil {
			return nil, err
		}
		res.Commit = commit
		res.CommitURL = fmt.Sprintf("https://github.com/%s/%s/commit/%s", s.GH.Owner, s.GH.Repo, commit)
		return res, nil
	}
}

// CancelPrune removes not-yet-applied entries from the manifest.
func (s *Service) CancelPrune(ctx context.Context, who string, refs []sdk.ArchiveRef) error {
	if s.GH == nil {
		return fmt.Errorf("%w: cleanup requests are manual here: edit %s yourself", pkgerr.ErrInvalidSpec, s.pruneFile())
	}
	m, sha, err := s.manifest(ctx)
	if err != nil {
		return err
	}
	n := 0
	var parts []string
	for _, r := range refs {
		if m.Remove(r.ABI, r.Name, r.Version) {
			n++
			parts = append(parts, r.Name+" "+r.Version+" ("+r.ABI+")")
		}
	}
	if n == 0 {
		return fmt.Errorf("%w: none of these is in the prune manifest", pkgerr.ErrNotFound)
	}
	content, err := m.Marshal()
	if err != nil {
		return err
	}
	_, err = s.GH.PutFile(ctx, s.pruneFile(), s.branch(), "Prune: cancel "+strings.Join(parts, ", ")+" requested by "+oneLine(who), content, sha)
	return err
}

func commitMessage(who string, add []prune.Entry, reason string) string {
	who = oneLine(who)
	if len(add) == 1 {
		e := add[0]
		msg := fmt.Sprintf("Prune: remove %s %s (%s) requested by %s", e.Name, e.Version, e.ABI, who)
		if reason = firstLine(reason); reason != "" {
			msg += "\n\n" + reason
		}
		return msg
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Prune: remove %d package versions requested by %s\n\n", len(add), who)
	for _, e := range add {
		fmt.Fprintf(&b, "- %s %s (%s)\n", e.Name, e.Version, e.ABI)
	}
	if reason = firstLine(reason); reason != "" {
		b.WriteString("\n" + reason + "\n")
	}
	return b.String()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return s
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// Republish triggers the publish workflow.
func (s *Service) Republish(ctx context.Context, who string) (*sdk.PublishInfo, error) {
	if s.GH == nil || s.Workflow == "" {
		return nil, fmt.Errorf("%w: no publish workflow is configured (--prune-via and --workflow)", pkgerr.ErrInvalidSpec)
	}
	if err := s.GH.DispatchWorkflow(ctx, s.Workflow, s.branch(), nil); err != nil {
		return nil, err
	}
	pi := s.publishInfo(ctx)
	return &pi, nil
}

// ArchiveDelete deletes files from the permanent archive. It is separate from
// pruning and needs its own typed confirmation and a reason.
func (s *Service) ArchiveDelete(ctx context.Context, who string, req sdk.ArchiveDeleteRequest) error {
	if s.Archive == nil {
		return fmt.Errorf("%w: no archive is configured", pkgerr.ErrInvalidSpec)
	}
	if req.Confirm != "delete from archive" {
		return fmt.Errorf("%w: confirm must be \"delete from archive\"", pkgerr.ErrInvalidSpec)
	}
	if len(req.Refs) == 0 || strings.TrimSpace(req.Reason) == "" {
		return fmt.Errorf("%w: refs and a reason are required", pkgerr.ErrInvalidSpec)
	}
	var refs []archive.Ref
	for _, r := range req.Refs {
		if _, err := pkgfile.ParseABI(r.ABI); err != nil {
			return err
		}
		refs = append(refs, archive.Ref{ABI: r.ABI, Name: r.Name, Version: r.Version})
	}
	defer s.forgetArchive()
	return s.Archive.Delete(ctx, refs, who, req.Reason)
}
