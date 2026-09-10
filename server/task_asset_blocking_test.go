package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func TestAssetBlockAPICancelsOnlyCurrentTaskWorkers(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip(err)
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	assets := pg.Assets()
	manager := &Manager{pg: pg, assets: assets, tasks: map[string]*Task{}}
	engine := &Engine{m: manager, work: map[int64]*workExecution{}}
	s := &Server{m: manager, engine: engine}
	var tasks []*db.Task
	var contexts []context.Context
	var root int64
	for i := 0; i < 2; i++ {
		task, err := pg.CreateTask("block API", "goal", nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer pg.DeleteTask(task.ID)
		tasks = append(tasks, task)
		key := strconv.FormatInt(task.ID, 10)
		manager.tasks[key] = &Task{ID: key, ExpID: task.ExplorationID}
		host := fmt.Sprintf("block-api-%d.test", tasks[0].ID)
		root, err = assets.UpsertRootDomain(db.UpsertRootDomainReq{Domain: host, TaskID: task.ID})
		if err != nil {
			t.Fatal(err)
		}
		child, err := assets.UpsertHTTPService(db.UpsertHTTPServiceReq{URL: "https://" + host, TaskID: task.ID})
		if err != nil {
			t.Fatal(err)
		}
		intent, err := pg.Exploration(task.ExplorationID).AddIntent(map[string]any{"summary": "test child"}, 5, []int64{child}, "planner")
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := pg.Exploration(task.ExplorationID).ClaimIntent(intent, "worker"); err != nil || !ok {
			t.Fatalf("claim: %v %v", ok, err)
		}
		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)
		contexts = append(contexts, ctx)
		engine.registerWork(intent, cancel)
	}
	for _, operation := range []string{"block", "approve"} {
		body, _ := json.Marshal(map[string]any{"asset_ids": []int64{root}})
		r := httptest.NewRequest(http.MethodPost, "/api/tasks/"+strconv.FormatInt(tasks[0].ID, 10)+"/asset-approvals/"+operation, bytes.NewReader(body))
		r.SetPathValue("id", strconv.FormatInt(tasks[0].ID, 10))
		w := httptest.NewRecorder()
		s.mutateTaskAssetApprovals(w, r, operation)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s", operation, w.Code, w.Body.String())
		}
		var response struct {
			Items []db.TaskAssetApproval `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		want := db.ApprovalBlocked
		if operation == "approve" {
			want = db.ApprovalApproved
		}
		if len(response.Items) != 1 || response.Items[0].ApprovalState != want {
			t.Fatalf("%s DTO: %+v", operation, response)
		}
	}
	code, _, _, ok := agent.AbortReason(contexts[0])
	if !ok || code != "asset_authorization_changed" {
		t.Fatalf("worker not cancelled: %s", code)
	}
	if contexts[1].Err() != nil {
		t.Fatal("other task worker was cancelled")
	}
	if err := assets.ValidateTaskAssetsApproved(tasks[1].ID, []int64{root}); err != nil {
		t.Fatal(err)
	}
}
