package tools

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"
)

// SummaryMax is how much of a description muster's discovery shows: its
// filter_tools and list_tools answer with each description's first line, cut
// to 120 runes (summaryMaxLen in muster's internal/metatools/handlers_tools.go).
// A model picks its tool from that summary; the rest arrives with
// describe_tool, after the pick.
const SummaryMax = 120

// describeTool is every tool's description (giantswarm/giantswarm#38592). summary
// is the first line, all that discovery shows: what the tool does, and which
// tool to use instead or the one thing a person must do. details follow after
// a blank line. A summary that does not fit SummaryMax on one line panics, so
// no server with one builds.
func describeTool(summary, details string) mcp.ToolOption {
	return mcp.WithDescription(description(summary, details))
}

func description(summary, details string) string {
	n := utf8.RuneCountInString(summary)
	if n == 0 || n > SummaryMax || strings.Contains(summary, "\n") {
		panic(fmt.Sprintf("tool summary %q is %d runes: it must fit one line of at most %d, all muster's discovery shows", summary, n, SummaryMax))
	}
	if details = strings.TrimSpace(details); details == "" {
		return summary
	}
	return summary + "\n\n" + details
}
