package tools

import "testing"

// TestPendingRow: watch_repository promises devctl's automatic set-up only
// for a row devctl v8.164.0's pending pass keeps: a team, on GitHub, not
// archived, lifecycle production or unset.
func TestPendingRow(t *testing.T) {
	cases := []struct {
		name string
		row  Row
		want bool
	}{
		{"production", Row{Team: testTeam, Lifecycle: lifecycleProduction}, true},
		{"lifecycle unset", Row{Team: testTeam}, true},
		{"deprecated", Row{Team: testTeam, Lifecycle: LifecycleDeprecated}, false},
		{"archived on GitHub", Row{Team: testTeam, Archived: true}, false},
		{"gone from GitHub", Row{Team: testTeam, Gone: true}, false},
		{"no team", Row{}, false},
	}
	for _, tc := range cases {
		if got := pendingRow(tc.row); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}
