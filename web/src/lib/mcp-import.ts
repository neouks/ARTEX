type Transport = "stdio" | "http" | "sse";

export type MCPImportItem = {
  name: string;
  transport: Transport;
  command?: string;
  args: string[];
  env: Record<string, string>;
  url?: string;
  enabled: boolean;
  insecure?: boolean;
};



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
      type === "sse" ? "sse" : type === "http" || type === "streamable-http" || (!type && item.url) ? "http" : "stdio";
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
    if (transport !== "stdio" && !url) throw new Error(`servers[${index}] 的 http 配置缺少 url`);
    return {
      name,
      transport,
      command: transport === "stdio" ? command : "",
      args: transport === "stdio" ? args : [],
      env,
      url: transport !== "stdio" ? url : "",
      enabled: typeof item.enabled === "boolean" ? item.enabled : true,
      insecure: transport !== "stdio" && item.insecure === true,
    };
  });
  return { servers };
}
