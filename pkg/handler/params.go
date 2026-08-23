package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

func invalidArguments(message string) *ToolError {
	return &ToolError{Code: "invalid_arguments", Message: message}
}

// decodeArguments parses the request arguments into output, rejecting unknown
// fields so a misspelled parameter fails loudly instead of being ignored.
func decodeArguments(request mcp.CallToolRequest, output any) error {
	raw, err := json.Marshal(request.GetArguments())
	if err != nil {
		return errors.New("invalid tool arguments")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("invalid tool arguments: %w", err)
	}
	return nil
}

func requiredChannelID(request mcp.CallToolRequest) (string, error) {
	channelID := strings.TrimSpace(request.GetString("channel_id", ""))
	if channelID == "" {
		return "", invalidArguments("channel_id is required")
	}
	if !strings.HasPrefix(channelID, "C") && !strings.HasPrefix(channelID, "G") {
		return "", invalidArguments("channel_id must be a public or private channel ID starting with C or G")
	}
	return channelID, nil
}

// presentString reads a string that must be present but may be empty, for
// fields where an empty value means "clear".
func presentString(request mcp.CallToolRequest, name string) (string, error) {
	value, present := request.GetArguments()[name]
	if !present {
		return "", invalidArguments(name + " is required; pass an empty string to clear it")
	}
	text, ok := value.(string)
	if !ok {
		return "", invalidArguments(name + " must be a string")
	}
	return text, nil
}

// pageLimit reads the limit argument, substituting def when it is missing or
// non-positive and clamping to max when max is positive. GetInt only applies
// its default to an absent key, so a negative limit would otherwise pass through.
func pageLimit(request mcp.CallToolRequest, def, max int) int {
	limit := request.GetInt("limit", def)
	if limit <= 0 {
		limit = def
	}
	if max > 0 && limit > max {
		limit = max
	}
	return limit
}
