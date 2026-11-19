package collect

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// TestFetchReleasesReadsTwoPagesOfFifty: one pass reads the 100 most
// recently pushed repositories as two queries of 50, the second after the
// first's cursor — GitHub stops a query after about ten seconds, and 100
// repositories in one take about that long — and no third query however
// many repositories follow; a first page without a next page ends the read;
// a second page that fails fails the pass, with none of the first page's
// repositories.
func TestFetchReleasesReadsTwoPagesOfFifty(t *testing.T) {
	cases := []struct {
		name      string
		repos     int
		wantNodes int
		wantCalls []string // after of each query, "" for none
		// failSecond answers the second query with a GraphQL error and no
		// data.
		failSecond bool
	}{
		{name: "an org of many repositories: two pages, then stop", repos: 1858, wantNodes: 100, wantCalls: []string{"", "c50"}},
		{name: "an org of 30 repositories: one page", repos: 30, wantNodes: 30, wantCalls: []string{""}},
		{name: "the second page fails: the pass fails", repos: 1858, wantCalls: []string{"", "c50"}, failSecond: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Variables struct {
						First int     `json:"first"`
						After *string `json:"after"`
					} `json:"variables"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("request: %v", err)
				}
				start := 0
				after := ""
				if req.Variables.After != nil {
					after = *req.Variables.After
					start, _ = strconv.Atoi(after[1:])
				}
				calls = append(calls, after)
				if req.Variables.First != 50 {
					t.Errorf("first=%d, want 50", req.Variables.First)
				}
				if tc.failSecond && after != "" {
					_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]any{{"message": "Something went wrong while executing your query."}}})
					return
				}
				end := min(start+req.Variables.First, tc.repos)
				nodes := []map[string]any{}
				for i := start; i < end; i++ {
					nodes = append(nodes, map[string]any{kName: "r" + strconv.Itoa(i)})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"organization": map[string]any{"repositories": map[string]any{
					"pageInfo": map[string]any{"hasNextPage": end < tc.repos, "endCursor": "c" + strconv.Itoa(end)},
					"nodes":    nodes,
				}}}})
			}))
			defer srv.Close()
			c := &Collector{opts: Options{Org: "giantswarm"}, gql: newGraphQL(srv.URL, srv.Client(), 0)}
			nodes, err := c.fetchReleases(context.Background())
			if tc.failSecond {
				if err == nil || nodes != nil {
					t.Errorf("a failed second page: err=%v, %d nodes; want an error and none", err, len(nodes))
				}
				if len(calls) != 2 {
					t.Errorf("queries after %q, want two", calls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(nodes) != tc.wantNodes || nodes[0].Name != "r0" || nodes[len(nodes)-1].Name != "r"+strconv.Itoa(tc.wantNodes-1) {
				t.Errorf("nodes: %d, first %s, last %s; want %d", len(nodes), nodes[0].Name, nodes[len(nodes)-1].Name, tc.wantNodes)
			}
			if len(calls) != len(tc.wantCalls) {
				t.Fatalf("queries after %q, want %q", calls, tc.wantCalls)
			}
			for i := range calls {
				if calls[i] != tc.wantCalls[i] {
					t.Errorf("queries after %q, want %q", calls, tc.wantCalls)
				}
			}
		})
	}
}
