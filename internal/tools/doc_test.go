package tools

import (
	"slices"
	"sort"
	"testing"

	"github.com/giantswarm/mcp-toolkit/tooldesc"
	"github.com/giantswarm/mcp-toolkit/tooldesc/tooldesctest"
)

// cacheWrites are the tools that write only the inventory cache: the
// non-read-only tools outside the write framework, as docsHeader says.
var cacheWrites = []string{ToolRefreshRepository, ToolSweepInventory}

var docsHeader = "# The tools\n\nGenerated from the registered tools (`make tools-doc`); through muster every tool is `x_" + ToolPrefix + "_<name>`. " +
	"`" + cacheWrites[0] + "` and `" + cacheWrites[1] + "` write only the inventory cache; every other write tool takes `dryRun` and `mode`, " +
	"where `commit` is the only write mode and `apply` is refused."

// TestToolsDocIsCurrent keeps docs/tools.md — every tool's description and
// input schema as a model reads them — equal to the registered tools.
// TOOLS_DOC_UPDATE=1 rewrites the file (`make tools-doc`).
func TestToolsDocIsCurrent(t *testing.T) {
	tooldesctest.AssertServerDoc(t, NewMCPServer(Deps{Version: testVersion}), docsHeader, "../../docs/tools.md")
}

// TestDocsHeaderNamesTheCacheWrites keeps docsHeader true: every write tool
// outside the write framework (no dryRun, no mode) is one of cacheWrites.
func TestDocsHeaderNamesTheCacheWrites(t *testing.T) {
	var outside []string
	for name, st := range NewMCPServer(Deps{Version: testVersion}).ListTools() {
		if ro := st.Tool.Annotations.ReadOnlyHint; (ro == nil || !*ro) && !slices.Contains(WriteToolNames(), name) {
			outside = append(outside, name)
		}
	}
	sort.Strings(outside)
	want := slices.Sorted(slices.Values(cacheWrites))
	if !slices.Equal(outside, want) {
		t.Errorf("write tools outside the write framework are %v, docsHeader names %v", outside, want)
	}
}

// TestToolDescriptionBudgets holds the registered descriptions to the
// budget of giantswarm/giantswarm#38592.
func TestToolDescriptionBudgets(t *testing.T) {
	if err := tooldesc.CheckServer(NewMCPServer(Deps{Version: testVersion}), tooldesc.DefaultBudget); err != nil {
		t.Error(err)
	}
}
