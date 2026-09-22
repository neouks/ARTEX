"use client";

import * as React from "react";
import { Search } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { api } from "@/lib/api";
import { SourceTranscript } from "@/components/source-transcript";

type Hit = { id: number; kind: string; snippet: string };
export function SessionSearch({
  taskId,
  session,
  enabled,
  children,
  onLocate,
  onLatest,
}: {
  taskId: string;
  session: string;
  enabled: boolean;
  children: React.ReactNode;
  onLocate?: () => void;
  onLatest?: () => void;
}) {
  const [open, setOpen] = React.useState(false);
  const [query, setQuery] = React.useState("");
  const [settled, setSettled] = React.useState("");
  const [hits, setHits] = React.useState<Hit[]>([]);
  const [index, setIndex] = React.useState(-1);
  const [cursor, setCursor] = React.useState("");
  const [anchor, setAnchor] = React.useState<number>();
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState("");
  const [retry, setRetry] = React.useState(0);
  const generation = React.useRef(0);
  const input = React.useRef<HTMLInputElement>(null);
  const root = React.useRef<HTMLDivElement>(null);
  const show = React.useCallback(() => {
    setOpen(true);
    requestAnimationFrame(() => input.current?.focus());
  }, []);
  React.useEffect(() => {
    if (!enabled) return;
    const key = (e: KeyboardEvent) => {
      const find = (e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "f" && !e.altKey;
      const close = open && e.key === "Escape";
      if (!find && !close) return;
      if (
        !root.current?.getClientRects().length ||
        [...document.querySelectorAll('[role="dialog"], [role="alertdialog"]')].some((el) => el.getClientRects().length)
      )
        return;
      e.preventDefault();
      if (close) setOpen(false);
      else show();
    };
    document.addEventListener("keydown", key);
    return () => document.removeEventListener("keydown", key);
  }, [enabled, show, open]);
  React.useEffect(() => {
    const request = ++generation.current;
    setHits([]);
    setIndex(-1);
    setCursor("");
    setError("");
    setSettled("");
    setBusy(false);
    if (!open || !query.trim()) return;
    setBusy(true);
    const timer = setTimeout(() => {
      api
        .activitySearch(taskId, session, query)
        .then((page) => {
          if (generation.current !== request) return;
          setHits(page.items);
          setCursor(page.next_cursor);
          setSettled(query);
          if (page.items.length) {
            setIndex(0);
            setAnchor(page.items[0].id);
            onLocate?.();
          }
        })
        .catch((e) => {
          if (generation.current === request) setError(String(e.message || e));
        })
        .finally(() => {
          if (generation.current === request) setBusy(false);
        });
    }, 300);
    return () => {
      clearTimeout(timer);
      ++generation.current;
    };
  }, [query, open, taskId, session, retry]);
  async function move(direction: number) {
    if (busy || !hits.length) return;
    const next = index + direction;
    if (next >= 0 && next < hits.length) {
      setIndex(next);
      setAnchor(hits[next].id);
      onLocate?.();
      return;
    }
    if (next < hits.length || !cursor) return;
    const request = generation.current;
    setBusy(true);
    setError("");
    try {
      const page = await api.activitySearch(taskId, session, settled, cursor);
      if (request !== generation.current) return;
      setHits((old) => [...old, ...page.items]);
      setCursor(page.next_cursor);
      if (page.items.length) {
        setIndex(hits.length);
        setAnchor(page.items[0].id);
        onLocate?.();
      }
    } catch (e) {
      if (request === generation.current) setError(String((e as Error).message || e));
    } finally {
      if (request === generation.current) setBusy(false);
    }
  }
  return (
    <div ref={root} className="flex min-h-0 flex-1 flex-col">
      {open ? (
        <div className="flex flex-col gap-1 border-b px-3 py-2" role="search" aria-label="搜索当前会话">
          <div className="flex items-center gap-1">
            <Input
              ref={input}
              value={query}
              maxLength={200}
              aria-label="会话关键词"
              placeholder="搜索当前会话完整历史"
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Escape") {
                  e.preventDefault();
                  setOpen(false);
                }
                if (e.key === "Enter") {
                  e.preventDefault();
                  void move(e.shiftKey ? -1 : 1);
                }
              }}
            />
            <Button size="sm" variant="ghost" disabled={busy || index <= 0} onClick={() => void move(-1)}>
              上一处
            </Button>
            <Button
              size="sm"
              variant="ghost"
              disabled={busy || !hits.length || (index === hits.length - 1 && !cursor)}
              onClick={() => void move(1)}
            >
              下一处
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setOpen(false)}>
              关闭搜索
            </Button>
          </div>
          <div className="text-xs text-muted-foreground" aria-live="polite">
            {busy ? (
              "搜索中…"
            ) : error ? (
              <span role="alert">
                搜索失败：{error}{" "}
                <Button size="sm" variant="ghost" onClick={() => (cursor ? void move(1) : setRetry((n) => n + 1))}>
                  重试
                </Button>
              </span>
            ) : hits.length ? (
              `第 ${index + 1} 条消息 · 已加载 ${hits.length} 条${cursor ? "，还有更多" : ""}`
            ) : settled ? (
              "没有匹配消息"
            ) : (
              "输入关键词开始搜索"
            )}
          </div>
          {hits[index] && (
            <p className="truncate text-xs text-muted-foreground" title={hits[index].snippet}>
              {hits[index].snippet}
            </p>
          )}
        </div>
      ) : (
        <div className="flex justify-end px-3">
          <Button size="sm" variant="ghost" onClick={show}>
            <Search data-icon="inline-start" />
            搜索会话
          </Button>
        </div>
      )}
      {anchor ? (
        <SourceTranscript
          key={`${taskId}:${session}:${anchor}`}
          taskId={taskId}
          session={session}
          anchor={anchor}
          searchQuery={open ? settled : ""}
          searchMode
          onLatest={() => {
            setAnchor(undefined);
            onLatest?.();
          }}
        />
      ) : null}
      <div className={anchor ? "hidden" : "flex min-h-0 flex-1 flex-col"}>{children}</div>
    </div>
  );
}
