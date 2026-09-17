// Command giantswarm-repo-manager is Giant Swarm's repository set-up service:
// an MCP server behind muster that lists, validates, creates and reconciles
// the giantswarm org's repositories as the person calling it, and keeps the
// inventory of every repository of the org.
//
// Every flag can also be set through the environment variable named next to
// it; flags win over the environment. Optional components (the GitHub App,
// the inventory store) that are not configured leave the server running and
// are reported as missing by get_info.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/giantswarm/devctl/v8/pkg/circleciclient"

	"github.com/giantswarm/giantswarm-repo-manager/internal/collect"
	"github.com/giantswarm/giantswarm-repo-manager/internal/gh"
	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
	"github.com/giantswarm/giantswarm-repo-manager/internal/review"
	"github.com/giantswarm/giantswarm-repo-manager/internal/server"
	"github.com/giantswarm/giantswarm-repo-manager/internal/teamfiles"
	"github.com/giantswarm/giantswarm-repo-manager/internal/tools"
)

type options struct {
	listen, mcpPath string

	valkeyAddr, org, internalToken string
	sweepInterval, connectTimeout  time.Duration
	sweepEngineChecks, sweepOnce   bool
	sweepConcurrency, staleDays    int
	graphqlBudgetFloor             int

	githubAPIURL, githubAppPrivateKeyFile, githubToken string
	githubAppID, githubAppInstallationID               int64

	circleciToken string

	teamFilesRepository, teamFilesRef             string
	reviewsURL, reviewsTokenFile, reviewsChannels string

	oauthEnabled                           bool
	oauthBaseURL, oauthAuthorizationServer string
}

func parseFlags(args []string) (*options, error) {
	o := &options{}
	f := flag.NewFlagSet("giantswarm-repo-manager", flag.ContinueOnError)
	f.StringVar(&o.listen, "listen", envOr("LISTEN", ":8080"), "Listen address (LISTEN)")
	f.StringVar(&o.mcpPath, "mcp-path", envOr("MCP_PATH", "/mcp"), "MCP endpoint path (MCP_PATH)")
	f.StringVar(&o.valkeyAddr, "valkey-addr", envOr("VALKEY_ADDR", ""), "host:port of the Valkey the inventory lives in (VALKEY_ADDR)")
	f.DurationVar(&o.connectTimeout, "inventory-connect-timeout", envDuration("INVENTORY_CONNECT_TIMEOUT", 5*time.Minute), "How long the start waits for the inventory store, retrying with backoff, before the server gives up and exits; it serves meanwhile, not ready. 0 waits for ever (INVENTORY_CONNECT_TIMEOUT)")
	f.StringVar(&o.org, "org", envOr("INVENTORY_ORG", "giantswarm"), "The GitHub organization the inventory covers (INVENTORY_ORG)")
	f.DurationVar(&o.sweepInterval, "sweep-interval", envDuration("SWEEP_INTERVAL", 24*time.Hour), "Full inventory sweep every interval; 0 turns the schedule off (SWEEP_INTERVAL)")
	f.BoolVar(&o.sweepEngineChecks, "sweep-engine-checks", envBoolDefault("SWEEP_ENGINE_CHECKS", true), "Run the engine's set-up checks in read mode for every declared repository during a sweep (SWEEP_ENGINE_CHECKS)")
	f.IntVar(&o.sweepConcurrency, "sweep-concurrency", int(envInt64Default("SWEEP_CONCURRENCY", 4)), "Parallel CircleCI and engine reads during a sweep (SWEEP_CONCURRENCY)")
	f.IntVar(&o.staleDays, "stale-days", int(envInt64Default("ORPHAN_STALE_DAYS", 180)), "Stale period of the orphan score in days (ORPHAN_STALE_DAYS)")
	f.IntVar(&o.graphqlBudgetFloor, "graphql-budget-floor", int(envInt64Default("GRAPHQL_BUDGET_FLOOR", 0)), "Stop a sweep cleanly when the GraphQL budget's remaining points fall below this; 0 never stops (GRAPHQL_BUDGET_FLOOR)")
	f.BoolVar(&o.sweepOnce, "sweep-once", envBool("SWEEP_ONCE"), "Run one full sweep, print its summary as JSON and exit (SWEEP_ONCE)")
	f.StringVar(&o.internalToken, "internal-token", envSecret("INTERNAL_TOKEN"), "Bearer token of the internal endpoints (/internal/refresh for the reconciler, /internal/sweep); empty disables them (INTERNAL_TOKEN)")
	f.StringVar(&o.githubAPIURL, "github-api-url", envOr("GITHUB_API_URL", ""), "GitHub API base URL; empty is api.github.com (GITHUB_API_URL)")
	f.Int64Var(&o.githubAppID, "github-app-id", envInt64("GITHUB_APP_ID"), "GitHub App id for unattended reads (GITHUB_APP_ID)")
	f.Int64Var(&o.githubAppInstallationID, "github-app-installation-id", envInt64("GITHUB_APP_INSTALLATION_ID"), "The App's installation id on the org (GITHUB_APP_INSTALLATION_ID)")
	f.StringVar(&o.githubAppPrivateKeyFile, "github-app-private-key-file", envOr("GITHUB_APP_PRIVATE_KEY_FILE", ""), "PEM private key of the App (GITHUB_APP_PRIVATE_KEY_FILE)")
	f.StringVar(&o.githubToken, "github-token", envSecret("GITHUB_TOKEN"), "Development only: a personal token for the unattended reads when no App is configured; draws from that person's budget (GITHUB_TOKEN)")
	f.StringVar(&o.circleciToken, "circleci-api-token", envSecret("CIRCLECI_API_TOKEN"), "CircleCI API token, read scope; prefer the environment (CIRCLECI_API_TOKEN)")
	f.StringVar(&o.teamFilesRepository, "team-files-repository", envOr("TEAM_FILES_REPOSITORY", teamfiles.DefaultRepository), "owner/name of the repository that holds the team files and policy files (TEAM_FILES_REPOSITORY)")
	f.StringVar(&o.teamFilesRef, "team-files-ref", envOr("TEAM_FILES_REF", teamfiles.DefaultRef), "Branch the team files are read from and pull requests target (TEAM_FILES_REF)")
	f.StringVar(&o.reviewsURL, "reviews-url", envOr("REVIEWS_URL", ""), "klaus-gateway's base URL for the team-review endpoint (POST /reviews, /notices); empty leaves the asks undelivered (REVIEWS_URL)")
	f.StringVar(&o.reviewsTokenFile, "reviews-token-file", envOr("REVIEWS_TOKEN_FILE", "/var/run/secrets/klaus-gateway/token"), "Projected ServiceAccount token (audience klaus-gateway) sent to the team-review endpoint (REVIEWS_TOKEN_FILE)")
	f.StringVar(&o.reviewsChannels, "reviews-channels", envOr("REVIEWS_CHANNELS", ""), "Comma-separated name=ID pairs mapping a policy file's slackChannel to its Slack channel ID (REVIEWS_CHANNELS)")
	f.BoolVar(&o.oauthEnabled, "enable-oauth", envBool("OAUTH_ENABLED"), "Require a GitHub user token as the bearer of every MCP request — behind muster the person's own, through the App giantswarm-repo-manager — verified with GET /user; the caller and the token travel with the request (OAUTH_ENABLED)")
	f.StringVar(&o.oauthBaseURL, "oauth-base-url", envOr("OAUTH_BASE_URL", ""), "URL muster reaches this server at, without the MCP path: the resource of its OAuth protected-resource metadata (OAUTH_BASE_URL)")
	f.StringVar(&o.oauthAuthorizationServer, "oauth-authorization-server", envOr("OAUTH_AUTHORIZATION_SERVER", server.DefaultAuthorizationServer), "Issuer identity of the authorization server muster pins for this server, named in the protected-resource metadata (OAUTH_AUTHORIZATION_SERVER)")
	if err := f.Parse(args); err != nil {
		return nil, err
	}
	return o, nil
}

func main() {
	o, err := parseFlags(os.Args[1:])
	if err != nil {
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, o, slog.Default()); err != nil {
		slog.Error("giantswarm-repo-manager failed", "error", err)
		os.Exit(1)
	}
}

// run wires the components and serves until ctx is done.
func run(ctx context.Context, o *options, log *slog.Logger) error {
	deps := tools.Deps{Version: version(), GitHubAPIURL: o.githubAPIURL, CircleCIConfigured: o.circleciToken != "", Log: log,
		TeamFilesRepository: o.teamFilesRepository, TeamFilesRef: o.teamFilesRef,
		Review: review.New(review.Config{BaseURL: o.reviewsURL, TokenFile: o.reviewsTokenFile, Channels: channelMap(o.reviewsChannels)})}

	var reader *gh.Reader
	switch {
	case o.githubAppID != 0 || o.githubAppInstallationID != 0 || o.githubAppPrivateKeyFile != "":
		key, err := os.ReadFile(o.githubAppPrivateKeyFile) // #nosec G304 G703 -- operator-provided path
		if err != nil {
			return fmt.Errorf("github app private key: %w", err)
		}
		app, err := gh.NewApp(gh.AppConfig{APIURL: o.githubAPIURL, AppID: o.githubAppID, InstallationID: o.githubAppInstallationID, PrivateKey: key})
		if err != nil {
			return err
		}
		deps.App = app
		reader = app.Reader()
	case o.githubToken != "":
		r, err := gh.TokenReader(o.githubAPIURL, o.githubToken)
		if err != nil {
			return err
		}
		reader = r
		deps.Reader = r
		log.Warn("reading GitHub with a personal token (GITHUB_TOKEN): development only, it draws from that person's budget")
	}
	var store *inventory.Store
	if o.valkeyAddr != "" {
		s, err := inventory.New(o.valkeyAddr, log)
		if err != nil {
			return err
		}
		defer s.Close()
		store, deps.Inventory = s, s
	}
	var circle *collect.CircleCIClient
	if o.circleciToken != "" {
		c, err := collect.NewCircleCI(o.circleciToken)
		if err != nil {
			return err
		}
		circle = c
	}
	if reader != nil && deps.Inventory != nil {
		var cc *circleciclient.Client
		var circleReads collect.CircleCI
		if circle != nil {
			cc, circleReads = circle.Client(), circle
		}
		deps.Collector = collect.New(collect.Options{
			Org: o.org, Stale: time.Duration(o.staleDays) * 24 * time.Hour, EngineChecks: o.sweepEngineChecks,
			Concurrency: o.sweepConcurrency, BudgetFloor: o.graphqlBudgetFloor,
		}, reader, deps.Inventory, collect.NewEngine(o.org, reader, cc), circleReads, log)
	}
	if o.sweepOnce {
		if store != nil {
			if err := store.WaitConnected(ctx, o.connectTimeout); err != nil {
				return err
			}
		}
		return sweepOnce(ctx, deps.Collector)
	}

	cfg := server.Config{Addr: o.listen, MCPPath: o.mcpPath}
	if store != nil {
		cfg.Ready = store.Ping
	}
	if o.oauthEnabled {
		cfg.OAuth = &server.OAuthConfig{BaseURL: o.oauthBaseURL, AuthorizationServer: o.oauthAuthorizationServer, GitHubAPIURL: o.githubAPIURL}
		deps.AuthorizationServer = o.oauthAuthorizationServer
	}
	ts := tools.New(deps)
	if deps.Collector != nil {
		cfg.Internal = deps.Collector.InternalHandler(o.internalToken)
		deps.Collector.OnReconciled(ts.Reconciled)
	}
	srv, err := server.New(cfg, ts.MCPServer(), log)
	if err != nil {
		return err
	}
	// The store connects while the server already serves: liveness passes,
	// readiness fails until Valkey answers, the identity tools work
	// throughout, and the sweep schedule starts on a connected store. A store
	// that stays away for the whole window ends the run with its error.
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	if store != nil {
		go func() {
			if err := store.WaitConnected(runCtx, o.connectTimeout); err != nil {
				cancel(err)
				return
			}
			if deps.Collector != nil {
				deps.Collector.RunSchedule(runCtx, o.sweepInterval)
			}
		}()
	}
	readsAs := "none"
	if reader != nil {
		readsAs = reader.Name()
	}
	log.Info("giantswarm-repo-manager starting", "version", deps.Version, "engine", tools.EngineVersion(), "listen", o.listen, "mcp", o.mcpPath,
		"oauth", o.oauthEnabled, "authorizationServer", deps.AuthorizationServer, "githubApp", deps.App != nil, "reads", readsAs,
		"inventory", o.valkeyAddr, "inventoryConnectTimeout", o.connectTimeout, "collector", deps.Collector != nil, "sweepInterval", o.sweepInterval,
		"engineChecks", o.sweepEngineChecks, "internalEndpoints", deps.Collector != nil && o.internalToken != "",
		"teamFiles", o.teamFilesRepository+"@"+o.teamFilesRef, "reviews", o.reviewsURL)
	err = srv.Run(runCtx)
	if cause := context.Cause(runCtx); cause != nil && !errors.Is(cause, context.Canceled) {
		return cause
	}
	return err
}

// sweepOnce runs one sweep and prints the summary; a budget stop is printed
// too and is not a failure.
func sweepOnce(ctx context.Context, c *collect.Collector) error {
	if c == nil {
		return errors.New("--sweep-once needs the inventory store (VALKEY_ADDR) and a GitHub read identity (the App, or GITHUB_TOKEN)")
	}
	sum, err := c.Sweep(ctx)
	if err != nil && !errors.Is(err, collect.ErrBudget) {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(sum)
}

// version is the module version of the build, else the VCS revision — the
// org convention: no -ldflags, debug.ReadBuildInfo() is the source.
func version() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	rev, dirty := "", ""
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if rev == "" {
		return "dev"
	}
	return "dev-" + rev + dirty
}

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// envSecret reads a secret from the environment with surrounding whitespace
// and quotes trimmed: a Secret created from a file carries the file's
// trailing newline, and a token copied from a YAML file may carry its quotes,
// which CircleCI answers with 401.
func envSecret(key string) string {
	return strings.Trim(strings.TrimSpace(os.Getenv(key)), `"'`)
}

func envBool(key string) bool {
	b, err := strconv.ParseBool(os.Getenv(key))
	return err == nil && b
}

func envBoolDefault(key string, def bool) bool {
	b, err := strconv.ParseBool(os.Getenv(key))
	if err != nil {
		return def
	}
	return b
}

func envInt64(key string) int64 {
	n, _ := strconv.ParseInt(os.Getenv(key), 10, 64)
	return n
}

func envInt64Default(key string, def int64) int64 {
	n, err := strconv.ParseInt(os.Getenv(key), 10, 64)
	if err != nil {
		return def
	}
	return n
}

func envDuration(key string, def time.Duration) time.Duration {
	d, err := time.ParseDuration(os.Getenv(key))
	if err != nil {
		return def
	}
	return d
}

// channelMap parses "name=ID,name=ID".
func channelMap(s string) map[string]string {
	out := map[string]string{}
	for _, p := range splitList(s) {
		if name, id, ok := strings.Cut(p, "="); ok {
			out[strings.TrimPrefix(strings.TrimSpace(name), "#")] = strings.TrimSpace(id)
		}
	}
	return out
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
