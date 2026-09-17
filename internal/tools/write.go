package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/giantswarm-repo-manager/internal/identity"
)

// Mode is how a write lands. The managers' contract offers apply and commit;
// for repositories only commit exists: the declaration in the team file is
// the source of truth, and a repository changed on GitHub without its
// declaration is exactly the drift the reconciler reports.
type Mode string

// ModeCommit is the only accepted mode: a team-file pull request as the caller.
const ModeCommit Mode = "commit"

// modeApply is named so the refusal can explain itself.
const modeApply = "apply"

const (
	// ArgDryRun and ArgMode are the two arguments every write tool takes.
	ArgDryRun = "dryRun"
	ArgMode   = "mode"
)

// ApplyRefusal is the reason mode: apply is refused, word for word.
const ApplyRefusal = `mode "apply" is refused: a repository without its declaration is drift the reconciler reports. ` +
	`Use mode "commit" (a team-file pull request opened as you) or dryRun: true for the rendered change`

// ErrNotImplemented marks a commit path a later slice delivers.
var ErrNotImplemented = errors.New("not implemented yet")

// WriteTool is a write registered through the framework: the framework owns
// dryRun and mode, refuses apply before the tool runs, and dispatches to
// DryRun or Commit.
type WriteTool struct {
	Name        string
	Description string
	// Options are the tool's own arguments (mcp.WithString and friends).
	Options []mcp.ToolOption
	// DryRun renders the change and writes nothing.
	DryRun func(ctx context.Context, args map[string]any) (any, error)
	// Commit lands the change as the caller. nil means ErrNotImplemented.
	Commit func(ctx context.Context, args map[string]any) (any, error)
}

// registerWrite adds wt to s with the framework's arguments and guard.
func registerWrite(s *mcpserver.MCPServer, wt WriteTool) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("WRITES (as you, with your own GitHub token through the App giantswarm-repo-manager). " + wt.Description +
			" Every write takes dryRun and mode: dryRun: true returns the rendered change and writes nothing; " +
			`mode: "commit" opens the team-file pull request as you. mode: "apply" is refused for every write tool ` +
			"(a repository without its declaration is drift), and mode is required unless dryRun is true."),
		mcp.WithBoolean(ArgDryRun, mcp.Description("Render the change and write nothing (default false).")),
		mcp.WithString(ArgMode, mcp.Description(`How the change lands: "commit" (a team-file pull request as you). "apply" is refused.`), mcp.Enum(string(ModeCommit))),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
	}
	opts = append(opts, wt.Options...)
	s.AddTool(mcp.NewTool(wt.Name, opts...), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		dryRun, _ := args[ArgDryRun].(bool)
		mode, _ := args[ArgMode].(string)
		if err := checkMode(mode, dryRun); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		delete(args, ArgDryRun)
		delete(args, ArgMode)
		if dryRun {
			return result(wt.DryRun(ctx, args))
		}
		if wt.Commit == nil {
			return mcp.NewToolResultError(fmt.Sprintf("%s: mode commit %v; run with dryRun: true for the rendered change", wt.Name, ErrNotImplemented)), nil
		}
		if _, ok := identity.FromContext(ctx); !ok {
			return mcp.NewToolResultError(fmt.Sprintf("%s: mode commit needs a caller: the request carried no identity to open the pull request as", wt.Name)), nil
		}
		return result(wt.Commit(ctx, args))
	})
}

// checkMode is the framework's guard: apply is refused, unknown modes are
// refused, and a write without dryRun needs a mode.
func checkMode(mode string, dryRun bool) error {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case string(ModeCommit):
		return nil
	case modeApply:
		return errors.New(ApplyRefusal)
	case "":
		if dryRun {
			return nil
		}
		return fmt.Errorf(`mode is required unless dryRun is true: "%s"`, ModeCommit)
	default:
		return fmt.Errorf(`mode %q is not known: "%s" is the only write mode (apply is refused)`, mode, ModeCommit)
	}
}
