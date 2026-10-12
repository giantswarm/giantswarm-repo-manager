package e2e

import (
	"context"
	"testing"

	"github.com/giantswarm/devctl/v8/pkg/reposetup/reconcile"

	"github.com/giantswarm/giantswarm-repo-manager/internal/collect"
)

// The engine's labels step, run by the inventory's checks, reports a
// repository without its owning team's label team/<team> as drift — what
// `devctl repo status` shows and Align now repairs — and one that carries it
// as ok.
func TestEngineReportsAMissingOwningTeamLabel(t *testing.T) {
	st := newStack(t)
	ctx := context.Background()
	repo := st.ghs.repos.add(repoPresent, alice)
	entry := st.acceptedEntry(t, repoPresent)
	engine := collect.NewEngine(st.app.Reader(), 0)
	label := reconcile.TeamLabel(team)

	for _, tc := range []struct {
		name    string
		present bool
		verdict reconcile.Verdict
		summary string
		changes []string
	}{
		{name: "missing", verdict: reconcile.VerdictDrift, changes: []string{"create the label " + label + " (#" + reconcile.DefaultLabelColor + ")"}},
		{name: "present", present: true, verdict: reconcile.VerdictOK, summary: "has " + label},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st.ghs.repos.mu.Lock()
			repo.labels[label] = tc.present
			st.ghs.repos.mu.Unlock()
			res, err := engine.Check(ctx, reconcile.Request{Owner: org, Team: team, Entry: entry, Steps: []reconcile.Step{reconcile.StepLabels}})
			if err != nil {
				t.Fatalf("check: %v", err)
			}
			s := res.Step(reconcile.StepLabels)
			if s == nil || s.Verdict != tc.verdict || s.Summary != tc.summary || len(s.Changes) != len(tc.changes) || res.Converged != tc.present {
				t.Fatalf("labels step: %+v (converged %v), want %s %q %q", s, res.Converged, tc.verdict, tc.summary, tc.changes)
			}
			for i := range tc.changes {
				if s.Changes[i] != tc.changes[i] {
					t.Errorf("change %d: %q, want %q", i, s.Changes[i], tc.changes[i])
				}
			}
		})
	}
}
