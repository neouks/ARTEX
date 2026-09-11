package guard

import (
	"encoding/base64"
	"testing"
)

func TestProxySkipScopeSignedAndCompatible(t *testing.T) {
	for _, scope := range []string{"", "worker:123", "planner", "mainagent"} {
		user, pass, _ := TaskProxyCredentials(17, scope)
		header := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
		id, got, tagged, err := ParseTaskProxyScope(header)
		if err != nil || !tagged || id != 17 || got != scope {
			t.Fatalf("roundtrip: %d %q %v %v", id, got, tagged, err)
		}
		if id, tagged, err := ParseTaskProxyAuthorization(header); err != nil || id != 17 || !tagged {
			t.Fatal("legacy parser compatibility")
		}
		other, _, _ := TaskProxyCredentials(17, "worker:999")
		if _, _, _, err := ParseTaskProxyScope("Basic " + base64.StdEncoding.EncodeToString([]byte(other+":"+pass))); err == nil {
			t.Fatal("scope tampering accepted")
		}
	}
}
