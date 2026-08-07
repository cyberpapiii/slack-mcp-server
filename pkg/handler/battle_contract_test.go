package handler

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/text"
)

func checkHandlerBattleGolden(t *testing.T, name, got string) {
	t.Helper()

	path := filepath.Join("..", "..", "testdata", "battle", "contracts", name)
	if os.Getenv("UPDATE_BATTLE_GOLDENS") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create golden directory: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with UPDATE_BATTLE_GOLDENS=1 to capture)", path, err)
	}
	if got != string(want) {
		t.Fatalf("contract drift in %s\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}

func TestBattleContractMessageCSVLegend(t *testing.T) {
	result, err := marshalMessagesToCSV(
		compactCSVFixtureMessagesN(4),
		renderOptions{mode: text.ModeStandard, workspaceURL: "https://loop.slack.com/"},
	)
	if err != nil {
		t.Fatalf("marshal compact messages: %v", err)
	}
	checkHandlerBattleGolden(t, "message-csv-legend.txt", csvResultBody(t, result))
}

func BenchmarkBattleMarshalMessagesCompact(b *testing.B) {
	messages := compactCSVFixtureMessagesN(100)
	opts := renderOptions{mode: text.ModeStandard, workspaceURL: "https://loop.slack.com/"}

	b.ReportAllocs()
	for b.Loop() {
		result, err := marshalMessagesToCSV(messages, opts)
		if err != nil {
			b.Fatal(err)
		}
		if result == nil {
			b.Fatal("nil result")
		}
	}
}
