"use client";

import * as React from "react";

import { toast } from "sonner";

import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import type { Finding } from "@/lib/types";

export function FindingDeleteDialog({
  finding,
  disabled,
  onDelete,
}: {
  finding: Finding;
  disabled?: boolean;
  onDelete: (finding: Finding, reason: string) => Promise<void>;
}) {
  const [open, setOpen] = React.useState(false);
  const [reason, setReason] = React.useState("");
  const [error, setError] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const submitting = React.useRef(false);
  const inputId = React.useId();
  async function submit() {
    if (submitting.current) return;
    submitting.current = true;
    setBusy(true);
    setError("");
    try {
      await onDelete(finding, reason.trim());
      setOpen(false);
      setReason("");
      toast.success("已删除漏洞");
    } catch (e) {
      setError(e instanceof Error ? e.message : "删除失败，请重试");
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  }
  return (
    <AlertDialog
      open={open}
      onOpenChange={(value) => {
        if (submitting.current) return;
        setOpen(value);
        if (!value) {
          setReason("");
          setError("");
        }
      }}
    >
      <AlertDialogTrigger asChild>
        <Button variant="destructive" size="sm" disabled={busy || disabled === true}>
          删除
        </Button>
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>删除此漏洞？</AlertDialogTitle>
          <AlertDialogDescription>
            将删除“{[finding.name, finding.vulnclass, "此漏洞"].find(Boolean)}
            ”，删除后无法恢复。填写的原因将提供给所属任务的 Planner，用于后续规划。
          </AlertDialogDescription>
        </AlertDialogHeader>
        <FieldGroup>
          <Field data-disabled={busy}>
            <FieldLabel htmlFor={inputId}>删除原因（可选）</FieldLabel>
            <Textarea
              id={inputId}
              value={reason}
              disabled={busy}
              onChange={(e) => setReason(Array.from(e.target.value).slice(0, 2000).join(""))}
              placeholder="例如：现有证据不足以证明存在越权。"
            />
            <FieldDescription>{Array.from(reason).length} / 2000 字符；留空也可删除。</FieldDescription>
          </Field>
        </FieldGroup>
        {error && (
          <p role="alert" className="text-destructive">
            删除失败：{error}
          </p>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>取消</AlertDialogCancel>
          <Button variant="destructive" disabled={busy} onClick={() => void submit()}>
            {busy && <Spinner data-icon="inline-start" />}确认删除
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
