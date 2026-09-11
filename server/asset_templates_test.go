package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestTaskTemplateAndGroupAPI(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Fatal(err)
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	row, err := pg.CreateTaskWithOptions("template API", "goal", db.TaskCreateOptions{AssetApprovalTemplate: "explicit_targets"})
	if err != nil {
		t.Fatal(err)
	}
	defer pg.DeleteTask(row.ID)
	task := &Task{ID: strconv.FormatInt(row.ID, 10), ExpID: row.ExplorationID, AssetApprovalTemplate: row.AssetApprovalTemplate}
	as := pg.Assets()
	m := &Manager{pg: pg, assets: as, tasks: map[string]*Task{task.ID: task}}
	s := &Server{m: m, engine: &Engine{m: m}}
	host := fmt.Sprintf("www.group-api-%d.test", row.ID)
	var ids []int64
	for _, rt := range []string{"A", "AAAA"} {
		id, err := as.UpsertSubdomain(db.UpsertSubdomainReq{Domain: host, RecordType: rt, TaskID: row.ID, AgentDiscovered: true})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	key := fmt.Sprintf("%d|host:%s", row.ID, host)
	call := func(method, path, body string, run func(*httptest.ResponseRecorder, *testing.T)) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.SetPathValue("id", task.ID)
		w := httptest.NewRecorder()
		if method == "GET" {
			s.listTaskAssetApprovals(w, r)
		} else if method == "PUT" {
			s.updateTaskAssetTemplate(w, r)
		} else {
			s.mutateTaskAssetApprovals(w, r, "approve")
		}
		run(w, t)
	}
	call("POST", "/", fmt.Sprintf(`{"group_keys":[%q,"0|host:bad.test"]}`, key), func(w *httptest.ResponseRecorder, t *testing.T) {
		if w.Code != 400 {
			t.Fatalf("mixed groups=%d %s", w.Code, w.Body.String())
		}
	})
	if err := as.ValidateTaskAssetsApproved(row.ID, ids); err == nil {
		t.Fatal("partial API approval")
	}
	call("POST", "/", fmt.Sprintf(`{"group_keys":[%q]}`, key), func(w *httptest.ResponseRecorder, t *testing.T) {
		if w.Code != 200 {
			t.Fatalf("approve=%d %s", w.Code, w.Body.String())
		}
	})
	call("GET", "/?group_by=host", "", func(w *httptest.ResponseRecorder, t *testing.T) {
		var out struct {
			Items []db.TaskAssetApprovalGroup `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, g := range out.Items {
			if g.GroupKey == key {
				found = true
				if len(g.AssetIDs) != 2 || g.ApprovalState != "approved" {
					t.Fatalf("group=%+v", g)
				}
			}
		}
		if !found {
			t.Fatal("missing group")
		}
	})
	call("PUT", "/", `{"asset_approval_template":"all_assets"}`, func(w *httptest.ResponseRecorder, t *testing.T) {
		if w.Code != 200 {
			t.Fatalf("template=%d %s", w.Code, w.Body.String())
		}
	})
	if _, err := pg.StampFirstRun(row.ID, 0); err != nil {
		t.Fatal(err)
	}
	call("PUT", "/", `{"asset_approval_template":"explicit_targets"}`, func(w *httptest.ResponseRecorder, t *testing.T) {
		if w.Code != 400 {
			t.Fatalf("started template update=%d", w.Code)
		}
	})
}
