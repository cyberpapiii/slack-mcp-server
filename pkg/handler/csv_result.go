package handler

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gocarina/gocsv"
	"github.com/korotovsky/slack-mcp-server/pkg/text"
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

// csvSection is a companion table that follows the main rows under its own
// "#<name>:" line, for rows the agent acts on with another tool (saved state,
// activity feed keys).
type csvSection struct {
	name string
	rows any
}

// csvResult is the one way a tool returns a table. rows is a pointer to a
// slice of csv-tagged structs; an empty slice renders the header alone, so a
// caller can always read the columns.
func csvResult(legend string, meta ResultMeta, rows any, sections ...csvSection) (*mcp.CallToolResult, error) {
	body, err := gocsv.MarshalString(rows)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal CSV rows: %w", err)
	}
	for _, section := range sections {
		table, err := gocsv.MarshalString(section.rows)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal %s: %w", section.name, err)
		}
		body += "#" + section.name + ":\n" + table
	}
	return NewCSVResult(legend, meta, body), nil
}

// encodeCursor and decodeCursor are the one cursor encoding this server
// hands out for paging it does itself: base64 of whatever the tool needs to
// resume. Cursors from Slack pass through untouched.
func encodeCursor(value string) string {
	return base64.StdEncoding.EncodeToString([]byte(value))
}

func decodeCursor(cursor string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return "", invalidCursor(cursor)
	}
	return string(decoded), nil
}

// encodePageCursor and decodePageCursor carry a 1-based page number. An
// empty cursor is page 1.
func encodePageCursor(page int) string {
	return encodeCursor("page:" + strconv.Itoa(page))
}

func decodePageCursor(cursor string) (int, error) {
	if cursor == "" {
		return 1, nil
	}
	value, err := decodeCursor(cursor)
	if err != nil {
		return 0, err
	}
	page, err := strconv.Atoi(strings.TrimPrefix(value, "page:"))
	if !strings.HasPrefix(value, "page:") || err != nil || page < 1 {
		return 0, invalidCursor(cursor)
	}
	return page, nil
}

func invalidCursor(cursor string) error {
	return invalidArguments(fmt.Sprintf("invalid cursor %q: pass the #next_cursor value from the previous call", cursor))
}

// timeText is how every tool renders an instant: RFC 3339 in UTC, "" for
// the zero time.
func timeText(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// unixTimeText renders Unix seconds; zero or negative means unset.
func unixTimeText(seconds int64) string {
	if seconds <= 0 {
		return ""
	}
	return timeText(time.Unix(seconds, 0))
}

// slackTsText renders a Slack "seconds.micros" timestamp and passes anything
// else through unchanged.
func slackTsText(ts string) string {
	if rendered, err := text.TimestampToIsoRFC3339(ts); err == nil {
		return rendered
	}
	return ts
}

// channelsLegend emits "#channels: C1=#general, D2=@bob" for every distinct
// ID that channelName can label, or "" when none can be named.
func channelsLegend(channelIDs []string, channelName func(string) string) string {
	if channelName == nil {
		return ""
	}
	seen := make(map[string]bool)
	var parts []string
	for _, id := range channelIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if name := channelName(id); name != "" {
			parts = append(parts, id+"="+name)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "#channels: " + strings.Join(parts, ", ") + "\n"
}

// Labels names conversations and users for legends and name columns.
// *provider.ApiProvider is the production implementation.
type Labels interface {
	// ChannelLabel is "#name" or "@user", or "" when unknown.
	ChannelLabel(channelID string) string
	// UserLabel is the real name, else the username, or "" when unknown.
	UserLabel(userID string) string
}
