package db

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"testing"
	"time"
)

func TestManualScopeAcceptsArbitraryInput(t *testing.T) {
	for _, value := range []string{"10.0.0.0/8", "999.1.2.3", "https://example.test:8443/Case?Token=AbC", "HTTP GET /", "*.example.test", "证书 SHA256: AbCd", "京ICP备123号"} {
		rule, err := ParseScopeInput(ScopeInput{Value: value, Manual: true})
		if err != nil || rule.Raw != value {
			t.Fatalf("input %q: rule=%+v err=%v", value, rule, err)
		}
		if rule.Kind == "keyword" && rule.Value != value {
			t.Fatalf("raw text changed: %q -> %q", value, rule.Value)
		}
	}
}

func TestManualCertificateScopeRoundTrip(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	data := make([]byte, 6000)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	certificate := "-----BEGIN CERTIFICATE-----\n" + base64.StdEncoding.EncodeToString(data) + "\n-----END CERTIFICATE-----"
	input := []ScopeInput{{Value: certificate, Manual: true}}
	companyID, added, _, invalid, _, err := d.Companies().CreateCompanyWithScope(fmt.Sprintf("certificate-%d", time.Now().UnixNano()), "", input, "test")
	if err != nil || invalid != 0 || added != 1 {
		t.Fatalf("company: added=%d invalid=%d err=%v", added, invalid, err)
	}
	defer d.Companies().DeleteCompany(companyID)
	var stored string
	if err := d.QueryRow(`SELECT value FROM company_scope WHERE company_id=$1`, companyID).Scan(&stored); err != nil || stored != certificate {
		t.Fatalf("company roundtrip: %v", err)
	}
	task, err := d.CreateTask("certificate scope", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	for i := 0; i < 2; i++ {
		if _, err := d.Assets().RegisterTaskAssetScopes(task.ID, input); err != nil {
			t.Fatal(err)
		}
	}
	scopes, err := d.Assets().ListTaskScope(task.ID)
	if err != nil || len(scopes) != 1 || scopes[0].Value != certificate {
		t.Fatalf("task certificate roundtrip: count=%d err=%v", len(scopes), err)
	}
}
