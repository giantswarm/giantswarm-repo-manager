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
