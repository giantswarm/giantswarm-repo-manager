// Package teamfiles is giantswarm/github as the writes see it: the team
// files (repositories/<team>.yaml, the desired state), the per-team policy
// files (repository-setup/<team>.yaml: Slack channels, repair opt-in), and the
// one way a change lands — a branch and a pull request, opened with the
// GitHub client the caller passes in (the person's token for every write, the
// App for unattended reads). Nothing here decides who the client is.
package teamfiles

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"
	"github.com/google/go-github/v92/github"
	"gopkg.in/yaml.v3"
)

const (
	// DefaultRepository and DefaultRef are where the team files live.
	DefaultRepository = "giantswarm/github"
	DefaultRef        = "main"
	// PolicyDir holds the per-team policy files (PRD D6).
	PolicyDir = "repository-setup"
	// ReconcilerWorkflow is the reconciler's workflow file in the repository
	// (giantswarm/github#6031); its workflow_dispatch takes repository, team
	// and dry-run.
	ReconcilerWorkflow = "reconcile-repositories.yaml"
)

// ErrEntryNotFound says no team file declares the repository.
var ErrEntryNotFound = errors.New("no team file declares the repository")

// Repo is the repository that holds the team files, at Ref, as Client. As
// names the client's identity for messages: the person's login, or empty for
// the inventory App.
type Repo struct {
	Client      *github.Client
	Owner, Name string
	Ref         string
	As          string
}

// WriteApp is the App whose authorization a person's token carries; a read
// that fails with 404 or 403 as the person names it.
const WriteApp = "giantswarm-repo-manager"

// ErrNotReachable says the credential does not reach the repository: the
// person's authorization of the App, or the inventory App's installation,
// does not include it. GitHub answers 404 for that, the same as for a
// missing file; Read tells them apart with one probe of the repository.
var ErrNotReachable = errors.New("the repository is not reachable with this credential")

// New returns the Repo for "owner/name" (DefaultRepository when empty) at
// ref (DefaultRef when empty).
func New(client *github.Client, repository, ref string) (Repo, error) {
	if repository == "" {
		repository = DefaultRepository
	}
	if ref == "" {
		ref = DefaultRef
	}
	owner, name, ok := strings.Cut(repository, "/")
	if !ok || owner == "" || name == "" {
		return Repo{}, fmt.Errorf("team files repository %q is not owner/name", repository)
	}
	return Repo{Client: client, Owner: owner, Name: name, Ref: ref}, nil
}

// Slug is owner/name@ref, for messages.
func (r Repo) Slug() string { return r.Owner + "/" + r.Name + "@" + r.Ref }

// URL is the repository's page.
func (r Repo) URL() string { return "https://github.com/" + r.Owner + "/" + r.Name }

// File is one file as it stands on Ref.
type File struct {
	Path    string
	Content []byte
	SHA     string
}

// Read reads one file at Ref. A 404 is one of two things and the error says
// which: the file is missing on Ref, or the credential does not reach the
// repository at all (the repository itself answers 404 or 403).
func (r Repo) Read(ctx context.Context, path string) (*File, error) {
	fc, _, resp, err := r.Client.Repositories.GetContents(ctx, r.Owner, r.Name, path, &github.RepositoryContentGetOptions{Ref: r.Ref})
	if err != nil {
		return nil, r.readError(ctx, path, resp, err)
	}
	if fc == nil {
		return nil, fmt.Errorf("%s: %s is not a file", r.Slug(), path)
	}
	content, err := fc.GetContent()
	if err != nil {
		return nil, fmt.Errorf("%s: decode %s: %w", r.Slug(), path, err)
	}
	return &File{Path: path, Content: []byte(content), SHA: fc.GetSHA()}, nil
}

// readError is the error of a failed read of path: on 404 or 403 the
// repository is probed once to tell a credential that does not reach it from
// a path missing on Ref.
func (r Repo) readError(ctx context.Context, path string, resp *github.Response, err error) error {
	if denied(resp) {
		reachable, perr := r.Reachable(ctx)
		switch {
		case perr != nil:
			return fmt.Errorf("%s: read %s: %w (and probing the repository: %v)", r.Slug(), path, err, perr)
		case !reachable:
			return r.notReachable(resp.StatusCode)
		case resp.StatusCode == http.StatusNotFound:
			return fmt.Errorf("%s/%s: %s not found in %s", r.Owner, r.Name, path, r.Ref)
		}
	}
	return fmt.Errorf("%s: read %s: %w", r.Slug(), path, err)
}

// Reachable says whether Client reaches the repository at all (GET
// /repos/{owner}/{name}): false on 404 or 403, an error on anything else.
func (r Repo) Reachable(ctx context.Context) (bool, error) {
	_, resp, err := r.Client.Repositories.Get(ctx, r.Owner, r.Name)
	switch {
	case err == nil:
		return true, nil
	case denied(resp):
		return false, nil
	}
	return false, fmt.Errorf("%s: GET /repos/%s/%s: %w", r.Slug(), r.Owner, r.Name, err)
}

// notReachable is the error of a read the credential cannot make, naming
// the credential and the fix.
func (r Repo) notReachable(status int) error {
	repo := r.Owner + "/" + r.Name
	if r.As == "" {
		return fmt.Errorf("reading %s as the inventory App failed (%d): %w — the App's installation must include the repository", repo, status, ErrNotReachable)
	}
	return fmt.Errorf("reading %s as %s failed (%d): %w — your authorization of the App %s does not reach the repository: the App must be installed on all repositories (an org owner's setting), or your own access does not include it", repo, r.As, status, ErrNotReachable, WriteApp)
}

// denied is GitHub's answer for what the credential cannot see: 404 (the
// usual, also for a repository it has no access to) or 403.
func denied(resp *github.Response) bool {
	return resp != nil && (resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden)
}

// TeamFile is a team's file with its parsed entries.
type TeamFile struct {
	*File
	Team    string
	Entries *reposetup.TeamFile
}

// TeamFile reads and parses repositories/<team>.yaml.
func (r Repo) TeamFile(ctx context.Context, team string) (*TeamFile, error) {
	f, err := r.Read(ctx, reposetup.TeamFilePath(team))
	if err != nil {
		return nil, err
	}
	tf, err := reposetup.ParseTeamFile(team, strings.NewReader(string(f.Content)))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", f.Path, err)
	}
	return &TeamFile{File: f, Team: team, Entries: tf}, nil
}

// Teams lists the teams that have a team file.
func (r Repo) Teams(ctx context.Context) ([]string, error) {
	_, dir, resp, err := r.Client.Repositories.GetContents(ctx, r.Owner, r.Name, reposetup.TeamFilesDir, &github.RepositoryContentGetOptions{Ref: r.Ref})
	if err != nil {
		return nil, r.readError(ctx, reposetup.TeamFilesDir, resp, err)
	}
	var teams []string
	for _, e := range dir {
		if e.GetType() == "file" && strings.HasSuffix(e.GetName(), ".yaml") {
			teams = append(teams, reposetup.TeamOf(e.GetPath()))
		}
	}
	sort.Strings(teams)
	return teams, nil
}

// FindEntry returns the team file declaring name. hint is the team the
// inventory knows (tried first); otherwise every team file is read.
func (r Repo) FindEntry(ctx context.Context, name, hint string) (*TeamFile, error) {
	teams := []string{}
	if hint != "" {
		teams = append(teams, hint)
	} else {
		all, err := r.Teams(ctx)
		if err != nil {
			return nil, err
		}
		teams = all
	}
	for _, team := range teams {
		tf, err := r.TeamFile(ctx, team)
		if err != nil {
			return nil, err
		}
		if _, ok := tf.Entries.Entry(name); ok {
			return tf, nil
		}
	}
	return nil, fmt.Errorf("%w: %s (looked in %s)", ErrEntryNotFound, name, strings.Join(teams, ", "))
}

// Policy is a team's repository-setup policy file: the two channels every
// message of the set-up automation goes to — asks with an Approve button
// (archive, deprecate, an incoming transfer, a repair review) to
// SlackChannel, notices (what someone did, a failed step, a finding) to
// StandupChannel — and the opt-in to alignment (AlignOptIn): without it the
// automation checks the team's repositories and changes nothing. Both
// channels are required; a file without one is refused, nothing stands in
// for it.
type Policy struct {
	Team           string `json:"team" yaml:"-"`
	SlackChannel   string `json:"slackChannel" yaml:"slackChannel"`
	StandupChannel string `json:"standupChannel" yaml:"standupChannel"`
	AlignOptIn     bool   `json:"alignOptIn" yaml:"alignOptIn"`
}

// Policy reads repository-setup/<team>.yaml.
func (r Repo) Policy(ctx context.Context, team string) (*Policy, error) {
	f, err := r.Read(ctx, PolicyDir+"/"+team+".yaml")
	if err != nil {
		return nil, err
	}
	return ParsePolicy(team, f.Path, f.Content)
}

// ParsePolicy parses a team's policy file; path names it in errors.
func ParsePolicy(team, path string, content []byte) (*Policy, error) {
	p := Policy{Team: team}
	if err := yaml.Unmarshal(content, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if p.SlackChannel == "" {
		return nil, fmt.Errorf("%s: slackChannel is empty", path)
	}
	if p.StandupChannel == "" {
		return nil, fmt.Errorf("%s: standupChannel is empty", path)
	}
	return &p, nil
}

// Change is one pull request: the files as they should read on the branch.
type Change struct {
	Branch string
	Title  string
	Body   string
	Files  map[string][]byte
}

// PullRequest is what OpenPullRequest returns.
type PullRequest struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	Branch string `json:"branch"`
	Title  string `json:"title"`
	// Author is the login the pull request was opened as.
	Author string `json:"author,omitempty"`
	// Existing says the pull request was open already for this change's
	// branch and is reported, not opened again.
	Existing bool `json:"existing,omitempty"`
}

// OpenPullRequest creates the branch off Ref with one commit carrying every
// file of the change and opens the pull request — all as Client, so the
// commit and the pull request are the caller's.
func (r Repo) OpenPullRequest(ctx context.Context, ch Change) (*PullRequest, error) {
	if len(ch.Files) == 0 {
		return nil, errors.New("open pull request: no files to change")
	}
	base, _, err := r.Client.Git.GetRef(ctx, r.Owner, r.Name, "heads/"+r.Ref)
	if err != nil {
		return nil, fmt.Errorf("%s: read %s: %w", r.Slug(), r.Ref, err)
	}
	if _, resp, err := r.Client.Git.GetRef(ctx, r.Owner, r.Name, "heads/"+ch.Branch); err == nil {
		return r.openFor(ctx, ch.Branch)
	} else if resp == nil || resp.StatusCode != http.StatusNotFound {
		return nil, fmt.Errorf("%s: read branch %s: %w", r.Slug(), ch.Branch, err)
	}
	paths := make([]string, 0, len(ch.Files))
	for p := range ch.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	entries := make([]*github.TreeEntry, 0, len(paths))
	for _, p := range paths {
		entries = append(entries, &github.TreeEntry{Path: ptr(p), Mode: ptr("100644"), Type: ptr("blob"), Content: ptr(string(ch.Files[p]))})
	}
	tree, _, err := r.Client.Git.CreateTree(ctx, r.Owner, r.Name, base.GetObject().GetSHA(), entries)
	if err != nil {
		return nil, fmt.Errorf("%s: create tree: %w", r.Slug(), err)
	}
	commit, _, err := r.Client.Git.CreateCommit(ctx, r.Owner, r.Name, github.Commit{
		Message: ptr(ch.Title),
		Tree:    tree,
		Parents: []*github.Commit{{SHA: ptr(base.GetObject().GetSHA())}},
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: create commit: %w", r.Slug(), err)
	}
	if _, _, err := r.Client.Git.CreateRef(ctx, r.Owner, r.Name, github.CreateRef{Ref: "refs/heads/" + ch.Branch, SHA: commit.GetSHA()}); err != nil {
		return nil, fmt.Errorf("%s: create branch %s: %w", r.Slug(), ch.Branch, err)
	}
	pr, _, err := r.Client.PullRequests.Create(ctx, r.Owner, r.Name, github.CreatePullRequest{
		Title: ptr(ch.Title), Head: ch.Branch, Base: r.Ref, Body: ptr(ch.Body),
	})
	if err != nil {
		return nil, fmt.Errorf("%s: open pull request: %w", r.Slug(), err)
	}
	return &PullRequest{Number: pr.GetNumber(), URL: pr.GetHTMLURL(), Branch: ch.Branch, Title: ch.Title, Author: pr.GetUser().GetLogin()}, nil
}

// openFor is the open pull request from branch — a change committed once
// already, reported instead of opened again — or an error naming the branch
// when none is open (the branch is left over; a person removes it).
func (r Repo) openFor(ctx context.Context, branch string) (*PullRequest, error) {
	prs, _, err := r.Client.PullRequests.List(ctx, r.Owner, r.Name, &github.PullRequestListOptions{
		State: "open", Head: r.Owner + ":" + branch, Base: r.Ref, ListOptions: github.ListOptions{PerPage: 1},
	})
	if err != nil {
		return nil, fmt.Errorf("%s: branch %s exists already; read its pull request: %w", r.Slug(), branch, err)
	}
	if len(prs) == 0 {
		return nil, fmt.Errorf("%s: branch %s exists already without an open pull request — delete the branch and run again", r.Slug(), branch)
	}
	pr := prs[0]
	return &PullRequest{Number: pr.GetNumber(), URL: pr.GetHTMLURL(), Branch: branch, Title: pr.GetTitle(), Author: pr.GetUser().GetLogin(), Existing: true}, nil
}

// ChangedTeamFiles lists the teams whose files a pull request touches.
func (r Repo) ChangedTeamFiles(ctx context.Context, number int) ([]string, error) {
	files, _, err := r.Client.PullRequests.ListFiles(ctx, r.Owner, r.Name, number, &github.ListOptions{PerPage: 100})
	if err != nil {
		return nil, fmt.Errorf("%s: files of #%d: %w", r.Slug(), number, err)
	}
	var teams []string
	for _, f := range files {
		if strings.HasPrefix(f.GetFilename(), reposetup.TeamFilesDir+"/") && strings.HasSuffix(f.GetFilename(), ".yaml") {
			teams = append(teams, reposetup.TeamOf(f.GetFilename()))
		}
	}
	return teams, nil
}

// Approve submits the approving review on a pull request as Client.
func (r Repo) Approve(ctx context.Context, number int, body string) (string, error) {
	rev, _, err := r.Client.PullRequests.CreateReview(ctx, r.Owner, r.Name, number, &github.PullRequestReviewRequest{Event: ptr("APPROVE"), Body: ptr(body)})
	if err != nil {
		return "", fmt.Errorf("%s: approve #%d: %w", r.Slug(), number, err)
	}
	return rev.GetHTMLURL(), nil
}

// Dispatch starts a workflow_dispatch of the workflow file on Ref.
func (r Repo) Dispatch(ctx context.Context, workflow string, inputs map[string]any) error {
	_, _, err := r.Client.Actions.CreateWorkflowDispatchEventByFileName(ctx, r.Owner, r.Name, workflow, github.CreateWorkflowDispatchEventRequest{Ref: r.Ref, Inputs: inputs})
	if err != nil {
		return fmt.Errorf("%s: dispatch %s: %w", r.Slug(), workflow, err)
	}
	return nil
}

// WorkflowURL is the workflow's runs page.
func (r Repo) WorkflowURL(workflow string) string {
	return r.URL() + "/actions/workflows/" + workflow
}

// ptr is a pointer to v (go-github's fields are pointers; its own Ptr is
// generic and trips govet's inline analyzer).
func ptr[T any](v T) *T { return &v }

// IsMember says whether login is an active member of the team in the org,
// read as Client (the person reading their own membership needs read:org).
func IsMember(ctx context.Context, client *github.Client, org, team, login string) (bool, error) {
	m, resp, err := client.Teams.GetTeamMembershipBySlug(ctx, org, team, login)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return false, nil
		}
		return false, fmt.Errorf("membership of %s in %s/%s: %w", login, org, team, err)
	}
	return m.GetState() == "active", nil
}
