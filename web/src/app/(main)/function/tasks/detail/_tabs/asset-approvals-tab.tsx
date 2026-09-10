"use client";

import * as React from "react";

import { CheckIcon, CircleAlertIcon, RefreshCwIcon, ShieldCheckIcon, ShieldXIcon } from "lucide-react";
import { toast } from "sonner";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Spinner } from "@/components/ui/spinner";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import { taskAssetSourceLabel } from "@/lib/task-assets";
import type { TaskAssetApproval } from "@/lib/types";

type ApprovalFilter = "all" | "pending" | "approved" | "revoked" | "blocked";

const ASSET_TYPE_LABELS: Record<string, string> = {
  root_domain: "根域名",
  subdomain: "子域名",
  ip: "IP",
  app: "应用",
};

function approvalLabel(item: TaskAssetApproval) {
  if (item.blocked) return "已封禁";
  if (item.approval_state === "pending") return "待审批";
  if (item.approval_state === "revoked") return "已撤回";
  return "已批准";
}

function ApprovalBadge({ item }: { item: TaskAssetApproval }) {
  if (item.blocked) return <Badge variant="destructive">已封禁</Badge>;
  if (item.approval_state === "pending") return <Badge variant="outline">待审批</Badge>;
  if (item.approval_state === "revoked") return <Badge variant="secondary">已撤回</Badge>;
  return <Badge>已批准</Badge>;
}

function formatTime(value?: string) {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "—" : date.toLocaleString();
}

export function AssetApprovalsTab({ taskId }: { taskId: string }) {
  const [items, setItems] = React.useState<TaskAssetApproval[]>([]);
  const [filter, setFilter] = React.useState<ApprovalFilter>("all");
  const [selected, setSelected] = React.useState<Set<number>>(new Set());
  const [loaded, setLoaded] = React.useState(false);
  const [error, setError] = React.useState("");
  const [saving, setSaving] = React.useState(false);
  const [revokeIDs, setRevokeIDs] = React.useState<number[]>([]);
  const [excludeItem, setExcludeItem] = React.useState<TaskAssetApproval | null>(null);

  const load = React.useCallback(async () => {
    try {
      const next = (await api.taskAssetApprovals(taskId)).filter(
        (item) => item.asset_type !== "service" && item.asset_type !== "endpoint",
      );
      setItems(next);
      setError("");
      const validIDs = new Set(
        next.filter((item) => !item.blocked && !item.read_only && item.asset_id > 0).map((item) => item.asset_id),
      );
      setSelected((current) => new Set([...current].filter((id) => validIDs.has(id))));
    } catch (reason) {
      setError(String((reason as Error)?.message ?? reason));
    } finally {
      setLoaded(true);
    }
  }, [taskId]);

  React.useEffect(() => {
    let active = true;
    const refresh = () => {
      if (active) void load();
    };
    refresh();
    const timer = setInterval(refresh, 10_000);
    return () => {
      active = false;
      clearInterval(timer);
    };
  }, [load]);

  const visible = React.useMemo(
    () =>
      items.filter((item) => {
        if (filter === "all") return true;
        if (filter === "blocked") return item.blocked;
        return !item.blocked && item.approval_state === filter;
      }),
    [filter, items],
  );
  const selectableIDs = visible
    .filter((item) => !item.blocked && !item.read_only && item.asset_id > 0)
    .map((item) => item.asset_id);
  const selectedVisible = selectableIDs.filter((id) => selected.has(id));
  const allSelected = selectableIDs.length > 0 && selectedVisible.length === selectableIDs.length;
  let selectionState: boolean | "indeterminate" = false;
  if (allSelected) selectionState = true;
  else if (selectedVisible.length > 0) selectionState = "indeterminate";

  const refresh = React.useCallback(() => {
    setLoaded(false);
    void load();
  }, [load]);

  const mutate = async (ids: number[], approve: boolean) => {
    if (ids.length === 0) return;
    setSaving(true);
    try {
      if (approve) await api.approveTaskAssets(taskId, ids, "用户在资产审批面板批准");
      else await api.revokeTaskAssets(taskId, ids, "用户在资产审批面板撤回批准");
      toast.success(approve ? `已批准 ${ids.length} 项资产` : `已撤回 ${ids.length} 项资产，相关 Worker 已停止`);
      setSelected(new Set());
      setRevokeIDs([]);
      refresh();
    } catch (reason) {
      toast.error(`审批操作失败：${String((reason as Error)?.message ?? reason)}`);
    } finally {
      setSaving(false);
    }
  };

  const excludeInherited = async (item: TaskAssetApproval) => {
    setSaving(true);
    try {
      await api.detachTaskAsset(taskId, item.asset_id);
      toast.success("已从当前任务排除该来源资产，来源任务不受影响");
      setExcludeItem(null);
      refresh();
    } catch (reason) {
      toast.error(`排除资产失败：${String((reason as Error)?.message ?? reason)}`);
    } finally {
      setSaving(false);
    }
  };

  const counts = React.useMemo(
    () => ({
      pending: items.filter((item) => !item.blocked && item.approval_state === "pending").length,
      approved: items.filter((item) => !item.blocked && item.approval_state === "approved").length,
      revoked: items.filter((item) => !item.blocked && item.approval_state === "revoked").length,
      blocked: items.filter((item) => item.blocked).length,
    }),
    [items],
  );
  let confirmLabel = "确认撤回";
  if (saving) confirmLabel = "处理中";
  else if (excludeItem) confirmLabel = "确认排除";

  let body: React.ReactNode;
  if (!loaded) {
    body = (
      <div className="flex min-h-64 items-center justify-center">
        <Spinner />
      </div>
    );
  } else if (visible.length === 0) {
    body = (
      <Empty className="min-h-64 border">
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <CheckIcon />
          </EmptyMedia>
          <EmptyTitle>当前筛选下没有资产</EmptyTitle>
          <EmptyDescription>Agent 新发现的根域名、子域名和 IP 会在这里等待审批。</EmptyDescription>
        </EmptyHeader>
      </Empty>
    );
  } else {
    body = (
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="w-10">
              <Checkbox
                checked={selectionState}
                onCheckedChange={(checked) => {
                  setSelected((current) => {
                    const next = new Set(current);
                    for (const id of selectableIDs) checked ? next.add(id) : next.delete(id);
                    return next;
                  });
                }}
                aria-label="选择当前页所有可审批资产"
              />
            </TableHead>
            <TableHead>资产</TableHead>
            <TableHead>类型</TableHead>
            <TableHead>状态</TableHead>
            <TableHead>发现来源</TableHead>
            <TableHead>登记 / 审批时间</TableHead>
            <TableHead>操作人</TableHead>
            <TableHead className="text-right">操作</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {visible.map((item, index) => {
            const rowKey = `${item.source_task_id}-${item.asset_id}-${item.name}-${item.blocked_at ?? index}`;
            const selectable = !item.blocked && !item.read_only && item.asset_id > 0;
            let action: React.ReactNode;
            if (item.inherited && !item.blocked) {
              action = (
                <Button size="xs" variant="outline" onClick={() => setExcludeItem(item)} disabled={saving}>
                  从本任务排除
                </Button>
              );
            } else if (item.inherited) {
              action = <span className="text-muted-foreground text-xs">来源任务只读</span>;
            } else if (!selectable) {
              action = <span className="text-muted-foreground text-xs">{approvalLabel(item)}</span>;
            } else if (item.approval_state === "approved") {
              action = (
                <Button size="xs" variant="outline" onClick={() => setRevokeIDs([item.asset_id])} disabled={saving}>
                  撤回
                </Button>
              );
            } else {
              action = (
                <Button size="xs" onClick={() => void mutate([item.asset_id], true)} disabled={saving}>
                  批准
                </Button>
              );
            }
            return (
              <TableRow key={rowKey} data-state={selected.has(item.asset_id) ? "selected" : undefined}>
                <TableCell>
                  <Checkbox
                    checked={selected.has(item.asset_id)}
                    disabled={!selectable || saving}
                    onCheckedChange={(checked) =>
                      setSelected((current) => {
                        const next = new Set(current);
                        checked ? next.add(item.asset_id) : next.delete(item.asset_id);
                        return next;
                      })
                    }
                    aria-label={`选择资产 ${item.name}`}
                  />
                </TableCell>
                <TableCell className="max-w-sm whitespace-normal [overflow-wrap:anywhere]">
                  <div className="flex flex-col gap-0.5">
                    <span className="font-medium font-mono text-xs">{item.name}</span>
                    {item.blocked ? (
                      <span className="text-destructive text-xs">{item.block_reason ?? "禁止再次测试"}</span>
                    ) : null}
                  </div>
                </TableCell>
                <TableCell>{ASSET_TYPE_LABELS[item.asset_type] ?? item.asset_type}</TableCell>
                <TableCell>
                  <ApprovalBadge item={item} />
                </TableCell>
                <TableCell className="max-w-xs whitespace-normal [overflow-wrap:anywhere]">
                  <div className="flex flex-col gap-0.5 text-xs">
                    <span>{item.source ? taskAssetSourceLabel(item.source) : "—"}</span>
                    <span className="text-muted-foreground">{item.source_summary || "—"}</span>
                    {item.inherited ? (
                      <span className="text-muted-foreground">来源任务 #{item.source_task_id} · 状态只读</span>
                    ) : null}
                  </div>
                </TableCell>
                <TableCell className="text-xs">
                  <div className="flex flex-col gap-0.5">
                    <span>{formatTime(item.created_at)}</span>
                    <span className="text-muted-foreground">{formatTime(item.blocked_at ?? item.approved_at)}</span>
                  </div>
                </TableCell>
                <TableCell className="text-xs">{item.blocked_by || item.approved_by || "—"}</TableCell>
                <TableCell className="text-right">{action}</TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    );
  }

  return (
    <div className="flex min-h-0 flex-col gap-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 className="font-medium text-sm">资产审批</h2>
          <p className="text-muted-foreground text-xs">
            待审批资产不会进入可测试范围。服务和接口继承父域名/IP 的授权，无需单独审批，可在测试资产页查看。
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Select value={filter} onValueChange={(value) => setFilter(value as ApprovalFilter)}>
            <SelectTrigger size="sm" className="w-36">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                <SelectItem value="all">全部 {items.length}</SelectItem>
                <SelectItem value="pending">待审批 {counts.pending}</SelectItem>
                <SelectItem value="approved">已批准 {counts.approved}</SelectItem>
                <SelectItem value="revoked">已撤回 {counts.revoked}</SelectItem>
                <SelectItem value="blocked">已封禁 {counts.blocked}</SelectItem>
              </SelectGroup>
            </SelectContent>
          </Select>
          <Button
            variant="outline"
            size="icon-sm"
            onClick={refresh}
            disabled={!loaded || saving}
            aria-label="刷新资产审批"
          >
            <RefreshCwIcon />
          </Button>
        </div>
      </div>

      <Separator />

      {selected.size > 0 ? (
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-muted-foreground text-xs">已选 {selected.size} 项</span>
          <Button size="sm" onClick={() => void mutate([...selected], true)} disabled={saving}>
            {saving ? <Spinner data-icon="inline-start" /> : <ShieldCheckIcon data-icon="inline-start" />}
            批准选中
          </Button>
          <Button size="sm" variant="outline" onClick={() => setRevokeIDs([...selected])} disabled={saving}>
            <ShieldXIcon data-icon="inline-start" />
            撤回选中
          </Button>
        </div>
      ) : null}

      {error ? (
        <Alert variant="destructive">
          <CircleAlertIcon />
          <AlertTitle>资产审批加载失败</AlertTitle>
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      ) : null}

      {body}

      <AlertDialog
        open={revokeIDs.length > 0 || excludeItem !== null}
        onOpenChange={(open) => {
          if (!open && !saving) {
            setRevokeIDs([]);
            setExcludeItem(null);
          }
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{excludeItem ? "从当前任务排除来源资产？" : "撤回资产测试授权？"}</AlertDialogTitle>
            <AlertDialogDescription>
              {excludeItem
                ? `系统会为“${excludeItem.name}”建立当前任务墓碑，并立即停止相关 Worker；来源任务及其他任务不受影响。`
                : `将撤回 ${revokeIDs.length} 项资产。系统会立即阻止新的 Planner/Worker 操作，并停止当前任务中命中这些资产的运行中 Worker。`}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={saving}>取消</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={saving}
              onClick={(event) => {
                event.preventDefault();
                if (excludeItem) void excludeInherited(excludeItem);
                else void mutate(revokeIDs, false);
              }}
            >
              {saving ? <Spinner data-icon="inline-start" /> : <ShieldXIcon data-icon="inline-start" />}
              {confirmLabel}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
