import type { Asset, Task, TaskAssetApproval } from "@/lib/types";

import { mockHandle } from "./handler";
import assert from "node:assert/strict";
import test from "node:test";

test("服务不独立审批，删除仅隐藏记录且允许重新关联", async () => {
  const inventory = await mockHandle<{ assets: Asset[] }>("GET", "/assets?type=service");
  const asset = inventory.assets[0];
  assert.ok(asset);
  const task = await mockHandle<Task>(
    "POST",
    "/tasks",
    JSON.stringify({
      description: "主机授权回归",
      goal: "test",
      asset_ids: [asset.id],
      asset_approval_template: "explicit_targets",
    }),
  );
  const approvals = await mockHandle<{ items: TaskAssetApproval[] }>("GET", `/tasks/${task.id}/asset-approvals`);
  assert.ok(approvals.items.every((row) => ["root_domain", "subdomain", "ip"].includes(row.asset_type)));
  await assert.rejects(
    mockHandle("POST", `/tasks/${task.id}/asset-approvals/block`, JSON.stringify({ asset_ids: [asset.id] })),
  );
  await mockHandle("DELETE", `/tasks/${task.id}/assets/${asset.id}`);
  const list = await mockHandle<{ assets: Asset[] }>("GET", `/assets?task_id=${task.id}&type=service`);
  assert.ok(!list.assets.some((row) => row.id === asset.id));
  await mockHandle(
    "POST",
    `/tasks/${task.id}/assets`,
    JSON.stringify({ asset_ids: [asset.id], source_summary: "用户重新关联" }),
  );
  const restored = await mockHandle<{ assets: Asset[] }>("GET", `/assets?task_id=${task.id}&type=service`);
  assert.equal(restored.assets.find((row) => row.id === asset.id)?.approval_state, "approved");
});

test("任务模板默认值、分组原子校验和手动封禁恢复", async () => {
  const inventory = await mockHandle<{ assets: Asset[] }>("GET", "/assets?type=root_domain");
  const asset = inventory.assets[0];
  assert.ok(asset);
  const task = await mockHandle<Task>(
    "POST",
    "/tasks",
    JSON.stringify({ description: "模板回归", goal: "test", asset_ids: [asset.id] }),
  );
  assert.equal(task.asset_approval_template, "all_assets");
  const list = () =>
    mockHandle<{ items: TaskAssetApproval[] }>("GET", `/tasks/${task.id}/asset-approvals?group_by=host`);
  const group = (await list()).items.find((item) => item.asset_ids?.includes(asset.id));
  assert.ok(group?.group_key);
  assert.equal(group.approval_state, "approved");
  await assert.rejects(
    mockHandle(
      "POST",
      `/tasks/${task.id}/asset-approvals/block`,
      JSON.stringify({ group_keys: [group.group_key, "0|host:not-a-group.test"] }),
    ),
  );
  assert.equal((await list()).items.find((item) => item.group_key === group.group_key)?.approval_state, "approved");
  await mockHandle(
    "POST",
    `/tasks/${task.id}/asset-approvals/block`,
    JSON.stringify({ group_keys: [group.group_key] }),
  );
  assert.equal((await list()).items.find((item) => item.group_key === group.group_key)?.approval_state, "blocked");
  await mockHandle(
    "POST",
    `/tasks/${task.id}/asset-approvals/approve`,
    JSON.stringify({ group_keys: [group.group_key] }),
  );
  assert.equal((await list()).items.find((item) => item.group_key === group.group_key)?.approval_state, "approved");
  await mockHandle(
    "PUT",
    `/tasks/${task.id}/asset-approval-template`,
    JSON.stringify({ asset_approval_template: "explicit_targets" }),
  );
  assert.equal((await mockHandle<Task>("GET", `/tasks/${task.id}`)).asset_approval_template, "explicit_targets");
  await assert.rejects(
    mockHandle(
      "POST",
      "/tasks",
      JSON.stringify({ description: "bad", goal: "test", asset_approval_template: "unknown" }),
    ),
  );
});
