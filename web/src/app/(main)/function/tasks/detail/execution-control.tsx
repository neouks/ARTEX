"use client";

import * as React from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Switch as SwitchPrimitive } from "radix-ui";
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogCancel,
} from "@/components/ui/alert-dialog";
import { api } from "@/lib/api";
import type { Task } from "@/lib/types";

export function ExecutionControl({ task, onUpdated }: { task: Task; onUpdated: () => void }) {
  const [busy, setBusy] = React.useState(false);
  const [confirm, setConfirm] = React.useState(false);
  const submitting = React.useRef(false);
  const modeId = React.useId();
  const managed = (task.execution_mode ?? "managed") === "managed";
  async function change(mode: string) {
    if (submitting.current || (mode !== "managed" && mode !== "manual") || mode === (task.execution_mode ?? "managed"))
      return;
    submitting.current = true;
    setBusy(true);
    try {
      await api.setExecutionMode(task.id, mode);
      onUpdated();
      toast.success(
        mode === "manual"
          ? "已切换手工：Planner 自动规划已停止，意图等待选择下发"
          : "已切换托管：Planner 恢复自动规划与意图调度",
      );
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  }
  async function finish() {
    if (submitting.current) return;
    submitting.current = true;
    setBusy(true);
    try {
      await api.controlTask(task.id, "finish");
      setConfirm(false);
      onUpdated();
      toast.success("任务已结束，已有产出已保留");
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  }
  return (
    <div className="flex flex-wrap items-center gap-2">
      <div className="shrink-0">
        <SwitchPrimitive.Root
          id={modeId}
          checked={managed}
          onCheckedChange={(checked) => void change(checked ? "managed" : "manual")}
          disabled={busy}
          aria-busy={busy}
          aria-label="自动托管"
          aria-describedby={`${modeId}-description`}
          title={managed ? "托管：按规划自动调度意图" : "手工：停止 Planner 自动规划，由用户或主 Agent 下发"}
          className="relative grid h-8 w-28 grid-cols-2 items-center rounded-[5px] border bg-muted text-xs text-muted-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50"
        >
          <span aria-hidden="true">手工</span>
          <span aria-hidden="true">托管</span>
          <SwitchPrimitive.Thumb
            aria-hidden="true"
            className="pointer-events-none absolute inset-y-0 left-0 flex w-1/2 items-center justify-center rounded-[5px] bg-primary font-medium text-primary-foreground transition-transform duration-200 ease-out data-[state=checked]:translate-x-full motion-reduce:transition-none"
          >
            {managed ? "托管" : "手工"}
          </SwitchPrimitive.Thumb>
        </SwitchPrimitive.Root>
        <span id={`${modeId}-description`} className="sr-only">
          开启为托管，关闭为手工。手工模式停止 Planner 自动规划，由用户或主 Agent 下发意图；运行中的 Worker 继续执行。
        </span>
      </div>
      {!["done", "failed", "timeout"].includes(task.status) && (
        <Button size="sm" variant="outline" disabled={busy} onClick={() => setConfirm(true)}>
          结束任务
        </Button>
      )}
      <AlertDialog
        open={confirm}
        onOpenChange={(value) => {
          if (!busy) setConfirm(value);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>结束任务？</AlertDialogTitle>
            <AlertDialogDescription>
              立即停止 Planner 和正在运行的
              Worker，不再领取新意图。会话、意图和已有产出均保留，未达成目标不会被标记为达成。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>返回</AlertDialogCancel>
            <Button disabled={busy} onClick={() => void finish()}>
              {busy ? "结束中…" : "确认结束"}
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
