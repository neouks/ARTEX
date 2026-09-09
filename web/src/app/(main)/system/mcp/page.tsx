"use client";

import * as React from "react";

import {
  CheckCircle2Icon,
  FileJsonIcon,
  PlusIcon,
  RefreshCwIcon,
  ServerIcon,
  Trash2Icon,
  XCircleIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Separator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { Agent, MCPCall, MCPServer, MCPTestResult, MCPTool, MCPUsageStat } from "@/lib/types";

type Transport = "stdio" | "http";
type FormState = { name: string; transport: Transport; command: string; args: string; url: string; env: string };

export type MCPImportItem = {
  name: string;
  transport: Transport;
  command?: string;
  args: string[];
  env: Record<string, string>;
  url?: string;
  enabled: boolean;
};

const emptyForm: FormState = { name: "", transport: "stdio", command: "", args: "", url: "", env: "" };

function record(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

/** Normalize common MCP client JSON formats into the API's stable servers[] shape. */
export function normalizeMCPImportConfig(value: unknown): { servers: MCPImportItem[] } {
  const root = record(value);
  if (!root) throw new Error("JSON 根必须是对象");
  const source = root.mcpServers ?? root.servers;
  const sourceRecord = record(source);
  let entries: Array<[string, unknown]> = [];
  if (Array.isArray(source)) entries = source.map((item) => ["", item]);
  else if (sourceRecord) entries = Object.entries(sourceRecord);
  else if (source === undefined) entries = [[String(root.name ?? ""), root]];
  if (!entries.length) throw new Error("未找到 MCP 服务器配置");
  const servers = entries.map(([mapName, raw], index) => {
    const item = record(raw);
    if (!item) throw new Error(`servers[${index}] 必须是对象`);
    const name = String(item.name ?? mapName ?? "").trim();
    if (!name) throw new Error(`servers[${index}] 缺少 name`);
    const type = String(item.transport ?? item.type ?? "")
      .trim()
      .toLowerCase();
    const transport: Transport =
      type === "http" || type === "sse" || type === "streamable-http" || (!type && item.url) ? "http" : "stdio";
    if (item.command !== undefined && item.command !== null && typeof item.command !== "string")
      throw new Error(`servers[${index}].command 必须是字符串`);
    const command = item.command === undefined || item.command === null ? "" : item.command.trim();
    const rawArgs = item.args;
    let args: string[] = [];
    if (Array.isArray(rawArgs)) {
      if (!rawArgs.every((arg) => typeof arg === "string")) throw new Error(`servers[${index}].args 必须是字符串数组`);
      args = rawArgs.filter((arg) => arg.length > 0);
    } else if (typeof rawArgs === "string") {
      args = rawArgs.trim() ? rawArgs.trim().split(/\s+/) : [];
    } else if (rawArgs !== undefined && rawArgs !== null) {
      throw new Error(`servers[${index}].args 必须是字符串数组`);
    }
    const env: Record<string, string> = {};
    const rawEnv = item.env;
    if (rawEnv !== undefined && rawEnv !== null) {
      const envRecord = record(rawEnv);
      if (!envRecord || Object.entries(envRecord).some(([, envValue]) => typeof envValue !== "string"))
        throw new Error(`servers[${index}].env 必须是字符串键值对象`);
      for (const [key, envValue] of Object.entries(envRecord)) {
        if (!key.trim()) throw new Error(`servers[${index}].env 不能包含空键`);
        env[key] = envValue as string;
      }
    }
    if (item.url !== undefined && item.url !== null && typeof item.url !== "string")
      throw new Error(`servers[${index}].url 必须是字符串`);
    const url = item.url === undefined || item.url === null ? "" : item.url.trim();
    if (transport === "stdio" && !command) throw new Error(`servers[${index}] 的 stdio 配置缺少 command`);
    if (transport === "http" && !url) throw new Error(`servers[${index}] 的 http 配置缺少 url`);
    return {
      name,
      transport,
      command: transport === "stdio" ? command : "",
      args: transport === "stdio" ? args : [],
      env,
      url: transport === "http" ? url : "",
      enabled: typeof item.enabled === "boolean" ? item.enabled : true,
    };
  });
  return { servers };
}

export default function MCPPage() {
  const [servers, setServers] = React.useState<MCPServer[]>([]);
  const [agents, setAgents] = React.useState<Agent[]>([]);
  const [visibility, setVisibility] = React.useState<Record<number, string[]>>({});
  const [open, setOpen] = React.useState(false);
  const [editing, setEditing] = React.useState<MCPServer | null>(null);
  const [tab, setTab] = React.useState<"config" | "tools">("config");
  const [form, setForm] = React.useState<FormState>(emptyForm);
  const [saving, setSaving] = React.useState(false);
  const [tools, setTools] = React.useState<MCPTool[]>([]);
  const [toolsLoading, setToolsLoading] = React.useState(false);
  const [refreshing, setRefreshing] = React.useState(false);
  const [testing, setTesting] = React.useState(false);
  const [testResult, setTestResult] = React.useState<MCPTestResult | null>(null);
  const [importOpen, setImportOpen] = React.useState(false);
  const [importText, setImportText] = React.useState("");
  const [importPreview, setImportPreview] = React.useState<MCPImportItem[] | null>(null);
  const [importError, setImportError] = React.useState("");
  const [importing, setImporting] = React.useState(false);
  const [usageStats, setUsageStats] = React.useState<MCPUsageStat[]>([]);
  const [recentCalls, setRecentCalls] = React.useState<MCPCall[]>([]);
  const [usageLoading, setUsageLoading] = React.useState(false);

  const load = React.useCallback(() => {
    api
      .agents()
      .then(setAgents)
      .catch(() => undefined);
    api
      .mcpServers()
      .then((ss) => {
        setServers(ss);
        ss.forEach((s) => {
          void api
            .resourceVisibility("mcp", s.id)
            .then((ids) => setVisibility((v) => ({ ...v, [s.id]: ids })))
            .catch(() => undefined);
        });
      })
      .catch(() => undefined);
  }, []);
  React.useEffect(() => load(), [load]);

  function setF(patch: Partial<FormState>) {
    setForm((current) => ({ ...current, ...patch }));
    setTestResult(null);
  }
  function parseEnv(text: string): Record<string, string> {
    const out: Record<string, string> = {};
    for (const line of text.split("\n")) {
      const trimmed = line.trim();
      if (!trimmed) continue;
      const idx = trimmed.indexOf("=");
      if (idx > 0) out[trimmed.slice(0, idx)] = trimmed.slice(idx + 1);
    }
    return out;
  }
  function envToText(env: Record<string, string> | undefined): string {
    return Object.entries(env ?? {})
      .map(([key, value]) => `${key}=${value}`)
      .join("\n");
  }
  function formPayload(): Partial<MCPServer> {
    return form.transport === "http"
      ? {
          name: form.name.trim(),
          transport: "http",
          url: form.url.trim(),
          command: "",
          args: [],
          env: parseEnv(form.env),
          enabled: editing?.enabled ?? true,
        }
      : {
          name: form.name.trim(),
          transport: "stdio",
          command: form.command.trim(),
          args: form.args.trim() ? form.args.trim().split(/\s+/) : [],
          env: parseEnv(form.env),
          url: "",
          enabled: editing?.enabled ?? true,
        };
  }
  function validateForm() {
    if (!form.name.trim()) return "请填写名称";
    if (form.transport === "stdio" && !form.command.trim()) return "请填写命令";
    if (form.transport === "http" && !form.url.trim()) return "请填写远程 URL";
    return "";
  }
  function openAdd() {
    setEditing(null);
    setForm(emptyForm);
    setTools([]);
    setTab("config");
    setTestResult(null);
    setUsageStats([]);
    setRecentCalls([]);
    setOpen(true);
  }
  function openEdit(s: MCPServer) {
    setEditing(s);
    setForm({
      name: s.name,
      transport: s.transport,
      command: s.command ?? "",
      args: s.args.join(" "),
      url: s.url ?? "",
      env: envToText(s.env),
    });
    setTab("config");
    setTestResult(null);
    setUsageStats([]);
    setRecentCalls([]);
    setOpen(true);
    void loadTools(s.id);
    void loadUsage(s.id);
  }
  async function loadTools(id: number) {
    setToolsLoading(true);
    try {
      setTools(await api.mcpTools(id));
    } catch {
      setTools([]);
    } finally {
      setToolsLoading(false);
    }
  }
  async function loadUsage(id: number) {
    setUsageLoading(true);
    try {
      const result = await api.mcpUsage(id, 50);
      setUsageStats(result.stats ?? []);
      setRecentCalls(result.calls ?? []);
    } catch {
      setUsageStats([]);
      setRecentCalls([]);
    } finally {
      setUsageLoading(false);
    }
  }
  async function testForm() {
    const error = validateForm();
    if (error) {
      setTestResult({ ok: false, error });
      return;
    }
    setTesting(true);
    setTestResult(null);
    try {
      setTestResult(await api.testMcpServer(formPayload()));
    } catch (error) {
      setTestResult({ ok: false, error: (error as Error).message });
    } finally {
      setTesting(false);
    }
  }
  async function saveForm() {
    const error = validateForm();
    if (error) {
      toast.error(error);
      return;
    }
    setSaving(true);
    try {
      await api.saveMcpServer({ ...(editing ? { id: editing.id } : {}), ...formPayload() });
      toast.success(editing ? "已保存" : "已添加 MCP 服务器");
      if (!editing) setOpen(false);
      load();
    } catch (error) {
      toast.error(`保存失败：${(error as Error).message}`);
    } finally {
      setSaving(false);
    }
  }
  async function refreshTools() {
    if (!editing) return;
    setRefreshing(true);
    try {
      const next = await api.refreshMcpServer(editing.id);
      setTools(next);
      toast.success(`发现 ${next.length} 个工具`);
      load();
      void loadUsage(editing.id);
    } catch (error) {
      toast.error(`刷新失败：${(error as Error).message}`);
    } finally {
      setRefreshing(false);
    }
  }
  async function removeServer(s: MCPServer) {
    try {
      await api.deleteMcpServer(s.id);
      toast.success(`已删除：${s.name}`);
      setOpen(false);
      load();
    } catch (error) {
      toast.error(`删除失败：${(error as Error).message}`);
    }
  }
  async function toggleEnabled(s: MCPServer) {
    try {
      await api.saveMcpServer({ ...s, enabled: !s.enabled });
      load();
    } catch (error) {
      toast.error(`操作失败：${(error as Error).message}`);
    }
  }
  async function toggleVisibility(serverId: number, agentId: string, agentName: string) {
    const on = (visibility[serverId] ?? []).includes(agentId);
    try {
      await api.toggleVisibility(agentId, "mcp", serverId, !on);
      toast.success(`${on ? "取消" : "授予"}「${agentName}」可见`);
      load();
    } catch (error) {
      toast.error(`操作失败：${(error as Error).message}`);
    }
  }
  function parseImport() {
    setImportError("");
    setImportPreview(null);
    try {
      setImportPreview(normalizeMCPImportConfig(JSON.parse(importText)).servers);
    } catch (error) {
      setImportError((error as Error).message);
    }
  }
  async function submitImport() {
    if (!importPreview) return;
    setImporting(true);
    try {
      const result = await api.importMcpServers(importPreview);
      toast.success(`已导入 ${result.results.length} 个 MCP`);
      setImportOpen(false);
      setImportPreview(null);
      setImportText("");
      load();
      window.setTimeout(load, 1500);
      window.setTimeout(load, 5000);
    } catch (error) {
      setImportError((error as Error).message);
    } finally {
      setImporting(false);
    }
  }

  function renderForm() {
    return (
      <FieldGroup className="py-4">
        <Field>
          <FieldLabel>传输方式</FieldLabel>
          <div className="flex gap-2">
            <Button
              type="button"
              variant={form.transport === "stdio" ? "default" : "outline"}
              onClick={() => setF({ transport: "stdio" })}
            >
              stdio（本地）
            </Button>
            <Button
              type="button"
              variant={form.transport === "http" ? "default" : "outline"}
              onClick={() => setF({ transport: "http" })}
            >
              http（远程）
            </Button>
          </div>
        </Field>
        <Field>
          <FieldLabel htmlFor="m-name">名称</FieldLabel>
          <Input
            id="m-name"
            placeholder="filesystem"
            value={form.name}
            onChange={(e) => setF({ name: e.target.value })}
          />
        </Field>
        {form.transport === "stdio" ? (
          <>
            <Field>
              <FieldLabel htmlFor="m-cmd">命令</FieldLabel>
              <Input
                id="m-cmd"
                className="font-mono"
                placeholder="npx"
                value={form.command}
                onChange={(e) => setF({ command: e.target.value })}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="m-args">参数（空格分隔）</FieldLabel>
              <Input
                id="m-args"
                className="font-mono"
                placeholder="-y @modelcontextprotocol/server-filesystem /data"
                value={form.args}
                onChange={(e) => setF({ args: e.target.value })}
              />
              <FieldDescription>JSON 导入会保留参数数组；手工输入按空格拆分。</FieldDescription>
            </Field>
          </>
        ) : (
          <Field>
            <FieldLabel htmlFor="m-url">远程 URL</FieldLabel>
            <Input
              id="m-url"
              className="font-mono"
              placeholder="https://mcp.example.com/mcp"
              value={form.url}
              onChange={(e) => setF({ url: e.target.value })}
            />
          </Field>
        )}
        <Field>
          <FieldLabel htmlFor="m-env">
            {form.transport === "http" ? "请求头（每行 KEY=VALUE）" : "环境变量（每行 KEY=VALUE）"}
          </FieldLabel>
          <Textarea
            id="m-env"
            className="font-mono"
            placeholder={form.transport === "http" ? "Authorization=Bearer xxxx" : "API_KEY=xxxx\nFOO=bar"}
            value={form.env}
            onChange={(e) => setF({ env: e.target.value })}
          />
        </Field>
        {testResult && (
          <Alert variant={testResult.ok ? "default" : "destructive"}>
            {testResult.ok ? <CheckCircle2Icon /> : <XCircleIcon />}
            <AlertTitle>{testResult.ok ? "连接可用" : "检测失败"}</AlertTitle>
            <AlertDescription>
              {testResult.ok ? (
                <div className="grid gap-1">
                  <span>
                    耗时 {testResult.latency_ms ?? 0} ms · 发现 {testResult.tool_count ?? 0} 个工具
                  </span>
                  <span className="break-words">
                    {(testResult.tools ?? []).map((tool) => tool.name).join("、") || "未发现工具"}
                  </span>
                </div>
              ) : (
                testResult.error
              )}
            </AlertDescription>
          </Alert>
        )}
        <div className="flex flex-wrap gap-2 pt-1">
          <Button type="button" variant="outline" onClick={testForm} disabled={testing}>
            {testing && <Spinner data-icon="inline-start" />}测试可用性
          </Button>
          <Button type="button" onClick={saveForm} disabled={saving}>
            {saving && <Spinner data-icon="inline-start" />}
            {editing ? "保存" : "添加"}
          </Button>
        </div>
      </FieldGroup>
    );
  }
  function renderTools() {
    let body: React.ReactNode;
    if (toolsLoading) body = <p className="text-muted-foreground text-sm">加载中…</p>;
    else if (tools.length === 0)
      body = <p className="text-muted-foreground text-sm">尚未发现工具，点击刷新重新获取。</p>;
    else
      body = (
        <div className="flex flex-col divide-y">
          {tools.map((tool) => (
            <div key={tool.name} className="py-2.5">
              <div className="flex items-center justify-between gap-2">
                <code className="font-mono text-sm">{tool.name}</code>
                <Badge variant="secondary">调用 {tool.calls ?? 0} 次</Badge>
              </div>
              {tool.description && (
                <p className="mt-0.5 text-muted-foreground text-xs leading-relaxed">{tool.description}</p>
              )}
            </div>
          ))}
        </div>
      );
    return (
      <div className="flex flex-col gap-3 py-4">
        <div className="flex items-center justify-between">
          <span className="text-muted-foreground text-sm">{tools.length} 个工具</span>
          <Button size="sm" variant="outline" disabled={refreshing} onClick={refreshTools}>
            {refreshing ? <Spinner data-icon="inline-start" /> : <RefreshCwIcon data-icon="inline-start" />}刷新
          </Button>
        </div>
        {body}
      </div>
    );
  }

  function renderUsage() {
    if (usageLoading) return <p className="text-muted-foreground text-sm">加载调用统计中…</p>;
    if (usageStats.length === 0 && recentCalls.length === 0)
      return <p className="text-muted-foreground text-sm">暂无调用记录。工具调用后会在此显示统计与最近明细。</p>;
    return (
      <div className="flex flex-col gap-4">
        <div className="grid gap-2">
          <h3 className="font-medium text-sm">工具统计</h3>
          <div className="flex flex-col divide-y rounded-md border">
            {usageStats.map((stat) => (
              <div key={stat.tool_name} className="flex flex-wrap items-center gap-2 px-3 py-2 text-sm">
                <code className="min-w-0 flex-1 truncate font-mono">{stat.tool_name}</code>
                <span className="text-muted-foreground text-xs">
                  {stat.calls} 次 · {stat.tasks} 个任务
                </span>
                {stat.agents.length > 0 && <Badge variant="outline">{stat.agents.join("、")}</Badge>}
                {stat.last_used && (
                  <time className="text-muted-foreground text-xs">{new Date(stat.last_used).toLocaleString()}</time>
                )}
              </div>
            ))}
          </div>
        </div>
        <Separator />
        <div className="grid gap-2">
          <h3 className="font-medium text-sm">最近调用</h3>
          <div className="flex flex-col divide-y rounded-md border">
            {recentCalls.map((call) => (
              <div
                key={`${call.ts}-${call.tool_name}-${call.task_id}-${call.session_id}`}
                className="grid gap-1 px-3 py-2 text-xs"
              >
                <div className="flex flex-wrap items-center gap-2">
                  <time className="text-muted-foreground">{new Date(call.ts).toLocaleString()}</time>
                  <code className="font-mono">{call.tool_name}</code>
                  <Badge variant="outline">{call.agent_key || "未知 Agent"}</Badge>
                </div>
                <span className="text-muted-foreground">
                  任务 {call.task_id || "—"} · 会话 {call.session_id || "—"}
                </span>
              </div>
            ))}
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="font-semibold text-xl tracking-tight">MCP</h1>
          <p className="text-muted-foreground text-sm">外部 MCP 工具服务器 · 按 Agent 授权可见</p>
        </div>
        <Button
          variant="outline"
          onClick={() => {
            setImportOpen(true);
            setImportError("");
          }}
        >
          <FileJsonIcon data-icon="inline-start" />
          导入 JSON
        </Button>
      </div>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <button
          type="button"
          onClick={openAdd}
          className="flex min-h-[116px] flex-col items-center justify-center gap-2 rounded-xl border border-foreground/70 border-dashed text-foreground/70 transition hover:bg-muted/60 hover:shadow-sm"
        >
          <PlusIcon className="size-6" />
          <span className="text-sm">添加 MCP</span>
        </button>
        {servers.map((s) => (
          <Card
            key={s.id}
            onClick={(event) => {
              const target = event.target as HTMLElement;
              if (target.closest("button,[role=checkbox],[role=switch]")) return;
              openEdit(s);
            }}
            className="cursor-pointer gap-3 transition hover:border-primary/60 hover:shadow-sm"
          >
            <CardHeader>
              <div className="flex items-center gap-2">
                <ServerIcon className="size-4 shrink-0 text-muted-foreground" />
                <CardTitle className="truncate text-base">{s.name}</CardTitle>
                <Badge variant="outline" className="uppercase">
                  {s.transport}
                </Badge>
                <div className="ml-auto flex items-center gap-2">
                  <Switch checked={s.enabled} onCheckedChange={() => toggleEnabled(s)} aria-label="启用" />
                  <Button size="icon" variant="outline" aria-label="删除" onClick={() => removeServer(s)}>
                    <Trash2Icon className="text-destructive" />
                  </Button>
                </div>
              </div>
            </CardHeader>
            <CardContent className="grid gap-3">
              <p className="text-muted-foreground text-sm">
                {s.tools && s.tools.length > 0 ? `${s.tools.length} 个工具` : "尚未发现工具"}
              </p>
              <div className="flex flex-wrap items-center gap-2 text-xs">
                <Badge variant="secondary">累计调用 {s.calls ?? 0} 次</Badge>
                <Badge variant="outline">覆盖任务 {s.tasks ?? 0}</Badge>
                {s.last_used && (
                  <span className="text-muted-foreground">最近：{new Date(s.last_used).toLocaleString()}</span>
                )}
              </div>
              <div className="grid gap-2">
                <span className="text-muted-foreground text-xs">可见性（按 Agent 授权）</span>
                <div className="flex flex-wrap gap-x-4 gap-y-2">
                  {agents.map((agent) => (
                    <label
                      key={agent.key}
                      htmlFor={`mcp-agent-${s.id}-${agent.id}`}
                      className="flex items-center gap-2 text-sm"
                    >
                      <Checkbox
                        id={`mcp-agent-${s.id}-${agent.id}`}
                        checked={(visibility[s.id] ?? []).includes(agent.id)}
                        onCheckedChange={() => toggleVisibility(s.id, agent.id, agent.name)}
                      />
                      {agent.name}
                    </label>
                  ))}
                </div>
              </div>
            </CardContent>
          </Card>
        ))}
      </div>
      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent side="right" className="w-full data-[side=right]:sm:max-w-lg">
          <SheetHeader>
            <SheetTitle>{editing ? editing.name : "添加 MCP 服务器"}</SheetTitle>
            <SheetDescription>stdio（本地起进程）或 http（远程 Streamable HTTP）</SheetDescription>
          </SheetHeader>
          {editing ? (
            <Tabs
              value={tab}
              onValueChange={(value) => setTab(value as "config" | "tools")}
              className="flex min-h-0 flex-1 flex-col px-4"
            >
              <TabsList>
                <TabsTrigger value="config">配置</TabsTrigger>
                <TabsTrigger value="tools">工具列表{tools.length ? `（${tools.length}）` : ""}</TabsTrigger>
              </TabsList>
              <TabsContent value="config" className="min-h-0 flex-1 overflow-y-auto">
                {renderForm()}
              </TabsContent>
              <TabsContent value="tools" className="min-h-0 flex-1 overflow-y-auto">
                {renderTools()}
                <Separator />
                <div className="py-4">
                  <h3 className="mb-3 font-medium text-sm">调用统计</h3>
                  {renderUsage()}
                </div>
              </TabsContent>
            </Tabs>
          ) : (
            <div className="flex min-h-0 flex-1 flex-col overflow-y-auto px-4">{renderForm()}</div>
          )}
        </SheetContent>
      </Sheet>
      <Dialog open={importOpen} onOpenChange={setImportOpen}>
        <DialogContent className="flex max-h-[90dvh] flex-col sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>导入 MCP JSON</DialogTitle>
            <DialogDescription>支持 mcpServers、servers 对象或单个服务器配置。解析后确认才会写入。</DialogDescription>
          </DialogHeader>
          <div className="min-h-0 overflow-y-auto">
            <FieldGroup>
              {!importPreview ? (
                <Field>
                  <FieldLabel htmlFor="mcp-import">配置 JSON</FieldLabel>
                  <Textarea
                    id="mcp-import"
                    className="min-h-48 font-mono text-xs"
                    value={importText}
                    onChange={(e) => {
                      setImportText(e.target.value);
                      setImportError("");
                    }}
                    placeholder={'{"mcpServers":{"playwright":{"command":"npx","args":["@playwright/mcp"]}}}'}
                  />
                  <FieldDescription>stdio 支持 command/args/env；远程服务使用 url，可选 enabled。</FieldDescription>
                </Field>
              ) : (
                <Field>
                  <FieldLabel>标准化预览</FieldLabel>
                  <div className="flex flex-col gap-3">
                    {importPreview.map((item) => {
                      const existing = servers.find((server) => server.name === item.name);
                      const visibleAgents = existing
                        ? (visibility[existing.id] ?? [])
                            .map((id) => agents.find((agent) => agent.id === id)?.name ?? id)
                            .filter(Boolean)
                        : [];
                      return (
                        <div
                          key={`${item.name}-${item.transport}`}
                          className="grid gap-2 rounded-lg border p-3 text-sm"
                        >
                          <div className="flex flex-wrap items-center gap-2">
                            <strong className="break-all">{item.name}</strong>
                            <Badge variant="outline">{item.transport}</Badge>
                            <Badge variant={item.enabled ? "default" : "secondary"}>
                              {item.enabled ? "启用" : "禁用"}
                            </Badge>
                            {existing && <Badge variant="destructive">覆盖现有配置</Badge>}
                          </div>
                          <code className="break-all text-xs">
                            {item.transport === "stdio" ? [item.command, ...item.args].join(" ") : item.url}
                          </code>
                          <span className="text-muted-foreground text-xs">
                            Agent 可见性：{visibleAgents.length ? visibleAgents.join("、") : "未授权"}
                            （不会修改现有授权）
                          </span>
                        </div>
                      );
                    })}
                  </div>
                </Field>
              )}
              {importError && (
                <Alert variant="destructive">
                  <XCircleIcon />
                  <AlertTitle>无法导入</AlertTitle>
                  <AlertDescription>{importError}</AlertDescription>
                </Alert>
              )}
            </FieldGroup>
          </div>
          <Separator />
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => {
                setImportOpen(false);
                setImportPreview(null);
              }}
            >
              {importPreview ? "取消" : "关闭"}
            </Button>
            {importPreview ? (
              <>
                <Button type="button" variant="outline" onClick={() => setImportPreview(null)}>
                  返回修改
                </Button>
                <Button type="button" onClick={submitImport} disabled={importing}>
                  {importing && <Spinner data-icon="inline-start" />}确认导入
                </Button>
              </>
            ) : (
              <Button type="button" onClick={parseImport} disabled={!importText.trim()}>
                解析预览
              </Button>
            )}
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
