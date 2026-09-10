package guard

import (
	"reflect"
	"testing"
)

func TestCollectAssetIDsSupportsStructuredToolShapes(t *testing.T) {
	value := map[string]any{
		"asset_id":  float64(4),
		"assetIds":  []any{float64(5), float64(4)},
		"unrelated": map[string]any{"asset-id": float64(6)},
	}
	if got, want := collectAssetIDs(value), []int64{4, 5, 6}; !reflect.DeepEqual(got, want) {
		t.Fatalf("collectAssetIDs=%v, want %v", got, want)
	}
}

func TestAssetPolicyAuditSubjectKeepsOnlyShellSurface(t *testing.T) {
	if got := assetPolicyAuditSubject("Bash", []byte(`{"command":"curl https://example.test"}`)); got != "curl https://example.test" {
		t.Fatalf("bash audit subject=%q", got)
	}
	if got := assetPolicyAuditSubject("custom_http", []byte(`{"url":"https://example.test"}`)); got != "" {
		t.Fatalf("non-shell audit subject=%q", got)
	}
}

func TestCollectHostsIncludesURLsIPv4AndRawIPv6(t *testing.T) {
	got := collectHosts(`{"url":"https://api.example.test/v1","ip":"198.51.100.7","ipv6":"2001:db8::7"}`)
	want := []string{"api.example.test", "198.51.100.7", "2001:db8::7"}
	for _, host := range want {
		found := false
		for _, candidate := range got {
			if candidate == host {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("collectHosts(%q)=%v, missing %q", host, got, host)
		}
	}
}
