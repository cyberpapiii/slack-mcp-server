package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"
)

const updateBattleGoldensEnv = "UPDATE_BATTLE_GOLDENS"

func checkBattleGolden(t *testing.T, name string, got []byte) {
	t.Helper()

	path := filepath.Join("..", "..", "testdata", "battle", "contracts", name)
	if os.Getenv(updateBattleGoldensEnv) == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create golden directory: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with %s=1 to capture)", path, err, updateBattleGoldensEnv)
	}
	if string(got) != string(want) {
		t.Fatalf("contract drift in %s\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}

func TestBattleContractToolRegistrationSurface(t *testing.T) {
	checkBattleGolden(t, "tool-registration-surface.txt", []byte(strings.Join(ValidToolNames, "\n")+"\n"))
}

func TestBattleContractErrorTaxonomy(t *testing.T) {
	type errorSample struct {
		Class         string `json:"class"`
		ProtocolError bool   `json:"protocol_error"`
		IsError       bool   `json:"is_error"`
		ContentType   string `json:"content_type"`
		Text          string `json:"text"`
	}

	middleware := buildErrorRecoveryMiddleware(zap.NewNop())
	handler := middleware(func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, fmt.Errorf("simulated tool error: invalid channel ID")
	})
	result, err := handler(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("middleware returned protocol error: %v", err)
	}
	if result == nil || len(result.Content) != 1 {
		t.Fatalf("middleware result = %#v, want one content item", result)
	}
	content, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("content type = %T, want mcp.TextContent", result.Content[0])
	}

	got, err := json.MarshalIndent(errorSample{
		Class:         "handler_error",
		ProtocolError: false,
		IsError:       result.IsError,
		ContentType:   content.Type,
		Text:          content.Text,
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal error sample: %v", err)
	}
	checkBattleGolden(t, "error-taxonomy.json", append(got, '\n'))
}

func BenchmarkBattleValidateEnabledTools(b *testing.B) {
	valid := append([]string(nil), ValidToolNames...)
	b.ReportAllocs()
	for b.Loop() {
		if err := ValidateEnabledTools(valid); err != nil {
			b.Fatal(err)
		}
	}
}

var _ mcpserver.ToolHandlerMiddleware = buildErrorRecoveryMiddleware(zap.NewNop())
