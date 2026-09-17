package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"
	"github.com/google/go-github/v92/github"
)

// The fake org's repositories as the REST surface the engine's create and
// scaffold steps write and the App's name checks read: an in-memory state
// behind the endpoints, seeded by a test and inspected afterwards.

const (
	roleAdmin  = "admin"
	roleMember = "member"
	readmeFile = "README.md"
)

// fakeRepos are the org's repositories.
type fakeRepos struct {
	mu    sync.Mutex
	repos map[string]*fakeRepo
}

// fakeRepo is one repository's state: who administers it, the files at the
// head of main and the git data the scaffold push creates.
type fakeRepo struct {
	name, description string
	private           bool
	admins            map[string]bool
	empty             bool
	files             map[string]string
	blobs             map[string][]byte
	trees             map[string][]*github.TreeEntry
	commits           map[string]string // sha → tree sha
	head              string
	seq               int
}

func newFakeRepos() *fakeRepos { return &fakeRepos{repos: map[string]*fakeRepo{}} }

// add seeds a repository administered by admin, holding only the README of
// its creation (the state after a create step whose scaffold never landed).
func (f *fakeRepos) add(name, admin string) *fakeRepo {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := &fakeRepo{name: name, admins: map[string]bool{admin: true}, files: map[string]string{readmeFile: "# " + name + "\n"},
		blobs: map[string][]byte{}, trees: map[string][]*github.TreeEntry{}, commits: map[string]string{}}
	r.head = r.next("c")
	f.repos[name] = r
	return r
}

// get is the repository by name, nil when it does not exist.
func (f *fakeRepos) get(name string) *fakeRepo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.repos[name]
}

func (r *fakeRepo) next(prefix string) string {
	r.seq++
	return fmt.Sprintf("%s%04d", prefix, r.seq)
}

func (r *fakeRepo) toGitHub(asAdmin bool) map[string]any {
	return map[string]any{
		kName: r.name, "full_name": org + "/" + r.name, kHTMLURL: "https://github.com/" + org + "/" + r.name,
		"default_branch": mainBranch, kPrivate: r.private, kDescription: r.description,
		"owner": map[string]any{kLogin: org}, "permissions": map[string]any{roleAdmin: asAdmin, "push": asAdmin, "pull": true},
	}
}

// register serves the store; g answers who the bearer is and the org roles.
func (f *fakeRepos) register(mux *http.ServeMux, g *fakeGitHub) {
	withRepo := func(h func(w http.ResponseWriter, r *http.Request, repo *fakeRepo)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			defer f.mu.Unlock()
			repo, ok := f.repos[r.PathValue("repo")]
			if !ok || r.PathValue("owner") != org {
				ghMessage(w, http.StatusNotFound, "Not Found")
				return
			}
			h(w, r, repo)
		}
	}
	mux.HandleFunc("GET /api/v3/repos/{owner}/{repo}", withRepo(func(w http.ResponseWriter, r *http.Request, repo *fakeRepo) {
		writeJSON(w, http.StatusOK, repo.toGitHub(repo.admins[g.logins[bearer(r)]]))
	}))
	mux.HandleFunc("GET /api/v3/user/memberships/orgs/{org}", func(w http.ResponseWriter, r *http.Request) {
		role, ok := g.roles[g.logins[bearer(r)]]
		if !ok || r.PathValue("org") != org {
			ghMessage(w, http.StatusNotFound, "Not Found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{kState: "active", "role": role})
	})
	mux.HandleFunc("POST /api/v3/orgs/{org}/repos", func(w http.ResponseWriter, r *http.Request) {
		login := g.logins[bearer(r)]
		if g.roles[login] != roleAdmin {
			ghMessage(w, http.StatusForbidden, "Resource not accessible by integration")
			return
		}
		var in github.Repository
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, taken := f.repos[in.GetName()]; taken {
			ghMessage(w, http.StatusUnprocessableEntity, "name already exists on this account")
			return
		}
		repo := &fakeRepo{name: in.GetName(), description: in.GetDescription(), private: in.GetPrivate(), admins: map[string]bool{login: true},
			empty: !in.GetAutoInit(), files: map[string]string{}, blobs: map[string][]byte{}, trees: map[string][]*github.TreeEntry{}, commits: map[string]string{}}
		if in.GetAutoInit() {
			repo.files[readmeFile] = "# " + repo.name + "\n"
			repo.head = repo.next("c")
		}
		f.repos[repo.name] = repo
		writeJSON(w, http.StatusCreated, repo.toGitHub(true))
	})
	mux.HandleFunc("GET /api/v3/repos/{owner}/{repo}/commits", withRepo(func(w http.ResponseWriter, _ *http.Request, repo *fakeRepo) {
		if repo.empty {
			ghMessage(w, http.StatusConflict, "Git Repository is empty.")
			return
		}
		writeJSON(w, http.StatusOK, []map[string]any{{kSHA: repo.head}})
	}))
	mux.HandleFunc("GET /api/v3/repos/{owner}/{repo}/contents/{path...}", withRepo(func(w http.ResponseWriter, r *http.Request, repo *fakeRepo) {
		if repo.empty || strings.Trim(r.PathValue("path"), "/") != "" {
			ghMessage(w, http.StatusNotFound, "Not Found")
			return
		}
		var listing []map[string]string
		for p := range repo.files {
			listing = append(listing, map[string]string{kType: "file", kName: p, "path": p})
		}
		sort.Slice(listing, func(i, j int) bool { return listing[i][kName] < listing[j][kName] })
		writeJSON(w, http.StatusOK, listing)
	}))
	mux.HandleFunc("PUT /api/v3/repos/{owner}/{repo}/contents/{path...}", withRepo(func(w http.ResponseWriter, r *http.Request, repo *fakeRepo) {
		var in struct{ Content string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		data, _ := base64.StdEncoding.DecodeString(in.Content)
		repo.files[r.PathValue("path")] = string(data)
		repo.empty = false
		repo.head = repo.next("c")
		writeJSON(w, http.StatusCreated, map[string]any{"content": map[string]any{"path": r.PathValue("path")}, "commit": map[string]any{kSHA: repo.head}})
	}))
	mux.HandleFunc("POST /api/v3/repos/{owner}/{repo}/git/blobs", withRepo(func(w http.ResponseWriter, r *http.Request, repo *fakeRepo) {
		var in github.Blob
		_ = json.NewDecoder(r.Body).Decode(&in)
		data, _ := base64.StdEncoding.DecodeString(in.GetContent())
		sha := repo.next("b")
		repo.blobs[sha] = data
		writeJSON(w, http.StatusCreated, map[string]any{kSHA: sha})
	}))
	mux.HandleFunc("POST /api/v3/repos/{owner}/{repo}/git/trees", withRepo(func(w http.ResponseWriter, r *http.Request, repo *fakeRepo) {
		if repo.empty {
			ghMessage(w, http.StatusConflict, "Git Repository is empty.")
			return
		}
		var in struct {
			Tree []*github.TreeEntry `json:"tree"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		sha := repo.next("t")
		repo.trees[sha] = in.Tree
		writeJSON(w, http.StatusCreated, map[string]any{kSHA: sha, "tree": in.Tree})
	}))
	mux.HandleFunc("POST /api/v3/repos/{owner}/{repo}/git/commits", withRepo(func(w http.ResponseWriter, r *http.Request, repo *fakeRepo) {
		var in struct{ Tree string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		sha := repo.next("c")
		repo.commits[sha] = in.Tree
		writeJSON(w, http.StatusCreated, map[string]any{kSHA: sha})
	}))
	mux.HandleFunc("PATCH /api/v3/repos/{owner}/{repo}/git/refs/{ref...}", withRepo(func(w http.ResponseWriter, r *http.Request, repo *fakeRepo) {
		var in github.UpdateRef
		_ = json.NewDecoder(r.Body).Decode(&in)
		tree, ok := repo.commits[in.SHA]
		if !ok {
			ghMessage(w, http.StatusUnprocessableEntity, "unknown commit "+in.SHA)
			return
		}
		files := map[string]string{}
		for _, e := range repo.trees[tree] {
			switch {
			case e.Content != nil:
				files[e.GetPath()] = e.GetContent()
			case e.SHA != nil:
				files[e.GetPath()] = string(repo.blobs[e.GetSHA()])
			}
		}
		repo.files, repo.empty, repo.head = files, false, in.SHA
		writeJSON(w, http.StatusOK, map[string]any{kRef: "refs/" + r.PathValue(kRef), kObject: map[string]any{kSHA: in.SHA}})
	}))
}

// scaffoldFiles is what the fake renderer writes: the engine's renderer is
// devctl's, tested there; the stack proves the push and the order.
var scaffoldFiles = map[string]string{
	readmeFile:   "# shiny-service\n",
	"CODEOWNERS": reposetup.Codeowners(team),
	"Makefile":   "include Makefile.*.mk\n",
}

// fakeScaffold stands in for the engine's renderer.
type fakeScaffold struct{}

func (fakeScaffold) Render(_ context.Context, req reposetup.RenderRequest) (*reposetup.Scaffold, error) {
	files := make([]string, 0, len(scaffoldFiles))
	for p, c := range scaffoldFiles {
		if err := os.WriteFile(filepath.Join(req.Dir, p), []byte(c), 0o600); err != nil {
			return nil, err
		}
		files = append(files, p)
	}
	sort.Strings(files)
	return &reposetup.Scaffold{Dir: req.Dir, Template: req.Entry.Template, Files: files}, nil
}
