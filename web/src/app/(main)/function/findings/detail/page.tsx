"use client";
import * as React from "react";

import Link from "next/link";
import { useSearchParams } from "next/navigation";

import { ArrowLeftIcon } from "lucide-react";

import { FindingDetailContent } from "@/components/finding-detail-content";
import { Button } from "@/components/ui/button";
import { SidebarTrigger } from "@/components/ui/sidebar";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type { Finding } from "@/lib/types";

import { FindingLineageView } from "./lineage";

function FindingDetailInner({ id, contextTask }: { id: string; contextTask?: string }) {
  const [finding, setFinding] = React.useState<Finding | null>(null);
  const [error, setError] = React.useState("");
  const [revision, refresh] = React.useReducer((n: number) => n + 1, 0);
  const reload = React.useCallback(() => refresh(), []);
  React.useEffect(() => {
    void revision; // Retry or reload after a successful mutation.
    let active = true;
    if (!id) {
      setError("缺少漏洞 ID");
      return;
    }
    api
      .getFinding(id, contextTask)
      .then((value) => {
        if (active) {
          setFinding(value);
          setError("");
        }
      })
      .catch((e: Error) => {
        if (active) setError(e.message);
      });
    return () => {
      active = false;
    };
  }, [id, contextTask, revision]);
  return (
    <Tabs defaultValue="overview" className="flex flex-1 flex-col">
      <header className="flex flex-wrap items-center gap-3 border-b px-4 py-3">
        <SidebarTrigger />
        <Button variant="outline" size="sm" asChild>
          <Link href="/function/findings">
            <ArrowLeftIcon />
            返回发现
          </Link>
        </Button>
        <h1 className="min-w-0 flex-1 break-words font-semibold">
          {finding?.name || finding?.vulnclass || "漏洞详情"}
        </h1>
        <TabsList>
          <TabsTrigger value="overview">概览</TabsTrigger>
          <TabsTrigger value="lineage" disabled={!finding}>
            链路图
          </TabsTrigger>
        </TabsList>
      </header>
      <div className="min-w-0 p-4 lg:p-6">
        {error ? (
          <div role="alert" className="flex items-center gap-3">
            加载失败：{error}
            <Button onClick={reload}>重试</Button>
          </div>
        ) : !finding ? (
          <p role="status">加载中…</p>
        ) : (
          <>
            <TabsContent value="overview">
              <FindingDetailContent finding={finding} contextTask={contextTask} onRefresh={reload} />
            </TabsContent>
            <TabsContent value="lineage">
              <FindingLineageView findingId={id} />
            </TabsContent>
          </>
        )}
      </div>
    </Tabs>
  );
}
function FindingDetailRoute() {
  const params = useSearchParams();
  const id = params.get("id") ?? "";
  const contextTask = params.get("context_task") || undefined;
  return <FindingDetailInner key={`${id}:${contextTask ?? ""}`} id={id} contextTask={contextTask} />;
}
export default function FindingDetailPage() {
  return (
    <React.Suspense fallback={<p className="p-6">加载中…</p>}>
      <FindingDetailRoute />
    </React.Suspense>
  );
}
