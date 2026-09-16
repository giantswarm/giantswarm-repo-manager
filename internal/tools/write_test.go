package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/giantswarm-repo-manager/internal/identity"
)

const (
	testVersion = "test"
	testTeam    = "team-bumblebee"
)

// validEntry is a declaration the embedded schema and the creation rules
// accept: a Go service, chart flavour, name free (no name checker → unchecked).
var validEntry = map[string]any{
	"name":          "example-service",
	"componentType": "service",
	"gen": map[string]any{
		"language": "go",
		"flavours": []any{"app"},
		"ci":       map[string]any{"chartName": "example-service"},
	},
}

func newTestClient(t *testing.T, s *mcpserver.MCPServer) *client.Client {
	t.Helper()
	c, err := client.NewInProcessClient(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Initialize(context.Background(), mcp.InitializeRequest{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func call(t *testing.T, c *client.Client, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := c.CallTool(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: tool, Arguments: args}})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	var text strings.Builder
	for _, cnt := range res.Content {
		if tc, ok := cnt.(mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	return text.String(), res.IsError
}

// TestEveryWriteToolRefusesApply is the framework-level contract: mode apply is
// refused before any tool body runs, an unknown mode is refused, and a write
// without dryRun needs a mode.
func TestEveryWriteToolRefusesApply(t *testing.T) {
	c := newTestClient(t, NewMCPServer(Deps{Version: testVersion}))
	for _, name := range WriteToolNames() {
		for _, tc := range []struct {
			args map[string]any
			want string
		}{
			{map[string]any{ArgMode: modeApply, argTeam: testTeam, argEntry: validEntry}, ApplyRefusal},
			{map[string]any{ArgMode: "Apply", ArgDryRun: true, argTeam: testTeam, argEntry: validEntry}, ApplyRefusal},
			{map[string]any{ArgMode: "yolo", argTeam: testTeam, argEntry: validEntry}, `mode "yolo" is not known`},
			{map[string]any{argTeam: testTeam, argEntry: validEntry}, "mode is required unless dryRun is true"},
		} {
			text, isErr := call(t, c, name, tc.args)
			if !isErr || !strings.Contains(text, tc.want) {
				t.Errorf("%s %v: isError=%v text=%q, want an error containing %q", name, tc.args["mode"], isErr, text, tc.want)
			}
		}
	}
}

// TestWriteToolsDeclareTheFrameworkArguments: every write tool advertises
// dryRun and mode with commit as the only enum value, so a client never
// learns apply from the schema.
func TestWriteToolsDeclareTheFrameworkArguments(t *testing.T) {
	c := newTestClient(t, NewMCPServer(Deps{Version: testVersion}))
	res, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, tool := range res.Tools {
		props := tool.InputSchema.Properties
		if _, isWrite := props[ArgMode]; !isWrite {
			continue
		}
		seen[tool.Name] = true
		if _, ok := props[ArgDryRun]; !ok {
			t.Errorf("%s: no %s argument", tool.Name, ArgDryRun)
		}
		mode, _ := props[ArgMode].(map[string]any)
		enum, _ := json.Marshal(mode["enum"])
		if string(enum) != `["commit"]` {
			t.Errorf("%s: mode enum %s, want [\"commit\"]", tool.Name, enum)
		}
	}
	for _, name := range WriteToolNames() {
		if !seen[name] {
			t.Errorf("%s is not registered through the write framework", name)
		}
	}
}

// TestCreateRepositoryDryRunIsTheEngineValidation: the dry run returns the
// engine's Result — the rendered entry with defaults, the template, accepted.
func TestCreateRepositoryDryRunIsTheEngineValidation(t *testing.T) {
	c := newTestClient(t, NewMCPServer(Deps{Version: testVersion}))
	text, isErr := call(t, c, ToolCreateRepository, map[string]any{ArgDryRun: true, argTeam: testTeam, argEntry: validEntry})
	if isErr {
		t.Fatalf("dry run failed: %s", text)
	}
	var res struct {
		Team     string `json:"team"`
		Accepted bool   `json:"accepted"`
		Entries  []struct {
			Name     string `json:"name"`
			Template string `json:"template"`
			Rendered string `json:"rendered"`
			Problems []any  `json:"problems"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		t.Fatalf("result is not the engine's Result JSON: %v\n%s", err, text)
	}
	if !res.Accepted || len(res.Entries) != 1 || res.Entries[0].Template != "giantswarm/template" || !strings.Contains(res.Entries[0].Rendered, "generate: true") {
		t.Errorf("unexpected dry run: %s", text)
	}

	// A refused entry is data, not an error.
	bad := map[string]any{"name": "example-app", "componentType": "service", "gen": map[string]any{"language": "go", "flavours": []any{"app"}}}
	text, isErr = call(t, c, ToolCreateRepository, map[string]any{ArgDryRun: true, argTeam: testTeam, argEntry: bad})
	if isErr || !strings.Contains(text, `"accepted": false`) || !strings.Contains(text, "-app") {
		t.Errorf("refusal should be data naming the -app suffix: isError=%v %s", isErr, text)
	}

	// commit is not delivered by this slice: named as such, nothing written.
	text, isErr = call(t, c, ToolCreateRepository, map[string]any{ArgMode: string(ModeCommit), argTeam: testTeam, argEntry: validEntry})
	if !isErr || !strings.Contains(text, ErrNotImplemented.Error()) {
		t.Errorf("commit: isError=%v %s", isErr, text)
	}
}

// TestCommitNeedsACaller: a write tool with a commit path refuses to commit
// without an identity — there is nobody to open the pull request as.
func TestCommitNeedsACaller(t *testing.T) {
	s := mcpserver.NewMCPServer("t", "0")
	registerWrite(s, WriteTool{
		Name:   "noop",
		DryRun: func(context.Context, map[string]any) (any, error) { return map[string]any{"dry": true}, nil },
		Commit: func(ctx context.Context, _ map[string]any) (any, error) {
			return map[string]any{"as": identity.Caller(ctx)}, nil
		},
	})
	c := newTestClient(t, s)
	text, isErr := call(t, c, "noop", map[string]any{ArgMode: string(ModeCommit)})
	if !isErr || !strings.Contains(text, "needs a caller") {
		t.Errorf("commit without identity: isError=%v %s", isErr, text)
	}
	text, isErr = call(t, c, "noop", map[string]any{ArgMode: modeApply})
	if !isErr || text != ApplyRefusal {
		t.Errorf("apply: isError=%v %q", isErr, text)
	}
	if text, isErr = call(t, c, "noop", map[string]any{ArgDryRun: true}); isErr || !strings.Contains(text, `"dry": true`) {
		t.Errorf("dryRun: isError=%v %s", isErr, text)
	}
}

// TestGetInfoWithoutComponents: the server runs with nothing configured and
// get_info says so instead of failing.
func TestGetInfoWithoutComponents(t *testing.T) {
	c := newTestClient(t, NewMCPServer(Deps{Version: testVersion}))
	text, isErr := call(t, c, ToolGetInfo, nil)
	if isErr {
		t.Fatal(text)
	}
	var info Info
	if err := json.Unmarshal([]byte(text), &info); err != nil {
		t.Fatal(err)
	}
	if info.Caller != nil || info.GitHub.Grant.Obtained || info.GitHub.Grant.Reason == "" || info.GitHub.AppError == "" ||
		info.Inventory.Connected || !info.Capabilities.ApplyRefused || info.Engine.Package != engineModule+"/pkg/reposetup" {
		t.Errorf("unexpected info: %s", text)
	}
}
