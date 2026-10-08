package e2e

import (
	"strings"
	"testing"

	"github.com/giantswarm/devctl/v8/pkg/reposetup"

	"github.com/giantswarm/giantswarm-repo-manager/internal/tools"
)

// TestWriteDryRunsHoldEntriesToTheSchemaTheTeamFileDeclares: every write's
// dry run validates the way the sweep does once the entry merges — with the
// repositories schema beside the team files, not the engine's embedded copy.
// An entry using a field of a devctl release newer than the engine's is
// refused while the team files declare no schema (the embedded copy stands
// in) and accepted once they declare one that knows it, by
// validate_repository, update_repository, transfer_repository and
// adopt_repository alike; a malformed value of that field stays refused.
func TestWriteDryRunsHoldEntriesToTheSchemaTheTeamFileDeclares(t *testing.T) {
	st := newStack(t)
	c := st.as(t, aliceToken)
	st.ghs.repos.add(repoStray, alice)
	// The present entry carries the newer field, as an edit merged after a
	// devctl release declared it.
	st.ghs.files.commit(map[string][]byte{"repositories/" + team + ".yaml": []byte(strings.Replace(teamFile,
		"- name: "+repoPresent+"\n", "- name: "+repoPresent+"\n  "+futureField+": true\n", 1))})
	present := map[string]any{"name": repoPresent, kComponentType: kService, futureField: true,
		kGen: map[string]any{kLanguage: kGo, kFlavours: []any{kApp}, kCI: map[string]any{kGenerate: true, kChartName: repoPresent}}}
	stray := map[string]any{kComponentType: kService, futureField: true}

	type verdict struct {
		accepted bool
		problems []reposetup.Problem
		schema   reposetup.SchemaOrigin
	}
	dryRuns := map[string]func(value any) verdict{
		tools.ToolValidateRepository: func(value any) verdict {
			var v tools.Validation
			st.callJSON(t, c, tools.ToolValidateRepository, map[string]any{argTeam: team, argEntry: withField(newEntry, futureField, value)}, &v)
			var problems []reposetup.Problem
			for _, e := range v.Entries {
				problems = append(problems, e.Problems...)
			}
			return verdict{v.Accepted, problems, v.Schema}
		},
		tools.ToolUpdateRepository: func(value any) verdict {
			var pl tools.Plan
			st.callJSON(t, c, tools.ToolUpdateRepository, map[string]any{argDryRun: true, kRepository: repoPresent, argEntry: withField(present, futureField, value)}, &pl)
			return verdict{pl.Accepted, pl.Problems, ""}
		},
		tools.ToolAdoptRepository: func(value any) verdict {
			var pl tools.Plan
			st.callJSON(t, c, tools.ToolAdoptRepository, map[string]any{argDryRun: true, kRepository: repoStray, argTeam: team, argEntry: withField(stray, futureField, value)}, &pl)
			return verdict{pl.Accepted, pl.Problems, ""}
		},
		// The transfer moves the entry as the team file carries it.
		tools.ToolTransferRepository: func(any) verdict {
			var pl tools.Plan
			st.callJSON(t, c, tools.ToolTransferRepository, map[string]any{argDryRun: true, kRepository: repoPresent, argToTeam: teamPlaneteers}, &pl)
			return verdict{pl.Accepted, pl.Problems, ""}
		},
	}

	for tool, dryRun := range dryRuns {
		if v := dryRun(true); v.accepted || !hasProblem(v.problems, futureField) {
			t.Errorf("%s without a declared schema: the embedded copy should refuse %s: %+v", tool, futureField, v)
		}
	}

	st.ghs.files.commit(map[string][]byte{reposetup.SchemaPath: []byte(schemaWith(t, futureField))})
	for tool, dryRun := range dryRuns {
		if v := dryRun(true); !v.accepted || len(v.problems) != 0 {
			t.Errorf("%s with the declared schema should accept %s: %+v", tool, futureField, v)
		} else if tool == tools.ToolValidateRepository && v.schema != reposetup.SchemaOriginGitHub {
			t.Errorf("%s validated with %q, want the declared schema %q", tool, v.schema, reposetup.SchemaOriginGitHub)
		}
		if tool == tools.ToolTransferRepository {
			continue
		}
		if v := dryRun("yes"); v.accepted || !hasProblem(v.problems, futureField) {
			t.Errorf("%s with the declared schema should refuse a malformed %s: %+v", tool, futureField, v)
		}
	}
}
