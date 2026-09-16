# 资产审批来源定位核验（2026-09-16）

基线：main / c5ed9f1。仅修复两处可复现的前端缺口，无接口、迁移、审批权限或 Agent 策略变更。

## 已满足

| 项目 | 证据 |
| --- | --- |
| 首次成功登记来源、重复登记不覆盖、自动主机来源、批量部分失败和并发调用隔离 | db-final.log、agent.log；现有 AssetOrigin / ApprovalGroupOrigins / InsertAssetsInvocationOrigins 回归 |
| 主 Agent 多分段、Planner、Worker、继承资产打开来源任务 | scenarios-verified.log；真实后端、暂停测试任务、模拟工具事件 |
| 正确配对命令与结果，中间跨度超过常规页大小，前后连续分页 | db-final.log、pagination-verified.log；命令 231 与结果 447，前后分别浏览至 earlier-000、later-239 |
| 居中、高亮、展开完整命令与结果 | pagination-verified.log；中心偏差 0.78125 px |
| 快速切换后仅新来源高亮、未知来源不回退其他调用 | scenarios-verified.log |
| 结果迟到就地更新，用户滚动后不重新抢位置，返回最新清除定位 | scenarios-verified.log、late-result.png |
| 多来源选择、历史无精确来源、手动登记无猜测、返回恢复排序和行位置 | multiple-verified.log |
| 来源删除、跨任务/会话隔离、继承读取约束 | db-final.log、server-final.log |
| 原始审计未修改 | scenarios-verified.log：浏览器操作前后对原始活动 ID <= 694 的 row_to_json 聚合 MD5 相同；迟到结果仅新增测试活动 |

## 发现问题与修复结果

1. **详情失败时来源调用未居中。** 注入详情接口 503 后，旧调用位于视口中心下约 6,659.59 px，用户看不到重试按钮。`transcript.tsx` 将详情错误也视为可定位的已完成加载状态。修复后中心偏差 1.09 px；重试能显示真实接口结果。见 error-before.log、error-after.log、error-after.png。
2. **动作审批定位旧主 Agent 分段持续加载。** 有 main:0、main:1 时，初始化预留 main:0 加载标记，解析当前分段后释放标记，但旧分段仍被审批选中，未再次触发加载。`sessions-tab.tsx` 在当前分段解析变化时重检选中会话。真实浏览器动作审批跳转及详情失败重试均通过。

新增 `web/tests/asset-source-retry.cjs`，覆盖资产来源与动作审批来源的错误居中和重试。测试不主动滚动目标，避免掩盖居中问题；动作审批样例包含旧主会话分段。

## 验证环境与命令

独立 PostgreSQL 17 容器 artex-source-review-tests，127.0.0.1:55441；分别使用 source_db_tests、source_agent_tests、source_server_tests 和 source_browser。未连接业务数据库。浏览器为 Playwright 驱动的 Chrome，Next 开发服务使用 webpack，NEXT_PUBLIC_MOCK=0，连接真实 Go 服务。仅详情失败测试注入 503；其他响应来自真实接口。任务暂停、无模型配置，不执行真实目标探测。

以下命令均成功（DSN 指向各自独立测试库）：

```sh
GOSUMDB=sum.golang.org go test ./db -run 'Test(AssetOrigin|ApprovalGroupOrigins|ActivityWindow|ActivityPageSessions|InheritedActivityReadsRequireTerminalIntent)' -count=1
GOSUMDB=sum.golang.org go test ./agent -run TestInsertAssetsInvocationOrigins -count=1
GOSUMDB=sum.golang.org go test ./server -run 'Test(ActivityHistoryAnchorScope|InterceptDetailHTTP|InheritedIntentResultOnlyAllowsHistory|ToolCalls|InheritedActivityDetailAndRelationDeletion)' -count=1
web/node_modules/.bin/tsc --noEmit -p web/tsconfig.json
git diff --check
```

浏览器回归脚本使用环境变量 ARTEX_TEST_WEB、ARTEX_TEST_TOKEN、ARTEX_TEST_FIXTURE、PLAYWRIGHT_MODULE。fixture JSON 包含 task、anchor、result、approval；应在隔离服务准备至少两个主会话分段，anchor/result 为旧 main:0 中的 insert_assets 命令/结果，approval 指向同一调用。然后执行 `node web/tests/asset-source-retry.cjs`。令牌不包含在证据文件中。

## 范围说明

本次使用模拟工具事件验证真实 HTTP / 数据库 / 页面链路，没有让模型发起实际扫描。Worker 并发与重复登记依靠已有数据库/登记流程测试及代码核对；浏览器覆盖单个 Worker 会话。未运行全项目测试，也未声称覆盖所有设备尺寸和浏览器。业务原始审计未接触。未新增接口或回填历史来源。
