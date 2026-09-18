package teamfiles

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"
	"github.com/google/go-github/v92/github"
)

// A pull request this server opens is one commit on a fresh branch of Ref.
// When a neighbouring entry of the same team file changes first — a
// repository declared right after the one being archived, two lifecycle
// changes side by side, a transfer — the commit no longer applies and GitHub
// reports the pull request conflicting (mergeable: false): the approving
// review lands, auto-merge cannot fire, and the change waits for somebody to
// rebase by hand. The change is declarative — the entries as they should
// read, not a diff — so the pull request is re-rendered instead: its
// entries, found against its merge base, re-applied to the team files as
// they read on Ref now, and the branch force-pushed as Client with that one
// commit. The pull request, its ask and its auto-merge stay.

// GitHub computes mergeability on demand: a pull request read right after
// its base moved reports it unknown (null) until a background job has run.
// Conflicts reads again a few times before giving up.
const (
	mergeableReads = 4
	mergeableWait  = 1500 * time.Millisecond
)

// ErrMergeableUnknown says GitHub had not computed the pull request's
// mergeability after the reads Conflicts makes.
var ErrMergeableUnknown = errors.New("GitHub has not computed whether the pull request is mergeable")

// Conflicts says whether GitHub reports the pull request conflicting with its
// base (mergeable: false). pr is the pull request as read; it is read again,
// as Client, while GitHub has not computed its mergeability, and the pull
// request as last read comes back with the answer. ErrMergeableUnknown when
// GitHub still has not said after mergeableReads reads.
func (r Repo) Conflicts(ctx context.Context, pr *github.PullRequest) (*github.PullRequest, bool, error) {
	for i := 1; pr.Mergeable == nil; i++ {
		if i == mergeableReads {
			return pr, false, fmt.Errorf("%s#%d: %w", r.Owner+"/"+r.Name, pr.GetNumber(), ErrMergeableUnknown)
		}
		select {
		case <-ctx.Done():
			return pr, false, ctx.Err()
		case <-time.After(mergeableWait):
		}
		again, _, err := r.Client.PullRequests.Get(ctx, r.Owner, r.Name, pr.GetNumber())
		if err != nil {
			return pr, false, fmt.Errorf("%s#%d: %w", r.Owner+"/"+r.Name, pr.GetNumber(), err)
		}
		pr = again
	}
	return pr, !pr.GetMergeable(), nil
}

// Rerendered is a pull request re-rendered on the current Ref.
type Rerendered struct {
	Number int    `json:"number"`
	Branch string `json:"branch"`
	// Base is the commit of Ref the pull request sits on now, Commit its one
	// commit.
	Base   string `json:"base"`
	Commit string `json:"commit"`
	// Files are the team files rewritten; Entries the names of the entries
	// the pull request changes in them — the repositories.
	Files   []string `json:"files"`
	Entries []string `json:"entries"`
}

// ErrNotATeamFileChange says the pull request changes a file that is not a
// team file: not this server's to re-render.
var ErrNotATeamFileChange = errors.New("the pull request changes a file that is not a team file")

// ErrOnRefAlready says every entry the pull request changes reads on Ref
// already as the pull request wants it: nothing to re-render, the pull
// request is redundant.
var ErrOnRefAlready = errors.New("the pull request's change is on the base already")

// Rerender rewrites the pull request as one fresh commit on Ref's current
// head, as Client: the entries it changes — different from, or absent in,
// its merge base with Ref, per team file — re-applied to the files as they
// read on Ref now, and the branch force-pushed. A file the re-render leaves
// as it reads on Ref is left out of the commit; a pull request whose whole
// change is on Ref already is ErrOnRefAlready, one that changes a file that
// is not a team file ErrNotATeamFileChange — neither is pushed.
func (r Repo) Rerender(ctx context.Context, pr *github.PullRequest) (*Rerendered, error) {
	number, branch, head := pr.GetNumber(), pr.GetHead().GetRef(), pr.GetHead().GetSHA()
	slug := fmt.Sprintf("%s/%s#%d", r.Owner, r.Name, number)
	if branch == "" || head == "" {
		return nil, fmt.Errorf("%s: the pull request names no head branch", slug)
	}
	cmp, _, err := r.Client.Repositories.CompareCommits(ctx, r.Owner, r.Name, r.Ref, head, &github.ListOptions{PerPage: 100})
	if err != nil {
		return nil, fmt.Errorf("%s: compare %s with %s: %w", slug, r.Ref, head, err)
	}
	mergeBase := cmp.GetMergeBaseCommit().GetSHA()
	if mergeBase == "" {
		return nil, fmt.Errorf("%s: GitHub names no merge base of %s and %s", slug, r.Ref, head)
	}
	base, _, err := r.Client.Git.GetRef(ctx, r.Owner, r.Name, "heads/"+r.Ref)
	if err != nil {
		return nil, fmt.Errorf("%s: read %s: %w", r.Slug(), r.Ref, err)
	}
	files := map[string][]byte{}
	entries := map[string]bool{}
	for _, f := range cmp.Files {
		path := f.GetFilename()
		if !isTeamFile(path) {
			return nil, fmt.Errorf("%w: %s changes %s", ErrNotATeamFileChange, slug, path)
		}
		was, err := r.at(mergeBase).readOptional(ctx, path)
		if err != nil {
			return nil, err
		}
		want, err := r.at(branch).readOptional(ctx, path)
		if err != nil {
			return nil, err
		}
		now, err := r.readOptional(ctx, path)
		if err != nil {
			return nil, err
		}
		content, names, err := rerenderFile(reposetup.TeamOf(path), was, want, now)
		if err != nil {
			return nil, fmt.Errorf("%s: re-render %s: %w", slug, path, err)
		}
		for _, n := range names {
			entries[n] = true
		}
		if !bytes.Equal(content, now) {
			files[path] = content
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrOnRefAlready, slug)
	}
	sha, err := r.commit(ctx, base.GetObject().GetSHA(), Change{Title: pr.GetTitle(), Files: files})
	if err != nil {
		return nil, err
	}
	if _, _, err := r.Client.Git.UpdateRef(ctx, r.Owner, r.Name, "heads/"+branch, github.UpdateRef{SHA: sha, Force: ptr(true)}); err != nil {
		return nil, fmt.Errorf("%s: force-push %s: %w", slug, branch, err)
	}
	out := &Rerendered{Number: number, Branch: branch, Base: base.GetObject().GetSHA(), Commit: sha}
	for p := range files {
		out.Files = append(out.Files, p)
	}
	for n := range entries {
		out.Entries = append(out.Entries, n)
	}
	sort.Strings(out.Files)
	sort.Strings(out.Entries)
	return out, nil
}

// Approved says whether the pull request carries an approving review, read
// as Client.
func (r Repo) Approved(ctx context.Context, number int) (bool, error) {
	opts := &github.ListOptions{PerPage: 100}
	for {
		reviews, resp, err := r.Client.PullRequests.ListReviews(ctx, r.Owner, r.Name, number, opts)
		if err != nil {
			return false, fmt.Errorf("%s: reviews of #%d: %w", r.Slug(), number, err)
		}
		for _, rev := range reviews {
			if rev.GetState() == "APPROVED" {
				return true, nil
			}
		}
		if resp.NextPage == 0 {
			return false, nil
		}
		opts.Page = resp.NextPage
	}
}

// at is the repository at another ref: a branch, a commit.
func (r Repo) at(ref string) Repo {
	r.Ref = ref
	return r
}

// readOptional reads path at Ref: nil content, no error, when Ref has no
// such file.
func (r Repo) readOptional(ctx context.Context, path string) ([]byte, error) {
	f, err := r.Read(ctx, path)
	if errors.Is(err, ErrFileNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return f.Content, nil
}
