package tools

import (
	"testing"

	"github.com/giantswarm/mcp-toolkit/tooldesc"
	"github.com/giantswarm/mcp-toolkit/tooldesc/tooldesctest"
)

// TestToolsDocIsCurrent keeps docs/tools.md — every tool's description and
// input schema as a model reads them — equal to the registered tools.
// TOOLS_DOC_UPDATE=1 rewrites the file (`make tools-doc`).
func TestToolsDocIsCurrent(t *testing.T) {
	tooldesctest.AssertServerDoc(t, NewMCPServer(Deps{Version: testVersion}),
		"# The tools\n\nGenerated from the registered tools (`make tools-doc`); through muster every tool is `x_"+ToolPrefix+"_<name>`. "+
			"`refresh_repository` and `sweep_inventory` write only the inventory cache; every other write tool takes `dryRun` and `mode`, "+
			"where `commit` is the only write mode and `apply` is refused.",
		"../../docs/tools.md")
}

// TestToolDescriptionBudgets holds the registered descriptions to the
// budget of giantswarm/giantswarm#38592.
func TestToolDescriptionBudgets(t *testing.T) {
	if err := tooldesc.CheckServer(NewMCPServer(Deps{Version: testVersion}), tooldesc.DefaultBudget); err != nil {
		t.Error(err)
	}
}
