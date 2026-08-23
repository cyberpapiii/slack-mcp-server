package handler

import (
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// NewCSVResult returns tabular data as CSV text with no structured content.
// Clients that prefer structuredContent would otherwise hide the CSV, so
// tabular tools ship one representation. Coverage and paging travel as
// comment lines ahead of the header: "#partial: <reason>" when the page
// stopped early and "#next_cursor: <cursor>" when more rows exist. legend
// holds any further comment lines, such as "#users:" and "#link_template:".
func NewCSVResult(legend string, meta ResultMeta, csv string) *mcp.CallToolResult {
	var sb strings.Builder
	sb.WriteString(legend)
	if meta.Partial && meta.PartialReason != "" {
		sb.WriteString("#partial: " + meta.PartialReason + "\n")
	}
	if meta.NextCursor != "" {
		sb.WriteString("#next_cursor: " + meta.NextCursor + "\n")
	}
	sb.WriteString(csv)
	return mcp.NewToolResultText(sb.String())
}
