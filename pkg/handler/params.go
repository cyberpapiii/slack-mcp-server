package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
)

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
func signpostFileParam(request mcp.CallToolRequest) *ToolError {
	for key := range request.GetArguments() {
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
	if err == nil {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		err = decoder.Decode(output)
	}
	if err != nil {
		return &ToolError{Code: codeInvalidArguments, Message: "invalid tool arguments: " + err.Error(), Cause: err}
	}
	return nil
}

func requiredChannelID(request mcp.CallToolRequest) (string, error) {
	channelID := strings.TrimSpace(request.GetString("channel_id", ""))
	if channelID == "" {
		return "", invalidArguments("channel_id is required")
	}
	if kind := provider.ClassifyID(channelID); kind != provider.IDChannel && kind != provider.IDGroup {
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

// parseChoices reads raw, the comma-separated argument name, whose values
// must come from allowed. An empty argument gives def.
func parseChoices(name, raw string, allowed, def []string) ([]string, error) {
	var values []string
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if !slices.Contains(allowed, value) {
			return nil, unsupportedChoice(name, value, allowed)
		}
		if !slices.Contains(values, value) {
			values = append(values, value)
		}
	}
	if len(values) == 0 {
		return def, nil
	}
	return values, nil
}

// parseChoice is parseChoices for an argument that takes one value.
func parseChoice(name, raw string, allowed []string, def string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return def, nil
	}
	if !slices.Contains(allowed, value) {
		return "", unsupportedChoice(name, value, allowed)
	}
	return value, nil
}

func unsupportedChoice(name, value string, allowed []string) error {
	return invalidArguments(fmt.Sprintf("unsupported %s %q: must be one of %s", name, value, strings.Join(allowed, ", ")))
}

// quotedList renders values as 'a', 'b', 'c' for a schema description.
func quotedList(values []string) string {
	return "'" + strings.Join(values, "', '") + "'"
}

// positiveInt reads a count argument, substituting def when it is missing or
// non-positive. GetInt only applies its default to an absent key, so a
// negative value would otherwise pass through.
func positiveInt(request mcp.CallToolRequest, name string, def int) int {
	if n := request.GetInt(name, def); n > 0 {
		return n
	}
	return def
}

// pageLimit reads the limit argument like positiveInt, clamped to max when
// max is positive.
func pageLimit(request mcp.CallToolRequest, def, max int) int {
	limit := positiveInt(request, "limit", def)
	if max > 0 && limit > max {
		limit = max
	}
	return limit
}
