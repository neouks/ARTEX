package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func TestTaskAssetApprovalsRejectDerivedAssetsAtomically(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skipf("no database configured: %v", err)
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	persisted, err := pg.CreateTask("derived approval API", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.DeleteTask(persisted.ID) })
	assets := pg.Assets()
	host := fmt.Sprintf("approval-api-%d.test", time.Now().UnixNano())
	parentID, err := assets.UpsertRootDomain(db.UpsertRootDomainReq{Domain: host, TaskID: persisted.ID})
	if err != nil {
		t.Fatal(err)
	}
	serviceID, err := assets.UpsertHTTPService(db.UpsertHTTPServiceReq{URL: "https://" + host, TaskID: persisted.ID})
	if err != nil {
		t.Fatal(err)
	}
	endpointID, err := assets.UpsertEndpoint(db.UpsertEndpointReq{URL: "https://" + host + "/api", Method: "GET", TaskID: persisted.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = assets.DeleteByIDs([]int64{parentID, serviceID, endpointID}) })
	task := &Task{ID: strconv.FormatInt(persisted.ID, 10), ExpID: persisted.ExplorationID}
	manager := &Manager{pg: pg, assets: assets, tasks: map[string]*Task{task.ID: task}}
	s := &Server{m: manager, engine: &Engine{m: manager}}

	for _, operation := range []string{"approve", "revoke", "block"} {
		initialState := db.ApprovalApproved
		if operation == "approve" {
			initialState = db.ApprovalPending
		}
		for _, derivedID := range []int64{serviceID, endpointID} {
			for _, mixed := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%d/mixed=%v", operation, derivedID, mixed), func(t *testing.T) {
					if _, err := pg.Exec(`UPDATE task_asset_links SET approval_state=$3 WHERE task_id=$1 AND asset_id=$2`, persisted.ID, parentID, initialState); err != nil {
						t.Fatal(err)
					}
					ids := []int64{derivedID}
					if mixed {
						ids = []int64{parentID, derivedID}
					}
					body, err := json.Marshal(map[string]any{"asset_ids": ids})
					if err != nil {
						t.Fatal(err)
					}
					r := httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/asset-approvals/"+operation, bytes.NewReader(body))
					r.SetPathValue("id", task.ID)
					w := httptest.NewRecorder()
					s.mutateTaskAssetApprovals(w, r, operation)
					if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "服务和接口无需单独审批，请操作父域名/IP") {
						t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
					}
					var state string
					if err := pg.QueryRow(`SELECT approval_state FROM task_asset_links WHERE task_id=$1 AND asset_id=$2`, persisted.ID, parentID).Scan(&state); err != nil {
						t.Fatal(err)
					}
					if state != initialState {
						t.Fatalf("rejected mixed request changed parent state to %q; want %q", state, initialState)
					}
				})
			}
		}
	}

	if detached, err := assets.DetachAssetFromTask(persisted.ID, serviceID); err != nil || !detached {
		t.Fatalf("detach service: detached=%v err=%v", detached, err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/asset-approvals", nil)
	r.SetPathValue("id", task.ID)
	w := httptest.NewRecorder()
	s.listTaskAssetApprovals(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Items []db.TaskAssetApproval `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 1 || response.Items[0].AssetID != parentID {
		t.Fatalf("approval list must contain only the host, excluding linked endpoint and service tombstone: %+v", response.Items)
	}
}
