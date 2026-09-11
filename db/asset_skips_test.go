package db

import (
	"sync"
	"testing"
)

func TestTaskAssetSkipLifecycle(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTaskWithOptions("skip memory", "goal", TaskCreateOptions{AssetApprovalTemplate: "explicit_targets"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	as := d.Assets()
	id, err := as.UpsertSubdomain(UpsertSubdomainReq{Domain: "cdn.skip.test", TaskID: task.ID, AgentDiscovered: true})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := as.RememberTaskAssetDenials(task.ID, []string{"CDN.SKIP.TEST", "cdn.skip.test"}, []int64{id})
	if err != nil || len(rows) != 1 || rows[0].Attempts != 1 || rows[0].State != ApprovalPending {
		t.Fatalf("first: %v %v", rows, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := as.RememberTaskAssetDenials(task.ID, []string{"cdn.skip.test"}, nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	rows, err = as.ActiveTaskAssetSkips(task.ID)
	if err != nil || len(rows) != 1 || rows[0].Attempts != 6 {
		t.Fatalf("repeat: %v %v", rows, err)
	}
	if err := as.ApproveTaskAssets(task.ID, []int64{id}, "user", ""); err != nil {
		t.Fatal(err)
	}
	rows, err = as.ActiveTaskAssetSkips(task.ID)
	if err != nil || len(rows) != 0 {
		t.Fatalf("approved: %v %v", rows, err)
	}
	rows, err = as.RememberTaskAssetDenials(task.ID, []string{"cdn.skip.test"}, nil)
	if err != nil || len(rows) != 0 {
		t.Fatalf("approved recheck: %v %v", rows, err)
	}
	if err := as.BlockTaskAssets(task.ID, []int64{id}, "user", ""); err != nil {
		t.Fatal(err)
	}
	rows, err = as.ActiveTaskAssetSkips(task.ID)
	if err != nil || len(rows) != 1 || rows[0].State != ApprovalBlocked {
		t.Fatalf("block: %v %v", rows, err)
	}
	rows, err = as.ActiveTaskAssetSkips(task.ID + 100000)
	if err != nil || len(rows) != 0 {
		t.Fatalf("task isolation: %v %v", rows, err)
	}
	if err := d.DeleteTask(task.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM task_asset_skips WHERE task_id=$1`, task.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("cleanup %d %v", n, err)
	}
}
