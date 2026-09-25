package teamfiles

import (
	"strings"
	"testing"
)

// TestParseChannels: the channel file is read strictly — notices is
// required, asks optional, every channel an ID and a name, an unknown key
// refused naming it; nothing stands in for a missing channel.
func TestParseChannels(t *testing.T) {
	const team, path = "team-a", "teams/team-a.yaml"
	asks := &Channel{ID: "C0TEAMA0001", Name: "team-a"}
	notices := Channel{ID: "C0STANDA001", Name: "standup-a"}
	const both = "asks:\n  id: C0TEAMA0001\n  name: team-a\nnotices:\n  id: C0STANDA001\n  name: standup-a\n"
	cases := []struct {
		name, yaml string
		want       Channels
		wantErr    string
	}{
		{name: "asks and notices", yaml: "# the team's channels\n" + both,
			want: Channels{Team: team, Asks: asks, Notices: notices}},
		{name: "notices only", yaml: "notices:\n  id: C0STANDA001\n  name: standup-a\n",
			want: Channels{Team: team, Notices: notices}},
		{name: "no notices", yaml: "asks:\n  id: C0TEAMA0001\n  name: team-a\n", wantErr: path + ": notices is missing"},
		{name: "empty file", yaml: "", wantErr: path + ": notices is missing"},
		{name: "an unknown key is refused", yaml: both + "someFutureKey: true\n", wantErr: "someFutureKey"},
		{name: "a channel without a name", yaml: "notices:\n  id: C0STANDA001\n", wantErr: path + `: notices.name "" is not a Slack channel name`},
		{name: "a channel without an ID", yaml: "notices:\n  name: standup-a\n", wantErr: path + `: notices.id "" is not a Slack channel ID`},
		{name: "a name for an ID", yaml: "asks:\n  id: team-a\n  name: team-a\nnotices:\n  id: C0STANDA001\n  name: standup-a\n", wantErr: `asks.id "team-a" is not a Slack channel ID`},
		{name: "a name with the #", yaml: "notices:\n  id: C0STANDA001\n  name: '#standup-a'\n", wantErr: `notices.name "#standup-a"`},
		{name: "a channel as a bare ID", yaml: "notices: C0STANDA001\n", wantErr: path + ":"},
		{name: "two documents", yaml: both + "---\n" + both, wantErr: path + ": one YAML document expected"},
		{name: "not yaml", yaml: "notices: [", wantErr: path + ":"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := ParseChannels(team, path, []byte(tc.yaml))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want an error with %q, got %v (%+v)", tc.wantErr, err, c)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.Team != tc.want.Team || c.Notices != tc.want.Notices || (c.Asks == nil) != (tc.want.Asks == nil) || (c.Asks != nil && *c.Asks != *tc.want.Asks) {
				t.Errorf("got %+v (asks %+v), want %+v (asks %+v)", *c, c.Asks, tc.want, tc.want.Asks)
			}
		})
	}
}
