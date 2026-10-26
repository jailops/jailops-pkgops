package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"time"
)

// This file holds the types and client calls of the admin API: the overview
// of every published file, the prune (cleanup) requests, republishing and the
// permanent archive. See docs/admin.md.

// Policy is the repository policy (pkgops-repo.yaml).
type Policy struct {
	ABIs       []string `json:"abis"`
	MinRelease string   `json:"min_release"`
	Keep       int      `json:"keep"` // retention hint, 0 = none
}

// Version is one package file, live or pruned, with its lifecycle.
type Version struct {
	ABI        string    `json:"abi"`
	Name       string    `json:"name"`
	Version    string    `json:"version"`
	Size       int64     `json:"size"`
	SHA256     string    `json:"sha256,omitempty"`
	Built      time.Time `json:"built,omitzero"`
	MinRelease string    `json:"min_release,omitempty"`
	Comment    string    `json:"comment,omitempty"`
	// Newest is the version pkg clients are offered.
	Newest bool `json:"newest,omitempty"`
	// State is "active" (served on Pages), "pending-prune" (requested, the
	// next deploy removes it), "pruned" (removed from Pages) or
	// "archive-only" (in the archive, unknown to Pages).
	State string `json:"state"`
	// Phase of a requested prune: "committed", "workflow-running" or "published".
	Phase    string `json:"phase,omitempty"`
	Reason   string `json:"reason,omitempty"`
	PrunedAt string `json:"pruned_at,omitempty"`
	// Archive is "archived", "not-archived", "archive-deleted" (pruned and no
	// longer archived) or "unknown" (no archive configured).
	Archive    string `json:"archive"`
	ArchiveURL string `json:"archive_url,omitempty"`
}

// ABISummary totals one ABI.
type ABISummary struct {
	ABI      string `json:"abi"`
	Packages int    `json:"packages"`
	Versions int    `json:"versions"`
	Bytes    int64  `json:"bytes"`
	Pending  int    `json:"pending"`
	Pruned   int    `json:"pruned"`
}

// PruneInfo describes how cleanup requests are carried out.
type PruneInfo struct {
	// Mode is "github" (PkgOps commits the manifest) or "manual" (shows the
	// steps to apply by hand).
	Mode   string `json:"mode"`
	Repo   string `json:"repo,omitempty"`
	Branch string `json:"branch,omitempty"`
	File   string `json:"file,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Run is one run of the publish workflow.
type Run struct {
	ID         int64     `json:"id"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion,omitempty"`
	URL        string    `json:"url"`
	Created    time.Time `json:"created"`
}

// PublishInfo is the state of the publish workflow.
type PublishInfo struct {
	Available bool   `json:"available"`
	Workflow  string `json:"workflow,omitempty"`
	Error     string `json:"error,omitempty"`
	Runs      []Run  `json:"runs,omitempty"`
}

// ArchiveReport compares the Pages index with the archive.
type ArchiveReport struct {
	OnPagesNotArchived  []string `json:"on_pages_not_archived"`
	ArchivedNotOnPages  []string `json:"archived_not_on_pages"`
	Mismatched          []string `json:"mismatched"`
	PrunedStillArchived []string `json:"pruned_still_archived"`
	PrunedNotArchived   []string `json:"pruned_not_archived"`
	OK                  bool     `json:"ok"`
}

// ArchiveInfo is the state of the permanent archive (GitHub Releases).
type ArchiveInfo struct {
	Available bool           `json:"available"`
	Repo      string         `json:"repo,omitempty"`
	Error     string         `json:"error,omitempty"`
	Files     int            `json:"files"`
	Report    *ArchiveReport `json:"report,omitempty"`
}

// Overview is everything the admin UI shows.
type Overview struct {
	// Mode is "local" (a directory this server owns) or "source" (read-through
	// view of a published site).
	Mode     string       `json:"mode"`
	Source   string       `json:"source,omitempty"`
	Policy   *Policy      `json:"policy,omitempty"`
	Status   *Status      `json:"status,omitempty"`
	ABIs     []ABISummary `json:"abis"`
	Versions []Version    `json:"versions"`
	Bytes    int64        `json:"bytes"`
	Prune    PruneInfo    `json:"prune"`
	Publish  PublishInfo  `json:"publish"`
	Archive  ArchiveInfo  `json:"archive"`
	Identity *Session     `json:"identity,omitempty"`
	Problems []string     `json:"problems,omitempty"`
}

// PruneEntry names one published file to remove from the next deploy.
type PruneEntry struct {
	ABI     string `json:"abi"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Reason  string `json:"reason,omitempty"`
	// Force allows pruning the newest version of a package.
	Force bool `json:"force,omitempty"`
}

// PrunePlanRequest asks which files a selection would remove (nothing changes).
type PrunePlanRequest struct {
	ABI           string `json:"abi,omitempty"`
	Name          string `json:"name,omitempty"`
	Version       string `json:"version,omitempty"`
	Keep          int    `json:"keep,omitempty"`
	OlderThanDays int    `json:"older_than_days,omitempty"`
	Force         bool   `json:"force,omitempty"`
}

// PruneRequest asks for files to be pruned from the next deploy.
type PruneRequest struct {
	Entries []PruneEntry `json:"entries"`
	Reason  string       `json:"reason,omitempty"`
	// DryRun returns the result (diff, commit message) without writing it.
	DryRun bool `json:"dry_run,omitempty"`
	// Confirm must be "prune" (typed in the UI), so a stray call cannot request a cleanup.
	Confirm string `json:"confirm"`
}

// Manual lists the steps to apply a cleanup by hand when PkgOps cannot commit.
type Manual struct {
	File     string   `json:"file"`
	Diff     string   `json:"diff"`
	Content  string   `json:"content"`
	Commands []string `json:"commands"`
}

// PruneResult is the answer to a plan or a request.
type PruneResult struct {
	// Mode is how it was (or would be) carried out: github, file, manual.
	Mode    string       `json:"mode"`
	DryRun  bool         `json:"dry_run"`
	Entries []PruneEntry `json:"entries"`
	// Skipped are entries that were already listed or already pruned.
	Skipped   []PruneEntry `json:"skipped,omitempty"`
	Message   string       `json:"message,omitempty"`
	Commit    string       `json:"commit,omitempty"`
	CommitURL string       `json:"commit_url,omitempty"`
	Diff      string       `json:"diff"`
	Manual    *Manual      `json:"manual,omitempty"`
	// Bytes is the size the entries free on the site.
	Bytes int64 `json:"bytes"`
	// Effect states when the change takes effect.
	Effect string `json:"effect"`
}

// ArchiveRef names one archived file.
type ArchiveRef struct {
	ABI     string `json:"abi"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ArchiveDeleteRequest deletes files from the permanent archive. It is never
// part of a prune.
type ArchiveDeleteRequest struct {
	Refs   []ArchiveRef `json:"refs"`
	Reason string       `json:"reason"`
	// Confirm must be "delete from archive" (typed in the UI).
	Confirm string `json:"confirm"`
}

// Overview returns the admin overview.
func (c *Client) Overview(ctx context.Context) (*Overview, error) {
	var o Overview
	return &o, c.do(ctx, "GET", "/api/v1/overview", nil, 0, &o)
}

func (c *Client) postJSON(ctx context.Context, path string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return c.doCT(ctx, "POST", path, "application/json", bytes.NewReader(b), int64(len(b)), out)
}

// PrunePlan shows which files a selection would prune; nothing changes. Needs an admin.
func (c *Client) PrunePlan(ctx context.Context, req PrunePlanRequest) (*PruneResult, error) {
	var r PruneResult
	return &r, c.postJSON(ctx, "/api/v1/prune/plan", req, &r)
}

// PruneRequest asks for files to be removed from the next deploy. Needs an admin.
func (c *Client) PruneRequest(ctx context.Context, req PruneRequest) (*PruneResult, error) {
	var r PruneResult
	return &r, c.postJSON(ctx, "/api/v1/prune", req, &r)
}

// Republish triggers the publish workflow. Needs an admin.
func (c *Client) Republish(ctx context.Context) (*PublishInfo, error) {
	var r PublishInfo
	return &r, c.postJSON(ctx, "/api/v1/republish", struct{}{}, &r)
}

// DeleteFromArchive deletes archived files. Needs an admin and the confirmation text.
func (c *Client) DeleteFromArchive(ctx context.Context, req ArchiveDeleteRequest) error {
	return c.postJSON(ctx, "/api/v1/archive/delete", req, nil)
}
