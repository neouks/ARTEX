package guard

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

func TestCommandTargetsDoNotTreatLocalFilesAsHosts(t *testing.T) {
	for _, tc := range []struct {
		command string
		hosts   []string
	}{
		{`curl.exe -s -k -i -m 30 "https://hogee.baidu.com/hogee/employee" -o resp_employee.txt -D headers_employee.txt; Get-Content headers_employee.txt | Select-Object -First 30; echo "----BODYLEN----"; (Get-Item resp_employee.txt).Length`, []string{"hogee.baidu.com"}},
		{`curl https://first.test -o "C:\work\resp_employee.txt"; curl second.test -D headers.txt`, []string{"first.test", "second.test"}},
		{`Get-Content headers.txt; python -c "open('home.html').read()"`, nil},
		{`nc target.test 443 > result.txt; dig api.test A`, []string{"api.test", "target.test"}},
		{`curl -O https://example.test/file.txt`, []string{"example.test"}},
		{`nmap -A target.test; curl example.test/path -o report.txt`, []string{"example.test", "target.test"}},
	} {
		input, _ := json.Marshal(map[string]string{"command": tc.command})
		got := collectHosts(string(input))
		sort.Strings(got)
		if len(got) != len(tc.hosts) || (len(got) > 0 && !reflect.DeepEqual(got, tc.hosts)) {
			t.Errorf("%s: got %v want %v", tc.command, got, tc.hosts)
		}
	}
}

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
	got := collectHosts(`{"url":"https://api.example.test/v1","host":"intranet2","internal":"http://intranet:8080/health","idn":"https://xn--fiqs8s.example/path","ip":"198.51.100.7","ipv6":"2001:db8::7"}`)
	want := []string{"api.example.test", "intranet", "intranet2", "xn--fiqs8s.example", "198.51.100.7", "2001:db8::7"}
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
