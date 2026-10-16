package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// TestToolsDocIsCurrent keeps docs/tools.md — the tool names, descriptions
// and input schemas as muster exposes them — equal to the registered tools.
// TOOLS_DOC_UPDATE=1 rewrites the file (`make tools-doc`).
func TestToolsDocIsCurrent(t *testing.T) {
	c := newTestClient(t, NewMCPServer(Deps{Version: testVersion}))
	res, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	want := renderToolsDoc(res.Tools)
	const path = "../../docs/tools.md"
	if os.Getenv("TOOLS_DOC_UPDATE") != "" {
		if err := os.WriteFile(path, []byte(want), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v — run with TOOLS_DOC_UPDATE=1", err)
	}
	if string(got) != want {
		t.Errorf("docs/tools.md is out of date: run `make tools-doc`")
	}
}

// Description budgets (giantswarm/giantswarm#38592). muster's filter_tools
// shows the first summaryChars of a description, the text a model picks a
// tool by.
const (
	summaryChars     = 117
	descriptionChars = 600
	paramChars       = 150
	prefixChars      = 40
)

func TestToolDescriptionBudgets(t *testing.T) {
	c := newTestClient(t, NewMCPServer(Deps{Version: testVersion}))
	res, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	prefixes := map[string]string{}
	for _, tool := range res.Tools {
		d := tool.Description
		first, _, _ := strings.Cut(d, ". ")
		if n := len([]rune(first)) + 1; n > summaryChars {
			t.Errorf("%s: first sentence is %d chars, over %d: %q", tool.Name, n, summaryChars, first)
		}
		if n := len([]rune(d)); n > descriptionChars {
			t.Errorf("%s: description is %d chars, over %d", tool.Name, n, descriptionChars)
		}
		p := string([]rune(d)[:min(prefixChars, len([]rune(d)))])
		if other, ok := prefixes[p]; ok {
			t.Errorf("%s and %s start alike: %q", tool.Name, other, p)
		}
		prefixes[p] = tool.Name
		for name, prop := range tool.InputSchema.Properties {
			pd, _ := prop.(map[string]any)["description"].(string)
			if n := len([]rune(pd)); n > paramChars {
				t.Errorf("%s.%s: description is %d chars, over %d", tool.Name, name, n, paramChars)
			}
		}
	}
}

func renderToolsDoc(ts []mcp.Tool) string {
	sort.Slice(ts, func(i, j int) bool { return ts[i].Name < ts[j].Name })
	var b strings.Builder
	b.WriteString("# The tools\n\n")
	b.WriteString("Generated from the registered tools (`make tools-doc`); ")
	b.WriteString("through muster every tool is `x_" + ToolPrefix + "_<name>`. Every write tool takes `dryRun` and `mode`; `commit` is the only write mode, `apply` is refused.\n\n")
	b.WriteString("| Tool | Kind |\n|---|---|\n")
	writes := map[string]bool{}
	for _, n := range WriteToolNames() {
		writes[n] = true
	}
	for _, tool := range ts {
		kind := "read-only"
		switch {
		case writes[tool.Name]:
			kind = "write (dryRun, mode: commit)"
		case tool.Annotations.ReadOnlyHint == nil || !*tool.Annotations.ReadOnlyHint:
			kind = "cache annotation"
		}
		fmt.Fprintf(&b, "| `%s` | %s |\n", tool.Name, kind)
	}
	for _, tool := range ts {
		fmt.Fprintf(&b, "\n## `%s`\n\n%s\n\n", tool.Name, tool.Description)
		schema, _ := json.MarshalIndent(tool.InputSchema, "", "  ")
		fmt.Fprintf(&b, "```json\n%s\n```\n", schema)
	}
	return b.String()
}
