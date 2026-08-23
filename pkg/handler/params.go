package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

func invalidArguments(message string) *ToolError {
	return &ToolError{Code: "invalid_arguments", Message: message}
}

// fileParamHints are the substrings that show up in a parameter name an agent
// invented while trying to attach something. Matching substrings rather than an
// exact list keeps the signpost working for names nobody predicted.
var fileParamHints = []string{"file", "attach", "upload", "image", "photo", "screenshot", "media", "document"}

// signpostFileParam builds the error for a caller that passed a file-shaped
// parameter to a tool that cannot attach one. Naming files_upload matters more
// than rejecting the call. A client that loads tool schemas on demand holds no
// list to consult, so this error text is the only place it can learn that the
// capability exists at all; without it the caller concludes Slack cannot attach
// files and reaches for something outside the server entirely.
func signpostFileParam(request mcp.CallToolRequest, known ...string) *ToolError {
	for key := range request.GetArguments() {
		if slices.Contains(known, key) {
			continue
		}
		lower := strings.ToLower(key)
		for _, hint := range fileParamHints {
			if !strings.Contains(lower, hint) {
				continue
			}
			return invalidArguments(fmt.Sprintf(
				"%q is not a parameter of this tool, and this tool cannot attach files. To post a message with a file, call files_upload with channel_id and initial_comment: it uploads the file and posts the message together in one call.",
				key,
			))
		}
	}
	return nil
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
