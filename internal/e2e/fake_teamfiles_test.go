package e2e

import (
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
	kType          = "type"
	kMessage       = "message"
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
	kChannel       = "channel"
	kVisibility    = "visibility"
	kFile          = "file"
	kID            = "id"
	modeApply      = "apply"
	bob            = "bob"
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
	MergeTitle string
}

type fakeReview struct{ User, Event, Body string }

type fakeTeamFiles struct {
	mu         sync.Mutex
	files      map[string][]byte // path → content at main
	refs       map[string]string // heads/<branch> → sha
	trees      map[string]map[string][]byte
	commits    map[string]string // sha → tree sha
	pulls      map[int]*fakePullRequest
	next       int
	dispatches []map[string]any
	// denied are the logins whose credential does not reach the repository.
	denied map[string]bool
	// checksPending marks pull requests whose checks still run: GitHub
	// refuses to merge them (405) and lets auto-merge be armed.
	checksPending map[int]bool
}

func newFakeTeamFiles() *fakeTeamFiles {
	f := &fakeTeamFiles{
		files: map[string][]byte{
			"repositories/" + team + ".yaml":               []byte(teamFile),
			"repositories/" + teamPlaneteers + ".yaml":     []byte(planeteersFile),
			"repository-setup/" + team + ".yaml":           []byte("alignOptIn: true\nslackChannel: " + bumblebeeChannel + "\nstandupChannel: " + bumblebeeStandup + "\n"),
			"repository-setup/" + teamPlaneteers + ".yaml": []byte("alignOptIn: false\nslackChannel: " + teamPlaneteers + "\nstandupChannel: standup-planeteers\n"),
		},
		refs: map[string]string{"heads/" + mainBranch: "base000"}, trees: map[string]map[string][]byte{}, commits: map[string]string{}, pulls: map[int]*fakePullRequest{}, next: 4711,
	}
	return f
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
		if c, ok := f.files[p]; ok {
			writeJSON(w, http.StatusOK, map[string]any{kType: kFile, kName: path.Base(p), kPath: p, kSHA: "blob-" + p, "encoding": "base64", "content": base64.StdEncoding.EncodeToString(c)})
			return
		}
		var dir []map[string]any
		for name := range f.files {
			if path.Dir(name) == p {
				dir = append(dir, map[string]any{kType: kFile, kName: path.Base(name), kPath: name})
			}
		}
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
			Tree string `json:"tree"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		defer f.mu.Unlock()
		sha := fmt.Sprintf("commit%d", len(f.commits)+1)
		f.commits[sha] = req.Tree
		writeJSON(w, http.StatusCreated, map[string]any{kSHA: sha})
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
		pr := &fakePullRequest{Number: f.next, Title: req.Title, Body: req.Body, Head: req.Head, Author: login, Files: f.trees[f.commits[f.refs["heads/"+req.Head]]]}
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
		if req.Event == "APPROVE" && pr.AutoMerge && !f.checksPending[pr.Number] {
			// GitHub merges an auto-merge pull request itself once the review is in.
			pr.Merged = true
		}
		writeJSON(w, http.StatusOK, map[string]any{kID: len(pr.Reviews), kState: "APPROVED", kHTMLURL: fmt.Sprintf("https://github.com/%s/github/pull/%d#pullrequestreview-%d", org, pr.Number, len(pr.Reviews)), "user": map[string]any{kLogin: login}})
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
		case f.checksPending[pr.Number]:
			ghMessage(w, http.StatusMethodNotAllowed, "Required status check \"Repositories YAML\" is expected.")
		case len(pr.Reviews) == 0:
			ghMessage(w, http.StatusMethodNotAllowed, "At least 1 approving review is required by reviewers with write access.")
		case req.MergeMethod != "squash":
			ghMessage(w, http.StatusMethodNotAllowed, "Merge method not allowed")
		default:
			pr.Merged, pr.MergeTitle = true, req.CommitTitle
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
	out := map[string]any{"number": pr.Number, "node_id": pullNodeID(pr.Number), "title": pr.Title, "body": pr.Body, kHTMLURL: fmt.Sprintf("https://github.com/%s/github/pull/%d", org, pr.Number),
		"user": map[string]any{kLogin: pr.Author}, "head": map[string]any{kRef: pr.Head}, "created_at": time.Now().UTC().Format(time.RFC3339),
		"merged": pr.Merged, "mergeable": !pr.Merged, "mergeable_state": "blocked"}
	if len(pr.Reviews) > 0 && !f.checksPending[pr.Number] {
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
		writeJSON(w, http.StatusOK, map[string]any{"data": nil, "errors": []map[string]any{{kMessage: msg, kType: "UNPROCESSABLE"}}})
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
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"enablePullRequestAutoMerge": map[string]any{"pullRequest": map[string]any{"number": n}}}})
	}
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
