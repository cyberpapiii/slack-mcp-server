package handler

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tools that decodeArguments into a provider request struct name each
// parameter twice: in the schema and in the struct's json tags. A rename on
// one side would make the decoder reject the advertised parameter, so the two
// lists must match. A draft also carries fields Slack sets (updated_ts), so
// for the drafts tools every schema parameter must be a struct field.
func TestUnitDecodedSchemasMatchTheirStructs(t *testing.T) {
	var defs []ToolDef
	defs = append(defs, NewCanvasHandler(nil).Tools()...)
	defs = append(defs, NewListsHandler(nil, nil).Tools()...)
	defs = append(defs, NewSemanticSearchHandler(nil, nil).Tools()...)
	defs = append(defs, NewDraftsHandler(nil, nil, nil).Tools()...)
	schemas := map[string][]string{}
	for _, def := range defs {
		tool := mcp.NewTool(def.Name, def.Options...)
		var names []string
		for name := range tool.InputSchema.Properties {
			names = append(names, name)
		}
		sort.Strings(names)
		schemas[def.Name] = names
	}

	exact := map[string]any{
		capability.CanvasesCreate:   provider.CanvasCreateRequest{},
		capability.CanvasesRead:     provider.CanvasReadRequest{},
		capability.CanvasesUpdate:   provider.CanvasUpdateRequest{},
		capability.SearchSemantic:   provider.SemanticSearchRequest{},
		capability.ListsCreate:      provider.CreateListRequest{},
		capability.ListsUpdate:      provider.UpdateListRequest{},
		capability.ListsItemsList:   provider.ListItemsRequest{},
		capability.ListsItemsCreate: provider.CreateListItemRequest{},
		capability.ListsItemsUpdate: provider.UpdateListItemsRequest{},
	}
	for tool, decoded := range exact {
		require.Contains(t, schemas, tool)
		assert.Equal(t, jsonFields(decoded), schemas[tool], tool)
	}
	for _, tool := range []string{capability.DraftsCreate, capability.DraftsUpdate} {
		require.Contains(t, schemas, tool)
		assert.Subset(t, jsonFields(provider.Draft{}), schemas[tool], tool)
	}
}

// jsonFields is the sorted list of json names a struct decodes.
func jsonFields(value any) []string {
	var names []string
	typ := reflect.TypeOf(value)
	for i := range typ.NumField() {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
