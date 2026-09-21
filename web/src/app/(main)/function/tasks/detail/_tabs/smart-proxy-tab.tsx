"use client";

import * as React from "react";

import { RadioTowerIcon, RefreshCwIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Spinner } from "@/components/ui/spinner";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { SmartProxyHost } from "@/lib/types";

/**
 * Task-scoped view of the smart-proxy marks. Marks belong to a task, so this tab
 * lives on the task detail page rather than only in global settings: a new task
 * starts with an empty list, and deleting the task drops its own marks.
 */
export function SmartProxyTab({ taskId }: { taskId: string }) {
  const [hosts, setHosts] = React.useState<SmartProxyHost[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [removing, setRemoving] = React.useState<string | null>(null);

  const load = React.useCallback(() => {
    setLoading(true);
    api
      .taskSmartProxyHosts(taskId)
      .then((rows) => setHosts(rows ?? []))
      .catch((e) => toast.error("加载失败：" + (e as Error).message))
      .finally(() => setLoading(false));
  }, [taskId]);

  React.useEffect(() => {
    load();
  }, [load]);

  const remove = (host: string) => {
    setRemoving(host);
    api
      .deleteTaskSmartProxyHost(taskId, host)
      .then(() => {
        setHosts((prev) => prev.filter((h) => h.host !== host));
        toast.success("已移除标记");
      })
      .catch((e) => toast.error("移除失败：" + (e as Error).message))
      .finally(() => setRemoving(null));
  };

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-muted-foreground text-sm">
          被标记的主机将经<b>代理池</b>出网（仅本任务生效）。Agent 判定目标被 WAF / 风控拦截时自动标记。
        </p>
        <Button size="sm" variant="outline" onClick={load} disabled={loading}>
          {loading ? <Spinner data-icon="inline-start" /> : <RefreshCwIcon data-icon="inline-start" />}
          刷新
        </Button>
      </div>

      {loading ? (
        <p className="text-muted-foreground text-sm">加载中…</p>
      ) : hosts.length === 0 ? (
        <Empty>
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <RadioTowerIcon />
            </EmptyMedia>
            <EmptyTitle>暂无标记</EmptyTitle>
            <EmptyDescription>
              当 Agent 判定某主机被 WAF / 风控拦截、直连不可达时，会在此登记并改走代理池。
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      ) : (
        <div className="rounded-md border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>主机</TableHead>
                <TableHead>标记原因</TableHead>
                <TableHead>来源</TableHead>
                <TableHead className="w-[100px]">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {hosts.map((h) => (
                <TableRow key={h.host}>
                  <TableCell className="font-mono text-xs">{h.host}</TableCell>
                  <TableCell className="max-w-[360px] text-xs">{h.reason}</TableCell>
                  <TableCell className="text-muted-foreground text-xs">
                    {h.source === "ai" ? `Agent${h.agent_key ? `（${h.agent_key}）` : ""}` : "手动"}
                  </TableCell>
                  <TableCell>
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={removing === h.host}
                      onClick={() => remove(h.host)}
                    >
                      移除
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  );
}