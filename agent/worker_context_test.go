package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestRenderIntentTaskUsesOnlyWorkerFacingFields(t *testing.T) {
	intent := &db.Node{
		ID:       42,
		Priority: 8,
		Payload:  json.RawMessage(`{"summary":"inspect <admin>","fingerprint":"internal-only","asset_ids":[7]}`),
	}
	got := renderIntentTask(intent)
	if !strings.Contains(got, `<intent id=42 priority=8>`) || !strings.Contains(got, "inspect &lt;admin&gt;") {
		t.Fatalf("worker intent fields missing:\n%s", got)
	}
	if strings.Contains(got, "fingerprint") || strings.Contains(got, "internal-only") {
		t.Fatalf("internal dedup metadata leaked into worker prompt:\n%s", got)
	}
}

func TestCompactWorkerAssetsOmitsEmptyNullableFields(t *testing.T) {
	port, status := 443, 200
	got := compactWorkerAssets([]*db.Asset{
		{ID: 1, Type: "service", URL: "https://example.test"},
		{ID: 2, Type: "service", URL: "https://example.test/admin", Port: &port, StatusCode: &status},
	})
	if _, exists := got[0]["port"]; exists {
		t.Fatalf("nil port rendered in compact asset: %#v", got[0])
	}
	if _, exists := got[0]["status"]; exists {
		t.Fatalf("nil status rendered in compact asset: %#v", got[0])
	}
	if got[1]["port"] != port || got[1]["status"] != status {
		t.Fatalf("non-empty asset fields lost: %#v", got[1])
	}
}
