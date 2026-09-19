# 官方提交同步清单（2026-09-19）

官方仓库：[Autumn-27/ARTEX](https://github.com/Autumn-27/ARTEX/commits/main/)。本地基线 `e2653bb`，同步分支 `codex/upstream-sync-20260919`，备份 `codex/backup-before-sync-20260919`。

工作区开始时干净。隔离工作区整合及验证后，将 main 快进到同步结果；不推送、不重写历史。仅整合下面指定提交及节点搜索所需的播报板基础依赖，不整体合并 upstream/main。

## 逐提交处理

| 官方提交 | 处理 | 本地对应 / 说明 |
| --- | --- | --- |
| a91c0a72230851a5d787e5d699a3c77fb2ebb4e1 | 引入 | `a92eb43`；通过 13ce2ea 的第一父差异引入筛选及其正式合并修正，避免重复应用 |
| 1c6342d8b33454bdda49e70019c52fcd726d8a74 | 引入 | `a76c829`；待审批队列独立于历史分页，保留本地通知已读逻辑 |
| f6b5c11352469b9c0a372a53056deb766f59a64f | 引入并适配 | `b5cb4c3`；多类型 @ 引用和分页；保留本地 Worker 控制锁、Guard、工具调用列表、全局代理接口；不恢复本地已移除的 report 接口 |
| d5c1995641acb5e89ecaa79d4510ba2290e3b914 | 已覆盖 | 本地长消息容器约束与官方补丁相同，应用结果为空；保留来源详情失败居中修复 |
| 5fbeb35b8cb23d848f97bee2d7714238d92dba9c | 本地已覆盖修复目的 | 本地 MockFindingTraffic 已支持空绑定、绑定/详情/正文/编辑/解绑/排序，同时有版本及继承只读校验；保留该实现和本地示例数据，不替换成官方平行 mock 状态 |
| 6d0614ae798ed531776b5bf3d133bafaf673b1eb | 合并核对 | 5fbeb35 的合并，无额外冲突解决差异，不重复应用 |
| a15285a7ae1091635df5bfce6d518faf149d65b8 | 合并核对 | 两侧对应功能已处理，无额外冲突解决差异 |
| 23666365758a5826d8efd3a0492579bc3e774a51 | 引入 | `f714c3a`；通用网络安全平台角色描述 |
| 13ce2ea6e3c00ad1e8cc9c92a6fd81ed6f8dba96 | 引入 | `a92eb43`；保留官方冲突解决：筛选不影响待审批队列，决策后重取筛选计数；同时保留本地 beginRead |
| 6e3dbf645293a04cfa5a7b39659b7f088304d018 | 引入 | `6c84607`；清空 file input 前复制 FileList，修复首次异步创建对话后的上传 |
| 4756bb1c45f0687a3440b782acf272f84a4e68a3 | 设计冲突，保留本地 | **未引入官方软删除/级联真删除模式。** 官方改变 cancel 语义、必填原因及 Planner 重新规划；本地取消执行可选原因、保留产出、仅用户重新开启、独立删除及等待队列控制是完整的既有设计，按用户要求保留 |
| 8a3f40b6f8107f32702db5b7d212cc91ee69efcf | 同上 | 4756bb1 的合并，无额外差异；不绕过上述本地设计再应用一次 |
| ec0ae108c4e8e9e27774af6c2a9f56b567dd8bbb | 引入 | `9d2f412`；第一父差异包含节点 ID / #ID 搜索及测试 |

直接依赖：`c03e008d7bf6a35bffafd35b03e9b9671e2ac4ee` → `13842f8`。节点 ID 搜索依赖其 NodesPage API 与播报板页面，本地此前没有该功能。使用本地动态加载及任务通知容器；未引入其后无关的引擎重构、冷压缩、报告页及复测页恢复，也未额外引入后续播报板资产卡片功能。

测试适配：`8b9d73c` 为官方新 Worker 引用测试绑定自己的数据库工具目录，解决全量运行时继承已关闭数据库 resolver 的问题；不放宽生产授权检查。

## 本地设计保护

- 资产审批预检、逐项授权、登记来源、来源定位、默认未审批与登记时间排序保持。
- 漏洞删除反馈、Worker 取消/恢复/删除、等待队列、通知计数保持。
- `third_party/norma`、noa 修复及压缩后上下文刷新不变。
- 两类来源定位可共存，保留详情失败居中及旧 main 分段加载修复。
- 本次无数据库 schema 迁移；新增引用查询和节点分页使用现有表。

## 验证

独立 PostgreSQL 17 容器 `artex-sync-20260919`，仅绑定 `127.0.0.1:55449`；数据库为 sync_db / sync_agent / sync_server / sync_browser，未使用业务数据库。以下全部通过：

```sh
# 分别设置 ARTEX_PG_DSN 到上述对应测试库；GOSUMDB=sum.golang.org
go test ./db -count=1       # 31.301s
go test ./agent -count=1    # 13.299s
go test ./server -count=1   # 修正测试隔离后 26.076s
go build ./...
web/node_modules/.bin/tsc --noEmit -p web/tsconfig.json
# web 目录，使用已安装 tsx loader
node --import <tsx-loader> --test src/lib/*.test.* src/lib/mock/*.test.*
# 30 项通过，无失败
git diff --check
```

服务端首次全量运行仅新测试 TestChatMentionWorkerReceivesServerDetails 失败（上一测试关闭的数据库配置残留），修正后整个 server 包通过，最终日志见 server-final.log。

真实接口 Chrome 验证：Next webpack 服务 `3139` → Go `18799` → sync_browser，NEXT_PUBLIC_MOCK=0。仅来源错误测试注入 503；无真实扫描，暂停测试任务配合模拟工具事件。

- `features.log`：节点 ID 搜索；历史筛选不隐藏待处理审批；从真实目录选择 @ 引用；草稿对话首次上传成功。
- `source-browser.log`：资产来源与动作审批来源详情失败后居中并可重试。
- `local-browser.log`：主 Agent 多分段、Planner、Worker、继承来源任务、快速切换、未知来源不误定位、迟到结果保留滚动位置、返回最新、两类审批兼容；测试原始审计记录前后聚合校验一致。
- `web-tests.log`：包括本地资产来源、漏洞删除、Worker 取消/队列、审批来源及流量 mock 回归。
- 截图：broadcast.png、approval-filter.png、mention.png。

未执行真实 LLM 扫描，也未把所有官方删除行为宣称为已引入；冲突保留项如上表所列。
