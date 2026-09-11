package db

import (
	"fmt"
	"testing"
)

func TestHostOnlyApprovalTemplates(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, template := range []string{"all_assets", "related_assets", "explicit_targets"} {
		t.Run(template, func(t *testing.T) {
			task, err := d.CreateTaskWithOptions("测试 https://www.host-policy.test", "goal", TaskCreateOptions{AssetApprovalTemplate: template})
			if err != nil {
				t.Fatal(err)
			}
			defer d.DeleteTask(task.ID)
			as := d.Assets()
			hostID, err := as.RegisterDescriptionAsset(task.ID, "host", "www.host-policy.test", "测试 https://www.host-policy.test")
			if err != nil {
				t.Fatal(err)
			}
			_, err = as.UpsertSubdomain(UpsertSubdomainReq{Domain: "api.host-policy.test", RecordType: "A", RecordValue: []string{"192.0.2.179"}, TaskID: task.ID, AgentDiscovered: true})
			if err != nil {
				t.Fatal(err)
			}
			states, err := as.TaskHostApprovalStates(task.ID, []string{"www.host-policy.test", "api.host-policy.test", "192.0.2.179", "unrelated.test"})
			if err != nil {
				t.Fatal(err)
			}
			for host, want := range map[string]bool{"www.host-policy.test": true, "api.host-policy.test": template != "explicit_targets", "192.0.2.179": template != "explicit_targets", "unrelated.test": template == "all_assets"} {
				if (states[host] == ApprovalApproved) != want {
					t.Fatalf("%s: %s", host, states[host])
				}
			}
			for _, port := range []int{80, 443, 8443, 5432} {
				id, err := as.UpsertEndpoint(UpsertEndpointReq{URL: fmt.Sprintf("https://www.host-policy.test:%d/new/path", port), Method: "POST", TaskID: task.ID, AgentDiscovered: true})
				if err != nil {
					t.Fatal(err)
				}
				if err := as.ValidateTaskAssetsApproved(task.ID, []int64{id}); err != nil {
					t.Fatal(err)
				}
				if err := as.ApproveTaskAssets(task.ID, []int64{id}, "user", ""); err == nil {
					t.Fatal("endpoint must not support independent approval")
				}
				if _, err := as.DetachAssetFromTask(task.ID, id); err != nil {
					t.Fatal(err)
				}
				if err := as.ValidateTaskAssetsApproved(task.ID, []int64{id}); err != nil {
					t.Fatalf("record deletion restricted host: %v", err)
				}
			}
			if err := as.BlockTaskAssets(task.ID, []int64{hostID}, "user", ""); err != nil {
				t.Fatal(err)
			}
			if err := as.ValidateTaskHostsApproved(task.ID, []string{"www.host-policy.test"}); err == nil {
				t.Fatal("manual block bypassed")
			}
			if err := as.ApproveTaskAssets(task.ID, []int64{hostID}, "user", ""); err != nil {
				t.Fatal(err)
			}
			if err := as.ValidateTaskHostsApproved(task.ID, []string{"www.host-policy.test"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTaskGrantDoesNotConsumeTaskSequence(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTaskWithOptions("sequence ownership", "goal", TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	var before, after int64
	if err := d.QueryRow(`SELECT last_value FROM tasks_id_seq`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO task_asset_grants(task_id,kind,value,source) SELECT $1,'host','grant-'||n||'.test','manual' FROM generate_series(1,100) n`, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT last_value FROM tasks_id_seq`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("grants consumed task sequence: %d -> %d", before, after)
	}
}
