package capability

import (
	"slices"
	"testing"
)

func TestCatalogHasUniqueStableIDsAndOwners(t *testing.T) {
	seen := map[string]bool{}
	for _, entry := range Entries() {
		if entry.ID == "" || entry.Owner == "" || entry.Auth == "" || entry.Confirmation == "" || entry.ResultType == "" || entry.Migration == "" {
			t.Fatalf("incomplete catalog entry: %#v", entry)
		}
		if seen[entry.ID] {
			t.Fatalf("duplicate capability ID %q", entry.ID)
		}
		seen[entry.ID] = true
		if entry.Owner == OwnerOfficial && entry.OfficialAction == "" {
			t.Fatalf("official capability %q has no official action", entry.ID)
		}
		if entry.Owner != OwnerOfficial && entry.LocalTool == "" {
			t.Fatalf("local capability %q has no local tool", entry.ID)
		}
	}
}

func TestPresetsSeparateCanonicalAndLegacyTools(t *testing.T) {
	daily := DailyPowerLocalTools()
	legacy := LegacyFullLocalTools()
	if len(daily) == 0 || len(legacy) <= len(daily) {
		t.Fatalf("unexpected preset sizes: daily=%d legacy=%d", len(daily), len(legacy))
	}
	for _, tool := range daily {
		if !slices.Contains(legacy, tool) {
			t.Fatalf("daily tool %q missing from legacy preset", tool)
		}
	}
	for _, entry := range Entries() {
		if entry.Owner == OwnerOfficial {
			t.Fatalf("custom-only catalog must not delegate %q to official Slack MCP", entry.ID)
		}
	}
	for _, forbidden := range []string{"conversations_add_message", "conversations_draft_message", "conversations_search_messages", "files_list", "users_search"} {
		if slices.Contains(daily, forbidden) {
			t.Fatalf("official-owned duplicate %q present in daily preset", forbidden)
		}
	}
}

func TestDailyPowerToolsHaveNeutralBehaviorContracts(t *testing.T) {
	for _, tool := range DailyPowerLocalTools() {
		behavior, ok := BehaviorForLocalTool(tool)
		if !ok {
			t.Fatalf("daily-power tool %q has no behavior contract", tool)
		}
		if behavior.Title == "" || !behavior.ReadOnly || behavior.Destructive || !behavior.Idempotent || !behavior.OpenWorld {
			t.Fatalf("unexpected daily-power behavior for %q: %#v", tool, behavior)
		}
		entry, ok := EntryForLocalTool(tool)
		if !ok || entry.Confirmation != ConfirmationNone {
			t.Fatalf("confirmation policy missing or mixed into behavior for %q: %#v", tool, entry)
		}
	}
}

func TestVerifyInventoryRejectsMissingAndUnverifiedLocalIdentity(t *testing.T) {
	official := InventorySnapshot{
		CatalogVersion: CatalogVersion,
		Identity:       Identity{TeamID: "T1", UserID: "U1"},
		Tools:          []ObservedTool{{Name: "slack_read_thread", InputSchemaObject: true, StructuredResult: true, SemanticsVerified: true}},
	}
	host := HostInventory{
		CatalogVersion:   CatalogVersion,
		OfficialIdentity: Identity{TeamID: "T1", UserID: "U1"},
		LocalIdentity:    Identity{},
		Tools: []VisibleTool{
			{CapabilityID: "message.thread.read", Provider: OwnerOfficial, Name: "slack_read_thread"},
			{CapabilityID: "message.thread.read", Provider: OwnerLocal, Name: "conversations_replies"},
		},
	}

	report := VerifyInventory(official, host)
	if report.OK() {
		t.Fatal("invalid inventory passed")
	}
	for _, code := range []string{"identity_unverified", "missing_capability"} {
		if !report.Has(code) {
			t.Errorf("missing issue %q: %#v", code, report.Issues)
		}
	}
}

func TestVerifyInventoryRejectsExcludedAdministrationFamilies(t *testing.T) {
	report := VerifyInventory(InventorySnapshot{CatalogVersion: CatalogVersion}, HostInventory{
		CatalogVersion: CatalogVersion,
		Tools:          []VisibleTool{{CapabilityID: "workspace.admin", Provider: OwnerOfficial, Name: "slack_admin_workspaces"}},
	})
	if !report.Has("excluded_family") {
		t.Fatalf("excluded family passed: %#v", report.Issues)
	}
}

func TestVerifyInventoryRequiresOnlyLocalProviderIdentity(t *testing.T) {
	host := HostInventory{CatalogVersion: CatalogVersion}
	if report := VerifyInventory(InventorySnapshot{}, host); !report.Has("identity_unverified") {
		t.Fatalf("missing local identity passed: %#v", report.Issues)
	}
	host.LocalIdentity = Identity{TeamID: "T1", UserID: "U1"}
	if report := VerifyInventory(InventorySnapshot{}, host); report.Has("identity_unverified") || report.Has("identity_mismatch") {
		t.Fatalf("complete local identity failed: %#v", report.Issues)
	}
}
