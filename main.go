// Command giantswarm-repo-manager is Giant Swarm's repository set-up service:
// an MCP server behind muster that lists, validates, creates and reconciles
// the giantswarm org's repositories as the person calling it.
//
// Every flag can also be set through the environment variable named next to
// it; flags win over the environment. Optional components (the broker client,
// the GitHub App, the inventory store) that are not configured leave the
// server running and are reported as missing by get_info.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"

	"github.com/giantswarm/giantswarm-repo-manager/internal/broker"
	"github.com/giantswarm/giantswarm-repo-manager/internal/gh"
	"github.com/giantswarm/giantswarm-repo-manager/internal/inventory"
	"github.com/giantswarm/giantswarm-repo-manager/internal/server"
	"github.com/giantswarm/giantswarm-repo-manager/internal/tools"
)

type options struct {
	listen, mcpPath string

	valkeyAddr string

	musterURL, brokerClientID, brokerClientSecret, brokerAudience string

	githubAPIURL, githubAppPrivateKeyFile string
	githubAppID, githubAppInstallationID  int64

	circleciToken string

	oauthEnabled, oauthAllowPrivateURLs, ssoAllowPrivateIPs, allowPublicClientRegistration     bool
	oauthBaseURL, dexIssuerURL, dexClientID, dexClientSecret, dexCAFile, oauthTrustedAudiences string
}

func parseFlags(args []string) (*options, error) {
	o := &options{}
	f := flag.NewFlagSet("giantswarm-repo-manager", flag.ContinueOnError)
	f.StringVar(&o.listen, "listen", envOr("LISTEN", ":8080"), "Listen address (LISTEN)")
	f.StringVar(&o.mcpPath, "mcp-path", envOr("MCP_PATH", "/mcp"), "MCP endpoint path (MCP_PATH)")
	f.StringVar(&o.valkeyAddr, "valkey-addr", envOr("VALKEY_ADDR", ""), "host:port of the Valkey the inventory lives in (VALKEY_ADDR)")
	f.StringVar(&o.musterURL, "muster-url", envOr("MUSTER_URL", ""), "muster's base URL; the broker exchange goes to <url>/oauth/token (MUSTER_URL)")
	f.StringVar(&o.brokerClientID, "broker-client-id", envOr("BROKER_CLIENT_ID", ""), "This server's muster broker client id (BROKER_CLIENT_ID)")
	f.StringVar(&o.brokerClientSecret, "broker-client-secret", envOr("BROKER_CLIENT_SECRET", ""), "The broker client secret; prefer the environment (BROKER_CLIENT_SECRET)")
	f.StringVar(&o.brokerAudience, "broker-audience", envOr("BROKER_AUDIENCE", broker.DefaultAudience), "Broker target that releases the person's GitHub grant (BROKER_AUDIENCE)")
	f.StringVar(&o.githubAPIURL, "github-api-url", envOr("GITHUB_API_URL", ""), "GitHub API base URL; empty is api.github.com (GITHUB_API_URL)")
	f.Int64Var(&o.githubAppID, "github-app-id", envInt64("GITHUB_APP_ID"), "GitHub App id for unattended reads (GITHUB_APP_ID)")
	f.Int64Var(&o.githubAppInstallationID, "github-app-installation-id", envInt64("GITHUB_APP_INSTALLATION_ID"), "The App's installation id on the org (GITHUB_APP_INSTALLATION_ID)")
	f.StringVar(&o.githubAppPrivateKeyFile, "github-app-private-key-file", envOr("GITHUB_APP_PRIVATE_KEY_FILE", ""), "PEM private key of the App (GITHUB_APP_PRIVATE_KEY_FILE)")
	f.StringVar(&o.circleciToken, "circleci-api-token", envOr("CIRCLECI_API_TOKEN", ""), "CircleCI API token, read scope; prefer the environment (CIRCLECI_API_TOKEN)")
	f.BoolVar(&o.oauthEnabled, "enable-oauth", envBool("OAUTH_ENABLED"), "Require a bearer token on the MCP endpoint, validated against Dex; the caller's identity and id_token travel with every request (OAUTH_ENABLED)")
	f.StringVar(&o.oauthBaseURL, "oauth-base-url", envOr("OAUTH_BASE_URL", ""), "Public base URL of this server, the issuer of its OAuth metadata (OAUTH_BASE_URL)")
	f.StringVar(&o.dexIssuerURL, "dex-issuer-url", envOr("DEX_ISSUER_URL", ""), "Dex issuer URL (DEX_ISSUER_URL)")
	f.StringVar(&o.dexClientID, "dex-client-id", envOr("DEX_CLIENT_ID", ""), "Dex client ID (DEX_CLIENT_ID)")
	f.StringVar(&o.dexClientSecret, "dex-client-secret", envOr("DEX_CLIENT_SECRET", ""), "Dex client secret; prefer the environment (DEX_CLIENT_SECRET)")
	f.StringVar(&o.dexCAFile, "dex-ca-file", envOr("DEX_CA_FILE", ""), "PEM CA bundle of a Dex with a private certificate (DEX_CA_FILE)")
	f.BoolVar(&o.oauthAllowPrivateURLs, "allow-private-oauth-urls", envBool("OAUTH_ALLOW_PRIVATE_URLS"), "Let the Dex issuer resolve to a private or loopback address (OAUTH_ALLOW_PRIVATE_URLS)")
	f.StringVar(&o.oauthTrustedAudiences, "oauth-trusted-audiences", envOr("OAUTH_TRUSTED_AUDIENCES", ""), "Comma-separated OAuth client IDs whose id_tokens are accepted as bearers (OAUTH_TRUSTED_AUDIENCES)")
	f.BoolVar(&o.ssoAllowPrivateIPs, "sso-allow-private-ips", envBool("SSO_ALLOW_PRIVATE_IPS"), "Let the JWKS endpoint resolve to a private address (SSO_ALLOW_PRIVATE_IPS)")
	f.BoolVar(&o.allowPublicClientRegistration, "allow-public-client-registration", envBool("OAUTH_ALLOW_PUBLIC_REGISTRATION"), "Accept unauthenticated dynamic client registration; labs only (OAUTH_ALLOW_PUBLIC_REGISTRATION)")
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
	deps := tools.Deps{Version: version(), GitHubAPIURL: o.githubAPIURL, CircleCIConfigured: o.circleciToken != "", Log: log}

	if o.musterURL != "" || o.brokerClientID != "" || o.brokerClientSecret != "" {
		b, err := broker.New(broker.Config{MusterURL: o.musterURL, ClientID: o.brokerClientID, ClientSecret: o.brokerClientSecret, Audience: o.brokerAudience})
		if err != nil {
			return err
		}
		deps.Broker = b
	}
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
	}
	if o.valkeyAddr != "" {
		store, err := inventory.Open(o.valkeyAddr)
		if err != nil {
			return err
		}
		defer store.Close()
		deps.Inventory = store
	}

	cfg := server.Config{Addr: o.listen, MCPPath: o.mcpPath}
	if o.oauthEnabled {
		cfg.OAuth = &server.OAuthConfig{
			BaseURL: o.oauthBaseURL, DexIssuerURL: o.dexIssuerURL, DexClientID: o.dexClientID, DexClientSecret: o.dexClientSecret,
			DexCAFile: o.dexCAFile, DexAllowPrivateIP: o.oauthAllowPrivateURLs, TrustedAudiences: splitList(o.oauthTrustedAudiences),
			SSOAllowPrivateIPs: o.ssoAllowPrivateIPs, AllowPublicClientRegistration: o.allowPublicClientRegistration,
		}
	}
	srv, err := server.New(cfg, tools.NewMCPServer(deps), log)
	if err != nil {
		return err
	}
	log.Info("giantswarm-repo-manager starting", "version", deps.Version, "engine", tools.EngineVersion(), "listen", o.listen, "mcp", o.mcpPath,
		"oauth", o.oauthEnabled, "broker", deps.Broker != nil, "muster", o.musterURL, "githubApp", deps.App != nil, "inventory", o.valkeyAddr)
	return srv.Run(ctx)
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

func envInt64(key string) int64 {
	n, _ := strconv.ParseInt(os.Getenv(key), 10, 64)
	return n
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
