// Command giantswarm-repo-manager is Giant Swarm's repository set-up service:
// an MCP server behind muster that lists, validates, creates and reconciles
// the giantswarm org's repositories as the person calling it, and keeps the
// inventory of every repository of the org.
//
// Every flag can also be set through the environment variable named next to
// it; flags win over the environment. Optional components (the inventory App
// giantswarm-repo-manager-inventory for the unattended reads, the inventory
// store) that are not configured leave the server running and are reported
// as missing by get_info; nothing stands in for them. The server holds no
// CircleCI token: the inventory's CircleCI facts come from the `ci/circleci:`
// commit statuses GitHub carries and from the reconciler's run artifact.
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
	"github.com/giantswarm/devctl/v8/pkg/reposetup"

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

	valkeyAddr, org, sweepTeams   string
	sweepInterval, connectTimeout time.Duration
	sweepEngineChecks, sweepOnce  bool
	sweepConcurrency              int
	renovateActiveDays            int
	graphqlBudgetFloor            int
	restBudgetFloor               int

	githubAPIURL, githubAppPrivateKeyFile string
	githubAppID, githubAppInstallationID  int64
	devctlAppID                           int64

	teamFilesRepository, teamFilesRef                                  string
	reconcilerWorkflow                                                 string
	reconcilerPollInterval                                             time.Duration
	releasesInterval, releasesGrace                                    time.Duration
	circleciToken, circleciAPIURL                                      string
	reviewsURL, reviewsTokenFile, reviewsAudience, reviewsDebugChannel string

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
	f.IntVar(&o.sweepConcurrency, "sweep-concurrency", int(envInt64Default("SWEEP_CONCURRENCY", 4)), "Parallel engine reads during a sweep (SWEEP_CONCURRENCY)")
	f.IntVar(&o.renovateActiveDays, "renovate-active-days", int(envInt64Default("RENOVATE_ACTIVE_DAYS", 180)), "Days within which a Renovate pull request or commit counts as Renovate activity — list_repositories' renovate: active | inactive (RENOVATE_ACTIVE_DAYS)")
	f.StringVar(&o.sweepTeams, "sweep-teams", envOr("SWEEP_TEAMS", tools.DefaultSweepTeams), "Comma-separated GitHub team slugs whose members may start a sweep with sweep_inventory — the teams that own the manager and the reconciler (SWEEP_TEAMS)")
	f.IntVar(&o.graphqlBudgetFloor, "graphql-budget-floor", int(envInt64Default("GRAPHQL_BUDGET_FLOOR", 0)), "Stop a sweep cleanly when the GraphQL budget's remaining points fall below this; 0 never stops (GRAPHQL_BUDGET_FLOOR)")
	f.IntVar(&o.restBudgetFloor, "rest-budget-floor", int(envInt64Default("REST_BUDGET_FLOOR", 500)), "Pause a sweep's engine checks until the REST budget resets when its remaining requests fall below this; 0 never pauses (REST_BUDGET_FLOOR)")
	f.BoolVar(&o.sweepOnce, "sweep-once", envBool("SWEEP_ONCE"), "Run one full sweep, print its summary as JSON and exit (SWEEP_ONCE)")
	f.DurationVar(&o.reconcilerPollInterval, "reconciler-poll-interval", envDuration("RECONCILER_POLL_INTERVAL", collect.DefaultReconcilerPoll), "Read the reconciler workflow's completed runs from GitHub every interval and store their reconcile-<repository> artifacts as the repositories' setup.lastRun; every 30 s while an Align now is pending; 0 turns the poller off (RECONCILER_POLL_INTERVAL)")
	f.StringVar(&o.reconcilerWorkflow, "reconciler-workflow", envOr("RECONCILER_WORKFLOW", teamfiles.ReconcilerWorkflow), "The reconciler's workflow file in the team files repository: the one align_repository dispatches and the poller reads the runs of (RECONCILER_WORKFLOW)")
	f.StringVar(&o.githubAPIURL, "github-api-url", envOr("GITHUB_API_URL", ""), "GitHub API base URL; empty is api.github.com (GITHUB_API_URL)")
	f.Int64Var(&o.githubAppID, "github-app-id", envInt64("GITHUB_APP_ID"), "Id of the read-only GitHub App giantswarm-repo-manager-inventory, the identity of the unattended reads (GITHUB_APP_ID)")
	f.Int64Var(&o.githubAppInstallationID, "github-app-installation-id", envInt64("GITHUB_APP_INSTALLATION_ID"), "The inventory App's installation id on the org (GITHUB_APP_INSTALLATION_ID)")
	f.StringVar(&o.githubAppPrivateKeyFile, "github-app-private-key-file", envOr("GITHUB_APP_PRIVATE_KEY_FILE", ""), "PEM private key of the inventory App (GITHUB_APP_PRIVATE_KEY_FILE)")
	f.Int64Var(&o.devctlAppID, "devctl-app-id", envInt64("DEVCTL_APP_ID"), "Numeric id of the devctl GitHub App, the reconciler's bypass actor on the ruleset devctl: default branch. Set it only for a read identity that sees a ruleset's bypass actors (the inventory App does not: GitHub shows them to identities that administer the repository); 0, the default, compares the rules alone and reports the bypass list as not compared (DEVCTL_APP_ID)")
	f.StringVar(&o.teamFilesRepository, "team-files-repository", envOr("TEAM_FILES_REPOSITORY", teamfiles.DefaultRepository), "owner/name of the repository that holds the team files and the teams' channel files (TEAM_FILES_REPOSITORY)")
	f.StringVar(&o.teamFilesRef, "team-files-ref", envOr("TEAM_FILES_REF", teamfiles.DefaultRef), "Branch the team files are read from and pull requests target (TEAM_FILES_REF)")
	f.DurationVar(&o.releasesInterval, "releases-interval", envDuration("RELEASES_INTERVAL", collect.DefaultReleaseInterval), "Follow the latest release of every declared repository from its tag to the end of the tag's own CircleCI pipeline, reading every interval; a red pipeline, or none within the grace period, is one notice to the team's standup channel, told once; 0 turns the watch off (RELEASES_INTERVAL)")
	f.DurationVar(&o.releasesGrace, "releases-grace", envDuration("RELEASES_GRACE", collect.DefaultReleaseGrace), "How long after its creation a release may have no CircleCI pipeline before it is a missed build (RELEASES_GRACE)")
	f.StringVar(&o.circleciToken, "circleci-token", envOr("CIRCLECI_TOKEN", ""), "CircleCI API token for the release watch's reads; empty reads anonymously, which CircleCI answers for public projects alone, and a private repository's release reads unchecked (CIRCLECI_TOKEN)")
	f.StringVar(&o.circleciAPIURL, "circleci-api-url", envOr("CIRCLECI_API_URL", ""), "CircleCI API v2 URL; empty is https://circleci.com/api/v2 (CIRCLECI_API_URL)")
	f.StringVar(&o.reviewsURL, "reviews-url", envOr("REVIEWS_URL", ""), "klaus-gateway's base URL for the team-review endpoint (POST /reviews, /notices); empty leaves the asks undelivered (REVIEWS_URL)")
	f.StringVar(&o.reviewsTokenFile, "reviews-token-file", envOr("REVIEWS_TOKEN_FILE", "/var/run/secrets/klaus-gateway/token"), "Projected ServiceAccount token (audience reviews-audience) sent to the team-review endpoint (REVIEWS_TOKEN_FILE)")
	f.StringVar(&o.reviewsAudience, "reviews-audience", envOr("REVIEWS_AUDIENCE", review.DefaultAudience), "Audience the reviews-token-file token is projected for, the gateway's reviews.audience; get_info reports it (REVIEWS_AUDIENCE)")
	f.StringVar(&o.reviewsDebugChannel, "reviews-debug-channel", envOr("REVIEWS_DEBUG_CHANNEL", ""), "A Slack channel ID that receives every ask and notice instead of the team's channel, the text naming that channel; empty delivers to the channel the team's teams/<team>.yaml names (REVIEWS_DEBUG_CHANNEL)")
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
	reviews, err := review.New(review.Config{BaseURL: o.reviewsURL, TokenFile: o.reviewsTokenFile, Audience: o.reviewsAudience, DebugChannel: o.reviewsDebugChannel})
	if err != nil {
		return err
	}
	// The one repositories schema of the process: the copy embedded in the
	// engine's devctl version, validated against by the tools and the
	// collector alike and reported by get_info.
	schema, err := reposetup.EmbeddedSchema()
	if err != nil {
		return fmt.Errorf("repositories schema: %w", err)
	}
	deps := tools.Deps{Version: version(), GitHubAPIURL: o.githubAPIURL, Log: log, Schema: schema,
		TeamFilesRepository: o.teamFilesRepository, TeamFilesRef: o.teamFilesRef, ReconcilerWorkflow: o.reconcilerWorkflow, SweepTeams: splitList(o.sweepTeams),
		RenovateActive: time.Duration(o.renovateActiveDays) * 24 * time.Hour,
		Review:         reviews}

	var reader *gh.Reader
	if o.githubAppID != 0 || o.githubAppInstallationID != 0 || o.githubAppPrivateKeyFile != "" {
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
	releases := collect.ReleaseOptions{Interval: o.releasesInterval, Grace: o.releasesGrace, Anonymous: o.circleciToken == ""}
	deps.TagPipelines = tools.TagPipelinesOff
	if o.releasesInterval > 0 {
		cc, err := circleciclient.New(circleciclient.Config{Token: o.circleciToken, Anonymous: o.circleciToken == "", BaseURL: circleciclient.BaseURLFromAPIURL(o.circleciAPIURL)})
		if err != nil {
			return fmt.Errorf("circleci client: %w", err)
		}
		releases.CircleCI, deps.CircleCI = cc, cc
		deps.TagPipelines = tools.TagPipelinesToken
		if releases.Anonymous {
			deps.TagPipelines = tools.TagPipelinesAnonymous
		}
	}
	if reader != nil && deps.Inventory != nil {
		deps.Collector = collect.New(collect.Options{
			Org: o.org, EngineChecks: o.sweepEngineChecks, Schema: schema,
			Concurrency: o.sweepConcurrency, BudgetFloor: o.graphqlBudgetFloor, RESTFloor: o.restBudgetFloor, Interval: o.sweepInterval,
			Reconciler: collect.ReconcilerOptions{Repository: o.teamFilesRepository, Workflow: o.reconcilerWorkflow, PollInterval: o.reconcilerPollInterval},
			Releases:   releases,
		}, reader, deps.Inventory, collect.NewEngine(reader, o.devctlAppID), log)
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
		deps.Collector.OnReconciled(ts.Reconciled)
		deps.Collector.OnConflict(ts.Conflicted)
		deps.Collector.OnReleased(ts.Released)
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
				go deps.Collector.RunReconcilerPoll(runCtx)
				go deps.Collector.RunReleaseWatch(runCtx)
				deps.Collector.RunSchedule(runCtx)
			}
		}()
	}
	if deps.Collector == nil {
		log.Info("reconciler poller off: the inventory store and the inventory App are what it reads with", "inventory", o.valkeyAddr != "", "githubApp", reader != nil)
	}
	readsAs := "none"
	if reader != nil {
		readsAs = reader.Name()
	}
	log.Info("giantswarm-repo-manager starting", "version", deps.Version, "engine", tools.EngineVersion(), "listen", o.listen, "mcp", o.mcpPath,
		"oauth", o.oauthEnabled, "authorizationServer", deps.AuthorizationServer, "githubApp", deps.App != nil, "reads", readsAs,
		"inventory", o.valkeyAddr, "inventoryConnectTimeout", o.connectTimeout, "collector", deps.Collector != nil, "sweepInterval", o.sweepInterval,
		"reconcilerPollInterval", o.reconcilerPollInterval, "reconcilerWorkflow", o.reconcilerWorkflow, "releasesInterval", o.releasesInterval, "tagPipelines", deps.TagPipelines,
		"engineChecks", o.sweepEngineChecks, "sweepTeams", o.sweepTeams,
		"teamFiles", o.teamFilesRepository+"@"+o.teamFilesRef, "reviews", o.reviewsURL, "reviewsDebugChannel", o.reviewsDebugChannel)
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
		return errors.New("--sweep-once needs the inventory store (VALKEY_ADDR) and the inventory App giantswarm-repo-manager-inventory (GITHUB_APP_ID, GITHUB_APP_INSTALLATION_ID, GITHUB_APP_PRIVATE_KEY_FILE)")
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

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
