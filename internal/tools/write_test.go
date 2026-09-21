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
	"github.com/giantswarm/giantswarm-repo-manager/internal/teamfiles"
)

const (
	testVersion = "test"
	testTeam    = "team-bumblebee"
)

// validEntry is a declaration the embedded schema and the creation rules
// accept: a Go service, chart flavour, name free (no name checker → unchecked).
var validEntry = map[string]any{
	teamfiles.FieldName:          "example-service",
	teamfiles.FieldComponentType: "service",
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

// TestValidateRepositoryTakesCreateRepositoryArguments: the dry run declares
// every argument of create_repository but the framework's two, reason among
// them — a client checks a call against the schema and refuses an argument
// the schema lacks, and it runs the dry run with the arguments it commits.
func TestValidateRepositoryTakesCreateRepositoryArguments(t *testing.T) {
	c := newTestClient(t, NewMCPServer(Deps{Version: testVersion}))
	res, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	schemas := map[string]mcp.ToolInputSchema{}
	for _, tool := range res.Tools {
		schemas[tool.Name] = tool.InputSchema
	}
	validate, create := schemas[ToolValidateRepository], schemas[ToolCreateRepository]
	if _, ok := validate.Properties[argReason]; !ok {
		t.Errorf("%s: no %s argument", ToolValidateRepository, argReason)
	}
	want := map[string]any{}
	for name, p := range create.Properties {
		if name != ArgDryRun && name != ArgMode {
			want[name] = p
		}
	}
	got, _ := json.Marshal(validate.Properties)
	exp, _ := json.Marshal(want)
	if string(got) != string(exp) {
		t.Errorf("%s arguments differ from %s's:\n%s\n%s", ToolValidateRepository, ToolCreateRepository, got, exp)
	}
	if gotReq, expReq := strings.Join(validate.Required, ","), strings.Join(create.Required, ","); gotReq != expReq {
		t.Errorf("%s required %s, %s requires %s", ToolValidateRepository, gotReq, ToolCreateRepository, expReq)
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
	if !res.Accepted || len(res.Entries) != 1 || res.Entries[0].Template != "giantswarm/template" || !strings.Contains(res.Entries[0].Rendered, "generate: true") ||
		!strings.Contains(res.Entries[0].Rendered, "align: true") {
		t.Errorf("unexpected dry run: %s", text)
	}

	// A refused entry is data, not an error.
	bad := map[string]any{"name": "example-app", "componentType": "service", "gen": map[string]any{"language": "go", "flavours": []any{"app"}}}
	text, isErr = call(t, c, ToolCreateRepository, map[string]any{ArgDryRun: true, argTeam: testTeam, argEntry: bad})
	if isErr || !strings.Contains(text, `"accepted": false`) || !strings.Contains(text, "-app") {
		t.Errorf("refusal should be data naming the -app suffix: isError=%v %s", isErr, text)
	}

	// commit without a caller: nobody to open the pull request as.
	text, isErr = call(t, c, ToolCreateRepository, map[string]any{ArgMode: string(ModeCommit), argTeam: testTeam, argEntry: validEntry})
	if !isErr || !strings.Contains(text, "needs a caller") {
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
	if info.Caller != nil || info.Auth.Mode != AuthModeNone || info.Auth.Reason == "" || info.GitHub.AppError == "" ||
		info.Inventory.Connected || !info.Capabilities.ApplyRefused || info.Engine.Package != engineModule+"/pkg/reposetup" {
		t.Errorf("unexpected info: %s", text)
	}
}

// TestParseEntriesOptsEveryEntryIn: the creation is the repository's opt-in
// to alignment — every entry the creation tools parse carries align: true,
// after the keys the caller wrote; one saying align: false is refused with
// the reason, and the caller's map is left as it was.
func TestParseEntriesOptsEveryEntryIn(t *testing.T) {
	tf, err := parseEntries(testTeam, []any{validEntry, map[string]any{teamfiles.FieldName: "other-service", teamfiles.FieldAlign: true}})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range tf.Entries {
		f, err := d.Fields()
		if err != nil || !f.Align {
			t.Errorf("%s: %+v %v", d.Name, f, err)
		}
	}
	if y, _ := tf.Entries[0].YAML(); !strings.Contains(y, "\n  componentType: service\n  align: true\n  gen:\n") {
		t.Errorf("rendered:\n%s", y)
	}
	if _, set := validEntry[teamfiles.FieldAlign]; set {
		t.Error("the caller's entry was changed")
	}
	_, err = parseEntries(testTeam, []any{map[string]any{teamfiles.FieldName: "opted-out", teamfiles.FieldAlign: false}})
	if err == nil || err.Error() != "opted-out: "+alignRefusal {
		t.Errorf("align: false: %v", err)
	}
}

// TestAlignWarningNamesTheRepositoryAndItsOptIn: the paragraph names the
// repository, not the team, and says what this run does — the entry's
// opt-in decides: applied, opted in first (the pull request the team
// reviews), or a check from the team alone for an undeclared repository.
func TestAlignWarningNamesTheRepositoryAndItsOptIn(t *testing.T) {
	const repo = "giantswarm/example-service"
	head := "Align now changes " + repo + " on GitHub and CircleCI to its declared set-up and the company baseline: " + alignChanges + ". It runs as you. "
	cases := []struct {
		name              string
		team              string
		optedIn, declared bool
		want              string
	}{
		{"opted in", testTeam, true, true, head + repo + " is opted in to alignment (`align: true` in its entry): the planned changes are applied."},
		{"declared without the field", testTeam, false, true, repo + " has not opted in to alignment. Align now opts it in — `align: true` in its entry, in a pull request " + testTeam + " reviews (the ask goes to " + testTeam + "'s channel; a member other than you approves) — and the reconciler applies the planned changes when it merges: " + alignChanges + ". It runs as you."},
		{"undeclared with a team", testTeam, false, false, head + repo + " has no entry: this run checks from the team alone and changes nothing; declare the repository with `align: true` in its entry to have it aligned."},
		{"undeclared without a team", "", false, false, head + repo + " has no entry and no team is known for it: pass team. The run then checks from the team alone and changes nothing; declare the repository with `align: true` in its entry to have it aligned."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := alignWarning(repo, tc.team, tc.optedIn, tc.declared); got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestPlannedSentenceCountsTheChanges: the opt-in's ask and pull request
// body say what the reconciler applies once the pull request merges — the
// last check's changes counted with their steps, or why none are known.
func TestPlannedSentenceCountsTheChanges(t *testing.T) {
	const checked = "2026-09-18T12:00:00Z"
	cases := []struct {
		name      string
		planned   []PlannedStep
		checkedAt string
		want      string
	}{
		{"no check", nil, "", "the reconciler applies what its run finds once merged (no check has run yet)"},
		{"converged", nil, checked, "the reconciler applies what its run finds once merged (the last check found the repository converged)"},
		{"one change", []PlannedStep{{Step: "merge", Changes: []string{"squash only"}}}, checked, "the reconciler applies 1 planned change once merged — merge"},
		{"several steps", []PlannedStep{{Step: "merge", Changes: []string{"squash only", "auto-merge"}}, {Step: "protection", Changes: []string{"enforce_admins on"}}}, checked, "the reconciler applies 3 planned changes once merged — merge, protection"},
		{"drift without a listed change", []PlannedStep{{Step: "circleci"}}, checked, "the reconciler applies the planned changes once merged — circleci"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := plannedSentence(tc.planned, tc.checkedAt); got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}
