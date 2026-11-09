package tools

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/giantswarm/mcp-toolkit/brand"
	"github.com/mark3labs/mcp-go/mcp"
)

// TestInitializeAdvertisesTheGiantSwarmIcon keeps the logo muster stores as
// the MCPServer's status.icon in the initialize result.
func TestInitializeAdvertisesTheGiantSwarmIcon(t *testing.T) {
	initReq := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`)
	raw, err := json.Marshal(NewMCPServer(Deps{Version: testVersion}).HandleMessage(t.Context(), initReq))
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Result mcp.InitializeResult `json:"result"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if want := []mcp.Icon{brand.GiantSwarmIcon()}; !reflect.DeepEqual(resp.Result.ServerInfo.Icons, want) {
		t.Errorf("serverInfo.icons = %+v, want %+v", resp.Result.ServerInfo.Icons, want)
	}
}
