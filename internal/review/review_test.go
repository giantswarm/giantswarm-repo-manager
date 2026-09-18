package review

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	teamChannel   = "team-x"
	teamChannelID = "C0TEAMX00001"
	standupID     = "C0STANDUPX01"
	debugChannel  = "debug-x"
	debugID       = "C0DEBUGX0001"
	token         = "sa-token"
	hello         = "hello"
	pathReviews   = "/reviews"
	pathNotices   = "/notices"
	approveTool   = "x_approve"
)

// posted is one message the fake gateway took: the path and the body.
type posted struct {
	Path string
	Body map[string]any
}

// gateway is klaus-gateway's team-review endpoint: it admits one bearer and
// records what was posted.
func gateway(t *testing.T) (*Client, *[]posted, func(Config) (*Client, error)) {
	t.Helper()
	var got []posted
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		got = append(got, posted{Path: r.URL.Path, Body: body})
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Posted{ID: "review-1", Channel: body["channel"].(string), TS: "1.2"})
	}))
	t.Cleanup(srv.Close)
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	channels := map[string]string{teamChannel: teamChannelID, "standup-x": standupID, debugChannel: debugID}
	with := func(cfg Config) (*Client, error) {
		cfg.BaseURL, cfg.TokenFile, cfg.Channels = srv.URL, tokenFile, channels
		return New(cfg)
	}
	c, err := with(Config{})
	if err != nil {
		t.Fatal(err)
	}
	return c, &got, with
}

func TestNewWithoutURLIsOff(t *testing.T) {
	c, err := New(Config{})
	if c != nil || err != nil {
		t.Fatalf("New: %v %v", c, err)
	}
	if c.DebugChannel() != "" {
		t.Errorf("nil client names a debug channel: %q", c.DebugChannel())
	}
	if _, err := c.Review(context.Background(), Ask{}); err != ErrNotConfigured {
		t.Errorf("Review off: %v", err)
	}
}

func TestChannelID(t *testing.T) {
	c, _, _ := gateway(t)
	for in, want := range map[string]string{teamChannelID: teamChannelID, teamChannel: teamChannelID, "#" + teamChannel: teamChannelID, " standup-x ": standupID} {
		if got, err := c.ChannelID(in); err != nil || got != want {
			t.Errorf("ChannelID(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := c.ChannelID("team-unknown"); err == nil || !strings.Contains(err.Error(), "reviews.channels") {
		t.Errorf("unknown name: %v", err)
	}
}

func TestNewRefusesADebugChannelWithoutID(t *testing.T) {
	_, _, with := gateway(t)
	if _, err := with(Config{DebugChannel: "nowhere"}); err == nil || !strings.Contains(err.Error(), `debug channel: slack channel "nowhere" has no ID`) {
		t.Fatalf("New: %v", err)
	}
}

// Without a debug channel the messages go where the callers say, as they say it.
func TestDeliveryUnchangedWithoutDebugChannel(t *testing.T) {
	c, got, _ := gateway(t)
	ctx := context.Background()
	if p, err := c.Review(ctx, Ask{Team: teamChannel, Channel: teamChannelID, Text: hello, Approve: Approve{Tool: approveTool}}); err != nil || p.ID != "review-1" || p.Channel != teamChannelID {
		t.Fatalf("Review: %+v %v", p, err)
	}
	if _, err := c.Notify(ctx, Notice{Team: teamChannel, Channel: standupID, Text: hello}); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 2 || (*got)[0].Path != pathReviews || (*got)[0].Body["channel"] != teamChannelID || (*got)[0].Body["text"] != hello ||
		(*got)[1].Path != pathNotices || (*got)[1].Body["channel"] != standupID || (*got)[1].Body["text"] != hello {
		t.Errorf("posted: %+v", *got)
	}
}

// With one, every ask and notice lands there, the text closing with the
// channel the policy file chose: its name when Channels maps it, else the
// ID as the file carries it. Everything else on the wire is as given.
func TestDebugChannelReceivesEveryAskAndNotice(t *testing.T) {
	_, got, with := gateway(t)
	c, err := with(Config{DebugChannel: "#" + debugChannel})
	if err != nil {
		t.Fatal(err)
	}
	if c.DebugChannel() != "#"+debugChannel {
		t.Errorf("DebugChannel() = %q", c.DebugChannel())
	}
	ctx := context.Background()
	if p, err := c.Review(ctx, Ask{Team: teamChannel, Channel: teamChannelID, Text: hello, Link: "https://example.invalid/pr/1", Approve: Approve{Tool: approveTool}}); err != nil || p.Channel != debugID {
		t.Fatalf("Review: %+v %v", p, err)
	}
	if _, err := c.Notify(ctx, Notice{Team: teamChannel, Channel: "C0UNMAPPED01", Text: hello}); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 2 {
		t.Fatalf("posted: %+v", *got)
	}
	ask, notice := (*got)[0], (*got)[1]
	if ask.Path != pathReviews || ask.Body["channel"] != debugID || ask.Body["text"] != hello+" (for #"+teamChannel+")" ||
		ask.Body["team"] != teamChannel || ask.Body["link"] != "https://example.invalid/pr/1" || ask.Body["approve"].(map[string]any)["tool"] != approveTool {
		t.Errorf("ask: %+v", ask)
	}
	if notice.Path != pathNotices || notice.Body["channel"] != debugID || notice.Body["text"] != hello+" (for C0UNMAPPED01)" || notice.Body["team"] != teamChannel {
		t.Errorf("notice: %+v", notice)
	}
}
