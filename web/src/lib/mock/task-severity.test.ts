import assert from "node:assert/strict";
import test from "node:test";
import { mockHandle } from "./handler";
import type { Finding, Task } from "../types";

test("任务分级计数随漏洞删除刷新且任务隔离",async()=>{
 const findings=await mockHandle<Finding[]>("GET","/exploration/findings");
 const finding=findings.find(f=>f.task_id && ["critical","high","medium","low"].includes(f.severity));
 assert.ok(finding?.task_id);
 const before=await mockHandle<{tasks:Task[]}>("GET","/tasks");
 const counts=before.tasks.find(t=>t.id===finding.task_id)?.findings;
 assert.ok(counts);
 const severity=finding.severity as keyof typeof counts;
 assert.equal(counts[severity],findings.filter(f=>f.task_id===finding.task_id&&f.severity===severity).length);
 await mockHandle("DELETE",`/exploration/findings/${finding.id}`);
 const after=await mockHandle<{tasks:Task[]}>("GET","/tasks");
 assert.equal(after.tasks.find(t=>t.id===finding.task_id)?.findings?.[severity],counts[severity]-1);
 for(const task of before.tasks.filter(t=>t.id!==finding.task_id)) {
  assert.deepEqual(after.tasks.find(t=>t.id===task.id)?.findings,task.findings);
 }
});
