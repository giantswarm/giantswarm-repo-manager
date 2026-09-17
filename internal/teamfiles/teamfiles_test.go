package teamfiles

import (
	"strings"
	"testing"
)

// TestParsePolicy: both channels are required — a file without one is
// refused naming the field; nothing stands in for a missing channel.
func TestParsePolicy(t *testing.T) {
	const team, path = "team-a", "repository-setup/team-a.yaml"
	cases := []struct {
		name, yaml string
		want       Policy
		wantErr    string
	}{
		{name: "both channels", yaml: "repairOptIn: true\nslackChannel: team-a\nstandupChannel: standup-a\n",
			want: Policy{Team: team, SlackChannel: team, StandupChannel: "standup-a", RepairOptIn: true}},
		{name: "channel IDs as written", yaml: "slackChannel: C0TEAM\nstandupChannel: C0STANDUP\n",
			want: Policy{Team: team, SlackChannel: "C0TEAM", StandupChannel: "C0STANDUP"}},
		{name: "no standupChannel", yaml: "slackChannel: team-a\n", wantErr: path + ": standupChannel is empty"},
		{name: "no slackChannel", yaml: "standupChannel: standup-a\n", wantErr: path + ": slackChannel is empty"},
		{name: "empty file", yaml: "", wantErr: "slackChannel is empty"},
		{name: "not yaml", yaml: "slackChannel: [", wantErr: path + ":"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ParsePolicy(team, path, []byte(tc.yaml))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want an error with %q, got %v (%+v)", tc.wantErr, err, p)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if *p != tc.want {
				t.Errorf("got %+v, want %+v", *p, tc.want)
			}
		})
	}
}
