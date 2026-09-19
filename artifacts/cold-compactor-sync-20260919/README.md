# 冷节点压缩补同步

官方提交：96ff07579e8adfe8b720b1908b9c69c8ce3dcb47。
本地官方补丁提交：cadc4bc。

- 为 agentsForTask 实际创建的任务级 Planner 接入 Compactor，使用 plannerRuntime / task-router，与任务模型路由一致。
- 在官方补丁基础上补充 SetAssetStore(s.m.Assets())，保留本地压缩过程中的任务资产授权校验。
- noa 配置及其压缩机制保持不变。

新增 TestTaskPlannerAdvancesColdCompactionRounds，通过实际 agentsForTask / Planner.Plan 路径验证复用同一任务 bundle 时轮次逐次推进。为避免发起模型请求，测试在工具装配阶段显式终止。

修复前测试失败：round=0，预期 1（before.log）。修复后 server 全包测试通过；Agent 冷节点/摘要/压缩相关测试通过。

验证命令（均使用独立测试容器 127.0.0.1:55449 的 perf_server / perf_agent，未操作业务库）：

```
go test ./server -count=1
go test ./agent -run 'Test.*(Compac|Digest|Cold)' -count=1
git diff --check
```

未执行真实 LLM 压缩；轮次推进由新增集成回归覆盖，资产授权及压缩行为由 Agent 现有回归覆盖。
