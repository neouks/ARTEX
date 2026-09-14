"use client";

import * as React from "react";

import { Badge } from "@/components/ui/badge";
import { api } from "@/lib/api";
import {
  type NotificationCategory,
  type NotificationCounts,
  type NotificationCursor,
  newerNotificationCursor,
  notificationKey,
  notificationLabel,
  notificationStoragePrefix,
  parseNotificationCursor,
} from "@/lib/task-notifications";

const changeEvent = "artex:task-unread-changed";
const baselineKey = `${notificationStoragePrefix}baseline`;
const memory = new Map<string, NotificationCursor>();
function preferred(key: string, old: NotificationCursor | undefined, incoming: NotificationCursor) {
  // Concurrent first visits converge on the earliest baseline, whereas actual
  // read progress must only move forward.
  if (key === baselineKey && old) return old.observed_at <= incoming.observed_at ? old : incoming;
  return newerNotificationCursor(old, incoming);
}
function read(key: string) {
  let stored: NotificationCursor | undefined;
  try {
    stored = parseNotificationCursor(localStorage.getItem(key));
  } catch {
    /* Private browsing fallback. */
  }
  const old = memory.get(key);
  const cursor = stored ? preferred(key, old, stored) : old;
  if (cursor) memory.set(key, cursor);
  // A slower browser tab may have overwritten localStorage with an older
  // snapshot. Merge monotonically and repair storage for the next refresh.
  if (cursor && (!stored || cursor.observed_at !== stored.observed_at)) {
    try {
      localStorage.setItem(key, JSON.stringify(cursor));
    } catch {
      /* In-memory fallback. */
    }
  }
  return cursor;
}
function advance(key: string, cursor: NotificationCursor) {
  const old = read(key);
  if (preferred(key, old, cursor) === old) return;
  memory.set(key, cursor);
  try {
    localStorage.setItem(key, JSON.stringify(cursor));
  } catch {
    /* Keep the in-memory read progress. */
  }
  window.dispatchEvent(new Event(changeEvent));
}

function readCursor(taskID: string, category: NotificationCategory) {
  return read(notificationKey(taskID, category)) ?? read(baselineKey);
}

// No timer: callers reuse the task list/detail refresh cycle. Scope changes abort
// old requests; failures retain the last good counts instead of inventing zeroes.
export function useTaskNotifications(taskIDs: string[], mode: "all" | "findings") {
  const scope = JSON.stringify([...new Set(taskIDs)].filter(Boolean).sort());
  const activeScope = React.useRef(scope);
  activeScope.current = scope;
  const [counts, setCounts] = React.useState<Record<string, NotificationCounts>>({});
  const snapshot = React.useRef<NotificationCursor | undefined>(undefined);
  const request = React.useRef<AbortController | null>(null);
  const rerun = React.useRef(false);
  const refresh = React.useCallback(async () => {
    if (document.visibilityState !== "visible") return;
    const ids = JSON.parse(scope) as string[];
    if (!ids.length) return;
    if (request.current) {
      rerun.current = true;
      return;
    }
    const controller = new AbortController();
    request.current = controller;
    try {
      const queries = ids.map((task_id) => ({
        task_id,
        findings: readCursor(task_id, "findings")?.snapshot ?? "",
        assets: mode === "all" ? (readCursor(task_id, "assets")?.snapshot ?? "") : "",
        intercepts: mode === "all" ? (readCursor(task_id, "intercepts")?.snapshot ?? "") : "",
      }));
      const result = await api.taskNotifications(queries, mode, controller.signal);
      if (controller.signal.aborted) return;
      snapshot.current = { snapshot: result.snapshot, observed_at: result.observed_at };
      // One browser-wide baseline also covers categories/tasks not yet opened.
      // A later first visit must not swallow events created since feature setup.
      if (!read(baselineKey)) advance(baselineKey, snapshot.current);
      setCounts(Object.fromEntries(result.items.map((row) => [row.task_id, row])));
    } catch {
      /* Retain the last successful summary. */
    } finally {
      if (request.current === controller) {
        request.current = null;
        if (rerun.current) {
          rerun.current = false;
          void refresh();
        }
      }
    }
  }, [scope, mode]);

  React.useEffect(() => {
    setCounts({});
    snapshot.current = undefined;
    const changed = () => {
      void refresh();
    };
    const storage = (event: StorageEvent) => {
      if (event.key?.startsWith(notificationStoragePrefix)) changed();
    };
    window.addEventListener(changeEvent, changed);
    window.addEventListener("storage", storage);
    document.addEventListener("visibilitychange", changed);
    void refresh();
    return () => {
      request.current?.abort();
      request.current = null;
      rerun.current = false;
      window.removeEventListener(changeEvent, changed);
      window.removeEventListener("storage", storage);
      document.removeEventListener("visibilitychange", changed);
    };
  }, [refresh]);
  const capture = React.useCallback(
    (taskID: string, category: NotificationCategory) => {
      const cursor = snapshot.current;
      return () => {
        if (!cursor || activeScope.current !== scope || document.visibilityState !== "visible") return;
        advance(notificationKey(taskID, category), cursor);
        setCounts((old) => {
          if (!old[taskID] || (snapshot.current?.observed_at ?? 0) > cursor.observed_at) return old;
          return { ...old, [taskID]: { ...old[taskID], [category]: 0 } };
        });
      };
    },
    [scope],
  );
  return { counts, refresh, capture };
}

const noRead = () => () => {
  /* Not a task detail approval page. */
};
export const NotificationReadContext = React.createContext<(category: NotificationCategory) => () => void>(noRead);
export function useNotificationRead(category: NotificationCategory) {
  const capture = React.useContext(NotificationReadContext);
  return React.useCallback(() => capture(category), [capture, category]);
}
export function TaskUnreadBadge({ count = 0, label }: { count?: number; label: string }) {
  if (count <= 0) return null;
  return (
    <Badge variant="notification" className="ml-1 min-w-5 shrink-0" aria-label={`${label}，${count} 条未读`}>
      {notificationLabel(count)}
    </Badge>
  );
}
