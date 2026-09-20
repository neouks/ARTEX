# 官方 a590b41 同步记录

- 官方提交：a590b415baf4382e04fce044f4ec02e4d75aa539（norma v0.4.0 → v0.4.1）。
- 本地基线：5e423b6；备份分支：codex/backup-before-a590b41-20260920。
- 官方依赖文件 cherry-pick：a3892e2。
- 保留 go.mod 的本地 replace；将 norma v0.4.0..v0.4.1 的完整源码增量合入 third_party/norma，包含上游 9db3faa（v0.4.1 标签 77645cf）。

## 行为

压缩压力按裁剪后的请求投影视图计量；有效的 provider 输入用量作为下限，压缩前过期的用量仍被排除。保留本地较快压缩节奏、失败退避、工具上下文、授权刷新、MCP 元数据缓存及历史去重。

## 测试适配

初次全量运行 3 个失败：

1. 官方原版 v0.4.1 也能复现 TestTierTwoIsUnreachableOnSmallWindows 失败：旧测试断言缺陷仍存在。改为断言本地默认配置下小窗口能够进入二级压缩。
2. 固定 120 轮的压力回归假定上游 50,000/20,000 cadence，而本地默认值为 2,000/1,000，正常间隔就是 8 轮。为该官方计量回归固定上游配置，不改变产品默认值。
3. 固定 182 轮的长会话二级压缩测试同样固定上游 cadence，确保生成足够的摘要积压。更快的本地默认节奏另由小窗口端到端测试覆盖。

没有为通过测试改变运行时压缩阈值或禁用测试。

## 验证

- 本地 norma：go test ./... -count=1 全量通过，见 norma.log。
- ARTEX：go test ./... -run '^$' 全包编译通过。
- Agent：noa 开关、归档失败回退、模型窗口、审批刷新、延迟工具与 Skill 解锁回归通过，见 agent.log。使用独立容器 artex-sync-20260919 的 perf_agent 测试库；不使用业务库。
- Server：MCPRuntime、NormalizeMCPServer、MeterMCPTool 回归及 race 检查通过。
- noa/noaadapter：新计量与二级压缩专项 race 检查见 noa-race.log。
- git diff --check 通过。

本次无前端变更，不需要浏览器交互测试。不推送远端。
