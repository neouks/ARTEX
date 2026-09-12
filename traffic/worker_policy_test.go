package traffic

import (
	"encoding/base64"
	"fmt"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/guard"
)

func TestWorkerProxyPendingAndManualLimits(t *testing.T) {
	dsn := os.Getenv("ARTEX_PG_DSN")
	if dsn == "" {
		t.Skip("requires explicit isolated ARTEX_PG_DSN")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTaskWithOptions("worker proxy", "goal", db.TaskCreateOptions{AssetApprovalTemplate: "explicit_targets"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	es := d.Exploration(task.ExplorationID)
	intent, err := es.AddNode(db.KindIntent, map[string]any{"summary": "proxy work"}, 1, "running", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	tr := &Traffic{assets: d.Assets()}
	request := func(scope string) bool {
		t.Helper()
		user, pass, ok := guard.TaskProxyCredentials(task.ID, scope)
		if !ok {
			t.Fatal("proxy signing unavailable")
		}
		req := httptest.NewRequest("GET", "https://pending.proxy.test/x", nil)
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pass)))
		allowed, err := tr.authorizeTaskRequest(httptest.NewRecorder(), req)
		if allowed && err != nil {
			t.Fatal(err)
		}
		if req.Header.Get("Proxy-Authorization") != "" {
			t.Fatal("proxy tag leaked")
		}
		return allowed
	}
	scope := fmt.Sprintf("worker:%d", intent)
	if !request(scope) {
		t.Fatal("worker pending blocked")
	}
	for _, scope := range []string{"planner", "mainagent", "", "worker:invalid", fmt.Sprintf("worker:%d", intent+99999)} {
		if request(scope) {
			t.Fatalf("untrusted/inactive scope allowed: %s", scope)
		}
	}
	id, err := d.Assets().UpsertRootDomain(db.UpsertRootDomainReq{Domain: "pending.proxy.test", TaskID: task.ID, AgentDiscovered: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Assets().BlockTaskAssets(task.ID, []int64{id}, "user", ""); err != nil {
		t.Fatal(err)
	}
	if request(scope) {
		t.Fatal("worker bypassed block")
	}
	if err := d.Assets().ApproveTaskAssets(task.ID, []int64{id}, "user", ""); err != nil {
		t.Fatal(err)
	}
	if !request(scope) {
		t.Fatal("approval did not restore")
	}
	if err := es.SetNodeState(intent, "done"); err != nil {
		t.Fatal(err)
	}
	if request(scope) {
		t.Fatal("completed worker token usable")
	}
}
