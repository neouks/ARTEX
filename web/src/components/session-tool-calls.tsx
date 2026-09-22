"use client";

import { type ReactNode, useEffect, useRef, useState } from "react";

import { Loader2Icon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { SessionSearch } from "@/components/session-search";
import { api } from "@/lib/api";
import { extraToolName, type ToolCall, type ToolCallPage, type ToolCallText } from "@/lib/tool-calls";

const types = { builtin: "内置", custom: "自定义", mcp: "MCP", unknown: "未知" };
const states = { success: "成功", failed: "失败", running: "执行中", missing: "结果缺失" };

// Keep the transcript mounted: its existing SSE/polling and scroll refs continue
// to own live activity. This read-only view subscribes to their revision, not a timer.
export function SessionToolCalls({
  base,
  revision,
  children,
  taskId,
  session,
  onLocate,
  onLatest,
}: {
  base: string;
  revision: string;
  children: ReactNode;
  taskId?: string;
  session?: string;
  onLocate?: () => void;
  onLatest?: () => void;
}) {
  const [tab, setTab] = useState("chat");
  return (
    <Tabs value={tab} onValueChange={setTab} className="min-h-0 min-w-0 flex-1 gap-0">
      <TabsList className="mx-3 my-2 shrink-0">
        <TabsTrigger value="chat">对话</TabsTrigger>
        <TabsTrigger value="tools">工具调用</TabsTrigger>
      </TabsList>
      <TabsContent value="chat" forceMount className="flex min-h-0 flex-1 flex-col data-[state=inactive]:hidden">
        {taskId && session ? (
          <SessionSearch
            key={`${taskId}:${session}`}
            taskId={taskId}
            session={session}
            enabled={tab === "chat"}
            onLocate={onLocate}
            onLatest={onLatest}
          >
            {children}
          </SessionSearch>
        ) : (
          children
        )}
      </TabsContent>
      <TabsContent value="tools" className="min-h-0 flex-1 overflow-auto px-3 pb-3">
        <ToolCallList key={base} base={base} revision={revision} />
      </TabsContent>
    </Tabs>
  );
}

function ToolCallList({ base, revision }: { base: string; revision: string }) {
  const [q, setQ] = useState("");
  const [search, setSearch] = useState("");
  const [type, setType] = useState("all");
  const [status, setStatus] = useState("all");
  const [cursors, setCursors] = useState<string[]>([""]);
  const [page, setPage] = useState<ToolCallPage | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [refresh, setRefresh] = useState(0);
  const [updates, setUpdates] = useState(false);
  const lastRevision = useRef(revision);
  const refreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const cursor = cursors[cursors.length - 1];
  useEffect(() => {
    const timer = setTimeout(() => {
      setSearch(q.trim());
      setCursors([""]);
    }, 300);
    return () => clearTimeout(timer);
  }, [q]);
  useEffect(() => {
    if (lastRevision.current === revision) return;
    lastRevision.current = revision;
    if (refreshTimer.current) return;
    // Throttle, not a trailing debounce: a busy stream must not starve updates.
    refreshTimer.current = setTimeout(() => {
      refreshTimer.current = null;
      if (cursor) setUpdates(true);
      setRefresh((n) => n + 1);
    }, 400);
  }, [revision, cursor]);
  useEffect(
    () => () => {
      if (refreshTimer.current) clearTimeout(refreshTimer.current);
    },
    [],
  );
  // biome-ignore lint/correctness/useExhaustiveDependencies: refresh is an intentional event-driven reload nonce.
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError("");
    // The old page's cursor fixes its upper bound, even as results arrive.
    api
      .toolCalls(
        base,
        { q: search, type: type === "all" ? "" : type, status: status === "all" ? "" : status, cursor },
        controller.signal,
      )
      .then((value) => {
        if (!controller.signal.aborted) setPage(value);
      })
      .catch((err) => {
        if (!controller.signal.aborted) setError(String(err));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [base, search, type, status, cursor, refresh]);
  const reset = () => {
    setCursors([""]);
    setPage(null);
    setUpdates(false);
  };
  return (
    <div className="min-w-0 space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <Input
          aria-label="搜索工具名称"
          placeholder="搜索工具名称"
          className="w-full sm:w-56"
          value={q}
          onChange={(e) => setQ(e.target.value)}
        />
        <Select
          value={type}
          onValueChange={(v) => {
            setType(v);
            reset();
          }}
        >
          <SelectTrigger aria-label="工具类型" className="w-32">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">全部类型</SelectItem>
            {Object.entries(types).map(([v, label]) => (
              <SelectItem key={v} value={v}>
                {label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={status}
          onValueChange={(v) => {
            setStatus(v);
            reset();
          }}
        >
          <SelectTrigger aria-label="调用状态" className="w-32">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">全部状态</SelectItem>
            {Object.entries(states).map(([v, label]) => (
              <SelectItem key={v} value={v}>
                {label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Button variant="outline" size="sm" disabled={loading} onClick={() => setRefresh((n) => n + 1)}>
          刷新
        </Button>
      </div>
      <p className="text-muted-foreground text-xs">仅查看本会话已保存的调用记录，不调用模型或执行工具。</p>
      {page?.read_only && <p className="text-muted-foreground text-xs">来源任务 #{page.source_task_id} · 只读记录</p>}
      {updates && (
        <Button
          variant="outline"
          size="sm"
          onClick={() => {
            reset();
            setRefresh((n) => n + 1);
          }}
        >
          会话有更新，返回最新页
        </Button>
      )}
      {error && (
        <p role="alert" className="text-destructive text-sm">
          加载失败：{error}
        </p>
      )}
      {loading && (
        <p role="status" className="flex items-center gap-2 text-muted-foreground text-sm">
          <Loader2Icon className="size-4 animate-spin" />
          加载中…
        </p>
      )}
      {!loading && !error && !page?.items.length && (
        <p className="py-8 text-center text-muted-foreground text-sm">当前筛选下暂无工具调用</p>
      )}
      {!error && page?.items.map((call) => <ToolCallRow key={call.id} base={base} call={call} />)}
      <div className="flex items-center justify-between gap-2">
        <Button
          variant="outline"
          size="sm"
          disabled={loading || cursors.length === 1}
          onClick={() => {
            setPage(null);
            setCursors((v) => v.slice(0, -1));
          }}
        >
          上一页
        </Button>
        <span className="text-muted-foreground text-xs">第 {cursors.length} 页 · 每页 20 条</span>
        <Button
          variant="outline"
          size="sm"
          disabled={loading || !!error || !page?.has_more}
          onClick={() => {
            if (page?.next_cursor) {
              setPage(null);
              setCursors((v) => [...v, page.next_cursor]);
            }
          }}
        >
          下一页
        </Button>
      </div>
    </div>
  );
}

function ToolCallRow({ base, call }: { base: string; call: ToolCall }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="min-w-0 rounded-lg border">
      <Button
        variant="ghost"
        className="h-auto w-full flex-wrap justify-start gap-2 px-3 py-2 text-left"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
      >
        <span className="min-w-0 break-all font-medium">{call.name || "未知工具"}</span>
        <Badge variant="outline">{types[call.type]}</Badge>
        <Badge variant={call.status === "failed" ? "destructive" : "secondary"}>{states[call.status]}</Badge>
        <span className="font-normal text-muted-foreground text-xs">{new Date(call.created_at).toLocaleString()}</span>
        <span className="ml-auto text-xs">{open ? "收起" : "展开"}</span>
      </Button>
      {open && (
        <div className="space-y-3 border-t p-3">
          <CallText key={`input:${call.use_id}`} base={base} id={call.use_id} title="调用参数" tool={call.name} />
          <CallText key={`result:${call.result_id}`} base={base} id={call.result_id} title="返回结果" />
        </div>
      )}
    </div>
  );
}

function CallText({ base, id, title, tool = "" }: { base: string; id: number | null; title: string; tool?: string }) {
  const [part, setPart] = useState<ToolCallText | null>(null);
  const [offsets, setOffsets] = useState([0]);
  const [error, setError] = useState("");
  const [retry, setRetry] = useState(0);
  const offset = offsets[offsets.length - 1];
  // biome-ignore lint/correctness/useExhaustiveDependencies: retry explicitly reloads the same failed text segment.
  useEffect(() => {
    if (id == null) return;
    const controller = new AbortController();
    setPart(null);
    setError("");
    api
      .toolCallDetail(base, id, offset, controller.signal)
      .then((value) => {
        if (!controller.signal.aborted) setPart(value);
      })
      .catch((err) => {
        if (!controller.signal.aborted) setError(String(err));
      });
    return () => controller.abort();
  }, [base, id, offset, retry]);
  const actual = part?.offset === 0 ? extraToolName(tool, part.text) : null;
  return (
    <section className="min-w-0 space-y-2">
      <h4 className="font-medium text-xs">{title}</h4>
      {actual && (
        <p className="break-all text-muted-foreground text-xs">封装调用的实际工具：{actual}（非独立调用记录）</p>
      )}
      {id == null ? (
        <p className="text-muted-foreground text-xs">记录缺失</p>
      ) : error ? (
        <div role="alert" className="text-destructive text-xs">
          {error}{" "}
          <Button variant="outline" size="sm" onClick={() => setRetry((n) => n + 1)}>
            重试
          </Button>
        </div>
      ) : !part ? (
        <p role="status" className="text-muted-foreground text-xs">
          读取中…
        </p>
      ) : (
        <>
          <pre className="max-h-80 overflow-auto whitespace-pre-wrap break-all rounded-md bg-muted p-2 text-xs">
            {part.text || "（空内容）"}
          </pre>
          <div className="flex flex-wrap items-center gap-2 text-muted-foreground text-xs">
            <span>
              {part.offset}–{part.next_offset} / {part.total} 字符
            </span>
            {offsets.length > 1 && (
              <Button variant="outline" size="sm" onClick={() => setOffsets((v) => v.slice(0, -1))}>
                上一段
              </Button>
            )}
            {part.has_more && (
              <Button variant="outline" size="sm" onClick={() => setOffsets((v) => [...v, part.next_offset])}>
                下一段
              </Button>
            )}
          </div>
        </>
      )}
    </section>
  );
}
