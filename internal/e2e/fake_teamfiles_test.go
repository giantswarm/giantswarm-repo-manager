package e2e

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The fake giantswarm/github, as the writes see it: file contents at main,
// the git data API (refs, trees, commits) the pull-request path uses, the
// pull requests with their files and reviews, and workflow dispatches.
const (
	teamPlaneteers = "team-planeteers"
	teamOther      = "team-other"
	// Channel IDs: bumblebee's policy carries the IDs, planeteers' names the
	// server maps (reviews.channels). The team channel takes the asks, the
	// standup channel the notices.
	bumblebeeChannel  = "C0BUMBLEBEE"
	bumblebeeStandup  = "C0STANDUPBEE"
	planeteersChannel = "C0PLANETEERS"
	planeteersStandup = "C0STANDUPPLA"
	// The debug channel: a name reviews.channels resolves, like a team's.
	debugChannelName = "repo-manager-debug"
	debugChannelID   = "C0DEBUGROUND"

	// Tool arguments and values the scenarios repeat.
	argDryRun      = "dryRun"
	argMode        = "mode"
	argScope       = "scope"
	argLifecycle   = "lifecycle"
	modeCommit     = "commit"
	kComponentType = "componentType"
	kApp           = "app"
	kPublic        = "public"
	kPath          = "path"
	kContent       = "content"
	kType          = "type"
	kMessage       = "message"
	kData          = "data"
	kGitHub        = "github"
	kSHA           = "sha"
	kObject        = "object"
	kDescription   = "description"
	kHTMLURL       = "html_url"
	kRef           = "ref"
	kChartName     = "chartName"
	argPullRequest = "pullRequest"
	argToTeam      = "toTeam"
	kFlavours      = "flavours"
	kService       = "service"
	kGen           = "gen"
	kLanguage      = "language"
	kGo            = "go"
	kCI            = "ci"
	kGenerate      = "generate"
	kChannel       = "channel"
	kVisibility    = "visibility"
	kFile          = "file"
	kID            = "id"
	modeApply      = "apply"
	bob            = "bob"
	// eventApprove is the review event of an approval; reviewApproved its state.
	eventApprove   = "APPROVE"
	reviewApproved = "APPROVED"
)

const planeteersFile = `# Planeteers' repositories.
- name: planet-service
  componentType: service
  gen:
    language: go
    flavours: [app]
`

type fakePullRequest struct {
	Number  int
	Title   string
	Body    string
	Head    string
	Author  string
	Files   map[string][]byte
	Reviews []fakeReview
	// AutoMerge is GitHub's auto-merge, armed through GraphQL; Merged is set
	// by a merge call, or by an approving review while AutoMerge is armed —
	// the way GitHub merges by itself. MergeTitle is the squash commit's.
	AutoMerge  bool
	Merged     bool
	MergedAt   time.Time
	MergeTitle string
	// MergeSHA is the squash commit on main: the head of the push run that
	// follows the merge.
	MergeSHA string
	// Closed is a pull request closed without a merge.
	Closed bool
}

type fakeReview struct{ User, Event, Body string }

// headsPrefix is the git ref prefix of a branch.
const headsPrefix = "heads/"

type fakeTeamFiles struct {
	mu      sync.Mutex
	files   map[string][]byte // path → content at main
	refs    map[string]string // heads/<branch> → sha
	trees   map[string]map[string][]byte
	commits map[string]string // sha → tree sha
	// parents is each commit's parent; snapshots the files at main per main
	// commit — a branch's files are its commits laid over the main commit
	// they descend from, and a merge base is that main commit. A merge lays
	// the pull request's files on main and moves it to a new commit.
	parents    map[string]string
	snapshots  map[string]map[string][]byte
	pulls      map[int]*fakePullRequest
	next       int
	dispatches []map[string]any
	// denied are the logins whose credential does not reach the repository.
	denied map[string]bool
	// checksPending marks pull requests whose checks still run: GitHub
	// refuses to merge them (405) and lets auto-merge be armed.
	checksPending map[int]bool
	// reads counts the contents reads by path and ref.
	reads map[string]int
}

func newFakeTeamFiles() *fakeTeamFiles {
	f := &fakeTeamFiles{
		files: map[string][]byte{
			"repositories/" + team + ".yaml":               []byte(teamFile),
			"repositories/" + teamPlaneteers + ".yaml":     []byte(planeteersFile),
			"repository-setup/" + team + ".yaml":           []byte("slackChannel: " + bumblebeeChannel + "\nstandupChannel: " + bumblebeeStandup + "\n"),
			"repository-setup/" + teamPlaneteers + ".yaml": []byte("slackChannel: " + teamPlaneteers + "\nstandupChannel: standup-planeteers\n"),
		},
		refs: map[string]string{headsPrefix + mainBranch: "base000"}, trees: map[string]map[string][]byte{}, commits: map[string]string{}, parents: map[string]string{}, pulls: map[int]*fakePullRequest{}, next: 4711,
		reads: map[string]int{},
	}
	f.snapshots = map[string]map[string][]byte{"base000": copyFiles(f.files)}
	return f
}

func copyFiles(files map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(files))
	for p, c := range files {
		out[p] = c
	}
	return out
}

// viewAt is the repository's files at ref — main, a branch, a commit: a
// branch's commits laid over the main commit they descend from. nil for a
// ref the fake does not know.
func (f *fakeTeamFiles) viewAt(ref string) map[string][]byte {
	if ref == "" || ref == mainBranch {
		return f.files
	}
	sha := ref
	if s, ok := f.refs[headsPrefix+ref]; ok {
		sha = s
	}
	if snap, ok := f.snapshots[sha]; ok {
		return snap
	}
	tree, ok := f.commits[sha]
	if !ok {
		return nil
	}
	view := copyFiles(f.viewAt(f.parents[sha]))
	for p, c := range f.trees[tree] {
		view[p] = c
	}
	return view
}

// mergeBase is the main commit the commit descends from, and the paths its
// commits since then touch.
func (f *fakeTeamFiles) mergeBase(sha string) (string, []string) {
	touched := map[string]bool{}
	for {
		if _, ok := f.snapshots[sha]; ok {
			paths := make([]string, 0, len(touched))
			for p := range touched {
				paths = append(paths, p)
			}
			sort.Strings(paths)
			return sha, paths
		}
		for p := range f.trees[f.commits[sha]] {
			touched[p] = true
		}
		parent, ok := f.parents[sha]
		if !ok {
			return "", nil
		}
		sha = parent
	}
}

// headFiles is what the pull request's branch changes: the tree of its head.
func (f *fakeTeamFiles) headFiles(pr *fakePullRequest) map[string][]byte {
	return f.trees[f.commits[f.refs[headsPrefix+pr.Head]]]
}

// conflicts says whether main moved under the pull request in a file it
// changes — GitHub's mergeable: false for two changes to neighbouring
// entries of one team file.
func (f *fakeTeamFiles) conflicts(pr *fakePullRequest) bool {
	base, _ := f.mergeBase(f.refs[headsPrefix+pr.Head])
	for p := range f.headFiles(pr) {
		if !bytes.Equal(f.files[p], f.snapshots[base][p]) {
			return true
		}
	}
	return false
}

// land merges the pull request at: its files land on main, which moves to a
// new commit — the way GitHub's squash does.
func (f *fakeTeamFiles) land(pr *fakePullRequest, at time.Time) {
	pr.Merged, pr.MergedAt = true, at
	for p, c := range f.headFiles(pr) {
		f.files[p] = c
	}
	sha := fmt.Sprintf("main%03d", len(f.snapshots))
	pr.MergeSHA = sha
	f.refs[headsPrefix+mainBranch] = sha
	f.snapshots[sha] = copyFiles(f.files)
}

// commit lands files on main as one new commit, the way a push to main does.
func (f *fakeTeamFiles) commit(files map[string][]byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for p, c := range files {
		f.files[p] = c
	}
	sha := fmt.Sprintf("main%03d", len(f.snapshots))
	f.refs[headsPrefix+mainBranch] = sha
	f.snapshots[sha] = copyFiles(f.files)
}

// head is the commit at main.
func (f *fakeTeamFiles) head() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refs[headsPrefix+mainBranch]
}

// readsOf is how many times path was read at ref.
func (f *fakeTeamFiles) readsOf(path, ref string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads[path+"@"+ref]
}

// mergeSHA is the merge commit of pull request n, "" while it is unmerged.
func (f *fakeTeamFiles) mergeSHA(n int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pulls[n].MergeSHA
}

// deny makes the repository unreachable for login: every read as them
// answers 404, as GitHub does for a person whose authorization of the App
// does not reach the repository.
func (f *fakeTeamFiles) deny(login string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.denied == nil {
		f.denied = map[string]bool{}
	}
	f.denied[login] = true
}

func (f *fakeTeamFiles) deniedFor(r *http.Request, g *fakeGitHub) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.denied[g.logins[bearer(r)]]
}

// pullRequests are the pull requests opened so far, in order.
func (f *fakeTeamFiles) pullRequests() []*fakePullRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*fakePullRequest, 0, len(f.pulls))
	for _, pr := range f.pulls {
		out = append(out, pr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out
}

func (f *fakeTeamFiles) register(mux *http.ServeMux, g *fakeGitHub) {
	base := "/api/v3/repos/" + org + "/github"
	// The repository itself: 404 for a person whose credential does not reach
	// it (GitHub's answer for a repository the App is not installed on), the
	// same as for its files.
	mux.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		if f.deniedFor(r, g) {
			ghMessage(w, http.StatusNotFound, "Not Found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{kName: "github", "full_name": org + "/github", kPrivate: true, "default_branch": mainBranch})
	})
	mux.HandleFunc("GET "+base+"/contents/{path...}", func(w http.ResponseWriter, r *http.Request) {
		if f.deniedFor(r, g) {
			ghMessage(w, http.StatusNotFound, "Not Found")
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		p := r.PathValue("path")
		f.reads[p+"@"+r.URL.Query().Get(kRef)]++
		view := f.viewAt(r.URL.Query().Get(kRef))
		if c, ok := view[p]; ok {
			writeJSON(w, http.StatusOK, map[string]any{kType: kFile, kName: path.Base(p), kPath: p, kSHA: "blob-" + p, "encoding": "base64", kContent: base64.StdEncoding.EncodeToString(c)})
			return
		}
		// A directory lists its files and its subdirectories, as GitHub does.
		var dir []map[string]any
		listed := map[string]bool{}
		for name := range view {
			rest, ok := strings.CutPrefix(name, p+"/")
			if !ok {
				continue
			}
			first, _, sub := strings.Cut(rest, "/")
			if listed[first] {
				continue
			}
			listed[first] = true
			typ := kFile
			if sub {
				typ = "dir"
			}
			dir = append(dir, map[string]any{kType: typ, kName: first, kPath: p + "/" + first})
		}
		sort.Slice(dir, func(i, j int) bool { return dir[i][kName].(string) < dir[j][kName].(string) })
		if dir == nil {
			ghMessage(w, http.StatusNotFound, "Not Found")
			return
		}
		writeJSON(w, http.StatusOK, dir)
	})
	mux.HandleFunc("GET "+base+"/git/ref/{ref...}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if sha, ok := f.refs[r.PathValue("ref")]; ok {
			writeJSON(w, http.StatusOK, map[string]any{kRef: "refs/" + r.PathValue(kRef), kObject: map[string]any{kSHA: sha, kType: modeCommit}})
			return
		}
		ghMessage(w, http.StatusNotFound, "Not Found")
	})
	mux.HandleFunc("POST "+base+"/git/trees", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tree []struct{ Path, Content string } `json:"tree"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		defer f.mu.Unlock()
		sha := fmt.Sprintf("tree%d", len(f.trees)+1)
		files := map[string][]byte{}
		for _, e := range req.Tree {
			files[e.Path] = []byte(e.Content)
		}
		f.trees[sha] = files
		writeJSON(w, http.StatusCreated, map[string]any{kSHA: sha})
	})
	mux.HandleFunc("POST "+base+"/git/commits", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tree    string   `json:"tree"`
			Parents []string `json:"parents"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		defer f.mu.Unlock()
		sha := fmt.Sprintf("commit%d", len(f.commits)+1)
		f.commits[sha] = req.Tree
		if len(req.Parents) > 0 {
			f.parents[sha] = req.Parents[0]
		}
		writeJSON(w, http.StatusCreated, map[string]any{kSHA: sha})
	})
	// The force-push of a re-render: the branch moves to the new commit and
	// the pull request changes what that commit changes.
	mux.HandleFunc("PATCH "+base+"/git/refs/{ref...}", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			SHA   string `json:"sha"`
			Force bool   `json:"force"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		defer f.mu.Unlock()
		ref := r.PathValue(kRef)
		if _, ok := f.refs[ref]; !ok {
			ghMessage(w, http.StatusUnprocessableEntity, "Reference does not exist")
			return
		}
		if _, ok := f.commits[req.SHA]; !ok {
			ghMessage(w, http.StatusUnprocessableEntity, "Object does not exist")
			return
		}
		if !req.Force {
			ghMessage(w, http.StatusUnprocessableEntity, "Update is not a fast forward")
			return
		}
		f.refs[ref] = req.SHA
		for _, pr := range f.pulls {
			if headsPrefix+pr.Head == ref {
				pr.Files = f.headFiles(pr)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{kRef: "refs/" + ref, kObject: map[string]any{kSHA: req.SHA, kType: modeCommit}})
	})
	// The three-dot compare a re-render reads: the merge base of main and the
	// head, and the files the head changes since it.
	mux.HandleFunc("GET "+base+"/compare/{basehead}", func(w http.ResponseWriter, r *http.Request) {
		_, head, ok := strings.Cut(r.PathValue("basehead"), "...")
		f.mu.Lock()
		defer f.mu.Unlock()
		if sha, isBranch := f.refs[headsPrefix+head]; isBranch {
			head = sha
		}
		mb, paths := f.mergeBase(head)
		if !ok || mb == "" {
			ghMessage(w, http.StatusNotFound, "Not Found")
			return
		}
		files := make([]map[string]any, 0, len(paths))
		for _, p := range paths {
			files = append(files, map[string]any{"filename": p})
		}
		writeJSON(w, http.StatusOK, map[string]any{"merge_base_commit": map[string]any{kSHA: mb}, "files": files})
	})
	mux.HandleFunc("POST "+base+"/git/refs", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Ref, SHA string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.refs[strings.TrimPrefix(req.Ref, "refs/")] = req.SHA
		writeJSON(w, http.StatusCreated, map[string]any{kRef: req.Ref})
	})
	mux.HandleFunc("POST "+base+"/pulls", func(w http.ResponseWriter, r *http.Request) {
		login, ok := g.logins[bearer(r)]
		if !ok {
			ghMessage(w, http.StatusUnauthorized, "Bad credentials")
			return
		}
		var req struct{ Title, Head, Base, Body string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		defer f.mu.Unlock()
		pr := &fakePullRequest{Number: f.next, Title: req.Title, Body: req.Body, Head: req.Head, Author: login}
		pr.Files = f.headFiles(pr)
		f.pulls[pr.Number] = pr
		f.next++
		writeJSON(w, http.StatusCreated, f.pullJSON(pr))
	})
	mux.HandleFunc("GET "+base+"/pulls", func(w http.ResponseWriter, r *http.Request) {
		head := strings.TrimPrefix(r.URL.Query().Get("head"), org+":")
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []map[string]any{}
		for _, pr := range f.pulls {
			if head == "" || pr.Head == head {
				out = append(out, f.pullJSON(pr))
			}
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("GET "+base+"/pulls/{n}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if pr := f.pull(r); pr != nil {
			writeJSON(w, http.StatusOK, f.pullJSON(pr))
			return
		}
		ghMessage(w, http.StatusNotFound, "Not Found")
	})
	mux.HandleFunc("GET "+base+"/pulls/{n}/files", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		pr := f.pull(r)
		if pr == nil {
			ghMessage(w, http.StatusNotFound, "Not Found")
			return
		}
		var out []map[string]any
		for p := range pr.Files {
			out = append(out, map[string]any{"filename": p})
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("POST "+base+"/pulls/{n}/reviews", func(w http.ResponseWriter, r *http.Request) {
		login := g.logins[bearer(r)]
		var req struct{ Event, Body string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		defer f.mu.Unlock()
		pr := f.pull(r)
		if pr == nil {
			ghMessage(w, http.StatusNotFound, "Not Found")
			return
		}
		pr.Reviews = append(pr.Reviews, fakeReview{User: login, Event: req.Event, Body: req.Body})
		if req.Event == eventApprove && pr.AutoMerge && !f.checksPending[pr.Number] && !f.conflicts(pr) {
			// GitHub merges an auto-merge pull request itself once the review is in.
			f.land(pr, time.Now())
		}
		writeJSON(w, http.StatusOK, map[string]any{kID: len(pr.Reviews), kState: reviewApproved, kHTMLURL: fmt.Sprintf("https://github.com/%s/github/pull/%d#pullrequestreview-%d", org, pr.Number, len(pr.Reviews)), kUser: map[string]any{kLogin: login}})
	})
	mux.HandleFunc("GET "+base+"/pulls/{n}/reviews", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		pr := f.pull(r)
		if pr == nil {
			ghMessage(w, http.StatusNotFound, "Not Found")
			return
		}
		out := []map[string]any{}
		for i, rev := range pr.Reviews {
			state := "COMMENTED"
			if rev.Event == eventApprove {
				state = reviewApproved
			}
			out = append(out, map[string]any{kID: i + 1, kState: state, kUser: map[string]any{kLogin: rev.User}})
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("PUT "+base+"/pulls/{n}/merge", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			CommitTitle string `json:"commit_title"`
			MergeMethod string `json:"merge_method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		defer f.mu.Unlock()
		pr := f.pull(r)
		switch {
		case pr == nil:
			ghMessage(w, http.StatusNotFound, "Not Found")
		case pr.Merged:
			ghMessage(w, http.StatusMethodNotAllowed, "Pull Request is not mergeable")
		case f.conflicts(pr):
			ghMessage(w, http.StatusMethodNotAllowed, "Pull Request is not mergeable")
		case f.checksPending[pr.Number]:
			ghMessage(w, http.StatusMethodNotAllowed, "Required status check \"Repositories YAML\" is expected.")
		case len(pr.Reviews) == 0:
			ghMessage(w, http.StatusMethodNotAllowed, "At least 1 approving review is required by reviewers with write access.")
		case req.MergeMethod != "squash":
			ghMessage(w, http.StatusMethodNotAllowed, "Merge method not allowed")
		default:
			f.land(pr, time.Now())
			pr.MergeTitle = req.CommitTitle
			writeJSON(w, http.StatusOK, map[string]any{"sha": "merged00", "merged": true, kMessage: "Pull Request successfully merged"})
		}
	})
	mux.HandleFunc("POST "+base+"/actions/workflows/{file}/dispatches", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		req["workflow"] = r.PathValue("file")
		req["as"] = g.logins[bearer(r)]
		f.mu.Lock()
		f.dispatches = append(f.dispatches, req)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
}

func (f *fakeTeamFiles) pull(r *http.Request) *fakePullRequest {
	n, _ := strconv.Atoi(r.PathValue("n"))
	return f.pulls[n]
}

func (f *fakeTeamFiles) pullJSON(pr *fakePullRequest) map[string]any {
	conflicting := !pr.Merged && f.conflicts(pr)
	out := map[string]any{"number": pr.Number, "node_id": pullNodeID(pr.Number), "title": pr.Title, "body": pr.Body, kHTMLURL: fmt.Sprintf("https://github.com/%s/github/pull/%d", org, pr.Number),
		"user": map[string]any{kLogin: pr.Author}, "head": map[string]any{kRef: pr.Head, kSHA: f.refs[headsPrefix+pr.Head]}, kCreatedAtREST: time.Now().UTC().Format(time.RFC3339),
		"merged": pr.Merged, "mergeable": !pr.Merged && !conflicting, "mergeable_state": "blocked", kState: "open"}
	if conflicting {
		out["mergeable_state"] = "dirty"
	}
	if pr.Merged {
		out[kState], out["merged_at"], out["merge_commit_sha"] = "closed", pr.MergedAt.UTC().Format(time.RFC3339), pr.MergeSHA
	}
	if pr.Closed {
		out[kState] = "closed"
	}
	if len(pr.Reviews) > 0 && !f.checksPending[pr.Number] && !conflicting {
		out["mergeable_state"] = "clean"
	}
	if pr.AutoMerge {
		out["auto_merge"] = map[string]any{"merge_method": "squash"}
	}
	return out
}

// pullNodeID is a pull request's GraphQL id in the fake.
func pullNodeID(n int) string { return fmt.Sprintf("PR_%d", n) }

// handleAutoMerge answers the enablePullRequestAutoMerge mutation the way
// GitHub does: armed for a pull request that cannot be merged yet, refused
// for one that could be merged right away ("clean status") or is merged.
func (f *fakeTeamFiles) handleAutoMerge(w http.ResponseWriter, body []byte) {
	var req struct {
		Variables struct {
			ID string `json:"id"`
		} `json:"variables"`
	}
	_ = json.Unmarshal(body, &req)
	n, _ := strconv.Atoi(strings.TrimPrefix(req.Variables.ID, "PR_"))
	f.mu.Lock()
	defer f.mu.Unlock()
	pr := f.pulls[n]
	refuse := func(msg string) {
		writeJSON(w, http.StatusOK, map[string]any{kData: nil, "errors": []map[string]any{{kMessage: msg, kType: "UNPROCESSABLE"}}})
	}
	switch {
	case pr == nil:
		refuse("Could not resolve to a node with the global id of '" + req.Variables.ID + "'")
	case pr.Merged:
		refuse("Pull request is in merged status")
	case len(pr.Reviews) > 0 && !f.checksPending[pr.Number]:
		refuse("Pull request is in clean status")
	default:
		pr.AutoMerge = true
		writeJSON(w, http.StatusOK, map[string]any{kData: map[string]any{"enablePullRequestAutoMerge": map[string]any{"pullRequest": map[string]any{"number": n}}}})
	}
}

// merge merges pull request n the way a machine approval does.
func (f *fakeTeamFiles) merge(n int) { f.mergeAt(n, time.Now()) }

// mergeAt merges n at — the clock the poller reads.
func (f *fakeTeamFiles) mergeAt(n int, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.land(f.pulls[n], at)
}

// mergeable is GitHub's mergeable for pull request n.
func (f *fakeTeamFiles) mergeable(n int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	pr := f.pulls[n]
	return !pr.Merged && !f.conflicts(pr)
}

// at is the file at ref.
func (f *fakeTeamFiles) at(ref, p string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.viewAt(ref)[p]
}

// close closes n without a merge.
func (f *fakeTeamFiles) close(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulls[n].Closed = true
}

// setChecksPending marks n's checks as still running (or done).
func (f *fakeTeamFiles) setChecksPending(n int, pending bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.checksPending == nil {
		f.checksPending = map[int]bool{}
	}
	f.checksPending[n] = pending
}

// disarmAutoMerge models a pull request opened before auto-merge was armed
// at opening.
func (f *fakeTeamFiles) disarmAutoMerge(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulls[n].AutoMerge = false
}

// fakeGateway is klaus-gateway's team-review endpoint: it admits one bearer
// and records what was posted.
type fakeGateway struct {
	mu      sync.Mutex
	token   string
	asks    []map[string]any
	notices []map[string]any
}

func (g *fakeGateway) handler() http.Handler {
	mux := http.NewServeMux()
	post := func(kind string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if bearer(r) != g.token {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			g.mu.Lock()
			defer g.mu.Unlock()
			out := map[string]any{kChannel: body[kChannel], "ts": "1726512345.000100"}
			if kind == "review" {
				g.asks = append(g.asks, body)
				out[kID] = fmt.Sprintf("review-%d", len(g.asks))
			} else {
				g.notices = append(g.notices, body)
			}
			writeJSON(w, http.StatusCreated, out)
		}
	}
	mux.HandleFunc("POST /reviews", post("review"))
	mux.HandleFunc("POST /notices", post("notice"))
	return mux
}

func (g *fakeGateway) posted() (asks, notices []map[string]any) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]map[string]any{}, g.asks...), append([]map[string]any{}, g.notices...)
}
