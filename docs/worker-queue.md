# Worker 删除与等待队列

等待中的 Worker（`open`）可取消、删除、排序；用户已取消的 Worker（`stopped` 且 `cancelled_by_user=true`）可删除或手动开启。其余状态及继承 Worker 不可删除或排序。删除是物理删除 Worker、执行活动、旁路会话及对应 transcript 文件；独立漏洞、事实、资产、历史计量保留。

## 用户 API

- `DELETE /api/tasks/{id}/intents/{iid}`：成功返回 `{ "id": "123", "deleted": true }`。同任务重复删除幂等；状态变化返回 409。删除不提供给 Agent 工具。
- `GET /api/tasks/{id}/worker-queue`：返回 `{ "items": [...TaskNode], "manual": false, "version": 1 }`，包含该任务当前全部等待项，不受会话列表分页影响。资产审批仍由领取入口验证。
- `POST /api/tasks/{id}/worker-queue/move`：请求 `{ "id": "123", "before_id": "456", "version": 1 }`；`before_id: null` 表示队尾。成功返回最新队列；旧版本、非等待项、跨任务项或失效目标返回 409，客户端刷新后重新操作。

首次手动排序以 `priority DESC, id ASC` 初始化队列。之后按位置领取；所有新建、重新开启及恢复进入等待状态的 Worker 追加队尾，未离队的 Worker 保持位置。数值优先级不再覆盖手动顺序。每个任务使用所属 exploration 保存队列开关、版本及 Worker 位置；服务重启不重置。

所有相关图写入、排序、删除和领取先取得同一 exploration 事务 advisory lock，再操作行；不要在新入口中直接更新 Worker 状态而绕过这一锁。领取在同一事务中读取最新队列并完成状态变化，不抢占已运行项。

删除收据独立于 Worker 节点，保留原工作内容与取消标记。Planner 每轮读取删除记录和手动队列状态；已取消后删除的方向继续保留原取消约束。语义替代方向依靠上下文说明，不增加新建方向拦截。
