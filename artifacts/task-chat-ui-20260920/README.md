# 任务会话 UI 改版

## 实现

- 任务专用 `Transcript variant="task"`；平台聊天使用原有默认样式和分组方式。
- 阅读列最大 800px，用户浅色圆角气泡、Agent 无底色正文、Worker 执行目标标识。
- 连续同角色工具／推理收为过程组，默认折叠；回答、错误和审批不隐藏在过程组中。正文详情沿用视口懒加载，工具详情仅在展开时读取。
- 工具结果按调用 ID 配对，迟到结果保留过程组标识。来源定位自动展开组和调用，继续使用原来的详情接口与滚动定位。
- 桌面会话列表可收起，窄屏使用 Sheet 抽屉；输入框 24px 圆角并限制最大输入高度 200px，保留附件、引用、发送及停止功能。
- 增加返回最新按钮，沿用原来的历史加载与滚动跟随规则。
- 新增 Mock 并行工具、失败工具和长 HTTP 报文示例。

## 验证

- `npx tsc --noEmit` 通过。
- `tsx --test src/lib/transcript-groups.test.ts src/lib/mock/*.test.ts`：16 项通过。
- 浏览器 Chrome + Mock：过程组展开、错误外显、桌面收起列表、窄屏抽屉、Planner 切换、主 Agent 多行输入、浅色／深色主题通过，无页面脚本错误。
- 资产来源 `main:0/activity=1410` 自动展开并显示正确的 insert_assets 命令；返回最新通过。
- 动作审批来源 `approval=94` 高亮通过。来源范围与连续窗口规则还通过现有 Mock 回归。
- 查看截图确认长报文在自身代码块内滚动，窄屏输入框可见。
- `git diff --check` 通过。

## 验证边界

本轮使用 Mock，未运行真实模型、未连接业务数据库。迟到结果通过分组单测验证；真实 SSE 持续流、真实历史分页及详情失败重试未做端到端故障注入，相关加载和定位逻辑沿用原实现。平台聊天的默认文本分组有回归测试，未改其页面。

截图：desktop.png、dark.png、mobile-main.png、mobile-list.png、source.png。Mock 地址： http://127.0.0.1:3000/function/tasks/detail?id=t-acme-web&tab=sessions 。
