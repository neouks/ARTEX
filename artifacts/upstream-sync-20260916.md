# 指定官方提交同步记录（2026-09-16）

## 范围与保护

官方来源：`upstream`（`https://github.com/Autumn-27/ARTEX.git`）。仅整合以下 15 项指定提交的效果。

- 原 `main` 的已跟踪及未跟踪改动已保存为 `8c1426b`（资产审批来源定位、排序等）。
- 备份引用：`codex/backup-before-official-sync-20260916` → `8c1426b`。
- 同步分支：`codex/sync-official-20260916`；隔离工作区：`/tmp/artex-sync-official-20260916`。
- 功能整合提交：`6aff823`；随后文档提交为交付端点。
- 当前 `main` 保持检查点，尚未快进：等待确认是否接受以下官方 noa 基线失败。
- 不推送远端、不重写历史。未引入 `f4d89c6`、`8ee8907` 的补丁。

## 逐项对应

| 官方 SHA | 本地对应 | 结果 / 处理 |
| --- | --- | --- |
| `7c8b55ca6c0d86a384a016043a88c22c7c30fd4d` | `e01eb3a` | 已存在：带端口的流量搜索；核对实际代码及回归，不重复应用。 |
| `5db71f7c536e5891e4abd28f7d45dd21d43e720a` | `bd176c2` | 已存在：证据迁移；patch-id 相同。 |
| `71fa9195993ebffbea5aba06bc03b1c39664d5e3` | `669b2a7` | 已存在：审批分页；保留本地通知与详情读取扩展。 |
| `1a388e3e9ed93b10b7c3e12f9a2b30214251873b` | `cdbc72d` | 已存在：旧式 MCP SSE；保留本地输入校验。 |
| `5963281582072d1025b3dae575767b6665e50d32` | `3f7d42a` | 已存在：无任务工具保护；保留逐项资产校验和 Shell 上下文。 |
| `234bd8bf8ea2b63268d094d0c96a6fa9945967df` | `2aee715` | 新引入：旁路 SSE 扫描失败直接报告。 |
| `c0d1aa6f3792b04a6a26fe9885aba91d840f2377` | `d1bebf4` | 新引入：模型审查上下文和说明；目录声明沿用本地 Shell 初始化顺序。 |
| `0d6fa880b8b67184c11b6d927a4bf36b4662e60f` | `0ad00b5` | 新引入：解析代码块内的审查裁决，不再静默放行。 |
| `4ae319feca50a483c57253e111b9ec455560916c` | `53b9e3b`、`005a603` | 版本升级，并将 v0.3.8→v0.4.0 的能力三方整合进本地 norma 副本；保留 replace 和本地工具策略。 |
| `865d52267dd23481063f79451eec69e4f71317bf` | `7e30ffe`、`6aff823` | 新引入：noa 默认关闭开关；保留任务专属代理及 ShellProfile；补充归档不可写回退检查。 |
| `04d25eb13368dfb046ff5b643e606ff0b9ee3f46` | `72091c4`、`6aff823` | 新引入：精简动作审查、动作审批来源定位；与本地资产来源定位共存，保留工具调用页。 |
| `c4579637dc63ea7fd78387e4c0de858336426ce6` | `144d173` | 新引入：归档统一到 `<workDir>/noa/<session>`。 |
| `95bcd4ef9c0231e3aae2e42f1e07a2b8b70685ec` | `72091c4` + `144d173` 等两侧效果 | 合并提交；`git show --remerge-diff` 为空，无额外冲突解决补丁，不重复应用整包差异。 |
| `7f71f77d165c513a049fd079dc31eeadfb045427` | 本地已满足 | 本地 CHANGELOG 的 NUL 数为 0，无需修改；版本记录与检查点完全一致。 |
| `80a085d1574b91e086b5866133ee00750b516eeb` | `bf14d33` | 新引入：流量代理默认监听 127.0.0.1，显式配置仍生效。 |

前五项通过 range-diff、对应实现和测试核对，未发现需要补齐的遗漏。后续的精简审查提交按官方最终设计更新早期上下文实现。

## 本地设计保留及兼容调整

- 资产审批预检、逐项意图授权、Worker 可见性与执行策略继续保留。
- 漏洞删除反馈仍只提供给所属任务 Planner；每轮从数据库注入，压缩不改写系统反馈。
- 资产来源仍通过 session/activity 定位连续历史窗口，前后分页、返回状态、未审批默认筛选及登记时间排序保留。
- 动作审批采用 approval 参数，支持 task / conversation 及 Worker / Planner / 主 Agent 会话；本地资产定位存在时优先处理，避免两种定位同时控制滚动。
- 保留展开失败重试、来源高亮及本地对话/工具调用切换。独立对话进入来源定位时重置到对话页。
- norma 的 ToolUseID、延迟工具执行完整策略链、默认参数校验、审批约束、ShellProfile 均保留。官方新增测试调用签名适配本地带 deadline/settlement 的 execOne。
- noa 模型视图会给工具结果追加引用标签；新增适配层仅在授权复核时去除标签，复核后恢复，避免 JSON 解析失败使审批状态失效。只处理模型视图副本，不改原始审计或 noa 账本。
- noa 开启前检查集中归档目录是否可写；失败保留原压缩器，记录诊断。关闭时不建立归档、不添加工具或提示词。

## 验证结果

Go 工具链命令使用 `GOSUMDB=sum.golang.org`。测试 PostgreSQL 使用专用 Docker 容器 `artex-sync-tests`，绑定 `127.0.0.1:55440`，各包独立测试库，未连接业务数据库。

| 检查 | 结果 |
| --- | --- |
| `go test ./traffic ./mcphttp ./intercept` | 通过：流量搜索/默认监听、MCP SSE、审查解析及上下文。 |
| `go test ./sidequestion ./guard` | 通过：SSE 扫描及执行约束。 |
| `go test ./db`（独立库） | 通过：证据迁移、两类来源、授权、删除反馈等。 |
| `go test ./agent -count=1`（独立库，最终适配后） | 通过：无任务工具保护、审批复核/恢复、删除反馈、来源登记、noa 开关/回退/标签授权刷新。 |
| `go test ./server -count=1`（全新独立库，最终适配后） | 通过：分页、来源历史接口、删除反馈等。 |
| `go test ./... -run '^$'` | 全仓库编译通过。 |
| `go -C third_party/norma test ./...` | agentcore、harness、llm、tool 等通过；noa / noaadapter 有下述 4 项官方既有失败。 |
| `tsc --noEmit -p web/tsconfig.json` | 通过。 |
| `npx tsx --test src/lib/mock/*.test.ts` | 10/10 通过，包含分页、两类来源和删除反馈。 |
| Chrome 无头浏览器 + mock | 默认未审批/最新优先、切换最早优先、点击资产来源、完整命令和结果展开、浏览器返回恢复排序通过；从动作审批来源链接跳转 Worker 会话，展开及居中通过，居中偏差约 0.03 px；无页面 JS 错误。 |
| 历史分页 | 数据库/服务端回归及 mock 验证连续前后游标、命令与结果配对、任务/会话范围；浏览器验证来源窗口展示。 |
| `git diff --check` | 通过。 |

浏览器使用 webpack 开发模式：隔离工作区共享 node_modules 软链接位于 Turbopack 根目录之外，Turbopack 拒绝该路径；未为测试修改产品构建配置。浏览器测试使用 mock，未调用真实目标或 LLM。

## 官方 norma v0.4.0 已知限制（未隐藏或跳过测试）

以下失败在未修改的 Go 模块缓存 `github.com/Autumn-27/norma@v0.4.0` 中同样复现，失败文本与本地一致：

1. `TestAdaptiveGrowthTracksTheWindow`：默认 GrowthFloor 与 GrowthCap 同为 50,000，窗口自适应失效。
2. `TestSuppressionReleaseFitsInsideTheWindow`：小窗口的压缩提醒恢复间隔过大。
3. `TestMostlyCompetentModelStaysBounded/seed11`：上述间隔使 40,000 窗口用量达到 48,201。
4. `TestOverflowLearningIsWiredIn`：提供商报告的窗口上限尚未接入恢复逻辑。

本次同步未调整官方 noa 调参或溢出学习算法，实验开关继续默认关闭。**不能将 norma 全量测试描述为全部通过，也未验证开启 noa 后的真实长会话效果。**其余主线功能与本地兼容回归通过；以上作为官方基线已知失败单独列示。

- [本地 norma 全量输出](upstream-sync-20260916/norma-local.log)
- [未修改官方 noa 输出](upstream-sync-20260916/norma-upstream.log)
- [浏览器断言结果](upstream-sync-20260916/browser.log)
- [资产来源截图](upstream-sync-20260916/asset-source.png)
- [动作审批来源截图](upstream-sync-20260916/action-source.png)
