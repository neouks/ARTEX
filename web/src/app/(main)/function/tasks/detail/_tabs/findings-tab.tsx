"use client";

import * as React from "react";

import { FindingWorkspaceDetail } from "@/components/finding-detail-content";
import { FindingSelector } from "@/components/finding-selector";
import { TablePagination } from "@/components/table-pagination";
import { useNotificationRead } from "@/components/task-notifications";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Spinner } from "@/components/ui/spinner";
import { api } from "@/lib/api";
import { findingRowKey, selectFinding } from "@/lib/findings";
import { useStoredSortPreference } from "@/lib/sort-preference";
import type { FindingsPage } from "@/lib/types";

const FIELDS = ["time"] as const;
export function FindingsTab({ taskId }: { taskId: string }) {
  return <TaskFindings key={taskId} taskId={taskId} />;
}
function TaskFindings({ taskId }: { taskId: string }) {
  const beginRead = useNotificationRead("findings");
  const [sort, setSort] = useStoredSortPreference("artex_task_findings_sort", FIELDS, "time", "desc");
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(20);
  const [data, setData] = React.useState<FindingsPage | null>(null);
  const [selectedKey, setSelectedKey] = React.useState<string | null>(null);
  const [error, setError] = React.useState("");
  const [revision, refresh] = React.useReducer((n: number) => n + 1, 0);
  React.useEffect(() => {
    void revision; // Refresh the current page after a successful mutation.
    let active = true;
    let timer: ReturnType<typeof setTimeout>;
    async function load() {
      const markRead = beginRead();
      try {
        const value = await api.taskFindingsPage(taskId, page, pageSize, sort.direction);
        if (!active) return;
        if (page > Math.max(1, Math.ceil(value.total / pageSize))) {
          setPage(Math.max(1, Math.ceil(value.total / pageSize)));
          return;
        }
        setData(value);
        setSelectedKey((key) => {
          const selected = selectFinding(value.items, key);
          return selected ? findingRowKey(selected) : null;
        });
        setError("");
        markRead();
      } catch (e) {
        if (active) setError((e as Error).message);
      } finally {
        if (active) timer = setTimeout(() => void load(), 3000);
      }
    }
    void load();
    return () => {
      active = false;
      clearTimeout(timer);
    };
  }, [taskId, page, pageSize, sort.direction, beginRead, revision]);
  const selected = data ? selectFinding(data.items, selectedKey) : null;
  return (
    <div className="grid min-w-0 items-start gap-4 xl:grid-cols-[minmax(18rem,0.8fr)_minmax(0,2fr)]">
      <Card className="min-w-0">
        <CardHeader className="flex-row flex-wrap items-center justify-between gap-2">
          <CardTitle>选择漏洞 · {data?.total ?? "…"}</CardTitle>
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              setSort({ field: "time", direction: sort.direction === "asc" ? "desc" : "asc" });
              setPage(1);
            }}
          >
            发现时间 {sort.direction === "asc" ? "↑" : "↓"}
          </Button>
        </CardHeader>
        <CardContent className="px-0">
          {error && (
            <Alert>
              <AlertDescription>
                加载失败：{error}
                <Button variant="outline" size="sm" onClick={refresh}>
                  重试
                </Button>
              </AlertDescription>
            </Alert>
          )}
          {!data && !error ? (
            <div className="flex justify-center p-8">
              <Spinner />
            </div>
          ) : (
            <FindingSelector
              items={data?.items ?? []}
              selectedKey={selected ? findingRowKey(selected) : null}
              onSelect={(item) => setSelectedKey(findingRowKey(item))}
            />
          )}
          {data && (
            <TablePagination
              page={page}
              pageSize={pageSize}
              total={data?.total ?? 0}
              onPageChange={(value) => {
                setData(null);
                setPage(value);
              }}
              onPageSizeChange={(value) => {
                setData(null);
                setPageSize(value);
                setPage(1);
              }}
            />
          )}
        </CardContent>
      </Card>
      <div className="min-w-0">
        {selected ? (
          <FindingWorkspaceDetail finding={selected} contextTask={taskId} onChanged={refresh} />
        ) : (
          <p className="py-12 text-center text-muted-foreground">请选择漏洞查看详情、流量证据与复测记录。</p>
        )}
      </div>
    </div>
  );
}
