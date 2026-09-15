"use client";

import * as React from "react";

import {
  closestCenter,
  DndContext,
  type DragEndEvent,
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
} from "@dnd-kit/core";
import {
  arrayMove,
  SortableContext,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { GripVerticalIcon, ListOrderedIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle, SheetTrigger } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { api } from "@/lib/api";
import type { TaskNode } from "@/lib/types";
import { cn } from "@/lib/utils";

type Queue = Awaited<ReturnType<typeof api.workerQueue>>;
function titleOf(node: TaskNode) {
  try {
    return String(JSON.parse(node.payload ?? "{}").summary ?? `Worker #${node.id}`);
  } catch {
    return `Worker #${node.id}`;
  }
}
function QueueRow({ node, index, disabled }: { node: TaskNode; index: number; disabled: boolean }) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id: node.id,
    disabled,
  });
  return (
    <li
      ref={setNodeRef}
      style={{ transform: transform ? `translate3d(${transform.x}px, ${transform.y}px, 0)` : undefined, transition }}
      className={cn("flex items-center gap-3 rounded-md border bg-card p-3", isDragging && "opacity-50")}
    >
      <Button
        variant="ghost"
        size="icon-sm"
        className="touch-none shrink-0 cursor-grab"
        {...attributes}
        {...listeners}
        disabled={disabled}
        aria-label={`移动 Worker #${node.id}`}
      >
        <GripVerticalIcon />
      </Button>
      <span className="text-muted-foreground text-sm tabular-nums">{index + 1}</span>
      <div className="min-w-0">
        <span className="text-muted-foreground text-xs">#{node.id}</span>
        <p className="break-words text-sm">{titleOf(node)}</p>
      </div>
    </li>
  );
}
export function WorkerQueue({ taskId, disabled }: { taskId: string; disabled: boolean }) {
  const [open, setOpen] = React.useState(false);
  const [queue, setQueue] = React.useState<Queue | null>(null);
  const [error, setError] = React.useState("");
  const [saving, setSaving] = React.useState(false);
  const [dragging, setDragging] = React.useState(false);
  const [revision, refresh] = React.useReducer((n: number) => n + 1, 0);
  const busy = React.useRef(false);
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );
  React.useEffect(() => {
    void revision;
    if (!open || saving || dragging) return;
    let active = true;
    let loading = false;
    const load = async () => {
      if (loading) return;
      loading = true;
      try {
        const result = await api.workerQueue(taskId);
        if (active) {
          setQueue(result);
          setError("");
        }
      } catch (e) {
        if (active) setError((e as Error).message);
      } finally {
        loading = false;
      }
    };
    void load();
    const timer = setInterval(load, 3000);
    return () => {
      active = false;
      clearInterval(timer);
    };
  }, [taskId, open, saving, dragging, revision]);
  async function move(event: DragEndEvent) {
    setDragging(false);
    if (!queue || busy.current || !event.over || event.active.id === event.over.id) return;
    const from = queue.items.findIndex((n) => n.id === event.active.id);
    const to = queue.items.findIndex((n) => n.id === event.over?.id);
    if (from < 0 || to < 0) return;
    const snapshot = queue;
    const items = arrayMove(queue.items, from, to);
    busy.current = true;
    setSaving(true);
    setQueue({ ...queue, items });
    try {
      setQueue(await api.moveWorker(taskId, String(event.active.id), items[to + 1]?.id ?? null, snapshot.version));
      setError("");
    } catch (e) {
      setQueue(snapshot);
      setError((e as Error).message);
      toast.error("队列已变化或保存失败，请刷新后重试");
    } finally {
      busy.current = false;
      setSaving(false);
    }
  }
  return (
    <Sheet
      open={open}
      onOpenChange={(value) => {
        if (busy.current) return;
        setOpen(value);
        if (!value) {
          setQueue(null);
          setError("");
        }
      }}
    >
      <SheetTrigger asChild>
        <Button size="sm" variant="ghost" disabled={disabled}>
          <ListOrderedIcon data-icon="inline-start" />
          等待队列
        </Button>
      </SheetTrigger>
      <SheetContent className="w-full! sm:max-w-xl!">
        <SheetHeader>
          <SheetTitle>等待队列</SheetTitle>
          <SheetDescription>
            拖动手柄调整执行顺序；键盘按空格拾起，上下箭头移动，空格放下。手动排序后，新建及重新开启的 Worker
            追加队尾。已运行的 Worker 不受影响。
          </SheetDescription>
        </SheetHeader>
        <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto px-4 pb-4">
          {saving && <p className="text-muted-foreground text-sm">保存中…</p>}
          {error && (
            <div role="alert" className="text-destructive text-sm">
              {error}
              <Button size="sm" variant="outline" onClick={refresh}>
                刷新
              </Button>
            </div>
          )}
          {!queue && !error && <Spinner />}
          {queue && (
            <>
              <p className="text-muted-foreground text-xs">
                {queue.manual ? "按用户顺序" : "按 Planner 优先级"} · {queue.items.length} 个等待项
              </p>
              <DndContext
                sensors={sensors}
                collisionDetection={closestCenter}
                onDragStart={() => setDragging(true)}
                onDragCancel={() => setDragging(false)}
                onDragEnd={(event) => void move(event)}
              >
                <SortableContext items={queue.items.map((n) => n.id)} strategy={verticalListSortingStrategy}>
                  <ol className="flex flex-col gap-2">
                    {queue.items.map((node, index) => (
                      <QueueRow key={node.id} node={node} index={index} disabled={saving || disabled || !!error} />
                    ))}
                  </ol>
                </SortableContext>
              </DndContext>
              {!queue.items.length && <p className="py-8 text-center text-muted-foreground">暂无等待运行的 Worker</p>}
            </>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}
